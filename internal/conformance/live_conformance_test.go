//go:build unix

package conformance

import (
	"database/sql"
	"os"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/internal/dbtest"
)

func TestConformancePostgreSQL17(t *testing.T) {
	engine, ok := EngineByName("postgresql")
	if !ok {
		t.Fatal("postgresql conformance engine is missing")
	}
	database := dbtest.PostgreSQLDB(t)
	runLiveConformance(t, engine, database, ModuleVersion("github.com/jackc/pgx/v5"))
}

func TestConformanceMySQL84(t *testing.T) {
	engine, ok := EngineByName("mysql")
	if !ok {
		t.Fatal("mysql conformance engine is missing")
	}
	database := dbtest.MySQLDB(t)
	runLiveConformance(t, engine, database, ModuleVersion("github.com/go-sql-driver/mysql"))
}

func runLiveConformance(t *testing.T, engine Engine, database *sql.DB, driverVersion string) {
	t.Helper()
	database.SetMaxOpenConns(1)
	if err := SeedDatabaseForEngine(t.Context(), database, engine.Name); err != nil {
		t.Fatal(err)
	}
	rootDB, err := rasql.New(database, engine.Dialect)
	if err != nil {
		t.Fatal(err)
	}
	serverVersion, err := conformanceServerVersion(t.Context(), database, engine.Name)
	if err != nil {
		t.Fatal(err)
	}
	environment := EnvironmentSnapshot(CommitFromEnvironment(), serverVersion, driverVersion, os.Getenv(dsnEnvironment(engine.Name)))
	runCanonicalWorkloads(t, engine, database, rootDB, nil, environment)
}

func dsnEnvironment(engine string) string {
	if engine == "postgresql" {
		return "RASQL_TEST_POSTGRES_DSN"
	}
	return "RASQL_TEST_MYSQL_DSN"
}
