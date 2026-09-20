package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
)

type compilerExampleDialect struct {
	dialect.Dialect
}

// Override only Compiler so the wrapper keeps SQLite's identifier quoting and
// placeholders while replacing expression and pagination compilation.
func (compilerExampleDialect) Compiler() dialect.Compiler { return compilerExample{} }

type compilerExample struct{}

type containsExample struct {
	column query.Expression
	value  any
}

// These marker methods let the query validator recognize the value as a named
// custom expression and route it to the dialect compiler.
func (containsExample) ExpressionNode()              {}
func (containsExample) CustomExpressionName() string { return "contains" }

func (compilerExample) CompileExpression(emitter dialect.Emitter, expression query.Expression) (bool, error) {
	contains, ok := expression.(containsExample)
	if !ok {
		// Returning false lets the normal compiler handle expressions this
		// extension does not own.
		return false, nil
	}
	emitter.WriteSQL("CONTAINS(")
	// Emit the column as an expression so the base dialect still qualifies and
	// quotes it correctly.
	if err := emitter.Expression(contains.column); err != nil {
		return true, err
	}
	emitter.WriteSQL(", ")
	// Emit the search value as an argument so it becomes a placeholder rather
	// than trusted SQL text.
	if err := emitter.Argument(contains.value); err != nil {
		return true, err
	}
	emitter.WriteSQL(")")
	return true, nil
}

func (compilerExample) CompilePagination(emitter dialect.Emitter, pagination dialect.Pagination) error {
	// This custom syntax has no offset form, so reject one rather than silently
	// changing the requested page.
	if pagination.HasOffset {
		return fmt.Errorf("offset is unsupported")
	}
	if pagination.HasLimit {
		// Bind the limit for the same reason ordinary query values are bound.
		emitter.WriteSQL(" FETCH FIRST ")
		if err := emitter.Argument(pagination.Limit); err != nil {
			return err
		}
		emitter.WriteSQL(" ROWS ONLY")
	}
	return nil
}

// Example_customCompiler solves the case where a target engine needs an
// expression and pagination syntax the built-in dialect does not provide. A
// dialect.Compiler extension handles those two nodes while the base dialect
// still quotes identifiers and binds values.
func Example_customCompiler() {
	users := store.Users()
	// containsExample holds a query.Expression, and Column is the generated
	// table's only way to produce a query.ColumnRef: the generated columns
	// struct binds a rasql.Column, which the query package does not take.
	statement, err := query.NewSelect(users.Ref(), query.Project(containsExample{column: users.Ref().Column("email"), value: "@example.com"}))
	if err != nil {
		fmt.Println(err)
		return
	}
	// Limit exercises the pagination hook independently of the custom
	// expression hook used by the projection.
	statement, err = statement.WithLimit(3)
	if err != nil {
		fmt.Println(err)
		return
	}
	rendered, err := render.Select(compilerExampleDialect{Dialect: dialect.SQLite()}, statement)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(rendered.SQL())
	fmt.Println(rendered.Args())
	// Output:
	// SELECT CONTAINS("users"."email", ?) FROM "users" FETCH FIRST ? ROWS ONLY
	// [@example.com 3]
}
