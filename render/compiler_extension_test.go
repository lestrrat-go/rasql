package render_test

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/dynamic"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type compilerDialect struct {
	dialect.Dialect
	compiler dialect.Compiler
}

type nilCompilerDialect struct{ dialect.Dialect }

func (nilCompilerDialect) Compiler() dialect.Compiler { return nil }

type failingCompiler struct{}

func (failingCompiler) CompileExpression(dialect.Emitter, query.Expression) (bool, error) {
	return false, errors.New("compiler expression failure")
}

func (failingCompiler) CompilePagination(dialect.Emitter, dialect.Pagination) error { return nil }

func (d compilerDialect) Compiler() dialect.Compiler { return d.compiler }

type containsExpression struct {
	left  query.Expression
	right any
}

func (containsExpression) ExpressionNode()              {}
func (containsExpression) CustomExpressionName() string { return "contains" }

type testCompiler struct {
	offsetErr error
}

type identifierExpression struct{}

func (identifierExpression) ExpressionNode() {}

type emptyIdentifierExpression struct{}

func (emptyIdentifierExpression) ExpressionNode() {}

type recursiveExpression struct {
	values []int
}

func (recursiveExpression) ExpressionNode() {}

type recursiveCompiler struct{}

func (recursiveCompiler) CompileExpression(emitter dialect.Emitter, expression query.Expression) (bool, error) {
	if _, ok := expression.(recursiveExpression); !ok {
		return false, nil
	}
	return true, emitter.Expression(expression)
}

func (recursiveCompiler) CompilePagination(dialect.Emitter, dialect.Pagination) error { return nil }

func (testCompiler) CompileExpression(emitter dialect.Emitter, expression query.Expression) (bool, error) {
	if _, ok := expression.(identifierExpression); ok {
		if err := emitter.Identifier("", "users", "email"); err != nil {
			return true, err
		}
		return true, nil
	}
	if _, ok := expression.(emptyIdentifierExpression); ok {
		return true, emitter.Identifier("", "")
	}
	contains, ok := expression.(containsExpression)
	if !ok {
		return false, nil
	}
	emitter.WriteSQL("CONTAINS(")
	if err := emitter.Expression(contains.left); err != nil {
		return true, err
	}
	emitter.WriteSQL(", ")
	if err := emitter.Argument(contains.right); err != nil {
		return true, err
	}
	emitter.WriteSQL(")")
	return true, nil
}

func (c testCompiler) CompilePagination(emitter dialect.Emitter, pagination dialect.Pagination) error {
	if pagination.HasOffset {
		if c.offsetErr != nil {
			return c.offsetErr
		}
		return errors.New("offset is unsupported")
	}
	if !pagination.HasLimit {
		return nil
	}
	emitter.WriteSQL(" FETCH FIRST ")
	if err := emitter.Argument(pagination.Limit); err != nil {
		return err
	}
	emitter.WriteSQL(" ROWS ONLY")
	return nil
}

type recordingEmitter struct {
	args []any
}

func (recordingEmitter) WriteSQL(string)            {}
func (recordingEmitter) Identifier(...string) error { return nil }
func (e *recordingEmitter) Argument(value any) error {
	e.args = append(e.args, value)
	return nil
}
func (recordingEmitter) Expression(query.Expression) error { return nil }

func TestCompilerExtensionRendersCustomExpressionAndPagination(t *testing.T) {
	table := query.MustTableRef(schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "email", Type: schema.TextType{}}}})
	d := compilerDialect{Dialect: dialect.SQLite(), compiler: testCompiler{}}
	statement, err := query.NewSelect(table, query.Project(containsExpression{left: table.Column("email"), right: "@example.com"}))
	require.NoError(t, err)
	statement, err = statement.WithLimit(7)
	require.NoError(t, err)
	rendered, err := render.Select(d, statement)
	require.NoError(t, err)
	require.Equal(t, `SELECT CONTAINS("users"."email", ?) FROM "users" FETCH FIRST ? ROWS ONLY`, rendered.SQL())
	require.Equal(t, []any{"@example.com", 7}, rendered.Args())
}

func TestCompilerExtensionDelegatesAndRejectsUnsupportedExpressions(t *testing.T) {
	table := query.MustTableRef(schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "email", Type: schema.TextType{}}}})
	statement, err := query.NewSelect(table, query.Project(query.Lower(table.Column("email"))))
	require.NoError(t, err)
	custom := compilerDialect{Dialect: dialect.SQLite(), compiler: testCompiler{}}
	rendered, err := render.Select(custom, statement)
	require.NoError(t, err)
	require.Contains(t, rendered.SQL(), `LOWER("users"."email")`)

	unknown, err := query.NewSelect(table, query.Project(unknownExpression{}))
	require.NoError(t, err)
	_, err = render.Select(dialect.SQLite(), unknown)
	require.ErrorContains(t, err, "unsupported expression")

	other := query.MustTableRef(schema.TableDef{Name: "other", Columns: []schema.ColumnDef{{Name: "secret", Type: schema.TextType{}}}})
	wrapped, err := query.NewSelect(table, query.Project(containsExpression{left: other.Column("secret"), right: "x"}))
	require.NoError(t, err)
	_, err = render.Select(custom, wrapped)
	require.ErrorContains(t, err, "outside the statement")
	identifier, err := query.NewSelect(table, query.Project(identifierExpression{}))
	require.NoError(t, err)
	identifierSQL, err := render.Select(custom, identifier)
	require.NoError(t, err)
	require.Contains(t, identifierSQL.SQL(), `"users"."email"`)
	emptyIdentifier, err := query.NewSelect(table, query.Project(emptyIdentifierExpression{}))
	require.NoError(t, err)
	_, err = render.Select(custom, emptyIdentifier)
	require.ErrorContains(t, err, "non-empty component")
}

func TestCompilerExtensionReturnsPaginationErrorsAndPreservesNil(t *testing.T) {
	table := query.MustTableRef(schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "email", Type: schema.TextType{}}}})
	statement, err := query.NewSelect(table, query.Project(table.Column("email")))
	require.NoError(t, err)
	statement, err = statement.WithOffset(2)
	require.NoError(t, err)
	want := errors.New("custom offset")
	_, err = render.Select(compilerDialect{Dialect: dialect.SQLite(), compiler: testCompiler{offsetErr: want}}, statement)
	require.ErrorIs(t, err, want)

	emitter := &recordingEmitter{}
	err = (testCompiler{}).CompilePagination(emitter, dialect.Pagination{HasLimit: true, Limit: nil})
	require.NoError(t, err)
	require.Equal(t, []any{nil}, emitter.args)

	recursive, err := query.NewSelect(table, query.Project(recursiveExpression{values: []int{1}}))
	require.NoError(t, err)
	_, err = render.Select(compilerDialect{Dialect: dialect.SQLite(), compiler: recursiveCompiler{}}, recursive)
	require.ErrorContains(t, err, "unsupported expression")
	failing, err := query.NewSelect(table, query.Project(table.Column("email")))
	require.NoError(t, err)
	_, err = render.Select(compilerDialect{Dialect: dialect.SQLite(), compiler: failingCompiler{}}, failing)
	require.ErrorContains(t, err, "compiler expression failure")
	stock, err := failing.WithLimit(7)
	require.NoError(t, err)
	stockSQL, err := render.Select(dialect.SQLite(), stock)
	require.NoError(t, err)
	nilProviderSQL, err := render.Select(nilCompilerDialect{Dialect: dialect.SQLite()}, stock)
	require.NoError(t, err)
	require.Equal(t, stockSQL.SQL(), nilProviderSQL.SQL())
	require.Equal(t, stockSQL.Args(), nilProviderSQL.Args())
}

func TestCompilerExtensionExecutesRawDynamicAndTypedSelects(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	db, err := rasql.New(database, compilerDialect{Dialect: dialect.SQLite(), compiler: testCompiler{}})
	require.NoError(t, err)
	table := query.MustTableRef(schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "email", Type: schema.TextType{}}}})
	statement, err := query.NewSelect(table, table.Column("email"))
	require.NoError(t, err)
	statement, err = statement.WithLimit(2)
	require.NoError(t, err)
	expectedSQL := `SELECT "users"."email" FROM "users" FETCH FIRST ? ROWS ONLY`
	for range 3 {
		mock.ExpectQuery(expectedSQL).WithArgs(2).WillReturnRows(sqlmock.NewRows([]string{"email"}).AddRow("a@example.com"))
	}
	mock.ExpectClose()
	raw, err := dynamic.Query(t.Context(), db, statement)
	require.NoError(t, err)
	for _, rowErr := range raw {
		require.NoError(t, rowErr)
	}
	dynamicRows, err := dynamic.SelectFrom(table).Select("email").Limit(2).Query(t.Context(), db)
	require.NoError(t, err)
	for _, rowErr := range dynamicRows {
		require.NoError(t, rowErr)
	}
	type user struct {
		Email string `rasql:"email"`
	}
	users, err := rasql.TableOf[user](schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "email", Type: schema.TextType{}}}})
	require.NoError(t, err)
	typedRows, err := rasql.DecodeFrom[user](users).Project(users.Column("email")).Limit(2).Query(t.Context(), db)
	require.NoError(t, err)
	for _, rowErr := range typedRows {
		require.NoError(t, rowErr)
	}
	require.NoError(t, database.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

type unknownExpression struct{}

func (unknownExpression) ExpressionNode() {}
