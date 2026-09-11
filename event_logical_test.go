package rasql_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// A logical invocation groups the statements one call makes, so that an
// observer sees them under one parent with their own index sequence. Within
// opens one, and so does each mutation batch, which is how these cases reach
// the behavior without naming anything the package keeps to itself.

// logicalExecutor returns an executor whose events are appended to a slice the
// caller owns, along with the mutex guarding it.
func logicalExecutor(t *testing.T, observers ...rasql.EventObserver) rasql.Executor {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.ExecContext(t.Context(),
		`CREATE TABLE logical_items (id INTEGER PRIMARY KEY, name INTEGER NOT NULL)`)
	require.NoError(t, err)
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	base, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	if len(observers) == 0 {
		return base
	}
	observed, err := rasql.WithEventObservers(base,
		rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), observers...)
	require.NoError(t, err)
	return observed
}

// recordingObserver collects every event it is shown, start and terminal
// alike, behind its own lock so a concurrent case can read it safely.
type recordingObserver struct {
	mu     sync.Mutex
	events []rasql.Event
}

func (r *recordingObserver) Start(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) {
	r.record(event)
	return ctx, rasql.EventCompletionFunc(func(_ context.Context, terminal rasql.Event) error {
		r.record(terminal)
		return nil
	})
}

func (r *recordingObserver) record(event rasql.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *recordingObserver) snapshot() []rasql.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]rasql.Event(nil), r.events...)
}

func (r *recordingObserver) starts(kind rasql.EventKind) []rasql.Event {
	var result []rasql.Event
	for _, event := range r.snapshot() {
		if event.Phase == rasql.EventStart && event.Kind == kind {
			result = append(result, event)
		}
	}
	return result
}

func TestLogicalInvocation(t *testing.T) {
	t.Run("uses fresh statement indexes and one terminal", func(t *testing.T) {
		observer := &recordingObserver{}
		executor := logicalExecutor(t, observer)
		for range 2 {
			err := rasql.Within(t.Context(), executor, nil, func(ctx context.Context, child rasql.Executor) error {
				for range 3 {
					if _, execErr := child.Exec(ctx, stmt.New("SELECT 1")); execErr != nil {
						return execErr
					}
				}
				return nil
			})
			require.NoError(t, err)
		}

		events := observer.snapshot()
		require.Len(t, events, 16)
		for invocation := range 2 {
			base := invocation * 8
			logical := events[base]
			require.Equal(t, rasql.EventScope, logical.Kind)
			require.Equal(t, rasql.EventStart, logical.Phase)
			for index := range 3 {
				start := events[base+1+index*2]
				terminal := events[base+2+index*2]
				require.Equal(t, rasql.EventStatement, start.Kind)
				require.Equal(t, index, start.StatementIndex)
				require.Equal(t, logical.LogicalID, start.ParentID)
				require.Equal(t, start.LogicalID, terminal.LogicalID)
				require.Equal(t, index, terminal.StatementIndex)
			}
			logicalTerminal := events[base+7]
			require.Equal(t, rasql.EventScope, logicalTerminal.Kind)
			require.Equal(t, rasql.EventTerminal, logicalTerminal.Phase)
			require.Equal(t, logical.LogicalID, logicalTerminal.LogicalID)
		}
	})

	// An executor with no observers skips the invocation bookkeeping entirely.
	// Nothing outside the package can watch that skip happen, because watching
	// requires attaching an observer, which is the very thing being left out.
	// What is left to check is that the work still runs and the scope still
	// commits, which is what would break if the no-op path were wrong.
	t.Run("an executor with no observers still runs the work", func(t *testing.T) {
		executor := logicalExecutor(t)
		_, plan := logicalMutationPlan(t)
		err := rasql.Within(t.Context(), executor, nil, func(ctx context.Context, child rasql.Executor) error {
			_, batchErr := rasql.ExecMutationBatch(ctx, child,
				[]rasql.MutationPlan{plan(1), plan(2)}, rasql.BulkOptions{})
			return batchErr
		})
		require.NoError(t, err)

		rows, err := rasql.All(t.Context(), executor, logicalCountQuery(t))
		require.NoError(t, err)
		require.Equal(t, []int64{2}, rows)
	})

	t.Run("concurrent invocations use independent counters", func(t *testing.T) {
		observer := &recordingObserver{}
		// Each goroutine needs its own database, because an in-memory SQLite
		// handle serializes transactions and would turn this into two runs in
		// sequence rather than two at once.
		executors := []rasql.Executor{logicalExecutor(t, observer), logicalExecutor(t, observer)}
		var wg sync.WaitGroup
		for _, executor := range executors {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = rasql.Within(t.Context(), executor, nil, func(ctx context.Context, child rasql.Executor) error {
					_, _ = child.Exec(ctx, stmt.New("SELECT 1"))
					_, _ = child.Exec(ctx, stmt.New("SELECT 1"))
					return nil
				})
			}()
		}
		wg.Wait()

		indexes := make(map[string][]int)
		for _, event := range observer.starts(rasql.EventStatement) {
			indexes[event.ParentID] = append(indexes[event.ParentID], event.StatementIndex)
		}
		require.Len(t, indexes, 2)
		for _, values := range indexes {
			require.Equal(t, []int{0, 1}, values)
		}
	})

	t.Run("a nested invocation uses the outer parent", func(t *testing.T) {
		observer := &recordingObserver{}
		executor := logicalExecutor(t, observer)
		_, plan := logicalMutationPlan(t)
		err := rasql.Within(t.Context(), executor, nil, func(ctx context.Context, child rasql.Executor) error {
			_, batchErr := rasql.ExecMutationBatch(ctx, child,
				[]rasql.MutationPlan{plan(1)}, rasql.BulkOptions{})
			return batchErr
		})
		require.NoError(t, err)

		var starts []rasql.Event
		for _, event := range observer.snapshot() {
			if event.Phase == rasql.EventStart {
				starts = append(starts, event)
			}
		}
		require.Len(t, starts, 3)
		require.Equal(t, rasql.EventScope, starts[0].Kind)
		require.Equal(t, rasql.EventMutationBatch, starts[1].Kind)
		require.Equal(t, rasql.EventStatement, starts[2].Kind)
		require.Equal(t, starts[0].LogicalID, starts[1].ParentID)
		require.Equal(t, starts[1].LogicalID, starts[2].ParentID)
	})

	t.Run("nested invocations have independent indexes", func(t *testing.T) {
		observer := &recordingObserver{}
		executor := logicalExecutor(t, observer)
		_, plan := logicalMutationPlan(t)
		err := rasql.Within(t.Context(), executor, nil, func(ctx context.Context, child rasql.Executor) error {
			if _, execErr := child.Exec(ctx, stmt.New("SELECT 1")); execErr != nil {
				return execErr
			}
			if _, batchErr := rasql.ExecMutationBatch(ctx, child,
				[]rasql.MutationPlan{plan(1), plan(2)}, rasql.BulkOptions{MaxRows: 1}); batchErr != nil {
				return batchErr
			}
			_, execErr := child.Exec(ctx, stmt.New("SELECT 1"))
			return execErr
		})
		require.NoError(t, err)

		starts := observer.starts(rasql.EventStatement)
		require.Len(t, starts, 4)
		require.Equal(t, []int{0, 0, 1, 1},
			[]int{starts[0].StatementIndex, starts[1].StatementIndex, starts[2].StatementIndex, starts[3].StatementIndex})
		require.Equal(t, starts[0].ParentID, starts[3].ParentID, "the outer scope keeps counting its own statements")
		require.NotEqual(t, starts[0].ParentID, starts[1].ParentID, "the nested batch counts its own")
	})

	t.Run("chains contexts and reverses completions", func(t *testing.T) {
		type key string
		var mu sync.Mutex
		var order []string
		first := rasql.EventObserverFunc(func(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) {
			return context.WithValue(ctx, key("first"), true), rasql.EventCompletionFunc(func(ctx context.Context, _ rasql.Event) error {
				require.True(t, ctx.Value(key("second")).(bool))
				mu.Lock()
				order = append(order, "first")
				mu.Unlock()
				return nil
			})
		})
		second := rasql.EventObserverFunc(func(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) {
			require.True(t, ctx.Value(key("first")).(bool))
			return context.WithValue(ctx, key("second"), true), rasql.EventCompletionFunc(func(context.Context, rasql.Event) error {
				mu.Lock()
				order = append(order, "second")
				mu.Unlock()
				return nil
			})
		})

		executor := logicalExecutor(t, first, second)
		err := rasql.Within(t.Context(), executor, nil, func(ctx context.Context, child rasql.Executor) error {
			_, execErr := child.Exec(ctx, stmt.New("SELECT 1"))
			return execErr
		})
		require.NoError(t, err)

		mu.Lock()
		defer mu.Unlock()
		require.Equal(t, []string{"second", "first", "second", "first"}, order,
			"each completion runs in reverse registration order, once per invocation")
	})

	t.Run("an observer failure never changes the application result", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		database.SetMaxOpenConns(1)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		base, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)

		var handled int
		executor, err := rasql.WithEventObservers(base,
			rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) { handled++ }),
			rasql.EventObserverFunc(func(context.Context, rasql.Event) (context.Context, rasql.EventCompletion) {
				panic("observer start panicked")
			}),
			rasql.EventObserverFunc(func(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) {
				return ctx, rasql.EventCompletionFunc(func(context.Context, rasql.Event) error {
					panic("observer completion panicked")
				})
			}),
		)
		require.NoError(t, err)

		err = rasql.Within(t.Context(), executor, nil, func(ctx context.Context, child rasql.Executor) error {
			_, execErr := child.Exec(ctx, stmt.New("SELECT 1"))
			return execErr
		})
		require.NoError(t, err, "a panicking observer never reaches the caller's result")
		require.NotZero(t, handled, "the extension error handler sees it instead")
	})
}

type logicalRow struct{ ID, Name int64 }

// logicalMutationPlan returns the logical_items table that logicalExecutor
// creates, and a builder for one insert against it.
func logicalMutationPlan(t *testing.T) (rasql.Table[logicalRow], func(int64) rasql.MutationPlan) {
	t.Helper()
	table, err := rasql.TableOf[logicalRow](schema.TableDef{
		Name: "logical_items", PrimaryKey: []string{"id"},
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "name", Type: schema.IntegerType{}},
		},
	})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "")
	require.NoError(t, err)
	id, err := rasql.BindColumn[logicalRow, int64](relation, "id", "")
	require.NoError(t, err)
	name, err := rasql.BindColumn[logicalRow, int64](relation, "name", "")
	require.NoError(t, err)
	return table, func(n int64) rasql.MutationPlan {
		plan, planErr := rasql.NewCreatePlan(table, rasql.SetField(id, n), rasql.SetField(name, n))
		require.NoError(t, planErr)
		return plan
	}
}

// logicalCountQuery counts the rows logical_items holds, so a case with no
// observers can still show its work reached the database.
func logicalCountQuery(t *testing.T) rasql.Query[int64] {
	t.Helper()
	table, err := rasql.ReadTableOf[logicalRow](schema.TableDef{
		Name: "logical_items",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "name", Type: schema.IntegerType{}},
		},
	})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "")
	require.NoError(t, err)
	projection, err := rasql.Scalar("total", rasql.CountRows(), schema.IntegerType{}, "")
	require.NoError(t, err)
	return rasql.Select(relation.Source(), projection)
}
