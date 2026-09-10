package conformance

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type scanValue struct {
	ID         int64
	ProjectID  int64
	AssigneeID rasql.Nullable[int64]
	Title      string
	Open       bool
}

type scanTaskRow struct {
	ID         int64
	ProjectID  int64
	AssigneeID sql.NullInt64
	Title      string
	Open       bool
}

type scanTaskDecoder struct{}

func (scanTaskDecoder) ResultSchema() rasql.ResultSchema {
	result, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "project_id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "assignee_id", Type: schema.IntegerType{}, Nullable: true},
		rasql.ResultColumn{Name: "title", Type: schema.TextType{}},
		rasql.ResultColumn{Name: "is_open", Type: schema.BooleanType{}},
	)
	if err != nil {
		panic(err)
	}
	return result
}

func (scanTaskDecoder) Presence() []rasql.Presence { return nil }

func (scanTaskDecoder) DecodeRow(source rasql.ScanSource, row *scanTaskRow) error {
	return source.Scan(&row.ID, &row.ProjectID, &row.AssigneeID, &row.Title, &row.Open)
}

func scanTypedTaskQuery(f typedFixture) (rasql.Query[scanTaskRow], error) {
	relation := mustSource(f.tasks, "t")
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", f.taskID.Expr(), schema.IntegerType{}, ""),
		rasql.Item("project_id", f.taskProject.Expr(), schema.IntegerType{}, ""),
		rasql.NullItem("assignee_id", f.taskAssignee.NullExpr(), schema.IntegerType{}, ""),
		rasql.Item("title", f.taskTitle.Expr(), schema.TextType{}, ""),
		rasql.Item("is_open", f.taskOpen.Expr(), schema.BooleanType{}, ""),
	}, scanTaskDecoder{})
	if err != nil {
		return rasql.Query[scanTaskRow]{}, err
	}
	query := rasql.Select(relation.Source(), projection).OrderBy(rasql.AscExpr(f.taskID.Expr()))
	return query.Limit(32)
}

func TestScanRowsLane(t *testing.T) {
	fixture, err := newTypedFixture()
	require.NoError(t, err)
	query, err := scanTypedTaskQuery(fixture)
	require.NoError(t, err)
	expected := scanExpectedRows()
	values := scanFixtureRows(expected)

	rasqlState := &recordingDriverState{cols: scanColumns(), rows: values}
	rasqlDB := openRecordingDB(rasqlState)
	t.Cleanup(func() { require.NoError(t, rasqlDB.Close()) })
	raw, err := rasql.New(rasqlDB, dialect.SQLite())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(raw, profile)
	require.NoError(t, err)
	sequence, err := rasql.Rows(t.Context(), executor, query)
	require.NoError(t, err)
	actualRasql := make([]scanValue, 0, len(expected))
	for row, rowErr := range sequence {
		require.NoError(t, rowErr)
		actualRasql = append(actualRasql, scanValue{ID: row.ID, ProjectID: row.ProjectID, AssigneeID: rasql.Nullable[int64]{Value: row.AssigneeID.Int64, Valid: row.AssigneeID.Valid}, Title: row.Title, Open: row.Open})
	}

	sqlState := &recordingDriverState{cols: scanColumns(), rows: values}
	sqlDB := openRecordingDB(sqlState)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	actualSQL, err := scanSQL(t.Context(), sqlDB, "SELECT id, project_id, assignee_id, title, is_open FROM tasks ORDER BY id LIMIT 32")
	require.NoError(t, err)
	require.Equal(t, expected, actualRasql)
	require.Equal(t, expected, actualSQL)
	require.Equal(t, actualRasql, actualSQL)
	require.Len(t, rasqlState.Calls(), 1)
	require.Len(t, sqlState.Calls(), 1)
	require.Equal(t, 1, rasqlState.closes)
	require.Equal(t, 1, sqlState.closes)
	require.NotEmpty(t, rasqlState.Calls()[0].sql)
	require.Equal(t, []any{int64(32)}, rasqlState.Calls()[0].args)
	require.NotEmpty(t, sqlState.Calls()[0].sql)
	require.Empty(t, sqlState.Calls()[0].args)
}

func scanSQL(ctx context.Context, database *sql.DB, query string) ([]scanValue, error) {
	rows, err := database.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	values := make([]scanValue, 0, 32)
	for rows.Next() {
		var value scanValue
		var assignee sql.NullInt64
		if err := rows.Scan(&value.ID, &value.ProjectID, &assignee, &value.Title, &value.Open); err != nil {
			return nil, err
		}
		value.AssigneeID = rasql.Nullable[int64]{Value: assignee.Int64, Valid: assignee.Valid}
		values = append(values, value)
	}
	return values, rows.Err()
}

func scanColumns() []string {
	return []string{"id", "project_id", "assignee_id", "title", "is_open"}
}

func scanExpectedRows() []scanValue {
	values := make([]scanValue, 32)
	for index := range values {
		id := int64(100 + index)
		values[index] = scanValue{ID: id, ProjectID: int64(10 + index%5), Title: fmt.Sprintf("task-%02d", index), Open: index%2 == 0}
		if index%3 != 0 {
			values[index].AssigneeID = rasql.Nullable[int64]{Value: int64(500 + index), Valid: true}
		}
	}
	return values
}

func scanFixtureRows(expected []scanValue) [][]driver.Value {
	rows := make([][]driver.Value, len(expected))
	for index, value := range expected {
		var assignee driver.Value
		if value.AssigneeID.Valid {
			assignee = value.AssigneeID.Value
		}
		rows[index] = []driver.Value{value.ID, value.ProjectID, assignee, value.Title, value.Open}
	}
	return rows
}

func scanDigest(values []scanValue) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = fmt.Fprintf(hash, "%d/%d/%t/%d/%s/%t;", value.ID, value.ProjectID, value.AssigneeID.Valid, value.AssigneeID.Value, value.Title, value.Open)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func scanTaskDigest(values []scanTaskRow) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = fmt.Fprintf(hash, "%d/%d/%t/%d/%s/%t;", value.ID, value.ProjectID, value.AssigneeID.Valid, value.AssigneeID.Int64, value.Title, value.Open)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

var scanBenchmarkSink string

func BenchmarkConformanceScanRowsRasql(b *testing.B) {
	fixture, err := newTypedFixture()
	if err != nil {
		b.Fatal(err)
	}
	query, err := scanTypedTaskQuery(fixture)
	if err != nil {
		b.Fatal(err)
	}
	state := &recordingDriverState{strict: true, cols: scanColumns()}
	database := openRecordingDB(state)
	b.Cleanup(func() {
		if err := database.Close(); err != nil {
			b.Error(err)
		}
	})
	raw, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		b.Fatal(err)
	}
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	if err != nil {
		b.Fatal(err)
	}
	executor, err := rasql.AsExecutor(raw, profile)
	if err != nil {
		b.Fatal(err)
	}
	expected := scanExpectedRows()
	rows := scanFixtureRows(expected)
	state.responses = []recordingResponse{{Kind: "query", Columns: scanColumns(), Rows: rows}}
	sequence, err := rasql.Rows(b.Context(), executor, query)
	if err != nil {
		b.Fatal(err)
	}
	actual := make([]scanTaskRow, 0, len(expected))
	for row, rowErr := range sequence {
		if rowErr != nil {
			b.Fatal(rowErr)
		}
		actual = append(actual, row)
	}
	if len(actual) != len(expected) {
		b.Fatalf("rasql semantic rows = %d, want %d", len(actual), len(expected))
	}
	if digest := scanTaskDigest(actual); digest != scanDigest(expected) {
		b.Fatalf("rasql semantic digest = %s, want %s", digest, scanDigest(expected))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		state.mu.Lock()
		state.responses = []recordingResponse{{Kind: "query", Columns: scanColumns(), Rows: rows}}
		state.mu.Unlock()
		sequence, err := rasql.Rows(b.Context(), executor, query)
		if err != nil {
			b.Fatal(err)
		}
		actual := make([]scanTaskRow, 0, len(expected))
		for row, rowErr := range sequence {
			if rowErr != nil {
				b.Fatal(rowErr)
			}
			actual = append(actual, row)
		}
		if len(actual) != len(expected) {
			b.Fatalf("rasql rows = %d, want %d", len(actual), len(expected))
		}
		scanBenchmarkSink = scanTaskDigest(actual)
		if scanBenchmarkSink != scanDigest(expected) {
			b.Fatalf("rasql digest = %s, want %s", scanBenchmarkSink, scanDigest(expected))
		}
	}
}

func BenchmarkConformanceScanRowsHandwritten(b *testing.B) {
	state := &recordingDriverState{strict: true, cols: scanColumns()}
	database := openRecordingDB(state)
	b.Cleanup(func() {
		if err := database.Close(); err != nil {
			b.Error(err)
		}
	})
	expected := scanExpectedRows()
	rows := scanFixtureRows(expected)
	state.responses = []recordingResponse{{Kind: "query", Columns: scanColumns(), Rows: rows}}
	values, err := scanSQL(b.Context(), database, "SELECT id, project_id, assignee_id, title, is_open FROM tasks ORDER BY id LIMIT 32")
	if err != nil {
		b.Fatal(err)
	}
	if len(values) != len(expected) {
		b.Fatalf("database/sql semantic rows = %d, want %d", len(values), len(expected))
	}
	if digest := scanDigest(values); digest != scanDigest(expected) {
		b.Fatalf("database/sql semantic digest = %s, want %s", digest, scanDigest(expected))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		state.mu.Lock()
		state.responses = []recordingResponse{{Kind: "query", Columns: scanColumns(), Rows: rows}}
		state.mu.Unlock()
		values, err := scanSQL(b.Context(), database, "SELECT id, project_id, assignee_id, title, is_open FROM tasks ORDER BY id LIMIT 32")
		if err != nil {
			b.Fatal(err)
		}
		if len(values) != len(expected) {
			b.Fatalf("database/sql rows = %d, want %d", len(values), len(expected))
		}
		scanBenchmarkSink = scanDigest(values)
		if scanBenchmarkSink != scanDigest(expected) {
			b.Fatalf("database/sql digest = %s, want %s", scanBenchmarkSink, scanDigest(expected))
		}
	}
}

func TestScanRowsLanePropagatesNextErrors(t *testing.T) {
	nextErr := errors.New("next failed")
	for _, name := range []string{"rasql", "database/sql"} {
		t.Run(name, func(t *testing.T) {
			state := &recordingDriverState{strict: true, responses: []recordingResponse{{Kind: "query", Columns: scanColumns(), NextErr: nextErr}}}
			database := openRecordingDB(state)
			t.Cleanup(func() { require.NoError(t, database.Close()) })
			if name == "database/sql" {
				_, err := scanSQL(t.Context(), database, "SELECT tasks")
				require.ErrorIs(t, err, nextErr)
			} else {
				raw, err := rasql.New(database, dialect.SQLite())
				require.NoError(t, err)
				profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
				require.NoError(t, err)
				executor, err := rasql.AsExecutor(raw, profile)
				require.NoError(t, err)
				fixture, err := newTypedFixture()
				require.NoError(t, err)
				query, err := scanTypedTaskQuery(fixture)
				require.NoError(t, err)
				sequence, err := rasql.Rows(t.Context(), executor, query)
				require.NoError(t, err)
				for _, rowErr := range sequence {
					require.ErrorIs(t, rowErr, nextErr)
				}
			}
			require.Equal(t, 1, state.closes)
		})
	}
}
