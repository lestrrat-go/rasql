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
			_, err = query.NewInsertRows(zero, []query.ColumnRef{validColumn}, [][]any{{1}})
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

	t.Run("InSchema", func(t *testing.T) {
		var err error
		require.NotPanics(t, func() {
			_, err = zero.InSchema("tenant_0001")
		})
		require.ErrorIs(t, err, query.ErrNilTable)
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

// TestTableRefInSchemaMovesACopy requires that InSchema reports the new
// namespace through every accessor that reads one, leaves the ref it was
// called on where it was, and re-indexes nothing: the moved ref answers a
// column lookup from the index the original built, and still reports a name
// neither of them holds.
func TestTableRefInSchemaMovesACopy(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)

	moved, err := users.InSchema("tenant_0001")
	require.NoError(t, err)

	require.Equal(t, "tenant_0001", moved.Schema())
	require.True(t, moved.Qualified())
	require.Equal(t, "tenant_0001", moved.QualifierSchema())
	require.Equal(t, "tenant_0001.users", moved.QualifiedName())
	require.Equal(t, "users", moved.Name())
	require.Equal(t, "users", moved.Qualifier())
	require.Equal(t, "tenant_0001", moved.Definition().Schema)

	require.Equal(t, "", users.Schema(), "the original stays where it was")
	require.False(t, users.Qualified())
	require.Equal(t, "users", users.QualifiedName())
	require.Equal(t, "", users.Definition().Schema)

	require.Equal(t, usersTable().Columns, moved.Definition().Columns)
	require.NoError(t, moved.Column("id").Validate())
	require.NoError(t, moved.Column("email").Validate())
	require.ErrorContains(t, moved.Column("absent").Validate(), `has no column "absent"`)

	// A second move replaces the first rather than stacking on it, and still
	// leaves both earlier refs alone.
	again, err := moved.InSchema("tenant_0002")
	require.NoError(t, err)
	require.Equal(t, "tenant_0002", again.Schema())
	require.Equal(t, "tenant_0001", moved.Schema())
	require.Equal(t, "", users.Schema())
}

// TestTableRefInSchemaSharesTheColumns requires that the moved ref reads the
// same columns slice as the ref it came from rather than a copy of it. A ref
// built by TableRefFrom is what can show that from outside the package: it
// reads the caller's slice in place, so a rename after the move is visible
// through the moved ref exactly as it is through the original.
func TestTableRefInSchemaSharesTheColumns(t *testing.T) {
	definition := usersTable()
	trusted := query.TableRefFrom(definition)

	moved, err := trusted.InSchema("tenant_0001")
	require.NoError(t, err)
	require.NoError(t, moved.Column("email").Validate())

	definition.Columns[1].Name = "changed"

	require.Error(t, moved.Column("email").Validate(), "the moved ref reads the same slice as the original")
	require.NoError(t, moved.Column("changed").Validate())
	require.NoError(t, trusted.Column("changed").Validate())
}

// TestTableRefInSchemaRejectsANamespaceItCannotRender requires that an empty
// namespace is an error rather than a way to unqualify a table, and that an
// identifier schema.ValidateIdentifier refuses never reaches a rendered
// statement.
func TestTableRefInSchemaRejectsANamespaceItCannotRender(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)

	t.Run("empty", func(t *testing.T) {
		_, err := users.InSchema("")
		require.ErrorContains(t, err, "must not be empty")
	})

	t.Run("NUL", func(t *testing.T) {
		_, err := users.InSchema("tenant\x000001")
		require.ErrorContains(t, err, "must not contain NUL")
	})

	t.Run("invalid UTF-8", func(t *testing.T) {
		_, err := users.InSchema("tenant\xff")
		require.ErrorContains(t, err, "must contain valid UTF-8")
	})
}

// TestTableRefInSchemaComposesWithAnAlias requires that a move and an alias
// write two different fields and compose in either order, and pins what the
// renderer then does with the pair. The rule is today's, unchanged: an alias
// replaces a table's whole qualified name for a column reference, so only the
// FROM entry names the namespace and every column renders under the bare
// alias.
func TestTableRefInSchemaComposesWithAnAlias(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)

	movedFirst, err := users.InSchema("tenant_0001")
	require.NoError(t, err)
	movedFirst, err = movedFirst.As("u")
	require.NoError(t, err)

	aliasedFirst, err := users.As("u")
	require.NoError(t, err)
	aliasedFirst, err = aliasedFirst.InSchema("tenant_0001")
	require.NoError(t, err)

	for _, order := range []struct {
		name  string
		table query.TableRef
	}{
		{name: "InSchema then As", table: movedFirst},
		{name: "As then InSchema", table: aliasedFirst},
	} {
		t.Run(order.name, func(t *testing.T) {
			require.Equal(t, "tenant_0001", order.table.Schema())
			require.Equal(t, "u", order.table.Alias())
			require.Equal(t, "u", order.table.Qualifier())
			require.True(t, order.table.Qualified(), "the table itself is still qualified")
			require.Equal(t, "", order.table.QualifierSchema(), "an alias replaces the whole qualified name")
			require.Equal(t, "u", order.table.QualifiedName())

			statement, err := render.SelectFrom(dialect.PostgreSQL(), order.table).Select("id", "email").Build()
			require.NoError(t, err)
			require.Equal(
				t,
				`SELECT "u"."id", "u"."email" FROM "tenant_0001"."users" AS "u"`,
				statement.SQL(),
			)
		})
	}
}

// TestMovedTableRendersQualifiedOnEveryDialect requires that a table moved at
// run time names its new namespace in every statement kind rasql renders, on
// each of the three dialects. It is the whole point of the move: a store
// generated against one namespace has to reach another one without being
// regenerated.
func TestMovedTableRendersQualifiedOnEveryDialect(t *testing.T) {
	for _, test := range []struct {
		name      string
		dialect   dialect.Dialect
		selectSQL string
		insertSQL string
		updateSQL string
		deleteSQL string
	}{
		{
			name:      "postgresql",
			dialect:   dialect.PostgreSQL(),
			selectSQL: `SELECT "tenant_0001"."users"."id", "tenant_0001"."users"."email" FROM "tenant_0001"."users" WHERE ("tenant_0001"."users"."id" = $1)`,
			insertSQL: `INSERT INTO "tenant_0001"."users" ("id", "email") VALUES ($1, $2)`,
			updateSQL: `UPDATE "tenant_0001"."users" SET "email" = $1 WHERE ("tenant_0001"."users"."id" = $2)`,
			deleteSQL: `DELETE FROM "tenant_0001"."users" WHERE ("tenant_0001"."users"."id" = $1)`,
		},
		{
			name:      "mysql",
			dialect:   dialect.MySQL(),
			selectSQL: "SELECT `tenant_0001`.`users`.`id`, `tenant_0001`.`users`.`email` FROM `tenant_0001`.`users` WHERE (`tenant_0001`.`users`.`id` = ?)",
			insertSQL: "INSERT INTO `tenant_0001`.`users` (`id`, `email`) VALUES (?, ?)",
			updateSQL: "UPDATE `tenant_0001`.`users` SET `email` = ? WHERE (`tenant_0001`.`users`.`id` = ?)",
			deleteSQL: "DELETE FROM `tenant_0001`.`users` WHERE (`tenant_0001`.`users`.`id` = ?)",
		},
		{
			name:      "sqlite",
			dialect:   dialect.SQLite(),
			selectSQL: `SELECT "tenant_0001"."users"."id", "tenant_0001"."users"."email" FROM "tenant_0001"."users" WHERE ("tenant_0001"."users"."id" = ?)`,
			insertSQL: `INSERT INTO "tenant_0001"."users" ("id", "email") VALUES (?, ?)`,
			updateSQL: `UPDATE "tenant_0001"."users" SET "email" = ? WHERE ("tenant_0001"."users"."id" = ?)`,
			deleteSQL: `DELETE FROM "tenant_0001"."users" WHERE ("tenant_0001"."users"."id" = ?)`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			users, err := query.NewTableRef(usersTable())
			require.NoError(t, err)
			moved, err := users.InSchema("tenant_0001")
			require.NoError(t, err)
			id := moved.Column("id")
			email := moved.Column("email")

			t.Run("SELECT", func(t *testing.T) {
				statement, err := render.SelectFrom(test.dialect, moved).
					Select("id", "email").
					Where(query.Equal(id, 1)).
					Build()
				require.NoError(t, err)
				require.Equal(t, test.selectSQL, statement.SQL())
			})

			t.Run("INSERT", func(t *testing.T) {
				insert, err := query.NewInsertRows(moved, []query.ColumnRef{id, email}, [][]any{{1, "ada@example.com"}})
				require.NoError(t, err)
				statement, err := render.Insert(test.dialect, insert)
				require.NoError(t, err)
				require.Equal(t, test.insertSQL, statement.SQL())
			})

			t.Run("UPDATE", func(t *testing.T) {
				update, err := query.NewUpdate(moved, query.Set(email, query.Bind("grace@example.com")))
				require.NoError(t, err)
				update, err = update.WithWhere(query.Equal(id, 1))
				require.NoError(t, err)
				statement, err := render.Update(test.dialect, update)
				require.NoError(t, err)
				require.Equal(t, test.updateSQL, statement.SQL())
			})

			t.Run("DELETE", func(t *testing.T) {
				del, err := query.NewDelete(moved)
				require.NoError(t, err)
				del, err = del.WithWhere(query.Equal(id, 1))
				require.NoError(t, err)
				statement, err := render.Delete(test.dialect, del)
				require.NoError(t, err)
				require.Equal(t, test.deleteSQL, statement.SQL())
			})
		})
	}
}

// TestOneStatementReachesTwoNamespaces requires that two copies of one table
// moved to two namespaces join in a single statement, each column rendering
// under its own namespace. This is the case a per-connection namespace could
// not express, since a connection points at one place.
//
// It also pins the limit the existing source rule puts on that: a moved copy
// joined to the unqualified original is refused, because rasql renders a
// column of an unqualified table under a bare "users" that names the qualified
// table equally well. Aliasing either side is the way out, and it is the same
// answer rasql already gives for a self-join.
func TestOneStatementReachesTwoNamespaces(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)

	first, err := users.InSchema("tenant_0001")
	require.NoError(t, err)
	second, err := users.InSchema("tenant_0002")
	require.NoError(t, err)

	statement, err := render.SelectFrom(dialect.PostgreSQL(), first).
		Select("id").
		Join(query.InnerJoin(query.Relation(second), query.Equal(first.Column("id"), second.Column("id")))).
		Build()
	require.NoError(t, err)
	require.Equal(
		t,
		`SELECT "tenant_0001"."users"."id" FROM "tenant_0001"."users" `+
			`INNER JOIN "tenant_0002"."users" ON ("tenant_0001"."users"."id" = "tenant_0002"."users"."id")`,
		statement.SQL(),
	)

	_, err = render.SelectFrom(dialect.PostgreSQL(), users).
		Select("id").
		Join(query.InnerJoin(query.Relation(first), query.Equal(users.Column("id"), first.Column("id")))).
		Build()
	require.ErrorContains(t, err, `is referred to as "users"`)
}
