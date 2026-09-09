package generate_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/querygen"
	"github.com/stretchr/testify/require"
)

func TestRenderCompactConcurrent(t *testing.T) {
	in := plainEmitterFixture(t)
	in.Generation.Emitter = "compact"
	ctx := t.Context()
	const renders = 100
	dirs := make([]string, renders)
	for i := range dirs {
		dirs[i] = t.TempDir()
	}
	outputs := make([][]byte, renders)
	errors := make([]error, renders)
	var wait sync.WaitGroup
	wait.Add(renders)
	for i := range renders {
		go func(index int) {
			defer wait.Done()
			store, err := generate.RenderCompact(in)
			if err != nil {
				errors[index] = err
				return
			}
			store.Root = dirs[index]
			store.Dir = "generated"
			plan, err := store.PlanContext(ctx)
			if err != nil {
				errors[index] = err
				return
			}
			var bytes []byte
			for _, file := range plan.Files() {
				bytes = append(bytes, filepath.Base(file.Path)...)
				bytes = append(bytes, 0)
				bytes = append(bytes, file.Source...)
				bytes = append(bytes, 0)
			}
			outputs[index] = bytes
		}(i)
	}
	wait.Wait()
	for _, err := range errors {
		require.NoError(t, err)
	}
	for i := 1; i < len(outputs); i++ {
		require.Equal(t, outputs[0], outputs[i])
	}
}

func TestCompactPlanIncludesTypedQueries(t *testing.T) {
	in := plainEmitterFixture(t)
	in.Generation.Emitter = "compact"
	store, err := generate.RenderCompact(in)
	require.NoError(t, err)
	store.Root, store.Dir = t.TempDir(), "generated"
	parameter := []querygen.TypedValue{{Go: compilerir.GoField{Name: "id", Type: "int64"}, Semantic: compilerir.SemanticValue{Name: "id", LogicalKind: "integer"}}}
	result := []querygen.TypedValue{{Go: compilerir.GoField{Name: "id", Type: "int64"}, Semantic: compilerir.SemanticValue{Name: "id", LogicalKind: "integer"}}}
	store.TypedQueries = []generate.TypedQuery{
		{Function: "FindUsers", Output: "find_users_gen.go", Engine: "sqlite", SQL: "SELECT id FROM users WHERE id = ?", Operation: "select", Cardinality: "many", Result: "FindUsersResult", Decoder: "FindUsersDecoder", Parameters: parameter, Results: result, ArgumentNames: []string{"id"}},
		{Function: "FindUser", Output: "find_user_gen.go", Engine: "sqlite", SQL: "SELECT id FROM users WHERE id = ?", Operation: "select", Cardinality: "one", Result: "FindUserResult", Decoder: "FindUserDecoder", Parameters: parameter, Results: result, ArgumentNames: []string{"id"}},
		{Function: "MaybeUser", Output: "maybe_user_gen.go", Engine: "sqlite", SQL: "SELECT id FROM users WHERE id = ?", Operation: "select", Cardinality: "maybe", Result: "MaybeUserResult", Decoder: "MaybeUserDecoder", Parameters: parameter, Results: result, ArgumentNames: []string{"id"}},
		{Function: "UpdateUser", Output: "update_user_gen.go", Engine: "sqlite", SQL: "UPDATE users SET id = id WHERE id = ?", Operation: "exec", Parameters: parameter, ArgumentNames: []string{"id"}},
	}
	plan, err := store.Plan()
	require.NoError(t, err)
	found := make(map[string]bool)
	for _, file := range plan.Files() {
		name := filepath.Base(file.Path)
		if name == "find_users_gen.go" || name == "find_user_gen.go" || name == "maybe_user_gen.go" || name == "update_user_gen.go" {
			found[name] = true
		}
		if name == "find_user_gen.go" {
			require.Contains(t, string(file.Source), "func FindUser")
		}
	}
	require.Len(t, found, 4)
}

func TestCompactPlanRejectsTypedQueryDeclarationCollision(t *testing.T) {
	in := plainEmitterFixture(t)
	in.Generation.Emitter = "compact"
	in.Generation.Objects[0].Source = "FindBindings"
	in.Generation.Objects[0].Row = "FindBindingsRow"
	model, diagnostics := compilerir.BuildGo(in.Semantic, in.Generation)
	require.Empty(t, diagnostics)
	in.Go = model
	store, err := generate.RenderCompact(in)
	require.NoError(t, err)
	store.Root, store.Dir = t.TempDir(), "generated"
	store.TypedQueries = []generate.TypedQuery{{
		Function: "Find", Output: "find_gen.go", Engine: "sqlite", SQL: "SELECT id FROM users", Operation: "select", Cardinality: "many",
		Results: []querygen.TypedValue{{Go: compilerir.GoField{Name: "id", Type: "int64"}, Semantic: compilerir.SemanticValue{Name: "id", LogicalKind: "integer"}}},
	}}
	_, err = store.Plan()
	require.Error(t, err)
	require.Contains(t, err.Error(), "FindBindings")
}

func TestCompactPlanRejectsTypedQuerySelfCollision(t *testing.T) {
	in := plainEmitterFixture(t)
	in.Generation.Emitter = "compact"
	store, err := generate.RenderCompact(in)
	require.NoError(t, err)
	store.Root, store.Dir = t.TempDir(), "generated"
	store.TypedQueries = []generate.TypedQuery{{
		Function: "Same", Output: "same_gen.go", Engine: "sqlite", SQL: "SELECT id FROM users", Operation: "select", Cardinality: "many", Result: "Same",
		Results: []querygen.TypedValue{{Go: compilerir.GoField{Name: "id", Type: "int64"}, Semantic: compilerir.SemanticValue{Name: "id", LogicalKind: "integer"}}},
	}}
	_, err = store.Plan()
	require.Error(t, err)
	require.Contains(t, err.Error(), "collides with its function declaration")
}

func TestCompactManifestMapsRenamedMutationSymbols(t *testing.T) {
	in := plainEmitterFixture(t)
	in.Generation.Emitter = "compact"
	in.Generation.Objects[0].Source = "Account"
	in.Generation.Objects[0].Row = "AccountRecord"
	in.Generation.Objects[0].Create = "AccountInsert"
	in.Generation.Objects[0].Patch = "AccountChange"
	model, diagnostics := compilerir.BuildGo(in.Semantic, in.Generation)
	require.Empty(t, diagnostics)
	in.Go = model
	store, err := generate.RenderCompact(in)
	require.NoError(t, err)
	manifest := store.APIManifest()
	find := func(legacy string) generate.APIMapping {
		for _, mapping := range manifest {
			if mapping.Legacy == legacy {
				return mapping
			}
		}
		return generate.APIMapping{}
	}
	require.Equal(t, generate.APIMapping{Legacy: "AccountCreate", Compact: "store.AccountInsert", Status: "replacement"}, find("AccountCreate"))
	require.Equal(t, generate.APIMapping{Legacy: "AccountPatch", Compact: "store.AccountChange", Status: "replacement"}, find("AccountPatch"))
}

func TestCompactRejectsGeneratedSymbolCollisions(t *testing.T) {
	cases := []struct {
		name, want string
		columns    []compilerir.PhysicalColumn
	}{
		{name: "scan row", columns: []compilerir.PhysicalColumn{{Name: "scan_row", Ordinal: 1, LogicalKind: "text"}}, want: "ScanRow"},
		{name: "create plan", columns: []compilerir.PhysicalColumn{{Name: "plan", Ordinal: 1, LogicalKind: "text"}}, want: "Plan"},
		{name: "patch where", columns: []compilerir.PhysicalColumn{{Name: "where", Ordinal: 1, LogicalKind: "text"}}, want: "Where"},
		{name: "reverse clear", columns: []compilerir.PhysicalColumn{{Name: "clear_name", Ordinal: 1, LogicalKind: "text"}, {Name: "name", Ordinal: 2, LogicalKind: "text", Nullable: true}}, want: "ClearName"},
		{name: "reverse default", columns: []compilerir.PhysicalColumn{{Name: "default_name", Ordinal: 1, LogicalKind: "text", DefaultSQL: "'default'"}, {Name: "name", Ordinal: 2, LogicalKind: "text", DefaultSQL: "'default'"}}, want: "DefaultName"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			columns := append([]compilerir.PhysicalColumn{{Name: "id", LogicalKind: "integer"}}, test.columns...)
			catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite", Version: "3"}, Objects: []compilerir.PhysicalObject{{
				ID: "users", Kind: "table", Name: "users", Columns: columns,
				Constraints: []compilerir.PhysicalConstraint{{Kind: "primary_key", Columns: []string{"id"}}},
			}}}
			semantic, diagnostics := compilerir.BuildSemantic(catalog, compilerir.MappingConfig{}, nil)
			require.Empty(t, diagnostics)
			config := compilerir.GoConfig{Package: "store", Output: "generated", Emitter: "compact", Objects: []compilerir.ObjectGoName{{ID: "users", File: "users_gen.go"}}}
			model, diagnostics := compilerir.BuildGo(semantic, config)
			require.Empty(t, diagnostics)
			input, err := generate.NewEmitterInput(catalog, semantic, model, config, compilerir.MappingConfig{})
			require.NoError(t, err)
			_, err = generate.RenderCompact(input)
			require.Error(t, err)
			require.Contains(t, err.Error(), test.want)
		})
	}
}

func TestCompactImportsOnlyUsedMappings(t *testing.T) {
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite", Version: "3"}, Objects: []compilerir.PhysicalObject{
		{ID: "users", Kind: "table", Name: "users", Columns: []compilerir.PhysicalColumn{{Name: "id", LogicalKind: "integer"}, {Name: "status", Ordinal: 1, LogicalKind: "text", Native: &compilerir.NativeType{Dialect: "sqlite", Kind: "other", Name: "status"}}}},
		{ID: "projects", Kind: "table", Name: "projects", Columns: []compilerir.PhysicalColumn{{Name: "id", LogicalKind: "integer"}, {Name: "title", Ordinal: 1, LogicalKind: "text"}}},
	}}
	mapping := compilerir.ScalarMapping{Name: "status", Match: compilerir.NativeMatch{Name: "status"}, GoType: "domain.Status", Codec: "status", Imports: []compilerir.GoImport{{Path: "example.com/domain", Alias: "domain"}}}
	semantic, diagnostics := compilerir.BuildSemantic(catalog, compilerir.MappingConfig{Scalars: []compilerir.ScalarMapping{mapping}}, nil)
	require.Empty(t, diagnostics)
	config := compilerir.GoConfig{Package: "store", Output: "generated", Emitter: "compact", Scalars: []compilerir.ScalarMapping{mapping}, Objects: []compilerir.ObjectGoName{{ID: "users", File: "users_gen.go"}, {ID: "projects", File: "projects_gen.go"}}}
	model, diagnostics := compilerir.BuildGo(semantic, config)
	require.Empty(t, diagnostics)
	in, err := generate.NewEmitterInput(catalog, semantic, model, config, compilerir.MappingConfig{Scalars: []compilerir.ScalarMapping{mapping}})
	require.NoError(t, err)
	store, err := generate.RenderCompact(in)
	require.NoError(t, err)
	store.Root, store.Dir = t.TempDir(), "generated"
	plan, err := store.Plan()
	require.NoError(t, err)
	for _, file := range plan.Files() {
		if filepath.Base(file.Path) == "projects_gen.go" {
			require.NotContains(t, string(file.Source), "example.com/domain")
		}
	}
}

func TestCompactEmitsGraphAndPageFactories(t *testing.T) {
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite", Version: "3"}, Objects: []compilerir.PhysicalObject{
		{ID: "users", Kind: "table", Name: "users", Columns: []compilerir.PhysicalColumn{
			{Name: "id", LogicalKind: "integer"}, {Name: "deleted_at", Ordinal: 1, LogicalKind: "text", Nullable: true},
		}, Constraints: []compilerir.PhysicalConstraint{{Kind: "primary_key", Columns: []string{"id"}}}},
	}}
	semantic, diagnostics := compilerir.BuildSemantic(catalog, compilerir.MappingConfig{}, nil)
	require.Empty(t, diagnostics)
	config := compilerir.GoConfig{Package: "store", Output: "generated", Emitter: "compact", Objects: []compilerir.ObjectGoName{{ID: "users", File: "users_gen.go"}}}
	model, diagnostics := compilerir.BuildGo(semantic, config)
	require.Empty(t, diagnostics)
	in, err := generate.NewEmitterInput(catalog, semantic, model, config, compilerir.MappingConfig{})
	require.NoError(t, err)
	store, err := generate.RenderCompact(in)
	require.NoError(t, err)
	store.Root, store.Dir = t.TempDir(), "generated"
	plan, err := store.Plan()
	require.NoError(t, err)
	var source string
	for _, file := range plan.Files() {
		if filepath.Base(file.Path) == "users_gen.go" {
			source = string(file.Source)
		}
	}
	require.Contains(t, source, "func UsersGraphKey(source rasql.TypedRelation[UsersRow])")
	require.Contains(t, source, "func UsersIDPageKey(source rasql.TypedRelation[UsersRow], direction rasql.PageDirection)")
	require.Contains(t, source, "func UsersDeletedAtPageKey(source rasql.TypedRelation[UsersRow], direction rasql.PageDirection, nulls rasql.NullOrder)")
	require.Contains(t, source, "rasqlgenNullablePageKey")
}

func TestCompactGeneratedGraphAliasReproducer(t *testing.T) {
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite", Version: "3"}, Objects: []compilerir.PhysicalObject{
		{ID: "users", Kind: "table", Name: "users", Columns: []compilerir.PhysicalColumn{{Name: "id", LogicalKind: "integer"}}, Constraints: []compilerir.PhysicalConstraint{{Kind: "primary_key", Columns: []string{"id"}}}},
		{ID: "projects", Kind: "table", Name: "projects", Columns: []compilerir.PhysicalColumn{{Name: "id", LogicalKind: "integer"}, {Name: "owner_id", Ordinal: 1, LogicalKind: "integer"}}, Constraints: []compilerir.PhysicalConstraint{{Kind: "primary_key", Columns: []string{"id"}}, {Kind: "foreign_key", Name: "projects_owner_fk", Columns: []string{"owner_id"}, Reference: &compilerir.ForeignReference{Object: "users", Columns: []string{"id"}}}}},
	}}
	semantic, diagnostics := compilerir.BuildSemantic(catalog, compilerir.MappingConfig{}, nil)
	require.Empty(t, diagnostics)
	config := compilerir.GoConfig{Package: "store", Output: "generated", Emitter: "compact", Objects: []compilerir.ObjectGoName{{ID: "users", File: "users_gen.go"}, {ID: "projects", File: "projects_gen.go"}}}
	model, diagnostics := compilerir.BuildGo(semantic, config)
	require.Empty(t, diagnostics)
	in, err := generate.NewEmitterInput(catalog, semantic, model, config, compilerir.MappingConfig{})
	require.NoError(t, err)
	store, err := generate.RenderCompact(in)
	require.NoError(t, err)
	root := t.TempDir()
	store.Root, store.Dir = root, "generated"
	plan, err := store.Plan()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "generated"), 0o755))
	require.NoError(t, plan.Commit())
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/graph\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\nrequire "+pinnedRequire(t, "modernc.org/sqlite")+"\nreplace github.com/lestrrat-go/rasql => "+repoRoot(t)+"\n"), 0o600))
	consumer := `package store_test

import (
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql"
	generated "example.com/graph/generated"
)

type projectGraph struct { Owner rasql.LoadedOne[userGraph] }
type userGraph struct { ID int64 }

func TestGeneratedEdgeUsesExactAliasedStage(t *testing.T) {
	parentSource, err := generated.Projects().Source("p")
	if err != nil { t.Fatal(err) }
	parentExpressions, err := (generated.ProjectsColumns{}).Bind(parentSource)
	if err != nil { t.Fatal(err) }
	parentProjection, err := generated.ProjectsProjection(parentExpressions)
	if err != nil { t.Fatal(err) }
	parentQuery := rasql.Select(parentSource.Source(), parentProjection)
	childSource, err := generated.Users().Source("u")
	if err != nil { t.Fatal(err) }
	childExpressions, err := (generated.UsersColumns{}).Bind(childSource)
	if err != nil { t.Fatal(err) }
	childProjection, err := generated.UsersProjection(childExpressions)
	if err != nil { t.Fatal(err) }
	childQuery := rasql.Select(childSource.Source(), childProjection)
	childPlan, err := rasql.NewGraphPlan(childQuery, func(row generated.UsersRow) userGraph { return userGraph{ID: row.ID} })
	if err != nil { t.Fatal(err) }
	edge, err := generated.ProjectsOwnerEdge(parentSource, childSource, childPlan, rasql.EdgeOptions{}, func(graph *projectGraph, value rasql.LoadedOne[userGraph]) { graph.Owner = value })
	if err != nil { t.Fatal(err) }
	if _, err = rasql.NewGraphPlan(parentQuery, func(generated.ProjectsRow) projectGraph { return projectGraph{} }, edge); err != nil { t.Fatal(err) }
	wrongParent, err := generated.Projects().Source("p2")
	if err != nil { t.Fatal(err) }
	wrongEdge, err := generated.ProjectsOwnerEdge(wrongParent, childSource, childPlan, rasql.EdgeOptions{}, func(graph *projectGraph, value rasql.LoadedOne[userGraph]) { graph.Owner = value })
	if err != nil { t.Fatal(err) }
	_, err = rasql.NewGraphPlan(parentQuery, func(generated.ProjectsRow) projectGraph { return projectGraph{} }, wrongEdge)
	if err == nil || !strings.Contains(err.Error(), "graph_key_mismatch") { t.Fatalf("err = %v, want graph_key_mismatch", err) }
}
`
	require.NoError(t, os.WriteFile(filepath.Join(root, "generated", "graph_test.go"), []byte(consumer), 0o600))
	command := exec.Command("go", "test", "-mod=mod", "./generated")
	command.Dir = root
	command.Env = append(os.Environ(), "GOCACHE="+filepath.Join(root, "cache"))
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
}

func TestCompactGeneratedGraphSourceMismatchMatrix(t *testing.T) {
	in := compactGraphMismatchInput(t)
	store, err := generate.RenderCompact(in)
	require.NoError(t, err)
	root := t.TempDir()
	store.Root, store.Dir = root, "generated"
	plan, err := store.Plan()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "generated"), 0o755))
	require.NoError(t, plan.Commit())
	module := "module example.com/mismatch\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\nrequire " + pinnedRequire(t, "modernc.org/sqlite") + "\nreplace github.com/lestrrat-go/rasql => " + repoRoot(t) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte(module), 0o600))
	consumer := `package store_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/exec"
	store "example.com/mismatch/generated"
	_ "modernc.org/sqlite"
)

type mismatchRoleGraph struct { ID int64 }
type mismatchAccountGraph struct { Roles rasql.LoadedMany[mismatchRoleGraph] }
type mismatchProjectGraph struct { ID int64 }

func TestSourceMismatchMatrix(t *testing.T) {
	parentSource, err := store.Project().Source("p")
	if err != nil { t.Fatal(err) }
	parentExpressions, err := (store.ProjectColumns{}).Bind(parentSource)
	if err != nil { t.Fatal(err) }
	parentProjection, err := store.ProjectProjection(parentExpressions)
	if err != nil { t.Fatal(err) }
	parentQuery := rasql.Select(parentSource.Source(), parentProjection)
	childSource, err := store.Account().Source("u")
	if err != nil { t.Fatal(err) }
	childExpressions, err := (store.AccountColumns{}).Bind(childSource)
	if err != nil { t.Fatal(err) }
	childProjection, err := store.AccountProjection(childExpressions)
	if err != nil { t.Fatal(err) }
	childQuery := rasql.Select(childSource.Source(), childProjection)
	childPlan, err := rasql.NewGraphPlan(childQuery, func(row store.AccountRecord) mismatchAccountGraph { return mismatchAccountGraph{} })
	if err != nil { t.Fatal(err) }
	validEdge, err := store.ProjectOwnerEdge(parentSource, childSource, childPlan, rasql.EdgeOptions{}, func(*mismatchProjectGraph, rasql.LoadedOne[mismatchAccountGraph]) {})
	if err != nil { t.Fatal(err) }
	if _, err = rasql.NewGraphPlan(parentQuery, func(store.ProjectRecord) mismatchProjectGraph { return mismatchProjectGraph{} }, validEdge); err != nil { t.Fatal(err) }
	wrongChildSource, err := store.Account().Source("u2")
	if err != nil { t.Fatal(err) }
	wrongChildEdge, err := store.ProjectOwnerEdge(parentSource, wrongChildSource, childPlan, rasql.EdgeOptions{}, func(*mismatchProjectGraph, rasql.LoadedOne[mismatchAccountGraph]) {})
	if err != nil { t.Fatal(err) }
	if _, err = rasql.NewGraphPlan(parentQuery, func(store.ProjectRecord) mismatchProjectGraph { return mismatchProjectGraph{} }, wrongChildEdge); err == nil ||
		!strings.Contains(err.Error(), "graph_key_mismatch") {
		t.Fatalf("wrong child error = %v", err)
	}

	rootSource, err := store.Account().Source("u3")
	if err != nil { t.Fatal(err) }
	rootExpressions, err := (store.AccountColumns{}).Bind(rootSource)
	if err != nil { t.Fatal(err) }
	rootProjection, err := store.AccountProjection(rootExpressions)
	if err != nil { t.Fatal(err) }
	rootQuery := rasql.Select(rootSource.Source(), rootProjection)
	junctionSource, err := store.Membership().Source("ur")
	if err != nil { t.Fatal(err) }
	junctionExpressions, err := (store.MembershipColumns{}).Bind(junctionSource)
	if err != nil { t.Fatal(err) }
	rolesSource, err := store.Role().Source("r")
	if err != nil { t.Fatal(err) }
	rolesExpressions, err := (store.RoleColumns{}).Bind(rolesSource)
	if err != nil { t.Fatal(err) }
	rolesProjection, err := store.RoleProjection(rolesExpressions)
	if err != nil { t.Fatal(err) }
	rolesPlan, err := rasql.NewGraphPlan(rasql.Select(rolesSource.Source(), rolesProjection), func(row store.RoleRecord) mismatchRoleGraph { return mismatchRoleGraph{ID: row.ID} })
	if err != nil { t.Fatal(err) }
	attachRoles := func(graph *mismatchAccountGraph, value rasql.LoadedMany[mismatchRoleGraph]) { graph.Roles = value }
	// The junctionSource argument selects the through stage, so any valid alias is legal.
	throughEdge, err := store.AccountRolesEdge(rootSource, junctionSource, rolesSource, rolesPlan, rasql.EdgeOptions{Order: []rasql.OrderTerm{rasql.AscExpr(junctionExpressions.RoleID.Expr())}}, attachRoles)
	if err != nil { t.Fatal(err) }
	throughPlan, err := rasql.NewGraphPlan(rootQuery, func(store.AccountRecord) mismatchAccountGraph { return mismatchAccountGraph{} }, throughEdge)
	if err != nil { t.Fatal(err) }
	wrongJunctionSource, err := store.Membership().Source("ur2")
	if err != nil { t.Fatal(err) }
	wrongJunctionExpressions, err := (store.MembershipColumns{}).Bind(wrongJunctionSource)
	if err != nil { t.Fatal(err) }
	wrongThroughEdge, err := store.AccountRolesEdge(rootSource, junctionSource, rolesSource, rolesPlan, rasql.EdgeOptions{Order: []rasql.OrderTerm{rasql.AscExpr(wrongJunctionExpressions.RoleID.Expr())}}, attachRoles)
	if err != nil { t.Fatal(err) }
	if _, err = rasql.NewGraphPlan(rootQuery, func(store.AccountRecord) mismatchAccountGraph { return mismatchAccountGraph{} }, wrongThroughEdge); err == nil ||
		!strings.Contains(err.Error(), "order source differs from child source") {
		t.Fatalf("wrong junction option error = %v", err)
	}

	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil { t.Fatal(err) }
	defer sqlDB.Close()
	for _, statement := range []string{
		"CREATE TABLE users (id INTEGER PRIMARY KEY)",
		"CREATE TABLE roles (id INTEGER PRIMARY KEY)",
		"CREATE TABLE user_roles (user_id INTEGER NOT NULL, role_id INTEGER NOT NULL)",
		"INSERT INTO users VALUES (1)",
		"INSERT INTO roles VALUES (9)",
		"INSERT INTO user_roles VALUES (1, 9)",
	} {
		if _, err = sqlDB.ExecContext(context.Background(), statement); err != nil { t.Fatal(err) }
	}
	var statements []string
	db, err := rasql.New(sqlDB, dialect.SQLite(), exec.HookFunc{BeforeFunc: func(_ context.Context, operation exec.Operation) error { statements = append(statements, operation.SQL()); return nil }})
	if err != nil { t.Fatal(err) }
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 1)
	if err != nil { t.Fatal(err) }
	executor, err := rasql.AsExecutor(db, profile)
	if err != nil { t.Fatal(err) }
	values, err := rasql.LoadGraph(context.Background(), executor, throughPlan)
	if err != nil || len(values) != 1 || !values[0].Roles.Loaded || len(values[0].Roles.Values) != 1 || values[0].Roles.Values[0].ID != 9 { t.Fatalf("valid junction alias values=%#v err=%v", values, err) }
	foundAlias := false
	for _, statement := range statements { if strings.Contains(statement, "\"user_roles\" AS \"ur\"") { foundAlias = true } }
	if !foundAlias { t.Fatalf("through SQL did not retain junction alias: %v", statements) }

	pageSource, err := store.Account().Source("u")
	if err != nil { t.Fatal(err) }
	pageKey, err := store.AccountIDPageKey(pageSource, rasql.PageAscending)
	if err != nil { t.Fatal(err) }
	pageSpec, err := rasql.NewPageSpec([]rasql.PageKey[store.AccountRecord]{pageKey}, pageKey)
	if err != nil { t.Fatal(err) }
	statements = nil
	_, err = rasql.PageGraphAfter(context.Background(), executor, throughPlan, pageSpec, rasql.PagePolicy{DefaultLimit: 1, MaxLimit: 2}, rasql.PageRequest{Limit: 1})
	if err == nil || !strings.Contains(err.Error(), "expression source is outside plan") { t.Fatalf("wrong page source error = %v", err) }
	if len(statements) != 0 { t.Fatalf("wrong page source started SQL: %v", statements) }
}
`
	require.NoError(t, os.WriteFile(filepath.Join(root, "generated", "mismatch_test.go"), []byte(consumer), 0o600))
	command := exec.Command("go", "test", "-mod=mod", "./generated")
	command.Dir = root
	command.Env = append(os.Environ(), "GOCACHE="+filepath.Join(root, "cache"))
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
}

func compactGraphMismatchInput(t *testing.T) generate.EmitterInput {
	t.Helper()
	users := compilerir.PhysicalObject{
		ID: "users", Kind: "table", Name: "users",
		Columns:     []compilerir.PhysicalColumn{{Name: "id", LogicalKind: "integer"}},
		Constraints: []compilerir.PhysicalConstraint{{Kind: "primary_key", Columns: []string{"id"}}},
	}
	projects := compilerir.PhysicalObject{
		ID: "projects", Kind: "table", Name: "projects",
		Columns: []compilerir.PhysicalColumn{
			{Name: "id", LogicalKind: "integer"},
			{Name: "owner_id", Ordinal: 1, LogicalKind: "integer"},
		},
		Constraints: []compilerir.PhysicalConstraint{
			{Kind: "primary_key", Columns: []string{"id"}},
			{Kind: "foreign_key", Name: "projects_owner_fk", Columns: []string{"owner_id"}, Reference: &compilerir.ForeignReference{Object: "users", Columns: []string{"id"}}},
		},
	}
	roles := compilerir.PhysicalObject{
		ID: "roles", Kind: "table", Name: "roles",
		Columns:     []compilerir.PhysicalColumn{{Name: "id", LogicalKind: "integer"}},
		Constraints: []compilerir.PhysicalConstraint{{Kind: "primary_key", Columns: []string{"id"}}},
	}
	links := compilerir.PhysicalObject{
		ID: "user_roles", Kind: "table", Name: "user_roles",
		Columns: []compilerir.PhysicalColumn{
			{Name: "user_id", LogicalKind: "integer"},
			{Name: "role_id", Ordinal: 1, LogicalKind: "integer"},
		},
		Constraints: []compilerir.PhysicalConstraint{
			{Kind: "foreign_key", Name: "user_roles_user_fk", Columns: []string{"user_id"}, Reference: &compilerir.ForeignReference{Object: "users", Columns: []string{"id"}}},
			{Kind: "foreign_key", Name: "user_roles_role_fk", Columns: []string{"role_id"}, Reference: &compilerir.ForeignReference{Object: "roles", Columns: []string{"id"}}},
		},
	}
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite", Version: "3"}, Objects: []compilerir.PhysicalObject{users, projects, roles, links}}
	relations := compilerir.MappingConfig{Relations: []compilerir.RelationMapping{{
		Name: "Roles", Source: "users", From: []string{"id"}, Target: "roles", To: []string{"id"},
		Through: compilerir.ThroughMapping{
			Object: "user_roles", SourceFrom: []string{"user_id"}, SourceTo: []string{"id"},
			TargetFrom: []string{"role_id"}, TargetTo: []string{"id"},
		},
	}}}
	semantic, diagnostics := compilerir.BuildSemantic(catalog, relations, nil)
	for _, diagnostic := range diagnostics {
		require.NotEqual(t, compilerir.DiagnosticError, diagnostic.Level, diagnostic.Message)
	}
	config := compilerir.GoConfig{
		Package: "store", Output: "generated", Emitter: "compact",
		Objects: []compilerir.ObjectGoName{
			{ID: "users", Source: "Account", Row: "AccountRecord", File: "users_gen.go"},
			{ID: "projects", Source: "Project", Row: "ProjectRecord", File: "projects_gen.go"},
			{ID: "roles", Source: "Role", Row: "RoleRecord", File: "roles_gen.go"},
			{ID: "user_roles", Source: "Membership", Row: "MembershipRecord", File: "user_roles_gen.go"},
		},
	}
	model, diagnostics := compilerir.BuildGo(semantic, config)
	for _, diagnostic := range diagnostics {
		require.NotEqual(t, compilerir.DiagnosticError, diagnostic.Level, diagnostic.Message)
	}
	in, err := generate.NewEmitterInput(catalog, semantic, model, config, relations)
	require.NoError(t, err)
	return in
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	return root
}

// pinnedRequire reads the version the repository's own go.mod pins for
// module, so a scratch fixture's go.mod names a version rasql actually
// depends on rather than one written down by hand that can drift out of
// sync with go.mod.
func pinnedRequire(t *testing.T, module string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "go.mod"))
	require.NoError(t, err)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, module+" ")
		if !ok {
			continue
		}
		return module + " " + strings.Fields(rest)[0]
	}
	t.Fatalf("go.mod has no requirement for %s", module)
	return ""
}
