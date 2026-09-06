package diff_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/stretchr/testify/require"
)

func TestPlanResolutionIsCopiedAndPublicationRequiresDecisions(t *testing.T) {
	plan := diff.Plan{
		Dialect: "sqlite",
		Operations: []diff.ProposedOperation{{
			ID: "add_column_sqlite_members_email", Table: "members", Column: "email",
			Kind: diff.OperationAddColumn, Summary: "add column members.email",
			Forward: []diff.PlannedStatement{{Source: "001_add.sql", SQL: "ALTER TABLE members ADD COLUMN email text;", ReverseSQL: "ALTER TABLE members DROP COLUMN email;"}},
		}},
		Decisions: []diff.RequiredDecision{{ID: "backfill_sqlite_members_email", Table: "members", Column: "email", Kind: diff.DecisionBackfill}},
	}
	require.False(t, plan.Executable())
	directory := filepath.Join(t.TempDir(), "migration")
	require.ErrorContains(t, diff.WriteMigration(directory, plan), "backfill_sqlite_members_email")
	_, err := os.Stat(directory)
	require.ErrorIs(t, err, os.ErrNotExist)

	resolved, err := plan.Resolve(diff.Resolution{DecisionID: "backfill_sqlite_members_email", BackfillSQL: "email_source"})
	require.NoError(t, err)
	require.True(t, resolved.Executable())
	require.Len(t, resolved.Statements, 1)
	require.Empty(t, plan.Statements)
	resolved.Operations[0].Forward[0].SQL = "changed"
	require.NotEqual(t, resolved.Operations[0].Forward[0].SQL, plan.Operations[0].Forward[0].SQL)
}

func TestPlanResolutionRejectsUnknownDuplicateAndEmptyAnswers(t *testing.T) {
	plan := diff.Plan{Dialect: "sqlite", Decisions: []diff.RequiredDecision{{ID: "rename_sqlite_members_name", Table: "members", Column: "display_name", Kind: diff.DecisionRename}}}
	_, err := plan.Resolve(diff.Resolution{DecisionID: "unknown", RenameFrom: "name"})
	require.ErrorContains(t, err, "unknown decision")
	_, err = plan.Resolve(diff.Resolution{DecisionID: "rename_sqlite_members_name", RenameFrom: ""})
	require.ErrorContains(t, err, "empty")
}
