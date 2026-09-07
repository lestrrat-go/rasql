package querycompile_test

import (
	"sync"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/querycompile"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

func queryProfile(t *testing.T) engineprofile.Profile {
	t.Helper()
	p, err := engineprofile.Builtin("postgresql-17", engineprofile.Version{Known: true, Major: 17})
	require.NoError(t, err)
	return p
}

func queryTable(t *testing.T) query.TableRef {
	t.Helper()
	return query.MustTableRef(schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "name", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
}

func TestAcceptanceQueryCompilesSelectAndWritesWithExactSQLAndArgs(t *testing.T) {
	c, err := querycompile.New(queryProfile(t))
	require.NoError(t, err)
	users := queryTable(t)

	selectQuery, err := query.NewSelect(users, users.Column("id"), users.Column("name"))
	require.NoError(t, err)
	selectQuery, err = selectQuery.WithWhere(query.Equal(users.Column("id"), 7))
	require.NoError(t, err)
	result, err := query.ResultOf(selectQuery,
		query.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		query.ResultColumn{Name: "name", Type: schema.TextType{}},
	)
	require.NoError(t, err)
	selectStatement, err := c.Select(result)
	require.NoError(t, err)
	require.Equal(t, `SELECT "users"."id", "users"."name" FROM "users" WHERE ("users"."id" = $1)`, selectStatement.SQL())
	require.Equal(t, []any{7}, selectStatement.Args())

	insertQuery, err := query.NewInsert(users, query.Set(users.Column("id"), 7), query.Set(users.Column("name"), "Ada"))
	require.NoError(t, err)
	insertStatement, err := c.Write(insertQuery)
	require.NoError(t, err)
	require.Equal(t, `INSERT INTO "users" ("id", "name") VALUES ($1, $2)`, insertStatement.SQL())
	require.Equal(t, []any{7, "Ada"}, insertStatement.Args())

	updateQuery, err := query.NewUpdate(users, query.Set(users.Column("name"), "Grace"))
	require.NoError(t, err)
	updateQuery, err = updateQuery.WithWhere(query.Equal(users.Column("id"), 7))
	require.NoError(t, err)
	updateStatement, err := c.Write(updateQuery)
	require.NoError(t, err)
	require.Equal(t, `UPDATE "users" SET "name" = $1 WHERE ("users"."id" = $2)`, updateStatement.SQL())
	require.Equal(t, []any{"Grace", 7}, updateStatement.Args())

	deleteQuery, err := query.NewDelete(users)
	require.NoError(t, err)
	deleteQuery, err = deleteQuery.WithWhere(query.Equal(users.Column("id"), 7))
	require.NoError(t, err)
	deleteStatement, err := c.Write(deleteQuery)
	require.NoError(t, err)
	require.Equal(t, `DELETE FROM "users" WHERE ("users"."id" = $1)`, deleteStatement.SQL())
	require.Equal(t, []any{7}, deleteStatement.Args())
}

func TestAcceptanceQueryPreservesReturningPresentNilAndNativeArguments(t *testing.T) {
	c, err := querycompile.New(queryProfile(t))
	require.NoError(t, err)
	users := queryTable(t)
	insertQuery, err := query.NewInsert(users, query.Set(users.Column("id"), nil), query.Set(users.Column("name"), "Ada"))
	require.NoError(t, err)
	insertQuery, err = insertQuery.WithReturning(users.Column("id"))
	require.NoError(t, err)
	statement, err := c.Write(insertQuery)
	require.NoError(t, err)
	require.Equal(t, `INSERT INTO "users" ("id", "name") VALUES ($1, $2) RETURNING "id"`, statement.SQL())
	require.Equal(t, []any{nil, "Ada"}, statement.Args())

	input := []byte("payload")
	native, err := c.Native(stmt.New("SELECT $1", input))
	require.NoError(t, err)
	require.Equal(t, "SELECT $1", native.SQL())
	require.Equal(t, []any{input}, native.Args())
	input[0] = 'X'
	require.Equal(t, []byte("payload"), native.Args()[0])
	_, err = c.Native(stmt.New("   "))
	require.Error(t, err)
}

func TestAcceptanceQueryRejectsZeroAndBindLimitWithoutOutput(t *testing.T) {
	users := queryTable(t)
	q, err := query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	result, err := query.ResultOf(q, query.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	var zero querycompile.Compiler
	got, err := zero.Select(result)
	require.ErrorIs(t, err, engineprofile.ErrInvalidProfile)
	require.Empty(t, got.SQL())

	c, err := querycompile.New(queryProfile(t))
	require.NoError(t, err)
	args := make([]any, 65536)
	got, err = c.Native(stmt.New("SELECT", args...))
	require.ErrorIs(t, err, engineprofile.ErrBindLimit)
	require.Empty(t, got.SQL())
}

func TestAcceptanceQueryCompilerIsReusableConcurrently(t *testing.T) {
	c, err := querycompile.New(queryProfile(t))
	require.NoError(t, err)
	users := queryTable(t)
	q, err := query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	q, err = q.WithWhere(query.Equal(users.Column("id"), 42))
	require.NoError(t, err)
	result, err := query.ResultOf(q, query.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)

	const workers = 100
	statements := make([]stmt.Statement, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range statements {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statements[i], errs[i] = c.Select(result)
		}(i)
	}
	wg.Wait()
	for i := range statements {
		require.NoError(t, errs[i])
		require.Equal(t, statements[0].SQL(), statements[i].SQL())
		require.Equal(t, []any{42}, statements[i].Args())
	}
}

func TestAcceptanceQueryDialectProfileMismatchIsRejected(t *testing.T) {
	p := queryProfile(t)
	_, err := querycompile.NewWithDialect(p, dialect.SQLite())
	require.Error(t, err)
}

func TestAcceptanceQueryCapabilityChecksIgnoreQuotedIdentifiers(t *testing.T) {
	p, err := engineprofile.Builtin("mysql-8.4", engineprofile.Version{Known: true, Major: 8, Minor: 4})
	require.NoError(t, err)
	c, err := querycompile.New(p)
	require.NoError(t, err)
	table := query.MustTableRef(schema.TableDef{
		Name:    "a RETURNING b",
		Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
	})
	body, err := query.NewSelect(table, table.Column("id"))
	require.NoError(t, err)
	result, err := query.ResultOf(body, query.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	statement, err := c.Select(result)
	require.NoError(t, err)
	require.Equal(t, "SELECT `a RETURNING b`.`id` FROM `a RETURNING b`", statement.SQL())
}

type typedNilDialect struct{}

func (*typedNilDialect) Name() string                              { return "typed-nil" }
func (*typedNilDialect) QuoteIdentifier(string) (string, error)    { return "", nil }
func (*typedNilDialect) Placeholder(int) (string, error)           { return "", nil }
func (*typedNilDialect) TypeName(schema.ColumnDef) (string, error) { return "", nil }
func (*typedNilDialect) UpsertStyle() dialect.UpsertStyle          { return dialect.UpsertUnsupported }
func (*typedNilDialect) Supports(dialect.Capability) bool          { return false }

func TestAcceptanceQueryRejectsTypedNilDialect(t *testing.T) {
	p, err := engineprofile.New("custom:typed-nil", engineprofile.Custom, "typed-nil", engineprofile.Version{}, engineprofile.Capabilities{}, engineprofile.Limits{MaxBindParameters: 1})
	require.NoError(t, err)
	var adapter *typedNilDialect
	_, err = querycompile.NewWithDialect(p, adapter)
	require.Error(t, err)
}

func customPostgresProfile(t *testing.T, edit func(*engineprofile.Capabilities)) engineprofile.Profile {
	t.Helper()
	base := queryProfile(t)
	caps := base.Capabilities
	edit(&caps)
	p, err := engineprofile.New("custom:acceptance", engineprofile.Custom, "postgresql", engineprofile.Version{}, caps, engineprofile.Limits{MaxBindParameters: 100})
	require.NoError(t, err)
	return p
}

func TestAcceptanceQueryRejectsWindowWhenProfileDisablesIt(t *testing.T) {
	p := customPostgresProfile(t, func(c *engineprofile.Capabilities) {
		c.WindowFunctions = false
		c.PerParentLimit = engineprofile.PerParentLimitUnsupported
	})
	c, err := querycompile.NewWithDialect(p, dialect.PostgreSQL())
	require.NoError(t, err)
	users := queryTable(t)
	body, err := query.NewSelect(users, query.Project(query.OverWindow(query.Func("row_number"), query.Window(nil, query.Asc(users.Column("id"))))).As("row"))
	require.NoError(t, err)
	result, err := query.ResultOf(body, query.ResultColumn{Name: "row", Type: schema.IntegerType{}})
	require.NoError(t, err)
	statement, err := c.Select(result)
	require.ErrorIs(t, err, engineprofile.ErrUnsupportedFeature)
	require.Empty(t, statement.SQL())
}

func TestAcceptanceQueryTraversesCTEForNestedCapabilities(t *testing.T) {
	p := customPostgresProfile(t, func(c *engineprofile.Capabilities) {
		c.WindowFunctions = false
		c.PerParentLimit = engineprofile.PerParentLimitUnsupported
	})
	c, err := querycompile.NewWithDialect(p, dialect.PostgreSQL())
	require.NoError(t, err)
	users := queryTable(t)
	body, err := query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	result, err := query.ResultOf(body, query.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	cte, err := query.CommonTable("recent", result)
	require.NoError(t, err)
	cteBody, err := query.NewSelect(users, query.Project(query.OverWindow(query.Func("row_number"), query.Window(nil, query.Asc(users.Column("id"))))).As("row"))
	require.NoError(t, err)
	cteBody, err = cteBody.WithCTEs(cte)
	require.NoError(t, err)
	cteResult, err := query.ResultOf(cteBody, query.ResultColumn{Name: "row", Type: schema.IntegerType{}})
	require.NoError(t, err)
	statement, err := c.Select(cteResult)
	require.ErrorIs(t, err, engineprofile.ErrUnsupportedFeature)
	require.Empty(t, statement.SQL())
}

func TestAcceptanceQueryReturningInsertOnlyRejectsUpdateAndDelete(t *testing.T) {
	p := customPostgresProfile(t, func(c *engineprofile.Capabilities) { c.Returning = engineprofile.ReturningInsert })
	c, err := querycompile.NewWithDialect(p, dialect.PostgreSQL())
	require.NoError(t, err)
	users := queryTable(t)
	update, err := query.NewUpdate(users, query.Set(users.Column("name"), "Ada"))
	require.NoError(t, err)
	update, err = update.AllowAll()
	require.NoError(t, err)
	update, err = update.WithReturning(users.Column("id"))
	require.NoError(t, err)
	statement, err := c.Write(update)
	require.ErrorIs(t, err, engineprofile.ErrUnsupportedFeature)
	require.Empty(t, statement.SQL())

	insert, err := query.NewInsert(users, query.Set(users.Column("id"), 1), query.Set(users.Column("name"), "Ada"))
	require.NoError(t, err)
	insert, err = insert.WithReturning(users.Column("id"))
	require.NoError(t, err)
	statement, err = c.Write(insert)
	require.NoError(t, err)
	require.Contains(t, statement.SQL(), "RETURNING")
}
