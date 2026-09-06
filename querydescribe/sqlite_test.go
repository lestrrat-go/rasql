package querydescribe_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/querydescribe"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestSQLiteDescribesAggregateResult(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(context.Background(), `CREATE TABLE users(id INTEGER PRIMARY KEY); CREATE TABLE profiles(user_id INTEGER, nickname TEXT); INSERT INTO users VALUES (1),(2); INSERT INTO profiles VALUES (1,'one')`)
	require.NoError(t, err)
	d := querydescribe.NewSQLite(db)
	got, err := d.Describe(context.Background(), querydescribe.Request{Name: "UserReport", SQL: `SELECT u.id AS user_id, p.nickname AS nickname, count(p.user_id) AS profile_count FROM users AS u LEFT JOIN profiles AS p ON p.user_id = u.id GROUP BY u.id, p.nickname ORDER BY u.id`, Cardinality: querydescribe.Many})
	require.NoError(t, err)
	require.Equal(t, []string{"user_id", "nickname", "profile_count"}, []string{got.Columns[0].Name, got.Columns[1].Name, got.Columns[2].Name})
	require.Equal(t, "int64", got.Columns[0].Binding.Type)
	require.Equal(t, "string", got.Columns[1].Binding.Type)
	require.Equal(t, "int64", got.Columns[2].Binding.Type)
	require.False(t, got.Columns[2].Nullable)
}

func TestSQLiteDescriberRejectsSecondStatement(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	d := querydescribe.NewSQLite(db)
	_, err = d.Describe(context.Background(), querydescribe.Request{Name: "x", SQL: "SELECT 1; SELECT 2"})
	require.ErrorIs(t, err, querydescribe.ErrInvalidRequest)
}

func TestSQLiteDescriberPreservesCanceledContext(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = querydescribe.NewSQLite(db).Describe(ctx, querydescribe.Request{Name: "canceled", SQL: "SELECT 1 AS id"})
	require.ErrorIs(t, err, context.Canceled)
}

func TestSQLiteDescriberChecksObservedExpectedType(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(t.Context(), "CREATE TABLE typed_source(id INTEGER)")
	require.NoError(t, err)
	want := &querydescribe.Description{Columns: []querydescribe.Column{{Name: "id", Binding: schema.GoBinding{Type: "string"}}}}
	_, err = querydescribe.NewSQLite(db).Describe(t.Context(), querydescribe.Request{Name: "typed", SQL: "SELECT id FROM typed_source", Expected: want})
	require.ErrorIs(t, err, querydescribe.ErrExpected)
}

func TestSQLiteDescriberRejectsNormalizedFieldCollision(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(t.Context(), "CREATE TABLE collision(user_id INTEGER, userID INTEGER)")
	require.NoError(t, err)
	_, err = querydescribe.NewSQLite(db).Describe(t.Context(), querydescribe.Request{Name: "collision", SQL: "SELECT user_id, userID FROM collision"})
	require.ErrorIs(t, err, querydescribe.ErrIncomplete)
}

func TestSQLiteCountParserAcceptsQuotedReferencesAndRejectsCompound(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(t.Context(), "CREATE TABLE counted(id INTEGER)")
	require.NoError(t, err)
	accepted := []string{
		`SELECT count("id") AS "total" FROM counted`,
		"SELECT /* count(*) */ count(`id`) AS `total` FROM counted",
	}
	for _, sqlText := range accepted {
		got, describeErr := querydescribe.NewSQLite(db).Describe(t.Context(), querydescribe.Request{Name: "count", SQL: sqlText})
		require.NoError(t, describeErr)
		require.Equal(t, "int64", got.Columns[0].Binding.Type)
	}
	_, err = querydescribe.NewSQLite(db).Describe(t.Context(), querydescribe.Request{Name: "compound", SQL: "SELECT count(*) AS total FROM counted UNION SELECT count(*) AS total FROM counted"})
	require.ErrorIs(t, err, querydescribe.ErrIncomplete)
}

func TestSQLiteExpectedMismatchFields(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(t.Context(), "CREATE TABLE expected_one(id INTEGER, name TEXT)")
	require.NoError(t, err)
	base, err := querydescribe.NewSQLite(db).Describe(t.Context(), querydescribe.Request{Name: "expected", SQL: "SELECT id AS id, name AS name FROM expected_one"})
	require.NoError(t, err)
	cases := []struct {
		name, field string
		mutate      func(*querydescribe.Description)
	}{
		{"count", "column count", func(d *querydescribe.Description) { d.Columns = d.Columns[:1] }},
		{"order", "name", func(d *querydescribe.Description) { d.Columns[0], d.Columns[1] = d.Columns[1], d.Columns[0] }},
		{"name", "name", func(d *querydescribe.Description) { d.Columns[0].Name = "other" }},
		{"type", "binding", func(d *querydescribe.Description) { d.Columns[0].Binding.Type = "string" }},
		{"nullable type", "binding", func(d *querydescribe.Description) { d.Columns[0].Binding.NullableType = "string" }},
		{"imports", "binding", func(d *querydescribe.Description) { d.Columns[0].Binding.Imports = []schema.GoImport{{Path: "fmt"}} }},
		{"nullability", "nullability", func(d *querydescribe.Description) { d.Columns[0].Nullable = !d.Columns[0].Nullable }},
		{"cardinality", "cardinality", func(d *querydescribe.Description) { d.Cardinality = querydescribe.ExactlyOne }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := base
			want.Columns = append([]querydescribe.Column(nil), base.Columns...)
			for i := range want.Columns {
				want.Columns[i].Binding.Imports = append([]schema.GoImport(nil), base.Columns[i].Binding.Imports...)
			}
			tc.mutate(&want)
			_, mismatch := querydescribe.NewSQLite(db).Describe(t.Context(), querydescribe.Request{Name: "expected", SQL: "SELECT id AS id, name AS name FROM expected_one", Expected: &want})
			require.ErrorIs(t, mismatch, querydescribe.ErrExpected)
			require.True(t, strings.Contains(mismatch.Error(), tc.field), mismatch.Error())
		})
	}
}
