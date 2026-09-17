package rasql_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"sync"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// runtimeParamQuery builds a query.[int64] whose WHERE clause compares the
// "value" column against a fresh Parameter, so a caller can Prepare it once
// and Bind a different value per run.
func runtimeParamQuery(t *testing.T) (rasql.Query[int64], rasql.Parameter[int64]) {
	t.Helper()
	table, err := rasql.TableOf[struct{}](schema.TableDef{Name: "items", Columns: []schema.ColumnDef{{Name: "value", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	relation, err := table.Source("i")
	require.NoError(t, err)
	column, err := rasql.BindColumn[struct{}, int64](relation, "value", "")
	require.NoError(t, err)
	resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "value", Type: schema.IntegerType{}})
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("value", column.Expr(), schema.IntegerType{}, "")}, runtimeDecoder{schema: resultSchema})
	require.NoError(t, err)
	param := rasql.NewParameter[int64]()
	query := rasql.Select(relation.Source(), projection).Where(rasql.EqualExpr(column.Expr(), param.Expr()))
	return query, param
}

// runtimeParamTwiceQuery is runtimeParamQuery with the parameter placed in
// two operands of one WHERE, so Bind must fill both from the one value it is
// given.
func runtimeParamTwiceQuery(t *testing.T) (rasql.Query[int64], rasql.Parameter[int64]) {
	t.Helper()
	table, err := rasql.TableOf[struct{}](schema.TableDef{Name: "items", Columns: []schema.ColumnDef{{Name: "value", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	relation, err := table.Source("i")
	require.NoError(t, err)
	column, err := rasql.BindColumn[struct{}, int64](relation, "value", "")
	require.NoError(t, err)
	resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "value", Type: schema.IntegerType{}})
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("value", column.Expr(), schema.IntegerType{}, "")}, runtimeDecoder{schema: resultSchema})
	require.NoError(t, err)
	param := rasql.NewParameter[int64]()
	query := rasql.Select(relation.Source(), projection).Where(rasql.Or(
		rasql.EqualExpr(column.Expr(), param.Expr()),
		rasql.EqualExpr(column.Expr(), param.Expr()),
	))
	return query, param
}

// runtimeParamCodecQuery is runtimeParamQuery with the parameter built through
// NewParameterWithCodec, so Bind encodes its value through the codec "age"
// names.
func runtimeParamCodecQuery(t *testing.T) (rasql.Query[int64], rasql.Parameter[int64]) {
	t.Helper()
	table, err := rasql.TableOf[struct{}](schema.TableDef{Name: "items", Columns: []schema.ColumnDef{{Name: "value", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	relation, err := table.Source("i")
	require.NoError(t, err)
	column, err := rasql.BindColumn[struct{}, int64](relation, "value", "")
	require.NoError(t, err)
	resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "value", Type: schema.IntegerType{}})
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("value", column.Expr(), schema.IntegerType{}, "")}, runtimeDecoder{schema: resultSchema})
	require.NoError(t, err)
	param, err := rasql.NewParameterWithCodec[int64]("age")
	require.NoError(t, err)
	query := rasql.Select(relation.Source(), projection).Where(rasql.EqualExpr(column.Expr(), param.Expr()))
	return query, param
}

// passthroughParamCodec encodes and decodes an int64 unchanged, which is
// enough to prove a bind travelled through it.
type passthroughParamCodec struct{}

func (passthroughParamCodec) Encode(v any) (driver.Value, error) { return v, nil }
func (passthroughParamCodec) Decode(v any, dest any) error {
	*dest.(*int64) = v.(int64)
	return nil
}

// runtimeEchoExecutor answers Query with one row holding the first bound
// argument, so a test can tell one run's parameter value from another's
// without inspecting the statement the runtime sent.
type runtimeEchoExecutor struct{ dialect dialect.Dialect }

func (e runtimeEchoExecutor) Dialect() dialect.Dialect { return e.dialect }
func (e runtimeEchoExecutor) Query(_ context.Context, statement stmt.Statement) (rasql.ResultRows, error) {
	args := statement.BoundArgs()
	var value int64
	if len(args) > 0 {
		value, _ = args[0].(int64)
	}
	return &runtimeFakeRows{values: [][]any{{value}}}, nil
}
func (runtimeEchoExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) {
	return driver.RowsAffected(0), nil
}

func runtimeEchoExecutorFor(t *testing.T) rasql.Executor {
	t.Helper()
	profile := rasql.SQLite335()
	executor, err := rasql.WithEngineProfile(runtimeEchoExecutor{dialect: dialect.SQLite()}, profile)
	require.NoError(t, err)
	return executor
}

func TestPrepare(t *testing.T) {
	t.Run("executes many times with identical results", func(t *testing.T) {
		q := runtimeQuery(t)
		executor, raw := runtimeExecutor(t, [][]any{{int64(1)}, {int64(2)}})
		prepared, err := rasql.Prepare(executor, q)
		require.NoError(t, err)
		for i := 0; i < 3; i++ {
			values, err := prepared.All(t.Context(), executor)
			require.NoError(t, err)
			require.Equal(t, []int64{1, 2}, values)
		}
		require.Equal(t, int64(3), raw.calls.Load())
	})

	t.Run("each execution gets a fresh row sequence", func(t *testing.T) {
		q := runtimeQuery(t)
		executor, _ := runtimeExecutor(t, [][]any{{int64(1)}, {int64(2)}})
		prepared, err := rasql.Prepare(executor, q)
		require.NoError(t, err)
		for i := 0; i < 3; i++ {
			sequence, err := prepared.Rows(t.Context(), executor)
			require.NoError(t, err)
			var got []int64
			for value, rowErr := range sequence {
				require.NoError(t, rowErr)
				got = append(got, value)
			}
			require.Equal(t, []int64{1, 2}, got)
		}
	})

	t.Run("reports an error for a query that fails validation", func(t *testing.T) {
		executor, _ := runtimeExecutor(t, nil)
		_, err := rasql.Prepare(executor, rasql.Query[int64]{})
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "invalid_source", planErr.Code)
	})

	t.Run("reports a mismatch when run against a different executor", func(t *testing.T) {
		q := runtimeQuery(t)
		executorA, _ := runtimeExecutor(t, [][]any{{int64(1)}})
		executorB, _ := runtimeExecutor(t, [][]any{{int64(1)}})
		prepared, err := rasql.Prepare(executorA, q)
		require.NoError(t, err)

		var planErr *rasql.PlanError

		_, err = prepared.Rows(t.Context(), executorB)
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "prepared_executor_mismatch", planErr.Code)

		_, err = prepared.All(t.Context(), executorB)
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "prepared_executor_mismatch", planErr.Code)

		_, err = prepared.One(t.Context(), executorB)
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "prepared_executor_mismatch", planErr.Code)

		_, _, err = prepared.Maybe(t.Context(), executorB)
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "prepared_executor_mismatch", planErr.Code)
	})

	t.Run("reports a mismatch when the executor is rewrapped with different codecs", func(t *testing.T) {
		q := runtimeQuery(t)
		executor, _ := runtimeExecutor(t, [][]any{{int64(1)}})
		registryA, err := rasql.NewCodecRegistry(nil)
		require.NoError(t, err)
		withA, err := rasql.WithCodecs(executor, registryA)
		require.NoError(t, err)
		prepared, err := rasql.Prepare(withA, q)
		require.NoError(t, err)

		registryB, err := rasql.NewCodecRegistry(nil)
		require.NoError(t, err)
		withB, err := rasql.WithCodecs(executor, registryB)
		require.NoError(t, err)

		var planErr *rasql.PlanError

		_, err = prepared.Rows(t.Context(), withB)
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "prepared_executor_mismatch", planErr.Code)

		_, err = prepared.All(t.Context(), withB)
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "prepared_executor_mismatch", planErr.Code)

		_, err = prepared.One(t.Context(), withB)
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "prepared_executor_mismatch", planErr.Code)

		_, _, err = prepared.Maybe(t.Context(), withB)
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "prepared_executor_mismatch", planErr.Code)

		// The registry Prepare actually captured still runs.
		values, err := prepared.All(t.Context(), withA)
		require.NoError(t, err)
		require.Equal(t, []int64{1}, values)
	})

	t.Run("keeps working inside a scope opened after Prepare", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		_, err = database.ExecContext(t.Context(), `CREATE TABLE items (value INTEGER)`)
		require.NoError(t, err)
		_, err = database.ExecContext(t.Context(), `INSERT INTO items VALUES (1), (2)`)
		require.NoError(t, err)
		base, err := rasql.Open(t.Context(), database, dialect.SQLite())
		require.NoError(t, err)
		registry, err := rasql.NewCodecRegistry(nil)
		require.NoError(t, err)
		executor, err := rasql.WithCodecs(base, registry)
		require.NoError(t, err)

		q := runtimeQuery(t)
		prepared, err := rasql.Prepare(executor, q)
		require.NoError(t, err)

		beginner, ok := executor.(rasql.ScopeBeginner)
		require.True(t, ok)
		child, finalizer, err := beginner.BeginScope(t.Context(), nil)
		require.NoError(t, err)
		values, err := prepared.All(t.Context(), child)
		require.NoError(t, err)
		require.Equal(t, []int64{1, 2}, values)
		require.NoError(t, finalizer.Rollback(t.Context()))
	})

	t.Run("is safe to execute from multiple goroutines", func(t *testing.T) {
		q := runtimeQuery(t)
		executor, raw := runtimeExecutor(t, [][]any{{int64(1)}})
		prepared, err := rasql.Prepare(executor, q)
		require.NoError(t, err)
		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				values, err := prepared.All(context.Background(), executor)
				require.NoError(t, err)
				require.Equal(t, []int64{1}, values)
			}()
		}
		wg.Wait()
		require.Equal(t, int64(100), raw.calls.Load())
	})

	t.Run("binds one parameter and runs with two different values", func(t *testing.T) {
		q, param := runtimeParamQuery(t)
		executor, raw := runtimeExecutor(t, [][]any{{int64(1)}})
		prepared, err := rasql.Prepare(executor, q)
		require.NoError(t, err)

		adults, err := prepared.Bind(param.Value(int64(18)))
		require.NoError(t, err)
		_, err = adults.All(t.Context(), executor)
		require.NoError(t, err)
		firstSQL, firstArgs := raw.lastStatement.SQL(), raw.lastStatement.Args()

		seniors, err := prepared.Bind(param.Value(int64(65)))
		require.NoError(t, err)
		_, err = seniors.All(t.Context(), executor)
		require.NoError(t, err)
		secondSQL, secondArgs := raw.lastStatement.SQL(), raw.lastStatement.Args()

		require.Equal(t, firstSQL, secondSQL)
		require.Equal(t, []any{int64(18)}, firstArgs)
		require.Equal(t, []any{int64(65)}, secondArgs)
	})

	t.Run("a parameter placed twice in one WHERE fills both arguments from one value", func(t *testing.T) {
		q, param := runtimeParamTwiceQuery(t)
		executor, raw := runtimeExecutor(t, [][]any{{int64(1)}})
		prepared, err := rasql.Prepare(executor, q)
		require.NoError(t, err)
		bound, err := prepared.Bind(param.Value(int64(7)))
		require.NoError(t, err)
		_, err = bound.All(t.Context(), executor)
		require.NoError(t, err)
		require.Equal(t, []any{int64(7), int64(7)}, raw.lastStatement.Args())
	})

	t.Run("running with a parameter unbound reports parameter_unbound and calls the executor zero times", func(t *testing.T) {
		q, _ := runtimeParamQuery(t)
		executor, raw := runtimeExecutor(t, [][]any{{int64(1)}})
		prepared, err := rasql.Prepare(executor, q)
		require.NoError(t, err)
		_, err = prepared.All(t.Context(), executor)
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "parameter_unbound", planErr.Code)
		require.Equal(t, "binds[0]", planErr.Path)
		require.Equal(t, int64(0), raw.calls.Load())
	})

	t.Run("Bind reports invalid_parameter, unknown_parameter, and duplicate_parameter", func(t *testing.T) {
		q, param := runtimeParamQuery(t)
		executor, _ := runtimeExecutor(t, [][]any{{int64(1)}})
		prepared, err := rasql.Prepare(executor, q)
		require.NoError(t, err)

		var planErr *rasql.PlanError

		var zero rasql.Parameter[int64]
		_, err = prepared.Bind(zero.Value(int64(1)))
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "invalid_parameter", planErr.Code)

		other := rasql.NewParameter[int64]()
		_, err = prepared.Bind(other.Value(int64(1)))
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "unknown_parameter", planErr.Code)

		_, err = prepared.Bind(param.Value(int64(1)), param.Value(int64(2)))
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "duplicate_parameter", planErr.Code)
	})

	t.Run("Bind leaves the receiver unbound", func(t *testing.T) {
		q, param := runtimeParamQuery(t)
		executor, _ := runtimeExecutor(t, [][]any{{int64(1)}})
		prepared, err := rasql.Prepare(executor, q)
		require.NoError(t, err)
		bound, err := prepared.Bind(param.Value(int64(1)))
		require.NoError(t, err)

		_, err = prepared.All(t.Context(), executor)
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "parameter_unbound", planErr.Code)

		values, err := bound.All(t.Context(), executor)
		require.NoError(t, err)
		require.Equal(t, []int64{1}, values)
	})

	t.Run("100 goroutines each bind a different value on one shared Prepared and see their own rows", func(t *testing.T) {
		q, param := runtimeParamQuery(t)
		executor := runtimeEchoExecutorFor(t)
		prepared, err := rasql.Prepare(executor, q)
		require.NoError(t, err)
		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				bound, err := prepared.Bind(param.Value(int64(i)))
				require.NoError(t, err)
				values, err := bound.All(context.Background(), executor)
				require.NoError(t, err)
				require.Equal(t, []int64{int64(i)}, values)
			}(i)
		}
		wg.Wait()
	})

	t.Run("a parameter built with NewParameterWithCodec is encoded through the captured registry", func(t *testing.T) {
		q, param := runtimeParamCodecQuery(t)
		base, raw := runtimeExecutor(t, [][]any{{int64(1)}})
		registryA, err := rasql.NewCodecRegistry(map[rasql.CodecID]rasql.ValueCodec{"age": passthroughParamCodec{}})
		require.NoError(t, err)
		executorA, err := rasql.WithCodecs(base, registryA)
		require.NoError(t, err)

		prepared, err := rasql.Prepare(executorA, q)
		require.NoError(t, err)
		bound, err := prepared.Bind(param.Value(int64(42)))
		require.NoError(t, err)
		values, err := bound.All(t.Context(), executorA)
		require.NoError(t, err)
		require.Equal(t, []int64{1}, values)
		require.Equal(t, []any{int64(42)}, raw.lastStatement.Args())

		registryB, err := rasql.NewCodecRegistry(map[rasql.CodecID]rasql.ValueCodec{"age": passthroughParamCodec{}})
		require.NoError(t, err)
		executorB, err := rasql.WithCodecs(base, registryB)
		require.NoError(t, err)

		_, err = bound.All(t.Context(), executorB)
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "prepared_executor_mismatch", planErr.Code)
	})

	t.Run("package-level All on a query with a parameter reports parameter_unbound", func(t *testing.T) {
		q, _ := runtimeParamQuery(t)
		executor, raw := runtimeExecutor(t, [][]any{{int64(1)}})
		_, err := rasql.All(t.Context(), executor, q)
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "parameter_unbound", planErr.Code)
		require.Equal(t, int64(0), raw.calls.Load())
	})
}
