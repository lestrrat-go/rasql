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

type equalChildExpression struct {
	value int
}

func (equalChildExpression) ExpressionNode() {}

type parentExpression struct {
	child equalChildExpression
}

func (parentExpression) ExpressionNode() {}

type parentChildCompiler struct {
	visits []string
}

func (c *parentChildCompiler) CompileExpression(emitter dialect.Emitter, expression query.Expression) (bool, error) {
	switch expression := expression.(type) {
	case parentExpression:
		c.visits = append(c.visits, "parent")
		emitter.WriteSQL("PARENT(")
		if err := emitter.Expression(expression.child); err != nil {
			return true, err
		}
		emitter.WriteSQL(")")
		return true, nil
	case equalChildExpression:
		c.visits = append(c.visits, "child")
		emitter.WriteSQL("CHILD(")
		if err := emitter.Argument(expression.value); err != nil {
			return true, err
		}
		emitter.WriteSQL(")")
		return true, nil
	default:
		return false, nil
	}
}

func (c *parentChildCompiler) CompilePagination(dialect.Emitter, dialect.Pagination) error {
	return nil
}

type callbackChainExpression struct {
	next *callbackChainExpression
}

func (callbackChainExpression) ExpressionNode() {}

type callbackChainCompiler struct{}

func (callbackChainCompiler) CompileExpression(emitter dialect.Emitter, expression query.Expression) (bool, error) {
	node := expression.(callbackChainExpression)
	if node.next == nil {
		emitter.WriteSQL("CHAIN")
		return true, nil
	}
	return true, emitter.Expression(*node.next)
}

func (callbackChainCompiler) CompilePagination(dialect.Emitter, dialect.Pagination) error { return nil }

func callbackChain(length int) callbackChainExpression {
	terminal := callbackChainExpression{}
	current := &terminal
	for i := 1; i < length; i++ {
		currentValue := callbackChainExpression{next: current}
		current = &currentValue
	}
	return *current
}

type equalValuedExpression struct {
	value int
}

func (equalValuedExpression) ExpressionNode() {}

type equalValuedCompiler struct {
	visits int
}

func (c *equalValuedCompiler) CompileExpression(emitter dialect.Emitter, expression query.Expression) (bool, error) {
	node := expression.(equalValuedExpression)
	c.visits++
	if c.visits == 1 {
		emitter.WriteSQL("PARENT(")
		if err := emitter.Expression(equalValuedExpression{value: node.value}); err != nil {
			return true, err
		}
		emitter.WriteSQL(")")
		return true, nil
	}
	emitter.WriteSQL("CHILD(")
	if err := emitter.Argument(node.value); err != nil {
		return true, err
	}
	emitter.WriteSQL(")")
	return true, nil
}

func (c *equalValuedCompiler) CompilePagination(dialect.Emitter, dialect.Pagination) error {
	return nil
}

type wrappedExpression struct {
	child query.Expression
}

func (wrappedExpression) ExpressionNode() {}

type wrappedCompiler struct{}

func (wrappedCompiler) CompileExpression(emitter dialect.Emitter, expression query.Expression) (bool, error) {
	if wrapper, ok := expression.(wrappedExpression); ok {
		return true, emitter.Expression(wrapper.child)
	}
	return false, nil
}

func (wrappedCompiler) CompilePagination(dialect.Emitter, dialect.Pagination) error { return nil }

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

type mapRecursiveExpression struct {
	values map[string]int
}

func (mapRecursiveExpression) ExpressionNode() {}

type functionRecursiveExpression struct {
	value func()
}

func (functionRecursiveExpression) ExpressionNode() {}

type recursiveCompiler struct{}

func (recursiveCompiler) CompileExpression(emitter dialect.Emitter, expression query.Expression) (bool, error) {
	switch expression.(type) {
	case recursiveExpression, mapRecursiveExpression, functionRecursiveExpression:
		return true, emitter.Expression(expression)
	default:
		return false, nil
	}
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

func TestCompilerExtensionDispatchesEqualValuedChild(t *testing.T) {
	table := query.MustTableRef(schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "email", Type: schema.TextType{}}}})
	compiler := &parentChildCompiler{}
	statement, err := query.NewSelect(table, query.Project(parentExpression{child: equalChildExpression{value: 7}}))
	require.NoError(t, err)
	rendered, err := render.Select(compilerDialect{Dialect: dialect.SQLite(), compiler: compiler}, statement)
	require.NoError(t, err)
	require.Equal(t, []string{"parent", "child"}, compiler.visits)
	require.Equal(t, `SELECT PARENT(CHILD(?)) FROM "users"`, rendered.SQL())
	require.Equal(t, []any{7}, rendered.Args())
}

func TestCompilerExtensionCallbackDepthBoundary(t *testing.T) {
	table := query.MustTableRef(schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "email", Type: schema.TextType{}}}})
	compiler := compilerDialect{Dialect: dialect.SQLite(), compiler: callbackChainCompiler{}}
	statement, err := query.NewSelect(table, query.Project(callbackChain(64)))
	require.NoError(t, err)
	rendered, err := render.Select(compiler, statement)
	require.NoError(t, err)
	require.Equal(t, `SELECT CHAIN FROM "users"`, rendered.SQL())

	statement, err = query.NewSelect(table, query.Project(callbackChain(65)))
	require.NoError(t, err)
	failed, err := render.Select(compiler, statement)
	require.EqualError(t, err, "render sqlite: compiler expression delegation exceeds 64 nested callbacks")
	require.Empty(t, failed.SQL())
	require.Empty(t, failed.Args())
}

func TestCompilerExtensionEqualValuedSameTypeChild(t *testing.T) {
	table := query.MustTableRef(schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "email", Type: schema.TextType{}}}})
	compiler := &equalValuedCompiler{}
	statement, err := query.NewSelect(table, query.Project(equalValuedExpression{value: 7}))
	require.NoError(t, err)
	rendered, err := render.Select(compilerDialect{Dialect: dialect.SQLite(), compiler: compiler}, statement)
	require.NoError(t, err)
	require.Equal(t, 2, compiler.visits)
	require.Equal(t, `SELECT PARENT(CHILD(?)) FROM "users"`, rendered.SQL())
	require.Equal(t, []any{7}, rendered.Args())
}

func TestCompilerExtensionValidatesEverySelectClauseContext(t *testing.T) {
	users := query.MustTableRef(schema.TableDef{Name: "users", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "email", Type: schema.TextType{}},
	}})
	orders := query.MustTableRef(schema.TableDef{Name: "orders", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "user_id", Type: schema.IntegerType{}},
	}})
	secrets := query.MustTableRef(schema.TableDef{Name: "secrets", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
	}})
	renderer := func(statement query.Select) error {
		_, err := render.Select(compilerDialect{Dialect: dialect.SQLite(), compiler: wrappedCompiler{}}, statement)
		return err
	}
	wrap := func(child query.Expression) wrappedExpression { return wrappedExpression{child: child} }

	projection, err := query.NewSelect(users, users.Column("email"), query.Project(wrap(users.Column("id"))))
	require.NoError(t, err)
	require.NoError(t, renderer(projection))
	projection, err = query.NewSelect(users, users.Column("email"), query.Project(wrap(secrets.Column("id"))))
	require.NoError(t, err)
	require.ErrorContains(t, renderer(projection), "projections[1]")
	aggregateProjection, err := query.NewSelect(users, query.Project(query.CountAll()), query.Project(wrap(query.Count(users.Column("id")))))
	require.NoError(t, err)
	require.NoError(t, renderer(aggregateProjection))
	bareAggregateProjection, err := query.NewSelect(users, query.Project(query.CountAll()), query.Project(wrap(users.Column("id"))))
	require.NoError(t, err)
	require.ErrorContains(t, renderer(bareAggregateProjection), "outside an aggregate")

	join, err := query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	join, err = join.WithJoin(query.InnerJoin(orders, wrap(query.Equal(users.Column("id"), orders.Column("user_id")))))
	require.NoError(t, err)
	require.NoError(t, renderer(join))
	join, err = query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	join, err = join.WithJoin(query.InnerJoin(orders, wrap(query.Equal(users.Column("id"), secrets.Column("id")))))
	require.NoError(t, err)
	require.ErrorContains(t, renderer(join), "joins[0].on")
	join, err = query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	join, err = join.WithJoin(query.InnerJoin(orders, wrap(query.Count(users.Column("id")))))
	require.NoError(t, err)
	require.ErrorContains(t, renderer(join), "JOIN ON")

	where, err := query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	where, err = where.WithWhere(wrap(query.Equal(users.Column("id"), 1)))
	require.NoError(t, err)
	require.NoError(t, renderer(where))
	where, err = query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	where, err = where.WithWhere(wrap(query.Equal(users.Column("id"), secrets.Column("id"))))
	require.NoError(t, err)
	require.ErrorContains(t, renderer(where), "where")
	where, err = query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	where, err = where.WithWhere(wrap(query.Count(users.Column("id"))))
	require.NoError(t, err)
	require.ErrorContains(t, renderer(where), "WHERE")

	group, err := query.NewGroupedSelect(users, []query.Expression{users.Column("id"), wrap(users.Column("email"))}, users.Column("id"))
	require.NoError(t, err)
	require.NoError(t, renderer(group))
	group, err = query.NewGroupedSelect(users, []query.Expression{users.Column("id"), wrap(secrets.Column("id"))}, users.Column("id"))
	require.NoError(t, err)
	require.ErrorContains(t, renderer(group), "group_by[1]")
	group, err = query.NewGroupedSelect(users, []query.Expression{users.Column("id"), wrap(query.Count(users.Column("id")))}, users.Column("id"))
	require.NoError(t, err)
	require.ErrorContains(t, renderer(group), "GROUP BY")
	group, err = query.NewGroupedSelect(users, []query.Expression{users.Column("id"), wrap(query.Bind(1))}, users.Column("id"))
	require.NoError(t, err)
	require.ErrorContains(t, renderer(group), "bound value")

	having, err := query.NewGroupedSelect(users, []query.Expression{users.Column("id")}, users.Column("id"))
	require.NoError(t, err)
	having, err = having.WithHaving(wrap(query.GreaterThan(query.Count(users.Column("id")), 1)))
	require.NoError(t, err)
	require.NoError(t, renderer(having))
	having, err = query.NewGroupedSelect(users, []query.Expression{users.Column("id")}, users.Column("id"))
	require.NoError(t, err)
	having, err = having.WithHaving(wrap(secrets.Column("id")))
	require.NoError(t, err)
	require.ErrorContains(t, renderer(having), "having")
	having, err = query.NewGroupedSelect(users, []query.Expression{users.Column("id")}, users.Column("id"))
	require.NoError(t, err)
	having, err = having.WithHaving(wrap(query.Max(query.Count(users.Column("id")))))
	require.NoError(t, err)
	require.ErrorContains(t, renderer(having), "inside another aggregate")
	ungroupedHaving, err := query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	require.ErrorContains(t, ungroupedHaving.ValidateCompilerExpression(wrap(query.Count(users.Column("id"))), "having", -1), "requires a GROUP BY")

	order, err := query.NewSelect(users, query.Project(query.CountAll()))
	require.NoError(t, err)
	order, err = order.WithOrder(query.Asc(query.Count(users.Column("id"))), query.Asc(wrap(query.Count(users.Column("id")))))
	require.NoError(t, err)
	renderedOrder, err := render.Select(compilerDialect{Dialect: dialect.SQLite(), compiler: wrappedCompiler{}}, order)
	require.NoError(t, err)
	require.Equal(t, `SELECT COUNT(*) FROM "users" ORDER BY COUNT("users"."id"), COUNT("users"."id")`, renderedOrder.SQL())
	order, err = query.NewSelect(users, query.Project(query.CountAll()))
	require.NoError(t, err)
	order, err = order.WithOrder(query.Asc(query.Count(users.Column("id"))), query.Desc(wrap(query.Count(users.Column("id")))))
	require.NoError(t, err)
	renderedOrder, err = render.Select(compilerDialect{Dialect: dialect.SQLite(), compiler: wrappedCompiler{}}, order)
	require.NoError(t, err)
	require.Equal(t, `SELECT COUNT(*) FROM "users" ORDER BY COUNT("users"."id"), COUNT("users"."id") DESC`, renderedOrder.SQL())
	order, err = query.NewSelect(users, query.Project(query.CountAll()))
	require.NoError(t, err)
	order, err = order.WithOrder(query.Asc(query.Count(users.Column("id"))), query.Desc(wrap(secrets.Column("id"))))
	require.NoError(t, err)
	require.ErrorContains(t, renderer(order), "order_by[1]")
	order, err = query.NewSelect(users, query.Project(query.CountAll()))
	require.NoError(t, err)
	order, err = order.WithOrder(query.Asc(query.Count(users.Column("id"))), query.Asc(wrap(users.Column("id"))))
	require.NoError(t, err)
	require.ErrorContains(t, renderer(order), "outside an aggregate")
	order, err = query.NewSelect(users, query.Project(query.CountAll()))
	require.NoError(t, err)
	order, err = order.WithOrder(query.Asc(query.Count(users.Column("id"))), query.Asc(wrap(query.Max(query.Count(users.Column("id"))))))
	require.NoError(t, err)
	require.ErrorContains(t, renderer(order), "inside another aggregate")
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
	require.EqualError(t, err, "render sqlite: compiler expression delegation exceeds 64 nested callbacks")
	mapRecursive, err := query.NewSelect(table, query.Project(mapRecursiveExpression{values: map[string]int{"x": 1}}))
	require.NoError(t, err)
	_, err = render.Select(compilerDialect{Dialect: dialect.SQLite(), compiler: recursiveCompiler{}}, mapRecursive)
	require.EqualError(t, err, "render sqlite: compiler expression delegation exceeds 64 nested callbacks")
	functionRecursive, err := query.NewSelect(table, query.Project(functionRecursiveExpression{value: func() {}}))
	require.NoError(t, err)
	_, err = render.Select(compilerDialect{Dialect: dialect.SQLite(), compiler: recursiveCompiler{}}, functionRecursive)
	require.EqualError(t, err, "render sqlite: compiler expression delegation exceeds 64 nested callbacks")
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
