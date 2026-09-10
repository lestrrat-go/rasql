package rasql_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type mutationCodec struct{ enc *int }

func (c mutationCodec) Encode(value any) (driver.Value, error) {
	*c.enc++
	return "encoded:" + value.(string), nil
}
func (mutationCodec) Decode(source any, destination any) error {
	*destination.(*string) = source.(string)
	return nil
}

func TestRootMutationColumnCodecEncodesEachOccurrence(t *testing.T) {
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
}

type mutationCodecRow struct{}

func TestMissingMutationCodecFailsBeforeExecution(t *testing.T) {
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
}
