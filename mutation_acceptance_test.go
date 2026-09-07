package rasql_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

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

func TestSQLiteMutationFourStatesAndReturning(t *testing.T) {
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
}
