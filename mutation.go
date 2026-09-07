package rasql

import (
	"context"
	"errors"
	"fmt"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
)

// MutationPlan is a validated immutable write command.
type MutationPlan interface {
	mutationPlan() (query.WriteStatement, error)
}

type mutationBatchPlan interface {
	MutationPlan
	mutationInsert() (query.Insert, error)
}

func (p CreatePlan[T]) mutationPlan() (query.WriteStatement, error) { return p.lower() }
func (p CreatePlan[T]) mutationInsert() (query.Insert, error)       { return p.lower() }
func (p PatchPlan[T]) mutationPlan() (query.WriteStatement, error)  { return p.lower() }
func (p PatchPlan[T]) mutationPrecondition() bool                   { return p.version != nil }

// DeletePlan is a typed immutable DELETE command.
type DeletePlan[T any] struct {
	table Table[T]
	where query.Predicate
	all   bool
}

// UpsertPlan adapts a validated dialect-neutral upsert to the mutation API.
type UpsertPlan[T any] struct{ statement query.Upsert }

func NewUpsertPlan[T any](statement query.Upsert) (UpsertPlan[T], error) {
	if err := statement.Validate(); err != nil {
		return UpsertPlan[T]{}, err
	}
	return UpsertPlan[T]{statement: statement}, nil
}

func (p UpsertPlan[T]) mutationPlan() (query.WriteStatement, error) { return p.statement, nil }

func NewDeletePlan[T any](table Table[T], where query.Predicate) (DeletePlan[T], error) {
	if err := requireTableOperation(table, schema.OperationDelete); err != nil {
		return DeletePlan[T]{}, err
	}
	if where.Expression() == nil {
		return DeletePlan[T]{}, fmt.Errorf("rasql: delete plan requires a predicate")
	}
	return DeletePlan[T]{table: table, where: where}, nil
}

func (p DeletePlan[T]) mutationPlan() (query.WriteStatement, error) {
	if isNilTable(p.table) {
		return nil, fmt.Errorf("rasql: delete plan table must not be nil")
	}
	statement, err := query.NewDelete(p.table.Ref())
	if err != nil {
		return nil, err
	}
	if p.where.Expression() == nil {
		if !p.all {
			return nil, fmt.Errorf("rasql: delete plan requires a predicate")
		}
		return statement.AllowAll()
	}
	return statement.WithWhere(p.where.Expression())
}

// StatementPlan adapts a validated query write statement to MutationPlan.
type StatementPlan struct{ statement query.WriteStatement }

func NewStatementPlan(statement query.WriteStatement) (StatementPlan, error) {
	if statement == nil {
		return StatementPlan{}, fmt.Errorf("rasql: mutation statement must not be nil")
	}
	if err := statement.Validate(); err != nil {
		return StatementPlan{}, err
	}
	return StatementPlan{statement: statement}, nil
}
func (p StatementPlan) mutationPlan() (query.WriteStatement, error) { return p.statement, nil }

func ExecMutation(ctx context.Context, executor Executor, plan MutationPlan) (MutationOutcome, error) {
	if executor == nil {
		return MutationOutcome{}, fmt.Errorf("rasql: executor must not be nil")
	}
	if plan == nil {
		return MutationOutcome{}, fmt.Errorf("rasql: mutation plan must not be nil")
	}
	statement, err := plan.mutationPlan()
	if err != nil {
		return MutationOutcome{}, err
	}
	if len(statement.Returning()) != 0 {
		return MutationOutcome{}, fmt.Errorf("rasql: mutation has RETURNING projections; use Returning")
	}
	compiled, err := compileMutation(executor, statement)
	if err != nil {
		return MutationOutcome{}, err
	}
	result, err := executor.Exec(ctx, compiled)
	if err != nil {
		return MutationOutcome{Durability: DurabilityUnknown}, err
	}
	if result == nil {
		return MutationOutcome{Durability: DurabilityUnknown}, fmt.Errorf("rasql: executor returned nil mutation result")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return MutationOutcome{Durability: DurabilityUnknown}, err
	}
	outcome := MutationOutcome{Affected: affected, Durability: executorDurability(executor)}
	if versioned, ok := plan.(interface{ mutationPrecondition() bool }); ok && versioned.mutationPrecondition() {
		switch {
		case affected == 0:
			return outcome, ErrPrecondition
		case affected > 1:
			return outcome, ErrMultipleRows
		}
	}
	return outcome, nil
}

func compileMutation(executor Executor, statement query.WriteStatement) (stmt.Statement, error) {
	provider, ok := executor.(compilerProvider)
	if !ok || provider.queryCompiler() == nil {
		return stmt.Statement{}, &PlanError{Code: "engine_profile_unavailable", Detail: "executor has no retained compiler"}
	}
	compiled, err := provider.queryCompiler().Write(statement)
	if err != nil {
		return stmt.Statement{}, err
	}
	compiledQuery, err := unwrapBindTokens(compiled)
	if err != nil {
		return stmt.Statement{}, err
	}
	registry := builtinCodecs
	if cp, ok := executor.(CodecProvider); ok && cp.Codecs() != nil {
		registry = cp.Codecs()
	}
	return encodeCompiled(compiledQuery, registry)
}

func executorDurability(executor Executor) Durability {
	provider, ok := executor.(executionDurabilityProvider)
	if !ok {
		return DurabilityUnknown
	}
	switch provider.executionDurability() {
	case executionDurabilityCommitted:
		return DurabilityCommitted
	case executionDurabilityPending:
		return DurabilityPending
	default:
		return DurabilityUnknown
	}
}

// Returning attaches the requested Q1 projection to a mutation.
func Returning[R any](plan MutationPlan, projection Projection[R]) (Query[R], error) {
	if plan == nil {
		return Query[R]{}, fmt.Errorf("rasql: mutation plan must not be nil")
	}
	if len(projection.items) == 0 || projection.decoder == nil {
		return Query[R]{}, fmt.Errorf("rasql: returning projection must not be zero")
	}
	statement, err := plan.mutationPlan()
	if err != nil {
		return Query[R]{}, err
	}
	projections := make([]query.Projection, 0, len(projection.items))
	for _, item := range projection.items {
		p := query.Project(item.expression)
		if item.column.Name != "" {
			p = p.As(item.column.Name)
		}
		projections = append(projections, p)
	}
	var returning query.WriteStatement
	switch value := statement.(type) {
	case query.Insert:
		returning, err = value.WithReturning(projections...)
	case query.Update:
		returning, err = value.WithReturning(projections...)
	case query.Delete:
		returning, err = value.WithReturning(projections...)
	case query.Upsert:
		returning, err = value.WithReturning(projections...)
	default:
		return Query[R]{}, fmt.Errorf("rasql: unsupported mutation statement %T", statement)
	}
	if err != nil {
		return Query[R]{}, err
	}
	return Query[R]{projection: projection, plan: QueryPlan{mutation: returning}}, nil
}

func ExecMutationBatch(ctx context.Context, executor Executor, plans []MutationPlan, options MutationBatchOptions) (MutationBatchOutcome, error) {
	if executor == nil {
		return MutationBatchOutcome{}, fmt.Errorf("rasql: executor must not be nil")
	}
	if len(plans) == 0 {
		return MutationBatchOutcome{}, fmt.Errorf("rasql: mutation batch requires at least one plan")
	}
	outcome := MutationBatchOutcome{Inputs: make([]InputOutcome, len(plans)), Durability: executorDurability(executor)}
	maxRows := options.MaxRows
	if maxRows == 0 {
		maxRows = 1000
	}
	if maxRows < 1 {
		return outcome, fmt.Errorf("rasql: mutation batch MaxRows must be positive")
	}
	if options.MaxBindParameters < 0 {
		return outcome, fmt.Errorf("rasql: mutation batch MaxBindParameters must be positive")
	}
	bindLimit := mutationBindLimit(executor, options.MaxBindParameters)
	var target string
	for index, plan := range plans {
		if plan == nil {
			return outcome, fmt.Errorf("rasql: mutation batch input %d is nil", index)
		}
		statement, err := plan.mutationPlan()
		if err != nil {
			return outcome, err
		}
		if len(statement.Returning()) > 0 {
			return outcome, fmt.Errorf("rasql: mutation batch input %d has RETURNING projections", index)
		}
		identity := mutationTarget(statement)
		if identity == "" {
			continue
		}
		if target == "" {
			target = identity
			continue
		}
		if target != identity {
			return outcome, fmt.Errorf("rasql: mutation batch inputs target mixed tables %q and %q", target, identity)
		}
	}
	if options.Atomic {
		return execAtomicMutationBatch(ctx, executor, plans, options)
	}
	prepared, err := prepareMutationBatches(executor, plans, maxRows, bindLimit)
	if err != nil {
		return outcome, err
	}
	if err := ctx.Err(); err != nil {
		return outcome, err
	}
	logicalCtx, logicalExecutor, complete := beginLogicalInvocation(ctx, executor, EventMutationBatch)
	var preparedOutcome MutationBatchOutcome
	var executionErr error
	func() {
		defer func() {
			if value := recover(); value != nil {
				complete.completeLogicalInvocation(fmt.Errorf("mutation batch panicked: %v", value), 0, false)
				panic(value)
			}
		}()
		preparedOutcome, executionErr = execPreparedMutationBatches(logicalCtx, logicalExecutor, prepared, options, outcome)
	}()
	completeErr := executionErr
	complete.completeLogicalInvocation(completeErr, 0, false)
	return preparedOutcome, executionErr
}

type preparedMutationBatch struct {
	start, end int
	statement  stmt.Statement
}

func prepareMutationBatches(executor Executor, plans []MutationPlan, maxRows, bindLimit int) ([]preparedMutationBatch, error) {
	prepared := make([]preparedMutationBatch, 0, len(plans))
	for i := 0; i < len(plans); {
		if batch, ok := plans[i].(mutationBatchPlan); ok {
			end := i + 1
			first, err := batch.mutationInsert()
			if err != nil {
				return nil, err
			}
			columns := first.Columns()
			rows := mutationRows(first.Rows())
			for end < len(plans) && !first.UsesDefaultValues() && end-i < maxRows {
				next, ok := plans[end].(mutationBatchPlan)
				if !ok {
					break
				}
				insert, insertErr := next.mutationInsert()
				if insertErr != nil || !sameColumns(columns, insert.Columns()) || insert.UsesDefaultValues() != first.UsesDefaultValues() {
					break
				}
				if bindLimit > 0 && (len(rows)+len(insert.Rows()))*len(columns) > bindLimit {
					break
				}
				rows = append(rows, mutationRows(insert.Rows())...)
				end++
			}
			var statement query.Insert
			if first.UsesDefaultValues() && end-i == 1 {
				statement = first
			} else if len(rows) > 0 {
				statement, err = query.NewInsertRows(first.Into(), columns, rows)
				if err != nil {
					return nil, err
				}
			}
			compiled, compileErr := compileMutation(executor, statement)
			if compileErr != nil {
				return nil, compileErr
			}
			prepared = append(prepared, preparedMutationBatch{start: i, end: end, statement: compiled})
			i = end
			continue
		}
		statement, err := plans[i].mutationPlan()
		if err != nil {
			return nil, err
		}
		compiled, err := compileMutation(executor, statement)
		if err != nil {
			return nil, err
		}
		prepared = append(prepared, preparedMutationBatch{start: i, end: i + 1, statement: compiled})
		i++
	}
	return prepared, nil
}

func execPreparedMutationBatches(ctx context.Context, executor Executor, prepared []preparedMutationBatch, options MutationBatchOptions, outcome MutationBatchOutcome) (MutationBatchOutcome, error) {
	for _, batch := range prepared {
		if err := ctx.Err(); err != nil {
			return outcome, err
		}
		result, execErr := executor.Exec(ctx, batch.statement)
		if execErr == nil && result == nil {
			execErr = fmt.Errorf("rasql: executor returned nil mutation result")
		}
		if execErr == nil {
			if _, rowsErr := result.RowsAffected(); rowsErr != nil {
				execErr = rowsErr
			}
		}
		if execErr != nil {
			certainty := InputUnknown
			if options.Classifier != nil && options.Classifier.Certainty(execErr) == OutcomeRejected {
				certainty = InputRejected
			}
			for index := batch.start; index < batch.end; index++ {
				outcome.Inputs[index] = certainty
			}
			outcome.FailedBatch = append([]int(nil), makeRange(batch.start, batch.end)...)
			return outcome, execErr
		}
		for index := batch.start; index < batch.end; index++ {
			outcome.Inputs[index] = InputApplied
		}
	}
	return outcome, nil
}

func mutationBindLimit(executor Executor, override int) int {
	limit := 0
	if provider, ok := executor.(compilerProvider); ok && provider.queryCompiler() != nil {
		limit = provider.queryCompiler().EngineProfile().Limits.MaxBindParameters
	}
	if override > 0 && (limit == 0 || override < limit) {
		return override
	}
	return limit
}

func execAtomicMutationBatch(ctx context.Context, executor Executor, plans []MutationPlan, options MutationBatchOptions) (MutationBatchOutcome, error) {
	var child Executor
	var finalizer scopeFinalizer
	var err error
	if state, ok := executor.(scopeState); ok && state.scopeIsTransaction() {
		beginner, supported := executor.(savepointBeginner)
		if !supported {
			return MutationBatchOutcome{}, planError("savepoint_unsupported", "scope", "executor does not support savepoints")
		}
		child, finalizer, err = beginner.beginSavepoint(ctx)
	} else {
		beginner, supported := executor.(transactionBeginner)
		if !supported {
			return MutationBatchOutcome{}, planError("transaction_scope_unsupported", "scope", "executor does not support transaction scopes")
		}
		child, finalizer, err = beginner.beginScope(ctx, nil)
	}
	if err != nil {
		return MutationBatchOutcome{}, err
	}
	if isNilExecutor(child) {
		return MutationBatchOutcome{}, planError("transaction_scope_invalid", "scope", "scope beginner returned a nil child executor")
	}
	if isNilScopeFinalizer(finalizer) {
		return MutationBatchOutcome{}, planError("transaction_scope_invalid", "scope", "scope beginner returned a nil finalizer")
	}
	nonAtomic := options
	nonAtomic.Atomic = false
	var outcome MutationBatchOutcome
	var executionErr error
	var panicked any
	func() {
		defer func() { panicked = recover() }()
		outcome, executionErr = ExecMutationBatch(ctx, child, plans, nonAtomic)
	}()
	if panicked != nil {
		attempted := mutationAttempted(outcome)
		cleanupCtx, cancel := atomicCleanupContext(ctx)
		rollbackErr := finalizer.Rollback(cleanupCtx)
		cancel()
		if rollbackErr != nil {
			for _, index := range attempted {
				outcome.Inputs[index] = InputUnknown
			}
			panic(AtomicPanic{Value: panicked, Cleanup: rollbackErr})
		}
		for _, index := range attempted {
			if outcome.Inputs[index] == InputApplied {
				outcome.Inputs[index] = InputRolledBack
			}
		}
		panic(panicked)
	}
	attempted := mutationAttempted(outcome)
	if executionErr != nil {
		cleanupCtx, cancel := atomicCleanupContext(ctx)
		defer cancel()
		rollbackErr := finalizer.Rollback(cleanupCtx)
		if rollbackErr != nil {
			for _, index := range attempted {
				outcome.Inputs[index] = InputUnknown
			}
			outcome.Durability = DurabilityUnknown
			return outcome, errors.Join(executionErr, rollbackErr)
		}
		for _, index := range attempted {
			if outcome.Inputs[index] == InputApplied {
				outcome.Inputs[index] = InputRolledBack
			}
		}
		outcome.Durability = DurabilityPending
		return outcome, executionErr
	}
	cleanupCtx, cancel := atomicCleanupContext(ctx)
	defer cancel()
	if err := finalizer.Commit(cleanupCtx); err != nil {
		for _, index := range attempted {
			outcome.Inputs[index] = InputUnknown
		}
		outcome.Durability = DurabilityUnknown
		return outcome, err
	}
	outcome.Durability = executorDurability(executor)
	return outcome, nil
}

func mutationAttempted(outcome MutationBatchOutcome) []int {
	indexes := make([]int, 0, len(outcome.Inputs))
	for index, state := range outcome.Inputs {
		if state != InputUnattempted {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

func mutationTarget(statement query.WriteStatement) string {
	switch value := statement.(type) {
	case query.Insert:
		return value.Into().Definition().QualifiedName()
	case query.Update:
		return value.Table().Definition().QualifiedName()
	case query.Delete:
		return value.From().Definition().QualifiedName()
	case query.Upsert:
		return value.Insert().Into().Definition().QualifiedName()
	default:
		return ""
	}
}

func mutationRows(rows [][]query.Expression) [][]any {
	result := make([][]any, len(rows))
	for i, row := range rows {
		result[i] = make([]any, len(row))
		for j, value := range row {
			result[i][j] = value
		}
	}
	return result
}

func makeRange(first, end int) []int {
	result := make([]int, end-first)
	for i := range result {
		result[i] = first + i
	}
	return result
}
