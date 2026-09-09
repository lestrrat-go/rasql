package rasql

import (
	"context"
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

// nilGuardExecutor is a valid, non-nil Executor whose methods are never
// reached by the tests below: ExecMutation returns from its typed-nil plan
// guard before it calls any of them. It exists only so those tests can pass a
// non-nil executor and exercise the plan guard specifically, instead of
// tripping ExecMutation's separate "executor must not be nil" check.
type nilGuardExecutor struct{}

func (nilGuardExecutor) Dialect() dialect.Dialect { return nil }
func (nilGuardExecutor) Query(context.Context, stmt.Statement) (ResultRows, error) {
	return nil, nil
}
func (nilGuardExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) {
	return nil, nil
}

// TestNewStatementPlanRejectsTypedNilWriteStatement pins the panic this repo
// hit converting a real caller onto the typed API: a var of a concrete
// WriteStatement type left unset and never checked for nil is a typed nil
// pointer stored in the query.WriteStatement interface, so it is not == nil.
// Before the fix, NewStatementPlan's bare "statement == nil" guard missed it
// and called Validate on a nil *Insert, which panics because Validate is a
// value method. Confirmed by running this test against the unmodified guard:
// it panicked with
//
//	value method github.com/lestrrat-go/rasql/query.Insert.Validate called using nil *Insert pointer
//
// instead of reaching the require.EqualError check below.
func TestNewStatementPlanRejectsTypedNilWriteStatement(t *testing.T) {
	var nilInsert *query.Insert
	var nilUpdate *query.Update
	var nilDelete *query.Delete
	var nilUpsert *query.Upsert
	for name, statement := range map[string]query.WriteStatement{
		"insert": nilInsert,
		"update": nilUpdate,
		"delete": nilDelete,
		"upsert": nilUpsert,
	} {
		t.Run(name, func(t *testing.T) {
			require.NotPanics(t, func() {
				_, err := NewStatementPlan(statement)
				require.EqualError(t, err, "rasql: mutation statement must not be nil")
			})
		})
	}
}

// TestExecMutationRejectsTypedNilMutationPlan is the same defect one level up
// the call chain: every MutationPlan implementation is a struct with
// value-receiver methods, so a typed nil pointer to one also passes a bare
// "plan == nil" guard and panics the first time ExecMutation calls
// plan.mutationPlan(). It is exercised through StatementPlan, the one
// MutationPlan implementation this package can hold a pointer to without
// generated code.
func TestExecMutationRejectsTypedNilMutationPlan(t *testing.T) {
	var nilPlan *StatementPlan
	require.NotPanics(t, func() {
		_, err := ExecMutation(t.Context(), nilGuardExecutor{}, nilPlan)
		require.EqualError(t, err, "rasql: mutation plan must not be nil")
	})
}
