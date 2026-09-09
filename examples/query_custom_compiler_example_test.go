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

func (compilerExampleDialect) Compiler() dialect.Compiler { return compilerExample{} }

type compilerExample struct{}

type containsExample struct {
	column query.Expression
	value  any
}

func (containsExample) ExpressionNode()              {}
func (containsExample) CustomExpressionName() string { return "contains" }

func (compilerExample) CompileExpression(emitter dialect.Emitter, expression query.Expression) (bool, error) {
	contains, ok := expression.(containsExample)
	if !ok {
		return false, nil
	}
	emitter.WriteSQL("CONTAINS(")
	if err := emitter.Expression(contains.column); err != nil {
		return true, err
	}
	emitter.WriteSQL(", ")
	if err := emitter.Argument(contains.value); err != nil {
		return true, err
	}
	emitter.WriteSQL(")")
	return true, nil
}

func (compilerExample) CompilePagination(emitter dialect.Emitter, pagination dialect.Pagination) error {
	if pagination.HasOffset {
		return fmt.Errorf("offset is unsupported")
	}
	if pagination.HasLimit {
		emitter.WriteSQL(" FETCH FIRST ")
		if err := emitter.Argument(pagination.Limit); err != nil {
			return err
		}
		emitter.WriteSQL(" ROWS ONLY")
	}
	return nil
}

// Example_customCompiler renders a custom expression and a custom pagination
// clause through a dialect.Compiler extension, at the portable query-builder
// level: a custom query.Expression carries no typed rasql.Expr counterpart,
// so it is projected the same way the query package projects any expression.
func Example_customCompiler() {
	users := store.Users()
	statement, err := query.NewSelect(users.Ref(), query.Project(containsExample{column: users.EmailRef(), value: "@example.com"}))
	if err != nil {
		fmt.Println(err)
		return
	}
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
