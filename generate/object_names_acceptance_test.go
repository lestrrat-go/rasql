package generate_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestGeneratedObjectNamesSQLiteConsumer(t *testing.T) {
	repo, err := os.Getwd()
	require.NoError(t, err)
	repo = filepath.Dir(repo)
	table := schema.MustTableDef("customer-id",
		schema.Integer("id"), schema.Text("scan row"), schema.Text("foo_bar"), schema.Text("foo__bar"),
		schema.Text("日本語"), schema.Text("profile.name"), schema.Text(`say"hi`),
	)
	table.Schema = "main"
	audit := schema.MustTableDef("customer-id", schema.Integer("id"), schema.Text("display-name"))
	audit.Schema = "audit"
	root := t.TempDir()
	store := generate.Store{
		Package: "generated",
		Root:    repo,
		Dir:     root,
		Tables:  []schema.TableDef{table, audit},
		Names: map[schema.ObjectName]generate.ObjectNames{{Schema: "main", Name: "customer-id"}: {
			Accessor: "Customer", TableType: "CustomerTable", RowType: "CustomerRow", FileBase: "customer",
			Columns: map[string]generate.ColumnNames{
				"scan row": {Field: "ScanRowValue", Accessor: "ScanRowColumn"}, "foo_bar": {Field: "FooBarValue", Accessor: "FooBarColumn"},
				"foo__bar": {Field: "FooBarOther", Accessor: "FooBarOtherColumn"}, "日本語": {Field: "Japanese", Accessor: "JapaneseColumn"},
				"profile.name": {Field: "ProfileName", Accessor: "ProfileNameColumn"}, `say"hi`: {Field: "SayHi", Accessor: "SayHiColumn"},
			},
		}, {Schema: "audit", Name: "customer-id"}: {
			Accessor: "AuditCustomer", TableType: "AuditCustomerTable", RowType: "AuditCustomerRow", FileBase: "audit_customer",
			Columns: map[string]generate.ColumnNames{"display-name": {Field: "DisplayName", Accessor: "DisplayNameColumn"}},
		}},
	}
	plan, err := store.Plan()
	require.NoError(t, err)
	for _, file := range plan.Files() {
		require.NoError(t, os.MkdirAll(filepath.Dir(file.Path), 0o755))
		require.NoError(t, os.WriteFile(file.Path, file.Source, 0o644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/generated\n\ngo 1.23\n\nrequire (\n github.com/lestrrat-go/rasql v0.0.0\n modernc.org/sqlite v1.55.0\n)\n\nreplace github.com/lestrrat-go/rasql => "+repo+"\n"), 0o644))
	consumer := `package generated_test
import (
 "context"
 "database/sql"
 "fmt"
 "testing"
 "github.com/lestrrat-go/rasql"
 "github.com/lestrrat-go/rasql/dialect"
 "github.com/lestrrat-go/rasql/query"
 generated "example.com/generated"
 _ "modernc.org/sqlite"
)
func TestConsumer(t *testing.T) {
 db, err := sql.Open("sqlite", ":memory:"); if err != nil { t.Fatal(err) }; defer db.Close()
 db.SetMaxOpenConns(1)
 _, err = db.ExecContext(context.Background(), "ATTACH DATABASE ':memory:' AS audit"); if err != nil { t.Fatal(err) }
 _, err = db.ExecContext(context.Background(), "CREATE TABLE main.\"customer-id\" (\"id\" INTEGER PRIMARY KEY, \"scan row\" TEXT, \"foo_bar\" TEXT, \"foo__bar\" TEXT, \"日本語\" TEXT, \"profile.name\" TEXT, \"say\"\"hi\" TEXT)"); if err != nil { t.Fatal(err) }
 _, err = db.ExecContext(context.Background(), "CREATE TABLE audit.\"customer-id\" (\"id\" INTEGER PRIMARY KEY, \"display-name\" TEXT)"); if err != nil { t.Fatal(err) }
 table := generated.Customer(); insert, err := query.NewInsert(table.Ref(), query.Set(table.ID(), 1), query.Set(table.ScanRowColumn(), "scan"), query.Set(table.FooBarColumn(), "one"), query.Set(table.FooBarOtherColumn(), "two"), query.Set(table.JapaneseColumn(), "jp"), query.Set(table.ProfileNameColumn(), "profile"), query.Set(table.SayHiColumn(), "quote")); if err != nil { t.Fatal(err) }
 rdb, err := rasql.New(db, dialect.SQLite()); if err != nil { t.Fatal(err) }; if _, err = rasql.Exec(context.Background(), rdb, insert); err != nil { t.Fatal(err) }
 rows, err := rasql.SelectFrom(table).All(context.Background(), rdb); if err != nil { t.Fatal(err) }; if len(rows) != 1 || rows[0].ScanRowValue != "scan" || rows[0].FooBarValue != "one" || rows[0].FooBarOther != "two" || rows[0].Japanese != "jp" || rows[0].ProfileName != "profile" || rows[0].SayHi != "quote" { t.Fatalf("unexpected rows: %#v", rows) }
 if table.Ref().Name() != "customer-id" || table.ScanRowColumn().Name() != "scan row" || table.SayHiColumn().Name() != "say\"hi" { t.Fatalf("unexpected physical refs") }; if generated.CustomerDef().Schema != "main" || generated.CustomerDef().Name != "customer-id" { t.Fatalf("unexpected descriptor: %#v", generated.CustomerDef()) }
 pragma, err := db.QueryContext(context.Background(), "PRAGMA main.table_info(\"customer-id\")"); if err != nil { t.Fatal(err) }; defer pragma.Close(); var physical []string; for pragma.Next() { var cid int; var name, typ string; var notnull, pk int; var dflt any; if err = pragma.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil { t.Fatal(err) }; physical = append(physical, name) }; if err = pragma.Err(); err != nil { t.Fatal(err) }; wantPhysical := []string{"id", "scan row", "foo_bar", "foo__bar", "日本語", "profile.name", "say\"hi"}; if fmt.Sprint(physical) != fmt.Sprint(wantPhysical) { t.Fatalf("physical columns: got %v want %v", physical, wantPhysical) }
 auditTable := generated.AuditCustomer(); auditInsert, err := query.NewInsert(auditTable.Ref(), query.Set(auditTable.ID(), 2), query.Set(auditTable.DisplayNameColumn(), "audit")); if err != nil { t.Fatal(err) }; if _, err = rasql.Exec(context.Background(), rdb, auditInsert); err != nil { t.Fatal(err) }; auditRows, err := rasql.SelectFrom(auditTable).All(context.Background(), rdb); if err != nil { t.Fatal(err) }; if len(auditRows) != 1 || auditRows[0].DisplayName != "audit" { t.Fatalf("unexpected audit rows: %#v", auditRows) }
}
`
	require.NoError(t, os.WriteFile(filepath.Join(root, "consumer_test.go"), []byte(consumer), 0o644))
	command := exec.CommandContext(context.Background(), "go", "test", "-mod=mod", "./...")
	command.Dir = root
	command.Env = append(os.Environ(), "GOCACHE=/tmp/rasql-gocache")
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
}
