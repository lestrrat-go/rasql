package render_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
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
		"spanner": {
			dialect: dialect.Spanner(),
			insert:  "INSERT INTO `users` (`id`, `email`) VALUES (@p1, @p2)",
			update:  "UPDATE `users` SET `email` = @p1 WHERE (`users`.`id` = @p2)",
			delete:  "DELETE FROM `users` WHERE (`users`.`id` = @p1)",
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

	_, err = render.Insert(dialect.Spanner(), statement)
	require.ErrorContains(t, err, "default-values INSERT is not supported")
}

func TestUpsertRendersDialectConflictSyntax(t *testing.T) {
	users, id, email := writeTable(t)
	insert, err := query.NewInsert(users, []query.Column{id, email}, []query.Expression{query.Bind(1), query.Bind("ada@example.com")})
	require.NoError(t, err)
	statement, err := query.NewUpsert(insert, []query.Column{id}, []query.Assignment{query.Set(email, query.Excluded(email))})
	require.NoError(t, err)

	tests := map[string]struct {
		dialect dialect.Dialect
		sql     string
	}{
		"postgresql": {
			dialect: dialect.PostgreSQL(),
			sql:     "INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2) ON CONFLICT (\"id\") DO UPDATE SET \"email\" = EXCLUDED.\"email\"",
		},
		"mysql": {
			dialect: dialect.MySQL(),
			sql:     "INSERT INTO `users` (`id`, `email`) VALUES (?, ?) ON DUPLICATE KEY UPDATE `email` = VALUES(`email`)",
		},
		"sqlite": {
			dialect: dialect.SQLite(),
			sql:     "INSERT INTO \"users\" (\"id\", \"email\") VALUES (?, ?) ON CONFLICT (\"id\") DO UPDATE SET \"email\" = EXCLUDED.\"email\"",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rendered, err := render.Upsert(test.dialect, statement)
			require.NoError(t, err)
			require.Equal(t, test.sql, rendered.SQL())
			require.Equal(t, []any{1, "ada@example.com"}, rendered.Args())
		})
	}

	_, err = render.Upsert(dialect.Spanner(), statement)
	require.Error(t, err)
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
