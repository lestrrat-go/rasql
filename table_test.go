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

// staffTable mirrors the wrapper type rasqlgen emits: the typed table is held
// as a field and every column is reachable through an accessor method.
type staffTable struct {
	rasql.Table[staffRow]
}

func (t staffTable) ID() query.ColumnRef        { return t.Column("id") }
func (t staffTable) ManagerID() query.ColumnRef { return t.Column("manager_id") }
func (t staffTable) Email() query.ColumnRef     { return t.Column("email") }

func (t staffTable) As(alias string) (staffTable, error) {
	aliased, err := t.Table.As(alias)
	if err != nil {
		return staffTable{}, err
	}
	return staffTable{Table: aliased}, nil
}

// InSchema mirrors the method rasqlgen's compact emitter gains once the
// generator half of this design lands: one call through Table.InSchema, and
// the moved table back inside the same wrapper type.
func (t staffTable) InSchema(namespace string) (staffTable, error) {
	moved, err := t.Table.InSchema(namespace)
	if err != nil {
		return staffTable{}, err
	}
	return staffTable{Table: moved}, nil
}

// auditedStaffTable mirrors a wrapper around a wrapper: it reaches
// rasql.Table[staffRow] through the embedded staffTable rather than directly.
type auditedStaffTable struct {
	staffTable
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

		t.Run("TableOf rejects an invalid definition", func(t *testing.T) {
			_, err := rasql.TableOf[staffRow](schema.TableDef{})
			require.Error(t, err)
			require.Panics(t, func() {
				rasql.MustTableOf[staffRow](schema.TableDef{})
			})
		})

		t.Run("TableOf accepts a descriptor no write reaches", func(t *testing.T) {
			// requireWritableDefinition used to demand insert, update and
			// delete together here, so a descriptor permitting only some of
			// the three had to be built through a second constructor that
			// permitted none of them. Every operation is checked where it is
			// performed instead; see TestTableOperations.
			definition := staffDefinition()
			definition.Operations = schema.OperationRead
			table, err := rasql.TableOf[staffRow](definition)
			require.NoError(t, err)
			require.Equal(t, "staff", table.Ref().Name())
		})
	})

	t.Run("binds a column", func(t *testing.T) {
		t.Run("keeps name and source for a column the table does not have", func(t *testing.T) {
			table, err := rasql.TableOf[staffRow](staffDefinition())
			require.NoError(t, err)

			column := table.Column("missing")
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

	t.Run("a table that carries a descriptor is usable", func(t *testing.T) {
		table, err := rasql.TableOf[staffRow](staffDefinition())
		require.NoError(t, err)

		requireTableUsable(t, "the handle itself", table)
		requireTableUsable(t, "a wrapper holding it", staff(t).Table)
		requireTableUsable(t, "a wrapper around a wrapper", auditedStaffTable{staffTable: staff(t)}.Table)
	})
}

// zeroTableEntryPoints returns every exported entry point that reads a table
// through rasql.Table[staffRow]. Each one takes the zero handle, which carries
// no descriptor, and each is required to report query.ErrNilTable rather than
// panic.
//
// The zero handle is the value a generated wrapper holds after its As fails,
// and the value a caller gets from a var declaration. Before Table[T] was a
// struct it was a nil interface at one of those and a non-nil interface over a
// nil pointer at the other, and only the first was caught.
func zeroTableEntryPoints() map[string]func(t *testing.T, table rasql.Table[staffRow]) error {
	return map[string]func(t *testing.T, table rasql.Table[staffRow]) error{
		"Source": func(_ *testing.T, table rasql.Table[staffRow]) error {
			_, err := table.Source("")
			return err
		},
		"As": func(_ *testing.T, table rasql.Table[staffRow]) error {
			_, err := table.As("alias")
			return err
		},
		"InSchema": func(_ *testing.T, table rasql.Table[staffRow]) error {
			_, err := table.InSchema("tenant_0001")
			return err
		},
		"NewCreatePlan": func(_ *testing.T, table rasql.Table[staffRow]) error {
			_, err := rasql.NewCreatePlan(table)
			return err
		},
		"NewPatchPlan": func(_ *testing.T, table rasql.Table[staffRow]) error {
			_, err := rasql.NewPatchPlan[staffRow, rasql.Predicate](table, rasql.Predicate{})
			return err
		},
		"NewDeletePlan": func(_ *testing.T, table rasql.Table[staffRow]) error {
			_, err := rasql.NewDeletePlan(table, query.Predicate{})
			return err
		},
		"CreateTable": func(t *testing.T, table rasql.Table[staffRow]) error {
			return rasql.CreateTable(t.Context(), dbForBuild(t), table.Ref())
		},
	}
}

func TestZeroTable(t *testing.T) {
	t.Run("every entry point reports query.ErrNilTable", func(t *testing.T) {
		for name, call := range zeroTableEntryPoints() {
			t.Run(name, func(t *testing.T) {
				var zero rasql.Table[staffRow]
				var err error
				require.NotPanics(t, func() { err = call(t, zero) })
				require.ErrorIs(t, err, query.ErrNilTable)
			})
		}
	})

	t.Run("a generated wrapper holding one reports the same error", func(t *testing.T) {
		var wrapper staffTable
		aliased, err := wrapper.As("alias")
		require.ErrorIs(t, err, query.ErrNilTable)
		require.Equal(t, staffTable{}, aliased)

		_, err = wrapper.Source("")
		require.ErrorIs(t, err, query.ErrNilTable)
	})

	t.Run("Column answers with a ColumnRef that reports it", func(t *testing.T) {
		// A generated accessor returns a ColumnRef alone, so Column reports
		// the zero table through the value it hands back rather than through
		// an error its signature cannot carry.
		var zero rasql.Table[staffRow]
		require.ErrorIs(t, zero.Column("id").Validate(), query.ErrNilTable)
	})
}

// requireTableUsable drives table through the entry points that build a
// statement without executing it and requires each one to reach the table
// behind it, so a value the guard must accept is proven usable rather than
// merely not rejected.
func requireTableUsable(t *testing.T, name string, table rasql.Table[staffRow]) {
	t.Helper()

	t.Run(name, func(t *testing.T) {
		_, err := table.Source("")
		require.NoError(t, err)

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

		aliased, err := table.As("alias")
		require.NoError(t, err)
		require.Equal(t, "alias", aliased.Ref().Qualifier())

		require.Equal(t, "email", table.Column("email").Name())

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

func viewCapabilityDB(t *testing.T) rasql.DB {
	t.Helper()

	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { mock.ExpectClose(); require.NoError(t, database.Close()) })
	// A descriptor that does permit DDL reaches the server, so the mock
	// answers the one CREATE TABLE the ddl case below sends. A descriptor
	// that does not never sends it, and the expectation goes unused, so the
	// mock is told not to require the order it was registered in.
	mock.MatchExpectationsInOrder(false)
	mock.ExpectExec("CREATE TABLE").WillReturnResult(sqlmock.NewResult(0, 0))
	db, err := rasql.Open(t.Context(), database, dialect.SQLite(), rasql.WithProfile(rasql.SQLite335()))
	require.NoError(t, err)
	return db
}

// capabilityCalls returns the five operations a descriptor can permit, each
// spelled the way a caller performs it. The name is the operation, so a
// subtest that fails names the bit that decided the outcome.
func capabilityCalls(t *testing.T, table rasql.Table[viewCapabilityRow]) map[string]func() error {
	t.Helper()

	id := query.TypedColumnOf[viewCapabilityRow, int64](table.Column("id"))
	db := viewCapabilityDB(t)
	return map[string]func() error{
		"read": func() error {
			_, err := table.Source("")
			return err
		},
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
		"ddl": func() error { return rasql.CreateTable(t.Context(), db, table.Ref()) },
	}
}

// requireCapabilities runs every operation against table and requires each one
// to be accepted exactly when permitted names it.
func requireCapabilities(t *testing.T, table rasql.Table[viewCapabilityRow], permitted ...string) {
	t.Helper()

	allowed := make(map[string]struct{}, len(permitted))
	for _, name := range permitted {
		allowed[name] = struct{}{}
	}
	for name, call := range capabilityCalls(t, table) {
		t.Run(name, func(t *testing.T) {
			err := call()
			if _, ok := allowed[name]; ok {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, "does not support")
		})
	}
}

// TestTableOperations pins the per-operation refusals that replaced
// requireWritableDefinition. A handle is built for a descriptor whatever it
// permits, and each of the five operations checks its own bit where the
// statement is assembled: insert in NewCreatePlan, update in NewPatchPlan,
// delete in NewDeletePlan, DDL in CreateTable, and read in Table.Source.
func TestTableOperations(t *testing.T) {
	viewDefinition := schema.TableDef{
		Name:    "active_users",
		Kind:    schema.ObjectView,
		Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
	}

	t.Run("a view states no operations and is read-only through its Kind", func(t *testing.T) {
		table, err := rasql.TableOf[viewCapabilityRow](viewDefinition)
		require.NoError(t, err)
		requireCapabilities(t, table, "read")
	})

	t.Run("a view naming the writes permits them", func(t *testing.T) {
		writable := viewDefinition
		writable.Operations = schema.OperationRead | schema.OperationInsert | schema.OperationUpdate |
			schema.OperationDelete | schema.OperationDDL
		table, err := rasql.TableOf[viewCapabilityRow](writable)
		require.NoError(t, err)
		requireCapabilities(t, table, "read", "insert", "update", "delete", "ddl")
	})

	t.Run("a descriptor stating Read and Insert refuses the other three", func(t *testing.T) {
		// This is the shape requireWritableDefinition refused outright: an
		// append-only table a caller inserts into and never updates or
		// deletes from.
		appendOnly := schema.TableDef{
			Name:       "audit_log",
			Operations: schema.OperationRead | schema.OperationInsert,
			Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		}
		table, err := rasql.TableOf[viewCapabilityRow](appendOnly)
		require.NoError(t, err)
		requireCapabilities(t, table, "read", "insert")
	})

	t.Run("a descriptor withholding read refuses Source", func(t *testing.T) {
		// Nothing in this repository read OperationRead before Source did.
		writeOnly := schema.TableDef{
			Name:       "outbox",
			Operations: schema.OperationInsert,
			Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		}
		table, err := rasql.TableOf[viewCapabilityRow](writeOnly)
		require.NoError(t, err)
		_, err = table.Source("")
		require.ErrorContains(t, err, `object "outbox" does not support operation 1`)
	})

	// TableFrom validates nothing, so it is how a handle reaches a mutation
	// constructor without TableOf ever having seen the descriptor. Each
	// constructor is required to check the capability again itself.
	t.Run("a handle built through TableFrom is refused the same way", func(t *testing.T) {
		table := rasql.TableFrom[viewCapabilityRow](viewDefinition)
		requireCapabilities(t, table, "read")
	})
}

// TestInSchema requires that a caller can point a generated table at a
// namespace while the program runs. Where a store is generated is rarely where
// it runs: a MySQL deployment copies one schema into tenant_0001 and
// tenant_0002, and a store generated against a development database has to
// reach production without being regenerated.
func TestInSchema(t *testing.T) {
	t.Run("moves a copy and leaves the original alone", func(t *testing.T) {
		original := staff(t)
		moved, err := original.InSchema("tenant_0001")
		require.NoError(t, err)

		require.Equal(t, "tenant_0001", moved.Ref().Schema())
		require.Equal(t, "tenant_0001.staff", moved.Ref().QualifiedName())
		require.Equal(t, "", original.Ref().Schema())
		require.Equal(t, "staff", original.Ref().QualifiedName())

		// Every accessor answers off the moved table, without the wrapper
		// rebinding a single column.
		require.Equal(t, "id", moved.ID().Name())
		require.Equal(t, "staff", moved.ID().Source().Qualifier())
		require.NoError(t, moved.Email().Validate())
	})

	t.Run("every statement kind names the new namespace", func(t *testing.T) {
		moved, err := staff(t).InSchema("tenant_0001")
		require.NoError(t, err)

		statement, err := render.SelectFrom(dbForBuild(t).Dialect(), moved.Ref()).
			Select("id", "email").
			Where(query.Equal(moved.ID(), 1)).
			Build()
		require.NoError(t, err)
		require.Equal(
			t,
			`SELECT "tenant_0001"."staff"."id", "tenant_0001"."staff"."email" `+
				`FROM "tenant_0001"."staff" WHERE ("tenant_0001"."staff"."id" = $1)`,
			statement.SQL(),
		)

		update, err := query.NewUpdate(moved.Ref(), query.Set(moved.Email(), query.Bind("ada@example.com")))
		require.NoError(t, err)
		update, err = update.WithWhere(query.Equal(moved.ID(), 1))
		require.NoError(t, err)
		rendered, err := render.Update(dbForBuild(t).Dialect(), update)
		require.NoError(t, err)
		require.Equal(
			t,
			`UPDATE "tenant_0001"."staff" SET "email" = $1 WHERE ("tenant_0001"."staff"."id" = $2)`,
			rendered.SQL(),
		)
	})

	t.Run("a relation built from a moved table carries the namespace", func(t *testing.T) {
		// Source is how a generated table becomes something a query selects
		// from, so the namespace has to survive that step rather than only the
		// table wrapper.
		moved, err := staff(t).InSchema("tenant_0001")
		require.NoError(t, err)
		relation, err := moved.Source("")
		require.NoError(t, err)
		id, err := rasql.BindColumn[staffRow, int64](relation, "id", "")
		require.NoError(t, err)
		projection, err := rasql.Scalar("id", id.Expr(), schema.IntegerType{}, "")
		require.NoError(t, err)

		statement, err := rasql.Render(rasql.Select(relation.Source(), projection), dialect.PostgreSQL())
		require.NoError(t, err)
		require.Equal(
			t,
			`SELECT "tenant_0001"."staff"."id" AS "id" FROM "tenant_0001"."staff"`,
			statement.SQL(),
		)
	})

	t.Run("an alias still replaces the whole qualified name", func(t *testing.T) {
		// InSchema writes the descriptor's namespace and Source writes the
		// alias, two different fields, so the two compose in either order. What
		// the renderer does with the pair is today's rule unchanged: only the
		// FROM entry names the namespace, and every column renders under the
		// bare alias.
		moved, err := staff(t).InSchema("tenant_0001")
		require.NoError(t, err)
		relation, err := moved.Source("s")
		require.NoError(t, err)
		id, err := rasql.BindColumn[staffRow, int64](relation, "id", "")
		require.NoError(t, err)
		projection, err := rasql.Scalar("id", id.Expr(), schema.IntegerType{}, "")
		require.NoError(t, err)

		statement, err := rasql.Render(rasql.Select(relation.Source(), projection), dialect.PostgreSQL())
		require.NoError(t, err)
		require.Equal(
			t,
			`SELECT "s"."id" AS "id" FROM "tenant_0001"."staff" AS "s"`,
			statement.SQL(),
		)

		// The same pair written the other way round: alias first, then the
		// move.
		aliased, err := staff(t).As("s")
		require.NoError(t, err)
		aliasedThenMoved, err := aliased.InSchema("tenant_0001")
		require.NoError(t, err)
		require.Equal(t, "tenant_0001", aliasedThenMoved.Ref().Schema())
		require.Equal(t, "s", aliasedThenMoved.Ref().Alias())
		require.Equal(t, "", aliasedThenMoved.Ref().QualifierSchema())
	})

	t.Run("rejects an empty namespace", func(t *testing.T) {
		// An empty string reaching here from configuration that failed to load
		// would otherwise retarget every statement with nothing to report.
		_, err := staff(t).InSchema("")
		require.ErrorContains(t, err, "must not be empty")
	})

	t.Run("moves a read-only object", func(t *testing.T) {
		view, err := rasql.TableOf[staffRow](staffDefinition())
		require.NoError(t, err)

		moved, err := view.InSchema("tenant_0001")
		require.NoError(t, err)
		require.Equal(t, "tenant_0001", moved.Ref().Schema())
		require.Equal(t, "", view.Ref().Schema())
		require.NoError(t, moved.Column("email").Validate())

		_, err = view.InSchema("")
		require.ErrorContains(t, err, "must not be empty")
	})
}
