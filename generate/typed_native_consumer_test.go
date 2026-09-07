package generate_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/querygen"
	"github.com/stretchr/testify/require"
)

func TestTypedNativeSQLiteTwoColumnConsumer(t *testing.T) {
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/typedconsumer\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\nreplace github.com/lestrrat-go/rasql => "+filepath.ToSlash(root)+"\n"), 0o600))
	input := querygen.TypedInput{Package: "queries", Function: "Find", Engine: "sqlite", SQL: "SELECT id, name FROM users WHERE id > ? ORDER BY id", Operation: "select", Cardinality: "many", Result: "FindResult", Decoder: "FindDecoder", Parameters: []querygen.TypedValue{{Go: compilerir.GoField{Name: "id", Type: "int64"}, Semantic: compilerir.SemanticValue{Name: "id", LogicalKind: "integer"}}}, ArgumentNames: []string{"id"}, Results: []querygen.TypedValue{{Go: compilerir.GoField{Name: "id", Type: "int64"}, Semantic: compilerir.SemanticValue{Name: "id", LogicalKind: "integer"}}, {Go: compilerir.GoField{Name: "name", Type: "rasql.Nullable[string]", Nullable: true}, Semantic: compilerir.SemanticValue{Name: "name", LogicalKind: "text", Nullable: true}}}}
	generated, err := querygen.TypedGoSource(input)
	require.NoError(t, err)
	oneInput := input
	oneInput.Function, oneInput.Result, oneInput.Decoder, oneInput.Cardinality = "FindOne", "FindOneResult", "FindOneDecoder", "one"
	oneGenerated, err := querygen.TypedGoSource(oneInput)
	require.NoError(t, err)
	maybeInput := input
	maybeInput.Function, maybeInput.Result, maybeInput.Decoder, maybeInput.Cardinality = "FindMaybe", "FindMaybeResult", "FindMaybeDecoder", "maybe"
	maybeGenerated, err := querygen.TypedGoSource(maybeInput)
	require.NoError(t, err)
	execInput := querygen.TypedInput{Package: "queries", Function: "Update", Engine: "sqlite", SQL: "UPDATE users SET name = name || '!' WHERE id > ?", Operation: "exec", Parameters: input.Parameters, ArgumentNames: []string{"id"}}
	execGenerated, err := querygen.TypedGoSource(execInput)
	require.NoError(t, err)
	codecInput := querygen.TypedInput{Package: "queries", Function: "FindMoney", Engine: "sqlite", SQL: "SELECT amount, note FROM payments WHERE amount > ?", Operation: "select", Cardinality: "many", Result: "MoneyResult", Decoder: "MoneyDecoder", Parameters: []querygen.TypedValue{{Go: compilerir.GoField{Name: "threshold", Type: "Money", Codec: "money"}, Semantic: compilerir.SemanticValue{Name: "threshold", LogicalKind: "integer"}}}, ArgumentNames: []string{"threshold"}, Results: []querygen.TypedValue{{Go: compilerir.GoField{Name: "amount", Type: "Money", Codec: "money"}, Semantic: compilerir.SemanticValue{Name: "amount", LogicalKind: "integer"}}, {Go: compilerir.GoField{Name: "note", Type: "rasql.Nullable[string]", Nullable: true, Codec: "money"}, Semantic: compilerir.SemanticValue{Name: "note", LogicalKind: "text", Nullable: true}}}}
	codecGenerated, err := querygen.TypedGoSource(codecInput)
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(dir, "queries"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "queries", "find_gen.go"), generated, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "queries", "find_one_gen.go"), oneGenerated, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "queries", "find_maybe_gen.go"), maybeGenerated, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "queries", "update_gen.go"), execGenerated, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "queries", "money_gen.go"), codecGenerated, 0o600))
	consumer := `package queries

import (
  "context"
  "database/sql/driver"
  "errors"
  "testing"
  "github.com/lestrrat-go/rasql"
  "github.com/lestrrat-go/rasql/dialect"
  "github.com/lestrrat-go/rasql/query"
  _ "modernc.org/sqlite"
  "database/sql"
)
type Money int64
type moneyCodec struct { enc, dec *int }
func (c moneyCodec) Encode(v any) (driver.Value, error) { (*c.enc)++; return int64(v.(Money)), nil }
func (c moneyCodec) Decode(v any, dst any) error { (*c.dec)++; switch d := dst.(type) { case *Money: *d = Money(v.(int64)); case *string: *d = v.(string) }; return nil }
func TestConsumer(t *testing.T) {
  db, err := sql.Open("sqlite", ":memory:"); if err != nil { t.Fatal(err) }; defer db.Close()
  if _, err = db.Exec("CREATE TABLE users (id INTEGER, name TEXT)"); err != nil { t.Fatal(err) }
  if _, err = db.Exec("INSERT INTO users VALUES (1, NULL), (2, 'two'), (3, 'three')"); err != nil { t.Fatal(err) }
  if _, err = db.Exec("CREATE TABLE payments (amount INTEGER, note TEXT)"); err != nil { t.Fatal(err) }; if _, err = db.Exec("INSERT INTO payments VALUES (1, NULL), (2, 'two')"); err != nil { t.Fatal(err) }
  rdb, err := rasql.New(db, dialect.SQLite()); if err != nil { t.Fatal(err) }
  profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 1); if err != nil { t.Fatal(err) }
  executor, err := rasql.AsExecutor(rdb, profile); if err != nil { t.Fatal(err) }
  enc, dec := 0, 0; registry, err := rasql.NewCodecRegistry(map[rasql.CodecID]rasql.ValueCodec{"money": moneyCodec{enc: &enc, dec: &dec}}); if err != nil { t.Fatal(err) }; executor, err = rasql.WithCodecs(executor, registry); if err != nil { t.Fatal(err) }
  q, err := Find(1); if err != nil { t.Fatal(err) }
  rows, err := rasql.All(t.Context(), executor, q); if err != nil || len(rows) != 2 { t.Fatalf("many 2: %#v %v", rows, err) }
  q, err = Find(3); if err != nil { t.Fatal(err) }; rows, err = rasql.All(t.Context(), executor, q); if err != nil || len(rows) != 0 { t.Fatalf("many 0: %#v %v", rows, err) }
  q, err = Find(0); if err != nil { t.Fatal(err) }; rows, err = rasql.All(t.Context(), executor, q); if err != nil || len(rows) != 3 { t.Fatalf("many 3: %#v %v", rows, err) }
  oneQuery, err := FindOne(2); if err != nil { t.Fatal(err) }; one, err := rasql.One(t.Context(), executor, oneQuery); if err != nil || one.ID != 3 { t.Fatalf("one 1: %#v %v", one, err) }
  oneQuery, err = FindOne(3); if err != nil { t.Fatal(err) }; _, err = rasql.One(t.Context(), executor, oneQuery); if !errors.Is(err, rasql.ErrNoRows) { t.Fatalf("one 0: %v", err) }
  oneQuery, err = FindOne(1); if err != nil { t.Fatal(err) }; _, err = rasql.One(t.Context(), executor, oneQuery); if !errors.Is(err, rasql.ErrMultipleRows) { t.Fatalf("one 2: %v", err) }
  maybeQuery, err := FindMaybe(2); if err != nil { t.Fatal(err) }; maybe, found, err := rasql.Maybe(t.Context(), executor, maybeQuery); if err != nil || !found || maybe.ID != 3 { t.Fatalf("maybe 1: %#v %t %v", maybe, found, err) }
  maybeQuery, err = FindMaybe(3); if err != nil { t.Fatal(err) }; _, found, err = rasql.Maybe(t.Context(), executor, maybeQuery); if err != nil || found { t.Fatalf("maybe 0: %t %v", found, err) }
  maybeQuery, err = FindMaybe(1); if err != nil { t.Fatal(err) }; _, _, err = rasql.Maybe(t.Context(), executor, maybeQuery); if !errors.Is(err, rasql.ErrMultipleRows) { t.Fatalf("maybe 2: %v", err) }
  update, err := Update(3); if err != nil { t.Fatal(err) }; outcome, err := rasql.ExecMutation(t.Context(), executor, update); if err != nil || outcome.Affected != 0 { t.Fatalf("exec 0: %#v %v", outcome, err) }
  update, err = Update(2); if err != nil { t.Fatal(err) }; outcome, err = rasql.ExecMutation(t.Context(), executor, update); if err != nil || outcome.Affected != 1 { t.Fatalf("exec 1: %#v %v", outcome, err) }
  update, err = Update(1); if err != nil { t.Fatal(err) }; outcome, err = rasql.ExecMutation(t.Context(), executor, update); if err != nil || outcome.Affected != 2 { t.Fatalf("exec 2: %#v %v", outcome, err) }
  cancelled, cancel := context.WithCancel(t.Context()); cancel(); update, err = Update(0); if err != nil { t.Fatal(err) }; _, err = rasql.ExecMutation(cancelled, executor, update); if !errors.Is(err, context.Canceled) { t.Fatalf("cancel: %v", err) }
  if err = rasql.Within(t.Context(), executor, nil, func(ctx context.Context, tx rasql.Executor) error { plan, e := Update(2); if e != nil { return e }; _, e = rasql.ExecMutation(ctx, tx, plan); return e }); err != nil { t.Fatalf("transaction: %v", err) }
  moneyQuery, err := FindMoney(0); if err != nil { t.Fatal(err) }; moneyRows, err := rasql.All(t.Context(), executor, moneyQuery); if err != nil || len(moneyRows) != 2 || moneyRows[0].Amount != 1 || moneyRows[0].Note.Valid { t.Fatalf("money: %#v %v", moneyRows, err) }; if enc != 1 || dec != 3 { t.Fatalf("codec counts encode=%d decode=%d", enc, dec) }
  _ = query.Bind
}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "queries", "consumer_test.go"), []byte(consumer), 0o600))
	cmd := exec.CommandContext(t.Context(), "go", "test", "-mod=mod", "./queries")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOCACHE="+filepath.Join(root, ".tmp", "go-cache"))
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
}
