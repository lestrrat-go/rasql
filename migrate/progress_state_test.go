package migrate

import (
	"testing"

	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
)

func TestValidateProgressAcceptsOnlyDurableCheckpointRelations(t *testing.T) {
	migration := preparedMigration{
		id: "001_users",
		statements: []Statement{
			{Source: "001.sql", SQL: sqltext.Text("CREATE TABLE users (id INT)")},
			{Source: "002.sql", SQL: sqltext.Text("CREATE INDEX users_id ON users (id)")},
		},
		down:     []Statement{{Source: "002.down.sql", SQL: sqltext.Text("DROP INDEX users_id")}},
		checksum: "sum",
	}
	for _, test := range []struct {
		name      string
		entry     progressEntry
		wantError string
	}{
		{name: "uncertain", entry: progressEntry{id: migration.id, checksum: "sum", direction: DirectionUp, sourceIndex: 0, source: "001.sql", nextIndex: 0}},
		{name: "known checkpoint", entry: progressEntry{id: migration.id, checksum: "sum", direction: DirectionUp, sourceIndex: 0, source: "001.sql", nextIndex: 1}},
		{name: "terminal", entry: progressEntry{id: migration.id, checksum: "sum", direction: DirectionUp, sourceIndex: 1, source: "002.sql", nextIndex: 2}},
		{name: "skips source", entry: progressEntry{id: migration.id, checksum: "sum", direction: DirectionUp, sourceIndex: 0, source: "001.sql", nextIndex: 2}, wantError: "index is invalid"},
		{name: "wrong source", entry: progressEntry{id: migration.id, checksum: "sum", direction: DirectionUp, sourceIndex: 1, source: "001.sql", nextIndex: 1}, wantError: "source does not match"},
		{name: "wrong direction", entry: progressEntry{id: migration.id, checksum: "sum", direction: "sideways", sourceIndex: 0, source: "001.sql", nextIndex: 0}, wantError: "direction is invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := (Runner{}).validateProgress(&test.entry, []preparedMigration{migration})
			if test.wantError == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, test.wantError)
		})
	}
}

func TestProgressStatementsRejectsUnknownDirection(t *testing.T) {
	_, err := progressStatements(preparedMigration{}, "sideways")
	require.ErrorContains(t, err, "invalid progress direction")
}

func TestPrioritizeProgressPreservesRemainingPlanSuffix(t *testing.T) {
	migrations := []preparedMigration{{id: "001"}, {id: "002"}, {id: "003"}}
	selected := []preparedMigration{migrations[0], migrations[1], migrations[2]}
	ordered := prioritizeProgress(selected, migrations, "002")
	require.Equal(t, []string{"002", "001", "003"}, []string{ordered[0].id, ordered[1].id, ordered[2].id})
}
