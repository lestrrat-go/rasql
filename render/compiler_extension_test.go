package render_test

import (
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type compilerDialect struct {
	dialect.Dialect
	compiler dialect.Compiler
}

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

func (testCompiler) CompileExpression(emitter dialect.Emitter, expression query.Expression) (bool, error) {
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
}

type unknownExpression struct{}

func (unknownExpression) ExpressionNode() {}
