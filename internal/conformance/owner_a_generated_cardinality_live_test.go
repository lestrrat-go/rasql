//go:build unix

package conformance

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestGeneratedOverdueCardinalityLive(t *testing.T) {
	t.Run("postgresql", func(t *testing.T) { runGeneratedLiveFixture(t, "postgresql", dbtest.PostgreSQLConfig(t).ConnString()) })
	t.Run("mysql", func(t *testing.T) { runGeneratedLiveFixture(t, "mysql", dbtest.MySQLConfig(t).FormatDSN()) })
}

func runGeneratedLiveFixture(t *testing.T, engine, dsn string) {
	t.Helper()
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	root := filepath.Join(t.TempDir(), "fixture")
	require.NoError(t, copyGeneratedStore(filepath.Join("testdata", engine, "internal", "store"), filepath.Join(root, "internal", "store")))
	modulePath := "example.test/live/" + engine
	goMod := fmt.Sprintf("module %s\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\nrequire %s\n\nreplace github.com/lestrrat-go/rasql => %s\n", modulePath, liveDriverRequirement(engine), filepath.ToSlash(repoRoot))
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte(goMod), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "cardinality_live_test.go"), []byte(generatedLiveCardinalityTest(engine, modulePath)), 0o600))
	command := exec.Command("go", "test", "-run", "^TestGeneratedLiveCardinalityRuntime$")
	command.Dir = root
	command.Env = append(offlineBuildEnv(t.TempDir()), "RASQL_LIVE_DSN="+dsn)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
}

func liveDriverRequirement(engine string) string {
	if engine == "postgresql" {
		return "github.com/jackc/pgx/v5 v5.10.0"
	}
	return "github.com/go-sql-driver/mysql v1.10.0"
}

func generatedLiveCardinalityTest(engine, modulePath string) string {
	driverName, dialectName, profile, openImport := "mysql", "MySQL", "mysql-8.4", `_ "github.com/go-sql-driver/mysql"`
	insertSQL := "INSERT INTO tasks(id,project_id,assignee_id,title,is_open,due_on,created_at) VALUES (?,?,?,?,?,?,?)"
	if engine == "postgresql" {
		driverName, dialectName, profile, openImport = "pgx", "PostgreSQL", "postgresql-17", `_ "github.com/jackc/pgx/v5/stdlib"`
		insertSQL = "INSERT INTO tasks(id,project_id,assignee_id,title,is_open,due_on,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7)"
	}
	return fmt.Sprintf(`package live

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"%s/internal/store"
	%s
)

func TestGeneratedLiveCardinalityRuntime(t *testing.T) {
	db, err := sql.Open("%s", getenv("RASQL_LIVE_DSN")); if err != nil { t.Fatal(err) }
	defer db.Close(); db.SetMaxOpenConns(1)
	_, err = db.Exec("DROP TABLE IF EXISTS tasks"); if err != nil { t.Fatal(err) }
	_, err = db.Exec("CREATE TABLE tasks (id BIGINT PRIMARY KEY, project_id BIGINT NOT NULL, assignee_id BIGINT, title VARCHAR(128) NOT NULL, is_open BOOLEAN NOT NULL, due_on DATE, created_at TIMESTAMP NOT NULL)"); if err != nil { t.Fatal(err) }
	for _, row := range []struct{id int64; due time.Time; assignee any}{{1,time.Date(2024,1,1,0,0,0,0,time.UTC),int64(1)},{3,time.Date(2024,1,3,0,0,0,0,time.UTC),int64(3)}} {
	_, err = db.Exec("%s", row.id,1,row.assignee,fmt.Sprintf("task-%%04d",row.id),true,row.due,time.Date(2024,1,1,0,0,0,0,time.UTC)); if err != nil { t.Fatal(err) }
	}
	raw, err := rasql.New(db, dialect.%s()); if err != nil { t.Fatal(err) }
	p, err := rasql.DiscoverEngineProfile(t.Context(), raw, "%s"); if err != nil { t.Fatal(err) }
	executor, err := rasql.AsExecutor(raw, p); if err != nil { t.Fatal(err) }
	for name, cutoff := range map[string]time.Time{"zero":time.Date(2024,1,1,0,0,0,0,time.UTC),"one":time.Date(2024,1,2,0,0,0,0,time.UTC),"two":time.Date(2024,1,4,0,0,0,0,time.UTC)} {
		one, err := store.OverdueTask(1,true,cutoff); if err != nil { t.Fatal(err) }; row, err := rasql.One(t.Context(),executor,one)
		if name=="zero" && !errors.Is(err,rasql.ErrNoRows) { t.Fatalf("one zero: %%v",err) }; if name=="one" && err!=nil { t.Fatal(err) }; if name=="one" { assertRow(t,row,1) }; if name=="two" && !errors.Is(err,rasql.ErrMultipleRows) { t.Fatalf("one two: %%v",err) }
		if name=="zero" || name=="two" { allQuery, err := store.OverdueTask(1,true,cutoff); if err != nil { t.Fatal(err) }; _, allErr := rasql.All(t.Context(),executor,allQuery); if name=="zero" && !errors.Is(allErr,rasql.ErrNoRows) { t.Fatalf("one all zero: %%v",allErr) }; if name=="two" && !errors.Is(allErr,rasql.ErrMultipleRows) { t.Fatalf("one all two: %%v",allErr) } }
		maybe, err := store.MaybeOverdueTask(1,true,cutoff); if err != nil { t.Fatal(err) }; maybeRow, found, err := rasql.Maybe(t.Context(),executor,maybe); if name=="zero" && (err!=nil||found||!reflect.ValueOf(maybeRow).IsZero()) { t.Fatalf("maybe zero: %%v %%t",err,found) }; if name=="one" && (err!=nil||!found) { t.Fatalf("maybe one: %%v %%t",err,found) }; if name=="two" && !errors.Is(err,rasql.ErrMultipleRows) { t.Fatalf("maybe two: %%v",err) }
		if name=="one" { assertRow(t,maybeRow,1) }
		if name=="zero" || name=="two" { allQuery, err := store.MaybeOverdueTask(1,true,cutoff); if err != nil { t.Fatal(err) }; allRows, allErr := rasql.All(t.Context(),executor,allQuery); if name=="zero" && (allErr!=nil||len(allRows)!=0) { t.Fatalf("maybe all zero: %%v %%d",allErr,len(allRows)) }; if name=="two" && !errors.Is(allErr,rasql.ErrMultipleRows) { t.Fatalf("maybe all two: %%v",allErr) } }
		many, err := store.OverdueTasks(1,true,cutoff); if err != nil { t.Fatal(err) }; rows, err := rasql.All(t.Context(),executor,many); if err!=nil { t.Fatal(err) }; want:=map[string]int{"zero":0,"one":1,"two":2}[name]; if len(rows)!=want { t.Fatalf("many %%s: %%d",name,len(rows)) }
		for index, id := range map[string][]int64{"zero":nil,"one":[]int64{1},"two":[]int64{1,3}}[name] { assertRow(t,rows[index],id) }
	}
}
func getenv(name string) string { return os.Getenv(name) }
func assertRow(t *testing.T, row any, id int64) {
	t.Helper(); value:=reflect.ValueOf(row)
	if value.FieldByName("ID").Interface()!=id || value.FieldByName("ProjectID").Interface()!=int64(1) { t.Fatalf("unexpected keys: %%#v",row) }
	if value.FieldByName("Title").Interface()!=fmt.Sprintf("task-%%04d",id) || value.FieldByName("IsOpen").Interface()!=true { t.Fatalf("unexpected text: %%#v",row) }
	if !reflect.DeepEqual(value.FieldByName("AssigneeID").Interface(),rasql.Nullable[int64]{Value:id,Valid:true}) { t.Fatalf("unexpected assignee: %%#v",row) }
	if !reflect.DeepEqual(value.FieldByName("DueOn").Interface(),rasql.Nullable[time.Time]{Value:time.Date(2024,1,int(id),0,0,0,0,time.UTC),Valid:true}) { t.Fatalf("unexpected due: %%#v",row) }
	if value.FieldByName("CreatedAt").Interface()!=time.Date(2024,1,1,0,0,0,0,time.UTC) { t.Fatalf("unexpected created: %%#v",row) }
}
`, modulePath, openImport, driverName, insertSQL, dialectName, profile)
}
