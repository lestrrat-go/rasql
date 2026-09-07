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

func TestTaskboardCreateRenderingPreservesFacts(t *testing.T) {
	data, err := os.ReadFile("../../../sample/taskboard/db/migrations/001_initial/003_create_tasks.up.sql")
	require.NoError(t, err)
	snapshot, err := New().Parse([]diff.Source{{Path: "tasks.sql", SQL: sqltext.Text(data)}})
	require.NoError(t, err)
	table := snapshot.(*schemaSnapshot).tables["5:tasks"]
	rendered, err := renderCreateTable(table)
	require.NoError(t, err)
	require.Contains(t, rendered, `"id" bigint  GENERATED ALWAYS AS IDENTITY NOT NULL`)
	require.Contains(t, rendered, `CONSTRAINT "tasks_assignee_id_fkey" FOREIGN KEY ("assignee_id") REFERENCES "members" ("id") ON DELETE NO ACTION ON UPDATE NO ACTION`)
	require.Contains(t, rendered, `CONSTRAINT "tasks_project_id_fkey" FOREIGN KEY ("project_id") REFERENCES "projects" ("id") ON DELETE CASCADE ON UPDATE NO ACTION`)
}

func TestTaskboardFactCarrierIsImmutableAcrossClone(t *testing.T) {
	data, err := os.ReadFile("../../../sample/taskboard/db/migrations/001_initial/003_create_tasks.up.sql")
	require.NoError(t, err)
	snapshot, err := New().Parse([]diff.Source{{Path: "tasks.sql", SQL: sqltext.Text(data)}})
	require.NoError(t, err)
	original := snapshot.(*schemaSnapshot).tables["5:tasks"]
	cloned, err := cloneTableDefinition(original)
	require.NoError(t, err)
	original.statement.Name[0].Name = "mutated"
	original.statement.Columns[0].Name.Name = "mutated"
	original.statement.Columns[0].Type.Words[0] = "text"
	original.statement.Columns[0].Constraints[0].Name = nil
	for index := range original.statement.Constraints {
		if original.statement.Constraints[index].References != nil {
			original.statement.Constraints[index].References.Table[0].Name = "mutated"
			original.statement.Constraints[index].References.Columns[0].Name = "mutated"
			break
		}
	}
	original.identities["2:id"] = identityByDefault
	original.foreignKeys[foreignKeyKey{table: "5:tasks", constraint: "24:tasks_project_id_fkey"}] = foreignKeyActions{onDelete: referenceRestrict}
	require.Equal(t, "tasks", cloned.statement.Name[0].Name)
	require.Equal(t, "id", cloned.statement.Columns[0].Name.Name)
	require.Equal(t, "bigint", cloned.statement.Columns[0].Type.Words[0])
	require.Equal(t, identityAlways, cloned.identities["2:id"])
}

func TestTaskboardFactCarrierIsImmutableAcrossResolve(t *testing.T) {
	analyzer := New()
	base, err := analyzer.Parse([]diff.Source{{Path: "base.sql", SQL: "CREATE TABLE tasks (id bigint PRIMARY KEY, owner_id bigint);"}})
	require.NoError(t, err)
	target, err := analyzer.Parse([]diff.Source{{Path: "target.sql", SQL: "CREATE TABLE tasks (id bigint PRIMARY KEY, owner_id bigint, due_on date, owner_label text NOT NULL);"}})
	require.NoError(t, err)
	plan, err := analyzer.Diff(base, target)
	require.NoError(t, err)
	previewOperations := cloneTestOperations(plan.Operations)
	previewDecisions := append([]diff.RequiredDecision(nil), plan.Decisions...)
	original := base.(*schemaSnapshot).tables["5:tasks"]
	original.statement.Name[0].Name = "changed"
	for index := range original.statement.Columns {
		original.statement.Columns[index].Name.Name = "changed"
		if len(original.statement.Columns[index].Type.Words) > 0 {
			original.statement.Columns[index].Type.Words[0] = "text"
		}
		if original.statement.Columns[index].Type.Modifiers != nil {
			original.statement.Columns[index].Type.Modifiers = []pgquery.Expression{&pgquery.Literal{Value: "changed"}}
		}
		for constraintIndex := range original.statement.Columns[index].Constraints {
			constraint := &original.statement.Columns[index].Constraints[constraintIndex]
			if constraint.Name != nil {
				constraint.Name.Name = "changed"
			}
			constraint.Expression = &pgquery.Literal{Value: "changed"}
			if constraint.References != nil {
				constraint.References.Table[0].Name = "changed"
				if len(constraint.References.Columns) > 0 {
					constraint.References.Columns[0].Name = "changed"
				}
			}
		}
	}
	for index := range original.statement.Constraints {
		constraint := &original.statement.Constraints[index]
		if constraint.Name != nil {
			constraint.Name.Name = "changed"
		}
		constraint.Expression = &pgquery.Literal{Value: "changed"}
		if len(constraint.Columns) > 0 {
			constraint.Columns[0].Name = "changed"
		}
		if constraint.References != nil {
			constraint.References.Table[0].Name = "changed"
			if len(constraint.References.Columns) > 0 {
				constraint.References.Columns[0].Name = "changed"
			}
		}
	}
	original.identities["2:id"] = identityByDefault
	if targetTable := target.(*schemaSnapshot).tables["5:tasks"]; targetTable.statement != nil {
		targetTable.statement.Name[0].Name = "changed"
		for index := range targetTable.statement.Columns {
			targetTable.statement.Columns[index].Name.Name = "changed"
			if len(targetTable.statement.Columns[index].Type.Words) > 0 {
				targetTable.statement.Columns[index].Type.Words[0] = "text"
			}
		}
	}
	var backfill diff.RequiredDecision
	for _, decision := range plan.Decisions {
		if decision.Kind == diff.DecisionBackfill {
			backfill = decision
			break
		}
	}
	first, err := plan.Resolve(diff.Resolution{DecisionID: backfill.ID, BackfillSQL: `UPDATE "tasks" SET "owner_label" = 'one-' || "owner_id";`})
	require.NoError(t, err)
	for index := range first.Operations {
		first.Operations[index].ID = "mutated"
		first.Operations[index].Table = "mutated"
		first.Operations[index].Column = "mutated"
		first.Operations[index].Constraint = "mutated"
		first.Operations[index].Summary = "mutated"
		for statementIndex := range first.Operations[index].Forward {
			first.Operations[index].Forward[statementIndex].Source = "mutated"
			first.Operations[index].Forward[statementIndex].SQL = "mutated"
			first.Operations[index].Forward[statementIndex].ReverseSQL = "mutated"
			first.Operations[index].Forward[statementIndex].Summary = "mutated"
		}
		for statementIndex := range first.Operations[index].Reverse {
			first.Operations[index].Reverse[statementIndex].Source = "mutated"
			first.Operations[index].Reverse[statementIndex].SQL = "mutated"
			first.Operations[index].Reverse[statementIndex].ReverseSQL = "mutated"
			first.Operations[index].Reverse[statementIndex].Summary = "mutated"
		}
	}
	for index := range first.Statements {
		first.Statements[index].Source = "mutated"
		first.Statements[index].SQL = "mutated"
		first.Statements[index].ReverseSQL = "mutated"
		first.Statements[index].Summary = "mutated"
	}
	second, err := plan.Resolve(diff.Resolution{DecisionID: backfill.ID, BackfillSQL: `UPDATE "tasks" SET "owner_label" = 'two-' || "owner_id";`})
	require.NoError(t, err)
	require.Equal(t, previewOperations, plan.Operations)
	require.Equal(t, previewDecisions, plan.Decisions)
	require.Equal(t, []diff.ProposedOperation{
		{ID: "add_column_postgresql_tasks_due_on", Table: "tasks", Column: "due_on", Summary: "add column tasks.due_on", Kind: diff.OperationAddColumn,
			Forward: []diff.PlannedStatement{{Source: "001_add_column_tasks_due_on.sql", SQL: "ALTER TABLE tasks ADD COLUMN due_on date;\n", ReverseSQL: "ALTER TABLE tasks DROP COLUMN due_on;\n", Summary: "add column tasks.due_on"}},
			Reverse: []diff.PlannedStatement{{Source: "001_add_column_tasks_due_on.sql", SQL: "ALTER TABLE tasks DROP COLUMN due_on;\n", ReverseSQL: "ALTER TABLE tasks ADD COLUMN due_on date;\n", Summary: "add column tasks.due_on"}}},
		{ID: "add_column_postgresql_tasks_owner_label", Table: "tasks", Column: "owner_label", Summary: "add column tasks.owner_label", Kind: diff.OperationAddColumn,
			Forward: []diff.PlannedStatement{{Source: "002_add_column_tasks_owner_label.sql", SQL: "ALTER TABLE tasks ADD COLUMN owner_label text;\nUPDATE \"tasks\" SET \"owner_label\" = 'two-' || \"owner_id\";\nALTER TABLE tasks ALTER COLUMN owner_label SET NOT NULL;\n", ReverseSQL: "ALTER TABLE tasks DROP COLUMN owner_label;\n", Summary: "add column tasks.owner_label"}},
			Reverse: []diff.PlannedStatement{{Source: "002_add_column_tasks_owner_label.sql", SQL: "ALTER TABLE tasks DROP COLUMN owner_label;\n", ReverseSQL: "ALTER TABLE tasks ADD COLUMN owner_label text;\nUPDATE \"tasks\" SET \"owner_label\" = 'two-' || \"owner_id\";\nALTER TABLE tasks ALTER COLUMN owner_label SET NOT NULL;\n", Summary: "add column tasks.owner_label"}}},
	}, second.Operations)
	require.Equal(t, []diff.PlannedStatement{
		{Source: "001_add_column_tasks_due_on.sql", SQL: "ALTER TABLE tasks ADD COLUMN due_on date;\n", ReverseSQL: "ALTER TABLE tasks DROP COLUMN due_on;\n", Summary: "add column tasks.due_on"},
		{Source: "002_add_column_tasks_owner_label.sql", SQL: "ALTER TABLE tasks ADD COLUMN owner_label text;\nUPDATE \"tasks\" SET \"owner_label\" = 'two-' || \"owner_id\";\nALTER TABLE tasks ALTER COLUMN owner_label SET NOT NULL;\n", ReverseSQL: "ALTER TABLE tasks DROP COLUMN owner_label;\n", Summary: "add column tasks.owner_label"},
	}, second.Statements)
}

func cloneTestOperations(in []diff.ProposedOperation) []diff.ProposedOperation {
	out := append([]diff.ProposedOperation(nil), in...)
	for index := range out {
		out[index].Forward = append([]diff.PlannedStatement(nil), in[index].Forward...)
		out[index].Reverse = append([]diff.PlannedStatement(nil), in[index].Reverse...)
	}
	return out
}
