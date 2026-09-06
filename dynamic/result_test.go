package dynamic_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/dynamic"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestQueryResultExposesImmutableOrderedMetadata(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	database.SetMaxOpenConns(1)
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	var phases []rasql.Phase
	db, err = db.WithInvocationObservers(rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.InvocationObserverFunc(func(ctx context.Context, _ rasql.Operation) (context.Context, rasql.CompletionObserver) {
		return ctx, rasql.CompletionObserverFunc(func(_ context.Context, completion rasql.Completion) error {
			phases = append(phases, completion.Phase)
			return nil
		})
	}))
	require.NoError(t, err)
	_, err = db.ExecRendered(t.Context(), stmtText("CREATE TABLE users (id INTEGER, name TEXT, nickname TEXT)"))
	require.NoError(t, err)
	_, err = db.ExecRendered(t.Context(), stmtText("INSERT INTO users VALUES (1, 'Ada', NULL)"))
	require.NoError(t, err)
	users := resultUsersTable(t)
	statement, err := query.NewSelect(users, users.Column("name").As("runtime_alias"), users.Column("nickname").As("nullable_alias"))
	require.NoError(t, err)
	result, err := dynamic.QueryResult(t.Context(), db, statement)
	require.NoError(t, err)
	header, err := result.Header()
	require.NoError(t, err)
	require.Equal(t, 2, header.Len())
	names := header.Names()
	require.Equal(t, []string{"runtime_alias", "nullable_alias"}, names)
	index, ok := header.Index("runtime_alias")
	require.True(t, ok)
	require.Equal(t, 0, index)
	_, ok = header.Index("missing")
	require.False(t, ok)
	names[0] = "changed"
	namesAgain := header.Names()
	require.Equal(t, []string{"runtime_alias", "nullable_alias"}, namesAgain)
	rows := make([]dynamic.Row, 0)
	for row, err := range result.Rows() {
		require.NoError(t, err)
		rows = append(rows, row)
	}
	require.Len(t, rows, 1)
	value, ok := rows[0].Value(0)
	require.True(t, ok)
	require.Equal(t, "Ada", value)
	value, ok = rows[0].Value(1)
	require.True(t, ok)
	require.Nil(t, value)
	_, ok = rows[0].Value(2)
	require.False(t, ok)
	_, ok = rows[0].Value(-1)
	require.False(t, ok)
	values := rows[0].Values()
	values[0] = "changed"
	valuesAgain := rows[0].Values()
	require.Equal(t, "Ada", valuesAgain[0])
	name, err := dynamic.Get[string](rows[0], "runtime_alias")
	require.NoError(t, err)
	require.Equal(t, "Ada", name)
	var nickname *string
	require.NoError(t, dynamic.Assign(rows[0], "nullable_alias", &nickname))
	require.Nil(t, nickname)
	require.NoError(t, result.Close())
	require.NoError(t, result.Close())
	require.Contains(t, phases, rasql.ExecutionPhase)
	require.Contains(t, phases, rasql.ConsumptionPhase)
}

func TestQueryResultEmptyRowsRetainsHeaderAndExecutesOnce(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	users := resultUsersTable(t)
	statement, err := query.NewSelect(users, users.Column("name").As("runtime_alias"), users.Column("nickname").As("nullable_alias"))
	require.NoError(t, err)
	mock.ExpectQuery(`SELECT "users"\."name" AS "runtime_alias", "users"\."nickname" AS "nullable_alias" FROM "users"`).WillReturnRows(sqlmock.NewRows([]string{"runtime_alias", "nullable_alias"}))
	result, err := dynamic.QueryResult(t.Context(), db, statement)
	require.NoError(t, err)
	header, err := result.Header()
	require.NoError(t, err)
	require.Equal(t, []string{"runtime_alias", "nullable_alias"}, header.Names())
	count := 0
	for _, err := range result.Rows() {
		count++
		require.NoError(t, err)
	}
	require.Zero(t, count)
	retained, err := result.Header()
	require.NoError(t, err)
	require.Equal(t, header.Names(), retained.Names())
	require.NoError(t, result.Close())
	mock.ExpectQuery(`SELECT .*`).WillReturnRows(sqlmock.NewRows([]string{"runtime_alias", "nullable_alias"}).AddRow("Ada", nil))
	second, err := dynamic.QueryResult(t.Context(), db, statement)
	require.NoError(t, err)
	count = 0
	for row, err := range second.Rows() {
		require.NoError(t, err)
		count++
		_, ok := row.Value(0)
		require.True(t, ok)
	}
	require.Equal(t, 1, count)
	retained, err = second.Header()
	require.NoError(t, err)
	require.Equal(t, header.Names(), retained.Names())
}

func TestResultCloseBeforeExecutionAndScanResultCompatibility(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	users := resultUsersTable(t)
	statement, err := query.NewSelect(users, users.Column("name"))
	require.NoError(t, err)
	result, err := dynamic.QueryResult(t.Context(), db, statement)
	require.NoError(t, err)
	require.NoError(t, result.Close())
	header, err := result.Header()
	require.NoError(t, err)
	require.Zero(t, header.Len())
	for range result.Rows() {
		t.Fatal("closed result yielded a row")
	}

	mock.ExpectQuery("SELECT name").WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow([]byte("Ada")).AddRow([]byte("Bob")))
	rows, err := database.QueryContext(t.Context(), "SELECT name")
	require.NoError(t, err)
	scanned := dynamic.ScanResult(rows)
	rowCount := 0
	for row, err := range scanned.Rows() {
		require.NoError(t, err)
		value, ok := row.Value(0)
		require.True(t, ok)
		bytes, ok := value.([]byte)
		require.True(t, ok)
		if rowCount == 0 {
			bytes[0] = 'X'
			fresh, _ := row.Value(0)
			require.Equal(t, []byte("Ada"), fresh)
		} else {
			require.Equal(t, []byte("Bob"), bytes)
		}
		rowCount++
	}
	require.Equal(t, 2, rowCount)
}

func TestResultRejectsDuplicateAndEmptyHeaders(t *testing.T) {
	for _, names := range [][]string{{"same", "same"}, {"", "value"}} {
		t.Run(names[0], func(t *testing.T) {
			database, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() {
				mock.ExpectClose()
				require.NoError(t, database.Close())
				require.NoError(t, mock.ExpectationsWereMet())
			})
			db, err := rasql.New(database, dialect.SQLite())
			require.NoError(t, err)
			mock.ExpectQuery("SELECT value").WillReturnRows(sqlmock.NewRows(names))
			rows, err := db.QueryRendered(t.Context(), stmtText("SELECT value"))
			require.NoError(t, err)
			result := dynamic.ScanResult(rows)
			_, err = result.Header()
			require.Error(t, err)
			if names[0] == "" {
				require.ErrorContains(t, err, "row: column name at index 0 is empty")
			} else {
				require.ErrorContains(t, err, "row: duplicate column name \"same\"")
			}
			seen := false
			for _, err := range result.Rows() {
				seen = true
				require.Error(t, err)
			}
			require.True(t, seen)
		})
	}
}

func TestBuilderResultMethodsExposeMetadata(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	users := resultUsersTable(t)
	mock.ExpectQuery("SELECT .*").WillReturnRows(sqlmock.NewRows([]string{"name"}))
	result, err := dynamic.SelectFrom(users).Select("name").QueryResult(t.Context(), db)
	require.NoError(t, err)
	header, err := result.Header()
	require.NoError(t, err)
	require.Equal(t, []string{"name"}, header.Names())
	require.NoError(t, result.Close())

	deleteBuilder := dynamic.DeleteFrom(users).AllowAll().Returning(users.Column("name"))
	mock.ExpectQuery(`DELETE FROM .* RETURNING .*`).WillReturnRows(sqlmock.NewRows([]string{"name"}))
	result, err = deleteBuilder.QueryResult(t.Context(), db)
	require.NoError(t, err)
	header, err = result.Header()
	require.NoError(t, err)
	require.Equal(t, []string{"name"}, header.Names())
	require.NoError(t, result.Close())
	mock.ExpectQuery(`DELETE FROM .* RETURNING .*`).WillReturnRows(sqlmock.NewRows([]string{"name"}))
	sequence, err := deleteBuilder.Query(t.Context(), db)
	require.NoError(t, err)
	for _, err := range sequence {
		require.NoError(t, err)
	}

	deleteStatement, err := query.NewDelete(users)
	require.NoError(t, err)
	deleteStatement, err = deleteStatement.AllowAll()
	require.NoError(t, err)
	deleteStatement, err = deleteStatement.WithReturning(users.Column("name"))
	require.NoError(t, err)
	mock.ExpectQuery("DELETE FROM .*").WillReturnRows(sqlmock.NewRows([]string{"name"}))
	result, err = dynamic.QueryWriteResult(t.Context(), db, deleteStatement)
	require.NoError(t, err)
	header, err = result.Header()
	require.NoError(t, err)
	require.Equal(t, []string{"name"}, header.Names())
	require.NoError(t, result.Close())
}

func resultUsersTable(t *testing.T) query.TableRef {
	t.Helper()
	users, err := query.NewTableRef(schema.TableDef{Name: "users", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "name", Type: schema.TextType{}},
		{Name: "nickname", Type: schema.TextType{}},
	}})
	require.NoError(t, err)
	return users
}

func stmtText(value string) stmt.Statement { return stmt.New(sqltext.Text(value)) }
