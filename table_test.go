package rasql_test

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type staffRow struct {
	ID        int64  `rasql:"id"`
	ManagerID int64  `rasql:"manager_id"`
	Email     string `rasql:"email"`
}

// staffTable mirrors the wrapper type rasqlgen emits: the typed table is
// embedded and every column is reachable through an accessor method.
type staffTable struct {
	rasql.Table[staffRow]
}

func (t staffTable) ID() query.ColumnRef        { return rasql.ColumnOf(t.Table, "id") }
func (t staffTable) ManagerID() query.ColumnRef { return rasql.ColumnOf(t.Table, "manager_id") }
func (t staffTable) Email() query.ColumnRef     { return rasql.ColumnOf(t.Table, "email") }

func (t staffTable) As(alias string) (staffTable, error) {
	aliased, err := rasql.As(t.Table, alias)
	if err != nil {
		return staffTable{}, err
	}
	return staffTable{Table: aliased}, nil
}

// auditedStaffTable mirrors a wrapper around a wrapper: it reaches
// rasql.Table[staffRow] through the embedded staffTable rather than directly.
type auditedStaffTable struct {
	staffTable
}

// selfMethodStaffTable mirrors a table that supplies its own Ref and
// Column and keeps the embedded rasql.Table[staffRow] nil, using it only for the
// unexported method that satisfies the interface. It is usable even though that
// embedded field is nil.
type selfMethodStaffTable struct {
	rasql.Table[staffRow]
	source query.TableRef
}

func (t selfMethodStaffTable) Ref() query.TableRef {
	return t.source
}

func (t selfMethodStaffTable) Column(name string) query.ColumnRef {
	return t.source.Column(name)
}

func staffDefinition() schema.TableDef {
	return schema.TableDef{
		Name: "staff",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "manager_id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	}
}

func staff(t *testing.T) staffTable {
	t.Helper()

	table, err := rasql.TableOf[staffRow](staffDefinition())
	require.NoError(t, err)
	return staffTable{Table: table}
}

// contractors is a second table, so a statement can take a staff table under
// test without duplicating the reference of the table it selects from.
func contractors(t *testing.T) rasql.Table[staffRow] {
	t.Helper()

	table, err := rasql.TableOf[staffRow](schema.TableDef{
		Name:       "contractors",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	return table
}

func TestTable(t *testing.T) {
	t.Run("describes a table", func(t *testing.T) {
		t.Run("Column resolves and rejects names", func(t *testing.T) {
			table, err := rasql.TableOf[staffRow](staffDefinition())
			require.NoError(t, err)

			column := table.Column("email")
			require.Equal(t, "email", column.Name())
			require.Equal(t, "staff", column.Source().Qualifier())

			require.ErrorContains(t, table.Column("missing").Validate(), "missing")
		})

		t.Run("Ref exposes the validated definition", func(t *testing.T) {
			table, err := rasql.TableOf[staffRow](staffDefinition())
			require.NoError(t, err)
			require.Equal(t, "staff", table.Ref().Name())
			require.Equal(t, staffDefinition().Columns, table.Ref().Definition().Columns)
			require.Equal(t, []string{"id"}, table.Ref().Definition().PrimaryKey)
		})

		t.Run("NewTable rejects an invalid definition", func(t *testing.T) {
			_, err := rasql.TableOf[staffRow](schema.TableDef{})
			require.Error(t, err)
			require.Panics(t, func() {
				rasql.MustTableOf[staffRow](schema.TableDef{})
			})
		})
	})

	t.Run("binds a column", func(t *testing.T) {
		t.Run("zero ColumnRef for a nil table", func(t *testing.T) {
			require.Equal(t, query.ColumnRef{}, rasql.ColumnOf[staffRow](nil, "id"))
		})

		t.Run("keeps name and source for a column the table does not have", func(t *testing.T) {
			table, err := rasql.TableOf[staffRow](staffDefinition())
			require.NoError(t, err)

			column := rasql.ColumnOf(table, "missing")
			require.Equal(t, "missing", column.Name())
			require.Equal(t, query.Relation(table.Ref()), column.Source())

			_, err = query.NewSelect(table.Ref(), column)
			require.ErrorContains(t, err, `references unknown column "missing"`)
		})

		t.Run("a zero generated-shape wrapper's accessor fails at Build, not a panic", func(t *testing.T) {
			var zero staffTable
			var err error
			require.NotPanics(t, func() {
				_, err = render.SelectFrom(dbForBuild(t).Dialect(), staff(t).Ref()).
					Select("id").
					Where(query.Equal(zero.ID(), 1)).
					Build()
			})
			require.ErrorContains(t, err, "query table: table must not be nil")
		})
	})

	t.Run("aliases a source", func(t *testing.T) {
		t.Run("qualifies every column accessor under the alias", func(t *testing.T) {
			manager, err := staff(t).As("manager")
			require.NoError(t, err)

			require.Equal(t, "manager", manager.Ref().Qualifier())
			require.Equal(t, "manager", manager.ID().Source().Qualifier())
			require.Equal(t, "manager", manager.ManagerID().Source().Qualifier())
			require.Equal(t, "manager", manager.Email().Source().Qualifier())
		})

		t.Run("rejects an invalid alias", func(t *testing.T) {
			_, err := staff(t).As("not an identifier")
			require.Error(t, err)
		})

		t.Run("self-join renders the alias qualifier", func(t *testing.T) {
			employees := staff(t)
			manager, err := employees.As("manager")
			require.NoError(t, err)

			statement, err := render.SelectFrom(dbForBuild(t).Dialect(), employees.Ref()).
				Select("id", "manager_id", "email").
				Join(query.InnerJoin(query.Relation(manager.Ref()), query.Equal(employees.ManagerID(), manager.ID()))).
				Order(query.Asc(manager.Email())).
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

			statement, err := render.SelectFrom(dbForBuild(t).Dialect(), employees.Ref()).
				Select("id", "manager_id", "email").
				Join(query.LeftJoin(query.Relation(manager.Ref()), query.Equal(employees.ManagerID(), manager.ID()))).
				Build()
			require.NoError(t, err)
			require.Contains(t, statement.SQL(), `LEFT JOIN "staff" AS "manager" ON ("staff"."manager_id" = "manager"."id")`)
		})
	})

	t.Run("a select builder rejects a foreign column", func(t *testing.T) {
		contractorID := contractors(t).Column("id")

		_, err := render.SelectFrom(dbForBuild(t).Dialect(), staff(t).Ref()).
			Select("id").
			Where(query.Equal(contractorID, 1)).
			Build()
		require.ErrorContains(t, err, "contractors")
	})

	t.Run("a nil table reports errors", func(t *testing.T) {
		requireNilTableRejected[rasql.Table[staffRow]](t, "nil interface", nil)

		failed, err := rasql.TableOf[staffRow](schema.TableDef{})
		require.Error(t, err)
		require.Nil(t, failed)
		requireNilTableRejected(t, "nil table from a failed NewTable", failed)

		t.Run("a generated As reports the error behind the zero wrapper it returns", func(t *testing.T) {
			var wrapper staffTable
			aliased, err := wrapper.As("alias")
			require.ErrorContains(t, err, "must not be nil")
			require.Equal(t, staffTable{}, aliased)
		})

		// A wrapper value is not the nil interface, so the contract check in As
		// cannot see it: Ref promotes through the wrapper's nil embedded
		// Table[staffRow] and the runtime panics, naming the caller that built
		// the value. Only the panic is asserted; the runtime's wording for a nil
		// dereference is not a contract.
		t.Run("a zero generated wrapper panics", func(t *testing.T) {
			require.Panics(t, func() {
				_, _ = rasql.As[staffRow](staffTable{}, "alias")
			})
		})
	})

	t.Run("a usable table is accepted", func(t *testing.T) {
		table, err := rasql.TableOf[staffRow](staffDefinition())
		require.NoError(t, err)

		requireTableUsable(t, "typed table", table)
		requireTableUsable(t, "wrapper around a typed table", staff(t))
		requireTableUsable(t, "wrapper around a usable wrapper", auditedStaffTable{staffTable: staff(t)})
		requireTableUsable(t, "pointer to a usable wrapper", &staffTable{Table: table})
		requireTableUsable(t, "table with its own Ref and a nil embedded table", selfMethodStaffTable{source: table.Ref()})
	})
}

// nilTableEntryPoint is one exported entry point that takes a
// rasql.Table[staffRow]. Wrapper is the type of the value the call receives, so
// each case hands the entry point the table value a caller holds instead of one
// the test converted to rasql.Table[staffRow] first.
type nilTableEntryPoint[Wrapper rasql.Table[staffRow]] struct {
	name          string
	errorContains string
	run           func(t *testing.T, table Wrapper) error
}

// nilTableEntryPoints returns every entry point that reaches a table through
// rasql.Table[staffRow] in the canonical API. The old builder surface had a
// separate entry point for a read (SelectFrom), a partial read (DecodeFrom),
// and a join source (InnerJoin, LeftJoin), each deferring its own table check
// to Build; the canonical API routes every one of those through a single
// gate, SourceOf, which reports the nil table immediately instead of waiting
// for a later Build, so the four old cases collapse into the one "SourceOf"
// case below. Insert and InsertWithOptions collapse the same way into
// NewCreatePlan, which a DefaultField turns into what InsertWithOptions was
// for. ColumnOf is missing because it returns the zero ColumnRef rather than
// reporting an error; requireNilTableRejected covers it separately. Every entry
// point here reports the nil interface only.
func nilTableEntryPoints[Wrapper rasql.Table[staffRow]]() []nilTableEntryPoint[Wrapper] {
	return []nilTableEntryPoint[Wrapper]{
		{
			name:          "SourceOf",
			errorContains: "must not be nil",
			run: func(t *testing.T, table Wrapper) error {
				_, err := rasql.SourceOf[staffRow](table, "")
				return err
			},
		},
		{
			name:          "NewCreatePlan",
			errorContains: "must not be nil",
			run: func(t *testing.T, table Wrapper) error {
				_, err := rasql.NewCreatePlan[staffRow](table)
				return err
			},
		},
		{
			name:          "NewPatchPlan",
			errorContains: "must not be nil",
			run: func(t *testing.T, table Wrapper) error {
				_, err := rasql.NewPatchPlan[staffRow, rasql.Predicate](table, rasql.Predicate{})
				return err
			},
		},
		{
			name:          "NewDeletePlan",
			errorContains: "must not be nil",
			run: func(t *testing.T, table Wrapper) error {
				_, err := rasql.NewDeletePlan[staffRow](table, query.Predicate{})
				return err
			},
		},
		{
			name:          "Create",
			errorContains: "must not be nil",
			run: func(t *testing.T, table Wrapper) error {
				return rasql.CreateTable[staffRow](t.Context(), dbForBuild(t), table)
			},
		},
		{
			name:          "As",
			errorContains: "must not be nil",
			run: func(t *testing.T, table Wrapper) error {
				_, err := rasql.As[staffRow](table, "alias")
				return err
			},
		},
	}
}

// requireNilTableRejected drives table through every typed entry point and
// requires each one to report the nil table instead of panicking. Only the nil
// interface reaches it. A wrapper whose embedded Table[staffRow] is nil is not
// the nil interface and panics at these entry points instead, which TestTable's
// "a zero generated wrapper panics" subtest pins.
func requireNilTableRejected[Wrapper rasql.Table[staffRow]](t *testing.T, name string, table Wrapper) {
	t.Helper()

	t.Run(name, func(t *testing.T) {
		for _, entryPoint := range nilTableEntryPoints[Wrapper]() {
			t.Run(entryPoint.name, func(t *testing.T) {
				var err error
				require.NotPanics(t, func() {
					err = entryPoint.run(t, table)
				})
				require.ErrorContains(t, err, entryPoint.errorContains)
			})
		}

		t.Run("ColumnOf", func(t *testing.T) {
			require.Equal(t, query.ColumnRef{}, rasql.ColumnOf[staffRow](table, "id"))
		})
	})
}

// requireTableUsable drives table through the entry points that build a
// statement without executing it and requires each one to reach the table behind
// it, so a value the guard must accept is proven usable rather than merely not
// rejected.
func requireTableUsable[Wrapper rasql.Table[staffRow]](t *testing.T, name string, table Wrapper) {
	t.Helper()

	t.Run(name, func(t *testing.T) {
		// SourceOf's own guard is exercised by requireNilTableRejected; here
		// the table is valid, so a plain render through its real Ref proves
		// the same reachability SelectFrom and DecodeFrom used to prove,
		// which the canonical API folds into the one SourceOf gate.
		selected, err := render.SelectFrom(dbForBuild(t).Dialect(), table.Ref()).Select("id").Build()
		require.NoError(t, err)
		require.Contains(t, selected.SQL(), `FROM "staff"`)

		deleteStmt, err := query.NewDelete(table.Ref())
		require.NoError(t, err)
		deleteStmt, err = deleteStmt.AllowAll()
		require.NoError(t, err)
		deleted, err := render.Delete(dbForBuild(t).Dialect(), deleteStmt)
		require.NoError(t, err)
		require.Contains(t, deleted.SQL(), `DELETE FROM "staff"`)

		aliased, err := rasql.As[staffRow](table, "alias")
		require.NoError(t, err)
		require.Equal(t, "alias", aliased.Ref().Qualifier())

		require.Equal(t, "email", rasql.ColumnOf[staffRow](table, "email").Name())

		others := contractors(t)
		othersID := others.Column("id")

		joined, err := render.SelectFrom(dbForBuild(t).Dialect(), others.Ref()).
			Select("id").
			Join(query.InnerJoin(query.Relation(table.Ref()), query.Equal(othersID, query.Bind(1)))).
			Build()
		require.NoError(t, err)
		require.Contains(t, joined.SQL(), `INNER JOIN "staff"`)

		left, err := render.SelectFrom(dbForBuild(t).Dialect(), others.Ref()).
			Select("id").
			Join(query.LeftJoin(query.Relation(table.Ref()), query.Equal(othersID, query.Bind(1)))).
			Build()
		require.NoError(t, err)
		require.Contains(t, left.SQL(), `LEFT JOIN "staff"`)
	})
}

type viewCapabilityRow struct {
	ID int64 `rasql:"id"`
}

func TestTableCapabilities(t *testing.T) {
	t.Run("constructors require write capabilities", func(t *testing.T) {
		view := schema.TableDef{
			Name:       "active_users",
			Kind:       schema.ObjectView,
			Operations: schema.OperationRead,
			Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		}
		read, err := rasql.ReadTableOf[viewCapabilityRow](view)
		require.NoError(t, err)
		require.NotNil(t, read)
		_, err = rasql.TableOf[viewCapabilityRow](view)
		require.ErrorContains(t, err, "does not support operation")
		require.Panics(t, func() { rasql.MustTableOf[viewCapabilityRow](view) })

		writableView := view
		writableView.Operations = schema.OperationRead | schema.OperationInsert | schema.OperationUpdate | schema.OperationDelete
		table, err := rasql.TableOf[viewCapabilityRow](writableView)
		require.NoError(t, err)
		require.NotNil(t, table)
	})

	// TestTableCapabilities/"a forged writable handle rejects every mutation" proves that a Table[T] handle
	// which bypassed TableOf, and whose schema does not support a given
	// mutation, is refused for every mutation kind: insert, update, delete and
	// DDL alike. TableFrom is the entry point that skips TableOf's own check,
	// which is exactly how a forged handle reaches NewCreatePlan, NewPatchPlan,
	// NewDeletePlan or CreateTable without ever passing through TableOf, so each
	// constructor is required to check the capability again itself.
	t.Run("a forged writable handle rejects every mutation", func(t *testing.T) {
		database, mock, err := sqlmock.New()
		require.NoError(t, err)
		t.Cleanup(func() { mock.ExpectClose(); require.NoError(t, database.Close()) })
		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		view := schema.TableDef{Name: "active_users", Kind: schema.ObjectView, Operations: schema.OperationRead, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}}
		table := rasql.TableFrom[viewCapabilityRow](view)
		id := query.TypedColumnOf[viewCapabilityRow, int64](table.Column("id"))
		for name, call := range map[string]func() error{
			"insert": func() error {
				_, err := rasql.NewCreatePlan(table, rasql.SetField[viewCapabilityRow, int64](id, 1))
				return err
			},
			"update": func() error {
				_, err := rasql.NewPatchPlan(table, query.EqualValue(id, int64(1)), rasql.SetField[viewCapabilityRow, int64](id, 1))
				return err
			},
			"delete": func() error {
				_, err := rasql.NewDeletePlan(table, query.EqualValue(id, int64(1)))
				return err
			},
			"ddl": func() error { return rasql.CreateTable(t.Context(), db, table) },
		} {
			t.Run(name, func(t *testing.T) { require.ErrorContains(t, call(), "does not support") })
		}
	})
}
