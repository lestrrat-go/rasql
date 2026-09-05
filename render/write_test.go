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
	insert, err := query.NewInsert(users, query.Set(id, 1), query.Set(email, "ada@example.com"))
	require.NoError(t, err)
	update, err := query.NewUpdate(users, query.Set(email, query.Bind("grace@example.com")))
	require.NoError(t, err)
	update, err = update.WithWhere(query.Equal(id, query.Bind(1)))
	require.NoError(t, err)
	deleteStatement, err := query.NewDelete(users)
	require.NoError(t, err)
	deleteStatement, err = deleteStatement.WithWhere(query.Equal(id, query.Bind(1)))
	require.NoError(t, err)

	multiInsert, err := query.NewInsertRows(users, []query.ColumnRef{id, email}, [][]any{
		{1, "ada@example.com"},
		{2, "grace@example.com"},
		{3, "edsger@example.com"},
	})
	require.NoError(t, err)

	tests := map[string]struct {
		dialect     dialect.Dialect
		insert      string
		update      string
		delete      string
		multiInsert string
	}{
		"postgresql": {
			dialect:     dialect.PostgreSQL(),
			insert:      "INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2)",
			update:      "UPDATE \"users\" SET \"email\" = $1 WHERE (\"users\".\"id\" = $2)",
			delete:      "DELETE FROM \"users\" WHERE (\"users\".\"id\" = $1)",
			multiInsert: "INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2), ($3, $4), ($5, $6)",
		},
		"mysql": {
			dialect:     dialect.MySQL(),
			insert:      "INSERT INTO `users` (`id`, `email`) VALUES (?, ?)",
			update:      "UPDATE `users` SET `email` = ? WHERE (`users`.`id` = ?)",
			delete:      "DELETE FROM `users` WHERE (`users`.`id` = ?)",
			multiInsert: "INSERT INTO `users` (`id`, `email`) VALUES (?, ?), (?, ?), (?, ?)",
		},
		"sqlite": {
			dialect:     dialect.SQLite(),
			insert:      "INSERT INTO \"users\" (\"id\", \"email\") VALUES (?, ?)",
			update:      "UPDATE \"users\" SET \"email\" = ? WHERE (\"users\".\"id\" = ?)",
			delete:      "DELETE FROM \"users\" WHERE (\"users\".\"id\" = ?)",
			multiInsert: "INSERT INTO \"users\" (\"id\", \"email\") VALUES (?, ?), (?, ?), (?, ?)",
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

			rendered, err = render.Insert(test.dialect, multiInsert)
			require.NoError(t, err)
			require.Equal(t, test.multiInsert, rendered.SQL())
			require.Equal(t, []any{1, "ada@example.com", 2, "grace@example.com", 3, "edsger@example.com"}, rendered.Args())
		})
	}
}

func TestUnconditionalWritesRequireExplicitAllowAll(t *testing.T) {
	users, id, email := writeTable(t)

	update, err := query.NewUpdate(users, query.Set(email, query.Bind("grace@example.com")))
	require.NoError(t, err)
	_, err = render.Update(dialect.PostgreSQL(), update)
	require.ErrorContains(t, err, "UPDATE requires a WHERE predicate or an explicit AllowAll")

	deleteStatement, err := query.NewDelete(users)
	require.NoError(t, err)
	_, err = render.Delete(dialect.PostgreSQL(), deleteStatement)
	require.ErrorContains(t, err, "DELETE requires a WHERE predicate or an explicit AllowAll")

	allowedUpdate, err := update.AllowAll()
	require.NoError(t, err)
	rendered, err := render.Update(dialect.PostgreSQL(), allowedUpdate)
	require.NoError(t, err)
	require.Equal(t, "UPDATE \"users\" SET \"email\" = $1", rendered.SQL())
	require.Equal(t, []any{"grace@example.com"}, rendered.Args())

	allowedDelete, err := deleteStatement.AllowAll()
	require.NoError(t, err)
	rendered, err = render.Delete(dialect.PostgreSQL(), allowedDelete)
	require.NoError(t, err)
	require.Equal(t, "DELETE FROM \"users\"", rendered.SQL())
	require.Empty(t, rendered.Args())

	targetedUpdate, err := update.WithWhere(query.Equal(id, query.Bind(1)))
	require.NoError(t, err)
	_, err = render.Update(dialect.PostgreSQL(), targetedUpdate)
	require.NoError(t, err)

	targetedDelete, err := deleteStatement.WithWhere(query.Equal(id, query.Bind(1)))
	require.NoError(t, err)
	_, err = render.Delete(dialect.PostgreSQL(), targetedDelete)
	require.NoError(t, err)
}

// TestUpdateRendersScalarFunctionAssignment proves a scalar function call in
// a SET assignment renders with no dialect-specific branch, identically to a
// SELECT projection, across all three built-in dialects.
func TestUpdateRendersScalarFunctionAssignment(t *testing.T) {
	users, id, email := writeTable(t)
	update, err := query.NewUpdate(users, query.Set(email, query.Lower(email)))
	require.NoError(t, err)
	update, err = update.WithWhere(query.Equal(id, query.Bind(1)))
	require.NoError(t, err)

	tests := map[string]struct {
		dialect dialect.Dialect
		sql     string
	}{
		"postgresql": {
			dialect: dialect.PostgreSQL(),
			sql:     "UPDATE \"users\" SET \"email\" = LOWER(\"users\".\"email\") WHERE (\"users\".\"id\" = $1)",
		},
		"mysql": {
			dialect: dialect.MySQL(),
			sql:     "UPDATE `users` SET `email` = LOWER(`users`.`email`) WHERE (`users`.`id` = ?)",
		},
		"sqlite": {
			dialect: dialect.SQLite(),
			sql:     "UPDATE \"users\" SET \"email\" = LOWER(\"users\".\"email\") WHERE (\"users\".\"id\" = ?)",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rendered, err := render.Update(test.dialect, update)
			require.NoError(t, err)
			require.Equal(t, test.sql, rendered.SQL())
			require.Equal(t, []any{1}, rendered.Args())
		})
	}
}

// TestInsertRendersColumnsInAssignmentOrder pins the render-side half of the
// rule that the rendered column list follows the argument order: the same
// two assignments given in reverse order render the columns and VALUES items
// in the reverse order too.
func TestInsertRendersColumnsInAssignmentOrder(t *testing.T) {
	users, id, email := writeTable(t)

	idFirst, err := query.NewInsert(users, query.Set(id, 1), query.Set(email, "ada@example.com"))
	require.NoError(t, err)
	emailFirst, err := query.NewInsert(users, query.Set(email, "ada@example.com"), query.Set(id, 1))
	require.NoError(t, err)

	renderedIDFirst, err := render.Insert(dialect.PostgreSQL(), idFirst)
	require.NoError(t, err)
	require.Equal(t, `INSERT INTO "users" ("id", "email") VALUES ($1, $2)`, renderedIDFirst.SQL())
	require.Equal(t, []any{1, "ada@example.com"}, renderedIDFirst.Args())

	renderedEmailFirst, err := render.Insert(dialect.PostgreSQL(), emailFirst)
	require.NoError(t, err)
	require.Equal(t, `INSERT INTO "users" ("email", "id") VALUES ($1, $2)`, renderedEmailFirst.SQL())
	require.Equal(t, []any{"ada@example.com", 1}, renderedEmailFirst.Args())
}

// TestQualifiedWriteStatementsRenderForBuiltInDialects pins the exact
// rendered SQL for INSERT, multi-row INSERT, UPDATE and DELETE against a
// schema-qualified table across all three built-in dialects. Only the table
// name in the statement's own clause is qualified: WHERE and SET still name
// unqualified columns because they belong to the statement's own table.
func TestQualifiedWriteStatementsRenderForBuiltInDialects(t *testing.T) {
	events, id, userID, action := qualifiedWriteTable(t)
	insert, err := query.NewInsert(events, query.Set(userID, 7), query.Set(action, "created"))
	require.NoError(t, err)
	update, err := query.NewUpdate(events, query.Set(action, query.Bind("closed")))
	require.NoError(t, err)
	update, err = update.WithWhere(query.Equal(id, query.Bind(2)))
	require.NoError(t, err)
	deleteStatement, err := query.NewDelete(events)
	require.NoError(t, err)
	deleteStatement, err = deleteStatement.WithWhere(query.Equal(id, query.Bind(2)))
	require.NoError(t, err)
	multiInsert, err := query.NewInsertRows(events, []query.ColumnRef{userID, action}, [][]any{
		{7, "created"},
		{8, "updated"},
	})
	require.NoError(t, err)

	tests := map[string]struct {
		dialect     dialect.Dialect
		insert      string
		update      string
		delete      string
		multiInsert string
	}{
		"postgresql": {
			dialect:     dialect.PostgreSQL(),
			insert:      `INSERT INTO "audit"."events" ("user_id", "action") VALUES ($1, $2)`,
			update:      `UPDATE "audit"."events" SET "action" = $1 WHERE ("audit"."events"."id" = $2)`,
			delete:      `DELETE FROM "audit"."events" WHERE ("audit"."events"."id" = $1)`,
			multiInsert: `INSERT INTO "audit"."events" ("user_id", "action") VALUES ($1, $2), ($3, $4)`,
		},
		"mysql": {
			dialect:     dialect.MySQL(),
			insert:      "INSERT INTO `audit`.`events` (`user_id`, `action`) VALUES (?, ?)",
			update:      "UPDATE `audit`.`events` SET `action` = ? WHERE (`audit`.`events`.`id` = ?)",
			delete:      "DELETE FROM `audit`.`events` WHERE (`audit`.`events`.`id` = ?)",
			multiInsert: "INSERT INTO `audit`.`events` (`user_id`, `action`) VALUES (?, ?), (?, ?)",
		},
		"sqlite": {
			dialect:     dialect.SQLite(),
			insert:      `INSERT INTO "audit"."events" ("user_id", "action") VALUES (?, ?)`,
			update:      `UPDATE "audit"."events" SET "action" = ? WHERE ("audit"."events"."id" = ?)`,
			delete:      `DELETE FROM "audit"."events" WHERE ("audit"."events"."id" = ?)`,
			multiInsert: `INSERT INTO "audit"."events" ("user_id", "action") VALUES (?, ?), (?, ?)`,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rendered, err := render.Insert(test.dialect, insert)
			require.NoError(t, err)
			require.Equal(t, test.insert, rendered.SQL())

			rendered, err = render.Update(test.dialect, update)
			require.NoError(t, err)
			require.Equal(t, test.update, rendered.SQL())

			rendered, err = render.Delete(test.dialect, deleteStatement)
			require.NoError(t, err)
			require.Equal(t, test.delete, rendered.SQL())

			rendered, err = render.Insert(test.dialect, multiInsert)
			require.NoError(t, err)
			require.Equal(t, test.multiInsert, rendered.SQL())
		})
	}
}

// TestQualifiedUpsertRendersDialectConflictSyntax pins that a conflict
// target, an EXCLUDED/VALUES() operand, a SET target and a RETURNING
// projection all stay unqualified against a schema-qualified table, because
// each names a column of the statement's own table rather than a table
// reference.
func TestQualifiedUpsertRendersDialectConflictSyntax(t *testing.T) {
	events, id, _, action := qualifiedWriteTable(t)
	insert, err := query.NewInsert(events, query.Set(id, 1), query.Set(action, "created"))
	require.NoError(t, err)
	statement, err := query.NewUpsert(insert, []query.ColumnRef{id}, []query.Assignment{query.Set(action, query.Excluded(action))})
	require.NoError(t, err)
	statement, err = statement.WithReturning(id, action)
	require.NoError(t, err)
	mysqlStatement, err := query.NewUpsert(insert, nil, []query.Assignment{query.Set(action, query.Excluded(action))})
	require.NoError(t, err)

	rendered, err := render.Upsert(dialect.PostgreSQL(), statement)
	require.NoError(t, err)
	require.Equal(
		t,
		`INSERT INTO "audit"."events" ("id", "action") VALUES ($1, $2) ON CONFLICT ("id") DO UPDATE SET "action" = EXCLUDED."action" RETURNING "id", "action"`,
		rendered.SQL(),
	)

	rendered, err = render.Upsert(dialect.SQLite(), statement)
	require.NoError(t, err)
	require.Equal(
		t,
		`INSERT INTO "audit"."events" ("id", "action") VALUES (?, ?) ON CONFLICT ("id") DO UPDATE SET "action" = EXCLUDED."action" RETURNING "id", "action"`,
		rendered.SQL(),
	)

	rendered, err = render.Upsert(dialect.MySQL(), mysqlStatement)
	require.NoError(t, err)
	require.Equal(
		t,
		"INSERT INTO `audit`.`events` (`id`, `action`) VALUES (?, ?) ON DUPLICATE KEY UPDATE `action` = VALUES(`action`)",
		rendered.SQL(),
	)
}

func TestDeleteRendersNotInForBuiltInDialects(t *testing.T) {
	users, id, _ := writeTable(t)
	deleteStatement, err := query.NewDelete(users)
	require.NoError(t, err)
	deleteStatement, err = deleteStatement.WithWhere(query.NotIn(id, query.Bind(1), query.Bind(2)))
	require.NoError(t, err)

	tests := map[string]struct {
		dialect dialect.Dialect
		sql     string
	}{
		"postgresql": {
			dialect: dialect.PostgreSQL(),
			sql:     "DELETE FROM \"users\" WHERE (\"users\".\"id\" NOT IN ($1, $2))",
		},
		"mysql": {
			dialect: dialect.MySQL(),
			sql:     "DELETE FROM `users` WHERE (`users`.`id` NOT IN (?, ?))",
		},
		"sqlite": {
			dialect: dialect.SQLite(),
			sql:     "DELETE FROM \"users\" WHERE (\"users\".\"id\" NOT IN (?, ?))",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rendered, err := render.Delete(test.dialect, deleteStatement)
			require.NoError(t, err)
			require.Equal(t, test.sql, rendered.SQL())
			require.Equal(t, []any{1, 2}, rendered.Args())
		})
	}
}

// TestDeleteRendersSubqueryForBuiltInDialects covers the shape every supported
// engine runs: the subquery reads a table other than the one the DELETE
// targets, so no dialect capability is in question and all three render it.
func TestDeleteRendersSubqueryForBuiltInDialects(t *testing.T) {
	users, id, _ := writeTable(t)
	orders, userID, amount := deleteSubqueryTable(t)

	candidates, err := query.NewSelect(orders, userID)
	require.NoError(t, err)
	candidates, err = candidates.WithWhere(query.GreaterThan(amount, 100.0))
	require.NoError(t, err)

	deleteStatement, err := query.NewDelete(users)
	require.NoError(t, err)
	deleteStatement, err = deleteStatement.WithWhere(query.InSelect(id, candidates))
	require.NoError(t, err)

	tests := map[string]struct {
		dialect dialect.Dialect
		sql     string
	}{
		"postgresql": {
			dialect: dialect.PostgreSQL(),
			sql:     "DELETE FROM \"users\" WHERE (\"users\".\"id\" IN (SELECT \"orders\".\"user_id\" FROM \"orders\" WHERE (\"orders\".\"amount\" > $1)))",
		},
		"mysql": {
			dialect: dialect.MySQL(),
			sql:     "DELETE FROM `users` WHERE (`users`.`id` IN (SELECT `orders`.`user_id` FROM `orders` WHERE (`orders`.`amount` > ?)))",
		},
		"sqlite": {
			dialect: dialect.SQLite(),
			sql:     "DELETE FROM \"users\" WHERE (\"users\".\"id\" IN (SELECT \"orders\".\"user_id\" FROM \"orders\" WHERE (\"orders\".\"amount\" > ?)))",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rendered, err := render.Delete(test.dialect, deleteStatement)
			require.NoError(t, err)
			require.Equal(t, test.sql, rendered.SQL())
			require.Equal(t, []any{100.0}, rendered.Args())
		})
	}
}

// TestDeleteSubqueryReadingTargetIsRefusedForMySQL covers the one shape the
// three engines disagree about: a subquery that reads the very table the
// DELETE removes rows from. PostgreSQL and SQLite run it and hold
// dialect.CapabilityDeleteSubqueryTarget; MySQL answers error 1093 and does
// not, so rendering for MySQL fails here instead of at the server.
//
// Each case names the target through a different route, because MySQL refuses
// all of them alike: directly in the subquery's FROM, under an alias there, in
// a join of the subquery, inside a scalar comparison, and one subquery deep
// inside another. The live counterpart that pins MySQL's own 1093 for these
// same shapes is TestDeleteSubqueryReadingTargetIsRefusedByLiveMySQL in
// delete_subquery_integration_test.go; this test asserts only what rasql does
// with them.
func TestDeleteSubqueryReadingTargetIsRefusedForMySQL(t *testing.T) {
	users, id, email := writeTable(t)
	orders, userID, _ := deleteSubqueryTable(t)

	aliased, err := users.As("u")
	require.NoError(t, err)

	fromTarget, err := query.NewSelect(users, id)
	require.NoError(t, err)
	fromAliasedTarget, err := query.NewSelect(aliased, aliased.Column("id"))
	require.NoError(t, err)
	joinedTarget, err := query.NewJoinedSelect(orders, []query.Join{
		query.InnerJoin(users, query.Equal(userID, id)),
	}, nil, userID)
	require.NoError(t, err)
	fromOther, err := query.NewSelect(orders, userID)
	require.NoError(t, err)
	nestedTarget, err := fromOther.WithWhere(query.InSelect(userID, fromTarget))
	require.NoError(t, err)

	tests := map[string]query.Expression{
		"IN over the target":            query.InSelect(id, fromTarget),
		"NOT IN over the target":        query.NotInSelect(id, fromTarget),
		"IN over an alias of it":        query.InSelect(id, fromAliasedTarget),
		"IN over a join onto it":        query.InSelect(id, joinedTarget),
		"scalar comparison against it":  query.Equal(id, query.Scalar(fromTarget)),
		"target one subquery deeper":    query.InSelect(id, nestedTarget),
		"subquery nested under an AND":  query.And(query.InSelect(id, fromTarget), query.Equal(email, "ada@example.com")),
		"subquery nested under a NOT":   query.Negate(query.InSelect(id, fromTarget)),
		"subquery inside a COALESCE":    query.Equal(id, query.Coalesce(query.Scalar(fromTarget), query.Bind(0))),
		"subquery in the ORDER BY of a": query.InSelect(id, orderedByTargetSubquery(t, orders, userID, fromTarget)),
	}
	for name, predicate := range tests {
		t.Run(name, func(t *testing.T) {
			statement, err := query.NewDelete(users)
			require.NoError(t, err)
			statement, err = statement.WithWhere(predicate)
			require.NoError(t, err, "query validation accepts every one of these; only rendering for MySQL refuses")

			_, err = render.Delete(dialect.MySQL(), statement)
			require.Error(t, err)
			var refusal *render.SubqueryReadsDeleteTargetError
			require.ErrorAs(t, err, &refusal)
			require.Equal(t, "mysql", refusal.Dialect)
			require.Equal(t, "users", refusal.Table)
			require.ErrorIs(t, err, render.ErrSubqueryReadsDeleteTarget)

			for _, permissive := range []dialect.Dialect{dialect.PostgreSQL(), dialect.SQLite()} {
				_, err := render.Delete(permissive, statement)
				require.NoError(t, err, "%s runs this shape, so rendering must not refuse it", permissive.Name())
			}
		})
	}
}

// orderedByTargetSubquery builds a SELECT over source that orders by a scalar
// subquery, so the walk that looks for the DELETE's target has to reach a
// clause other than FROM, WHERE, and the projections to find it.
func orderedByTargetSubquery(t *testing.T, source query.TableRef, key query.ColumnRef, target query.Select) query.Select {
	t.Helper()
	statement, err := query.NewSelect(source, key)
	require.NoError(t, err)
	statement, err = statement.WithOrder(query.Asc(query.Scalar(target)))
	require.NoError(t, err)
	return statement
}

// TestSQLiteDeleteSubqueryReadingTargetExecutes proves SQLite really does run
// the shape MySQL refuses, rather than rasql merely granting SQLite the
// capability and never checking. It executes rasql's own rendered SQL against
// an in-memory SQLite database and reads the surviving rows back.
func TestSQLiteDeleteSubqueryReadingTargetExecutes(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)

	users, id, email := writeTable(t)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE \"users\" (\"id\" INTEGER PRIMARY KEY, \"email\" TEXT NOT NULL)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "INSERT INTO \"users\" (\"id\", \"email\") VALUES (1, 'ada@example.com'), (2, 'grace@example.com')")
	require.NoError(t, err)

	doomed, err := query.NewSelect(users, id)
	require.NoError(t, err)
	doomed, err = doomed.WithWhere(query.Equal(email, "ada@example.com"))
	require.NoError(t, err)

	statement, err := query.NewDelete(users)
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.InSelect(id, doomed))
	require.NoError(t, err)

	rendered, err := render.Delete(dialect.SQLite(), statement)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), rendered.SQL(), rendered.Args()...)
	require.NoError(t, err, "SQLite must run a DELETE whose subquery reads the target table")

	var surviving int
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM \"users\"").Scan(&surviving))
	require.Equal(t, 1, surviving)
}

func TestReturningRequiresDialectCapability(t *testing.T) {
	users, id, email := writeTable(t)
	statement, err := query.NewInsert(users, query.Set(id, 1), query.Set(email, "ada@example.com"))
	require.NoError(t, err)
	statement, err = statement.WithReturning(id)
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

// TestReturningRendersBareColumnRefUnqualified pins the render/write.go
// writeReturningProjection path: ColumnRef.ProjectedExpression returns the
// receiver, so the type assertion there still succeeds for a bare column
// passed to WithReturning with no query.Project wrapper, and RETURNING still
// renders the unqualified column name.
func TestReturningRendersBareColumnRefUnqualified(t *testing.T) {
	users, id, email := writeTable(t)
	statement, err := query.NewInsert(users, query.Set(id, 1), query.Set(email, "ada@example.com"))
	require.NoError(t, err)
	statement, err = statement.WithReturning(id)
	require.NoError(t, err)

	rendered, err := render.Insert(dialect.PostgreSQL(), statement)
	require.NoError(t, err)
	require.Equal(t, "INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2) RETURNING \"id\"", rendered.SQL())
}

func TestMultiRowInsertRendersReturning(t *testing.T) {
	users, id, email := writeTable(t)
	statement, err := query.NewInsertRows(users, []query.ColumnRef{id, email}, [][]any{
		{1, "ada@example.com"},
		{2, "grace@example.com"},
	})
	require.NoError(t, err)
	statement, err = statement.WithReturning(id, email)
	require.NoError(t, err)

	rendered, err := render.Insert(dialect.PostgreSQL(), statement)
	require.NoError(t, err)
	require.Equal(t, "INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2), ($3, $4) RETURNING \"id\", \"email\"", rendered.SQL())

	rendered, err = render.Insert(dialect.SQLite(), statement)
	require.NoError(t, err)
	require.Equal(t, "INSERT INTO \"users\" (\"id\", \"email\") VALUES (?, ?), (?, ?) RETURNING \"id\", \"email\"", rendered.SQL())

	_, err = render.Insert(dialect.MySQL(), statement)
	require.Error(t, err)
}

func TestDefaultInsertRendersDialectSyntax(t *testing.T) {
	users, _, _ := writeTable(t)
	statement, err := query.NewInsert(users, query.Defaults())
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
	insert, err := query.NewInsert(users, query.Set(id, 1), query.Set(email, "ada@example.com"))
	require.NoError(t, err)
	statement, err := query.NewUpsert(insert, []query.ColumnRef{id}, []query.Assignment{query.Set(email, query.Excluded(email))})
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

func TestMultiRowUpsertRendersDialectConflictSyntax(t *testing.T) {
	users, id, email := writeTable(t)
	insert, err := query.NewInsertRows(users, []query.ColumnRef{id, email}, [][]any{
		{1, "ada@example.com"},
		{2, "grace@example.com"},
	})
	require.NoError(t, err)
	statement, err := query.NewUpsert(insert, []query.ColumnRef{id}, []query.Assignment{query.Set(email, query.Excluded(email))})
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
			sql:       "INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2), ($3, $4) ON CONFLICT (\"id\") DO UPDATE SET \"email\" = EXCLUDED.\"email\"",
		},
		"mysql": {
			dialect:   dialect.MySQL(),
			statement: mysqlStatement,
			sql:       "INSERT INTO `users` (`id`, `email`) VALUES (?, ?), (?, ?) ON DUPLICATE KEY UPDATE `email` = VALUES(`email`)",
		},
		"sqlite": {
			dialect:   dialect.SQLite(),
			statement: statement,
			sql:       "INSERT INTO \"users\" (\"id\", \"email\") VALUES (?, ?), (?, ?) ON CONFLICT (\"id\") DO UPDATE SET \"email\" = EXCLUDED.\"email\"",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rendered, err := render.Upsert(test.dialect, test.statement)
			require.NoError(t, err)
			require.Equal(t, test.sql, rendered.SQL())
			require.Equal(t, []any{1, "ada@example.com", 2, "grace@example.com"}, rendered.Args())
		})
	}
}

func TestMySQLUpsertRejectsConflictTarget(t *testing.T) {
	users, id, email := writeTable(t)
	insert, err := query.NewInsert(users, query.Set(id, 1), query.Set(email, "ada@example.com"))
	require.NoError(t, err)
	withAssignments, err := query.NewUpsert(insert, []query.ColumnRef{id}, []query.Assignment{query.Set(email, query.Excluded(email))})
	require.NoError(t, err)
	withoutAssignments, err := query.NewUpsert(insert, []query.ColumnRef{id}, nil)
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

func (upsertOnlyDialect) TypeName(schema.ColumnDef) (string, error) { return "", nil }

func (d upsertOnlyDialect) UpsertStyle() dialect.UpsertStyle { return d.style }

func (upsertOnlyDialect) Supports(capability dialect.Capability) bool {
	return capability&dialect.CapabilityUpsert == capability
}

func TestUpsertReportsEveryUnsupportedFeature(t *testing.T) {
	users, id, email := writeTable(t)
	defaultInsert, err := query.NewInsert(users, query.Defaults())
	require.NoError(t, err)
	withAssignments, err := query.NewUpsert(defaultInsert, []query.ColumnRef{id}, []query.Assignment{query.Set(email, query.Excluded(email))})
	require.NoError(t, err)
	withoutAssignments, err := query.NewUpsert(defaultInsert, []query.ColumnRef{id}, nil)
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

	insert, err := query.NewInsert(users, query.Set(id, 1), query.Set(email, "ada@example.com"))
	require.NoError(t, err)
	statement, err := query.NewUpsert(insert, []query.ColumnRef{id}, []query.Assignment{query.Set(email, query.Excluded(email))})
	require.NoError(t, err)
	rendered, err := render.Upsert(dialect.SQLite(), statement)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), rendered.SQL(), rendered.Args()...)
	require.NoError(t, err)

	insert, err = query.NewInsert(users, query.Set(id, 1), query.Set(email, "grace@example.com"))
	require.NoError(t, err)
	statement, err = query.NewUpsert(insert, []query.ColumnRef{id}, []query.Assignment{query.Set(email, query.Excluded(email))})
	require.NoError(t, err)
	rendered, err = render.Upsert(dialect.SQLite(), statement)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), rendered.SQL(), rendered.Args()...)
	require.NoError(t, err)

	var actual string
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT \"email\" FROM \"users\" WHERE \"id\" = 1").Scan(&actual))
	require.Equal(t, "grace@example.com", actual)
}

// TestUpsertRendersNestedExcludedColumn proves the renderer fix for the
// pre-existing defect the design investigation found: validation accepts an
// ExcludedColumn nested inside another expression, such as inside COALESCE,
// but the renderer used to recognise one only at the top level of a
// conflict-update assignment's value. writeExcludedColumn now renders one at
// any depth inside the value, across all three built-in dialects.
func TestUpsertRendersNestedExcludedColumn(t *testing.T) {
	users, id, email := writeTable(t)
	insert, err := query.NewInsert(users, query.Set(id, 1), query.Set(email, "ada@example.com"))
	require.NoError(t, err)
	statement, err := query.NewUpsert(insert, []query.ColumnRef{id}, []query.Assignment{
		query.Set(email, query.Coalesce(query.Excluded(email), query.Bind("unknown@example.com"))),
	})
	require.NoError(t, err)
	mysqlStatement, err := query.NewUpsert(insert, nil, []query.Assignment{
		query.Set(email, query.Coalesce(query.Excluded(email), query.Bind("unknown@example.com"))),
	})
	require.NoError(t, err)

	tests := map[string]struct {
		dialect   dialect.Dialect
		statement query.Upsert
		sql       string
	}{
		"postgresql": {
			dialect:   dialect.PostgreSQL(),
			statement: statement,
			sql:       "INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2) ON CONFLICT (\"id\") DO UPDATE SET \"email\" = COALESCE(EXCLUDED.\"email\", $3)",
		},
		"mysql": {
			dialect:   dialect.MySQL(),
			statement: mysqlStatement,
			sql:       "INSERT INTO `users` (`id`, `email`) VALUES (?, ?) ON DUPLICATE KEY UPDATE `email` = COALESCE(VALUES(`email`), ?)",
		},
		"sqlite": {
			dialect:   dialect.SQLite(),
			statement: statement,
			sql:       "INSERT INTO \"users\" (\"id\", \"email\") VALUES (?, ?) ON CONFLICT (\"id\") DO UPDATE SET \"email\" = COALESCE(EXCLUDED.\"email\", ?)",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rendered, err := render.Upsert(test.dialect, test.statement)
			require.NoError(t, err)
			require.Equal(t, test.sql, rendered.SQL())
			require.Equal(t, []any{1, "ada@example.com", "unknown@example.com"}, rendered.Args())
		})
	}
}

// TestSQLiteUpsertExecutesNestedExcludedColumn proves the rendered nested
// EXCLUDED reference actually runs: the conflicting row's EXCLUDED.email is
// not NULL, so COALESCE keeps it rather than falling back to the bound
// default.
func TestSQLiteUpsertExecutesNestedExcludedColumn(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)

	users, id, email := writeTable(t)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE \"users\" (\"id\" INTEGER PRIMARY KEY, \"email\" TEXT NOT NULL)")
	require.NoError(t, err)

	insert, err := query.NewInsert(users, query.Set(id, 1), query.Set(email, "ada@example.com"))
	require.NoError(t, err)
	statement, err := query.NewUpsert(insert, []query.ColumnRef{id}, []query.Assignment{
		query.Set(email, query.Coalesce(query.Excluded(email), query.Bind("unknown@example.com"))),
	})
	require.NoError(t, err)
	rendered, err := render.Upsert(dialect.SQLite(), statement)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), rendered.SQL(), rendered.Args()...)
	require.NoError(t, err)

	insert, err = query.NewInsert(users, query.Set(id, 1), query.Set(email, "grace@example.com"))
	require.NoError(t, err)
	statement, err = query.NewUpsert(insert, []query.ColumnRef{id}, []query.Assignment{
		query.Set(email, query.Coalesce(query.Excluded(email), query.Bind("unknown@example.com"))),
	})
	require.NoError(t, err)
	rendered, err = render.Upsert(dialect.SQLite(), statement)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), rendered.SQL(), rendered.Args()...)
	require.NoError(t, err)

	var actual string
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT \"email\" FROM \"users\" WHERE \"id\" = 1").Scan(&actual))
	require.Equal(t, "grace@example.com", actual)
}

// TestUpdateRejectsExcludedColumnOutsideUpsertAssignment proves the scope of
// the renderer fix above: validation admits ExcludedColumn wherever its
// source table is in scope, which includes an UPDATE's own WHERE clause, but
// EXCLUDED means nothing there. writeExcludedColumn refuses it with a named
// error instead of falling through to the generic "unsupported expression"
// message the same shape reported before this change added a case for it.
func TestUpdateRejectsExcludedColumnOutsideUpsertAssignment(t *testing.T) {
	users, id, email := writeTable(t)
	update, err := query.NewUpdate(users, query.Set(email, query.Bind("grace@example.com")))
	require.NoError(t, err)
	update, err = update.WithWhere(query.Equal(id, query.Excluded(id)))
	require.NoError(t, err)

	_, err = render.Update(dialect.SQLite(), update)
	require.ErrorContains(t, err, `references the excluded column "id" outside an upsert conflict-update assignment`)
}

func TestSQLiteMultiRowInsertExecutes(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)

	users, id, email := writeTable(t)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE \"users\" (\"id\" INTEGER PRIMARY KEY, \"email\" TEXT NOT NULL)")
	require.NoError(t, err)

	statement, err := query.NewInsertRows(users, []query.ColumnRef{id, email}, [][]any{
		{1, "ada@example.com"},
		{2, "grace@example.com"},
		{3, "edsger@example.com"},
	})
	require.NoError(t, err)
	rendered, err := render.Insert(dialect.SQLite(), statement)
	require.NoError(t, err)
	result, err := database.ExecContext(t.Context(), rendered.SQL(), rendered.Args()...)
	require.NoError(t, err)
	affected, err := result.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 3, affected)

	rows, err := database.QueryContext(t.Context(), "SELECT \"id\", \"email\" FROM \"users\" ORDER BY \"id\"")
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	var ids []int
	var emails []string
	for rows.Next() {
		var gotID int
		var gotEmail string
		require.NoError(t, rows.Scan(&gotID, &gotEmail))
		ids = append(ids, gotID)
		emails = append(emails, gotEmail)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []int{1, 2, 3}, ids)
	require.Equal(t, []string{"ada@example.com", "grace@example.com", "edsger@example.com"}, emails)
}

func TestSQLiteDefaultValuesUpsertIsRejected(t *testing.T) {
	users, id, email := writeTable(t)
	insert, err := query.NewInsert(users, query.Defaults())
	require.NoError(t, err)
	statement, err := query.NewUpsert(insert, []query.ColumnRef{id}, []query.Assignment{query.Set(email, query.Excluded(email))})
	require.NoError(t, err)

	_, err = render.Upsert(dialect.SQLite(), statement)
	require.ErrorContains(t, err, "default-values upsert is not supported")
}

func qualifiedWriteTable(t *testing.T) (query.TableRef, query.ColumnRef, query.ColumnRef, query.ColumnRef) {
	t.Helper()
	events, err := query.NewTableRef(schema.TableDef{
		Schema: "audit",
		Name:   "events",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
			{Name: "action", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	id := events.Column("id")
	userID := events.Column("user_id")
	action := events.Column("action")
	return events, id, userID, action
}

func writeTable(t *testing.T) (query.TableRef, query.ColumnRef, query.ColumnRef) {
	t.Helper()
	users, err := query.NewTableRef(schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	id := users.Column("id")
	email := users.Column("email")
	return users, id, email
}

// deleteSubqueryTable is the second table the DELETE subquery tests need: one
// a subquery can read that is not the table the DELETE targets, so a shape
// every engine accepts can be told apart from the self-referencing shape MySQL
// refuses.
func deleteSubqueryTable(t *testing.T) (query.TableRef, query.ColumnRef, query.ColumnRef) {
	t.Helper()
	orders, err := query.NewTableRef(schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.FloatType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	return orders, orders.Column("user_id"), orders.Column("amount")
}
