//go:build unix

package mysql_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/internal/migrationdir"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/lestrrat-go/rasql/migrate/diff/mysql"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
)

type mysqlColumnCatalog struct {
	Name, Type, Default, Nullable, Extra, Generated, Collation string
}

type mysqlConstraintCatalog struct {
	Name, Kind string
}

type mysqlKeyCatalog struct {
	Constraint, Column, ReferencedTable, ReferencedColumn string
}

type mysqlReferenceCatalog struct {
	Constraint, Match, Update, Delete string
}

type mysqlEvolutionFixture struct {
	Root      string
	Plan      diff.Plan
	Migration migrate.Migration
}

func TestSchemaEvolutionMySQLConstraintReplacementMatrix(t *testing.T) {
	tests := []struct {
		name        string
		baseline    string
		target      string
		want        mysqlConstraintCatalog
		wantKey     mysqlKeyCatalog
		baseKey     mysqlKeyCatalog
		wantRef     mysqlReferenceCatalog
		baseRef     mysqlReferenceCatalog
		check       string
		checkClause string
		baseClause  string
	}{
		{
			name:     "primary",
			baseline: "CREATE TABLE `evo_primary` (`id` BIGINT NOT NULL, `code` BIGINT NOT NULL, CONSTRAINT `pk_evo` PRIMARY KEY (`id`));",
			target:   "CREATE TABLE `evo_primary` (`id` BIGINT NOT NULL, `code` BIGINT NOT NULL, CONSTRAINT `pk_evo` PRIMARY KEY (`code`));",
			want:     mysqlConstraintCatalog{Name: "PRIMARY", Kind: "PRIMARY KEY"},
			wantKey:  mysqlKeyCatalog{Constraint: "PRIMARY", Column: "code"},
			baseKey:  mysqlKeyCatalog{Constraint: "PRIMARY", Column: "id"},
		},
		{
			name:     "unique",
			baseline: "CREATE TABLE `evo_unique` (`id` BIGINT NOT NULL, `code` VARCHAR(32) NOT NULL, `label` VARCHAR(32) NOT NULL, CONSTRAINT `uq_evo` UNIQUE (`code`));",
			target:   "CREATE TABLE `evo_unique` (`id` BIGINT NOT NULL, `code` VARCHAR(32) NOT NULL, `label` VARCHAR(32) NOT NULL, CONSTRAINT `uq_evo` UNIQUE (`label`));",
			want:     mysqlConstraintCatalog{Name: "uq_evo", Kind: "UNIQUE"},
			wantKey:  mysqlKeyCatalog{Constraint: "uq_evo", Column: "label"},
			baseKey:  mysqlKeyCatalog{Constraint: "uq_evo", Column: "code"},
		},
		{
			name:     "foreign key",
			baseline: "CREATE TABLE `evo_parent` (`id` BIGINT NOT NULL, `code` BIGINT NOT NULL, CONSTRAINT `uq_parent_code` UNIQUE (`code`)); CREATE TABLE `evo_fk` (`id` BIGINT NOT NULL, `parent_id` BIGINT NOT NULL, CONSTRAINT `fk_evo` FOREIGN KEY (`parent_id`) REFERENCES `evo_parent` (`id`));",
			target:   "CREATE TABLE `evo_parent` (`id` BIGINT NOT NULL, `code` BIGINT NOT NULL, CONSTRAINT `uq_parent_code` UNIQUE (`code`)); CREATE TABLE `evo_fk` (`id` BIGINT NOT NULL, `parent_id` BIGINT NOT NULL, CONSTRAINT `fk_evo` FOREIGN KEY (`parent_id`) REFERENCES `evo_parent` (`code`));",
			want:     mysqlConstraintCatalog{Name: "fk_evo", Kind: "FOREIGN KEY"},
			wantKey:  mysqlKeyCatalog{Constraint: "fk_evo", Column: "parent_id", ReferencedTable: "evo_parent", ReferencedColumn: "code"},
			baseKey:  mysqlKeyCatalog{Constraint: "fk_evo", Column: "parent_id", ReferencedTable: "evo_parent", ReferencedColumn: "id"},
			wantRef:  mysqlReferenceCatalog{Constraint: "fk_evo", Match: "NONE", Update: "RESTRICT", Delete: "RESTRICT"},
			baseRef:  mysqlReferenceCatalog{Constraint: "fk_evo", Match: "NONE", Update: "RESTRICT", Delete: "RESTRICT"},
		},
		{
			name:        "check",
			baseline:    "CREATE TABLE `evo_check` (`id` BIGINT NOT NULL, `quantity` INT NOT NULL, CONSTRAINT `ck_evo` CHECK (`quantity` > 0));",
			target:      "CREATE TABLE `evo_check` (`id` BIGINT NOT NULL, `quantity` INT NOT NULL, CONSTRAINT `ck_evo` CHECK (`quantity` >= 0));",
			want:        mysqlConstraintCatalog{Name: "ck_evo", Kind: "CHECK"},
			wantKey:     mysqlKeyCatalog{Constraint: "ck_evo", Column: ""},
			check:       "ck_evo",
			checkClause: "`quantity` >= 0",
			baseClause:  "`quantity` > 0",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := buildMySQLSchemaEvolutionFixture(t, test.baseline, test.target)
			require.Empty(t, fixture.Plan.Decisions)
			require.True(t, fixture.Plan.Executable())
			require.Len(t, fixture.Migration.Statements, len(fixture.Plan.Statements))
			for index, statement := range fixture.Plan.Statements {
				require.Equal(t, statement.Source[:len(statement.Source)-4]+".up.sql", fixture.Migration.Statements[index].Source)
				require.Equal(t, statement.SQL, string(fixture.Migration.Statements[index].SQL))
			}
			database := dbtest.MySQLDB(t)
			_, err := database.ExecContext(t.Context(), test.baseline)
			require.NoError(t, err)
			if test.name == "foreign key" {
				_, err = database.ExecContext(t.Context(), "INSERT INTO `evo_parent` VALUES (1, 101);")
				require.NoError(t, err)
			}
			seed := map[string]string{
				"primary":     "INSERT INTO `evo_primary` VALUES (1, 101);",
				"unique":      "INSERT INTO `evo_unique` VALUES (1, 'code', 'label');",
				"foreign key": "INSERT INTO `evo_fk` VALUES (1, 1);",
				"check":       "INSERT INTO `evo_check` VALUES (1, 1);",
			}[test.name]
			_, err = database.ExecContext(t.Context(), seed)
			require.NoError(t, err)
			migration := applyMySQLMigration(t, database, fixture.Migration)
			assertMySQLConstraint(t, database, "evo_"+map[string]string{"primary": "primary", "unique": "unique", "foreign key": "fk", "check": "check"}[test.name], test.want, test.wantKey, test.wantRef, test.check, test.checkClause)
			assertSeededConstraintRow(t, database, test.name)
			runner, err := migrate.New(database, dialect.MySQL())
			require.NoError(t, err)
			_, err = runner.Revert(t.Context(), migrate.Steps(1), migration)
			require.NoError(t, err)
			assertMySQLConstraint(t, database, "evo_"+map[string]string{"primary": "primary", "unique": "unique", "foreign key": "fk", "check": "check"}[test.name], test.want, test.baseKey, test.baseRef, test.check, test.baseClause)
			assertSeededConstraintRow(t, database, test.name)
		})
	}
}

func TestSchemaEvolutionMySQLArtifactRoundTrip(t *testing.T) {
	baseline := "CREATE TABLE `evo_order` (`id` BIGINT NOT NULL, `code` VARCHAR(32) NOT NULL, `quantity` INT NULL, `parent_id` BIGINT NULL, CONSTRAINT `uq_order` UNIQUE (`code`), CONSTRAINT `fk_order` FOREIGN KEY (`parent_id`) REFERENCES `evo_order_parent` (`id`));"
	target := "CREATE TABLE `evo_order` (`id` BIGINT NOT NULL, `code` VARCHAR(32) NOT NULL, `quantity` INT NOT NULL, `parent_id` BIGINT NULL, `due_on` DATETIME(6) NULL DEFAULT NULL, CONSTRAINT `uq_order` UNIQUE (`id`), CONSTRAINT `fk_order` FOREIGN KEY (`parent_id`) REFERENCES `evo_order_parent` (`id`)); CREATE INDEX `ix_order_quantity` ON `evo_order` (`quantity`);"
	fixture := buildMySQLSchemaEvolutionFixture(t, baseline, target)
	plan := fixture.Plan
	var err error
	originalPublic, err := json.Marshal(struct {
		Operations []diff.ProposedOperation
		Decisions  []diff.RequiredDecision
		Statements []diff.PlannedStatement
	}{plan.Operations, plan.Decisions, plan.Statements})
	require.NoError(t, err)
	require.Equal(t, []string{"add_column_mysql_evo_order_due_on", "alter_nullability_mysql_evo_order_quantity", "replace_constraint_mysql_evo_order_ix_order_quantity", "replace_constraint_mysql_evo_order_uq_order"}, operationIDs(plan.Operations))
	require.Equal(t, []diff.OperationKind{diff.OperationAddColumn, diff.OperationAlterNullability, diff.OperationReplaceConstraint, diff.OperationReplaceConstraint}, operationKinds(plan.Operations))
	require.Equal(t, []string{"evo_order", "evo_order", "evo_order", "evo_order"}, operationTables(plan.Operations))
	require.Equal(t, []string{"due_on", "quantity", "", ""}, operationColumns(plan.Operations))
	require.Equal(t, []string{"", "", "ix_order_quantity", "uq_order"}, operationConstraints(plan.Operations))
	require.Equal(t, []string{"add column evo_order.due_on", "alter nullability evo_order.quantity", "create index ix_order_quantity", "replace constraint uq_order"}, operationSummaries(plan.Operations))
	require.Empty(t, plan.Decisions)
	require.Equal(t, []string{"001_drop_constraint_evo_order_uq_order.sql", "002_add_column_evo_order_due_on.sql", "003_alter_nullability_evo_order_quantity.sql", "004_add_constraint_evo_order_uq_order.sql", "005_create_index_evo_order_ix_order_quantity.sql"}, statementSources(plan.Statements))
	want := []diff.PlannedStatement{
		{Source: "001_drop_constraint_evo_order_uq_order.sql", SQL: "ALTER TABLE `evo_order` DROP INDEX `uq_order`;\n", ReverseSQL: "ALTER TABLE `evo_order` ADD CONSTRAINT `uq_order` UNIQUE (`code`);\n", Summary: "replace constraint uq_order"},
		{Source: "002_add_column_evo_order_due_on.sql", SQL: "ALTER TABLE `evo_order` ADD COLUMN `due_on` datetime(6) NULL DEFAULT NULL;\n", ReverseSQL: "ALTER TABLE `evo_order` DROP COLUMN `due_on`;\n", Summary: "add column evo_order.due_on"},
		{Source: "003_alter_nullability_evo_order_quantity.sql", SQL: "ALTER TABLE `evo_order` MODIFY COLUMN `quantity` int NOT NULL;\n", ReverseSQL: "ALTER TABLE `evo_order` MODIFY COLUMN `quantity` int NULL;\n", Summary: "alter nullability evo_order.quantity"},
		{Source: "004_add_constraint_evo_order_uq_order.sql", SQL: "ALTER TABLE `evo_order` ADD CONSTRAINT `uq_order` UNIQUE (`id`);\n", ReverseSQL: "ALTER TABLE `evo_order` DROP INDEX `uq_order`;\n", Summary: "replace constraint uq_order"},
		{Source: "005_create_index_evo_order_ix_order_quantity.sql", SQL: "CREATE INDEX `ix_order_quantity` ON `evo_order` (`quantity`);\n", ReverseSQL: "DROP INDEX `ix_order_quantity` ON `evo_order`;\n", Summary: "create index ix_order_quantity"},
	}
	require.Equal(t, want, plan.Statements)
	disposable, err := plan.Resolve()
	require.NoError(t, err)
	currentPublic, err := json.Marshal(struct {
		Operations []diff.ProposedOperation
		Decisions  []diff.RequiredDecision
		Statements []diff.PlannedStatement
	}{plan.Operations, plan.Decisions, plan.Statements})
	require.NoError(t, err)
	require.Equal(t, originalPublic, currentPublic)
	require.True(t, disposable.Executable())
	require.Equal(t, []diff.PlannedStatement{want[1]}, plan.Operations[0].Forward)
	require.Equal(t, []diff.PlannedStatement{want[2]}, plan.Operations[1].Forward)
	require.Equal(t, []diff.PlannedStatement{want[4]}, plan.Operations[2].Forward)
	require.Equal(t, []diff.PlannedStatement{want[0], want[3]}, plan.Operations[3].Forward)
	require.Equal(t, []diff.PlannedStatement{{Source: want[3].Source, SQL: want[3].ReverseSQL, ReverseSQL: want[3].SQL, Summary: want[3].Summary}, {Source: want[0].Source, SQL: want[0].ReverseSQL, ReverseSQL: want[0].SQL, Summary: want[0].Summary}}, plan.Operations[3].Reverse)
	disposable.Operations[0].Forward[0].SQL = "mutated copy"
	disposable.Statements[0].SQL = "mutated statement"
	currentPublic, err = json.Marshal(struct {
		Operations []diff.ProposedOperation
		Decisions  []diff.RequiredDecision
		Statements []diff.PlannedStatement
	}{plan.Operations, plan.Decisions, plan.Statements})
	require.NoError(t, err)
	require.Equal(t, originalPublic, currentPublic)
	resolved, err := plan.Resolve()
	require.NoError(t, err)
	require.Equal(t, []diff.PlannedStatement{want[1]}, resolved.Operations[0].Forward)
	require.Equal(t, []diff.PlannedStatement{want[2]}, resolved.Operations[1].Forward)
	require.Equal(t, []diff.PlannedStatement{want[4]}, resolved.Operations[2].Forward)
	require.Equal(t, []diff.PlannedStatement{want[0], want[3]}, resolved.Operations[3].Forward)
	loaded := []migrate.Migration{fixture.Migration}
	require.Len(t, loaded, 1)
	wantUp := make([]migrate.Statement, len(resolved.Statements))
	wantDown := make([]migrate.Statement, len(resolved.Statements))
	for index, statement := range resolved.Statements {
		stem := statement.Source[:len(statement.Source)-4]
		wantUp[index] = migrate.Statement{Source: stem + ".up.sql", SQL: sqltext.Text(statement.SQL)}
		reverseIndex := len(resolved.Statements) - index - 1
		wantDown[reverseIndex] = migrate.Statement{Source: stem + ".down.sql", SQL: sqltext.Text(statement.ReverseSQL)}
	}
	require.Equal(t, wantUp, loaded[0].Statements)
	require.Equal(t, wantDown, loaded[0].Down)
	database := dbtest.MySQLDB(t)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE `evo_order_parent` (`id` BIGINT NOT NULL PRIMARY KEY);")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "INSERT INTO `evo_order_parent` VALUES (1);")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE `evo_order` (`id` BIGINT NOT NULL, `code` VARCHAR(32) NOT NULL, `quantity` INT NULL, `parent_id` BIGINT NULL, CONSTRAINT `uq_order` UNIQUE (`code`), CONSTRAINT `fk_order` FOREIGN KEY (`parent_id`) REFERENCES `evo_order_parent` (`id`));")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "INSERT INTO `evo_order` VALUES (1, 'one', 2, 1);")
	require.NoError(t, err)
	runner, err := migrate.New(database, dialect.MySQL())
	require.NoError(t, err)
	_, err = runner.Apply(t.Context(), migrate.AllPending(), loaded...)
	require.NoError(t, err)
	assertMySQLColumnFacts(t, database, "evo_order", "due_on")
	assertMySQLConstraint(t, database, "evo_order", mysqlConstraintCatalog{Name: "uq_order", Kind: "UNIQUE"}, mysqlKeyCatalog{Constraint: "uq_order", Column: "id"}, mysqlReferenceCatalog{}, "", "")
	assertMySQLConstraint(t, database, "evo_order", mysqlConstraintCatalog{Name: "fk_order", Kind: "FOREIGN KEY"}, mysqlKeyCatalog{Constraint: "fk_order", Column: "parent_id", ReferencedTable: "evo_order_parent", ReferencedColumn: "id"}, mysqlReferenceCatalog{Constraint: "fk_order", Match: "NONE", Update: "RESTRICT", Delete: "RESTRICT"}, "", "")
	require.True(t, liveIndexExists(t, database, "evo_order", "ix_order_quantity"))
	var quantity int
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT quantity FROM `evo_order` WHERE id = 1").Scan(&quantity))
	require.Equal(t, 2, quantity)
	_, err = runner.Revert(t.Context(), migrate.Steps(1), loaded...)
	require.NoError(t, err)
	assertMySQLConstraint(t, database, "evo_order", mysqlConstraintCatalog{Name: "uq_order", Kind: "UNIQUE"}, mysqlKeyCatalog{Constraint: "uq_order", Column: "code"}, mysqlReferenceCatalog{}, "", "")
	assertMySQLConstraint(t, database, "evo_order", mysqlConstraintCatalog{Name: "fk_order", Kind: "FOREIGN KEY"}, mysqlKeyCatalog{Constraint: "fk_order", Column: "parent_id", ReferencedTable: "evo_order_parent", ReferencedColumn: "id"}, mysqlReferenceCatalog{Constraint: "fk_order", Match: "NONE", Update: "RESTRICT", Delete: "RESTRICT"}, "", "")
	require.False(t, liveIndexExists(t, database, "evo_order", "ix_order_quantity"))
	var code string
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT code FROM `evo_order` WHERE id = 1").Scan(&code))
	require.Equal(t, "one", code)
	require.False(t, liveColumnExists(t, database, "evo_order", "due_on"))
}

func TestSchemaEvolutionMySQLBackfillIsIrreversibleLive(t *testing.T) {
	baseline := "CREATE TABLE `evo_backfill` (`id` BIGINT NOT NULL PRIMARY KEY);"
	target := "CREATE TABLE `evo_backfill` (`id` BIGINT NOT NULL PRIMARY KEY, `state` VARCHAR(20) NOT NULL DEFAULT NULL);"
	resolution := diff.Resolution{DecisionID: "backfill_mysql_evo_backfill_state", BackfillSQL: "UPDATE `evo_backfill` SET `state` = 'open';"}
	fixture := buildMySQLSchemaEvolutionFixture(t, baseline, target, resolution)
	plan := fixture.Plan
	_, err := plan.Resolve(resolution)
	require.NoError(t, err)
	require.Equal(t, []diff.ProposedOperation{{ID: "add_column_mysql_evo_backfill_state", Table: "evo_backfill", Column: "state", Summary: "add column evo_backfill.state", Kind: diff.OperationAddColumn}}, plan.Operations)
	require.Equal(t, []diff.RequiredDecision{{ID: "backfill_mysql_evo_backfill_state", Table: "evo_backfill", Column: "state", Target: "state", Reason: "required column needs an application-specific backfill", Kind: diff.DecisionBackfill}}, plan.Decisions)
	require.Nil(t, plan.Statements)
	require.False(t, plan.Executable())
	originalPublic, err := json.Marshal(struct {
		Operations []diff.ProposedOperation
		Decisions  []diff.RequiredDecision
		Statements []diff.PlannedStatement
	}{plan.Operations, plan.Decisions, plan.Statements})
	require.NoError(t, err)
	unresolvedRoot := t.TempDir()
	writeErr := diff.WriteMigration(filepath.Join(unresolvedRoot, "001_unresolved"), plan)
	require.ErrorContains(t, writeErr, "unresolved decisions")
	_, statErr := os.Stat(filepath.Join(unresolvedRoot, "001_unresolved"))
	require.ErrorIs(t, statErr, os.ErrNotExist)
	disposable, err := plan.Resolve(resolution)
	require.NoError(t, err)
	disposable.Operations[0].Forward[0].SQL = "mutated copy"
	disposable.Statements[0].SQL = "mutated statement"
	disposable.Decisions = append(disposable.Decisions, diff.RequiredDecision{ID: "mutated"})
	currentPublic, err := json.Marshal(struct {
		Operations []diff.ProposedOperation
		Decisions  []diff.RequiredDecision
		Statements []diff.PlannedStatement
	}{plan.Operations, plan.Decisions, plan.Statements})
	require.NoError(t, err)
	require.Equal(t, originalPublic, currentPublic)
	var resolved diff.Plan
	resolved, err = plan.Resolve(resolution)
	require.NoError(t, err)
	assertOperationStatementSlices(t, resolved.Operations, resolved.Statements)
	require.Equal(t, "caller-supplied MySQL backfill has no inferred reverse", resolved.IrreversibleReason)
	require.Equal(t, fixture.Migration.Statements, migrationStatementsFromPlan(resolved, false))
	require.Empty(t, fixture.Migration.Down)
	marker, err := os.ReadFile(filepath.Join(fixture.Root, "migrations", "001_schema_evolution", ".rasql-irreversible"))
	require.NoError(t, err)
	require.Equal(t, []byte("caller-supplied MySQL backfill has no inferred reverse\n"), marker)
	database := dbtest.MySQLDB(t)
	_, err = database.ExecContext(t.Context(), baseline)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "INSERT INTO `evo_backfill` VALUES (1);")
	require.NoError(t, err)
	migration := applyMySQLMigration(t, database, fixture.Migration)
	var state string
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT state FROM `evo_backfill` WHERE id = 1").Scan(&state))
	require.Equal(t, "open", state)
	runner, err := migrate.New(database, dialect.MySQL())
	require.NoError(t, err)
	_, err = runner.Revert(t.Context(), migrate.Steps(1), migration)
	require.ErrorContains(t, err, "irreversible")
}

func TestSchemaEvolutionMySQLFullMetadataLive(t *testing.T) {
	baseline := "CREATE TABLE `evo_metadata` (`id` BIGINT NOT NULL AUTO_INCREMENT, `quantity` INT NOT NULL, `price` DECIMAL(10,2) NOT NULL, `code` VARCHAR(64) COLLATE `utf8mb4_bin` NOT NULL DEFAULT 'new', `total` DECIMAL(12,2) GENERATED ALWAYS AS (`quantity` * `price`) STORED, PRIMARY KEY (`id`));"
	target := "CREATE TABLE `evo_metadata` (`id` BIGINT NOT NULL AUTO_INCREMENT, `quantity` INT NOT NULL, `price` DECIMAL(10,2) NOT NULL, `code` VARCHAR(64) COLLATE `utf8mb4_bin` NULL DEFAULT 'new', `total` DECIMAL(12,2) GENERATED ALWAYS AS (`quantity` * `price`) STORED, PRIMARY KEY (`id`));"
	fixture := buildMySQLSchemaEvolutionFixture(t, baseline, target)
	plan := fixture.Plan
	require.Len(t, plan.Statements, 1)
	require.Equal(t, "001_alter_nullability_evo_metadata_code.sql", plan.Statements[0].Source)
	require.Equal(t, "ALTER TABLE `evo_metadata` MODIFY COLUMN `code` varchar(64) COLLATE `utf8mb4_bin` NULL DEFAULT 'new';\n", plan.Statements[0].SQL)
	require.Equal(t, "ALTER TABLE `evo_metadata` MODIFY COLUMN `code` varchar(64) COLLATE `utf8mb4_bin` NOT NULL DEFAULT 'new';\n", plan.Statements[0].ReverseSQL)
	database := dbtest.MySQLDB(t)
	_, err := database.ExecContext(t.Context(), baseline)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "INSERT INTO `evo_metadata` (quantity, price, code) VALUES (2, 3.50, 'new');")
	require.NoError(t, err)
	migration := applyMySQLMigration(t, database, fixture.Migration)
	assertMySQLMetadata(t, database, "YES")
	runner, err := migrate.New(database, dialect.MySQL())
	require.NoError(t, err)
	_, err = runner.Revert(t.Context(), migrate.Steps(1), migration)
	require.NoError(t, err)
	assertMySQLMetadata(t, database, "NO")
}

func applyMySQLMigration(t *testing.T, database *sql.DB, migration migrate.Migration) migrate.Migration {
	t.Helper()
	runner, err := migrate.New(database, dialect.MySQL())
	require.NoError(t, err)
	_, err = runner.Apply(t.Context(), migrate.AllPending(), migration)
	require.NoError(t, err)
	return migration
}

func buildMySQLSchemaEvolutionFixture(t *testing.T, baseline, target string, resolutions ...diff.Resolution) mysqlEvolutionFixture {
	t.Helper()
	root := writeMySQLSchemaPair(t, baseline, target)
	analyzer := mysql.New()
	from, err := diff.LoadSources(filepath.Join(root, "from"))
	require.NoError(t, err)
	to, err := diff.LoadSources(filepath.Join(root, "to"))
	require.NoError(t, err)
	plan, err := analyzer.Diff(mustMySQLParse(t, analyzer, from), mustMySQLParse(t, analyzer, to))
	require.NoError(t, err)
	resolved, err := plan.Resolve(resolutions...)
	require.NoError(t, err)
	require.NoError(t, diff.WriteMigration(migrationPath(root), resolved))
	loaded, err := migrationdir.Load(filepath.Join(root, "migrations"))
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	return mysqlEvolutionFixture{Root: root, Plan: plan, Migration: loaded[0]}
}

func migrationStatementsFromPlan(plan diff.Plan, reverse bool) []migrate.Statement {
	statements := make([]migrate.Statement, len(plan.Statements))
	for index, statement := range plan.Statements {
		stem := statement.Source[:len(statement.Source)-4]
		if reverse {
			reverseIndex := len(plan.Statements) - index - 1
			statements[reverseIndex] = migrate.Statement{Source: stem + ".down.sql", SQL: sqltext.Text(statement.ReverseSQL)}
			continue
		}
		statements[index] = migrate.Statement{Source: stem + ".up.sql", SQL: sqltext.Text(statement.SQL)}
	}
	return statements
}

func migrationPath(root string) string {
	return filepath.Join(root, "migrations", "001_schema_evolution")
}

func liveColumnExists(t *testing.T, database *sql.DB, table, column string) bool {
	t.Helper()
	var count int
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?", table, column).Scan(&count))
	return count == 1
}

func liveIndexExists(t *testing.T, database *sql.DB, table, index string) bool {
	t.Helper()
	var count int
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?", table, index).Scan(&count))
	return count > 0
}

func writeMySQLSchemaPair(t *testing.T, baseline, target string) string {
	t.Helper()
	root := t.TempDir()
	for name, source := range map[string]string{"from": baseline, "to": target} {
		directory := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(directory, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(directory, "schema.sql"), []byte(source), 0o600))
	}
	return root
}

func mustMySQLParse(t *testing.T, analyzer mysql.Analyzer, sources []diff.Source) diff.Snapshot {
	t.Helper()
	snapshot, err := analyzer.Parse(sources)
	require.NoError(t, err)
	return snapshot
}

func operationIDs(operations []diff.ProposedOperation) []string {
	result := make([]string, len(operations))
	for index := range operations {
		result[index] = operations[index].ID
	}
	return result
}

func operationKinds(operations []diff.ProposedOperation) []diff.OperationKind {
	result := make([]diff.OperationKind, len(operations))
	for index := range operations {
		result[index] = operations[index].Kind
	}
	return result
}

func operationTables(operations []diff.ProposedOperation) []string {
	result := make([]string, len(operations))
	for index := range operations {
		result[index] = operations[index].Table
	}
	return result
}

func operationColumns(operations []diff.ProposedOperation) []string {
	result := make([]string, len(operations))
	for index := range operations {
		result[index] = operations[index].Column
	}
	return result
}

func operationConstraints(operations []diff.ProposedOperation) []string {
	result := make([]string, len(operations))
	for index := range operations {
		result[index] = operations[index].Constraint
	}
	return result
}

func operationSummaries(operations []diff.ProposedOperation) []string {
	result := make([]string, len(operations))
	for index := range operations {
		result[index] = operations[index].Summary
	}
	return result
}

func statementSources(statements []diff.PlannedStatement) []string {
	result := make([]string, len(statements))
	for index := range statements {
		result[index] = statements[index].Source
	}
	return result
}

func assertOperationStatementSlices(t *testing.T, operations []diff.ProposedOperation, statements []diff.PlannedStatement) {
	t.Helper()
	if len(operations) == 1 && len(statements) > 1 {
		require.Equal(t, statements, operations[0].Forward)
		require.Empty(t, operations[0].Reverse)
		return
	}
	require.Len(t, operations, len(statements))
	for index, operation := range operations {
		require.Equal(t, []diff.PlannedStatement{statements[index]}, operation.Forward)
		require.Equal(t, []diff.PlannedStatement{{
			Source: statements[index].Source, SQL: statements[index].ReverseSQL,
			ReverseSQL: statements[index].SQL, Summary: statements[index].Summary,
		}}, operation.Reverse)
	}
}

func assertMySQLConstraint(t *testing.T, database *sql.DB, table string, want mysqlConstraintCatalog, key mysqlKeyCatalog, reference mysqlReferenceCatalog, check, clause string) {
	t.Helper()
	var got mysqlConstraintCatalog
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT constraint_name, constraint_type FROM information_schema.table_constraints WHERE table_schema = DATABASE() AND table_name = ? AND constraint_name = ?", table, want.Name).Scan(&got.Name, &got.Kind))
	require.Equal(t, want, got)
	if key.Column != "" {
		var gotKey mysqlKeyCatalog
		if key.ReferencedTable == "" {
			require.NoError(t, database.QueryRowContext(t.Context(), "SELECT constraint_name, column_name FROM information_schema.key_column_usage WHERE table_schema = DATABASE() AND constraint_name = ? ORDER BY ordinal_position LIMIT 1", key.Constraint).Scan(&gotKey.Constraint, &gotKey.Column))
		} else {
			require.NoError(t, database.QueryRowContext(t.Context(), "SELECT constraint_name, column_name, referenced_table_name, referenced_column_name FROM information_schema.key_column_usage WHERE table_schema = DATABASE() AND constraint_name = ? ORDER BY ordinal_position LIMIT 1", key.Constraint).Scan(&gotKey.Constraint, &gotKey.Column, &gotKey.ReferencedTable, &gotKey.ReferencedColumn))
		}
		require.Equal(t, key, gotKey)
	}
	if reference.Constraint != "" {
		var gotRef mysqlReferenceCatalog
		require.NoError(t, database.QueryRowContext(t.Context(), "SELECT constraint_name, match_option, update_rule, delete_rule FROM information_schema.referential_constraints WHERE constraint_schema = DATABASE() AND constraint_name = ?", reference.Constraint).Scan(&gotRef.Constraint, &gotRef.Match, &gotRef.Update, &gotRef.Delete))
		require.Equal(t, reference, gotRef)
	}
	if check != "" {
		var gotClause string
		require.NoError(t, database.QueryRowContext(t.Context(), "SELECT check_clause FROM information_schema.check_constraints WHERE constraint_schema = DATABASE() AND constraint_name = ?", check).Scan(&gotClause))
		require.Contains(t, gotClause, clause)
	}
}

func assertSeededConstraintRow(t *testing.T, database *sql.DB, name string) {
	t.Helper()
	queries := map[string]string{
		"primary":     "SELECT COUNT(*) FROM `evo_primary` WHERE id = 1 AND code = 101",
		"unique":      "SELECT COUNT(*) FROM `evo_unique` WHERE id = 1 AND code = 'code' AND label = 'label'",
		"foreign key": "SELECT COUNT(*) FROM `evo_fk` WHERE id = 1 AND parent_id = 1",
		"check":       "SELECT COUNT(*) FROM `evo_check` WHERE id = 1 AND quantity = 1",
	}
	var count int
	require.NoError(t, database.QueryRowContext(t.Context(), queries[name]).Scan(&count))
	require.Equal(t, 1, count)
}

func assertMySQLColumnFacts(t *testing.T, database *sql.DB, table, column string) {
	t.Helper()
	if table == "" {
		table, column = "evo_order", "due_on"
	}
	var got mysqlColumnCatalog
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT column_name, column_type, COALESCE(column_default, ''), is_nullable, extra, generation_expression, COALESCE(collation_name, '') FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?", table, column).Scan(&got.Name, &got.Type, &got.Default, &got.Nullable, &got.Extra, &got.Generated, &got.Collation))
	require.Equal(t, column, got.Name)
	require.Equal(t, "YES", got.Nullable)
	require.Contains(t, got.Type, "datetime")
}

func assertMySQLMetadata(t *testing.T, database *sql.DB, nullable string) {
	t.Helper()
	rows, err := database.QueryContext(t.Context(), "SELECT column_name, column_type, COALESCE(column_default, ''), is_nullable, extra, generation_expression, COALESCE(collation_name, '') FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'evo_metadata' ORDER BY ordinal_position")
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	got := make([]mysqlColumnCatalog, 0, 5)
	for rows.Next() {
		var column mysqlColumnCatalog
		require.NoError(t, rows.Scan(&column.Name, &column.Type, &column.Default, &column.Nullable, &column.Extra, &column.Generated, &column.Collation))
		got = append(got, column)
	}
	require.NoError(t, rows.Err())
	require.Len(t, got, 5)
	require.Equal(t, "id", got[0].Name)
	require.Contains(t, got[0].Type, "bigint")
	require.Contains(t, got[0].Extra, "auto_increment")
	require.Equal(t, "quantity", got[1].Name)
	require.Equal(t, "price", got[2].Name)
	require.Equal(t, "code", got[3].Name)
	require.Equal(t, nullable, got[3].Nullable)
	require.Contains(t, got[3].Type, "varchar(64)")
	require.Equal(t, "new", got[3].Default)
	require.Equal(t, "utf8mb4_bin", got[3].Collation)
	require.Equal(t, "total", got[4].Name)
	require.Contains(t, got[4].Extra, "STORED GENERATED")
	require.Contains(t, got[4].Generated, "quantity")
}

func Example_mysqlLiveFixtureCoverage() {
	fmt.Println("MySQL schema evolution live fixtures use dbtest.MySQLDB")
	// Output: MySQL schema evolution live fixtures use dbtest.MySQLDB
}
