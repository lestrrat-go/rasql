package catalog

import (
	"testing"

	"github.com/lestrrat-go/rasql/inspect"
)

func TestDefaultHistoryIdentityOnlyTreatsSQLiteMainAsDefault(t *testing.T) {
	history := inspect.TableName{Schema: "main", Name: defaultHistoryTable}
	if !isDefaultHistoryIdentity(history, defaultHistoryTable, "sqlite") {
		t.Fatal("SQLite main history table must be the default identity")
	}
	if isDefaultHistoryIdentity(history, defaultHistoryTable, "postgresql") {
		t.Fatal("PostgreSQL schema main must remain qualified")
	}
	if isDefaultHistoryIdentity(history, defaultHistoryTable, "mysql") {
		t.Fatal("MySQL database main must remain qualified")
	}
}
