package query_test

import (
	"fmt"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestZeroTableRefErrorsInsteadOfPanicking pins that every entry point taking
// a TableRef reports ErrNilTable rather than dereferencing the nil descriptor
// pointer behind the zero value.
func TestZeroTableRefErrorsInsteadOfPanicking(t *testing.T) {
	var zero query.TableRef

	validColumn := query.MustTableRef(usersTable()).Column("id")

	valid, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	cond := query.Equal(validColumn, validColumn)
	projection := validColumn

	t.Run("NewSelect", func(t *testing.T) {
		var err error
		require.NotPanics(t, func() {
			_, err = query.NewSelect(zero, projection)
		})
		require.ErrorContains(t, err, "must not be nil")
	})

	t.Run("NewJoinedSelect", func(t *testing.T) {
		var err error
		require.NotPanics(t, func() {
			_, err = query.NewJoinedSelect(valid, []query.Join{query.InnerJoin(zero, cond)}, nil, projection)
		})
		require.ErrorContains(t, err, "must not be nil")
	})

	t.Run("NewInsertRows", func(t *testing.T) {
		var err error
		require.NotPanics(t, func() {
			_, err = query.NewInsertRows(zero, []query.ColumnRef{validColumn}, [][]query.Expression{{query.Bind(1)}})
		})
		require.ErrorContains(t, err, "must not be nil")
	})

	t.Run("NewUpdate", func(t *testing.T) {
		var err error
		require.NotPanics(t, func() {
			_, err = query.NewUpdate(zero, query.Set(validColumn, query.Bind(1)))
		})
		require.ErrorContains(t, err, "must not be nil")
	})

	t.Run("NewDelete", func(t *testing.T) {
		var err error
		require.NotPanics(t, func() {
			_, err = query.NewDelete(zero)
		})
		require.ErrorContains(t, err, "must not be nil")
	})

	t.Run("As", func(t *testing.T) {
		var err error
		require.NotPanics(t, func() {
			_, err = zero.As("u")
		})
		require.ErrorContains(t, err, "must not be nil")
	})

	t.Run("Column", func(t *testing.T) {
		var err error
		require.NotPanics(t, func() {
			err = zero.Column("id").Validate()
		})
		require.ErrorContains(t, err, "must not be nil")
	})
}

// TestZeroTableRefAccessorsAnswerWithoutPanicking pins the values every
// read-only accessor reports for a zero TableRef, matching what they reported
// before definition became a pointer.
func TestZeroTableRefAccessorsAnswerWithoutPanicking(t *testing.T) {
	var zero query.TableRef

	require.Equal(t, "", zero.Name())
	require.Equal(t, "", zero.Alias())
	require.Equal(t, "", zero.Qualifier())
	require.Equal(t, "", zero.Schema())
	require.Equal(t, "", zero.QualifierSchema())
	require.Equal(t, "", zero.QualifiedName())
	require.False(t, zero.Qualified())
}

// TestTableRefDefinitionIsIndependent pins that Definition returns a clone: a
// caller mutating the returned descriptor's slices cannot reach the ref's own
// state.
func TestTableRefDefinitionIsIndependent(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)

	definition := users.Definition()
	definition.Columns[0].Name = "changed"
	definition.PrimaryKey[0] = "changed"

	require.NoError(t, users.Column("id").Validate())
	require.Equal(t, []string{"id"}, users.Definition().PrimaryKey)

	require.Error(t, users.Column("changed").Validate())
}

// TestZeroTableRefDefinitionDoesNotPanic pins that Definition on a zero ref
// reports no columns instead of panicking.
func TestZeroTableRefDefinitionDoesNotPanic(t *testing.T) {
	var zero query.TableRef

	var definition schema.TableDef
	require.NotPanics(t, func() {
		definition = zero.Definition()
	})
	require.Empty(t, definition.Columns)
}

// TestTableRefFromMatchesNewTableRef requires that a ref built by
// TableRefFrom over an already-valid descriptor behaves the same as one built
// by NewTableRef, for every reader that does not depend on cloning.
func TestTableRefFromMatchesNewTableRef(t *testing.T) {
	validated, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	trusted := query.TableRefFrom(usersTable())

	require.Equal(t, validated.Name(), trusted.Name())
	require.Equal(t, validated.Qualifier(), trusted.Qualifier())
	require.Equal(t, validated.QualifiedName(), trusted.QualifiedName())

	for _, name := range []string{"id", "email"} {
		require.NoError(t, trusted.Column(name).Validate())
	}
	require.Error(t, trusted.Column("missing").Validate())

	validatedAlias, err := validated.As("u")
	require.NoError(t, err)
	trustedAlias, err := trusted.As("u")
	require.NoError(t, err)
	require.Equal(t, validatedAlias.Qualifier(), trustedAlias.Qualifier())

	// key() is unexported, so pin it indirectly: the same SELECT built over
	// each ref renders identical SQL, and joining a ref to itself is still
	// rejected as a duplicate source either way.
	validatedID := validated.Column("id")
	trustedID := trusted.Column("id")

	validatedStatement, err := render.SelectFrom(dialect.PostgreSQL(), validated).Select("id", "email").Build()
	require.NoError(t, err)
	trustedStatement, err := render.SelectFrom(dialect.PostgreSQL(), trusted).Select("id", "email").Build()
	require.NoError(t, err)
	require.Equal(t, validatedStatement.SQL(), trustedStatement.SQL())

	_, err = query.NewSelect(validated, validatedID)
	require.NoError(t, err)
	_, err = query.NewJoinedSelect(validated, []query.Join{query.InnerJoin(validated, query.Equal(validatedID, validatedID))}, nil, validatedID)
	require.ErrorContains(t, err, "duplicates table reference")

	_, err = query.NewJoinedSelect(trusted, []query.Join{query.InnerJoin(trusted, query.Equal(trustedID, trustedID))}, nil, trustedID)
	require.ErrorContains(t, err, "duplicates table reference")
}

// TestTableRefFromDoesNotClone requires that TableRefFrom shares the caller's
// slices rather than copying them, in contrast to NewTableRef.
func TestTableRefFromDoesNotClone(t *testing.T) {
	definition := usersTable()
	trusted := query.TableRefFrom(definition)

	require.NoError(t, trusted.Column("email").Validate())

	definition.Columns[1].Name = "changed"

	require.Error(t, trusted.Column("email").Validate(), "TableRefFrom shares the caller's slices, so the rename is visible")
	require.NoError(t, trusted.Column("changed").Validate())

	// Contrast: the same mutation is invisible through a validated ref, because
	// NewTableRef clones its input.
	validatedDefinition := usersTable()
	validated, err := query.NewTableRef(validatedDefinition)
	require.NoError(t, err)
	validatedDefinition.Columns[1].Name = "changed"

	require.NoError(t, validated.Column("email").Validate())
}

// TestTableRefStaysComparable requires what TableRef's own doc states: a
// TableRef can be compared with == and used as a map key, and == compares the
// descriptor pointer, so two refs over one descriptor are never equal. It is a
// compile-time check as much as a run-time one, because a map or a slice field
// on TableRef would stop == compiling for every caller. That is why the column
// index sits behind the descriptor pointer instead.
func TestTableRefStaysComparable(t *testing.T) {
	definition := usersTable()
	table := query.TableRefFrom(definition)
	same := table
	other := query.TableRefFrom(definition)

	require.True(t, same == table, "a copy of a ref carries the same descriptor pointer")
	require.False(t, other == table, "two refs over one descriptor hold separate pointers")

	refs := map[query.TableRef]string{table: "table"}
	require.Equal(t, "table", refs[same])
	_, found := refs[other]
	require.False(t, found, "a map over refs keys on the descriptor pointer too")
}

// TestTableRefColumnResolvesEveryPosition requires that a lookup finds a column
// wherever it sits in a wide table and still reports one the table does not
// have, through the validating and the trusting constructor alike. The lookup
// reads an index built once per table rather than walking the columns, and a
// column the index placed wrongly or dropped would show up here as a wrong
// answer instead of only as a slow one. BenchmarkColumnRefValidate covers the
// cost this pins the correctness of.
func TestTableRefColumnResolvesEveryPosition(t *testing.T) {
	const width = 64
	definition := benchmarkTableDef(width)

	validated, err := query.NewTableRef(definition)
	require.NoError(t, err)

	refs := []struct {
		name  string
		table query.TableRef
	}{
		{name: "NewTableRef", table: validated},
		{name: "TableRefFrom", table: query.TableRefFrom(definition)},
	}
	for _, ref := range refs {
		t.Run(ref.name, func(t *testing.T) {
			for i := range width {
				name := fmt.Sprintf("c%d", i)
				column := ref.table.Column(name)
				require.Equal(t, name, column.Name())
			}

			require.ErrorContains(t, ref.table.Column("absent").Validate(), `has no column "absent"`)
		})
	}
}

// TestTableRefFromInvalidDescriptorStillFailsBeforeReachingAServer requires
// that an invalid descriptor stops a statement from becoming SQL, even though
// TableRefFrom itself validates nothing. An empty column name survives to
// render time; dialect.QuoteIdentifier (dialect/dialect.go) is what stops it,
// not schema.TableDef.Validate.
func TestTableRefFromInvalidDescriptorStillFailsBeforeReachingAServer(t *testing.T) {
	bad := usersTable()
	bad.Columns[0].Name = ""

	// Contrast: NewTableRef catches this at construction.
	_, err := query.NewTableRef(bad)
	require.Error(t, err)

	trusted := query.TableRefFrom(bad)
	// TableRefFrom validates nothing, so an empty column name is accepted here.
	column := trusted.Column("")

	_, err = render.SelectFrom(dialect.PostgreSQL(), trusted).Project(column).Build()
	require.Error(t, err, "dialect.QuoteIdentifier catches the empty identifier at render time")

	// Not caught at all: a descriptor repeating a column name renders as SQL a
	// server accepts. TableRefFrom trades that class of mistake for speed.
}
