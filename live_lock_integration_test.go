package rasql_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

func TestLiveSelectLock(t *testing.T) {
	t.Run("SKIP LOCKED claims different rows", func(t *testing.T) {
		for _, test := range []struct {
			name    string
			open    func(*testing.T) *sql.DB
			dialect dialect.Dialect
		}{
			{"postgresql", dbtest.PostgreSQLDB, dialect.PostgreSQL()},
			{"mysql", dbtest.MySQLDB, dialect.MySQL()},
		} {
			t.Run(test.name, func(t *testing.T) {
				database := test.open(t)
				tableName := dbtest.UniqueName(t, "q5_queue")
				_, err := database.ExecContext(t.Context(), "CREATE TABLE "+tableName+" (id INTEGER PRIMARY KEY, claimed INTEGER NOT NULL)")
				require.NoError(t, err)
				t.Cleanup(func() { _, _ = database.ExecContext(t.Context(), "DROP TABLE "+tableName) })
				_, err = database.ExecContext(t.Context(), "INSERT INTO "+tableName+" (id, claimed) VALUES (1, 0), (2, 0)")
				require.NoError(t, err)
				table := query.MustTableRef(schema.MustTableDef(tableName, schema.Integer("id"), schema.Integer("claimed")))
				update, err := query.NewUpdate(table, query.Set(table.Column("claimed"), 1))
				require.NoError(t, err)
				statement, err := query.NewSelect(table, table.Column("id"))
				require.NoError(t, err)
				statement, err = statement.WithWhere(query.Equal(table.Column("claimed"), 0))
				require.NoError(t, err)
				statement, err = statement.WithOrder(query.Asc(table.Column("id")))
				require.NoError(t, err)
				statement, err = statement.WithLimit(1)
				require.NoError(t, err)
				statement, err = statement.WithLock(query.RowLock(query.LockUpdate).Wait(query.LockWaitSkipLocked))
				require.NoError(t, err)
				rendered, err := render.Select(test.dialect, statement)
				require.NoError(t, err)
				firstTx, err := database.BeginTx(t.Context(), nil)
				require.NoError(t, err)
				defer func() { _ = firstTx.Rollback() }()
				secondTx, err := database.BeginTx(t.Context(), nil)
				require.NoError(t, err)
				defer func() { _ = secondTx.Rollback() }()
				var firstID, secondID int64
				require.NoError(t, firstTx.QueryRowContext(t.Context(), rendered.SQL(), rendered.Args()...).Scan(&firstID))
				require.NoError(t, secondTx.QueryRowContext(t.Context(), rendered.SQL(), rendered.Args()...).Scan(&secondID))
				require.NotEqual(t, firstID, secondID)
				firstUpdate, err := update.WithWhere(query.Equal(table.Column("id"), firstID))
				require.NoError(t, err)
				firstRendered, err := render.Update(test.dialect, firstUpdate)
				require.NoError(t, err)
				_, err = firstTx.ExecContext(t.Context(), firstRendered.SQL(), firstRendered.Args()...)
				require.NoError(t, err)
				secondUpdate, err := update.WithWhere(query.Equal(table.Column("id"), secondID))
				require.NoError(t, err)
				secondRendered, err := render.Update(test.dialect, secondUpdate)
				require.NoError(t, err)
				_, err = secondTx.ExecContext(t.Context(), secondRendered.SQL(), secondRendered.Args()...)
				require.NoError(t, err)
				require.NoError(t, firstTx.Commit())
				require.NoError(t, secondTx.Commit())
				var claimed int
				require.NoError(t, database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+tableName+" WHERE claimed = 1").Scan(&claimed))
				require.Equal(t, 2, claimed)
			})
		}
	})

	t.Run("lock capabilities", func(t *testing.T) {
		for _, test := range []struct {
			name     string
			open     func(*testing.T) *sql.DB
			dialect  dialect.Dialect
			strength []query.LockStrength
		}{
			{"postgresql", dbtest.PostgreSQLDB, dialect.PostgreSQL(), []query.LockStrength{query.LockUpdate, query.LockNoKeyUpdate, query.LockShare, query.LockKeyShare}},
			{"mysql", dbtest.MySQLDB, dialect.MySQL(), []query.LockStrength{query.LockUpdate, query.LockShare}},
		} {
			t.Run(test.name, func(t *testing.T) {
				database := test.open(t)
				tableName := dbtest.UniqueName(t, "q5_locks")
				_, err := database.ExecContext(t.Context(), "CREATE TABLE "+tableName+" (id INTEGER PRIMARY KEY)")
				require.NoError(t, err)
				t.Cleanup(func() { _, _ = database.ExecContext(t.Context(), "DROP TABLE "+tableName) })
				_, err = database.ExecContext(t.Context(), "INSERT INTO "+tableName+" (id) VALUES (1)")
				require.NoError(t, err)
				table := query.MustTableRef(schema.MustTableDef(tableName, schema.Integer("id")))
				for _, strength := range test.strength {
					for _, wait := range []query.LockWait{query.LockWaitDefault, query.LockWaitNoWait, query.LockWaitSkipLocked} {
						statement, buildErr := query.NewSelect(table, table.Column("id"))
						require.NoError(t, buildErr)
						statement, buildErr = statement.WithLock(query.RowLock(strength).Of(table).Wait(wait))
						require.NoError(t, buildErr)
						rendered, renderErr := render.Select(test.dialect, statement)
						require.NoError(t, renderErr)
						tx, beginErr := database.BeginTx(t.Context(), nil)
						require.NoError(t, beginErr)
						var id int
						require.NoError(t, tx.QueryRowContext(t.Context(), rendered.SQL(), rendered.Args()...).Scan(&id))
						require.Equal(t, 1, id)
						require.NoError(t, tx.Rollback())
					}
				}
			})
		}
	})
}

func TestLiveConditionalUpsert(t *testing.T) {
	database := dbtest.PostgreSQLDB(t)
	tableName := dbtest.UniqueName(t, "q5_versions")
	_, err := database.ExecContext(t.Context(), "CREATE TABLE "+tableName+" (id INTEGER PRIMARY KEY, version INTEGER NOT NULL, payload TEXT NOT NULL)")
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = database.ExecContext(t.Context(), "DROP TABLE "+tableName) })
	table := query.MustTableRef(schema.MustTableDef(tableName, schema.Integer("id"), schema.Integer("version"), schema.Text("payload")))
	id, version, payload := table.Column("id"), table.Column("version"), table.Column("payload")
	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	profile, err := rasql.DiscoverEngineProfile(t.Context(), db, "postgresql-17")
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	seed, err := query.NewInsert(table, query.Set(id, 1), query.Set(version, 2), query.Set(payload, "v2"))
	require.NoError(t, err)
	seedPlan, err := rasql.NewStatementPlan(seed)
	require.NoError(t, err)
	_, err = rasql.ExecMutation(t.Context(), executor, seedPlan)
	require.NoError(t, err)
	run := func(versionValue int, payloadValue string) {
		insert, buildErr := query.NewInsert(table, query.Set(id, 1), query.Set(version, versionValue), query.Set(payload, payloadValue))
		require.NoError(t, buildErr)
		statement, buildErr := query.NewUpsert(insert, []query.ColumnRef{id}, []query.Assignment{query.Set(version, query.Excluded(version)), query.Set(payload, query.Excluded(payload))})
		require.NoError(t, buildErr)
		statement, buildErr = statement.WithUpdateWhere(query.LessThan(version, query.Excluded(version)))
		require.NoError(t, buildErr)
		statement, buildErr = statement.WithConflictWhere(query.GreaterThan(version, 0))
		require.NoError(t, buildErr)
		plan, planErr := rasql.NewStatementPlan(statement)
		require.NoError(t, planErr)
		_, execErr := rasql.ExecMutation(t.Context(), executor, plan)
		require.NoError(t, execErr)
	}
	run(1, "v1")
	var storedVersion int
	var storedPayload string
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT version, payload FROM "+tableName+" WHERE id = 1").Scan(&storedVersion, &storedPayload))
	require.Equal(t, 2, storedVersion)
	require.Equal(t, "v2", storedPayload)
	run(3, "v3")
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT version, payload FROM "+tableName+" WHERE id = 1").Scan(&storedVersion, &storedPayload))
	require.Equal(t, 3, storedVersion)
	require.Equal(t, "v3", storedPayload)
}

func TestLiveAtomicScope(t *testing.T) {
	engines := []struct {
		name    string
		open    func(*testing.T) *sql.DB
		dialect dialect.Dialect
	}{
		{name: "postgresql", open: dbtest.PostgreSQLDB, dialect: dialect.PostgreSQL()},
		{name: "mysql", open: dbtest.MySQLDB, dialect: dialect.MySQL()},
	}
	for _, engine := range engines {
		t.Run(engine.name, func(t *testing.T) {
			database := engine.open(t)
			db, err := rasql.New(database, engine.dialect)
			require.NoError(t, err)
			table := dbtest.UniqueName(t, "rasql_atomic_scope_records")
			quoted, err := engine.dialect.QuoteIdentifier(table)
			require.NoError(t, err)
			_, err = db.ExecRendered(t.Context(), stmt.New(sqltext.Text("CREATE TABLE "+quoted+" (value BIGINT)")))
			require.NoError(t, err)
			t.Cleanup(func() {
				_, _ = db.ExecRendered(context.Background(), stmt.New(sqltext.Text("DROP TABLE "+quoted)))
			})

			callbackErr := errors.New("nested live failure")
			err = db.Atomic(t.Context(), nil, func(ctx context.Context, outer rasql.DB) error {
				_, err := outer.ExecRendered(ctx, stmt.New(sqltext.Text("INSERT INTO "+quoted+" VALUES (1)")))
				require.NoError(t, err)
				err = outer.Atomic(ctx, nil, func(ctx context.Context, nested rasql.DB) error {
					_, err := nested.ExecRendered(ctx, stmt.New(sqltext.Text("INSERT INTO "+quoted+" VALUES (2)")))
					require.NoError(t, err)
					return callbackErr
				})
				require.ErrorIs(t, err, callbackErr)
				_, err = outer.ExecRendered(ctx, stmt.New(sqltext.Text("INSERT INTO "+quoted+" VALUES (3)")))
				return err
			})
			require.NoError(t, err)
			var count, total int64
			require.NoError(t, database.QueryRowContext(t.Context(), "SELECT COUNT(*), COALESCE(SUM(value), 0) FROM "+quoted).Scan(&count, &total))
			require.Equal(t, int64(2), count)
			require.Equal(t, int64(4), total)
		})
	}
}

type g5LiveRow struct{ ID, Value, Count int64 }

func TestLiveUpdateDefault(t *testing.T) {
	for _, tc := range []struct {
		name    string
		open    func(*testing.T) *sql.DB
		dialect dialect.Dialect
		profile string
		create  string
	}{
		{"postgresql", dbtest.PostgreSQLDB, dialect.PostgreSQL(), "postgresql-17", "CREATE TABLE %s (id BIGINT PRIMARY KEY, value BIGINT NOT NULL DEFAULT 41, count BIGINT NOT NULL)"},
		{"mysql", dbtest.MySQLDB, dialect.MySQL(), "mysql-8.4", "CREATE TABLE %s (id BIGINT PRIMARY KEY, value BIGINT NOT NULL DEFAULT 41, count BIGINT NOT NULL)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := tc.open(t)
			name := dbtest.UniqueName(t, "rasql_g5_default")
			quoted, err := tc.dialect.QuoteIdentifier(name)
			require.NoError(t, err)
			_, err = database.ExecContext(t.Context(), formatG5Create(tc.create, quoted))
			require.NoError(t, err)
			t.Cleanup(func() { _, _ = database.ExecContext(context.Background(), "DROP TABLE "+quoted) })
			_, err = database.ExecContext(t.Context(), "INSERT INTO "+quoted+" (id, value, count) VALUES (1, 9, 3)")
			require.NoError(t, err)
			db, err := rasql.New(database, tc.dialect)
			require.NoError(t, err)
			profile, err := rasql.DiscoverEngineProfile(t.Context(), db, tc.profile)
			require.NoError(t, err)
			executor, err := rasql.AsExecutor(db, profile)
			require.NoError(t, err)
			table := rasql.MustTableOf[g5LiveRow](schema.TableDef{Name: name, PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{
				{Name: "id", Type: schema.IntegerType{}}, {Name: "value", Type: schema.IntegerType{}, Default: "41"}, {Name: "count", Type: schema.IntegerType{}},
			}})
			id := query.TypedColumnOf[g5LiveRow, int64](table.Column("id"))
			value := query.TypedColumnOf[g5LiveRow, int64](table.Column("value"))
			count := query.TypedColumnOf[g5LiveRow, int64](table.Column("count"))
			plan, err := rasql.NewPatchPlan(table, query.EqualValue(id, int64(1)), rasql.DefaultField(value), rasql.SetField(count, int64(8)))
			require.NoError(t, err)
			outcome, err := rasql.ExecMutation(t.Context(), executor, plan)
			require.NoError(t, err)
			require.Equal(t, int64(1), outcome.Affected)
			var gotValue, gotCount int64
			require.NoError(t, database.QueryRowContext(t.Context(), "SELECT value, count FROM "+quoted+" WHERE id = 1").Scan(&gotValue, &gotCount))
			require.Equal(t, int64(41), gotValue)
			require.Equal(t, int64(8), gotCount)
		})
	}
}

func formatG5Create(format, name string) string {
	for i := 0; i+1 < len(format); i++ {
		if format[i] == '%' && format[i+1] == 's' {
			return format[:i] + name + format[i+2:]
		}
	}
	return format
}
