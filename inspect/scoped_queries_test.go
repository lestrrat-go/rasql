package inspect

import (
	"strings"
	"testing"
)

func TestInformationQueriesScopeEveryMySQLMetadataFamily(t *testing.T) {
	queries, err := informationSchemaQueries("mysql")
	if err != nil {
		t.Fatal(err)
	}
	scoped := queries.scoped("audit")
	for name, query := range map[string]string{
		"columns": scoped.columns, "primary key": scoped.primaryKey,
		"unique constraints": scoped.uniqueConstraints, "checks": scoped.checks,
		"exclusion constraints": scoped.exclusionConstraints, "indexes": scoped.indexes,
		"foreign keys": scoped.foreignKeys,
	} {
		if query == "" {
			continue
		}
		if strings.Contains(query, "DATABASE()") {
			t.Errorf("%s query still uses DATABASE(): %s", name, query)
		}
		if !strings.Contains(query, "?") {
			t.Errorf("%s query lost its scoped arguments: %s", name, query)
		}
	}
}

func TestMySQLDynamicIndexQueryUsesRequestedNamespace(t *testing.T) {
	query := (informationQueries{indexes: mysqlStatisticsIndexesQuery(true, true)}).scoped("audit").indexes
	if strings.Contains(query, "DATABASE()") {
		t.Fatalf("scoped index query still uses connection default: %s", query)
	}
	args := (informationQueries{indexes: query}).argumentsScoped(query, "audit", "events")
	if len(args) != 2 || args[0] != "audit" || args[1] != "events" {
		t.Fatalf("scoped index arguments = %#v, want namespace and table", args)
	}
}

func TestInformationQueriesScopeEveryPostgreSQLMetadataFamily(t *testing.T) {
	queries := informationQueries{
		columns:              "WHERE table_schema = current_schema() AND table_name = $1",
		primaryKey:           "WHERE table_schema = current_schema() AND table_name = $1",
		uniqueConstraints:    "WHERE table_schema = current_schema() AND table_name = $1",
		checks:               "WHERE constraint_schema = current_schema() AND table_name = $1",
		exclusionConstraints: "WHERE constraint_schema = current_schema() AND table_name = $1",
		indexes:              "WHERE schemaname = current_schema() AND tablename = $1",
		foreignKeys:          "WHERE constraint_schema = current_schema() AND table_name = $1",
	}
	scoped := queries.scoped("billing")
	for name, query := range map[string]string{
		"columns": scoped.columns, "primary key": scoped.primaryKey,
		"unique constraints": scoped.uniqueConstraints, "checks": scoped.checks,
		"exclusion constraints": scoped.exclusionConstraints, "indexes": scoped.indexes,
		"foreign keys": scoped.foreignKeys,
	} {
		if strings.Contains(query, "current_schema()") || !strings.Contains(query, "$2") {
			t.Errorf("%s query was not scoped: %s", name, query)
		}
	}
}
