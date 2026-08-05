package rasql_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/row"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestSQLiteTypedSelectRoundTripsBooleanAndTime(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)

	client, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	type event struct {
		ID        int64     `rasql:"id"`
		Active    bool      `rasql:"active"`
		CreatedAt time.Time `rasql:"created_at"`
	}
	events, err := rasql.NewTable[event](schema.Table{
		Name: "events",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "active", Type: schema.TypeBoolean},
			{Name: "created_at", Type: schema.TypeTime},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	eventID, err := events.Column("id")
	require.NoError(t, err)
	require.NoError(t, rasql.Create(t.Context(), client, events))

	expected := event{
		ID:        42,
		Active:    true,
		CreatedAt: time.Date(2026, time.August, 1, 12, 30, 45, 123456789, time.UTC),
	}
	_, err = rasql.Insert(t.Context(), client, events, expected)
	require.NoError(t, err)

	actual, err := rasql.SelectFrom(client, events).WhereEqual(eventID, expected.ID).One(t.Context())
	require.NoError(t, err)
	require.Equal(t, expected, actual)

	_, err = rasql.SelectFrom(client, events).WhereEqual(eventID, expected.ID+1).One(t.Context())
	require.ErrorIs(t, err, rasql.ErrNoRows)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

// generatedEventRow has the shape rasqlgen emits: no tags, and one method per
// direction stating the column-to-field mapping.
type generatedEventRow struct {
	ID        int64
	Active    bool
	CreatedAt time.Time
	Note      *string
}

func (r *generatedEventRow) DecodeRow(src row.Row) error {
	if err := row.Assign(src, "id", &r.ID); err != nil {
		return err
	}
	if err := row.Assign(src, "active", &r.Active); err != nil {
		return err
	}
	if err := row.Assign(src, "created_at", &r.CreatedAt); err != nil {
		return err
	}
	return row.Assign(src, "note", &r.Note)
}

func (r generatedEventRow) ColumnValue(name string) (any, bool) {
	switch name {
	case "id":
		return r.ID, true
	case "active":
		return r.Active, true
	case "created_at":
		return r.CreatedAt, true
	case "note":
		return r.Note, true
	}
	return nil, false
}

func TestSQLiteGeneratedRowMethodsRoundTrip(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)

	client, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	events, err := rasql.NewTable[generatedEventRow](schema.Table{
		Name: "events",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "active", Type: schema.TypeBoolean},
			{Name: "created_at", Type: schema.TypeTime},
			{Name: "note", Type: schema.TypeText, Nullable: true},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	eventID, err := events.Column("id")
	require.NoError(t, err)
	require.NoError(t, rasql.Create(t.Context(), client, events))

	note := "first"
	expected := generatedEventRow{
		ID:        42,
		Active:    true,
		CreatedAt: time.Date(2026, time.August, 1, 12, 30, 45, 123456789, time.UTC),
		Note:      &note,
	}
	// Insert reaches ColumnValue, and One reaches DecodeRow.
	_, err = rasql.Insert(t.Context(), client, events, expected)
	require.NoError(t, err)

	actual, err := rasql.SelectFrom(client, events).WhereEqual(eventID, expected.ID).One(t.Context())
	require.NoError(t, err)
	require.Equal(t, expected, actual)

	// A NULL column decodes back into a nil pointer.
	updated := expected
	updated.Note = nil
	_, err = rasql.Update(t.Context(), client, events, updated)
	require.NoError(t, err)

	actual, err = rasql.SelectFrom(client, events).WhereEqual(eventID, expected.ID).One(t.Context())
	require.NoError(t, err)
	require.Nil(t, actual.Note)
}
