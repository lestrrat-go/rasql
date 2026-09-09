package generate_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/scratchmod"
	"github.com/stretchr/testify/require"
)

// usersQueryHelperSource declares a fixture-local usersIDDecoder and
// usersQuery, the closest canonical-API equivalent to what the removed
// rasql.TypedSelectFrom(store.Users()) used to hand a caller directly: a
// rasql.Query[store.UsersRow] ready for Where. store.UsersRow only has a
// ScanRow method, not rasql.RowDecoder, so the decoder bridges the two
// rather than reimplementing the scan.
const usersQueryHelperSource = `type usersIDDecoder struct{ schema rasql.ResultSchema }

func (d usersIDDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (usersIDDecoder) Presence() []rasql.Presence         { return nil }
func (usersIDDecoder) DecodeRow(source rasql.ScanSource, result *store.UsersRow) error {
	return source.Scan(&result.ID)
}

func usersQuery() rasql.Query[store.UsersRow] {
	relation, err := rasql.SourceOf[store.UsersRow](store.Users(), "")
	if err != nil {
		panic(err)
	}
	id, err := rasql.BindColumn[store.UsersRow, int64](relation, "id", "")
	if err != nil {
		panic(err)
	}
	resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	if err != nil {
		panic(err)
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", id.Expr(), schema.IntegerType{}, ""),
	}, usersIDDecoder{schema: resultSchema})
	if err != nil {
		panic(err)
	}
	return rasql.Select(relation.Source(), projection)
}
`

func TestTypedQueryCompileFailures(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{name: "wrong comparison", body: `_ = query.EqualValue(store.Users().ID(), "wrong")`, want: "cannot use"},
		{name: "wrong assignment", body: `_ = query.AssignValue(store.Users().ID(), "wrong")`, want: "cannot use"},
		{name: "bare where", body: `_ = usersQuery().Where(query.Bind(1))`, want: "cannot use"},
		{name: "dynamic null bypass", body: `_ = usersQuery().Where(query.IsNull(query.Bind(1)))`, want: "cannot use"},
		{name: "mismatched join", body: `_ = query.EqualColumns(store.Users().ID(), store.Users().Email())`, want: "does not match inferred"},
		{name: "null on non-null", body: `_ = query.TypedIsNull(store.Users().ID())`, want: "does not match inferred"},
		{name: "not null on non-null", body: `_ = query.TypedIsNotNull(store.Users().ID())`, want: "does not match inferred"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			root, err := filepath.Abs("..")
			require.NoError(t, err)
			require.NoError(t, scratchmod.Write(dir, root, "example.com/typed-fixture"))
			source := "package fixture\n\nimport (\n\t\"github.com/lestrrat-go/rasql\"\n\t\"github.com/lestrrat-go/rasql/examples/store\"\n\t\"github.com/lestrrat-go/rasql/query\"\n\t\"github.com/lestrrat-go/rasql/schema\"\n)\n\nvar _ = rasql.Equal\n\n" + usersQueryHelperSource + "\n\nfunc invalid() {\n\t" + tc.body + "\n}\n"
			require.NoError(t, os.WriteFile(filepath.Join(dir, "invalid.go"), []byte(source), 0o600))
			command := exec.CommandContext(t.Context(), "go", "test", "-mod=mod", "-run", "^$", "./...")
			command.Dir = dir
			output, err := command.CombinedOutput()
			require.Error(t, err, "invalid typed caller compiled:\n%s", output)
			require.Contains(t, string(output), tc.want)
		})
	}
}

func TestTypedQueryPositiveConsumerCompiles(t *testing.T) {
	dir := t.TempDir()
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	require.NoError(t, scratchmod.Write(dir, root, "example.com/typed-positive"))
	source := `package positive

import (
    "github.com/lestrrat-go/rasql"
    "github.com/lestrrat-go/rasql/examples/store"
    "github.com/lestrrat-go/rasql/query"
    "github.com/lestrrat-go/rasql/schema"
)

` + usersQueryHelperSource + `
func compile() {
    users := store.Users()
    id := users.ID()
    email := users.Nickname()
    _ = query.EqualValue(id, int64(1))
    _ = query.EqualNullableValue(email, (*string)(nil))
    _ = query.LessValue(id, int64(2))
    _ = query.LessOrEqualValue(id, int64(2))
    _ = query.GreaterValue(id, int64(0))
    _ = query.GreaterOrEqualValue(id, int64(0))
    _ = query.InValues(id, int64(1), int64(2))
    _ = query.TypedIsNull(email)
    _ = query.TypedIsNotNull(email)
    _ = query.AndPredicates(query.EqualValue(id, int64(1)))
    _ = query.OrPredicates(query.EqualValue(id, int64(1)))
    _ = query.NotPredicate(query.EqualValue(id, int64(1)))
    _ = query.AssignValue(id, int64(3))
    _ = query.AssignNullableValue(email, (*string)(nil))
    other, _ := users.As("other")
    _ = query.TypedInnerJoin(other.Ref(), query.EqualColumns(id, other.ID()))
    _ = query.TypedLeftJoin(other.Ref(), query.EqualColumns(id, other.ID()))

    relation, err := rasql.SourceOf[store.UsersRow](store.Users(), "")
    if err != nil {
        panic(err)
    }
    relationID, err := rasql.BindColumn[store.UsersRow, int64](relation, "id", "")
    if err != nil {
        panic(err)
    }
    resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
    if err != nil {
        panic(err)
    }
    projection, err := rasql.NewProjection([]rasql.ProjectionItem{
        rasql.Item("id", relationID.Expr(), schema.IntegerType{}, ""),
    }, usersIDDecoder{schema: resultSchema})
    if err != nil {
        panic(err)
    }
    otherRelation, err := rasql.SourceOf[store.UsersRow](store.Users(), "other2")
    if err != nil {
        panic(err)
    }
    otherRelationID, err := rasql.BindColumn[store.UsersRow, int64](otherRelation, "id", "")
    if err != nil {
        panic(err)
    }
    selectQuery := rasql.Select(relation.Source(), projection).
        Join(otherRelation.Source(), rasql.EqualExpr(relationID.Expr(), otherRelationID.Expr())).
        Where(rasql.EqualValue(relationID.Expr(), int64(1))).
        GroupBy(rasql.Group(relationID.Expr())).
        Having(rasql.EqualValue(relationID.Expr(), int64(1))).
        OrderBy(rasql.AscExpr(relationID.Expr())).
        Distinct()
    limited, err := selectQuery.Limit(1)
    if err != nil {
        panic(err)
    }
    if _, err := limited.Offset(0); err != nil {
        panic(err)
    }
}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "positive.go"), []byte(source), 0o600))
	command := exec.CommandContext(t.Context(), "go", "test", "-mod=mod", "-run", "^$", "./...")
	command.Dir = dir
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "positive typed consumer output:\n%s", output)
}
