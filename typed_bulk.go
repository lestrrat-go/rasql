package rasql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/lestrrat-go/rasql/dberror"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
)

type BulkPlan[T any] struct {
	plans []CreatePlan[T]
	table string
}

type BulkOptions struct {
	MaxRows           int
	MaxBindParameters int
	Atomic            bool
	Classifier        BatchFailureClassifier
}

type FailureCertainty uint8

const (
	OutcomeUnknown FailureCertainty = iota
	OutcomeRejected
)

type BatchFailureClassifier interface {
	Certainty(error) FailureCertainty
}

type InputRange struct {
	First int
	Last  int
}

type FailedBatch struct {
	Indexes   []int
	Certainty FailureCertainty
	Err       error
}

type BulkOutcome struct {
	Completed []InputRange
	Failed    *FailedBatch
	Durable   bool
}

type ConstraintFailureClassifier struct {
	Classifiers []dberror.Classifier
}

func (c ConstraintFailureClassifier) Certainty(err error) FailureCertainty {
	metadata, ok := dberror.Classify(err, c.Classifiers...)
	if !ok {
		return OutcomeUnknown
	}
	switch metadata.Category {
	case dberror.UniqueViolation, dberror.ForeignKeyViolation, dberror.NotNullViolation, dberror.CheckViolation:
		return OutcomeRejected
	default:
		return OutcomeUnknown
	}
}

func NewBulkPlan[T any](plans ...CreatePlan[T]) (BulkPlan[T], error) {
	if len(plans) == 0 {
		return BulkPlan[T]{}, fmt.Errorf("rasql: bulk plan requires at least one create plan")
	}
	copyPlans := make([]CreatePlan[T], len(plans))
	var table string
	for i, plan := range plans {
		if plan.err != nil {
			return BulkPlan[T]{}, plan.err
		}
		if isNilTable(plan.table) {
			return BulkPlan[T]{}, fmt.Errorf("rasql: bulk plan input %d has no table", i)
		}
		identity := plan.table.Ref().Definition().QualifiedName()
		if i == 0 {
			table = identity
		} else if identity != table {
			return BulkPlan[T]{}, fmt.Errorf("rasql: bulk plans target mixed tables %q and %q", table, identity)
		}
		copyPlans[i] = CreatePlan[T]{table: plan.table, fields: append([]MutationField[T](nil), plan.fields...)}
	}
	return BulkPlan[T]{plans: copyPlans, table: table}, nil
}

func ExecBulkCreate[T any](ctx context.Context, db DB, bulk BulkPlan[T], options BulkOptions) (BulkOutcome, error) {
	if len(bulk.plans) == 0 {
		return BulkOutcome{}, fmt.Errorf("rasql: bulk plan requires at least one create plan")
	}
	if err := db.Validate(); err != nil {
		return BulkOutcome{}, err
	}
	maxRows, maxBinds, err := bulkLimits(db, options)
	if err != nil {
		return BulkOutcome{}, err
	}
	groups, err := bulkBatches(bulk.plans, maxRows, maxBinds)
	if err != nil {
		return BulkOutcome{}, err
	}
	if options.Atomic {
		if transaction, ok := db.Handle().(*sql.Tx); ok && transaction != nil {
			var outcome BulkOutcome
			var callbackMarker *bulkCallbackError
			callbackErr := db.Atomic(ctx, nil, func(callbackCtx context.Context, scoped DB) error {
				var err error
				outcome, err = executeBulkBatches(callbackCtx, scoped, groups, options.Classifier)
				if err != nil {
					callbackMarker = &bulkCallbackError{err: err}
					return callbackMarker
				}
				return nil
			})
			if callbackErr != nil {
				if outcome.Failed == nil {
					outcome.Failed = &FailedBatch{Indexes: bulkAttemptedIndexes(outcome), Certainty: OutcomeUnknown, Err: callbackErr}
				} else {
					cleanupFailed := bulkHasCleanupError(callbackErr, callbackMarker)
					if cleanupFailed {
						outcome.Failed.Indexes = bulkAttemptedIndexes(outcome)
						outcome.Failed.Certainty = OutcomeUnknown
					}
					outcome.Failed.Err = callbackErr
				}
				outcome.Completed = nil
				outcome.Durable = false
				return outcome, callbackErr
			}
			outcome.Durable = false
			return outcome, nil
		}
		transaction, err := db.Begin(ctx, nil)
		if err != nil {
			return BulkOutcome{}, err
		}
		outcome, executionErr := executeBulkBatches(ctx, transaction, groups, options.Classifier)
		if executionErr != nil {
			rollbackErr := transaction.Rollback()
			attempted := bulkAttemptedIndexes(outcome)
			outcome.Completed = nil
			if rollbackErr != nil {
				outcome.Failed = &FailedBatch{Indexes: attempted, Certainty: OutcomeUnknown, Err: errors.Join(executionErr, rollbackErr)}
				return outcome, outcome.Failed.Err
			}
			outcome.Durable = false
			return outcome, executionErr
		}
		if err := transaction.Commit(); err != nil {
			outcome.Completed = nil
			outcome.Failed = &FailedBatch{Indexes: bulkIndexes(bulk.plans), Certainty: OutcomeUnknown, Err: err}
			return outcome, err
		}
		outcome.Durable = true
		return outcome, nil
	}
	return executeBulkBatches(ctx, db, groups, options.Classifier)
}

type bulkCallbackError struct{ err error }

func (e *bulkCallbackError) Error() string { return e.err.Error() }
func (e *bulkCallbackError) Unwrap() error { return e.err }

func bulkIndexes[T any](plans []CreatePlan[T]) []int {
	indexes := make([]int, len(plans))
	for i := range indexes {
		indexes[i] = i
	}
	return indexes
}

func bulkAttemptedIndexes(outcome BulkOutcome) []int {
	indexes := make([]int, 0)
	for _, completed := range outcome.Completed {
		for index := completed.First; index <= completed.Last; index++ {
			indexes = append(indexes, index)
		}
	}
	if outcome.Failed != nil {
		indexes = append(indexes, outcome.Failed.Indexes...)
	}
	return indexes
}

func bulkHasCleanupError(err error, marker *bulkCallbackError) bool {
	if err == nil || marker == nil {
		return false
	}
	if err == marker {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range joined.Unwrap() {
			if bulkHasCleanupError(child, marker) {
				return true
			}
		}
		return false
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return bulkHasCleanupError(wrapped.Unwrap(), marker)
	}
	return true
}

func executeBulkBatches[T any](ctx context.Context, db DB, groups []bulkBatch[T], classifier BatchFailureClassifier) (BulkOutcome, error) {
	var outcome BulkOutcome
	for _, group := range groups {
		statement, err := bulkStatement(group)
		if err != nil {
			return outcome, err
		}
		rendered, err := render.Write(db.Dialect(), statement)
		if err != nil {
			return outcome, err
		}
		if _, err = db.ExecRendered(ctx, rendered); err != nil {
			wrapped := fmt.Errorf("rasql: bulk create batch: %w", err)
			certainty := OutcomeUnknown
			if classifier != nil {
				certainty = classifier.Certainty(err)
				if certainty != OutcomeRejected {
					certainty = OutcomeUnknown
				}
			}
			indexes := append([]int(nil), group.indexes...)
			outcome.Failed = &FailedBatch{Indexes: indexes, Certainty: certainty, Err: wrapped}
			outcome.Durable = !bulkDBIsTransaction(db)
			return outcome, wrapped
		}
		outcome.Completed = appendBulkRange(outcome.Completed, group.indexes)
	}
	outcome.Durable = !bulkDBIsTransaction(db)
	return outcome, nil
}

func bulkDBIsTransaction(db DB) bool {
	transaction, ok := db.Handle().(*sql.Tx)
	return ok && transaction != nil
}

type bulkBatch[T any] struct {
	plans    []CreatePlan[T]
	indexes  []int
	columns  []query.ColumnRef
	rows     [][]any
	defaults bool
}

func bulkBatches[T any](plans []CreatePlan[T], maxRows, maxBinds int) ([]bulkBatch[T], error) {
	var result []bulkBatch[T]
	for index, plan := range plans {
		lowered, err := plan.lowerNormalized()
		if err != nil {
			return nil, err
		}
		binds := len(lowered.rawValues)
		if binds > maxBinds {
			return nil, fmt.Errorf("rasql: bulk plan input %d uses %d bind parameters, limit is %d", index, binds, maxBinds)
		}
		if lowered.defaultOnly {
			result = append(result, bulkBatch[T]{plans: []CreatePlan[T]{plan}, indexes: []int{index}, defaults: true})
			continue
		}
		if len(result) == 0 || result[len(result)-1].defaults || !sameColumns(result[len(result)-1].columns, lowered.columns) || len(result[len(result)-1].plans) == maxRows || (len(result[len(result)-1].rows)+1)*len(lowered.values) > maxBinds {
			result = append(result, bulkBatch[T]{columns: append([]query.ColumnRef(nil), lowered.columns...), indexes: []int{index}, plans: []CreatePlan[T]{plan}, rows: [][]any{append([]any(nil), lowered.rawValues...)}})
			continue
		}
		batch := &result[len(result)-1]
		batch.plans = append(batch.plans, plan)
		batch.indexes = append(batch.indexes, index)
		batch.rows = append(batch.rows, append([]any(nil), lowered.rawValues...))
	}
	return result, nil
}

func sameColumns(left, right []query.ColumnRef) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].Name() != right[i].Name() {
			return false
		}
	}
	return true
}

func bulkStatement[T any](batch bulkBatch[T]) (query.Insert, error) {
	if batch.defaults {
		return query.NewInsert(batch.plans[0].table.Ref(), query.Defaults())
	}
	return query.NewInsertRows(batch.plans[0].table.Ref(), batch.columns, batch.rows)
}

func appendBulkRange(ranges []InputRange, indexes []int) []InputRange {
	if len(indexes) == 0 {
		return ranges
	}
	first, last := indexes[0], indexes[len(indexes)-1]
	if len(ranges) > 0 && ranges[len(ranges)-1].Last+1 == first {
		ranges[len(ranges)-1].Last = last
		return ranges
	}
	return append(ranges, InputRange{First: first, Last: last})
}

func bulkLimits(db DB, options BulkOptions) (int, int, error) {
	maxRows := options.MaxRows
	if maxRows == 0 {
		maxRows = 1000
	}
	if maxRows < 0 {
		return 0, 0, fmt.Errorf("rasql: bulk MaxRows must be positive")
	}
	maxBinds := options.MaxBindParameters
	if maxBinds == 0 {
		switch strings.ToLower(db.Dialect().Name()) {
		case "postgresql", "postgres":
			maxBinds = 65535
		case "mysql":
			maxBinds = 65535
		case "sqlite":
			maxBinds = 32766
		default:
			return 0, 0, fmt.Errorf("rasql: bulk MaxBindParameters is required for custom dialect %q", db.Dialect().Name())
		}
	}
	if maxBinds < 0 {
		return 0, 0, fmt.Errorf("rasql: bulk MaxBindParameters must be positive")
	}
	return maxRows, maxBinds, nil
}
