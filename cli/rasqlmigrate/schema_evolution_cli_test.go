package rasqlmigrate

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/internal/migrationdir"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/lestrrat-go/rasql/migrate/diff/sqlite"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
)

func TestSchemaEvolutionCLIPreviewsOperationsAndDecisions(t *testing.T) {
	for _, test := range []struct {
		dialect string
		id      string
		kind    string
		summary string
	}{
		{dialect: "postgresql", id: "add_column_postgresql_members_due_on", kind: "add_column", summary: "add column members.due_on"},
		{dialect: "mysql", id: "add_column_mysql_members_due_on", kind: "add_column", summary: "add column members.due_on"},
		{dialect: "sqlite", id: "rebuild_table_sqlite_members", kind: "rebuild_table", summary: "rebuild table members"},
	} {
		t.Run(test.dialect, func(t *testing.T) {
			baseline, target := schemaEvolutionSources(t, test.dialect)
			output := setCommandOutput(t)
			err := run([]string{"diff", "-dialect", test.dialect, "-from", baseline, "-to", target})
			require.NoError(t, err)
			want := "-- operation " + test.id + " (" + test.kind + "): " + test.summary + "\n"
			if test.dialect != "sqlite" {
				want += "-- operation add_column_" + test.dialect + "_members_email (add_column): add column members.email\n"
			}
			want += "-- decision backfill_" + test.dialect + "_members_email (backfill): required column needs an application-specific backfill\n"
			require.Equal(t, want, output.String())
		})
	}
}

func TestSchemaEvolutionCLIRefusesUnresolvedOutputWithoutCreatingPaths(t *testing.T) {
	for _, dialect := range []string{"postgresql", "mysql", "sqlite"} {
		t.Run(dialect, func(t *testing.T) {
			baseline, target := schemaEvolutionSources(t, dialect)
			parent := filepath.Join(t.TempDir(), "new-parent")
			output := filepath.Join(parent, "002_schema_evolution")
			setCommandOutput(t)
			err := run([]string{"diff", "-dialect", dialect, "-from", baseline, "-to", target, "-output", output})
			require.ErrorContains(t, err, "backfill_"+dialect+"_members_email")
			_, statErr := os.Stat(parent)
			require.ErrorIs(t, statErr, os.ErrNotExist)
			matches, globErr := filepath.Glob(filepath.Join(filepath.Dir(output), ".002_schema_evolution.tmp-*"))
			require.NoError(t, globErr)
			require.Empty(t, matches)
		})
	}
}

func TestSchemaEvolutionCLIDiffRejectsResolutionWhenThereAreNoChanges(t *testing.T) {
	baseline := filepath.Join(t.TempDir(), "baseline")
	target := filepath.Join(t.TempDir(), "target")
	source := "CREATE TABLE members (id INTEGER PRIMARY KEY, name TEXT NOT NULL);\n"
	writeTestSchema(t, baseline, "tables/members.sql", source)
	writeTestSchema(t, target, "tables/members.sql", source)
	backfill := filepath.Join(t.TempDir(), "backfill.sql")
	require.NoError(t, os.WriteFile(backfill, []byte("UPDATE members SET email = name;\n"), 0o600))
	output := setCommandOutput(t)
	err := run([]string{"diff", "-dialect", "sqlite", "-from", baseline, "-to", target, "-backfill", "unknown=" + backfill})
	require.ErrorContains(t, err, "expected one resolution")
	require.Empty(t, output.String())
}

func TestSchemaEvolutionCLIPublishesResolvedBackfillArtifacts(t *testing.T) {
	for _, test := range []struct {
		dialect  string
		backfill string
		id       string
		want     []cliArtifact
	}{
		{
			dialect:  "postgresql",
			backfill: "UPDATE members SET email = name || '@example.test' WHERE email IS NULL;\n",
			id:       "backfill_postgresql_members_email",
			want: []cliArtifact{
				{source: "001_add_column_members_due_on.up.sql", sql: "ALTER TABLE members ADD COLUMN due_on text NULL;\n"},
				{source: "002_add_column_members_email.up.sql", sql: "ALTER TABLE members ADD COLUMN email text;\nUPDATE members SET email = name || '@example.test' WHERE email IS NULL;\nALTER TABLE members ALTER COLUMN email SET NOT NULL;\n"},
			},
		},
		{
			dialect:  "mysql",
			backfill: "UPDATE members SET email = CONCAT(name, '@example.test') WHERE email IS NULL;\n",
			id:       "backfill_mysql_members_email",
			want: []cliArtifact{
				{source: "001_add_column_members_due_on.up.sql", sql: "ALTER TABLE `members` ADD COLUMN `due_on` text NULL;\n"},
				{source: "002_add_column_members_email.up.sql", sql: "ALTER TABLE `members` ADD COLUMN `email` text;\n"},
				{source: "003_backfill_members_email.up.sql", sql: "UPDATE members SET email = CONCAT(name, '@example.test') WHERE email IS NULL;\n"},
				{source: "004_require_column_members_email.up.sql", sql: "ALTER TABLE `members` MODIFY COLUMN `email` text NOT NULL;\n"},
			},
		},
	} {
		t.Run(test.dialect, func(t *testing.T) {
			baseline, target := schemaEvolutionSources(t, test.dialect)
			backfill := filepath.Join(t.TempDir(), "email.sql")
			require.NoError(t, os.WriteFile(backfill, []byte(test.backfill), 0o600))
			root := t.TempDir()
			output := filepath.Join(root, "002_schema_evolution")
			setCommandOutput(t)
			require.NoError(t, run([]string{"diff", "-dialect", test.dialect, "-from", baseline, "-to", target, "-backfill", test.id + "=" + backfill, "-output", output}))

			migrations, err := migrationdir.Load(root)
			require.NoError(t, err)
			require.Len(t, migrations, 1)
			migration := migrations[0]
			require.Equal(t, "002_schema_evolution", migration.ID)
			require.Empty(t, migration.Down)
			require.Len(t, migration.Statements, len(test.want))
			for index, want := range test.want {
				require.Equal(t, want.source, migration.Statements[index].Source)
				require.Equal(t, want.sql, string(migration.Statements[index].SQL))
			}
			allSQL := make([]string, len(migration.Statements))
			for index, statement := range migration.Statements {
				allSQL[index] = string(statement.SQL)
			}
			require.Equal(t, 1, strings.Count(strings.Join(allSQL, ""), strings.TrimSpace(test.backfill)))
			_, statErr := os.Stat(filepath.Join(output, ".rasql-irreversible"))
			require.ErrorIs(t, statErr, os.ErrNotExist, "an irreversible plan writes no marker file")
		})
	}
}

func TestSchemaEvolutionCLIOrdersRepeatedBackfillsByDependency(t *testing.T) {
	baseline := filepath.Join(t.TempDir(), "baseline")
	target := filepath.Join(t.TempDir(), "target")
	writeTestSchema(t, baseline, "tables/members.sql", "CREATE TABLE members (id BIGINT PRIMARY KEY, name TEXT NOT NULL);\n")
	writeTestSchema(t, target, "tables/members.sql", "CREATE TABLE members (id BIGINT PRIMARY KEY, name TEXT NOT NULL, email TEXT NOT NULL, status TEXT NOT NULL);\n")
	files := t.TempDir()
	emailSQL := "UPDATE members SET email = CONCAT(name, '@example.test') WHERE email IS NULL;\n"
	statusSQL := "UPDATE members SET status = 'new' WHERE status IS NULL;\n"
	emailFile := filepath.Join(files, "email.sql")
	statusFile := filepath.Join(files, "status.sql")
	require.NoError(t, os.WriteFile(emailFile, []byte(emailSQL), 0o600))
	require.NoError(t, os.WriteFile(statusFile, []byte(statusSQL), 0o600))
	root := t.TempDir()
	output := filepath.Join(root, "002_schema_evolution")
	setCommandOutput(t)
	require.NoError(t, run([]string{
		"diff", "-dialect", "mysql", "-from", baseline, "-to", target,
		"-backfill", "backfill_mysql_members_email=" + emailFile,
		"-backfill", "backfill_mysql_members_status=" + statusFile,
		"-output", output,
	}))
	migrations, err := migrationdir.Load(root)
	require.NoError(t, err)
	require.Len(t, migrations, 1)
	require.Equal(t, []string{
		"001_add_column_members_email.up.sql",
		"002_add_column_members_status.up.sql",
		"003_backfill_members_email.up.sql",
		"004_backfill_members_status.up.sql",
		"005_require_column_members_email.up.sql",
		"006_require_column_members_status.up.sql",
	}, cliSources(migrations[0].Statements))
	require.Equal(t, emailSQL, string(migrations[0].Statements[2].SQL))
	require.Equal(t, statusSQL, string(migrations[0].Statements[3].SQL))
	allSQL := make([]string, len(migrations[0].Statements))
	for index, statement := range migrations[0].Statements {
		allSQL[index] = string(statement.SQL)
	}
	joinedSQL := strings.Join(allSQL, "")
	require.Equal(t, 1, strings.Count(joinedSQL, strings.TrimSpace(emailSQL)))
	require.Equal(t, 1, strings.Count(joinedSQL, strings.TrimSpace(statusSQL)))
}

func TestSchemaEvolutionCLIResolutionFailures(t *testing.T) {
	for _, dialect := range []string{"postgresql", "mysql"} {
		t.Run(dialect, func(t *testing.T) {
			baseline, target := schemaEvolutionTwoRequiredSources(t, dialect)
			files := t.TempDir()
			valid := filepath.Join(files, "valid.sql")
			status := filepath.Join(files, "status.sql")
			blank := filepath.Join(files, "blank.sql")
			multiple := filepath.Join(files, "multiple.sql")
			require.NoError(t, os.WriteFile(valid, []byte("UPDATE members SET email = name WHERE email IS NULL;\n"), 0o600))
			require.NoError(t, os.WriteFile(status, []byte("UPDATE members SET status = name WHERE status IS NULL;\n"), 0o600))
			require.NoError(t, os.WriteFile(blank, []byte(" \n"), 0o600))
			require.NoError(t, os.WriteFile(multiple, []byte("UPDATE members SET email = name; UPDATE members SET email = 'x';\n"), 0o600))
			backfillID := "backfill_" + dialect + "_members_email"
			invalidDialect := "PostgreSQL"
			if dialect == "mysql" {
				invalidDialect = "MySQL"
			}
			cases := []struct {
				name string
				args []string
				want []string
			}{
				{name: "malformed", args: []string{"-backfill", "malformed"}, want: []string{"diff -backfill requires decision-id=value"}},
				{name: "unreadable", args: []string{"-backfill", backfillID + "=" + filepath.Join(files, "missing.sql")}, want: []string{"read -backfill"}},
				{name: "blank", args: []string{"-backfill", backfillID + "=" + blank, "-backfill", "backfill_" + dialect + "_members_status=" + status}, want: []string{"resolve -backfill", backfillID, "is empty"}},
				{name: "unknown", args: []string{"-backfill", "backfill_" + dialect + "_members_unknown=" + valid, "-backfill", "backfill_" + dialect + "_members_status=" + status}, want: []string{`unknown decision "backfill_`}},
				{name: "missing one of two", args: []string{"-backfill", backfillID + "=" + valid}, want: []string{"expected one resolution"}},
				{name: "duplicate within flag", args: []string{"-backfill", backfillID + "=" + valid, "-backfill", backfillID + "=" + valid}, want: []string{`duplicate resolution for "` + backfillID + `"`}},
				{name: "duplicate across kinds", args: []string{"-backfill", backfillID + "=" + valid, "-rename", backfillID + "=name"}, want: []string{`duplicate resolution for "` + backfillID + `"`}},
				{name: "wrong kind", args: []string{"-rename", backfillID + "=name", "-backfill", "backfill_" + dialect + "_members_status=" + status}, want: []string{"backfill resolution"}},
				{name: "multiple native statements", args: []string{"-backfill", backfillID + "=" + multiple, "-backfill", "backfill_" + dialect + "_members_status=" + status}, want: []string{"resolve -backfill", backfillID, "invalid " + invalidDialect + " SQL: multiple statements"}},
			}
			for _, test := range cases {
				t.Run(test.name, func(t *testing.T) {
					parent := filepath.Join(t.TempDir(), "new-parent")
					args := append([]string{"diff", "-dialect", dialect, "-from", baseline, "-to", target}, test.args...)
					args = append(args, "-output", filepath.Join(parent, "002_schema_evolution"))
					setCommandOutput(t)
					err := run(args)
					for _, want := range test.want {
						require.ErrorContains(t, err, want)
					}
					assertNoCLIOutputPath(t, parent, "002_schema_evolution")
				})
			}
		})
	}
}

func TestSchemaEvolutionCLIResolutionFailuresDiffLiveSQLite(t *testing.T) {
	cases := []struct {
		name string
		args func(files string) []string
		want []string
	}{
		{name: "malformed", args: func(string) []string { return []string{"-backfill", "malformed"} }, want: []string{"diff -backfill requires decision-id=value"}},
		{name: "unreadable", args: func(files string) []string {
			return []string{"-backfill", "backfill_sqlite_members_email=" + filepath.Join(files, "missing.sql"), "-backfill", "backfill_sqlite_members_status=" + filepath.Join(files, "status.sql")}
		}, want: []string{"read -backfill", "backfill_sqlite_members_email"}},
		{name: "blank", args: func(files string) []string {
			return []string{"-backfill", "backfill_sqlite_members_email=" + filepath.Join(files, "blank.sql"), "-backfill", "backfill_sqlite_members_status=" + filepath.Join(files, "status.sql")}
		}, want: []string{"resolve -backfill", "backfill_sqlite_members_email", "is empty"}},
		{name: "multiple native statements", args: func(files string) []string {
			return []string{"-backfill", "backfill_sqlite_members_email=" + filepath.Join(files, "multiple.sql"), "-backfill", "backfill_sqlite_members_status=" + filepath.Join(files, "status.sql")}
		}, want: []string{"resolve -backfill", "backfill_sqlite_members_email", "invalid SQLite SQL: multiple statements"}},
		{name: "duplicate within backfill", args: func(files string) []string {
			return []string{"-backfill", "backfill_sqlite_members_email=" + filepath.Join(files, "valid-email.sql"), "-backfill", "backfill_sqlite_members_email=" + filepath.Join(files, "valid-email.sql")}
		}, want: []string{`duplicate resolution for "backfill_sqlite_members_email"`}},
		{name: "duplicate across kinds", args: func(files string) []string {
			return []string{"-backfill", "backfill_sqlite_members_email=" + filepath.Join(files, "valid-email.sql"), "-rename", "backfill_sqlite_members_email=name"}
		}, want: []string{`duplicate resolution for "backfill_sqlite_members_email"`}},
		{name: "wrong kind", args: func(files string) []string {
			return []string{"-rename", "backfill_sqlite_members_email=name", "-backfill", "backfill_sqlite_members_status=" + filepath.Join(files, "status.sql")}
		}, want: []string{`backfill resolution "backfill_sqlite_members_email" is empty`}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			dsn := filepath.Join(t.TempDir(), "application.db")
			database, err := openDatabase("sqlite", dsn)
			require.NoError(t, err)
			_, err = database.ExecContext(t.Context(), "CREATE TABLE members (id INTEGER PRIMARY KEY, name TEXT NOT NULL)")
			require.NoError(t, err)
			require.NoError(t, database.Close())
			target := filepath.Join(t.TempDir(), "target")
			writeTestSchema(t, target, "tables/members.sql", "CREATE TABLE members (id INTEGER PRIMARY KEY, name TEXT NOT NULL, email TEXT NOT NULL, status TEXT NOT NULL);\n")
			files := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(files, "valid-email.sql"), []byte("UPDATE members SET email = name WHERE email IS NULL;\n"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(files, "status.sql"), []byte("UPDATE members SET status = name WHERE status IS NULL;\n"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(files, "blank.sql"), []byte(" \n"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(files, "multiple.sql"), []byte("UPDATE members SET email = name; UPDATE members SET email = 'x';\n"), 0o600))
			parent := filepath.Join(t.TempDir(), "new-parent")
			args := append([]string{"diff-live", "-dialect", "sqlite", "-dsn", dsn, "-table", "members", "-to", target}, test.args(files)...)
			args = append(args, "-output", filepath.Join(parent, "002_schema_evolution"))
			setCommandOutput(t)
			err = run(args)
			require.Error(t, err)
			for _, want := range test.want {
				require.ErrorContains(t, err, want)
			}
			require.NotContains(t, err.Error(), dsn)
			assertNoCLIOutputPath(t, parent, "002_schema_evolution")
		})
	}
}

func TestSchemaEvolutionCLIRenameResolutionFailures(t *testing.T) {
	for _, dialect := range []string{"postgresql", "mysql"} {
		t.Run(dialect, func(t *testing.T) {
			baseline := filepath.Join(t.TempDir(), "baseline")
			target := filepath.Join(t.TempDir(), "target")
			writeTestSchema(t, baseline, "tables/members.sql", "CREATE TABLE members (id BIGINT PRIMARY KEY, old_name TEXT);\n")
			writeTestSchema(t, baseline, "tables/profiles.sql", "CREATE TABLE profiles (id BIGINT PRIMARY KEY, old_label TEXT);\n")
			writeTestSchema(t, target, "tables/members.sql", "CREATE TABLE members (id BIGINT PRIMARY KEY, new_name TEXT);\n")
			writeTestSchema(t, target, "tables/profiles.sql", "CREATE TABLE profiles (id BIGINT PRIMARY KEY, new_label TEXT);\n")
			renameID := "rename_" + dialect + "_members_new_name"
			secondRenameID := "rename_" + dialect + "_profiles_new_label"
			files := t.TempDir()
			valid := filepath.Join(files, "valid.sql")
			require.NoError(t, os.WriteFile(valid, []byte("UPDATE members SET old_name = old_name;\n"), 0o600))
			cases := []struct {
				name string
				args []string
				want string
			}{
				{name: "blank rename", args: []string{"-rename", renameID + "="}, want: "diff -rename requires decision-id=value"},
				{name: "invalid rename source", args: []string{"-rename", renameID + "=missing_name", "-rename", secondRenameID + "=old_label"}, want: `non-candidate column "missing_name"`},
				{name: "duplicate rename", args: []string{"-rename", renameID + "=old_name", "-rename", renameID + "=old_name"}, want: `duplicate resolution for "` + renameID + `"`},
				{name: "cross-kind duplicate rename ID", args: []string{"-rename", renameID + "=old_name", "-backfill", renameID + "=" + valid}, want: `duplicate resolution for "` + renameID + `"`},
				{name: "wrong kind for rename", args: []string{"-backfill", renameID + "=" + valid, "-rename", secondRenameID + "=old_label"}, want: `rename resolution "` + renameID + `" is empty`},
			}
			for _, test := range cases {
				t.Run(test.name, func(t *testing.T) {
					parent := filepath.Join(t.TempDir(), "new-parent")
					args := append([]string{"diff", "-dialect", dialect, "-from", baseline, "-to", target}, test.args...)
					args = append(args, "-output", filepath.Join(parent, "002_schema_evolution"))
					setCommandOutput(t)
					err := run(args)
					require.ErrorContains(t, err, test.want)
					assertNoCLIOutputPath(t, parent, "002_schema_evolution")
				})
			}
		})
	}
}

func TestSchemaEvolutionCLIRenameResolutionFailuresSQLite(t *testing.T) {
	cases := []struct {
		name string
		args func(files string) []string
		want []string
	}{
		{name: "blank rename", args: func(string) []string { return []string{"-rename", "rename_sqlite_members_new_name="} }, want: []string{"diff -rename requires decision-id=value"}},
		{name: "invalid rename source", args: func(files string) []string {
			return []string{"-rename", "rename_sqlite_members_new_name=missing_name", "-backfill", "backfill_sqlite_members_email=" + filepath.Join(files, "valid-email.sql")}
		}, want: []string{"rename_sqlite_members_new_name", `non-candidate column "missing_name"`}},
		{name: "duplicate rename", args: func(string) []string {
			return []string{"-rename", "rename_sqlite_members_new_name=old_name", "-rename", "rename_sqlite_members_new_name=old_name"}
		}, want: []string{`duplicate resolution for "rename_sqlite_members_new_name"`}},
		{name: "cross-kind duplicate rename ID", args: func(files string) []string {
			return []string{"-rename", "rename_sqlite_members_new_name=old_name", "-backfill", "rename_sqlite_members_new_name=" + filepath.Join(files, "valid-email.sql")}
		}, want: []string{`duplicate resolution for "rename_sqlite_members_new_name"`}},
		{name: "wrong kind for rename", args: func(files string) []string {
			return []string{"-backfill", "rename_sqlite_members_new_name=" + filepath.Join(files, "valid-email.sql"), "-backfill", "backfill_sqlite_members_email=" + filepath.Join(files, "valid-email.sql")}
		}, want: []string{`rename resolution "rename_sqlite_members_new_name" is empty`}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			dsn := filepath.Join(t.TempDir(), "application.db")
			database, err := openDatabase("sqlite", dsn)
			require.NoError(t, err)
			_, err = database.ExecContext(t.Context(), "CREATE TABLE members (id INTEGER PRIMARY KEY, name TEXT NOT NULL, old_name TEXT)")
			require.NoError(t, err)
			require.NoError(t, database.Close())
			target := filepath.Join(t.TempDir(), "target")
			writeTestSchema(t, target, "tables/members.sql", "CREATE TABLE members (id INTEGER PRIMARY KEY, name TEXT NOT NULL, new_name TEXT, email TEXT NOT NULL);\n")
			files := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(files, "valid-email.sql"), []byte("UPDATE members SET email = name WHERE email IS NULL;\n"), 0o600))
			parent := filepath.Join(t.TempDir(), "new-parent")
			args := append([]string{"diff-live", "-dialect", "sqlite", "-dsn", dsn, "-table", "members", "-to", target}, test.args(files)...)
			args = append(args, "-output", filepath.Join(parent, "002_schema_evolution"))
			setCommandOutput(t)
			err = run(args)
			require.Error(t, err)
			for _, want := range test.want {
				require.ErrorContains(t, err, want)
			}
			require.NotContains(t, err.Error(), dsn)
			assertNoCLIOutputPath(t, parent, "002_schema_evolution")
		})
	}
}

func TestSchemaEvolutionCLILiveCatalogAttachmentRoutesByDialect(t *testing.T) {
	previousInspect := inspectSQLiteLiveCatalog
	called := 0
	inspectSQLiteLiveCatalog = func(ctx context.Context, transaction interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	}, tableName string) (sqlite.LiveCatalogFacts, error) {
		called++
		return previousInspect(ctx, transaction, tableName)
	}
	t.Cleanup(func() { inspectSQLiteLiveCatalog = previousInspect })

	dsn := filepath.Join(t.TempDir(), "application.db")
	database, err := openDatabase("sqlite", dsn)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE members (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	require.NoError(t, database.Close())
	target := filepath.Join(t.TempDir(), "target")
	writeTestSchema(t, target, "tables/members.sql", "CREATE TABLE members (id INTEGER PRIMARY KEY);\n")
	setCommandOutput(t)
	require.NoError(t, run([]string{"diff-live", "-dialect", "sqlite", "-dsn", dsn, "-table", "members", "-to", target}))
	require.Equal(t, 1, called)
}

func TestSchemaEvolutionCLILiveCatalogAttachmentSkipsOtherDialects(t *testing.T) {
	previousInspect := inspectSQLiteLiveCatalog
	called := 0
	inspectSQLiteLiveCatalog = func(ctx context.Context, queryer interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	}, tableName string) (sqlite.LiveCatalogFacts, error) {
		called++
		return previousInspect(ctx, queryer, tableName)
	}
	t.Cleanup(func() { inspectSQLiteLiveCatalog = previousInspect })

	for _, dialectName := range []string{"postgresql", "mysql"} {
		t.Run(dialectName, func(t *testing.T) {
			analyzer, err := schemaAnalyzer(dialectName)
			require.NoError(t, err)
			baseline, err := analyzer.Parse([]diff.Source{{Path: "members.sql", SQL: sqltext.Text([]byte("CREATE TABLE members (id BIGINT PRIMARY KEY);"))}})
			require.NoError(t, err)
			attached, err := attachSQLiteLiveCatalog(t.Context(), nil, analyzer, baseline, "members")
			require.NoError(t, err)
			require.Same(t, baseline, attached)
		})
	}
	require.Zero(t, called)
}

func TestSchemaEvolutionCLIRenamePublication(t *testing.T) {
	for _, dialect := range []string{"postgresql", "mysql"} {
		t.Run(dialect, func(t *testing.T) {
			baseline := filepath.Join(t.TempDir(), "baseline")
			target := filepath.Join(t.TempDir(), "target")
			writeTestSchema(t, baseline, "tables/members.sql", "CREATE TABLE members (name TEXT);\n")
			writeTestSchema(t, target, "tables/members.sql", "CREATE TABLE members (display_name TEXT);\n")
			root := t.TempDir()
			output := filepath.Join(root, "002_schema_evolution")
			preview := setCommandOutput(t)
			require.NoError(t, run([]string{"diff", "-dialect", dialect, "-from", baseline, "-to", target}))
			summary := "rename column members.display_name"
			if dialect == "mysql" {
				summary = "add column members.display_name"
			}
			require.Equal(t, "-- operation add_column_"+dialect+"_members_display_name (add_column): "+summary+"\n-- decision rename_"+dialect+"_members_display_name (rename): column rename requires caller confirmation\n", preview.String())
			preview.Reset()
			require.NoError(t, run([]string{"diff", "-dialect", dialect, "-from", baseline, "-to", target, "-rename", "rename_" + dialect + "_members_display_name=name", "-output", output}))
			migrations, err := migrationdir.Load(root)
			require.NoError(t, err)
			require.Len(t, migrations, 1)
			require.Len(t, migrations[0].Down, 1)
			wantSource := "001_rename_column_members"
			wantUp := "ALTER TABLE members RENAME COLUMN name TO display_name;\n"
			wantDown := "ALTER TABLE members RENAME COLUMN display_name TO name;\n"
			if dialect == "mysql" {
				wantSource = "001_rename_column_members_display_name"
				wantUp = "ALTER TABLE `members` RENAME COLUMN `name` TO `display_name`;\n"
				wantDown = "ALTER TABLE `members` RENAME COLUMN `display_name` TO `name`;\n"
			}
			require.Equal(t, []string{wantSource + ".up.sql"}, cliSources(migrations[0].Statements))
			require.Equal(t, wantUp, string(migrations[0].Statements[0].SQL))
			require.Equal(t, []string{wantSource + ".down.sql"}, cliSources(migrations[0].Down))
			require.Equal(t, wantDown, string(migrations[0].Down[0].SQL))
			for _, statement := range append(migrations[0].Statements, migrations[0].Down...) {
				require.Equal(t, string(statement.SQL), string(mustReadCLIFile(t, filepath.Join(output, statement.Source))))
			}
		})
	}
}

func TestSchemaEvolutionCLIRenamePublicationSQLiteLive(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "application.db")
	database, err := openDatabase("sqlite", dsn)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE members (name TEXT)")
	require.NoError(t, err)
	require.NoError(t, database.Close())
	target := filepath.Join(t.TempDir(), "target")
	writeTestSchema(t, target, "tables/members.sql", "CREATE TABLE members (display_name TEXT);\n")
	root := t.TempDir()
	output := filepath.Join(root, "002_schema_evolution")
	preview := setCommandOutput(t)
	require.NoError(t, run([]string{"diff-live", "-dialect", "sqlite", "-dsn", dsn, "-table", "members", "-to", target}))
	require.Equal(t, "-- operation rebuild_table_sqlite_members (rebuild_table): rebuild table members\n-- decision rename_sqlite_members_display_name (rename): column rename requires caller confirmation\n", preview.String())
	preview.Reset()
	require.NoError(t, run([]string{"diff-live", "-dialect", "sqlite", "-dsn", dsn, "-table", "members", "-to", target, "-rename", "rename_sqlite_members_display_name=name", "-output", output}))
	migrations, err := migrationdir.Load(root)
	require.NoError(t, err)
	require.Len(t, migrations, 1)
	require.Equal(t, []string{"001_2_create_rebuild.up.sql", "002_3_copy_rebuild.up.sql", "003_4_drop_table.up.sql", "004_5_rename_rebuild.up.sql"}, cliSources(migrations[0].Statements))
	require.Equal(t, []string{
		"CREATE TABLE members__rasql_rebuild (display_name text);\n\n",
		"INSERT INTO members__rasql_rebuild (display_name) SELECT name FROM members;\n",
		"DROP TABLE members;\n",
		"ALTER TABLE members__rasql_rebuild RENAME TO members;\n",
	}, cliSQL(migrations[0].Statements))
	require.Equal(t, []string{"004_5_rename_rebuild.down.sql", "003_4_drop_table.down.sql", "002_3_copy_rebuild.down.sql", "001_2_create_rebuild.down.sql"}, cliSources(migrations[0].Down))
	require.Equal(t, []string{
		"CREATE TABLE \"main\".members__rasql_rebuild_2 (\"name\" text);\n\n",
		"INSERT INTO \"main\".members__rasql_rebuild_2 (\"name\") SELECT display_name FROM members;\n",
		"DROP TABLE members;\n",
		"ALTER TABLE \"main\".members__rasql_rebuild_2 RENAME TO \"members\";\n",
	}, cliSQL(migrations[0].Down))
	for _, statement := range append(migrations[0].Statements, migrations[0].Down...) {
		require.Equal(t, string(statement.SQL), string(mustReadCLIFile(t, filepath.Join(output, statement.Source))))
	}
}

func TestSchemaEvolutionCLIDiffLiveSQLiteBackfillPublication(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "application.db")
	database, err := openDatabase("sqlite", dsn)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE members (id INTEGER PRIMARY KEY, name TEXT NOT NULL); INSERT INTO members (name) VALUES ('Ada')")
	require.NoError(t, err)
	require.NoError(t, database.Close())
	target := filepath.Join(t.TempDir(), "target")
	writeTestSchema(t, target, "tables/members.sql", "CREATE TABLE members (id INTEGER PRIMARY KEY, name TEXT NOT NULL, due_on TEXT NULL, email TEXT NOT NULL);\n")
	backfillSQL := "UPDATE members SET email = name || '@example.test' WHERE email IS NULL;\n"
	backfill := filepath.Join(t.TempDir(), "email.sql")
	require.NoError(t, os.WriteFile(backfill, []byte(backfillSQL), 0o600))
	preview := setCommandOutput(t)
	require.NoError(t, run([]string{"diff-live", "-dialect", "sqlite", "-dsn", dsn, "-table", "members", "-to", target}))
	require.Equal(t, "-- operation rebuild_table_sqlite_members (rebuild_table): rebuild table members\n-- decision backfill_sqlite_members_email (backfill): required column needs an application-specific backfill\n", preview.String())
	preview.Reset()

	root := t.TempDir()
	output := filepath.Join(root, "002_schema_evolution")
	require.NoError(t, run([]string{"diff-live", "-dialect", "sqlite", "-dsn", dsn, "-table", "members", "-to", target, "-backfill", "backfill_sqlite_members_email=" + backfill, "-output", output}))
	migrations, err := migrationdir.Load(root)
	require.NoError(t, err)
	require.Len(t, migrations, 1)
	migration := migrations[0]
	require.Len(t, migration.Statements, 6)
	require.Equal(t, []string{
		"001_0_stage_email.up.sql",
		"002_1_backfill_email.up.sql",
		"003_2_create_rebuild.up.sql",
		"004_3_copy_rebuild.up.sql",
		"005_4_drop_table.up.sql",
		"006_5_rename_rebuild.up.sql",
	}, cliSources(migration.Statements))
	require.Equal(t, []string{
		"ALTER TABLE members ADD COLUMN email text;\n",
		backfillSQL,
		"CREATE TABLE members__rasql_rebuild (id integer PRIMARY KEY, name text NOT NULL, due_on text NULL, email text NOT NULL);\n\n",
		"INSERT INTO members__rasql_rebuild (due_on, email, id, name) SELECT NULL, email, \"id\", \"name\" FROM members;\n",
		"DROP TABLE members;\n",
		"ALTER TABLE members__rasql_rebuild RENAME TO members;\n",
	}, cliSQL(migration.Statements))
	require.Equal(t, backfillSQL, string(migration.Statements[1].SQL))
	require.Len(t, migration.Down, 6)
	require.Equal(t, []string{
		"006_5_rename_rebuild.down.sql",
		"005_4_drop_table.down.sql",
		"004_3_copy_rebuild.down.sql",
		"003_2_create_rebuild.down.sql",
		"002_1_backfill_email.down.sql",
		"001_0_stage_email.down.sql",
	}, cliSources(migration.Down))
	require.Equal(t, []string{
		"CREATE TABLE \"main\".members__rasql_rebuild_2 (\"id\" integer NOT NULL, \"name\" text NOT NULL, PRIMARY KEY (\"id\"));\n\n",
		"INSERT INTO \"main\".members__rasql_rebuild_2 (\"id\", \"name\") SELECT id, name FROM members;\n",
		"DROP TABLE members;\n",
		"ALTER TABLE \"main\".members__rasql_rebuild_2 RENAME TO \"members\";\n",
		"-- backfill is removed by the structural rebuild\n",
		"-- staging column is removed by the structural rebuild\n",
	}, cliSQL(migration.Down))
	for _, statement := range append(migration.Statements, migration.Down...) {
		require.Equal(t, string(statement.SQL), string(mustReadCLIFile(t, filepath.Join(output, statement.Source))))
	}
}

func TestSchemaEvolutionCLIDiffLiveSQLiteRefusesRebuildDependencies(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "application.db")
	database, err := openDatabase("sqlite", dsn)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE members (name TEXT); CREATE VIEW member_names AS SELECT name FROM members; CREATE TRIGGER member_names_insert INSTEAD OF INSERT ON member_names BEGIN INSERT INTO members(name) VALUES (NEW.name); END")
	require.NoError(t, err)
	require.NoError(t, database.Close())
	target := filepath.Join(t.TempDir(), "target")
	writeTestSchema(t, target, "tables/members.sql", "CREATE TABLE members (name TEXT, email TEXT NOT NULL);\n")
	parent := filepath.Join(t.TempDir(), "new-parent")
	setCommandOutput(t)
	err = run([]string{"diff-live", "-dialect", "sqlite", "-dsn", dsn, "-table", "members", "-to", target, "-output", filepath.Join(parent, "002_schema_evolution")})
	require.ErrorContains(t, err, "unsafe live dependencies")
	assertNoCLIOutputPath(t, parent, "002_schema_evolution")
}

func assertNoCLIOutputPath(t *testing.T, parent, migration string) {
	t.Helper()
	_, err := os.Stat(parent)
	require.ErrorIs(t, err, os.ErrNotExist)
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(parent), "."+migration+".tmp-*"))
	require.NoError(t, err)
	require.Empty(t, matches)
}

func cliSources(statements []migrate.Statement) []string {
	sources := make([]string, len(statements))
	for index, statement := range statements {
		sources[index] = statement.Source
	}
	return sources
}

func cliSQL(statements []migrate.Statement) []string {
	sql := make([]string, len(statements))
	for index, statement := range statements {
		sql[index] = string(statement.SQL)
	}
	return sql
}

type cliArtifact struct {
	source string
	sql    string
}

func mustReadCLIFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	return contents
}

func schemaEvolutionSources(t *testing.T, dialect string) (string, string) {
	t.Helper()
	var typ string
	switch dialect {
	case "postgresql", "mysql":
		typ = "BIGINT"
	case "sqlite":
		typ = "INTEGER"
	default:
		t.Fatal("unsupported test dialect")
	}
	baseline := filepath.Join(t.TempDir(), "baseline")
	target := filepath.Join(t.TempDir(), "target")
	writeTestSchema(t, baseline, "tables/members.sql", "CREATE TABLE members (id "+typ+" PRIMARY KEY, name TEXT NOT NULL);\n")
	writeTestSchema(t, target, "tables/members.sql", "CREATE TABLE members (id "+typ+" PRIMARY KEY, name TEXT NOT NULL, due_on TEXT NULL, email TEXT NOT NULL);\n")
	return baseline, target
}

func schemaEvolutionTwoRequiredSources(t *testing.T, dialect string) (string, string) {
	t.Helper()
	typ := "BIGINT"
	baseline := filepath.Join(t.TempDir(), "baseline")
	target := filepath.Join(t.TempDir(), "target")
	if dialect == "sqlite" {
		typ = "INTEGER"
	}
	writeTestSchema(t, baseline, "tables/members.sql", "CREATE TABLE members (id "+typ+" PRIMARY KEY, name TEXT NOT NULL);\n")
	writeTestSchema(t, target, "tables/members.sql", "CREATE TABLE members (id "+typ+" PRIMARY KEY, name TEXT NOT NULL, email TEXT NOT NULL, status TEXT NOT NULL);\n")
	return baseline, target
}
