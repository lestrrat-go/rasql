package mysql_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/lestrrat-go/rasql/migrate/diff/mysql"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
)

func parseMySQL(t *testing.T, analyzer mysql.Analyzer, source string) diff.Snapshot {
	t.Helper()
	snapshot, err := analyzer.Parse([]diff.Source{{Path: "schema.sql", SQL: sqltext.Text(source)}})
	require.NoError(t, err)
	return snapshot
}

func TestSchemaEvolutionMySQLColumnLoweringMatrix(t *testing.T) {
	analyzer := mysql.New()
	fixtures := []struct {
		name, baseline, target, forward, reverse string
	}{
		{
			name:     "nullable to required",
			baseline: "CREATE TABLE `tasks` (`id` bigint NOT NULL AUTO_INCREMENT PRIMARY KEY, `amount` varchar(120) COLLATE `utf8mb4_bin` NULL DEFAULT 'open', `computed` int GENERATED ALWAYS AS (`amount` + 1) STORED);",
			target:   "CREATE TABLE `tasks` (`id` bigint NOT NULL AUTO_INCREMENT PRIMARY KEY, `amount` varchar(120) COLLATE `utf8mb4_bin` NOT NULL DEFAULT 'open', `computed` int GENERATED ALWAYS AS (`amount` + 1) STORED);",
			forward:  "ALTER TABLE `tasks` MODIFY COLUMN `amount` varchar(120) COLLATE `utf8mb4_bin` NOT NULL DEFAULT 'open';\n",
			reverse:  "ALTER TABLE `tasks` MODIFY COLUMN `amount` varchar(120) COLLATE `utf8mb4_bin` NULL DEFAULT 'open';\n",
		},
		{
			name:     "required to nullable",
			baseline: "CREATE TABLE `tasks` (`id` bigint NOT NULL AUTO_INCREMENT PRIMARY KEY, `amount` varchar(120) COLLATE `utf8mb4_bin` NOT NULL DEFAULT 'open', `computed` int GENERATED ALWAYS AS (`amount` + 1) STORED);",
			target:   "CREATE TABLE `tasks` (`id` bigint NOT NULL AUTO_INCREMENT PRIMARY KEY, `amount` varchar(120) COLLATE `utf8mb4_bin` NULL DEFAULT 'open', `computed` int GENERATED ALWAYS AS (`amount` + 1) STORED);",
			forward:  "ALTER TABLE `tasks` MODIFY COLUMN `amount` varchar(120) COLLATE `utf8mb4_bin` NULL DEFAULT 'open';\n",
			reverse:  "ALTER TABLE `tasks` MODIFY COLUMN `amount` varchar(120) COLLATE `utf8mb4_bin` NOT NULL DEFAULT 'open';\n",
		},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			baseline := parseMySQL(t, analyzer, fixture.baseline)
			target := parseMySQL(t, analyzer, fixture.target)
			plan, err := analyzer.Diff(baseline, target)
			require.NoError(t, err)
			require.Empty(t, plan.Decisions)
			require.Equal(t, []diff.ProposedOperation{expectedNullabilityOperation(fixture.forward, fixture.reverse)}, plan.Operations)
			require.Equal(t, []diff.PlannedStatement{expectedNullabilityStatement(fixture.forward, fixture.reverse)}, plan.Statements)
		})
	}
}

func expectedNullabilityOperation(forwardSQL, reverseSQL string) diff.ProposedOperation {
	forward := expectedNullabilityStatement(forwardSQL, reverseSQL)
	reverse := expectedNullabilityStatement(reverseSQL, forwardSQL)
	return diff.ProposedOperation{
		ID: "alter_nullability_mysql_tasks_amount", Table: "tasks", Column: "amount",
		Summary: "alter nullability tasks.amount", Kind: diff.OperationAlterNullability,
		Forward: []diff.PlannedStatement{forward}, Reverse: []diff.PlannedStatement{reverse},
	}
}

func expectedNullabilityStatement(sql, reverseSQL string) diff.PlannedStatement {
	return diff.PlannedStatement{Source: "001_alter_nullability_tasks_amount.sql", SQL: sql, ReverseSQL: reverseSQL, Summary: "alter nullability tasks.amount"}
}

func TestSchemaEvolutionMySQLRequiredAddStagesBackfill(t *testing.T) {
	analyzer := mysql.New()
	baseline := parseMySQL(t, analyzer, "CREATE TABLE `tasks` (`id` bigint NOT NULL);")
	target := parseMySQL(t, analyzer, "CREATE TABLE `tasks` (`id` bigint NOT NULL, `state` varchar(20) NOT NULL DEFAULT NULL);")
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.False(t, plan.Executable())
	directory := filepath.Join(t.TempDir(), "parent", "migration")
	require.ErrorContains(t, diff.WriteMigration(directory, plan), "unresolved decisions")
	_, statErr := os.Stat(filepath.Dir(directory))
	require.ErrorIs(t, statErr, os.ErrNotExist)
	resolved, err := plan.Resolve(diff.Resolution{DecisionID: "backfill_mysql_tasks_state", BackfillSQL: "UPDATE `tasks` SET `state` = 'open';"})
	require.NoError(t, err)
	require.Len(t, resolved.Statements, 3)
	require.Equal(t, "ALTER TABLE `tasks` ADD COLUMN `state` varchar(20) DEFAULT NULL;\n", resolved.Statements[0].SQL)
	require.Equal(t, "UPDATE `tasks` SET `state` = 'open';", resolved.Statements[1].SQL)
	require.Equal(t, "ALTER TABLE `tasks` MODIFY COLUMN `state` varchar(20) NOT NULL;\n", resolved.Statements[2].SQL)
	require.Empty(t, resolved.Operations[0].Reverse)
	require.Equal(t, "caller-supplied MySQL backfill has no inferred reverse", resolved.IrreversibleReason)
}

func TestSchemaEvolutionMySQLRenamePreservesBaselineAndTargetIdentifiers(t *testing.T) {
	analyzer := mysql.New()
	baselineName := "Old`Name"
	targetName := "New`Name"
	baseline := parseMySQL(t, analyzer, "CREATE TABLE `Accounts` (`Old``Name` varchar(20));")
	target := parseMySQL(t, analyzer, "CREATE TABLE `Accounts` (`New``Name` varchar(20));")
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	expectedOperation := diff.ProposedOperation{ID: "add_column_mysql_accounts_new`name", Table: "Accounts", Column: targetName, Summary: "add column Accounts." + targetName, Kind: diff.OperationAddColumn}
	expectedDecision := diff.RequiredDecision{ID: "rename_mysql_accounts_new`name", Kind: diff.DecisionRename, Table: "Accounts", Column: targetName, Baseline: baselineName, Target: targetName, Reason: "column rename requires caller confirmation"}
	require.Equal(t, []diff.ProposedOperation{expectedOperation}, plan.Operations)
	require.Equal(t, []diff.RequiredDecision{expectedDecision}, plan.Decisions)
	forwardSQL := "ALTER TABLE `Accounts` RENAME COLUMN `Old``Name` TO `New``Name`;\n"
	reverseSQL := "ALTER TABLE `Accounts` RENAME COLUMN `New``Name` TO `Old``Name`;\n"
	forward := diff.PlannedStatement{Source: "001_rename_column_accounts_new_name.sql", SQL: forwardSQL, ReverseSQL: reverseSQL, Summary: "rename Accounts." + baselineName}
	reverse := diff.PlannedStatement{Source: forward.Source, SQL: reverseSQL, ReverseSQL: forwardSQL, Summary: forward.Summary}
	expectedOperation.Forward = []diff.PlannedStatement{forward}
	expectedOperation.Reverse = []diff.PlannedStatement{reverse}
	resolved, err := plan.Resolve(diff.Resolution{DecisionID: expectedDecision.ID, RenameFrom: baselineName})
	require.NoError(t, err)
	require.Equal(t, []diff.ProposedOperation{expectedOperation}, resolved.Operations)
	require.Equal(t, []diff.PlannedStatement{forward}, resolved.Statements)
	require.Empty(t, resolved.Decisions)
	resolved.Operations[0].Forward[0].SQL = "mutated result"
	resolved.Statements[0].SQL = "mutated statement"
	again, err := plan.Resolve(diff.Resolution{DecisionID: expectedDecision.ID, RenameFrom: baselineName})
	require.NoError(t, err)
	require.Equal(t, []diff.ProposedOperation{expectedOperation}, again.Operations)
	require.Equal(t, []diff.PlannedStatement{forward}, again.Statements)
}

func TestSchemaEvolutionMySQLNamedConstraintReplacementMatrix(t *testing.T) {
	a := mysql.New()
	cases := []struct {
		name, baseline, target, constraint, drop, baselineAdd, targetAdd string
	}{
		{
			name: "primary", constraint: "pk_child", drop: "ALTER TABLE `child` DROP PRIMARY KEY;\n",
			baseline: "CONSTRAINT `pk_child` PRIMARY KEY (`id`)", target: "CONSTRAINT `pk_child` PRIMARY KEY (`parent_id`)",
			baselineAdd: "ALTER TABLE `child` ADD CONSTRAINT `pk_child` PRIMARY KEY (`id`);\n", targetAdd: "ALTER TABLE `child` ADD CONSTRAINT `pk_child` PRIMARY KEY (`parent_id`);\n",
		},
		{
			name: "unique", constraint: "uq_child", drop: "ALTER TABLE `child` DROP INDEX `uq_child`;\n",
			baseline: "CONSTRAINT `uq_child` UNIQUE (`id`)", target: "CONSTRAINT `uq_child` UNIQUE (`parent_id`)",
			baselineAdd: "ALTER TABLE `child` ADD CONSTRAINT `uq_child` UNIQUE (`id`);\n", targetAdd: "ALTER TABLE `child` ADD CONSTRAINT `uq_child` UNIQUE (`parent_id`);\n",
		},
		{
			name: "foreign key", constraint: "fk_child", drop: "ALTER TABLE `child` DROP FOREIGN KEY `fk_child`;\n",
			baseline: "CONSTRAINT `fk_child` FOREIGN KEY (`parent_id`) REFERENCES `parent` (`id`)", target: "CONSTRAINT `fk_child` FOREIGN KEY (`parent_id`) REFERENCES `parent` (`code`)",
			baselineAdd: "ALTER TABLE `child` ADD CONSTRAINT `fk_child` FOREIGN KEY (`parent_id`) REFERENCES `parent` (`id`);\n", targetAdd: "ALTER TABLE `child` ADD CONSTRAINT `fk_child` FOREIGN KEY (`parent_id`) REFERENCES `parent` (`code`);\n",
		},
		{
			name: "check", constraint: "ck_child", drop: "ALTER TABLE `child` DROP CHECK `ck_child`;\n",
			baseline: "CONSTRAINT `ck_child` CHECK (`id` > 0)", target: "CONSTRAINT `ck_child` CHECK (`id` >= 0)",
			baselineAdd: "ALTER TABLE `child` ADD CONSTRAINT `ck_child` CHECK (id > 0);\n", targetAdd: "ALTER TABLE `child` ADD CONSTRAINT `ck_child` CHECK (id >= 0);\n",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			baseline := parseMySQL(t, a, namedConstraintSchema(test.baseline))
			target := parseMySQL(t, a, namedConstraintSchema(test.target))
			plan, err := a.Diff(baseline, target)
			require.NoError(t, err)
			require.Empty(t, plan.Decisions)
			require.Equal(t, []diff.ProposedOperation{expectedConstraintOperation(test.constraint, test.drop, test.baselineAdd, test.targetAdd)}, plan.Operations)
			require.Equal(t, expectedConstraintStatements(test.constraint, test.drop, test.baselineAdd, test.targetAdd), plan.Statements)
		})
	}
}

func namedConstraintSchema(constraint string) string {
	if constraint == "" {
		return "CREATE TABLE `parent` (`id` bigint PRIMARY KEY, `code` bigint UNIQUE); CREATE TABLE `child` (`id` bigint NOT NULL, `parent_id` bigint NOT NULL);"
	}
	return "CREATE TABLE `parent` (`id` bigint PRIMARY KEY, `code` bigint UNIQUE); CREATE TABLE `child` (`id` bigint NOT NULL, `parent_id` bigint NOT NULL, " + constraint + ");"
}

func expectedConstraintOperation(name, drop, baselineAdd, targetAdd string) diff.ProposedOperation {
	summary := "replace constraint " + name
	forwardDrop := diff.PlannedStatement{Source: "001_drop_constraint_child_" + name + ".sql", SQL: drop, ReverseSQL: baselineAdd, Summary: summary}
	forwardAdd := diff.PlannedStatement{Source: "002_add_constraint_child_" + name + ".sql", SQL: targetAdd, ReverseSQL: drop, Summary: summary}
	return diff.ProposedOperation{
		ID: "replace_constraint_mysql_child_" + name, Table: "child", Constraint: name,
		Summary: "replace constraint " + name, Kind: diff.OperationReplaceConstraint,
		Forward: []diff.PlannedStatement{forwardDrop, forwardAdd},
		Reverse: []diff.PlannedStatement{
			{Source: forwardAdd.Source, SQL: drop, ReverseSQL: targetAdd, Summary: forwardAdd.Summary},
			{Source: forwardDrop.Source, SQL: baselineAdd, ReverseSQL: drop, Summary: forwardDrop.Summary},
		},
	}
}

func TestSchemaEvolutionMySQLNamedConstraintRefusals(t *testing.T) {
	a := mysql.New()
	cases := []struct {
		name, baseline, target, want string
	}{
		{name: "anonymous replacement", baseline: "CONSTRAINT_PLACEHOLDER", target: "CONSTRAINT_PLACEHOLDER", want: "mysql schema diff: table child constraints changed: anonymous UNIQUE replacement is unsupported"},
		{name: "anonymous addition", baseline: "", target: "UNIQUE (`parent_id`)", want: "mysql schema diff: table child constraints changed: anonymous UNIQUE addition is unsupported"},
		{name: "anonymous removal", baseline: "UNIQUE (`id`)", target: "", want: "mysql schema diff: table child constraints changed: anonymous UNIQUE removal is unsupported"},
		{name: "anonymous foreign key replacement", baseline: "FOREIGN KEY (`parent_id`) REFERENCES `parent` (`id`)", target: "FOREIGN KEY (`parent_id`) REFERENCES `parent` (`code`)", want: "mysql schema diff: table child constraints changed: anonymous FOREIGN KEY replacement is unsupported"},
		{name: "anonymous foreign key addition", baseline: "", target: "FOREIGN KEY (`parent_id`) REFERENCES `parent` (`id`)", want: "mysql schema diff: table child constraints changed: anonymous FOREIGN KEY addition is unsupported"},
		{name: "anonymous foreign key removal", baseline: "FOREIGN KEY (`parent_id`) REFERENCES `parent` (`id`)", target: "", want: "mysql schema diff: table child constraints changed: anonymous FOREIGN KEY removal is unsupported"},
		{name: "anonymous check replacement", baseline: "CHECK (`id` > 0)", target: "CHECK (`id` >= 0)", want: "mysql schema diff: table child constraints changed: anonymous CHECK replacement is unsupported"},
		{name: "anonymous check addition", baseline: "", target: "CHECK (`id` > 0)", want: "mysql schema diff: table child constraints changed: anonymous CHECK addition is unsupported"},
		{name: "anonymous check removal", baseline: "CHECK (`id` > 0)", target: "", want: "mysql schema diff: table child constraints changed: anonymous CHECK removal is unsupported"},
		{name: "renamed constraint", baseline: "CONSTRAINT `old_uq` UNIQUE (`id`)", target: "CONSTRAINT `new_uq` UNIQUE (`id`)", want: "mysql schema diff: constraint old_uq on table child was renamed to new_uq"},
		{name: "same-name kind change", baseline: "CONSTRAINT `same_name` UNIQUE (`id`)", target: "CONSTRAINT `same_name` CHECK (`id` > 0)", want: "mysql schema diff: named constraint same_name on table child changed kind from UNIQUE to CHECK"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			baselineSQL, targetSQL := test.baseline, test.target
			if baselineSQL == "CONSTRAINT_PLACEHOLDER" {
				baselineSQL, targetSQL = "UNIQUE (`id`)", "UNIQUE (`parent_id`)"
			}
			baseline := parseMySQL(t, a, namedConstraintSchema(baselineSQL))
			target := parseMySQL(t, a, namedConstraintSchema(targetSQL))
			plan, err := a.Diff(baseline, target)
			require.EqualError(t, err, test.want)
			require.Equal(t, diff.Plan{}, plan)
		})
	}
}

func TestSchemaEvolutionMySQLNamedConstraintOneSidedIsReversible(t *testing.T) {
	a := mysql.New()
	cases := []struct {
		name, baseline, target, source, summary, forward, reverse string
	}{
		{name: "target only", baseline: "", target: "CONSTRAINT `uq_child` UNIQUE (`id`)", source: "001_add_constraint_child_uq_child.sql", summary: "replace constraint uq_child", forward: "ALTER TABLE `child` ADD CONSTRAINT `uq_child` UNIQUE (`id`);\n", reverse: "ALTER TABLE `child` DROP INDEX `uq_child`;\n"},
		{name: "baseline only", baseline: "CONSTRAINT `uq_child` UNIQUE (`id`)", target: "", source: "001_drop_constraint_child_uq_child.sql", summary: "remove constraint uq_child", forward: "ALTER TABLE `child` DROP INDEX `uq_child`;\n", reverse: "ALTER TABLE `child` ADD CONSTRAINT `uq_child` UNIQUE (`id`);\n"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			baseline := parseMySQL(t, a, namedConstraintSchema(test.baseline))
			target := parseMySQL(t, a, namedConstraintSchema(test.target))
			plan, err := a.Diff(baseline, target)
			require.NoError(t, err)
			require.Empty(t, plan.Decisions)
			operation := diff.ProposedOperation{ID: "replace_constraint_mysql_child_uq_child", Table: "child", Constraint: "uq_child", Summary: test.summary, Kind: diff.OperationReplaceConstraint}
			forward := diff.PlannedStatement{Source: test.source, SQL: test.forward, ReverseSQL: test.reverse, Summary: operation.Summary}
			reverse := diff.PlannedStatement{Source: forward.Source, SQL: test.reverse, ReverseSQL: test.forward, Summary: operation.Summary}
			operation.Forward = []diff.PlannedStatement{forward}
			operation.Reverse = []diff.PlannedStatement{reverse}
			require.Equal(t, []diff.ProposedOperation{operation}, plan.Operations)
			require.Equal(t, []diff.PlannedStatement{forward}, plan.Statements)
		})
	}
}

func TestSchemaEvolutionMySQLRefusesUnsupportedColumnFacts(t *testing.T) {
	a := mysql.New()
	cases := []struct {
		name, baseline, target, fact string
	}{
		{name: "type", baseline: "`amount` varchar(20)", target: "`amount` bigint", fact: "type/modifier"},
		{name: "modifier", baseline: "`amount` decimal(10,2)", target: "`amount` decimal(10,3)", fact: "type/modifier"},
		{name: "collation", baseline: "`amount` varchar(20) COLLATE `utf8mb4_bin`", target: "`amount` varchar(20) COLLATE `latin1_bin`", fact: "collation"},
		{name: "default", baseline: "`amount` varchar(20) DEFAULT 'old'", target: "`amount` varchar(20) DEFAULT 'new'", fact: "default"},
		{name: "generated expression", baseline: "`amount` int GENERATED ALWAYS AS (`id` + 1) STORED", target: "`amount` int GENERATED ALWAYS AS (`id` + 2) STORED", fact: "generated expression/storage"},
		{name: "generated storage", baseline: "`amount` int GENERATED ALWAYS AS (`id` + 1) STORED", target: "`amount` int GENERATED ALWAYS AS (`id` + 1) VIRTUAL", fact: "generated expression/storage"},
		{name: "auto increment", baseline: "`id` bigint PRIMARY KEY", target: "`id` bigint AUTO_INCREMENT PRIMARY KEY", fact: "AUTO_INCREMENT"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			baselineSQL := "CREATE TABLE `facts` (`id` bigint PRIMARY KEY, " + test.baseline + ");"
			targetSQL := "CREATE TABLE `facts` (`id` bigint PRIMARY KEY, " + test.target + ");"
			wantColumn := "amount"
			if test.name == "auto increment" {
				baselineSQL = "CREATE TABLE `facts` (`id` bigint PRIMARY KEY);"
				targetSQL = "CREATE TABLE `facts` (`id` bigint AUTO_INCREMENT PRIMARY KEY);"
				wantColumn = "id"
			}
			baseline := parseMySQL(t, a, baselineSQL)
			target := parseMySQL(t, a, targetSQL)
			plan, err := a.Diff(baseline, target)
			require.EqualError(t, err, "mysql schema diff: table facts column "+wantColumn+" changed in unsupported fact "+test.fact)
			require.Equal(t, diff.Plan{}, plan)
		})
	}
}

func TestSchemaEvolutionMySQLRefusesUnrepresentableForeignKeyClauses(t *testing.T) {
	a := mysql.New()
	for _, test := range []struct{ clause, want string }{
		{clause: "MATCH FULL", want: `mysql schema source "schema.sql": unsupported FOREIGN KEY MATCH clause`},
		{clause: "ON UPDATE CASCADE", want: `mysql schema source "schema.sql": unsupported FOREIGN KEY ON UPDATE clause`},
		{clause: "ON DELETE SET NULL", want: `mysql schema source "schema.sql": unsupported FOREIGN KEY ON DELETE clause`},
	} {
		t.Run(test.clause, func(t *testing.T) {
			source := "CREATE TABLE `child` (`parent_id` bigint, CONSTRAINT `fk_child` FOREIGN KEY (`parent_id`) REFERENCES `parent` (`id`) " + test.clause + ");"
			snapshot, err := a.Parse([]diff.Source{{Path: "schema.sql", SQL: sqltext.Text(source)}})
			require.EqualError(t, err, test.want)
			require.Nil(t, snapshot)
		})
	}
}

func TestSchemaEvolutionMySQLParsePreservesOriginalMalformedDiagnostics(t *testing.T) {
	a := mysql.New()
	for _, test := range []struct {
		name, source string
	}{
		{name: "quoted string", source: "CREATE TABLE `facts` (`label` varchar(20) DEFAULT 'MATCH FULL',);"},
		{name: "quoted identifier", source: "CREATE TABLE `facts` (`MATCH FULL` varchar(20),);"},
		{name: "comment", source: "CREATE TABLE `facts` (`label` varchar(20) /* ON DELETE CASCADE */,);"},
		{name: "non foreign key", source: "CREATE TABLE `facts` (`label` varchar(20) MATCH FULL);"},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot, err := a.Parse([]diff.Source{{Path: "schema.sql", SQL: sqltext.Text(test.source)}})
			require.Error(t, err)
			require.Nil(t, snapshot)
			require.EqualError(t, err, map[string]string{
				"quoted string":     `mysql schema source "schema.sql": query: expected identifier at byte 63`,
				"quoted identifier": `mysql schema source "schema.sql": query: expected identifier at byte 47`,
				"comment":           `mysql schema source "schema.sql": query: expected identifier at byte 66`,
				"non foreign key":   `mysql schema source "schema.sql": query: expected comma or closing parenthesis in table definition at byte 42`,
			}[test.name])
		})
	}
}

func TestSchemaEvolutionMySQLRefusesIndexPrefixAndOrderChanges(t *testing.T) {
	a := mysql.New()
	cases := []struct {
		name, baseline, target string
	}{
		{name: "prefix", baseline: "CREATE INDEX `email_idx` ON `facts` (`email`);", target: "CREATE INDEX `email_idx` ON `facts` (`email`(4));"},
		{name: "order", baseline: "CREATE INDEX `email_idx` ON `facts` (`email` ASC);", target: "CREATE INDEX `email_idx` ON `facts` (`email` DESC);"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			baseline := parseMySQL(t, a, "CREATE TABLE `facts` (`email` varchar(20)); "+test.baseline)
			target := parseMySQL(t, a, "CREATE TABLE `facts` (`email` varchar(20)); "+test.target)
			plan, err := a.Diff(baseline, target)
			require.EqualError(t, err, "mysql schema diff: index email_idx changed on table facts; manual migration is required")
			require.Equal(t, diff.Plan{}, plan)
		})
	}
}

func TestSchemaEvolutionMySQLRefusesAnonymousInlineKeyChanges(t *testing.T) {
	a := mysql.New()
	for _, test := range []struct {
		name, baseline, target, want string
	}{
		{name: "primary", baseline: "PRIMARY KEY (`id`)", target: "PRIMARY KEY (`parent_id`)", want: "anonymous PRIMARY KEY replacement is unsupported"},
		{name: "unique", baseline: "UNIQUE (`id`)", target: "UNIQUE (`parent_id`)", want: "anonymous UNIQUE replacement is unsupported"},
	} {
		t.Run(test.name, func(t *testing.T) {
			baseline := parseMySQL(t, a, "CREATE TABLE `facts` (`id` bigint NOT NULL, `parent_id` bigint NOT NULL, "+test.baseline+");")
			target := parseMySQL(t, a, "CREATE TABLE `facts` (`id` bigint NOT NULL, `parent_id` bigint NOT NULL, "+test.target+");")
			plan, err := a.Diff(baseline, target)
			require.EqualError(t, err, "mysql schema diff: table facts constraints changed: "+test.want)
			require.Equal(t, diff.Plan{}, plan)
		})
	}
}

func expectedConstraintStatements(name, drop, baselineAdd, targetAdd string) []diff.PlannedStatement {
	operation := expectedConstraintOperation(name, drop, baselineAdd, targetAdd)
	return operation.Forward
}
