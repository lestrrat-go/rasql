//go:build unix

package render_test

import (
	"testing"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// deleteSubqueryTables builds the two table refs the live DELETE subquery
// tests share: the table a DELETE targets, and a second one a subquery can
// read instead of the target.
func deleteSubqueryTables(t *testing.T) (query.TableRef, query.TableRef) {
	t.Helper()
	people, err := query.NewTableRef(schema.TableDef{
		Name: "live_delete_people",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{Width: schema.NewTextWidth(255)}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	orders, err := query.NewTableRef(schema.TableDef{
		Name: "live_delete_orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "person_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	return people, orders
}

// TestDeleteSubqueryReadingTargetIsRefusedByLiveMySQL pins the MySQL rule that
// dialect.CapabilityWriteSubqueryTarget's absence rests on: a DELETE whose
// WHERE subquery reads the target table fails with MySQL's own error 1093, not
// merely "rasql refuses to build one." The statement is issued directly here,
// bypassing render, precisely because render already refuses the shape for a
// dialect without the capability before it would ever reach the server; the
// second half of the test then checks that rasql's refusal is the one a caller
// sees for the same statement.
func TestDeleteSubqueryReadingTargetIsRefusedByLiveMySQL(t *testing.T) {
	ctx := t.Context()
	database := dbtest.MySQLDB(t)

	_, err := database.ExecContext(ctx, "CREATE TABLE live_delete_people (id BIGINT PRIMARY KEY, email VARCHAR(255) NOT NULL) ENGINE=InnoDB")
	require.NoError(t, err, "create live table")
	_, err = database.ExecContext(ctx, "INSERT INTO live_delete_people (id, email) VALUES (1, 'ada@example.com'), (2, 'grace@example.com')")
	require.NoError(t, err, "seed live table")

	_, err = database.ExecContext(ctx, "DELETE FROM live_delete_people WHERE id IN (SELECT id FROM live_delete_people WHERE email = 'ada@example.com')")
	require.Error(t, err, "MySQL must refuse a DELETE whose WHERE subquery reads the target table")

	var mysqlErr *gomysql.MySQLError
	require.ErrorAs(t, err, &mysqlErr, "the refusal must come from MySQL itself, not from a connection or driver failure")
	require.EqualValues(t, 1093, mysqlErr.Number, "MySQL's own error number for reading the target table in a DELETE subquery")

	// The same statement built through rasql: render refuses it for MySQL with
	// its own typed error rather than sending the server SQL that earns 1093.
	people, _ := deleteSubqueryTables(t)
	id := people.Column("id")
	doomed, err := query.NewSelect(people, id)
	require.NoError(t, err)
	doomed, err = doomed.WithWhere(query.Equal(people.Column("email"), "ada@example.com"))
	require.NoError(t, err)
	statement, err := query.NewDelete(people)
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.InSelect(id, doomed))
	require.NoError(t, err, "query validation accepts the shape; only rendering for MySQL refuses it")

	_, err = render.Delete(dialect.MySQL(), statement)
	var refusal *render.SubqueryReadsWriteTargetError
	require.ErrorAs(t, err, &refusal, "rendering for MySQL must refuse the statement MySQL answers 1093 to")
	require.Equal(t, "live_delete_people", refusal.Table)
}

// TestDeleteSubqueryReadingAnotherTableExecutesAgainstLiveMySQL is the other
// half of the MySQL rule: only the target table is off limits to the subquery,
// so a subquery reading a different table renders for MySQL and the server runs
// it. Without this, granting no capability at all to MySQL would look equally
// correct.
func TestDeleteSubqueryReadingAnotherTableExecutesAgainstLiveMySQL(t *testing.T) {
	ctx := t.Context()
	database := dbtest.MySQLDB(t)

	_, err := database.ExecContext(ctx, "CREATE TABLE live_delete_people (id BIGINT PRIMARY KEY, email VARCHAR(255) NOT NULL) ENGINE=InnoDB")
	require.NoError(t, err, "create live target table")
	_, err = database.ExecContext(ctx, "CREATE TABLE live_delete_orders (id BIGINT PRIMARY KEY, person_id BIGINT NOT NULL) ENGINE=InnoDB")
	require.NoError(t, err, "create live subquery table")
	_, err = database.ExecContext(ctx, "INSERT INTO live_delete_people (id, email) VALUES (1, 'ada@example.com'), (2, 'grace@example.com')")
	require.NoError(t, err, "seed live target table")
	_, err = database.ExecContext(ctx, "INSERT INTO live_delete_orders (id, person_id) VALUES (10, 1)")
	require.NoError(t, err, "seed live subquery table")

	people, orders := deleteSubqueryTables(t)
	statement := deleteWherePersonHasAnOrder(t, people, orders)

	rendered, err := render.Delete(dialect.MySQL(), statement)
	require.NoError(t, err, "a subquery over a table other than the target renders for MySQL")

	_, err = database.ExecContext(ctx, rendered.SQL(), rendered.Args()...)
	require.NoError(t, err, "MySQL must run render.Delete's own subquery output")

	var surviving int
	require.NoError(t, database.QueryRowContext(ctx, "SELECT COUNT(*) FROM live_delete_people").Scan(&surviving))
	require.Equal(t, 1, surviving, "the one person with an order must be gone")
}

// TestDeleteSubqueryExecutesAgainstLivePostgreSQL proves the capability
// PostgreSQL holds is real rather than assumed: PostgreSQL runs render.Delete's
// own output for a subquery reading the target table, which is exactly the
// statement MySQL answers 1093 to, and for a subquery reading another table.
func TestDeleteSubqueryExecutesAgainstLivePostgreSQL(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)

	_, err := database.ExecContext(ctx, "CREATE TABLE live_delete_people (id BIGINT PRIMARY KEY, email TEXT NOT NULL)")
	require.NoError(t, err, "create live target table")
	_, err = database.ExecContext(ctx, "CREATE TABLE live_delete_orders (id BIGINT PRIMARY KEY, person_id BIGINT NOT NULL)")
	require.NoError(t, err, "create live subquery table")
	_, err = database.ExecContext(ctx, "INSERT INTO live_delete_people (id, email) VALUES (1, 'ada@example.com'), (2, 'grace@example.com'), (3, 'edsger@example.com')")
	require.NoError(t, err, "seed live target table")
	_, err = database.ExecContext(ctx, "INSERT INTO live_delete_orders (id, person_id) VALUES (10, 1)")
	require.NoError(t, err, "seed live subquery table")

	people, orders := deleteSubqueryTables(t)
	id := people.Column("id")

	byOrder := deleteWherePersonHasAnOrder(t, people, orders)
	rendered, err := render.Delete(dialect.PostgreSQL(), byOrder)
	require.NoError(t, err)
	_, err = database.ExecContext(ctx, rendered.SQL(), rendered.Args()...)
	require.NoError(t, err, "PostgreSQL must run a DELETE whose subquery reads another table")

	doomed, err := query.NewSelect(people, id)
	require.NoError(t, err)
	doomed, err = doomed.WithWhere(query.Equal(people.Column("email"), "grace@example.com"))
	require.NoError(t, err)
	selfReferencing, err := query.NewDelete(people)
	require.NoError(t, err)
	selfReferencing, err = selfReferencing.WithWhere(query.InSelect(id, doomed))
	require.NoError(t, err)

	rendered, err = render.Delete(dialect.PostgreSQL(), selfReferencing)
	require.NoError(t, err, "PostgreSQL holds dialect.CapabilityWriteSubqueryTarget, so rendering must not refuse this")
	_, err = database.ExecContext(ctx, rendered.SQL(), rendered.Args()...)
	require.NoError(t, err, "PostgreSQL must run the very statement MySQL answers 1093 to")

	var surviving int
	require.NoError(t, database.QueryRowContext(ctx, "SELECT COUNT(*) FROM live_delete_people").Scan(&surviving))
	require.Equal(t, 1, surviving, "only edsger, who has no order and is not grace, may remain")
}

// TestUpdateSubqueryReadingTargetIsRefusedByLiveMySQL is
// TestDeleteSubqueryReadingTargetIsRefusedByLiveMySQL for an UPDATE, and it
// covers both clauses that reach a subquery, because MySQL answers 1093 to
// each: a subquery reading the target from the WHERE clause, and one reading
// it from a SET assignment's value. Each statement is issued directly to pin
// the server's own error, then built through rasql to check that render
// refuses it before the server ever sees it.
func TestUpdateSubqueryReadingTargetIsRefusedByLiveMySQL(t *testing.T) {
	database := dbtest.MySQLDB(t)

	_, err := database.ExecContext(t.Context(), "CREATE TABLE live_delete_people (id BIGINT PRIMARY KEY, email VARCHAR(255) NOT NULL) ENGINE=InnoDB")
	require.NoError(t, err, "create live table")
	_, err = database.ExecContext(t.Context(), "INSERT INTO live_delete_people (id, email) VALUES (1, 'ada@example.com'), (2, 'grace@example.com')")
	require.NoError(t, err, "seed live table")

	live := map[string]string{
		"WHERE reads the target":     "UPDATE live_delete_people SET email = 'x@example.com' WHERE id IN (SELECT id FROM live_delete_people WHERE email = 'ada@example.com')",
		"SET value reads the target": "UPDATE live_delete_people SET email = (SELECT MAX(email) FROM live_delete_people) WHERE id = 1",
	}
	for name, statement := range live {
		t.Run("MySQL itself: "+name, func(t *testing.T) {
			_, err := database.ExecContext(t.Context(), statement)
			require.Error(t, err, "MySQL must refuse an UPDATE whose subquery reads the target table")

			var mysqlErr *gomysql.MySQLError
			require.ErrorAs(t, err, &mysqlErr, "the refusal must come from MySQL itself, not from a connection or driver failure")
			require.EqualValues(t, 1093, mysqlErr.Number, "MySQL's own error number for reading the target table in an UPDATE subquery")
		})
	}

	people, _ := deleteSubqueryTables(t)
	id := people.Column("id")
	email := people.Column("email")
	fromTarget, err := query.NewSelect(people, id)
	require.NoError(t, err)

	byWhere, err := query.NewUpdate(people, query.Set(email, "x@example.com"))
	require.NoError(t, err)
	byWhere, err = byWhere.WithWhere(query.InSelect(id, fromTarget))
	require.NoError(t, err, "query validation accepts the shape; only rendering for MySQL refuses it")

	bySet, err := query.NewUpdate(people, query.Set(email, query.Scalar(fromTarget)))
	require.NoError(t, err)
	bySet, err = bySet.WithWhere(query.Equal(id, 1))
	require.NoError(t, err)

	for name, statement := range map[string]query.Update{"WHERE reads the target": byWhere, "SET value reads the target": bySet} {
		t.Run("rasql: "+name, func(t *testing.T) {
			_, err := render.Update(dialect.MySQL(), statement)
			var refusal *render.SubqueryReadsWriteTargetError
			require.ErrorAs(t, err, &refusal, "rendering for MySQL must refuse the statement MySQL answers 1093 to")
			require.Equal(t, "UPDATE", refusal.Operation)
			require.Equal(t, "live_delete_people", refusal.Table)
		})
	}
}

// TestUpdateSubqueryExecutesAgainstLivePostgreSQL proves the capability
// PostgreSQL holds covers an UPDATE too: PostgreSQL runs render.Update's own
// output for a subquery reading the target table from either clause, which is
// exactly what MySQL answers 1093 to, and for one reading another table.
func TestUpdateSubqueryExecutesAgainstLivePostgreSQL(t *testing.T) {
	database := dbtest.PostgreSQLDB(t)

	_, err := database.ExecContext(t.Context(), "CREATE TABLE live_delete_people (id BIGINT PRIMARY KEY, email TEXT NOT NULL)")
	require.NoError(t, err, "create live target table")
	_, err = database.ExecContext(t.Context(), "CREATE TABLE live_delete_orders (id BIGINT PRIMARY KEY, person_id BIGINT NOT NULL)")
	require.NoError(t, err, "create live subquery table")
	_, err = database.ExecContext(t.Context(), "INSERT INTO live_delete_people (id, email) VALUES (1, 'ada@example.com'), (2, 'grace@example.com')")
	require.NoError(t, err, "seed live target table")
	_, err = database.ExecContext(t.Context(), "INSERT INTO live_delete_orders (id, person_id) VALUES (10, 1)")
	require.NoError(t, err, "seed live subquery table")

	people, orders := deleteSubqueryTables(t)
	id := people.Column("id")
	email := people.Column("email")

	buyers, err := query.NewSelect(orders, orders.Column("person_id"))
	require.NoError(t, err)
	byOtherTable, err := query.NewUpdate(people, query.Set(email, "buyer@example.com"))
	require.NoError(t, err)
	byOtherTable, err = byOtherTable.WithWhere(query.InSelect(id, buyers))
	require.NoError(t, err)

	rendered, err := render.Update(dialect.PostgreSQL(), byOtherTable)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), rendered.SQL(), rendered.Args()...)
	require.NoError(t, err, "PostgreSQL must run an UPDATE whose subquery reads another table")

	var updated string
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT email FROM live_delete_people WHERE id = 1").Scan(&updated))
	require.Equal(t, "buyer@example.com", updated, "the one person with an order must have been updated")

	fromTarget, err := query.NewSelect(people, query.Max(email))
	require.NoError(t, err)
	bySet, err := query.NewUpdate(people, query.Set(email, query.Scalar(fromTarget)))
	require.NoError(t, err)
	bySet, err = bySet.WithWhere(query.Equal(id, 2))
	require.NoError(t, err)

	rendered, err = render.Update(dialect.PostgreSQL(), bySet)
	require.NoError(t, err, "PostgreSQL holds dialect.CapabilityWriteSubqueryTarget, so rendering must not refuse this")
	_, err = database.ExecContext(t.Context(), rendered.SQL(), rendered.Args()...)
	require.NoError(t, err, "PostgreSQL must run the very statement MySQL answers 1093 to")

	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT email FROM live_delete_people WHERE id = 2").Scan(&updated))
	require.Equal(t, "grace@example.com", updated, "MAX over the two addresses is grace's, which row 2 already held")
}

// deleteWherePersonHasAnOrder builds DELETE FROM people WHERE id IN (SELECT
// person_id FROM orders), the shape every supported engine runs.
func deleteWherePersonHasAnOrder(t *testing.T, people query.TableRef, orders query.TableRef) query.Delete {
	t.Helper()
	buyers, err := query.NewSelect(orders, orders.Column("person_id"))
	require.NoError(t, err)
	statement, err := query.NewDelete(people)
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.InSelect(people.Column("id"), buyers))
	require.NoError(t, err)
	return statement
}
