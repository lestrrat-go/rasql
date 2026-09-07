package mysql_test

import (
	"os"
	"path/filepath"
	"testing"

	mysqlquery "github.com/lestrrat-go/rasql-mysql/query"
	"github.com/lestrrat-go/rasql/internal/migrationdir"
	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/lestrrat-go/rasql/migrate/diff/mysql"
	"github.com/stretchr/testify/require"
)

func TestSchemaEvolutionMySQLSchedulesOneNativeStatementPerSource(t *testing.T) {
	a := mysql.New()
	from := parseMySQL(t, a, "CREATE TABLE `tasks` (`id` bigint NOT NULL, `code` varchar(20) NULL DEFAULT NULL, `amount` varchar(20) NULL DEFAULT '0', CONSTRAINT `uq_tasks` UNIQUE (`code`));")
	to := parseMySQL(t, a, "CREATE TABLE `tasks` (`id` bigint NOT NULL, `code` varchar(20) NULL DEFAULT NULL, `amount` varchar(20) NOT NULL DEFAULT '0', `due_on` datetime(6) NULL DEFAULT NULL, CONSTRAINT `uq_tasks` UNIQUE (`id`)); CREATE INDEX `ix_tasks_amount` ON `tasks` (`amount`);")
	plan, err := a.Diff(from, to)
	require.NoError(t, err)
	want := []diff.PlannedStatement{{Source: "001_drop_constraint_tasks_uq_tasks.sql", SQL: "ALTER TABLE `tasks` DROP INDEX `uq_tasks`;\n", ReverseSQL: "ALTER TABLE `tasks` ADD CONSTRAINT `uq_tasks` UNIQUE (`code`);\n", Summary: "replace constraint uq_tasks"}, {Source: "002_add_column_tasks_due_on.sql", SQL: "ALTER TABLE `tasks` ADD COLUMN `due_on` datetime(6) NULL DEFAULT NULL;\n", ReverseSQL: "ALTER TABLE `tasks` DROP COLUMN `due_on`;\n", Summary: "add column tasks.due_on"}, {Source: "003_alter_nullability_tasks_amount.sql", SQL: "ALTER TABLE `tasks` MODIFY COLUMN `amount` varchar(20) NOT NULL DEFAULT '0';\n", ReverseSQL: "ALTER TABLE `tasks` MODIFY COLUMN `amount` varchar(20) NULL DEFAULT '0';\n", Summary: "alter nullability tasks.amount"}, {Source: "004_add_constraint_tasks_uq_tasks.sql", SQL: "ALTER TABLE `tasks` ADD CONSTRAINT `uq_tasks` UNIQUE (`id`);\n", ReverseSQL: "ALTER TABLE `tasks` DROP INDEX `uq_tasks`;\n", Summary: "replace constraint uq_tasks"}, {Source: "005_create_index_tasks_ix_tasks_amount.sql", SQL: "CREATE INDEX `ix_tasks_amount` ON `tasks` (`amount`);\n", ReverseSQL: "DROP INDEX `ix_tasks_amount` ON `tasks`;\n", Summary: "create index ix_tasks_amount"}}
	require.Equal(t, want, plan.Statements)
	require.Equal(t, []diff.PlannedStatement{want[1]}, plan.Operations[0].Forward)
	require.Equal(t, []diff.PlannedStatement{{Source: want[1].Source, SQL: want[1].ReverseSQL, ReverseSQL: want[1].SQL, Summary: want[1].Summary}}, plan.Operations[0].Reverse)
	require.Equal(t, []diff.PlannedStatement{want[2]}, plan.Operations[1].Forward)
	require.Equal(t, []diff.PlannedStatement{{Source: want[2].Source, SQL: want[2].ReverseSQL, ReverseSQL: want[2].SQL, Summary: want[2].Summary}}, plan.Operations[1].Reverse)
	require.Equal(t, []diff.PlannedStatement{want[4]}, plan.Operations[2].Forward)
	require.Equal(t, []diff.PlannedStatement{{Source: want[4].Source, SQL: want[4].ReverseSQL, ReverseSQL: want[4].SQL, Summary: want[4].Summary}}, plan.Operations[2].Reverse)
	require.Equal(t, []diff.PlannedStatement{want[0], want[3]}, plan.Operations[3].Forward)
	require.Equal(t, []diff.PlannedStatement{{Source: want[3].Source, SQL: want[3].ReverseSQL, ReverseSQL: want[3].SQL, Summary: want[3].Summary}, {Source: want[0].Source, SQL: want[0].ReverseSQL, ReverseSQL: want[0].SQL, Summary: want[0].Summary}}, plan.Operations[3].Reverse)
	statements := plan.Statements
	root := t.TempDir()
	directory := filepath.Join(root, "migrations", "001_schedule")
	require.NoError(t, diff.WriteMigration(directory, plan))
	loaded, err := migrationdir.Load(filepath.Dir(directory))
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	wantUpSources := []string{"001_drop_constraint_tasks_uq_tasks.up.sql", "002_add_column_tasks_due_on.up.sql", "003_alter_nullability_tasks_amount.up.sql", "004_add_constraint_tasks_uq_tasks.up.sql", "005_create_index_tasks_ix_tasks_amount.up.sql"}
	wantUpSQL := []string{want[0].SQL, want[1].SQL, want[2].SQL, want[3].SQL, want[4].SQL}
	wantDownSources := []string{"005_create_index_tasks_ix_tasks_amount.down.sql", "004_add_constraint_tasks_uq_tasks.down.sql", "003_alter_nullability_tasks_amount.down.sql", "002_add_column_tasks_due_on.down.sql", "001_drop_constraint_tasks_uq_tasks.down.sql"}
	wantDownSQL := []string{want[4].ReverseSQL, want[3].ReverseSQL, want[2].ReverseSQL, want[1].ReverseSQL, want[0].ReverseSQL}
	for i := range statements {
		require.Equal(t, wantUpSources[i], loaded[0].Statements[i].Source)
		require.Equal(t, wantUpSQL[i], string(loaded[0].Statements[i].SQL))
		require.Equal(t, wantDownSources[i], loaded[0].Down[i].Source)
		require.Equal(t, wantDownSQL[i], string(loaded[0].Down[i].SQL))
	}
	for _, index := range []int{1, 2, 4} {
		_, err := mysqlquery.ParseStatement(want[index].SQL)
		require.NoError(t, err)
	}
	require.Equal(t, []diff.PlannedStatement{statements[1]}, plan.Operations[0].Forward)
	require.Equal(t, []diff.PlannedStatement{diff.PlannedStatement{Source: statements[1].Source, SQL: statements[1].ReverseSQL, ReverseSQL: statements[1].SQL, Summary: statements[1].Summary}}, plan.Operations[0].Reverse)
	require.Equal(t, []diff.PlannedStatement{statements[2]}, plan.Operations[1].Forward)
	require.Equal(t, []diff.PlannedStatement{diff.PlannedStatement{Source: statements[2].Source, SQL: statements[2].ReverseSQL, ReverseSQL: statements[2].SQL, Summary: statements[2].Summary}}, plan.Operations[1].Reverse)
	require.Equal(t, []diff.PlannedStatement{statements[4]}, plan.Operations[2].Forward)
	require.Equal(t, []diff.PlannedStatement{diff.PlannedStatement{Source: statements[4].Source, SQL: statements[4].ReverseSQL, ReverseSQL: statements[4].SQL, Summary: statements[4].Summary}}, plan.Operations[2].Reverse)
	require.Equal(t, []diff.PlannedStatement{statements[0], statements[3]}, plan.Operations[3].Forward)
	require.Equal(t, []diff.PlannedStatement{diff.PlannedStatement{Source: statements[3].Source, SQL: statements[3].ReverseSQL, ReverseSQL: statements[3].SQL, Summary: statements[3].Summary}, diff.PlannedStatement{Source: statements[0].Source, SQL: statements[0].ReverseSQL, ReverseSQL: statements[0].SQL, Summary: statements[0].Summary}}, plan.Operations[3].Reverse)
	require.Equal(t, []string{"005_create_index_tasks_ix_tasks_amount.down.sql", "004_add_constraint_tasks_uq_tasks.down.sql", "003_alter_nullability_tasks_amount.down.sql", "002_add_column_tasks_due_on.down.sql", "001_drop_constraint_tasks_uq_tasks.down.sql"}, []string{loaded[0].Down[0].Source, loaded[0].Down[1].Source, loaded[0].Down[2].Source, loaded[0].Down[3].Source, loaded[0].Down[4].Source})
}

func TestSchemaEvolutionMySQLSchedulesOpaqueBackfillSeparately(t *testing.T) {
	a := mysql.New()
	from := parseMySQL(t, a, "CREATE TABLE `tasks` (`id` bigint NOT NULL, `code` varchar(20));")
	to := parseMySQL(t, a, "CREATE TABLE `tasks` (`id` bigint NOT NULL, `code` varchar(20), `email` varchar(255) NOT NULL);")
	plan, err := a.Diff(from, to)
	require.NoError(t, err)
	backfill := "UPDATE `tasks` SET `email` = CONCAT(`code`, '@example.test') WHERE `email` IS NULL;"
	resolved, err := plan.Resolve(diff.Resolution{DecisionID: plan.Decisions[0].ID, BackfillSQL: backfill})
	require.NoError(t, err)
	require.Len(t, resolved.Statements, 3)
	want := []diff.PlannedStatement{
		{Source: "001_add_column_tasks_email.sql", SQL: "ALTER TABLE `tasks` ADD COLUMN `email` varchar(255);\n", ReverseSQL: "ALTER TABLE `tasks` DROP COLUMN `email`;\n", Summary: "add column tasks.email"},
		{Source: "002_backfill_tasks_email.sql", SQL: backfill, Summary: "add column tasks.email"},
		{Source: "003_require_column_tasks_email.sql", SQL: "ALTER TABLE `tasks` MODIFY COLUMN `email` varchar(255) NOT NULL;\n", ReverseSQL: "ALTER TABLE `tasks` MODIFY COLUMN `email` varchar(255);\n", Summary: "add column tasks.email"},
	}
	require.Equal(t, want, resolved.Statements)
	require.Equal(t, want, resolved.Operations[0].Forward)
	require.Equal(t, "caller-supplied MySQL backfill has no inferred reverse", resolved.IrreversibleReason)
	require.Empty(t, resolved.Operations[0].Reverse)
	root := t.TempDir()
	directory := filepath.Join(root, "migrations", "001_backfill")
	require.NoError(t, diff.WriteMigration(directory, resolved))
	entries, err := migrationdir.Load(filepath.Dir(directory))
	require.NoError(t, err)
	require.Empty(t, entries[0].Down)
	marker, err := os.ReadFile(filepath.Join(directory, ".rasql-irreversible"))
	require.NoError(t, err)
	require.Equal(t, []byte("caller-supplied MySQL backfill has no inferred reverse\n"), marker)
	for i, statement := range want {
		require.Equal(t, statement.Source[:len(statement.Source)-4]+".up.sql", entries[0].Statements[i].Source)
		require.Equal(t, statement.SQL, string(entries[0].Statements[i].SQL))
	}
	quoted := "UPDATE `tasks` SET `email` = 'semi;colon' WHERE `email` IS NULL;"
	quotedPlan, err := plan.Resolve(diff.Resolution{DecisionID: plan.Decisions[0].ID, BackfillSQL: quoted})
	require.NoError(t, err)
	require.Equal(t, quoted, quotedPlan.Statements[1].SQL)
	_, err = plan.Resolve(diff.Resolution{DecisionID: plan.Decisions[0].ID, BackfillSQL: "UPDATE `tasks` SET `email` = 'one'; UPDATE `tasks` SET `email` = 'two';"})
	require.ErrorContains(t, err, "invalid MySQL SQL: multiple statements")
}

func TestSchemaEvolutionMySQLCreateTableDependencySchedule(t *testing.T) {
	a := mysql.New()
	from := parseMySQL(t, a, "CREATE TABLE `tasks` (`id` bigint NOT NULL);")
	to := parseMySQL(t, a, "CREATE TABLE `tasks` (`id` bigint NOT NULL); CREATE TABLE `z_child` (`id` bigint, `parent_id` bigint, CONSTRAINT `fk_parent` FOREIGN KEY (`parent_id`) REFERENCES `a_parent` (`id`)); CREATE TABLE `a_parent` (`id` bigint NOT NULL PRIMARY KEY);")
	plan, err := a.Diff(from, to)
	require.NoError(t, err)
	require.Len(t, plan.Statements, 2)
	want := []diff.PlannedStatement{
		{Source: "001_create_table_a_parent.sql", SQL: "CREATE TABLE `a_parent` (`id` bigint NOT NULL PRIMARY KEY);\n", ReverseSQL: "DROP TABLE `a_parent`;\n", Summary: "create table a_parent"},
		{Source: "002_create_table_z_child.sql", SQL: "CREATE TABLE `z_child` (`id` bigint, `parent_id` bigint, CONSTRAINT `fk_parent` FOREIGN KEY (`parent_id`) REFERENCES `a_parent` (`id`));\n", ReverseSQL: "DROP TABLE `z_child`;\n", Summary: "create table z_child"},
	}
	require.Equal(t, want, plan.Statements)
	require.Equal(t, []diff.PlannedStatement{want[0]}, plan.Operations[0].Forward)
	require.Equal(t, []diff.PlannedStatement{{Source: want[0].Source, SQL: want[0].ReverseSQL, ReverseSQL: want[0].SQL, Summary: want[0].Summary}}, plan.Operations[0].Reverse)
	require.Equal(t, []diff.PlannedStatement{want[1]}, plan.Operations[1].Forward)
	require.Equal(t, []diff.PlannedStatement{{Source: want[1].Source, SQL: want[1].ReverseSQL, ReverseSQL: want[1].SQL, Summary: want[1].Summary}}, plan.Operations[1].Reverse)
	root := t.TempDir()
	directory := filepath.Join(root, "migrations", "001_tables")
	require.NoError(t, diff.WriteMigration(directory, plan))
	entries, err := migrationdir.Load(filepath.Dir(directory))
	require.NoError(t, err)
	require.Equal(t, []string{"001_create_table_a_parent.up.sql", "002_create_table_z_child.up.sql"}, []string{entries[0].Statements[0].Source, entries[0].Statements[1].Source})
	require.Equal(t, []string{"CREATE TABLE `a_parent` (`id` bigint NOT NULL PRIMARY KEY);\n", "CREATE TABLE `z_child` (`id` bigint, `parent_id` bigint, CONSTRAINT `fk_parent` FOREIGN KEY (`parent_id`) REFERENCES `a_parent` (`id`));\n"}, []string{string(entries[0].Statements[0].SQL), string(entries[0].Statements[1].SQL)})
	require.Equal(t, "002_create_table_z_child.down.sql", entries[0].Down[0].Source)
	require.Equal(t, "DROP TABLE `z_child`;\n", string(entries[0].Down[0].SQL))
	require.Equal(t, "001_create_table_a_parent.down.sql", entries[0].Down[1].Source)
	require.Equal(t, "DROP TABLE `a_parent`;\n", string(entries[0].Down[1].SQL))
	for _, statement := range want {
		_, err := mysqlquery.ParseStatement(statement.SQL)
		require.NoError(t, err)
	}
}

func TestSchemaEvolutionMySQLForeignKeyCrossTableDependencySchedule(t *testing.T) {
	a := mysql.New()
	from := parseMySQL(t, a, "CREATE TABLE `parent` (`id` bigint NOT NULL PRIMARY KEY, `code` varchar(20) NULL, CONSTRAINT `uq_parent` UNIQUE (`code`)); CREATE TABLE `child` (`id` bigint, `parent_id` bigint, CONSTRAINT `fk_parent` FOREIGN KEY (`parent_id`) REFERENCES `parent` (`code`));")
	to := parseMySQL(t, a, "CREATE TABLE `parent` (`id` bigint NOT NULL PRIMARY KEY, `code` varchar(20) NOT NULL, CONSTRAINT `uq_parent` UNIQUE (`code`)); CREATE TABLE `child` (`id` bigint, `parent_id` bigint, CONSTRAINT `fk_parent` FOREIGN KEY (`parent_id`) REFERENCES `parent` (`id`));")
	plan, err := a.Diff(from, to)
	require.NoError(t, err)
	want := []diff.PlannedStatement{{Source: "001_drop_constraint_child_fk_parent.sql", SQL: "ALTER TABLE `child` DROP FOREIGN KEY `fk_parent`;\n", ReverseSQL: "ALTER TABLE `child` ADD CONSTRAINT `fk_parent` FOREIGN KEY (`parent_id`) REFERENCES `parent` (`code`);\n", Summary: "replace constraint fk_parent"}, {Source: "002_alter_nullability_parent_code.sql", SQL: "ALTER TABLE `parent` MODIFY COLUMN `code` varchar(20) NOT NULL;\n", ReverseSQL: "ALTER TABLE `parent` MODIFY COLUMN `code` varchar(20) NULL;\n", Summary: "alter nullability parent.code"}, {Source: "003_add_constraint_child_fk_parent.sql", SQL: "ALTER TABLE `child` ADD CONSTRAINT `fk_parent` FOREIGN KEY (`parent_id`) REFERENCES `parent` (`id`);\n", ReverseSQL: "ALTER TABLE `child` DROP FOREIGN KEY `fk_parent`;\n", Summary: "replace constraint fk_parent"}}
	require.Equal(t, want, plan.Statements)
	require.Equal(t, []string{"001_drop_constraint_child_fk_parent.sql", "002_alter_nullability_parent_code.sql", "003_add_constraint_child_fk_parent.sql"}, []string{plan.Statements[0].Source, plan.Statements[1].Source, plan.Statements[2].Source})
	require.Equal(t, "ALTER TABLE `child` DROP FOREIGN KEY `fk_parent`;\n", plan.Statements[0].SQL)
	require.Equal(t, "ALTER TABLE `parent` MODIFY COLUMN `code` varchar(20) NOT NULL;\n", plan.Statements[1].SQL)
	require.Equal(t, "ALTER TABLE `child` ADD CONSTRAINT `fk_parent` FOREIGN KEY (`parent_id`) REFERENCES `parent` (`id`);\n", plan.Statements[2].SQL)
	require.Equal(t, "ALTER TABLE `child` DROP FOREIGN KEY `fk_parent`;\n", plan.Statements[2].ReverseSQL)
	require.Equal(t, "ALTER TABLE `parent` MODIFY COLUMN `code` varchar(20) NULL;\n", plan.Statements[1].ReverseSQL)
	require.Equal(t, "ALTER TABLE `child` ADD CONSTRAINT `fk_parent` FOREIGN KEY (`parent_id`) REFERENCES `parent` (`code`);\n", plan.Statements[0].ReverseSQL)
}

func TestSchemaEvolutionMySQLForeignKeyUnqualifiedReferenceUsesOwnerNamespace(t *testing.T) {
	a := mysql.New()
	from := parseMySQL(t, a, "CREATE TABLE `app`.`parent` (`id` bigint NOT NULL PRIMARY KEY, `code` varchar(20) NULL, CONSTRAINT `uq_parent` UNIQUE (`code`)); CREATE TABLE `app`.`child` (`id` bigint, `parent_id` bigint, CONSTRAINT `fk_parent` FOREIGN KEY (`parent_id`) REFERENCES `parent` (`code`));")
	to := parseMySQL(t, a, "CREATE TABLE `app`.`parent` (`id` bigint NOT NULL PRIMARY KEY, `code` varchar(20) NOT NULL, CONSTRAINT `uq_parent` UNIQUE (`code`)); CREATE TABLE `app`.`child` (`id` bigint, `parent_id` bigint, CONSTRAINT `fk_parent` FOREIGN KEY (`parent_id`) REFERENCES `parent` (`id`));")
	plan, err := a.Diff(from, to)
	require.NoError(t, err)
	want := []diff.PlannedStatement{{Source: "001_drop_constraint_app_child_fk_parent.sql", SQL: "ALTER TABLE `app`.`child` DROP FOREIGN KEY `fk_parent`;\n", ReverseSQL: "ALTER TABLE `app`.`child` ADD CONSTRAINT `fk_parent` FOREIGN KEY (`parent_id`) REFERENCES `parent` (`code`);\n", Summary: "replace constraint fk_parent"}, {Source: "002_alter_nullability_app_parent_code.sql", SQL: "ALTER TABLE `app`.`parent` MODIFY COLUMN `code` varchar(20) NOT NULL;\n", ReverseSQL: "ALTER TABLE `app`.`parent` MODIFY COLUMN `code` varchar(20) NULL;\n", Summary: "alter nullability app.parent.code"}, {Source: "003_add_constraint_app_child_fk_parent.sql", SQL: "ALTER TABLE `app`.`child` ADD CONSTRAINT `fk_parent` FOREIGN KEY (`parent_id`) REFERENCES `parent` (`id`);\n", ReverseSQL: "ALTER TABLE `app`.`child` DROP FOREIGN KEY `fk_parent`;\n", Summary: "replace constraint fk_parent"}}
	require.Equal(t, want, plan.Statements)
	require.Equal(t, []diff.PlannedStatement{want[1]}, plan.Operations[0].Forward)
	require.Equal(t, []diff.PlannedStatement{{Source: "002_alter_nullability_app_parent_code.sql", SQL: "ALTER TABLE `app`.`parent` MODIFY COLUMN `code` varchar(20) NULL;\n", ReverseSQL: "ALTER TABLE `app`.`parent` MODIFY COLUMN `code` varchar(20) NOT NULL;\n", Summary: "alter nullability app.parent.code"}}, plan.Operations[0].Reverse)
	require.Equal(t, []diff.PlannedStatement{want[0], want[2]}, plan.Operations[1].Forward)
	require.Equal(t, []diff.PlannedStatement{{Source: "003_add_constraint_app_child_fk_parent.sql", SQL: "ALTER TABLE `app`.`child` DROP FOREIGN KEY `fk_parent`;\n", ReverseSQL: "ALTER TABLE `app`.`child` ADD CONSTRAINT `fk_parent` FOREIGN KEY (`parent_id`) REFERENCES `parent` (`id`);\n", Summary: "replace constraint fk_parent"}, {Source: "001_drop_constraint_app_child_fk_parent.sql", SQL: "ALTER TABLE `app`.`child` ADD CONSTRAINT `fk_parent` FOREIGN KEY (`parent_id`) REFERENCES `parent` (`code`);\n", ReverseSQL: "ALTER TABLE `app`.`child` DROP FOREIGN KEY `fk_parent`;\n", Summary: "replace constraint fk_parent"}}, plan.Operations[1].Reverse)
	root := t.TempDir()
	directory := filepath.Join(root, "migrations", "001_qualified")
	require.NoError(t, diff.WriteMigration(directory, plan))
	loaded, err := migrationdir.Load(filepath.Dir(directory))
	require.NoError(t, err)
	require.Equal(t, []string{"001_drop_constraint_app_child_fk_parent.up.sql", "002_alter_nullability_app_parent_code.up.sql", "003_add_constraint_app_child_fk_parent.up.sql"}, []string{loaded[0].Statements[0].Source, loaded[0].Statements[1].Source, loaded[0].Statements[2].Source})
	require.Equal(t, []string{"003_add_constraint_app_child_fk_parent.down.sql", "002_alter_nullability_app_parent_code.down.sql", "001_drop_constraint_app_child_fk_parent.down.sql"}, []string{loaded[0].Down[0].Source, loaded[0].Down[1].Source, loaded[0].Down[2].Source})
	require.Equal(t, want[2].ReverseSQL, string(loaded[0].Down[0].SQL))
	require.Equal(t, want[1].ReverseSQL, string(loaded[0].Down[1].SQL))
	require.Equal(t, want[0].ReverseSQL, string(loaded[0].Down[2].SQL))
	_, err = mysqlquery.ParseStatement(want[1].SQL)
	require.NoError(t, err)
}

func TestSchemaEvolutionMySQLCreateTableDependencyCycleRefusal(t *testing.T) {
	a := mysql.New()
	from := parseMySQL(t, a, "CREATE TABLE `tasks` (`id` bigint NOT NULL);")
	to := parseMySQL(t, a, "CREATE TABLE `b_table` (`id` bigint NOT NULL, `a_id` bigint, CONSTRAINT `fk_a` FOREIGN KEY (`a_id`) REFERENCES `a_table` (`id`)); CREATE TABLE `a_table` (`id` bigint NOT NULL, `b_id` bigint, CONSTRAINT `fk_b` FOREIGN KEY (`b_id`) REFERENCES `b_table` (`id`)); CREATE TABLE `tasks` (`id` bigint NOT NULL);")
	plan, err := a.Diff(from, to)
	require.ErrorContains(t, err, "mysql schema diff: dependency cycle involving")
	require.Contains(t, err.Error(), "0/0/7:a_table//0, 1/0/7:b_table//0")
	require.Empty(t, plan.Statements)
}
