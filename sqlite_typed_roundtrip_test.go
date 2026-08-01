package rasql_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
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
}
