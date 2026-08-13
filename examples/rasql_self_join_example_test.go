package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

// EmployeeRow is one row of the employees table, which points at itself through
// manager_id. A top-level employee has no manager, so the column is nullable and
// the field is a pointer.
type EmployeeRow struct {
	ID        int64  `rasql:"id"`
	Name      string `rasql:"name"`
	ManagerID *int64 `rasql:"manager_id"`
}

// EmployeesTable has the shape rasqlgen emits: the typed table plus one
// accessor method per column.
type EmployeesTable struct {
	rasql.Table[EmployeeRow]
}

func (t EmployeesTable) ID() query.ColumnRef        { return rasql.ColumnOf(t.Table, "id") }
func (t EmployeesTable) Name() query.ColumnRef      { return rasql.ColumnOf(t.Table, "name") }
func (t EmployeesTable) ManagerID() query.ColumnRef { return rasql.ColumnOf(t.Table, "manager_id") }

var employees = EmployeesTable{rasql.MustTableOf[EmployeeRow](schema.MustTableDef("employees",
	schema.Integer("id"),
	schema.Text("name"),
	schema.Integer("manager_id", schema.Nullable()),
	schema.PrimaryKey("id"),
))}

// As returns the table under alias.
func (t EmployeesTable) As(alias string) (EmployeesTable, error) {
	aliased, err := rasql.As(t.Table, alias)
	if err != nil {
		return EmployeesTable{}, err
	}
	return EmployeesTable{Table: aliased}, nil
}

// Example_rasql_self_join joins a table to itself. The alias is what keeps the
// two sides apart, and the column accessors read whichever table value they
// are called on, so the join condition names one side through employees and
// the other through manager.
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
	if err := rasql.CreateTable(ctx, db, employees); err != nil {
		fmt.Printf("failed to create employees table: %s\n", err)
		return
	}
	ada := int64(1)
	for _, employee := range []EmployeeRow{
		{ID: 1, Name: "ada"},
		{ID: 2, Name: "grace", ManagerID: &ada},
		{ID: 3, Name: "edsger", ManagerID: &ada},
	} {
		if _, err := rasql.Insert(ctx, db, employees, employee); err != nil {
			fmt.Printf("failed to insert employee: %s\n", err)
			return
		}
	}

	// SQL: SELECT employees.id, employees.name, employees.manager_id FROM employees INNER JOIN employees AS manager ON (employees.manager_id = manager.id) ORDER BY employees.id ASC
	// BEGIN(self_join)
	manager, err := employees.As("manager")
	if err != nil {
		fmt.Printf("failed to alias employees: %s\n", err)
		return
	}
	rows, err := rasql.SelectFrom(employees).
		Join(rasql.InnerJoin(manager, query.Equal(employees.ManagerID(), manager.ID()))).
		OrderAsc(employees.ID()).
		Query(ctx, db)
	// END(self_join)
	if err != nil {
		fmt.Printf("failed to query employees: %s\n", err)
		return
	}
	// The inner join drops ada, whose manager_id is NULL.
	for employee, err := range rows {
		if err != nil {
			fmt.Printf("failed to read employee: %s\n", err)
			return
		}
		fmt.Println(employee.Name, "reports to", *employee.ManagerID)
	}

	// Output:
	// grace reports to 1
	// edsger reports to 1
}
