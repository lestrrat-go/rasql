package diff_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/stretchr/testify/require"
)

func TestPlanResolutionIsCopiedAndPublicationRequiresDecisions(t *testing.T) {
	carrier := []diff.PlannedStatement{{Source: "001_add.sql", SQL: "ALTER TABLE members ADD COLUMN email text;", ReverseSQL: "ALTER TABLE members DROP COLUMN email;"}}
	operation := diff.ProposedOperation{
		ID: "add_column_sqlite_members_email", Table: "members", Column: "email",
		Kind: diff.OperationAddColumn, Summary: "add column members.email",
	}
	decision := diff.RequiredDecision{ID: "backfill_sqlite_members_email", Table: "members", Column: "email", Kind: diff.DecisionBackfill}
	plan, err := diff.NewPlan("sqlite", []diff.ProposedOperation{operation}, []diff.RequiredDecision{decision}, func(_ map[string]diff.Resolution) (diff.LoweringResult, error) {
		operation.Forward = carrier
		operation.Reverse = []diff.PlannedStatement{{Source: "001_add.sql", SQL: carrier[0].ReverseSQL, ReverseSQL: carrier[0].SQL}}
		return diff.LoweringResult{Operations: []diff.ProposedOperation{operation}, Statements: carrier}, nil
	})
	require.NoError(t, err)
	require.False(t, plan.Executable())
	directory := filepath.Join(t.TempDir(), "migration")
	require.ErrorContains(t, diff.WriteMigration(directory, plan), "backfill_sqlite_members_email")
	_, err = os.Stat(directory)
	require.ErrorIs(t, err, os.ErrNotExist)

	resolved, err := plan.Resolve(diff.Resolution{DecisionID: "backfill_sqlite_members_email", BackfillSQL: "SELECT 1;"})
	require.NoError(t, err)
	require.True(t, resolved.Executable())
	require.Len(t, resolved.Statements, 1)
	require.Empty(t, plan.Statements)
	require.Equal(t, operation.ID, plan.Operations[0].ID)
	resolved.Operations[0].Forward[0].SQL = "changed"
	require.Empty(t, plan.Operations[0].Forward)
	require.Equal(t, carrier[0].SQL, "ALTER TABLE members ADD COLUMN email text;")
}

func TestNewPlanRejectsMalformedLowering(t *testing.T) {
	operation := diff.ProposedOperation{ID: "add_column_sqlite_members_email", Table: "members", Kind: diff.OperationAddColumn}
	decision := diff.RequiredDecision{ID: "backfill_sqlite_members_email", Table: "members", Column: "email", Kind: diff.DecisionBackfill}
	plan, err := diff.NewPlan("sqlite", []diff.ProposedOperation{operation}, []diff.RequiredDecision{decision}, func(map[string]diff.Resolution) (diff.LoweringResult, error) {
		return diff.LoweringResult{Operations: []diff.ProposedOperation{operation, operation}}, nil
	})
	require.NoError(t, err)
	_, err = plan.Resolve(diff.Resolution{DecisionID: decision.ID, BackfillSQL: "SELECT 1;"})
	require.ErrorContains(t, err, "duplicate operation ID")

	_, err = diff.NewPlan("sqlite", []diff.ProposedOperation{operation}, []diff.RequiredDecision{decision}, nil)
	require.NoError(t, err)
	_, err = diff.NewPlan("sqlite", []diff.ProposedOperation{operation, operation}, nil, nil)
	require.ErrorContains(t, err, "duplicate operation ID")
}

func TestLoweringResultIrreversibleReasonCopiesAcrossConstructionAndResolve(t *testing.T) {
	operation := diff.ProposedOperation{ID: "create_table_sqlite_members", Table: "members", Kind: diff.OperationCreateTable, Forward: []diff.PlannedStatement{{Source: "create.sql", SQL: "CREATE TABLE members (id integer);"}}}
	plan, err := diff.NewPlan("sqlite", []diff.ProposedOperation{operation}, nil, func(map[string]diff.Resolution) (diff.LoweringResult, error) {
		return diff.LoweringResult{Operations: []diff.ProposedOperation{operation}, Statements: operation.Forward, IrreversibleReason: "custom lowering reason"}, nil
	})
	require.NoError(t, err)
	require.Equal(t, "custom lowering reason", plan.IrreversibleReason)

	decision := diff.RequiredDecision{ID: "backfill_sqlite_members_email", Table: "members", Column: "email", Kind: diff.DecisionBackfill}
	resolvedPlan, err := diff.NewPlan("sqlite", []diff.ProposedOperation{operation}, []diff.RequiredDecision{decision}, func(map[string]diff.Resolution) (diff.LoweringResult, error) {
		return diff.LoweringResult{Operations: []diff.ProposedOperation{operation}, Statements: operation.Forward, IrreversibleReason: "resolved lowering reason"}, nil
	})
	require.NoError(t, err)
	first, err := resolvedPlan.Resolve(diff.Resolution{DecisionID: decision.ID, BackfillSQL: "SELECT 1;"})
	require.NoError(t, err)
	second, err := resolvedPlan.Resolve(diff.Resolution{DecisionID: decision.ID, BackfillSQL: "SELECT 2;"})
	require.NoError(t, err)
	require.Equal(t, "resolved lowering reason", first.IrreversibleReason)
	require.Equal(t, first.IrreversibleReason, second.IrreversibleReason)
	require.Empty(t, resolvedPlan.IrreversibleReason)
}

func TestPlanResolutionRejectsUnknownDuplicateAndEmptyAnswers(t *testing.T) {
	plan := diff.Plan{Dialect: "sqlite", Decisions: []diff.RequiredDecision{{ID: "rename_sqlite_members_name", Table: "members", Column: "display_name", Kind: diff.DecisionRename}}}
	_, err := plan.Resolve(diff.Resolution{DecisionID: "unknown", RenameFrom: "name"})
	require.ErrorContains(t, err, "unknown decision")
	_, err = plan.Resolve(diff.Resolution{DecisionID: "rename_sqlite_members_name", RenameFrom: ""})
	require.ErrorContains(t, err, "empty")
}

func TestBackfillResolutionChangesLoweredSQL(t *testing.T) {
	plan := diff.Plan{
		Dialect: "sqlite",
		Operations: []diff.ProposedOperation{{
			ID: "add_column_sqlite_members_email", Table: "members", Column: "email", Kind: diff.OperationAddColumn,
			Forward: []diff.PlannedStatement{{Source: "001_add.sql", SQL: "ALTER TABLE members ADD COLUMN email text NOT NULL;", ReverseSQL: "ALTER TABLE members DROP COLUMN email;"}},
		}},
		Decisions: []diff.RequiredDecision{{ID: "backfill_sqlite_members_email", Table: "members", Column: "email", Kind: diff.DecisionBackfill}},
	}
	first, err := plan.Resolve(diff.Resolution{DecisionID: "backfill_sqlite_members_email", BackfillSQL: "SELECT 'first';"})
	require.NoError(t, err)
	second, err := plan.Resolve(diff.Resolution{DecisionID: "backfill_sqlite_members_email", BackfillSQL: "SELECT 'second';"})
	require.NoError(t, err)
	require.Contains(t, first.Statements[0].SQL, "SELECT 'first';")
	require.Contains(t, second.Statements[0].SQL, "SELECT 'second';")
	require.NotEqual(t, first.Statements[0].SQL, second.Statements[0].SQL)
}

func TestRenameResolutionValidatesCandidateAndMapsReverse(t *testing.T) {
	plan := diff.Plan{Dialect: "sqlite", Decisions: []diff.RequiredDecision{{ID: "rename_sqlite_members_display_name", Kind: diff.DecisionRename, Table: "members", Column: "display_name", Baseline: "name", Target: "display_name"}}}
	resolved, err := plan.Resolve(diff.Resolution{DecisionID: "rename_sqlite_members_display_name", RenameFrom: "name"})
	require.NoError(t, err)
	require.Contains(t, resolved.Statements[0].SQL, "name TO display_name")
	require.Contains(t, resolved.Statements[0].ReverseSQL, "display_name TO name")
	_, err = plan.Resolve(diff.Resolution{DecisionID: "rename_sqlite_members_display_name", RenameFrom: "other"})
	require.ErrorContains(t, err, "non-candidate")
	_, err = plan.Resolve(diff.Resolution{DecisionID: "rename_sqlite_members_display_name", RenameFrom: "name", BackfillSQL: "SELECT 1;"})
	require.Error(t, err)
	_, err = plan.Resolve(diff.Resolution{DecisionID: "rename_other_members_display_name", RenameFrom: "name"})
	require.ErrorContains(t, err, "unknown decision")
	plan.Decisions[0].Baseline = "mutated"
	require.Contains(t, resolved.Statements[0].ReverseSQL, "display_name TO name")
}
