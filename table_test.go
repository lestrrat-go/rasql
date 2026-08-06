package rasql_test

import (
	"runtime"
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

// auditedStaffTable mirrors a wrapper around a wrapper: it reaches
// rasql.Table[staffRow] through the embedded staffTable rather than directly.
type auditedStaffTable struct {
	staffTable
}

// pointerStaffTable mirrors a wrapper that embeds its inner wrapper by pointer,
// so its zero value promotes every table method to a nil pointer.
type pointerStaffTable struct {
	*staffTable
}

// recursiveStaffTable mirrors a wrapper reachable from itself. Both anonymous
// fields satisfy rasql.Table[staffRow], and Go promotes the table methods from
// the shallower one, so its zero value still reaches them through a nil embedded
// rasql.Table[staffRow]. Nothing reads the recursive field: its presence is the
// shape under test.
type recursiveStaffTable struct {
	*recursiveStaffTable
	rasql.Table[staffRow]
}

// twoCandidateStaffTable mirrors the same two-candidate shape with no recursion
// in it: an ordinary wrapper sits beside the embedded interface Go promotes from.
type twoCandidateStaffTable struct {
	rasql.Table[staffRow]
	staffTable
}

// selfMethodStaffTable mirrors a table that supplies its own QueryTable and
// Column and keeps the embedded rasql.Table[staffRow] nil, using it only for the
// unexported method that satisfies the interface. It is usable even though that
// embedded field is nil.
type selfMethodStaffTable struct {
	rasql.Table[staffRow]
	source query.Table
}

func (t selfMethodStaffTable) QueryTable() query.Table {
	return t.source
}

func (t selfMethodStaffTable) Column(name string) (query.Column, error) {
	return t.source.Column(name)
}

// staffTableBug is what buggyStaffTable.QueryTable panics with.
const staffTableBug = "staff table: bug of its own"

// buggyStaffTable mirrors a table whose own QueryTable panics for a reason that
// has nothing to do with a nil table.
type buggyStaffTable struct {
	rasql.Table[staffRow]
}

func (t buggyStaffTable) QueryTable() query.Table {
	panic(staffTableBug)
}

// nilDereferenceStaffTable mirrors a wrapper with a VALID embedded
// rasql.Table[staffRow] whose own QueryTable panics with a nil pointer
// dereference for a reason that has nothing to do with the embedded table. The
// guard probes the unexported tableRow method first, and tableRow succeeds
// here because the embedded table is real, so the guard must never call this
// QueryTable itself: the panic must reach the caller unrelabelled.
type nilDereferenceStaffTable struct {
	rasql.Table[staffRow]
	source *query.Table
}

func (t nilDereferenceStaffTable) QueryTable() query.Table {
	return *t.source
}

// fabricatedNilDereference is a caller-declared type that satisfies
// runtime.Error and echoes the runtime's own nil-dereference text from its
// Error method, but is not the runtime's own concrete error type. It exists to
// prove the guard tells a genuine nil dereference apart from a caller
// panicking with a value fabricated to look like one, which is only possible
// because the classifier also checks which package the concrete type behind
// the panic comes from, not just its interface and text.
type fabricatedNilDereference struct{}

func (fabricatedNilDereference) Error() string {
	return "runtime error: invalid memory address or nil pointer dereference"
}

func (fabricatedNilDereference) RuntimeError() {}

// fabricatedRuntimeErrorStaffTable mirrors a caller type whose own QueryTable
// panics with fabricatedNilDereference: a value that satisfies runtime.Error
// and carries the runtime's own nil-dereference text, yet did not come from an
// actual nil dereference. Its embedded rasql.Table[staffRow] is nil, so
// tableRow nil-dereferences first and the guard proceeds to probe QueryTable,
// which is where this fabricated panic is raised. A classifier that matched
// only the interface and the text would swallow it as "table must not be nil"
// instead of letting the caller's own panic propagate.
type fabricatedRuntimeErrorStaffTable struct {
	rasql.Table[staffRow]
}

func (t fabricatedRuntimeErrorStaffTable) QueryTable() query.Table {
	panic(fabricatedNilDereference{})
}

// requirePanicsWithNilDereference runs fn and requires it to panic with the Go
// runtime's own nil pointer dereference error, proving the panic reached the
// caller unchanged instead of being caught and relabelled as a missing table.
func requirePanicsWithNilDereference(t *testing.T, fn func()) {
	t.Helper()

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		fn()
	}()

	require.NotNil(t, recovered, "expected a panic")
	failure, ok := recovered.(runtime.Error)
	require.Truef(t, ok, "expected a runtime.Error, got %T: %v", recovered, recovered)
	require.Contains(t, failure.Error(), "invalid memory address or nil pointer dereference")
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

// contractors is a second table, so a statement can take a staff table under
// test without duplicating the reference of the table it selects from.
func contractors(t *testing.T) rasql.Table[staffRow] {
	t.Helper()

	table, err := rasql.NewTable[staffRow](schema.Table{
		Name:       "contractors",
		Columns:    []schema.Column{{Name: "id", Type: schema.TypeInteger}},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	return table
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

		statement, err := rasql.SelectFrom(employees).
			Join(rasql.InnerJoin(manager, query.Equal(employees.ManagerID, manager.ID))).
			OrderAsc(manager.Email).
			Build(clientForBuild(t).Dialect())
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

		statement, err := rasql.SelectFrom(employees).
			Join(rasql.LeftJoin(manager, query.Equal(employees.ManagerID, manager.ID))).
			Build(clientForBuild(t).Dialect())
		require.NoError(t, err)
		require.Contains(t, statement.SQL(), `LEFT JOIN "staff" AS "manager" ON ("staff"."manager_id" = "manager"."id")`)
	})
}

func TestTypedSelectBuilderRejectsForeignColumn(t *testing.T) {
	contractorID, err := contractors(t).Column("id")
	require.NoError(t, err)

	_, err = rasql.SelectFrom(staff(t)).WhereEqual(contractorID, 1).Build(clientForBuild(t).Dialect())
	require.ErrorContains(t, err, "contractors")
}

// nilTableEntryPoint is one exported entry point that takes a
// rasql.Table[staffRow]. Wrapper is the type of the value the call receives, so
// each case hands the entry point the table value a caller holds instead of one
// the test converted to rasql.Table[staffRow] first.
type nilTableEntryPoint[Wrapper rasql.Table[staffRow]] struct {
	name string
	// errorContains is the text the reported error must carry. InnerJoin and
	// LeftJoin return a query.Join, which has no error channel, so they join an
	// empty table and the error arrives from rendering that table at Build.
	errorContains string
	run           func(t *testing.T, table Wrapper) error
}

// nilTableEntryPoints returns every entry point that reaches a table through
// rasql.Table[staffRow]. MustColumn is missing because it panics by contract;
// requireNilTableRejected covers it separately.
func nilTableEntryPoints[Wrapper rasql.Table[staffRow]]() []nilTableEntryPoint[Wrapper] {
	return []nilTableEntryPoint[Wrapper]{
		{
			name:          "SelectFrom",
			errorContains: "must not be nil",
			run: func(t *testing.T, table Wrapper) error {
				_, err := rasql.SelectFrom[staffRow](table).Build(clientForBuild(t).Dialect())
				return err
			},
		},
		{
			name:          "DecodeFrom",
			errorContains: "must not be nil",
			run: func(t *testing.T, table Wrapper) error {
				_, err := rasql.DecodeFrom[staffRow, staffRow](table).Build(clientForBuild(t).Dialect())
				return err
			},
		},
		{
			name:          "DeleteFrom",
			errorContains: "must not be nil",
			run: func(t *testing.T, table Wrapper) error {
				_, err := rasql.DeleteFrom[staffRow](table).Build(clientForBuild(t).Dialect())
				return err
			},
		},
		{
			name:          "Insert",
			errorContains: "must not be nil",
			run: func(t *testing.T, table Wrapper) error {
				_, err := rasql.Insert[staffRow](t.Context(), clientForBuild(t), table, staffRow{})
				return err
			},
		},
		{
			name:          "InsertWithOptions",
			errorContains: "must not be nil",
			run: func(t *testing.T, table Wrapper) error {
				_, err := rasql.InsertWithOptions[staffRow](t.Context(), clientForBuild(t), table, staffRow{})
				return err
			},
		},
		{
			name:          "Update",
			errorContains: "must not be nil",
			run: func(t *testing.T, table Wrapper) error {
				_, err := rasql.Update[staffRow](t.Context(), clientForBuild(t), table, staffRow{})
				return err
			},
		},
		{
			name:          "Create",
			errorContains: "must not be nil",
			run: func(t *testing.T, table Wrapper) error {
				return rasql.Create[staffRow](t.Context(), clientForBuild(t), table)
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
		{
			name:          "InnerJoin",
			errorContains: "must not be empty",
			run: func(t *testing.T, table Wrapper) error {
				employees := staff(t)
				_, err := rasql.SelectFrom(employees).
					Join(rasql.InnerJoin[staffRow](table, query.Equal(employees.ID, query.Bind(1)))).
					Build(clientForBuild(t).Dialect())
				return err
			},
		},
		{
			name:          "LeftJoin",
			errorContains: "must not be empty",
			run: func(t *testing.T, table Wrapper) error {
				employees := staff(t)
				_, err := rasql.SelectFrom(employees).
					Join(rasql.LeftJoin[staffRow](table, query.Equal(employees.ID, query.Bind(1)))).
					Build(clientForBuild(t).Dialect())
				return err
			},
		},
	}
}

// requireNilTableRejected drives table through every typed entry point and
// requires each one to report the nil table instead of panicking.
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

		t.Run("MustColumn", func(t *testing.T) {
			require.PanicsWithValue(t, "rasql: table column: table must not be nil", func() {
				rasql.MustColumn[staffRow](table, "id")
			})
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
		selected, err := rasql.SelectFrom[staffRow](table).Build(clientForBuild(t).Dialect())
		require.NoError(t, err)
		require.Contains(t, selected.SQL(), `FROM "staff"`)

		// DecodeFrom projects nothing by default, so this one names a column.
		email, err := table.Column("email")
		require.NoError(t, err)
		decoded, err := rasql.DecodeFrom[staffRow, staffRow](table).Project(query.Project(email)).Build(clientForBuild(t).Dialect())
		require.NoError(t, err)
		require.Contains(t, decoded.SQL(), `FROM "staff"`)

		deleted, err := rasql.DeleteFrom[staffRow](table).AllowAll().Build(clientForBuild(t).Dialect())
		require.NoError(t, err)
		require.Contains(t, deleted.SQL(), `DELETE FROM "staff"`)

		aliased, err := rasql.As[staffRow](table, "alias")
		require.NoError(t, err)
		require.Equal(t, "alias", aliased.QueryTable().Qualifier())

		require.Equal(t, "email", rasql.MustColumn[staffRow](table, "email").Name())

		others := contractors(t)
		othersID, err := others.Column("id")
		require.NoError(t, err)

		joined, err := rasql.SelectFrom(others).
			Join(rasql.InnerJoin[staffRow](table, query.Equal(othersID, query.Bind(1)))).
			Build(clientForBuild(t).Dialect())
		require.NoError(t, err)
		require.Contains(t, joined.SQL(), `INNER JOIN "staff"`)

		left, err := rasql.SelectFrom(others).
			Join(rasql.LeftJoin[staffRow](table, query.Equal(othersID, query.Bind(1)))).
			Build(clientForBuild(t).Dialect())
		require.NoError(t, err)
		require.Contains(t, left.SQL(), `LEFT JOIN "staff"`)
	})
}

func TestNilTableReportsErrors(t *testing.T) {
	requireNilTableRejected[rasql.Table[staffRow]](t, "nil interface", nil)

	failed, err := rasql.NewTable[staffRow](schema.Table{})
	require.Error(t, err)
	require.Nil(t, failed)
	requireNilTableRejected(t, "nil table from a failed NewTable", failed)

	requireNilTableRejected[*staffTable](t, "typed nil wrapper pointer", nil)
	requireNilTableRejected(t, "zero generated wrapper by value", staffTable{})
	requireNilTableRejected(t, "pointer to a zero generated wrapper", &staffTable{})
	requireNilTableRejected(t, "zero wrapper around a wrapper", auditedStaffTable{})
	requireNilTableRejected(t, "zero wrapper around a wrapper pointer", pointerStaffTable{})
	requireNilTableRejected(t, "wrapper holding a nil wrapper pointer", staffTable{Table: (*staffTable)(nil)})
	requireNilTableRejected(t, "zero wrapper reachable from itself", recursiveStaffTable{recursiveStaffTable: nil})
	requireNilTableRejected(t, "zero wrapper beside a second wrapper", twoCandidateStaffTable{})

	t.Run("a generated As reports the error behind the zero wrapper it returns", func(t *testing.T) {
		var wrapper staffTable
		aliased, err := wrapper.As("alias")
		require.ErrorContains(t, err, "must not be nil")
		require.Equal(t, staffTable{}, aliased)
	})
}

func TestUsableTableIsAccepted(t *testing.T) {
	table, err := rasql.NewTable[staffRow](staffDefinition())
	require.NoError(t, err)

	requireTableUsable(t, "typed table", table)
	requireTableUsable(t, "wrapper around a typed table", staff(t))
	requireTableUsable(t, "wrapper around a usable wrapper", auditedStaffTable{staffTable: staff(t)})
	requireTableUsable(t, "pointer to a usable wrapper", &staffTable{Table: table})
	requireTableUsable(t, "table with its own QueryTable and a nil embedded table", selfMethodStaffTable{source: table.QueryTable()})
}

func TestTableGuardKeepsUnrelatedPanics(t *testing.T) {
	// The guard reads a nil pointer dereference from tableRow, and then from
	// QueryTable, as signs of a missing table, so every other panic from an
	// implementation must reach the caller unchanged instead of being reported
	// as a nil table. This case's own QueryTable panics with a plain string, so
	// it stays distinguishable from a nil dereference regardless of probe order.
	buggy := buggyStaffTable{Table: nil}

	require.PanicsWithValue(t, staffTableBug, func() {
		rasql.MustColumn[staffRow](buggy, "id")
	})
	require.PanicsWithValue(t, staffTableBug, func() {
		_, _ = rasql.As[staffRow](buggy, "alias")
	})
	require.PanicsWithValue(t, staffTableBug, func() {
		_, _ = rasql.SelectFrom[staffRow](buggy).Build(clientForBuild(t).Dialect())
	})

	// The nil-dereference subclass needs its own coverage: a string panic and a
	// nil-dereference panic are recovered and classified differently, so a fix
	// that keeps the string case passing could still relabel this one. Here the
	// embedded table is VALID, so tableRow succeeds and the guard never probes
	// this QueryTable at all; the panic below comes straight from it.
	table, err := rasql.NewTable[staffRow](staffDefinition())
	require.NoError(t, err)
	buggyNilDeref := nilDereferenceStaffTable{Table: table}

	requirePanicsWithNilDereference(t, func() {
		_, _ = rasql.As[staffRow](buggyNilDeref, "alias")
	})
	requirePanicsWithNilDereference(t, func() {
		_, _ = rasql.SelectFrom[staffRow](buggyNilDeref).Build(clientForBuild(t).Dialect())
	})
}

// TestTableGuardDoesNotRelabelACallersOwnNilDereference discriminates the
// defect a neutral audit confirmed in the QueryTable-only probe: a caller type
// that embeds a VALID rasql.Table[T] but also declares its own QueryTable that
// dereferences an unrelated nil pointer used to be misreported as "table must
// not be nil" instead of letting its own panic propagate. Probing tableRow
// first fixes this, because tableRow is unexported and a type outside this
// package can never intercept it, so it always reaches the embedded table.
func TestTableGuardDoesNotRelabelACallersOwnNilDereference(t *testing.T) {
	table, err := rasql.NewTable[staffRow](staffDefinition())
	require.NoError(t, err)
	buggy := nilDereferenceStaffTable{Table: table}

	t.Run("SelectFrom", func(t *testing.T) {
		requirePanicsWithNilDereference(t, func() {
			_, _ = rasql.SelectFrom[staffRow](buggy).Build(clientForBuild(t).Dialect())
		})
	})

	t.Run("As", func(t *testing.T) {
		requirePanicsWithNilDereference(t, func() {
			_, _ = rasql.As[staffRow](buggy, "alias")
		})
	})

	t.Run("DecodeFrom", func(t *testing.T) {
		requirePanicsWithNilDereference(t, func() {
			_, _ = rasql.DecodeFrom[staffRow, staffRow](buggy).Build(clientForBuild(t).Dialect())
		})
	})
}

// TestTableGuardDoesNotRelabelAFabricatedNilDereference discriminates the
// defect a neutral audit confirmed in the text-only classifier: fabricatedNilDereference
// implements runtime.Error and its Error method returns exactly the runtime's
// nil-dereference text, but it is declared outside rasql and never comes from an
// actual nil dereference. Its embedded rasql.Table[staffRow] is nil, so tableRow
// nil-dereferences and the guard proceeds to probe QueryTable, which is where
// this fabricated value is panicked. A classifier matching only the
// runtime.Error interface and the message text would swallow this as "table
// must not be nil"; the concrete type's package is what tells the two apart.
func TestTableGuardDoesNotRelabelAFabricatedNilDereference(t *testing.T) {
	buggy := fabricatedRuntimeErrorStaffTable{}

	t.Run("MustColumn", func(t *testing.T) {
		require.PanicsWithValue(t, fabricatedNilDereference{}, func() {
			rasql.MustColumn[staffRow](buggy, "id")
		})
	})

	t.Run("As", func(t *testing.T) {
		require.PanicsWithValue(t, fabricatedNilDereference{}, func() {
			_, _ = rasql.As[staffRow](buggy, "alias")
		})
	})

	t.Run("SelectFrom", func(t *testing.T) {
		require.PanicsWithValue(t, fabricatedNilDereference{}, func() {
			_, _ = rasql.SelectFrom[staffRow](buggy).Build(clientForBuild(t).Dialect())
		})
	})
}
