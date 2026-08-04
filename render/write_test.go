package render_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestWriteStatementsRenderForBuiltInDialects(t *testing.T) {
	users, id, email := writeTable(t)
	insert, err := query.NewInsert(users, []query.Column{id, email}, []query.Expression{query.Bind(1), query.Bind("ada@example.com")})
	require.NoError(t, err)
	update, err := query.NewUpdate(users, query.Set(email, query.Bind("grace@example.com")))
	require.NoError(t, err)
	update, err = update.WithWhere(query.Equal(id, query.Bind(1)))
	require.NoError(t, err)
	deleteStatement, err := query.NewDelete(users)
	require.NoError(t, err)
	deleteStatement, err = deleteStatement.WithWhere(query.Equal(id, query.Bind(1)))
	require.NoError(t, err)

	tests := map[string]struct {
		dialect dialect.Dialect
		insert  string
		update  string
		delete  string
	}{
		"postgresql": {
			dialect: dialect.PostgreSQL(),
			insert:  "INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2)",
			update:  "UPDATE \"users\" SET \"email\" = $1 WHERE (\"users\".\"id\" = $2)",
			delete:  "DELETE FROM \"users\" WHERE (\"users\".\"id\" = $1)",
		},
		"mysql": {
			dialect: dialect.MySQL(),
			insert:  "INSERT INTO `users` (`id`, `email`) VALUES (?, ?)",
			update:  "UPDATE `users` SET `email` = ? WHERE (`users`.`id` = ?)",
			delete:  "DELETE FROM `users` WHERE (`users`.`id` = ?)",
		},
		"sqlite": {
			dialect: dialect.SQLite(),
			insert:  "INSERT INTO \"users\" (\"id\", \"email\") VALUES (?, ?)",
			update:  "UPDATE \"users\" SET \"email\" = ? WHERE (\"users\".\"id\" = ?)",
			delete:  "DELETE FROM \"users\" WHERE (\"users\".\"id\" = ?)",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rendered, err := render.Insert(test.dialect, insert)
			require.NoError(t, err)
			require.Equal(t, test.insert, rendered.SQL())
			require.Equal(t, []any{1, "ada@example.com"}, rendered.Args())

			rendered, err = render.Update(test.dialect, update)
			require.NoError(t, err)
			require.Equal(t, test.update, rendered.SQL())
			require.Equal(t, []any{"grace@example.com", 1}, rendered.Args())

			rendered, err = render.Delete(test.dialect, deleteStatement)
			require.NoError(t, err)
			require.Equal(t, test.delete, rendered.SQL())
			require.Equal(t, []any{1}, rendered.Args())
		})
	}
}

func TestReturningRequiresDialectCapability(t *testing.T) {
	users, id, email := writeTable(t)
	statement, err := query.NewInsert(users, []query.Column{id, email}, []query.Expression{query.Bind(1), query.Bind("ada@example.com")})
	require.NoError(t, err)
	statement, err = statement.WithReturning(query.Project(id))
	require.NoError(t, err)

	rendered, err := render.Insert(dialect.PostgreSQL(), statement)
	require.NoError(t, err)
	require.Equal(t, "INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2) RETURNING \"id\"", rendered.SQL())

	rendered, err = render.Insert(dialect.SQLite(), statement)
	require.NoError(t, err)
	require.Equal(t, "INSERT INTO \"users\" (\"id\", \"email\") VALUES (?, ?) RETURNING \"id\"", rendered.SQL())

	_, err = render.Insert(dialect.MySQL(), statement)
	require.Error(t, err)
}

func TestDefaultInsertRendersDialectSyntax(t *testing.T) {
	users, _, _ := writeTable(t)
	statement, err := query.NewDefaultInsert(users)
	require.NoError(t, err)

	tests := map[string]struct {
		dialect dialect.Dialect
		sql     string
	}{
		"postgresql": {
			dialect: dialect.PostgreSQL(),
			sql:     "INSERT INTO \"users\" DEFAULT VALUES",
		},
		"mysql": {
			dialect: dialect.MySQL(),
			sql:     "INSERT INTO `users` () VALUES ()",
		},
		"sqlite": {
			dialect: dialect.SQLite(),
			sql:     "INSERT INTO \"users\" DEFAULT VALUES",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rendered, err := render.Insert(test.dialect, statement)
			require.NoError(t, err)
			require.Equal(t, test.sql, rendered.SQL())
			require.Empty(t, rendered.Args())
		})
	}

}

func TestUpsertRendersDialectConflictSyntax(t *testing.T) {
	users, id, email := writeTable(t)
	insert, err := query.NewInsert(users, []query.Column{id, email}, []query.Expression{query.Bind(1), query.Bind("ada@example.com")})
	require.NoError(t, err)
	statement, err := query.NewUpsert(insert, []query.Column{id}, []query.Assignment{query.Set(email, query.Excluded(email))})
	require.NoError(t, err)
	mysqlStatement, err := query.NewUpsert(insert, nil, []query.Assignment{query.Set(email, query.Excluded(email))})
	require.NoError(t, err)

	tests := map[string]struct {
		dialect   dialect.Dialect
		statement query.Upsert
		sql       string
	}{
		"postgresql": {
			dialect:   dialect.PostgreSQL(),
			statement: statement,
			sql:       "INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2) ON CONFLICT (\"id\") DO UPDATE SET \"email\" = EXCLUDED.\"email\"",
		},
		"mysql": {
			dialect:   dialect.MySQL(),
			statement: mysqlStatement,
			sql:       "INSERT INTO `users` (`id`, `email`) VALUES (?, ?) ON DUPLICATE KEY UPDATE `email` = VALUES(`email`)",
		},
		"sqlite": {
			dialect:   dialect.SQLite(),
			statement: statement,
			sql:       "INSERT INTO \"users\" (\"id\", \"email\") VALUES (?, ?) ON CONFLICT (\"id\") DO UPDATE SET \"email\" = EXCLUDED.\"email\"",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rendered, err := render.Upsert(test.dialect, test.statement)
			require.NoError(t, err)
			require.Equal(t, test.sql, rendered.SQL())
			require.Equal(t, []any{1, "ada@example.com"}, rendered.Args())
		})
	}

}

func TestMySQLUpsertRejectsConflictTarget(t *testing.T) {
	users, id, email := writeTable(t)
	insert, err := query.NewInsert(users, []query.Column{id, email}, []query.Expression{query.Bind(1), query.Bind("ada@example.com")})
	require.NoError(t, err)
	withAssignments, err := query.NewUpsert(insert, []query.Column{id}, []query.Assignment{query.Set(email, query.Excluded(email))})
	require.NoError(t, err)
	withoutAssignments, err := query.NewUpsert(insert, []query.Column{id}, nil)
	require.NoError(t, err)

	tests := map[string]struct {
		statement query.Upsert
		message   string
	}{
		"with assignments": {
			statement: withAssignments,
			message:   "render mysql: explicit conflict target is not supported: this dialect lacks dialect.CapabilityConflictTarget",
		},
		// MySQL rejects both the conflict target and the empty assignment list, so
		// the error names both problems. The conflict target keeps precedence
		// because it is unusable on MySQL for any assignment list, while zero
		// assignments render as ON CONFLICT DO NOTHING on PostgreSQL and SQLite.
		"without assignments": {
			statement: withoutAssignments,
			message:   "render mysql: explicit conflict target is not supported: this dialect lacks dialect.CapabilityConflictTarget; upsert without assignments is not supported",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := render.Upsert(dialect.MySQL(), test.statement)
			require.EqualError(t, err, test.message)
		})
	}
}

// upsertOnlyDialect is a stub dialect.Dialect that supports upserts but neither
// dialect.CapabilityConflictTarget nor dialect.CapabilityDefaultValuesUpsert. No
// built-in dialect lacks both, yet dialect.Dialect is a public interface, so a
// caller can supply one and must hear about both problems at once.
type upsertOnlyDialect struct {
	style dialect.UpsertStyle
}

func (upsertOnlyDialect) Name() string { return "stub" }

func (upsertOnlyDialect) QuoteIdentifier(name string) (string, error) { return `"` + name + `"`, nil }

func (upsertOnlyDialect) Placeholder(int) (string, error) { return "?", nil }

func (upsertOnlyDialect) TypeName(schema.LogicalType) (string, error) { return "", nil }

func (d upsertOnlyDialect) UpsertStyle() dialect.UpsertStyle { return d.style }

func (upsertOnlyDialect) Supports(capability dialect.Capability) bool {
	return capability&dialect.CapabilityUpsert == capability
}

func TestUpsertReportsEveryUnsupportedFeature(t *testing.T) {
	users, id, email := writeTable(t)
	defaultInsert, err := query.NewDefaultInsert(users)
	require.NoError(t, err)
	withAssignments, err := query.NewUpsert(defaultInsert, []query.Column{id}, []query.Assignment{query.Set(email, query.Excluded(email))})
	require.NoError(t, err)
	withoutAssignments, err := query.NewUpsert(defaultInsert, []query.Column{id}, nil)
	require.NoError(t, err)
	withoutTarget, err := query.NewUpsert(defaultInsert, nil, []query.Assignment{query.Set(email, query.Excluded(email))})
	require.NoError(t, err)

	tests := map[string]struct {
		style     dialect.UpsertStyle
		statement query.Upsert
		message   string
	}{
		// A default-values upsert that also carries a conflict target hits two
		// unsupported features at once, so the error names both. The conflict
		// target keeps precedence because it is unusable on this dialect for any
		// insert, while a default-values upsert renders on PostgreSQL and MySQL.
		"on conflict with assignments": {
			style:     dialect.UpsertOnConflict,
			statement: withAssignments,
			message:   "render stub: explicit conflict target is not supported: this dialect lacks dialect.CapabilityConflictTarget; default-values upsert is not supported",
		},
		"on conflict without assignments": {
			style:     dialect.UpsertOnConflict,
			statement: withoutAssignments,
			message:   "render stub: explicit conflict target is not supported: this dialect lacks dialect.CapabilityConflictTarget; default-values upsert is not supported",
		},
		"duplicate key with assignments": {
			style:     dialect.UpsertDuplicateKey,
			statement: withAssignments,
			message:   "render stub: explicit conflict target is not supported: this dialect lacks dialect.CapabilityConflictTarget; default-values upsert is not supported",
		},
		// The ON DUPLICATE KEY style rejects an empty assignment list too, so all
		// three problems appear in the same message.
		"duplicate key without assignments": {
			style:     dialect.UpsertDuplicateKey,
			statement: withoutAssignments,
			message:   "render stub: explicit conflict target is not supported: this dialect lacks dialect.CapabilityConflictTarget; default-values upsert is not supported; upsert without assignments is not supported",
		},
		// Without a conflict target the default-values problem still reports on its
		// own, so removing the target does not silence it.
		"no conflict target": {
			style:     dialect.UpsertOnConflict,
			statement: withoutTarget,
			message:   "render stub: default-values upsert is not supported",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := render.Upsert(upsertOnlyDialect{style: test.style}, test.statement)
			require.EqualError(t, err, test.message)
		})
	}
}

func TestSQLiteUpsertExecutes(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)

	users, id, email := writeTable(t)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE \"users\" (\"id\" INTEGER PRIMARY KEY, \"email\" TEXT NOT NULL)")
	require.NoError(t, err)

	insert, err := query.NewInsert(users, []query.Column{id, email}, []query.Expression{query.Bind(1), query.Bind("ada@example.com")})
	require.NoError(t, err)
	statement, err := query.NewUpsert(insert, []query.Column{id}, []query.Assignment{query.Set(email, query.Excluded(email))})
	require.NoError(t, err)
	rendered, err := render.Upsert(dialect.SQLite(), statement)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), rendered.SQL(), rendered.Args()...)
	require.NoError(t, err)

	insert, err = query.NewInsert(users, []query.Column{id, email}, []query.Expression{query.Bind(1), query.Bind("grace@example.com")})
	require.NoError(t, err)
	statement, err = query.NewUpsert(insert, []query.Column{id}, []query.Assignment{query.Set(email, query.Excluded(email))})
	require.NoError(t, err)
	rendered, err = render.Upsert(dialect.SQLite(), statement)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), rendered.SQL(), rendered.Args()...)
	require.NoError(t, err)

	var actual string
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT \"email\" FROM \"users\" WHERE \"id\" = 1").Scan(&actual))
	require.Equal(t, "grace@example.com", actual)
}

func TestSQLiteDefaultValuesUpsertIsRejected(t *testing.T) {
	users, id, email := writeTable(t)
	insert, err := query.NewDefaultInsert(users)
	require.NoError(t, err)
	statement, err := query.NewUpsert(insert, []query.Column{id}, []query.Assignment{query.Set(email, query.Excluded(email))})
	require.NoError(t, err)

	_, err = render.Upsert(dialect.SQLite(), statement)
	require.ErrorContains(t, err, "default-values upsert is not supported")
}

func writeTable(t *testing.T) (query.Table, query.Column, query.Column) {
	t.Helper()
	users, err := query.NewTable(schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	id, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)
	return users, id, email
}
