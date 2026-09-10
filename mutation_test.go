package rasql_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type mutationRow struct{ ID int64 }

type mutationDecoder struct{ schema rasql.ResultSchema }

type mutationRejectClassifier struct{}

func (mutationRejectClassifier) Certainty(error) rasql.FailureCertainty { return rasql.OutcomeRejected }

func (d mutationDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (d mutationDecoder) Presence() []rasql.Presence       { return nil }
func (d mutationDecoder) DecodeRow(source rasql.ScanSource, row *mutationRow) error {
	return source.Scan(&row.ID)
}

func mutationFixture(t *testing.T) (rasql.Executor, rasql.Table[mutationRow], query.TypedColumn[mutationRow, int64]) {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.ExecContext(t.Context(), `CREATE TABLE items (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`)
	require.NoError(t, err)
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `ALTER TABLE items ADD COLUMN version INTEGER NOT NULL DEFAULT 1`)
	require.NoError(t, err)
	table, err := rasql.TableOf[mutationRow](schema.TableDef{
		Name: "items", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "value", Type: schema.TextType{}}, {Name: "version", Type: schema.IntegerType{}, Default: "1"}}, PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 40, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	return executor, table, query.TypedColumnOf[mutationRow, int64](table.Column("id"))
}

func TestMutation(t *testing.T) {
	t.Run("an optimistic version", func(t *testing.T) {
		executor, table, id := mutationFixture(t)
		value := query.TypedColumnOf[mutationRow, string](table.Column("value"))
		relation, err := rasql.SourceOf[mutationRow](table, "")
		require.NoError(t, err)
		version, err := rasql.BindColumn[mutationRow, int64](relation, "version", "")
		require.NoError(t, err)
		create, err := rasql.NewCreatePlan(table, rasql.SetField(id, int64(10)), rasql.SetField(value, "before"))
		require.NoError(t, err)
		_, err = rasql.ExecMutation(t.Context(), executor, create)
		require.NoError(t, err)
		patch, err := rasql.NewPatchPlan(table, query.EqualValue(id, int64(10)), rasql.SetField(value, "after"))
		require.NoError(t, err)
		versioned, err := patch.WithVersion(version, 1)
		require.NoError(t, err)
		outcome, err := rasql.ExecMutation(t.Context(), executor, versioned)
		require.NoError(t, err)
		require.Equal(t, int64(1), outcome.Affected)
		outcome, err = rasql.ExecMutation(t.Context(), executor, versioned)
		require.ErrorIs(t, err, rasql.ErrPrecondition)
		require.Zero(t, outcome.Affected)
	})

	t.Run("RETURNING and outcomes", func(t *testing.T) {
		executor, table, id := mutationFixture(t)
		value := query.TypedColumnOf[mutationRow, string](table.Column("value"))
		create, err := rasql.NewCreatePlan(table, rasql.SetField(id, int64(7)), rasql.SetField(value, "before"))
		require.NoError(t, err)
		outcome, err := rasql.ExecMutation(t.Context(), executor, create)
		require.NoError(t, err)
		require.Equal(t, int64(1), outcome.Affected)
		require.Equal(t, rasql.DurabilityCommitted, outcome.Durability)

		projection := mutationProjection(t, table)
		patch, err := rasql.NewPatchPlan(table, query.EqualValue(id, int64(7)), rasql.SetField(value, "after"))
		require.NoError(t, err)
		returned, err := rasql.Returning(patch, projection)
		require.NoError(t, err)
		rows, err := rasql.All(t.Context(), executor, returned)
		require.NoError(t, err)
		require.Equal(t, []mutationRow{{ID: 7}}, rows)

		deletePlan, err := rasql.NewDeletePlan(table, query.EqualValue(id, int64(7)))
		require.NoError(t, err)
		deleteOutcome, err := rasql.ExecMutation(t.Context(), executor, deletePlan)
		require.NoError(t, err)
		require.Equal(t, int64(1), deleteOutcome.Affected)
	})

	t.Run("four states and RETURNING on SQLite", func(t *testing.T) {
		f := newMutationAcceptanceFixture(t)
		projection := mutationAcceptanceProjection(t, f.table)
		create, err := rasql.NewCreatePlan(f.table, rasql.SetField(f.required, "omitted"))
		require.NoError(t, err)
		returned, err := rasql.Returning(create, projection)
		require.NoError(t, err)
		got, err := rasql.One(t.Context(), f.executor, returned)
		require.NoError(t, err)
		require.Equal(t, mutationAcceptanceItem{ID: 1, RequiredText: "omitted", ZeroNumber: 41, DefaultText: "db-default", Version: 1, GeneratedText: "omitted:1"}, got)

		create, err = rasql.NewCreatePlan(f.table, rasql.SetField(f.required, "states"), rasql.SetField(f.zero, int64(0)), rasql.ClearField(f.nullable), rasql.DefaultField(f.defaults), rasql.DefaultField(f.version))
		require.NoError(t, err)
		returned, err = rasql.Returning(create, projection)
		require.NoError(t, err)
		values, err := rasql.All(t.Context(), f.executor, returned)
		require.NoError(t, err)
		require.Len(t, values, 1)
		require.Equal(t, int64(2), values[0].ID)
		require.Equal(t, int64(0), values[0].ZeroNumber)
		require.False(t, values[0].NullableText.Valid)
		require.Equal(t, "db-default", values[0].DefaultText)

		patch, err := rasql.NewPatchPlan(f.table, query.EqualValue(f.id, int64(2)), rasql.SetField(f.zero, int64(7)), rasql.SetNullableField(f.nullable, "present"), rasql.SetField(f.defaults, "explicit"))
		require.NoError(t, err)
		returned, err = rasql.Returning(patch, projection)
		require.NoError(t, err)
		got, ok, err := rasql.Maybe(t.Context(), f.executor, returned)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, "present", got.NullableText.String)
		require.Equal(t, int64(7), got.ZeroNumber)

		patch, err = rasql.NewPatchPlan(f.table, query.EqualValue(f.id, int64(2)), rasql.ClearField(f.nullable))
		require.NoError(t, err)
		returned, err = rasql.Returning(patch, projection)
		require.NoError(t, err)
		got, err = rasql.One(t.Context(), f.executor, returned)
		require.NoError(t, err)
		require.False(t, got.NullableText.Valid)

		deletePlan, err := rasql.NewDeletePlan(f.table, query.EqualValue(f.id, int64(1)))
		require.NoError(t, err)
		returned, err = rasql.Returning(deletePlan, projection)
		require.NoError(t, err)
		got, err = rasql.One(t.Context(), f.executor, returned)
		require.NoError(t, err)
		require.Equal(t, int64(1), got.ID)
		var count int
		require.NoError(t, f.database.QueryRowContext(t.Context(), "SELECT count(*) FROM mutation_items WHERE id = 1").Scan(&count))
		require.Zero(t, count)

		create, err = rasql.NewCreatePlan(f.table, rasql.SetField(f.required, "non-returning"))
		require.NoError(t, err)
		outcome, err := rasql.ExecMutation(t.Context(), f.executor, create)
		require.NoError(t, err)
		require.Equal(t, int64(1), outcome.Affected)
		require.Equal(t, rasql.DurabilityCommitted, outcome.Durability)
	})

	t.Run("transaction durability stays pending", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		database.SetMaxOpenConns(1)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		_, err = database.ExecContext(t.Context(), `CREATE TABLE items (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`)
		require.NoError(t, err)
		tx, err := database.BeginTx(t.Context(), nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = tx.Rollback() })
		db, err := rasql.New(tx, dialect.SQLite())
		require.NoError(t, err)
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 40, 0)
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		table, err := rasql.TableOf[mutationRow](schema.TableDef{Name: "items", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "value", Type: schema.TextType{}}}})
		require.NoError(t, err)
		id := query.TypedColumnOf[mutationRow, int64](table.Column("id"))
		value := query.TypedColumnOf[mutationRow, string](table.Column("value"))
		plan, err := rasql.NewCreatePlan(table, rasql.SetField(id, int64(1)), rasql.SetField(value, "pending"))
		require.NoError(t, err)
		outcome, err := rasql.ExecMutation(t.Context(), executor, plan)
		require.NoError(t, err)
		require.Equal(t, rasql.DurabilityPending, outcome.Durability)
	})

	t.Run("result states and the precondition sentinel", func(t *testing.T) {
		states := []rasql.InputOutcome{
			rasql.InputUnattempted,
			rasql.InputApplied,
			rasql.InputRolledBack,
			rasql.InputRejected,
			rasql.InputUnknown,
		}
		seen := make(map[rasql.InputOutcome]struct{}, len(states))
		for _, state := range states {
			_, duplicate := seen[state]
			require.False(t, duplicate)
			seen[state] = struct{}{}
		}
		require.ErrorIs(t, rasql.ErrPrecondition, rasql.ErrPrecondition)
		require.False(t, errors.Is(errors.New("other"), rasql.ErrPrecondition))
	})
}

func mutationProjection(t *testing.T, table rasql.Table[mutationRow]) rasql.Projection[mutationRow] {
	t.Helper()
	schemaValue, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	returnValue := mutationDecoder{schema: schemaValue}
	relation, err := rasql.SourceOf[mutationRow](table, "")
	require.NoError(t, err)
	id, err := rasql.BindColumn[mutationRow, int64](relation, "id", "")
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", id.Expr(), schema.IntegerType{}, "")}, returnValue)
	require.NoError(t, err)
	return projection
}

func TestMutationBatch(t *testing.T) {
	t.Run("groups compatible creates", func(t *testing.T) {
		executor, table, id := mutationFixture(t)
		value := query.TypedColumnOf[mutationRow, string](table.Column("value"))
		plans := make([]rasql.MutationPlan, 0, 6)
		for i := int64(1); i <= 6; i++ {
			plan, err := rasql.NewCreatePlan(table, rasql.SetField(id, i), rasql.SetField(value, "v"))
			require.NoError(t, err)
			plans = append(plans, plan)
		}
		outcome, err := rasql.ExecMutationBatch(t.Context(), executor, plans, rasql.BulkOptions{MaxRows: 3})
		require.NoError(t, err)
		require.Equal(t, []rasql.InputOutcome{rasql.InputApplied, rasql.InputApplied, rasql.InputApplied, rasql.InputApplied, rasql.InputApplied, rasql.InputApplied}, outcome.Inputs)
	})

	t.Run("emits a logical invocation", func(t *testing.T) {
		executor, table, id := mutationFixture(t)
		value := query.TypedColumnOf[mutationRow, string](table.Column("value"))
		plans := make([]rasql.MutationPlan, 0, 6)
		for i := int64(1); i <= 6; i++ {
			plan, err := rasql.NewCreatePlan(table, rasql.SetField(id, i), rasql.SetField(value, "event"))
			require.NoError(t, err)
			plans = append(plans, plan)
		}
		var mu sync.Mutex
		var events []rasql.Event
		executor, err := rasql.WithEventObservers(executor, rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.EventObserverFunc(func(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
			return ctx, rasql.EventCompletionFunc(func(_ context.Context, terminal rasql.Event) error {
				mu.Lock()
				events = append(events, terminal)
				mu.Unlock()
				return nil
			})
		}))
		require.NoError(t, err)
		_, err = rasql.ExecMutationBatch(t.Context(), executor, plans, rasql.BulkOptions{MaxRows: 3})
		require.NoError(t, err)
		mu.Lock()
		defer mu.Unlock()
		require.Len(t, events, 6)
		require.Equal(t, rasql.EventMutationBatch, events[0].Kind)
		require.Equal(t, rasql.EventStart, events[0].Phase)
		require.Equal(t, rasql.EventStatement, events[1].Kind)
		require.Equal(t, events[0].LogicalID, events[1].ParentID)
		require.Equal(t, 0, events[1].StatementIndex)
		require.Equal(t, rasql.EventStatement, events[3].Kind)
		require.Equal(t, events[0].LogicalID, events[3].ParentID)
		require.Equal(t, 1, events[3].StatementIndex)
		require.Equal(t, rasql.EventMutationBatch, events[5].Kind)
		require.Equal(t, rasql.EventTerminal, events[5].Phase)
	})

	t.Run("reports a rejected batch and unattempted inputs", func(t *testing.T) {
		executor, table, id := mutationFixture(t)
		value := query.TypedColumnOf[mutationRow, string](table.Column("value"))
		plans := make([]rasql.MutationPlan, 0, 6)
		for i := int64(1); i <= 6; i++ {
			key := i
			if i == 5 {
				key = 1
			}
			plan, err := rasql.NewCreatePlan(table, rasql.SetField(id, key), rasql.SetField(value, "v"))
			require.NoError(t, err)
			plans = append(plans, plan)
		}
		outcome, err := rasql.ExecMutationBatch(t.Context(), executor, plans, rasql.BulkOptions{MaxRows: 3, Classifier: mutationRejectClassifier{}})
		require.Error(t, err)
		require.Equal(t, []rasql.InputOutcome{
			rasql.InputApplied, rasql.InputApplied, rasql.InputApplied,
			rasql.InputRejected, rasql.InputRejected, rasql.InputRejected,
		}, outcome.Inputs)
		require.Equal(t, []int{3, 4, 5}, outcome.FailedBatch)
	})

	t.Run("cancellation before execution", func(t *testing.T) {
		executor, table, id := mutationFixture(t)
		value := query.TypedColumnOf[mutationRow, string](table.Column("value"))
		plan, err := rasql.NewCreatePlan(table, rasql.SetField(id, int64(1)), rasql.SetField(value, "v"))
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		outcome, err := rasql.ExecMutationBatch(ctx, executor, []rasql.MutationPlan{plan}, rasql.BulkOptions{})
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, []rasql.InputOutcome{rasql.InputUnattempted}, outcome.Inputs)
	})

	t.Run("atomic rollback states", func(t *testing.T) {
		executor, table, id := mutationFixture(t)
		value := query.TypedColumnOf[mutationRow, string](table.Column("value"))
		first, err := rasql.NewCreatePlan(table, rasql.SetField(id, int64(1)), rasql.SetField(value, "one"))
		require.NoError(t, err)
		second, err := rasql.NewCreatePlan(table, rasql.SetField(id, int64(1)), rasql.SetField(value, "duplicate"))
		require.NoError(t, err)
		outcome, err := rasql.ExecMutationBatch(t.Context(), executor, []rasql.MutationPlan{first, second}, rasql.BulkOptions{
			MaxRows: 1, Atomic: true, Classifier: mutationRejectClassifier{},
		})
		require.Error(t, err)
		require.Equal(t, []rasql.InputOutcome{rasql.InputRolledBack, rasql.InputRejected}, outcome.Inputs)
		require.Equal(t, rasql.DurabilityPending, outcome.Durability)
	})

	t.Run("a caller limit splits candidate overflow", func(t *testing.T) {
		f := newMutationAcceptanceFixture(t)
		plans := make([]rasql.MutationPlan, 0, 2)
		for _, text := range []string{"first", "second"} {
			plan, planErr := rasql.NewCreatePlan(f.table, rasql.SetField(f.required, text))
			require.NoError(t, planErr)
			plans = append(plans, plan)
		}
		outcome, err := rasql.ExecMutationBatch(t.Context(), f.executor, plans, rasql.BulkOptions{MaxRows: 2, MaxBindParameters: 1})
		require.NoError(t, err)
		require.Equal(t, []rasql.InputOutcome{rasql.InputApplied, rasql.InputApplied}, outcome.Inputs)
	})
}

type mutationAcceptanceItem struct {
	ID            int64
	RequiredText  string
	ZeroNumber    int64
	NullableText  sql.NullString
	DefaultText   string
	Version       int64
	GeneratedText string
}

type mutationAcceptanceDecoder struct{ result rasql.ResultSchema }

func (d mutationAcceptanceDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (mutationAcceptanceDecoder) Presence() []rasql.Presence         { return nil }
func (mutationAcceptanceDecoder) DecodeRow(source rasql.ScanSource, item *mutationAcceptanceItem) error {
	return source.Scan(&item.ID, &item.RequiredText, &item.ZeroNumber, &item.NullableText,
		&item.DefaultText, &item.Version, &item.GeneratedText)
}

type mutationAcceptanceFixture struct {
	database *sql.DB
	executor rasql.Executor
	table    rasql.Table[mutationAcceptanceItem]
	id       query.TypedColumn[mutationAcceptanceItem, int64]
	required query.TypedColumn[mutationAcceptanceItem, string]
	zero     query.TypedColumn[mutationAcceptanceItem, int64]
	nullable query.NullableColumn[mutationAcceptanceItem, string]
	defaults query.TypedColumn[mutationAcceptanceItem, string]
	version  query.TypedColumn[mutationAcceptanceItem, int64]
}

func newMutationAcceptanceFixture(t *testing.T) mutationAcceptanceFixture {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.ExecContext(t.Context(), `CREATE TABLE mutation_items (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		required_text TEXT NOT NULL,
		zero_number INTEGER NOT NULL DEFAULT 41,
		nullable_text TEXT NULL,
		default_text TEXT NOT NULL DEFAULT 'db-default',
		version INTEGER NOT NULL DEFAULT 1,
		generated_text TEXT GENERATED ALWAYS AS (required_text || ':' || version) STORED,
		UNIQUE(required_text)
	)`)
	require.NoError(t, err)
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 40, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	table := rasql.MustTableOf[mutationAcceptanceItem](schema.TableDef{
		Name: "mutation_items", PrimaryKey: []string{"id"},
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}, Identity: schema.IdentityAlways},
			{Name: "required_text", Type: schema.TextType{}},
			{Name: "zero_number", Type: schema.IntegerType{}, Default: "41"},
			{Name: "nullable_text", Type: schema.TextType{}, Nullable: true},
			{Name: "default_text", Type: schema.TextType{}, Default: "'db-default'"},
			{Name: "version", Type: schema.IntegerType{}, Default: "1"},
			{Name: "generated_text", Type: schema.TextType{}, GeneratedExpression: "required_text || ':' || version", GeneratedStorage: schema.GeneratedStored},
		},
	})
	return mutationAcceptanceFixture{database: database, executor: executor, table: table,
		id:       query.TypedColumnOf[mutationAcceptanceItem, int64](table.Column("id")),
		required: query.TypedColumnOf[mutationAcceptanceItem, string](table.Column("required_text")),
		zero:     query.TypedColumnOf[mutationAcceptanceItem, int64](table.Column("zero_number")),
		nullable: query.NullableColumnOf[mutationAcceptanceItem, string](table.Column("nullable_text")),
		defaults: query.TypedColumnOf[mutationAcceptanceItem, string](table.Column("default_text")),
		version:  query.TypedColumnOf[mutationAcceptanceItem, int64](table.Column("version"))}
}

func mutationAcceptanceProjection(t *testing.T, table rasql.Table[mutationAcceptanceItem]) rasql.Projection[mutationAcceptanceItem] {
	t.Helper()
	relation, err := rasql.SourceOf[mutationAcceptanceItem](table, "")
	require.NoError(t, err)
	id, err := rasql.BindColumn[mutationAcceptanceItem, int64](relation, "id", "")
	require.NoError(t, err)
	required, err := rasql.BindColumn[mutationAcceptanceItem, string](relation, "required_text", "")
	require.NoError(t, err)
	zero, err := rasql.BindColumn[mutationAcceptanceItem, int64](relation, "zero_number", "")
	require.NoError(t, err)
	nullable, err := rasql.BindNullColumn[mutationAcceptanceItem, string](relation, "nullable_text", "")
	require.NoError(t, err)
	defaults, err := rasql.BindColumn[mutationAcceptanceItem, string](relation, "default_text", "")
	require.NoError(t, err)
	version, err := rasql.BindColumn[mutationAcceptanceItem, int64](relation, "version", "")
	require.NoError(t, err)
	generated, err := rasql.BindColumn[mutationAcceptanceItem, string](relation, "generated_text", "")
	require.NoError(t, err)
	result, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "required_text", Type: schema.TextType{}},
		rasql.ResultColumn{Name: "zero_number", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "nullable_text", Type: schema.TextType{}, Nullable: true},
		rasql.ResultColumn{Name: "default_text", Type: schema.TextType{}},
		rasql.ResultColumn{Name: "version", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "generated_text", Type: schema.TextType{}},
	)
	require.NoError(t, err)
	items := []rasql.ProjectionItem{
		rasql.Item("id", id.Expr(), schema.IntegerType{}, ""), rasql.Item("required_text", required.Expr(), schema.TextType{}, ""),
		rasql.Item("zero_number", zero.Expr(), schema.IntegerType{}, ""), rasql.NullItem("nullable_text", nullable.NullExpr(), schema.TextType{}, ""),
		rasql.Item("default_text", defaults.Expr(), schema.TextType{}, ""), rasql.Item("version", version.Expr(), schema.IntegerType{}, ""),
		rasql.Item("generated_text", generated.Expr(), schema.TextType{}, ""),
	}
	returnValue, err := rasql.NewProjection(items, mutationAcceptanceDecoder{result: result})
	require.NoError(t, err)
	return returnValue
}

func TestMutationAtomic(t *testing.T) {
	t.Run("the commit and rollback lifecycle", func(t *testing.T) {
		for _, test := range []struct {
			name    string
			failure bool
		}{
			{name: "commit"}, {name: "rollback", failure: true},
		} {
			t.Run(test.name, func(t *testing.T) {
				executor, table, id := mutationFixture(t)
				value := queryTypedMutationValue(table)
				first, err := rasql.NewCreatePlan(table, rasql.SetField(id, int64(1)), rasql.SetField(value, "one"))
				require.NoError(t, err)
				plans := []rasql.MutationPlan{first}
				if test.failure {
					second, secondErr := rasql.NewCreatePlan(table, rasql.SetField(id, int64(1)), rasql.SetField(value, "duplicate"))
					require.NoError(t, secondErr)
					plans = append(plans, second)
				}
				var mu sync.Mutex
				events := make([]rasql.Event, 0, 8)
				executor, err = rasql.WithEventObservers(executor, rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.EventObserverFunc(func(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) {
					mu.Lock()
					events = append(events, event)
					mu.Unlock()
					return ctx, rasql.EventCompletionFunc(func(_ context.Context, terminal rasql.Event) error {
						mu.Lock()
						events = append(events, terminal)
						mu.Unlock()
						return nil
					})
				}))
				require.NoError(t, err)
				outcome, executionErr := rasql.ExecMutationBatch(t.Context(), executor, plans, rasql.BulkOptions{Atomic: true, MaxRows: 1, Classifier: mutationRejectClassifier{}})
				if test.failure {
					require.Error(t, executionErr)
					require.Equal(t, []rasql.InputOutcome{rasql.InputRolledBack, rasql.InputRejected}, outcome.Inputs)
				} else {
					require.NoError(t, executionErr)
					require.Equal(t, []rasql.InputOutcome{rasql.InputApplied}, outcome.Inputs)
				}
				mu.Lock()
				defer mu.Unlock()
				require.NotEmpty(t, events)
				var scopeTerminal, mutationTerminal int
				for index, event := range events {
					if event.Phase != rasql.EventTerminal {
						continue
					}
					if event.Kind == rasql.EventScope {
						scopeTerminal = index
					}
					if event.Kind == rasql.EventMutationBatch {
						mutationTerminal = index
					}
				}
				require.Greater(t, mutationTerminal, scopeTerminal)
			})
		}
	})

	t.Run("preflight rejects negative limits without executing", func(t *testing.T) {
		executor, table, id := mutationFixture(t)
		value := queryTypedMutationValue(table)
		plan, err := rasql.NewCreatePlan(table, rasql.SetField(id, int64(1)), rasql.SetField(value, "one"))
		require.NoError(t, err)
		for _, options := range []rasql.BulkOptions{{Atomic: true, MaxRows: -1}, {Atomic: true, MaxBindParameters: -1}} {
			outcome, executionErr := rasql.ExecMutationBatch(t.Context(), executor, []rasql.MutationPlan{plan}, options)
			require.Error(t, executionErr)
			require.Equal(t, []rasql.InputOutcome{rasql.InputUnattempted}, outcome.Inputs)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		outcome, executionErr := rasql.ExecMutationBatch(ctx, executor, []rasql.MutationPlan{plan}, rasql.BulkOptions{Atomic: true})
		require.ErrorIs(t, executionErr, context.Canceled)
		require.Equal(t, []rasql.InputOutcome{rasql.InputUnattempted}, outcome.Inputs)
	})
}

func queryTypedMutationValue(table rasql.Table[mutationRow]) query.TypedColumn[mutationRow, string] {
	return query.TypedColumnOf[mutationRow, string](table.Column("value"))
}

type mutationCodec struct{ enc *int }

func (c mutationCodec) Encode(value any) (driver.Value, error) {
	*c.enc++
	return "encoded:" + value.(string), nil
}
func (mutationCodec) Decode(source any, destination any) error {
	*destination.(*string) = source.(string)
	return nil
}

func TestMutationCodec(t *testing.T) {
	t.Run("a column codec encodes each occurrence", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		database.SetMaxOpenConns(1)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		_, err = database.ExecContext(t.Context(), "CREATE TABLE codec_items (id INTEGER PRIMARY KEY, value TEXT NOT NULL, nullable TEXT NULL)")
		require.NoError(t, err)
		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 40, 0)
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		count := 0
		registry, err := rasql.NewCodecRegistry(map[rasql.CodecID]rasql.ValueCodec{"prefix": mutationCodec{enc: &count}})
		require.NoError(t, err)
		executor, err = rasql.WithCodecs(executor, registry)
		require.NoError(t, err)
		table := rasql.MustTableOf[mutationCodecRow](schema.TableDef{
			Name: "codec_items", PrimaryKey: []string{"id"},
			Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "value", Type: schema.TextType{}}, {Name: "nullable", Type: schema.TextType{}, Nullable: true}},
		})
		relation, err := rasql.SourceOf[mutationCodecRow](table, "")
		require.NoError(t, err)
		id, err := rasql.BindColumn[mutationCodecRow, int64](relation, "id", "")
		require.NoError(t, err)
		value, err := rasql.BindColumn[mutationCodecRow, string](relation, "value", "prefix")
		require.NoError(t, err)
		nullable, err := rasql.BindNullColumn[mutationCodecRow, string](relation, "nullable", "prefix")
		require.NoError(t, err)
		create, err := rasql.NewCreatePlan(table, rasql.SetField(id, int64(1)), rasql.SetField(value, "one"), rasql.ClearField(nullable))
		require.NoError(t, err)
		_, err = rasql.ExecMutation(t.Context(), executor, create)
		require.NoError(t, err)
		require.Equal(t, 1, count)
		var stored string
		require.NoError(t, database.QueryRowContext(t.Context(), "SELECT value FROM codec_items WHERE id = 1").Scan(&stored))
		require.Equal(t, "encoded:one", stored)

		patch, err := rasql.NewPatchPlan(table, query.EqualValue(query.TypedColumnOf[mutationCodecRow, int64](table.Column("id")), int64(1)), rasql.SetField(value, "two"), rasql.ClearField(nullable))
		require.NoError(t, err)
		_, err = rasql.ExecMutation(t.Context(), executor, patch)
		require.NoError(t, err)
		require.Equal(t, 2, count)
	})

	t.Run("a missing codec fails before execution", func(t *testing.T) {
		executor, table, id := mutationFixture(t)
		relation, err := rasql.SourceOf[mutationRow](table, "")
		require.NoError(t, err)
		value, err := rasql.BindColumn[mutationRow, string](relation, "value", "missing")
		require.NoError(t, err)
		plan, err := rasql.NewCreatePlan(table, rasql.SetField(id, int64(40)), rasql.SetField(value, "value"))
		require.NoError(t, err)
		registry, err := rasql.NewCodecRegistry(map[rasql.CodecID]rasql.ValueCodec{})
		require.NoError(t, err)
		executor, err = rasql.WithCodecs(executor, registry)
		require.NoError(t, err)
		_, err = rasql.ExecMutation(context.Background(), executor, plan)
		require.Error(t, err)
	})
}

type mutationCodecRow struct{}

func TestMutationVersioned(t *testing.T) {
	t.Run("the RETURNING cardinality matrix", func(t *testing.T) {
		for _, test := range []struct {
			name string
			rows int
		}{
			{name: "zero", rows: 0}, {name: "one", rows: 1}, {name: "two", rows: 2},
		} {
			for _, terminal := range []string{"Rows", "All", "One", "Maybe"} {
				t.Run(test.name+"/"+terminal, func(t *testing.T) {
					executor, returned := newVersionedReturningAcceptance(t, test.rows)
					switch terminal {
					case "Rows":
						sequence, err := rasql.Rows(t.Context(), executor, returned)
						require.NoError(t, err)
						seen := 0
						var terminalErr error
						for item, itemErr := range sequence {
							if itemErr != nil {
								terminalErr = itemErr
								break
							}
							seen++
							_ = item
							//nolint:staticcheck // the explicit cardinality cases mirror the acceptance matrix.
							if test.rows == 1 {
								break
							}
						}
						expectedSeen := 0
						if test.rows == 1 {
							expectedSeen = 1
						}
						require.Equal(t, expectedSeen, seen)
						//nolint:staticcheck // the explicit cardinality cases mirror the acceptance matrix.
						if test.rows == 0 {
							require.ErrorIs(t, terminalErr, rasql.ErrPrecondition)
						} else if test.rows == 2 {
							require.ErrorIs(t, terminalErr, rasql.ErrMultipleRows)
						} else {
							require.NoError(t, terminalErr)
						}
					case "All":
						values, err := rasql.All(t.Context(), executor, returned)
						//nolint:staticcheck // the explicit cardinality cases mirror the acceptance matrix.
						if test.rows == 0 {
							require.ErrorIs(t, err, rasql.ErrPrecondition)
							require.Empty(t, values)
						} else if test.rows == 2 {
							require.ErrorIs(t, err, rasql.ErrMultipleRows)
							require.Empty(t, values)
						} else {
							require.NoError(t, err)
							require.Len(t, values, 1)
						}
					case "One":
						value, err := rasql.One(t.Context(), executor, returned)
						//nolint:staticcheck // the explicit cardinality cases mirror the acceptance matrix.
						if test.rows == 0 {
							require.ErrorIs(t, err, rasql.ErrPrecondition)
						} else if test.rows == 2 {
							require.ErrorIs(t, err, rasql.ErrMultipleRows)
						} else {
							require.NoError(t, err)
							require.Equal(t, int64(1), value.ID)
						}
					case "Maybe":
						value, ok, err := rasql.Maybe(t.Context(), executor, returned)
						//nolint:staticcheck // the explicit cardinality cases mirror the acceptance matrix.
						if test.rows == 0 {
							require.ErrorIs(t, err, rasql.ErrPrecondition)
							require.False(t, ok)
						} else if test.rows == 2 {
							require.ErrorIs(t, err, rasql.ErrMultipleRows)
							require.False(t, ok)
						} else {
							require.NoError(t, err)
							require.True(t, ok)
							require.Equal(t, int64(1), value.ID)
						}
					}
				})
			}
		}
	})

	t.Run("a patch canonicalizes an aliased version column", func(t *testing.T) {
		executor, table, id := mutationFixture(t)
		value := query.TypedColumnOf[mutationRow, string](table.Column("value"))
		relation, err := rasql.SourceOf[mutationRow](table, "vsrc")
		require.NoError(t, err)
		version, err := rasql.BindColumn[mutationRow, int64](relation, "version", "")
		require.NoError(t, err)
		create, err := rasql.NewCreatePlan(table, rasql.SetField(id, int64(10)), rasql.SetField(value, "before"))
		require.NoError(t, err)
		_, err = rasql.ExecMutation(t.Context(), executor, create)
		require.NoError(t, err)
		patch, err := rasql.NewPatchPlan(table, query.EqualValue(id, int64(10)), rasql.SetField(value, "after"))
		require.NoError(t, err)
		patch, err = patch.WithVersion(version, 1)
		require.NoError(t, err)
		outcome, err := rasql.ExecMutation(t.Context(), executor, patch)
		require.NoError(t, err)
		require.Equal(t, int64(1), outcome.Affected)
	})
}

func newVersionedReturningAcceptance(t *testing.T, rows int) (rasql.Executor, rasql.Query[mutationRow]) {
	t.Helper()
	executor, table, id := mutationFixture(t)
	value := query.TypedColumnOf[mutationRow, string](table.Column("value"))
	relation, err := rasql.SourceOf[mutationRow](table, "")
	require.NoError(t, err)
	version, err := rasql.BindColumn[mutationRow, int64](relation, "version", "")
	require.NoError(t, err)
	for i := int64(1); i <= 2; i++ {
		create, createErr := rasql.NewCreatePlan(table, rasql.SetField(id, i), rasql.SetField(value, "before"))
		require.NoError(t, createErr)
		_, createErr = rasql.ExecMutation(t.Context(), executor, create)
		require.NoError(t, createErr)
	}
	where := query.EqualValue(id, int64(99))
	//nolint:staticcheck // the explicit cardinality cases mirror the acceptance matrix.
	if rows == 1 {
		where = query.EqualValue(id, int64(1))
	} else if rows == 2 {
		where = query.GreaterOrEqualValue(id, int64(1))
	}
	patch, err := rasql.NewPatchPlan(table, where, rasql.SetField(value, "after"))
	require.NoError(t, err)
	patch, err = patch.WithVersion(version, 1)
	require.NoError(t, err)
	returned, err := rasql.Returning(patch, mutationProjection(t, table))
	require.NoError(t, err)
	return executor, returned
}
