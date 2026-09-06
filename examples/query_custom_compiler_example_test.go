package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/query"
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

func Example_customCompiler() {
	users := store.Users()
	builder := rasql.DecodeFromRef[struct{}](users.Ref()).
		Project(query.Project(containsExample{column: users.Email(), value: "@example.com"})).
		Limit(3)
	statement, err := builder.Build(compilerExampleDialect{Dialect: dialect.SQLite()})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(statement.SQL())
	fmt.Println(statement.Args())
	// Output:
	// SELECT CONTAINS("users"."email", ?) FROM "users" FETCH FIRST ? ROWS ONLY
	// [@example.com 3]
}
