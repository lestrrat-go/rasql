package postgresql

import (
	"os"
	"testing"

	pgquery "github.com/lestrrat-go/rasql-pg/query"
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
	require.Len(t, table.identities, 1)
	require.Len(t, table.foreignKeys, 2)
	require.Equal(t, identityAlways, table.identities[identifierKey(pgquery.Identifier{Name: "id"})])
	assignee := table.foreignKeys[foreignKeyKey{table: "5:tasks", constraint: identifierKey(pgquery.Identifier{Name: "tasks_assignee_id_fkey"}), inline: false}]
	project := table.foreignKeys[foreignKeyKey{table: "5:tasks", constraint: identifierKey(pgquery.Identifier{Name: "tasks_project_id_fkey"}), inline: false}]
	require.Equal(t, referenceNoAction, assignee.onDelete)
	require.Equal(t, referenceNoAction, assignee.onUpdate)
	require.Equal(t, referenceCascade, project.onDelete)
	require.Equal(t, referenceNoAction, project.onUpdate)
	assigneeFound, projectFound := false, false
	for _, constraint := range table.statement.Constraints {
		if constraint.Name == nil || constraint.References == nil {
			continue
		}
		switch constraint.Name.Name {
		case "tasks_assignee_id_fkey":
			assigneeFound = true
			require.Equal(t, "members", constraint.References.Table.String())
			require.Equal(t, []pgquery.Identifier{{Name: "id", Quoted: true}}, constraint.References.Columns)
		case "tasks_project_id_fkey":
			projectFound = true
			require.Equal(t, "projects", constraint.References.Table.String())
			require.Equal(t, []pgquery.Identifier{{Name: "id", Quoted: true}}, constraint.References.Columns)
		}
	}
	require.True(t, assigneeFound)
	require.True(t, projectFound)
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
