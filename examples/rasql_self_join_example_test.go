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

	db, err := rasql.Open(ctx, database, dialect.SQLite())
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
	// generated field carries a validity flag.
	ada := rasql.Nullable[int64]{Value: 1, Valid: true}
	for _, employee := range []store.EmployeesRow{
		{ID: 1, Name: "ada"},
		{ID: 2, Name: "grace", ManagerID: ada},
		{ID: 3, Name: "edsger", ManagerID: ada},
	} {
		create := store.Employees().Create().ID(employee.ID).Name(employee.Name)
		if employee.ManagerID.Valid {
			create = create.ManagerID(employee.ManagerID.Value)
		}
		plan, err := create.Plan()
		if err != nil {
			fmt.Printf("failed to build insert: %s\n", err)
			return
		}
		if _, err := rasql.Exec(ctx, db, plan); err != nil {
			fmt.Printf("failed to insert employee: %s\n", err)
			return
		}
	}

	// As names the second appearance of the table, and each Bind reads the
	// appearance it was given, so the two column sets stay apart.
	managerSource, err := employees.As("manager")
	if err != nil {
		fmt.Printf("failed to alias employees: %s\n", err)
		return
	}
	employeeColumns, err := (store.EmployeesColumns{}).Bind(employees)
	if err != nil {
		fmt.Printf("failed to bind employees columns: %s\n", err)
		return
	}
	managerColumns, err := (store.EmployeesColumns{}).Bind(managerSource)
	if err != nil {
		fmt.Printf("failed to bind manager columns: %s\n", err)
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
		rasql.Item("name", employeeColumns.Name.Expr(), schema.TextType{}, ""),
		rasql.Item("manager_name", managerColumns.Name.Expr(), schema.TextType{}, ""),
	}, managedEmployeeDecoder{result: result})
	if err != nil {
		fmt.Printf("failed to build projection: %s\n", err)
		return
	}

	// SQL: SELECT employees.name, manager.name FROM employees INNER JOIN employees AS manager ON employees.manager_id = manager.id ORDER BY employees.id ASC
	q := rasql.Select(employees, projection).
		Join(managerSource, rasql.EqualOptional(managerColumns.ID.Expr(), employeeColumns.ManagerID.NullExpr())).
		OrderBy(rasql.AscExpr(employeeColumns.ID.Expr()))
	rows, err := rasql.All(ctx, db, q)
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
