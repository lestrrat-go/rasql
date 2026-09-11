package rasql

import (
	"context"
	"errors"
	"fmt"

	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
)

// MutationPlan is a validated immutable write command.
type MutationPlan interface {
	mutationPlan() (query.WriteStatement, error)
}

func (p nativeMutation) mutationPlan() (query.WriteStatement, error) {
	return nil, planError("unsupported_feature", "native", "native mutations do not have a write statement")
}

func (p nativeMutation) nativeMutationPlan() (nativeQueryPlan, error) {
	return p.plan, nil
}

type mutationBatchPlan interface {
	MutationPlan
	mutationInsert() (query.Insert, error)
}

func requireTableOperation[T any](table Table[T], operation schema.Operation) error {
	if table == nil {
		return fmt.Errorf("rasql: table must not be nil")
	}
	if !table.Ref().Definition().Supports(operation) {
		return fmt.Errorf("rasql: object %q does not support operation %d",
			table.Ref().Definition().QualifiedName(), operation)
	}
	return nil
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

// NewDeletePlan builds a validated DELETE plan for the rows of table that where
// matches. It reports an error when where carries no expression.
//
// `table` must not be nil.
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
	if p.table == nil {
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

// NewStatementPlan adapts a validated query write statement to MutationPlan. It
// calls statement.Validate and reports what that returns.
//
// `statement` must not be nil.
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

// ExecMutation compiles plan for executor's engine profile, sends it, and
// reports how many rows it affected. It reports an error when the statement
// carries RETURNING projections; Returning is the entry point for those.
//
// `executor` and `plan` must not be nil.
func ExecMutation(ctx context.Context, executor Executor, plan MutationPlan) (MutationOutcome, error) {
	if executor == nil {
		return MutationOutcome{}, fmt.Errorf("rasql: executor must not be nil")
	}
	if plan == nil {
		return MutationOutcome{}, fmt.Errorf("rasql: mutation plan must not be nil")
	}
	var compiled stmt.Statement
	var err error
	if native, ok := plan.(nativeMutationPlanAccessor); ok {
		compiled, err = compileNativeMutation(executor, native)
	} else {
		statement, statementErr := plan.mutationPlan()
		if statementErr != nil {
			return MutationOutcome{}, statementErr
		}
		if len(statement.Returning()) != 0 {
			return MutationOutcome{}, fmt.Errorf("rasql: mutation has RETURNING projections; use Returning")
		}
		compiled, err = compileMutation(executor, statement)
	}
	if err != nil {
		return MutationOutcome{}, err
	}
	result, err := executor.Exec(ctx, compiled)
	if err != nil {
		// executor.Exec wraps an after-hook failure in *ExtensionError, which
		// still reports whether the driver call underneath it succeeded. When
		// it did, and a result came back, the write already landed: reporting
		// Unknown and zero rows here would tell the caller less than the
		// executor actually knows, so the outcome is filled in from that
		// result instead of being discarded alongside the hook error.
		var extensionErr *ExtensionError
		if !errors.As(err, &extensionErr) || !extensionErr.ExecutionSucceeded() || result == nil {
			return MutationOutcome{Durability: DurabilityUnknown}, err
		}
		affected, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return MutationOutcome{Durability: DurabilityUnknown}, err
		}
		return MutationOutcome{Affected: affected, Durability: executorDurability(executor)}, err
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

func compileMutationParts(executor Executor, statement query.WriteStatement) (compiledQuery, error) {
	provider, ok := executor.(compilerProvider)
	if !ok || provider.queryCompiler() == nil {
		return compiledQuery{}, &PlanError{Code: "engine_profile_unavailable", Detail: "executor has no retained compiler"}
	}
	compiled, err := provider.queryCompiler().Write(statement)
	if err != nil {
		return compiledQuery{}, err
	}
	return unwrapBindTokens(compiled)
}

func compileMutation(executor Executor, statement query.WriteStatement) (stmt.Statement, error) {
	compiledQuery, err := compileMutationParts(executor, statement)
	if err != nil {
		return stmt.Statement{}, err
	}
	statementCopy, err := compiledQuery.Copy()
	if err != nil {
		return stmt.Statement{}, err
	}
	registry := builtinCodecs
	if cp, ok := executor.(CodecProvider); ok && cp.Codecs() != nil {
		registry = cp.Codecs()
	}
	return encodeStatement(statementCopy, compiledQuery.Slots, registry)
}

func compileNativeMutation(executor Executor, native nativeMutationPlanAccessor) (stmt.Statement, error) {
	plan, err := native.nativeMutationPlan()
	if err != nil {
		return stmt.Statement{}, err
	}
	dialect := executor.Dialect()
	if dialect == nil || dialect.Name() != plan.engine {
		return stmt.Statement{}, planError("engine_mismatch", "native.engine", "executor dialect does not match native SQL")
	}
	provider, ok := executor.(compilerProvider)
	if !ok || provider.queryCompiler() == nil {
		return stmt.Statement{}, &PlanError{Code: "engine_profile_unavailable", Detail: "executor has no retained compiler"}
	}
	compiled, err := provider.queryCompiler().Native(plan.statement)
	if err != nil {
		return stmt.Statement{}, err
	}
	parts, err := unwrapBindTokens(compiled)
	if err != nil {
		return stmt.Statement{}, err
	}
	return encodeCompiledMutation(parts, executor)
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
//
// `plan` must not be nil.
func Returning[R any](plan MutationPlan, projection Projection[R]) (Query[R], error) {
	if plan == nil {
		return Query[R]{}, fmt.Errorf("rasql: mutation plan must not be nil")
	}
	if _, ok := plan.(nativeMutationPlanAccessor); ok {
		return Query[R]{}, planError("unsupported_feature", "native", "native mutations cannot return rows")
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
	result := Query[R]{projection: projection, plan: QueryPlan{mutation: returning}}
	if versioned, ok := plan.(interface{ mutationPrecondition() bool }); ok && versioned.mutationPrecondition() {
		result.resultRequirement = queryResultRequirement{cardinality: ExactlyOne, emptyErr: ErrPrecondition}
	}
	return result, nil
}

// ExecMutationBatch groups plans into multi-row statements bounded by options
// and sends them in order. Every plan must target the same table, and none may
// carry RETURNING projections.
//
// `executor` must not be nil, and no element of `plans` may be nil.
func ExecMutationBatch(ctx context.Context, executor Executor, plans []MutationPlan, options BulkOptions) (BulkOutcome, error) {
	if executor == nil {
		return BulkOutcome{}, fmt.Errorf("rasql: executor must not be nil")
	}
	if len(plans) == 0 {
		return BulkOutcome{}, fmt.Errorf("rasql: mutation batch requires at least one plan")
	}
	outcome := BulkOutcome{Inputs: make([]InputOutcome, len(plans))}
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
	for index, plan := range plans {
		if plan == nil {
			return outcome, fmt.Errorf("rasql: mutation batch input %d is nil", index)
		}
		if native, ok := plan.(nativeMutationPlanAccessor); ok {
			if _, err := native.nativeMutationPlan(); err != nil {
				return outcome, err
			}
			return outcome, planError("unsupported_feature", fmt.Sprintf("inputs[%d]", index), "native mutations are not supported in batches")
		}
	}
	outcome.Durability = executorDurability(executor)
	bindLimit := mutationBindLimit(executor, options.MaxBindParameters)
	var target string
	for index, plan := range plans {
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
	var preparedOutcome BulkOutcome
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
			var best compiledQuery
			compileCandidate := func(insert query.Insert) (compiledQuery, error) { return compileMutationParts(executor, insert) }
			if first.UsesDefaultValues() {
				best, err = compileCandidate(first)
				if err != nil {
					return nil, err
				}
			} else {
				best, err = compileCandidate(first)
				if err != nil {
					return nil, err
				}
				if bindLimit > 0 && len(best.Statement.BoundArgs()) > bindLimit {
					return nil, &PlanError{Code: "bind_limit", Path: "args", Detail: "mutation batch exceeds bind parameter limit"}
				}
			}
			for end < len(plans) && !first.UsesDefaultValues() && end-i < maxRows {
				next, ok := plans[end].(mutationBatchPlan)
				if !ok {
					break
				}
				insert, insertErr := next.mutationInsert()
				if insertErr != nil || !sameColumns(columns, insert.Columns()) || insert.UsesDefaultValues() != first.UsesDefaultValues() {
					break
				}
				candidateRows := append(append([][]any(nil), rows...), mutationRows(insert.Rows())...)
				candidate, candidateErr := query.NewInsertRows(first.Into(), columns, candidateRows)
				if candidateErr != nil {
					return nil, candidateErr
				}
				compiled, candidateErr := compileCandidate(candidate)
				if candidateErr != nil {
					if end > i && isMutationBindLimit(candidateErr) {
						break
					}
					return nil, candidateErr
				}
				if bindLimit > 0 && len(compiled.Statement.BoundArgs()) > bindLimit {
					break
				}
				rows = candidateRows
				best = compiled
				end++
			}
			encoded, err := encodeCompiledMutation(best, executor)
			if err != nil {
				return nil, err
			}
			prepared = append(prepared, preparedMutationBatch{start: i, end: end, statement: encoded})
			i = end
			continue
		}
		statement, err := plans[i].mutationPlan()
		if err != nil {
			return nil, err
		}
		compiledParts, err := compileMutationParts(executor, statement)
		if err != nil {
			return nil, err
		}
		if bindLimit > 0 && len(compiledParts.Statement.BoundArgs()) > bindLimit {
			return nil, &PlanError{Code: "bind_limit", Path: "args", Detail: "mutation statement exceeds bind parameter limit"}
		}
		compiled, err := encodeCompiledMutation(compiledParts, executor)
		if err != nil {
			return nil, err
		}
		prepared = append(prepared, preparedMutationBatch{start: i, end: i + 1, statement: compiled})
		i++
	}
	return prepared, nil
}

func isMutationBindLimit(err error) bool {
	var planErr *PlanError
	return errors.Is(err, engineprofile.ErrBindLimit) || (errors.As(err, &planErr) && planErr.Code == "bind_limit")
}

func encodeCompiledMutation(compiled compiledQuery, executor Executor) (stmt.Statement, error) {
	copy, err := compiled.Copy()
	if err != nil {
		return stmt.Statement{}, err
	}
	registry := builtinCodecs
	if cp, ok := executor.(CodecProvider); ok && cp.Codecs() != nil {
		registry = cp.Codecs()
	}
	return encodeStatement(copy, compiled.Slots, registry)
}

func execPreparedMutationBatches(ctx context.Context, executor Executor, prepared []preparedMutationBatch, options BulkOptions, outcome BulkOutcome) (BulkOutcome, error) {
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

func execAtomicMutationBatch(ctx context.Context, executor Executor, plans []MutationPlan, options BulkOptions) (BulkOutcome, error) {
	maxRows := options.MaxRows
	if maxRows == 0 {
		maxRows = 1000
	}
	prepared, err := prepareMutationBatches(executor, plans, maxRows, mutationBindLimit(executor, options.MaxBindParameters))
	outcome := BulkOutcome{Inputs: make([]InputOutcome, len(plans)), Durability: executorDurability(executor)}
	if err != nil {
		return outcome, err
	}
	if err := ctx.Err(); err != nil {
		return outcome, err
	}
	var child Executor
	var finalizer ScopeFinalizer
	if state, ok := executor.(ScopeState); ok && state.IsTransaction() {
		beginner, supported := executor.(SavepointBeginner)
		if !supported {
			return outcome, planError("savepoint_unsupported", "scope", "executor does not support savepoints")
		}
		child, finalizer, err = beginner.BeginSavepoint(ctx)
	} else {
		beginner, supported := executor.(ScopeBeginner)
		if !supported {
			return outcome, planError("transaction_scope_unsupported", "scope", "executor does not support transaction scopes")
		}
		child, finalizer, err = beginner.BeginScope(ctx, nil)
	}
	if err != nil {
		return outcome, err
	}
	if child == nil {
		return outcome, planError("transaction_scope_invalid", "scope", "scope beginner returned a nil child executor")
	}
	if finalizer == nil {
		return outcome, planError("transaction_scope_invalid", "scope", "scope beginner returned a nil finalizer")
	}
	logicalCtx, logicalExecutor, complete := beginLogicalInvocation(ctx, child, EventMutationBatch)
	var executionErr error
	var panicked any
	func() {
		defer func() { panicked = recover() }()
		outcome, executionErr = execPreparedMutationBatches(logicalCtx, logicalExecutor, prepared, options, outcome)
	}()
	attempted := mutationAttempted(outcome)
	cleanupCtx, cancel := atomicCleanupContext(logicalCtx)
	defer cancel()
	if panicked != nil {
		rollbackErr := finalizer.Rollback(cleanupCtx)
		if rollbackErr != nil {
			for _, index := range attempted {
				outcome.Inputs[index] = InputUnknown
			}
			outcome.Durability = DurabilityUnknown
			joined := errors.Join(fmt.Errorf("mutation batch panicked: %v", panicked), rollbackErr)
			complete.completeLogicalInvocation(joined, 0, false)
			panic(AtomicPanic{Value: panicked, Cleanup: rollbackErr})
		}
		for _, index := range attempted {
			if outcome.Inputs[index] == InputApplied {
				outcome.Inputs[index] = InputRolledBack
			}
		}
		outcome.Durability = DurabilityPending
		complete.completeLogicalInvocation(fmt.Errorf("mutation batch panicked: %v", panicked), 0, false)
		panic(panicked)
	}
	if executionErr != nil {
		rollbackErr := finalizer.Rollback(cleanupCtx)
		if rollbackErr != nil {
			for _, index := range attempted {
				outcome.Inputs[index] = InputUnknown
			}
			outcome.Durability = DurabilityUnknown
			joined := errors.Join(executionErr, rollbackErr)
			complete.completeLogicalInvocation(joined, 0, false)
			return outcome, joined
		}
		for _, index := range attempted {
			if outcome.Inputs[index] == InputApplied {
				outcome.Inputs[index] = InputRolledBack
			}
		}
		outcome.Durability = DurabilityPending
		complete.completeLogicalInvocation(executionErr, 0, false)
		return outcome, executionErr
	}
	if err := finalizer.Commit(cleanupCtx); err != nil {
		for _, index := range attempted {
			outcome.Inputs[index] = InputUnknown
		}
		outcome.Durability = DurabilityUnknown
		complete.completeLogicalInvocation(err, 0, false)
		return outcome, err
	}
	outcome.Durability = executorDurability(executor)
	complete.completeLogicalInvocation(nil, 0, false)
	return outcome, nil
}

func mutationAttempted(outcome BulkOutcome) []int {
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

func sameColumns(left, right []query.ColumnRef) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Name() != right[index].Name() {
			return false
		}
	}
	return true
}

func makeRange(first, end int) []int {
	result := make([]int, end-first)
	for i := range result {
		result[i] = first + i
	}
	return result
}
