package migrate_test

import (
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestCatalogOrdersInitialMigrationByForeignKey(t *testing.T) {
	users := schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	}
	orders := schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "user_id", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKey{{
			Name:              "orders_user_fk",
			Columns:           []string{"user_id"},
			ReferencedTable:   "users",
			ReferencedColumns: []string{"id"},
		}},
	}
	catalog, err := migrate.NewCatalog(orders, users)
	require.NoError(t, err)

	migration, err := catalog.InitialMigration("001_initial")
	require.NoError(t, err)
	require.Len(t, migration.Operations, 2)
	first, ok := migration.Operations[0].(migrate.CreateTable)
	require.True(t, ok)
	require.Equal(t, "users", first.Table.Name)
	second, ok := migration.Operations[1].(migrate.CreateTable)
	require.True(t, ok)
	require.Equal(t, "orders", second.Table.Name)

	tables := catalog.Tables()
	tables[0].Name = "changed"
	require.Equal(t, "orders", catalog.Tables()[0].Name)
}

func TestCatalogRejectsInvalidForeignKeyTargetsAndCycles(t *testing.T) {
	missingTarget, err := migrate.NewCatalog(schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "user_id", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKey{{
			Columns:           []string{"user_id"},
			ReferencedTable:   "users",
			ReferencedColumns: []string{"id"},
		}},
	})
	require.ErrorContains(t, err, "outside the catalog")
	require.Equal(t, migrate.Catalog{}, missingTarget)

	first := schema.Table{
		Name: "first",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "second_id", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKey{{
			Columns:           []string{"second_id"},
			ReferencedTable:   "second",
			ReferencedColumns: []string{"id"},
		}},
	}
	second := schema.Table{
		Name: "second",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "first_id", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKey{{
			Columns:           []string{"first_id"},
			ReferencedTable:   "first",
			ReferencedColumns: []string{"id"},
		}},
	}
	catalog, err := migrate.NewCatalog(first, second)
	require.NoError(t, err)
	_, err = catalog.InitialMigration("001_initial")
	require.ErrorContains(t, err, "foreign-key cycle")
}

func TestMigrationRenderUsesDialectDDL(t *testing.T) {
	migration := migrate.Migration{
		ID: "002_user_indexes",
		Operations: []migrate.Operation{
			migrate.AddColumn{Table: "users", Column: schema.Column{Name: "nickname", Type: schema.TypeText, Nullable: true}},
			migrate.CreateIndex{Table: "users", Index: schema.Index{Name: "users_nickname_idx", Columns: []string{"nickname"}}},
			migrate.DropIndex{Table: "users", Name: "users_legacy_idx"},
		},
	}

	postgres, err := migration.Render(dialect.PostgreSQL())
	require.NoError(t, err)
	require.Equal(t, []string{
		`ALTER TABLE "users" ADD COLUMN "nickname" TEXT`,
		`CREATE INDEX "users_nickname_idx" ON "users" ("nickname")`,
		`DROP INDEX "users_legacy_idx"`,
	}, statements(postgres))

	mysql, err := migration.Render(dialect.MySQL())
	require.NoError(t, err)
	require.Equal(t, []string{
		"ALTER TABLE `users` ADD COLUMN `nickname` TEXT",
		"CREATE INDEX `users_nickname_idx` ON `users` (`nickname`)",
		"DROP INDEX `users_legacy_idx` ON `users`",
	}, statements(mysql))
}

func TestMigrationRejectsUnsafeAddColumn(t *testing.T) {
	migration := migrate.Migration{
		ID: "002_add_required_column",
		Operations: []migrate.Operation{
			migrate.AddColumn{Table: "users", Column: schema.Column{Name: "name", Type: schema.TypeText}},
		},
	}
	_, err := migration.Render(dialect.SQLite())
	require.ErrorContains(t, err, "must be nullable or have a default")
}

func TestRunnerAppliesSQLiteMigrationsAndDetectsDrift(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)
	runner, err := migrate.New(database, dialect.SQLite())
	require.NoError(t, err)

	createUsers := migrate.Migration{
		ID: "001_create_users",
		Operations: []migrate.Operation{
			migrate.CreateTable{Table: schema.Table{
				Name: "users",
				Columns: []schema.Column{
					{Name: "id", Type: schema.TypeInteger},
				},
				PrimaryKey: []string{"id"},
			}},
		},
	}
	addNickname := migrate.Migration{
		ID: "002_add_nickname",
		Operations: []migrate.Operation{
			migrate.AddColumn{Table: "users", Column: schema.Column{Name: "nickname", Type: schema.TypeText, Nullable: true}},
		},
	}

	require.NoError(t, runner.Apply(t.Context(), addNickname, createUsers))
	require.NoError(t, runner.Apply(t.Context(), createUsers, addNickname))

	rows, err := database.QueryContext(t.Context(), "SELECT id, checksum FROM rasql_schema_migrations ORDER BY id")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, rows.Close())
	})
	var ids []string
	for rows.Next() {
		var id string
		var checksum string
		require.NoError(t, rows.Scan(&id, &checksum))
		require.Len(t, checksum, 64)
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{"001_create_users", "002_add_nickname"}, ids)

	drifted := addNickname
	drifted.Operations = []migrate.Operation{
		migrate.AddColumn{Table: "users", Column: schema.Column{Name: "display_name", Type: schema.TypeText, Nullable: true}},
	}
	err = runner.Apply(t.Context(), createUsers, drifted)
	require.ErrorContains(t, err, "checksum does not match")
}

func TestRunnerRollsBackFailedSQLiteMigration(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)
	runner, err := migrate.New(database, dialect.SQLite())
	require.NoError(t, err)

	failing := migrate.Migration{
		ID: "001_create_events",
		Operations: []migrate.Operation{
			migrate.CreateTable{Table: schema.Table{
				Name:       "events",
				Columns:    []schema.Column{{Name: "id", Type: schema.TypeInteger}},
				PrimaryKey: []string{"id"},
			}},
			migrate.CreateIndex{Table: "events", Index: schema.Index{Name: "events_id_idx", Columns: []string{"id"}}},
			migrate.CreateIndex{Table: "events", Index: schema.Index{Name: "events_id_idx", Columns: []string{"id"}}},
		},
	}
	err = runner.Apply(t.Context(), failing)
	require.ErrorContains(t, err, "execute migration")

	var count int
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'events'").Scan(&count))
	require.Zero(t, count)
}

func TestRunnerRejectsRecordedMigrationAfterAMissingMigration(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)
	runner, err := migrate.New(database, dialect.SQLite())
	require.NoError(t, err)

	first := migrate.Migration{
		ID: "001_create_users",
		Operations: []migrate.Operation{
			migrate.CreateTable{Table: schema.Table{
				Name:       "users",
				Columns:    []schema.Column{{Name: "id", Type: schema.TypeInteger}},
				PrimaryKey: []string{"id"},
			}},
		},
	}
	second := migrate.Migration{
		ID: "002_add_nickname",
		Operations: []migrate.Operation{
			migrate.AddColumn{Table: "users", Column: schema.Column{Name: "nickname", Type: schema.TypeText, Nullable: true}},
		},
	}
	third := migrate.Migration{
		ID: "003_add_display_name",
		Operations: []migrate.Operation{
			migrate.AddColumn{Table: "users", Column: schema.Column{Name: "display_name", Type: schema.TypeText, Nullable: true}},
		},
	}
	require.NoError(t, runner.Apply(t.Context(), first, third))
	err = runner.Apply(t.Context(), first, second, third)
	require.ErrorContains(t, err, "recorded after a missing migration")
}

func TestRunnerRejectsUnspecifiedRecordedMigration(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)
	runner, err := migrate.New(database, dialect.SQLite())
	require.NoError(t, err)
	first := createUsersMigration()
	second := migrate.Migration{
		ID: "002_add_nickname",
		Operations: []migrate.Operation{
			migrate.AddColumn{Table: "users", Column: schema.Column{Name: "nickname", Type: schema.TypeText, Nullable: true}},
		},
	}
	require.NoError(t, runner.Apply(t.Context(), first, second))
	err = runner.Apply(t.Context(), first)
	require.ErrorContains(t, err, "was not supplied")
}

func TestRunnerUsesPostgreSQLTransactionAndHistoryLock(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	runner, err := migrate.New(database, dialect.PostgreSQL())
	require.NoError(t, err)

	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS "rasql_schema_migrations" ("id" TEXT NOT NULL PRIMARY KEY, "checksum" TEXT NOT NULL, "applied_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectBegin()
	mock.ExpectExec(`LOCK TABLE "rasql_schema_migrations" IN EXCLUSIVE MODE`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT "id", "checksum" FROM "rasql_schema_migrations" ORDER BY "id"`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "checksum"}))
	mock.ExpectExec(`CREATE TABLE "users" ("id" BIGINT NOT NULL, PRIMARY KEY ("id"))`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO "rasql_schema_migrations" ("id", "checksum") VALUES ($1, $2)`).
		WithArgs("001_create_users", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, runner.Apply(t.Context(), createUsersMigration()))
}

func TestRunnerUsesMySQLConnectionLock(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	runner, err := migrate.New(database, dialect.MySQL())
	require.NoError(t, err)

	mock.ExpectQuery("SELECT GET_LOCK(?, ?)").
		WithArgs("rasql_schema_migrations", 30).
		WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(1))
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS `rasql_schema_migrations` (`id` VARCHAR(255) NOT NULL PRIMARY KEY, `checksum` CHAR(64) NOT NULL, `applied_at` TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT `id`, `checksum` FROM `rasql_schema_migrations` ORDER BY `id`").
		WillReturnRows(sqlmock.NewRows([]string{"id", "checksum"}))
	mock.ExpectExec("CREATE TABLE `users` (`id` BIGINT NOT NULL, PRIMARY KEY (`id`))").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO `rasql_schema_migrations` (`id`, `checksum`) VALUES (?, ?)").
		WithArgs("001_create_users", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT RELEASE_LOCK(?)").
		WithArgs("rasql_schema_migrations").
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

	require.NoError(t, runner.Apply(t.Context(), createUsersMigration()))
}

func TestNewRejectsUnsupportedRunnerDialects(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	_, err = migrate.New(database, dialect.Spanner())
	require.ErrorContains(t, err, "not supported")
}

func statements(rendered []render.Statement) []string {
	statements := make([]string, len(rendered))
	for index, statement := range rendered {
		statements[index] = statement.SQL()
	}
	return statements
}

func createUsersMigration() migrate.Migration {
	return migrate.Migration{
		ID: "001_create_users",
		Operations: []migrate.Operation{
			migrate.CreateTable{Table: schema.Table{
				Name:       "users",
				Columns:    []schema.Column{{Name: "id", Type: schema.TypeInteger}},
				PrimaryKey: []string{"id"},
			}},
		},
	}
}
