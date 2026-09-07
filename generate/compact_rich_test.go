package generate_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/querygen"
	"github.com/stretchr/testify/require"
)

func TestCompactRichExternalConsumer(t *testing.T) {
	in := richCompactInput(t)
	store, err := generate.RenderCompact(in)
	require.NoError(t, err)
	root := t.TempDir()
	store.Root, store.Dir = root, "generated"
	plan, err := store.Plan()
	require.NoError(t, err)
	var usersSource, projectsSource, viewSource string
	for _, file := range plan.Files() {
		switch filepath.Base(file.Path) {
		case "users_gen.go":
			usersSource = string(file.Source)
		case "projects_gen.go":
			projectsSource = string(file.Source)
		case "active_users_gen.go":
			viewSource = string(file.Source)
		}
	}
	require.Contains(t, usersSource, "func AccountRolesEdge")
	require.Contains(t, projectsSource, "func ProjectProjectsOwnerFkEdge")
	require.Contains(t, projectsSource, "func (v ProjectCreate) DefaultTitle")
	require.NotContains(t, viewSource, "ActiveUsersCreate")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "generated"), 0o755))
	require.NoError(t, plan.Commit())
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/rich\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\nrequire modernc.org/sqlite v1.55.0\nrequire example.com/domain v1.0.0\nreplace github.com/lestrrat-go/rasql => "+repoRoot(t)+"\nreplace example.com/domain => ./domain\n"), 0o600))
	domainDir := filepath.Join(root, "domain")
	require.NoError(t, os.MkdirAll(domainDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(domainDir, "go.mod"), []byte("module example.com/domain\n\ngo 1.26\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(domainDir, "money.go"), []byte(richDomainSource), 0o600))
	querySource, err := querygen.TypedGoSource(querygen.TypedInput{
		Package: "store", Function: "RichQuery", Engine: "sqlite", SQL: "SELECT id, balance FROM users WHERE id > ?",
		Operation: "select", Cardinality: "many", Result: "RichResult", Decoder: "RichDecoder",
		Parameters: []querygen.TypedValue{{Go: compilerir.GoField{Name: "after", Type: "int64"}}}, ArgumentNames: []string{"after"},
		Results: []querygen.TypedValue{{Go: compilerir.GoField{Name: "id", Type: "int64"}, Semantic: compilerir.SemanticValue{Name: "id", LogicalKind: "integer"}}, {Go: compilerir.GoField{Name: "balance", Type: "domain.Money", Codec: "money"}, Semantic: compilerir.SemanticValue{Name: "balance", LogicalKind: "integer"}}},
		Imports: []compilerir.GoImport{{Path: "example.com/domain", Alias: "domain"}},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "generated", "query_gen.go"), querySource, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "generated", "consumer_test.go"), []byte(richConsumerSource), 0o600))
	command := exec.Command("go", "test", "-mod=mod", "./generated")
	command.Dir = root
	command.Env = append(os.Environ(), "GOCACHE="+filepath.Join(root, "cache"))
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
}

func richCompactInput(t *testing.T) generate.EmitterInput {
	t.Helper()
	money := &compilerir.NativeType{Dialect: "sqlite", Name: "MONEY", Kind: "other"}
	users := compilerir.PhysicalObject{ID: "users", Kind: "table", Name: "users", Columns: []compilerir.PhysicalColumn{
		{Name: "id", LogicalKind: "integer"}, {Name: "nickname", Ordinal: 1, LogicalKind: "text", Nullable: true},
		{Name: "balance", Ordinal: 2, LogicalKind: "integer", Nullable: true, Native: money},
	}, Constraints: []compilerir.PhysicalConstraint{{Kind: "primary_key", Columns: []string{"id"}}}}
	projects := compilerir.PhysicalObject{ID: "projects", Kind: "table", Name: "projects", Columns: []compilerir.PhysicalColumn{
		{Name: "id", LogicalKind: "integer"}, {Name: "owner_id", Ordinal: 1, LogicalKind: "integer"}, {Name: "title", Ordinal: 2, LogicalKind: "text", DefaultSQL: "'untitled'"},
	}, Constraints: []compilerir.PhysicalConstraint{{Kind: "primary_key", Columns: []string{"id"}}, {Kind: "foreign_key", Name: "projects_owner_fk", Columns: []string{"owner_id"}, Reference: &compilerir.ForeignReference{Object: "users", Columns: []string{"id"}}}}}
	roles := compilerir.PhysicalObject{ID: "roles", Kind: "table", Name: "roles", Columns: []compilerir.PhysicalColumn{{Name: "id", LogicalKind: "integer"}}, Constraints: []compilerir.PhysicalConstraint{{Kind: "primary_key", Columns: []string{"id"}}}}
	links := compilerir.PhysicalObject{ID: "user_roles", Kind: "table", Name: "user_roles", Columns: []compilerir.PhysicalColumn{{Name: "user_id", LogicalKind: "integer"}, {Name: "role_id", Ordinal: 1, LogicalKind: "integer"}}, Constraints: []compilerir.PhysicalConstraint{{Kind: "foreign_key", Name: "user_roles_user_fk", Columns: []string{"user_id"}, Reference: &compilerir.ForeignReference{Object: "users", Columns: []string{"id"}}}, {Kind: "foreign_key", Name: "user_roles_role_fk", Columns: []string{"role_id"}, Reference: &compilerir.ForeignReference{Object: "roles", Columns: []string{"id"}}}}}
	view := compilerir.PhysicalObject{ID: "active_users", Kind: "view", Name: "active_users", Columns: []compilerir.PhysicalColumn{{Name: "id", LogicalKind: "integer"}, {Name: "nickname", Ordinal: 1, LogicalKind: "text", Nullable: true}, {Name: "balance", Ordinal: 2, LogicalKind: "integer", Nullable: true, Native: money}}}
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite", Version: "3"}, Objects: []compilerir.PhysicalObject{users, projects, roles, links, view}}
	mapping := compilerir.ScalarMapping{Name: "money", Match: compilerir.NativeMatch{Dialect: "sqlite", Name: "MONEY", Kind: "other"}, GoType: "domain.Money", Imports: []compilerir.GoImport{{Path: "example.com/domain", Alias: "domain"}}, Codec: "money"}
	relations := compilerir.MappingConfig{Scalars: []compilerir.ScalarMapping{mapping}, Relations: []compilerir.RelationMapping{{Name: "Roles", Source: "users", From: []string{"id"}, Target: "roles", To: []string{"id"}, Through: compilerir.ThroughMapping{Object: "user_roles", SourceFrom: []string{"user_id"}, SourceTo: []string{"id"}, TargetFrom: []string{"role_id"}, TargetTo: []string{"id"}}}}}
	semantic, diagnostics := compilerir.BuildSemantic(catalog, relations, nil)
	for _, diagnostic := range diagnostics {
		require.NotEqual(t, compilerir.DiagnosticError, diagnostic.Level, diagnostic.Message)
	}
	config := compilerir.GoConfig{Package: "store", Output: "generated", Emitter: "compact", Scalars: relations.Scalars, Objects: []compilerir.ObjectGoName{{ID: "users", Source: "Account", Row: "AccountRecord", File: "users_gen.go"}, {ID: "projects", Source: "Project", Row: "ProjectRecord", File: "projects_gen.go"}, {ID: "roles", Source: "Role", Row: "RoleRecord", File: "roles_gen.go"}, {ID: "user_roles", Source: "Membership", Row: "MembershipRecord", File: "user_roles_gen.go"}, {ID: "active_users", Source: "ActiveUser", Row: "ActiveUserRecord", File: "active_users_gen.go"}}}
	model, diagnostics := compilerir.BuildGo(semantic, config)
	for _, diagnostic := range diagnostics {
		require.NotEqual(t, compilerir.DiagnosticError, diagnostic.Level, diagnostic.Message)
	}
	in, err := generate.NewEmitterInput(catalog, semantic, model, config, relations)
	require.NoError(t, err)
	return in
}

const richDomainSource = `package domain

import (
 "database/sql/driver"
 "fmt"
)

type Money int64
type NullableMoney struct { Data Money; Valid bool }
func (m *Money) Scan(value any) error { switch v := value.(type) { case int64: *m = Money(v); return nil; case []byte: var n int64; _, err := fmt.Sscan(string(v), &n); *m = Money(n); return err }; return fmt.Errorf("money %T", value) }
func (m Money) Value() (driver.Value, error) { return int64(m), nil }
func (m *NullableMoney) Scan(value any) error { if value == nil { m.Data, m.Valid = 0, false; return nil }; m.Valid = true; return m.Data.Scan(value) }
func (m NullableMoney) Value() (driver.Value, error) { if !m.Valid { return nil, nil }; return m.Data.Value() }
`

const richConsumerSource = `package store_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
 "testing"
 "github.com/lestrrat-go/rasql"
 "github.com/lestrrat-go/rasql/dialect"
 "example.com/domain"
 store "example.com/rich/generated"
 _ "modernc.org/sqlite"
)

type userGraph struct { ID int64 }
type roleGraph struct { ID int64 }
type userWithRoles struct { Roles rasql.LoadedMany[roleGraph] }
type projectGraph struct { Owner rasql.LoadedOne[userGraph] }

func TestRichCompactRuntime(t *testing.T) {
 ctx := context.Background()
 sqlDB, err := sql.Open("sqlite", ":memory:"); if err != nil { t.Fatal(err) }; defer sqlDB.Close(); sqlDB.SetMaxOpenConns(1)
 if _, err = sqlDB.ExecContext(ctx, "CREATE TABLE users (id INTEGER PRIMARY KEY, nickname TEXT, balance INTEGER); CREATE TABLE projects (id INTEGER PRIMARY KEY, owner_id INTEGER NOT NULL, title TEXT DEFAULT 'untitled'); CREATE TABLE roles (id INTEGER PRIMARY KEY); CREATE TABLE user_roles (user_id INTEGER NOT NULL, role_id INTEGER NOT NULL); CREATE VIEW active_users AS SELECT id, nickname, balance FROM users"); err != nil { t.Fatal(err) }
 db, err := rasql.New(sqlDB, dialect.SQLite()); if err != nil { t.Fatal(err) }
 profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 1); if err != nil { t.Fatal(err) }
 executor, err := rasql.AsExecutor(db, profile); if err != nil { t.Fatal(err) }
 codecs, err := rasql.NewCodecRegistry(map[rasql.CodecID]rasql.ValueCodec{"money": moneyCodec{}}); if err != nil { t.Fatal(err) }; executor, err = rasql.WithCodecs(executor, codecs); if err != nil { t.Fatal(err) }
 create, err := store.NewAccountCreate().ID(1).Nickname("Ada").Balance(domain.Money(7)).Plan(); if err != nil { t.Fatal(err) }; if _, err = rasql.ExecMutation(ctx, executor, create); err != nil { t.Fatal(err) }
 source, err := store.Account().Source("u"); if err != nil { t.Fatal(err) }; expressions, err := (store.AccountColumns{}).Bind(source); if err != nil { t.Fatal(err) }; projection, err := store.AccountProjection(expressions); if err != nil { t.Fatal(err) }; query := rasql.Select(source.Source(), projection)
 rows, err := rasql.All(ctx, executor, query); if err != nil || len(rows) != 1 || rows[0].Balance.Value != domain.Money(7) || !rows[0].Balance.Valid { t.Fatalf("users=%#v err=%v", rows, err) }
 key, err := store.AccountIDPageKey(source, rasql.PageAscending); if err != nil { t.Fatal(err) }; if _, err = rasql.NewPageSpec([]rasql.PageKey[store.AccountRecord]{key}, key); err != nil { t.Fatal(err) }
 rich, err := store.RichQuery(0); if err != nil { t.Fatal(err) }; richRows, err := rasql.All(ctx, executor, rich); if err != nil || len(richRows) != 1 || richRows[0].Balance != domain.Money(7) { t.Fatalf("rich=%#v err=%v", richRows, err) }
 derived, err := rasql.Derive(rich, "d"); if err != nil { t.Fatal(err) }; if _, err = (store.RichQueryBindings{}).Bind(derived); err != nil { t.Fatal(err) }; cte, err := rasql.CTEOf("rich_values", rich); if err != nil { t.Fatal(err) }; cteSource, err := cte.Source("r"); if err != nil { t.Fatal(err) }; if _, err = (store.RichQueryBindings{}).Bind(cteSource); err != nil { t.Fatal(err) }
 projectSource, err := store.Project().Source("p"); if err != nil { t.Fatal(err) }; projectExpressions, err := (store.ProjectColumns{}).Bind(projectSource); if err != nil { t.Fatal(err) }; projectProjection, err := store.ProjectProjection(projectExpressions); if err != nil { t.Fatal(err) }; projectQuery := rasql.Select(projectSource.Source(), projectProjection); childSource, err := store.Account().Source("u2"); if err != nil { t.Fatal(err) }; childExpressions, err := (store.AccountColumns{}).Bind(childSource); if err != nil { t.Fatal(err) }; childProjection, err := store.AccountProjection(childExpressions); if err != nil { t.Fatal(err) }; childQuery := rasql.Select(childSource.Source(), childProjection); childPlan, err := rasql.NewGraphPlan(childQuery, func(row store.AccountRecord) userGraph { return userGraph{ID: row.ID} }); if err != nil { t.Fatal(err) }; edge, err := store.ProjectProjectsOwnerFkEdge(projectSource, childSource, childPlan, rasql.EdgeOptions{Order: []rasql.OrderTerm{rasql.AscExpr(childExpressions.ID.Expr())}, PerParentLimit: 2}, func(graph *projectGraph, value rasql.LoadedOne[userGraph]) { graph.Owner = value }); if err != nil { t.Fatal(err) }; graphPlan, err := rasql.NewGraphPlan(projectQuery, func(store.ProjectRecord) projectGraph { return projectGraph{} }, edge); if err != nil { t.Fatal(err) }; if _, err = rasql.LoadGraph(ctx, executor, graphPlan); err != nil { t.Fatal(err) }
 if _, err = sqlDB.ExecContext(ctx, "INSERT INTO roles (id) VALUES (9); INSERT INTO user_roles (user_id, role_id) VALUES (1, 9)"); err != nil { t.Fatal(err) }
 rootSource, err := store.Account().Source("u3"); if err != nil { t.Fatal(err) }; rootExpressions, err := (store.AccountColumns{}).Bind(rootSource); if err != nil { t.Fatal(err) }; rootProjection, err := store.AccountProjection(rootExpressions); if err != nil { t.Fatal(err) }; rootQuery := rasql.Select(rootSource.Source(), rootProjection)
 junctionSource, err := store.Membership().Source("ur"); if err != nil { t.Fatal(err) }; junctionExpressions, err := (store.MembershipColumns{}).Bind(junctionSource); if err != nil { t.Fatal(err) }
 rolesSource, err := store.Role().Source("r"); if err != nil { t.Fatal(err) }; rolesExpressions, err := (store.RoleColumns{}).Bind(rolesSource); if err != nil { t.Fatal(err) }; rolesProjection, err := store.RoleProjection(rolesExpressions); if err != nil { t.Fatal(err) }; rolesQuery := rasql.Select(rolesSource.Source(), rolesProjection); rolesPlan, err := rasql.NewGraphPlan(rolesQuery, func(row store.RoleRecord) roleGraph { return roleGraph{ID: row.ID} }); if err != nil { t.Fatal(err) }
 throughEdge, err := store.AccountRolesEdge(rootSource, junctionSource, rolesSource, rolesPlan, rasql.EdgeOptions{Order: []rasql.OrderTerm{rasql.AscExpr(junctionExpressions.RoleID.Expr())}}, func(graph *userWithRoles, value rasql.LoadedMany[roleGraph]) { graph.Roles = value }); if err != nil { t.Fatal(err) }; throughPlan, err := rasql.NewGraphPlan(rootQuery, func(store.AccountRecord) userWithRoles { return userWithRoles{} }, throughEdge); if err != nil { t.Fatal(err) }; if _, err = rasql.LoadGraph(ctx, executor, throughPlan); err != nil { t.Fatal(err) }
 if _, err = store.ActiveUser().Source(""); err != nil { t.Fatal(err) }
}

type moneyCodec struct{}
func (moneyCodec) Encode(value any) (driver.Value, error) { switch v := value.(type) { case domain.Money: return int64(v), nil; case domain.NullableMoney: if !v.Valid { return nil, nil }; return int64(v.Data), nil; default: return nil, nil } }
func (moneyCodec) Decode(value any, destination any) error { switch d := destination.(type) { case *domain.Money: return d.Scan(value); case *domain.NullableMoney: return d.Scan(value); default: return nil } }
func (moneyCodec) EncodeCursor(value any) ([]byte, error) { return []byte(fmt.Sprint(value)), nil }
func (moneyCodec) DecodeCursor(value []byte) (any, error) { var n int64; _, err := fmt.Sscan(string(value), &n); return domain.Money(n), err }
`
