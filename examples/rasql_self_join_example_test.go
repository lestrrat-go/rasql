package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

// managedEmployee is one employee alongside their manager's name. No
// generated row type holds a manager_name field, so a local type names it.
type managedEmployee struct {
	Name        string
	ManagerName string
}

type managedEmployeeDecoder struct{ result rasql.ResultSchema }

func (d managedEmployeeDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d managedEmployeeDecoder) Presence() []rasql.Presence       { return nil }
func (d managedEmployeeDecoder) DecodeRow(src rasql.ScanSource, row *managedEmployee) error {
	return src.Scan(&row.Name, &row.ManagerName)
}

// Example_rasql_self_join joins a table to itself. The alias is what keeps
// the two sides apart, and the columns bound from each source read whichever
// table value they are bound against, so the join condition names one side
// through the employees source and the other through the manager source.
func Example_rasql_self_join() {
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 40, 0)
	if err != nil {
		fmt.Printf("failed to describe engine profile: %s\n", err)
		return
	}
	executor, err := rasql.AsExecutor(db, profile)
	if err != nil {
		fmt.Printf("failed to create executor: %s\n", err)
		return
	}
	employees := store.Employees()
	if err := rasql.CreateTable(ctx, db, employees); err != nil {
		fmt.Printf("failed to create employees table: %s\n", err)
		return
	}
	// A top-level employee has no manager, so manager_id is nullable and the
	// generated field is a pointer.
	ada := int64(1)
	for _, employee := range []store.EmployeesRow{
		{ID: 1, Name: "ada"},
		{ID: 2, Name: "grace", ManagerID: &ada},
		{ID: 3, Name: "edsger", ManagerID: &ada},
	} {
		plan := store.NewEmployeesCreate().ID(employee.ID).Name(employee.Name)
		if employee.ManagerID != nil {
			plan = plan.ManagerID(employee.ManagerID)
		}
		if _, err := rasql.ExecMutation(ctx, executor, plan.Plan()); err != nil {
			fmt.Printf("failed to insert employee: %s\n", err)
			return
		}
	}

	employeesSource, err := rasql.SourceOf(employees, "")
	if err != nil {
		fmt.Printf("failed to bind employees source: %s\n", err)
		return
	}
	employeeID, err := rasql.BindColumn[store.EmployeesRow, int64](employeesSource, employees.IDRef().Name(), "")
	if err != nil {
		fmt.Printf("failed to bind employees id column: %s\n", err)
		return
	}
	employeeName, err := rasql.BindColumn[store.EmployeesRow, string](employeesSource, employees.NameRef().Name(), "")
	if err != nil {
		fmt.Printf("failed to bind employees name column: %s\n", err)
		return
	}
	employeeManagerID, err := rasql.BindNullColumn[store.EmployeesRow, int64](employeesSource, employees.ManagerIDRef().Name(), "")
	if err != nil {
		fmt.Printf("failed to bind employees manager_id column: %s\n", err)
		return
	}

	manager, err := employees.As("manager")
	if err != nil {
		fmt.Printf("failed to alias employees: %s\n", err)
		return
	}
	managerSource, err := rasql.SourceOf(manager, "manager")
	if err != nil {
		fmt.Printf("failed to bind manager source: %s\n", err)
		return
	}
	managerID, err := rasql.BindColumn[store.EmployeesRow, int64](managerSource, employees.IDRef().Name(), "")
	if err != nil {
		fmt.Printf("failed to bind manager id column: %s\n", err)
		return
	}
	managerName, err := rasql.BindColumn[store.EmployeesRow, string](managerSource, employees.NameRef().Name(), "")
	if err != nil {
		fmt.Printf("failed to bind manager name column: %s\n", err)
		return
	}

	result, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "name", Type: schema.TextType{}},
		rasql.ResultColumn{Name: "manager_name", Type: schema.TextType{}},
	)
	if err != nil {
		fmt.Printf("failed to build result schema: %s\n", err)
		return
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("name", employeeName.Expr(), schema.TextType{}, ""),
		rasql.Item("manager_name", managerName.Expr(), schema.TextType{}, ""),
	}, managedEmployeeDecoder{result: result})
	if err != nil {
		fmt.Printf("failed to build projection: %s\n", err)
		return
	}

	// SQL: SELECT employees.name, manager.name FROM employees INNER JOIN employees AS manager ON employees.manager_id = manager.id ORDER BY employees.id ASC
	q := rasql.Select(employeesSource.Source(), projection).
		Join(managerSource.Source(), rasql.EqualOptional(managerID.Expr(), employeeManagerID.NullExpr())).
		OrderBy(rasql.AscExpr(employeeID.Expr()))
	rows, err := rasql.All(ctx, executor, q)
	if err != nil {
		fmt.Printf("failed to query employees: %s\n", err)
		return
	}
	// The inner join drops ada, whose manager_id is NULL.
	for _, employee := range rows {
		fmt.Println(employee.Name, "reports to", employee.ManagerName)
	}

	// Output:
	// grace reports to ada
	// edsger reports to ada
}
