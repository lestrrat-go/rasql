package generate_test

import (
	"database/sql/driver"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/querygen"
	"github.com/lestrrat-go/rasql/internal/schemagen"
	"github.com/lestrrat-go/rasql/namedsql"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

const bindingConsumerTypes = `package generated

type NoScan struct{ Value string }
`

const bindingExternalTypes = `package types

import (
 "database/sql/driver"
 "fmt"
)
type UserID string
type Decimal string
type JSONValue string
type NullableDecimal struct { Data Decimal; Valid bool }
type NullableJSON struct { Data JSONValue; Valid bool }
func (d *Decimal) Scan(v any) error { switch x := v.(type) { case string: *d=Decimal(x); case []byte: *d=Decimal(x); default: return fmt.Errorf("decimal %T", v) }; return nil }
func (d Decimal) Value() (driver.Value,error) { return string(d),nil }
func (j *JSONValue) Scan(v any) error { switch x := v.(type) { case string: *j=JSONValue(x); case []byte: *j=JSONValue(x); default: return fmt.Errorf("json %T", v) }; return nil }
func (j JSONValue) Value() (driver.Value,error) { return string(j),nil }
func (d *NullableDecimal) Scan(v any) error { if v==nil { d.Valid=false; d.Data=""; return nil }; d.Valid=true; return d.Data.Scan(v) }
func (d NullableDecimal) Value() (driver.Value,error) { if !d.Valid { return nil,nil }; return d.Data.Value() }
func (j *NullableJSON) Scan(v any) error { if v==nil { j.Valid=false; j.Data=""; return nil }; j.Valid=true; return j.Data.Scan(v) }
func (j NullableJSON) Value() (driver.Value,error) { if !j.Valid { return nil,nil }; return j.Data.Value() }
`

const bindingConsumerTest = `package generated_test
import (
 "context"
 "database/sql"
 "strings"
 "testing"
 "github.com/lestrrat-go/rasql"
 "github.com/lestrrat-go/rasql/dialect"
 "example.com/types/v2"
 other "example.com/other/v2"
 "example.com/bindings/generated"
 _ "modernc.org/sqlite"
)
func TestBindingsRoundTrip(t *testing.T) {
 dbsql,err:=sql.Open("sqlite",":memory:"); if err!=nil { t.Fatal(err) }; defer dbsql.Close(); dbsql.SetMaxOpenConns(1)
 db,err:=rasql.New(dbsql,dialect.SQLite()); if err!=nil { t.Fatal(err) }; ctx:=context.Background()
 if err=rasql.CreateTable(ctx,db,generated.Users()); err!=nil { t.Fatal(err) }; if err=rasql.CreateTable(ctx,db,generated.Orders()); err!=nil { t.Fatal(err) }
 if _,err=rasql.Insert(ctx,db,generated.Users(),generated.UsersRow{ID:types.UserID("u1")}); err!=nil { t.Fatal(err) }
 want:=generated.OrdersRow{ID:1,UserID:types.UserID("u1"),OtherID:other.OtherID("o1"),Amount:types.NullableDecimal{Data:"12.50",Valid:true},Payload:types.NullableJSON{Data:` + "`" + `{"ok":true}` + "`" + `,Valid:true}}
 if _,err=rasql.Insert(ctx,db,generated.Orders(),want); err!=nil { t.Fatal(err) }
 nulls:=generated.OrdersRow{ID:2,UserID:types.UserID("u1"),OtherID:other.OtherID("o2"),Amount:types.NullableDecimal{},Payload:types.NullableJSON{}}
 if _,err=rasql.Insert(ctx,db,generated.Orders(),nulls); err!=nil { t.Fatal(err) }
 got,err:=rasql.SelectFrom(generated.Orders()).WhereEqual(generated.Orders().ID(),int64(1)).One(ctx,db); if err!=nil { t.Fatal(err) }; if got.UserID!=want.UserID || got.Amount!=want.Amount || got.Payload!=want.Payload { t.Fatalf("got %#v want %#v",got,want) }
 got,err=rasql.SelectFrom(generated.Orders()).WhereEqual(generated.Orders().ID(),int64(2)).One(ctx,db); if err!=nil { t.Fatal(err) }; if got.Amount.Valid || got.Payload.Valid { t.Fatalf("NULL wrappers %#v",got) }
 rows,err:=generated.Orders().User().Load(ctx,db,[]generated.OrdersRow{got}); if err!=nil { t.Fatal(err) }; if rows[types.UserID("u1")].ID!=types.UserID("u1") { t.Fatalf("relationship %#v",rows) }
 staticRows,err:=rasql.QueryRenderedAll[generated.OrdersRow](ctx,db,generated.OrderByUser(types.UserID("u1"))); if err!=nil { t.Fatal(err) }; if len(staticRows)!=2 || staticRows[0].UserID!=types.UserID("u1") { t.Fatalf("static rows %#v",staticRows) }
 if err=rasql.CreateTable(ctx,db,generated.BadValues()); err!=nil { t.Fatal(err) }; if _,err=dbsql.ExecContext(ctx,"INSERT INTO bad_values (id,value) VALUES (?,?)",1,"bad"); err!=nil { t.Fatal(err) }; _,err=rasql.SelectFrom(generated.BadValues()).One(ctx,db); if err==nil || !strings.Contains(err.Error(), "NoScan") { t.Fatalf("want NoScan scan error, got %v",err) }
}
`

func TestGeneratedGoBindingsRunInSQLiteConsumer(t *testing.T) {
	users, orders, bad := bindingTables()
	dir := t.TempDir()
	repository, err := filepath.Abs("../")
	require.NoError(t, err)
	module := strings.Replace(string(mustRead(t, "../go.mod")), "module github.com/lestrrat-go/rasql\n", "module example.com/bindings\n", 1)
	module += "\nrequire github.com/lestrrat-go/rasql v0.0.0\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(repository) + "\n"
	module += "require example.com/types/v2 v2.0.0\nreplace example.com/types/v2 => " + filepath.ToSlash(filepath.Join(dir, "types")) + "\n"
	module += "require example.com/other/v2 v2.0.0\nreplace example.com/other/v2 => " + filepath.ToSlash(filepath.Join(dir, "other")) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte(module), 0o600))
	typesDir := filepath.Join(dir, "types")
	require.NoError(t, os.Mkdir(typesDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(typesDir, "go.mod"), []byte("module example.com/types/v2\n\ngo 1.26\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(typesDir, "types.go"), []byte(bindingExternalTypes), 0o600))
	otherDir := filepath.Join(dir, "other")
	require.NoError(t, os.Mkdir(otherDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(otherDir, "go.mod"), []byte("module example.com/other/v2\n\ngo 1.26\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(otherDir, "types.go"), []byte("package types\n\ntype OtherID string\n"), 0o600))
	userQuery, err := namedsql.Parse("order_by_user", `SELECT id, user_id, amount, payload FROM orders WHERE user_id = {{bind "id" orders.user_id}}`)
	require.NoError(t, err)
	compiled, err := userQuery.Compile(dialect.SQLite())
	require.NoError(t, err)
	querySource, err := querygen.GoSourceInDir(dir, compiled.QueryDef(), "generated", "OrderByUser", users, orders)
	require.NoError(t, err)
	storeSource, err := schemagen.PackageSourceInDir(dir, "generated", users, orders, bad)
	require.NoError(t, err)
	require.Contains(t, string(storeSource), "types2.UserID")
	require.Contains(t, string(storeSource), "types.OtherID")
	require.Contains(t, string(storeSource), "NullableDecimal")
	require.Contains(t, string(querySource), "func OrderByUser(id types.UserID)")
	packageDir := filepath.Join(dir, "generated")
	require.NoError(t, os.Mkdir(packageDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(packageDir, "store_gen.go"), storeSource, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(packageDir, "query_gen.go"), querySource, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(packageDir, "types.go"), []byte(bindingConsumerTypes), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "consumer_test.go"), []byte(bindingConsumerTest), 0o600))
	command := exec.CommandContext(t.Context(), "go", "test", "-mod=mod", "./...")
	command.Dir = dir
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "consumer output:\n%s", output)
}

func bindingTables() (schema.TableDef, schema.TableDef, schema.TableDef) {
	userID := &schema.GoBinding{Type: "types.UserID", Imports: []schema.GoImport{{Path: "example.com/types/v2", Name: "types"}}}
	otherID := &schema.GoBinding{Type: "types.OtherID", Imports: []schema.GoImport{{Path: "example.com/other/v2", Name: "types"}}}
	orders := schema.TableDef{Name: "orders", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "user_id", Type: schema.TextType{}, GoBinding: userID}, {Name: "other_id", Type: schema.TextType{}, GoBinding: otherID}, {Name: "amount", Type: schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(2)}, Nullable: true, GoBinding: &schema.GoBinding{Type: "types.Decimal", NullableType: "types.NullableDecimal", Imports: []schema.GoImport{{Path: "example.com/types/v2", Name: "types"}}}}, {Name: "payload", Type: schema.JSONType{}, Nullable: true, GoBinding: &schema.GoBinding{Type: "types.JSONValue", NullableType: "types.NullableJSON", Imports: []schema.GoImport{{Path: "example.com/types/v2", Name: "types"}}}}}, ForeignKeys: []schema.ForeignKeyDef{{Name: "orders_user_fk", Columns: []string{"user_id"}, ReferencedTable: "users", ReferencedColumns: []string{"id"}}}, Relationships: []schema.RelationshipDef{{Name: "user", Kind: schema.RelationshipBelongsTo, Columns: []string{"user_id"}, ReferencedTable: "users", ReferencedColumns: []string{"id"}}}}
	users := schema.TableDef{Name: "users", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{Name: "id", Type: schema.TextType{}, GoBinding: userID}}}
	bad := schema.TableDef{Name: "bad_values", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "value", Type: schema.TextType{}, Nullable: true, GoBinding: &schema.GoBinding{Type: "NoScan", NullableType: "NoScan"}}}}
	return users, orders, bad
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(fmt.Errorf("read %s: %w", path, err))
	}
	return data
}

var _ driver.Valuer
