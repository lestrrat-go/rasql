package render_test

import (
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestSelectLocksRenderAndGate(t *testing.T) {
	table := query.MustTableRef(schema.MustTableDef("queue", schema.Integer("id")))
	statement, err := query.NewSelect(table, table.Column("id"))
	require.NoError(t, err)
	statement, err = statement.WithOrder(query.Asc(table.Column("id")))
	require.NoError(t, err)
	statement, err = statement.WithLimit(1)
	require.NoError(t, err)
	statement, err = statement.WithLock(query.RowLock(query.LockUpdate).Of(table).Wait(query.LockWaitSkipLocked))
	require.NoError(t, err)
	for _, test := range []struct {
		name    string
		dialect dialect.Dialect
		sql     string
	}{
		{"postgresql", dialect.PostgreSQL(), `SELECT "queue"."id" FROM "queue" ORDER BY "queue"."id" LIMIT $1 FOR UPDATE OF "queue" SKIP LOCKED`},
		{"mysql", dialect.MySQL(), "SELECT `queue`.`id` FROM `queue` ORDER BY `queue`.`id` LIMIT ? FOR UPDATE OF `queue` SKIP LOCKED"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rendered, err := render.Select(test.dialect, statement)
			require.NoError(t, err)
			require.Equal(t, test.sql, rendered.SQL())
			require.Equal(t, []any{1}, rendered.Args())
		})
	}
	_, err = render.Select(dialect.SQLite(), statement)
	var unsupported *render.UnsupportedSelectLockError
	require.ErrorAs(t, err, &unsupported)
	require.True(t, errors.Is(err, render.ErrUnsupportedSelectLock))
}

func TestConditionalUpsertRendersAndGates(t *testing.T) {
	table := query.MustTableRef(schema.MustTableDef("items", schema.Integer("id"), schema.Integer("version"), schema.Text("payload")))
	id, version, payload := table.Column("id"), table.Column("version"), table.Column("payload")
	insert, err := query.NewInsert(table, query.Set(id, 1), query.Set(version, 3), query.Set(payload, "new"))
	require.NoError(t, err)
	statement, err := query.NewUpsert(insert, []query.ColumnRef{id}, []query.Assignment{
		query.Set(version, query.Excluded(version)), query.Set(payload, query.Excluded(payload)),
	})
	require.NoError(t, err)
	statement, err = statement.WithConflictWhere(query.GreaterThan(version, 1))
	require.NoError(t, err)
	statement, err = statement.WithUpdateWhere(query.LessThan(version, query.Excluded(version)))
	require.NoError(t, err)
	postgres, err := render.Upsert(dialect.PostgreSQL(), statement)
	require.NoError(t, err)
	require.Equal(t, `INSERT INTO "items" ("id", "version", "payload") VALUES ($1, $2, $3) ON CONFLICT ("id") WHERE ("items"."version" > $4) DO UPDATE SET "version" = EXCLUDED."version", "payload" = EXCLUDED."payload" WHERE ("items"."version" < EXCLUDED."version")`, postgres.SQL())
	require.Equal(t, []any{1, 3, "new", 1}, postgres.Args())
	sqlite, err := render.Upsert(dialect.SQLite(), statement)
	require.NoError(t, err)
	require.Equal(t, `INSERT INTO "items" ("id", "version", "payload") VALUES (?, ?, ?) ON CONFLICT ("id") WHERE ("items"."version" > ?) DO UPDATE SET "version" = EXCLUDED."version", "payload" = EXCLUDED."payload" WHERE ("items"."version" < EXCLUDED."version")`, sqlite.SQL())
	_, err = render.Upsert(dialect.MySQL(), statement)
	require.ErrorAs(t, err, new(*render.UnsupportedUpsertPredicateError))
}
