package postgresql

import (
	"os"
	"testing"

	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
)

func TestTaskboardIdentityAndForeignKeyFacts(t *testing.T) {
	data, err := os.ReadFile("../../../sample/taskboard/db/migrations/001_initial/003_create_tasks.up.sql")
	require.NoError(t, err)
	snapshot, err := New().Parse([]diff.Source{{Path: "tasks.sql", SQL: sqltext.Text(data)}})
	require.NoError(t, err)
	table := snapshot.(*schemaSnapshot).tables["5:tasks"]
	require.Equal(t, identityAlways, table.identities[identifierKey(`"id"`)])
	require.Equal(t, referenceNoAction, table.foreignKeys[foreignKeyKey{table: "5:tasks", constraint: identifierKey(`"tasks_assignee_id_fkey"`)}].onDelete)
	require.Equal(t, referenceCascade, table.foreignKeys[foreignKeyKey{table: "5:tasks", constraint: identifierKey(`"tasks_project_id_fkey"`)}].onDelete)
}

func TestCreateTableRestoresIdentityFact(t *testing.T) {
	analyzer := New()
	base, err := analyzer.Parse([]diff.Source{{Path: "base.sql", SQL: "CREATE TABLE base (id integer);"}})
	require.NoError(t, err)
	target, err := analyzer.Parse([]diff.Source{{Path: "target.sql", SQL: "CREATE TABLE base (id integer); CREATE TABLE generated (id integer GENERATED ALWAYS AS IDENTITY);"}})
	require.NoError(t, err)
	plan, err := analyzer.Diff(base, target)
	require.NoError(t, err)
	require.Contains(t, plan.Statements[0].SQL, "GENERATED ALWAYS AS IDENTITY")
}
