package rasql_test

import (
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type staffRow struct {
	ID        int64  `rasql:"id"`
	ManagerID int64  `rasql:"manager_id"`
	Email     string `rasql:"email"`
}

// staffTable mirrors the wrapper type rasqlgen emits: the typed table is
// embedded and every column is reachable as a field.
type staffTable struct {
	rasql.Table[staffRow]
	ID        query.Column
	ManagerID query.Column
	Email     query.Column
}

func newStaffTable(table rasql.Table[staffRow]) staffTable {
	return staffTable{
		Table:     table,
		ID:        rasql.MustColumn(table, "id"),
		ManagerID: rasql.MustColumn(table, "manager_id"),
		Email:     rasql.MustColumn(table, "email"),
	}
}

func (t staffTable) As(alias string) (staffTable, error) {
	aliased, err := rasql.As(t.Table, alias)
	if err != nil {
		return staffTable{}, err
	}
	return newStaffTable(aliased), nil
}

func staffDefinition() schema.Table {
	return schema.Table{
		Name: "staff",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "manager_id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	}
}

func staff(t *testing.T) staffTable {
	t.Helper()

	table, err := rasql.NewTable[staffRow](staffDefinition())
	require.NoError(t, err)
	return newStaffTable(table)
}

func TestTable(t *testing.T) {
	t.Run("Column resolves and rejects names", func(t *testing.T) {
		table, err := rasql.NewTable[staffRow](staffDefinition())
		require.NoError(t, err)

		column, err := table.Column("email")
		require.NoError(t, err)
		require.Equal(t, "email", column.Name())
		require.Equal(t, "staff", column.Source().Qualifier())

		_, err = table.Column("missing")
		require.ErrorContains(t, err, "missing")
	})

	t.Run("QueryTable exposes the validated definition", func(t *testing.T) {
		table, err := rasql.NewTable[staffRow](staffDefinition())
		require.NoError(t, err)
		require.Equal(t, "staff", table.QueryTable().Name())
		require.Equal(t, staffDefinition().Columns, table.QueryTable().Definition().Columns)
		require.Equal(t, []string{"id"}, table.QueryTable().Definition().PrimaryKey)
	})

	t.Run("NewTable rejects an invalid definition", func(t *testing.T) {
		_, err := rasql.NewTable[staffRow](schema.Table{})
		require.Error(t, err)
		require.Panics(t, func() {
			rasql.MustTable[staffRow](schema.Table{})
		})
	})
}

func TestMustColumn(t *testing.T) {
	table, err := rasql.NewTable[staffRow](staffDefinition())
	require.NoError(t, err)

	require.Equal(t, "id", rasql.MustColumn(table, "id").Name())
	require.Panics(t, func() {
		rasql.MustColumn(table, "missing")
	})
}

func TestAs(t *testing.T) {
	t.Run("rebinds every column field", func(t *testing.T) {
		manager, err := staff(t).As("manager")
		require.NoError(t, err)

		require.Equal(t, "manager", manager.QueryTable().Qualifier())
		require.Equal(t, "manager", manager.ID.Source().Qualifier())
		require.Equal(t, "manager", manager.ManagerID.Source().Qualifier())
		require.Equal(t, "manager", manager.Email.Source().Qualifier())
	})

	t.Run("rejects an invalid alias", func(t *testing.T) {
		_, err := staff(t).As("not an identifier")
		require.Error(t, err)
	})

	t.Run("self-join renders the alias qualifier", func(t *testing.T) {
		employees := staff(t)
		manager, err := employees.As("manager")
		require.NoError(t, err)

		statement, err := rasql.SelectFrom(clientForBuild(t), employees).
			Join(rasql.InnerJoin(manager, query.Equal(employees.ManagerID, manager.ID))).
			OrderAsc(manager.Email).
			Build()
		require.NoError(t, err)
		require.Equal(
			t,
			`SELECT "staff"."id", "staff"."manager_id", "staff"."email" FROM "staff" `+
				`INNER JOIN "staff" AS "manager" ON ("staff"."manager_id" = "manager"."id") `+
				`ORDER BY "manager"."email"`,
			statement.SQL(),
		)
	})

	t.Run("left join keeps the alias qualifier", func(t *testing.T) {
		employees := staff(t)
		manager, err := employees.As("manager")
		require.NoError(t, err)

		statement, err := rasql.SelectFrom(clientForBuild(t), employees).
			Join(rasql.LeftJoin(manager, query.Equal(employees.ManagerID, manager.ID))).
			Build()
		require.NoError(t, err)
		require.Contains(t, statement.SQL(), `LEFT JOIN "staff" AS "manager" ON ("staff"."manager_id" = "manager"."id")`)
	})
}

func TestTypedSelectBuilderRejectsForeignColumn(t *testing.T) {
	other, err := rasql.NewTable[staffRow](schema.Table{
		Name:       "contractors",
		Columns:    []schema.Column{{Name: "id", Type: schema.TypeInteger}},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	contractorID, err := other.Column("id")
	require.NoError(t, err)

	_, err = rasql.SelectFrom(clientForBuild(t), staff(t)).WhereEqual(contractorID, 1).Build()
	require.ErrorContains(t, err, "contractors")
}
