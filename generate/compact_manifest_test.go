package generate_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/querygen"
	"github.com/stretchr/testify/require"
)

func TestCompactManifestIsExactSortedAndResolvable(t *testing.T) {
	store, err := generate.RenderCompact(renamedRichCompactInput(t))
	require.NoError(t, err)
	expected := []generate.APIMapping{
		{Legacy: "Account", Compact: "store.Account", Status: "replacement"},
		{Legacy: "AccountCreate", Compact: "store.AccountInsert", Status: "replacement"},
		{Legacy: "AccountDef", Status: "removed"},
		{Legacy: "AccountPatch", Compact: "store.AccountChange", Status: "replacement"},
		{Legacy: "AccountRecord", Compact: "store.AccountRecord", Status: "replacement"},
		{Legacy: "AccountTable", Compact: "store.AccountTable", Status: "replacement"},
		{Legacy: "ActiveUser", Compact: "store.ActiveUser", Status: "replacement"},
		{Legacy: "ActiveUserDef", Status: "removed"},
		{Legacy: "ActiveUserRecord", Compact: "store.ActiveUserRecord", Status: "replacement"},
		{Legacy: "ActiveUserTable", Compact: "store.ActiveUserTable", Status: "replacement"},
		{Legacy: "Membership", Compact: "store.Membership", Status: "replacement"},
		{Legacy: "MembershipCreate", Compact: "store.MembershipCreate", Status: "replacement"},
		{Legacy: "MembershipDef", Status: "removed"},
		{Legacy: "MembershipPatch", Compact: "store.MembershipPatch", Status: "replacement"},
		{Legacy: "MembershipRecord", Compact: "store.MembershipRecord", Status: "replacement"},
		{Legacy: "MembershipTable", Compact: "store.MembershipTable", Status: "replacement"},
		{Legacy: "NewAccountCreate", Compact: "store.NewAccountInsert", Status: "replacement"},
		{Legacy: "NewAccountPatch", Compact: "store.NewAccountChange", Status: "replacement"},
		{Legacy: "NewMembershipCreate", Compact: "store.NewMembershipCreate", Status: "replacement"},
		{Legacy: "NewMembershipPatch", Compact: "store.NewMembershipPatch", Status: "replacement"},
		{Legacy: "NewProjectCreate", Compact: "store.NewProjectCreate", Status: "replacement"},
		{Legacy: "NewProjectPatch", Compact: "store.NewProjectPatch", Status: "replacement"},
		{Legacy: "NewRoleCreate", Compact: "store.NewRoleCreate", Status: "replacement"},
		{Legacy: "NewRolePatch", Compact: "store.NewRolePatch", Status: "replacement"},
		{Legacy: "Project", Compact: "store.Project", Status: "replacement"},
		{Legacy: "ProjectCreate", Compact: "store.ProjectCreate", Status: "replacement"},
		{Legacy: "ProjectDef", Status: "removed"},
		{Legacy: "ProjectPatch", Compact: "store.ProjectPatch", Status: "replacement"},
		{Legacy: "ProjectRecord", Compact: "store.ProjectRecord", Status: "replacement"},
		{Legacy: "ProjectTable", Compact: "store.ProjectTable", Status: "replacement"},
		{Legacy: "Role", Compact: "store.Role", Status: "replacement"},
		{Legacy: "RoleCreate", Compact: "store.RoleCreate", Status: "replacement"},
		{Legacy: "RoleDef", Status: "removed"},
		{Legacy: "RolePatch", Compact: "store.RolePatch", Status: "replacement"},
		{Legacy: "RoleRecord", Compact: "store.RoleRecord", Status: "replacement"},
		{Legacy: "RoleTable", Compact: "store.RoleTable", Status: "replacement"},
		{Legacy: "Tables", Status: "removed"},
		{Legacy: "TestRasqlgenGeneratedDefinitionsAreValid", Status: "removed"},
		{Legacy: "accountDef", Status: "removed"},
		{Legacy: "accountTable", Status: "removed"},
		{Legacy: "accountTimeScanner", Status: "removed"},
		{Legacy: "activeUserDef", Status: "removed"},
		{Legacy: "activeUserTable", Status: "removed"},
		{Legacy: "activeUserTimeScanner", Status: "removed"},
		{Legacy: "membershipDef", Status: "removed"},
		{Legacy: "membershipTable", Status: "removed"},
		{Legacy: "membershipTimeScanner", Status: "removed"},
		{Legacy: "projectDef", Status: "removed"},
		{Legacy: "projectTable", Status: "removed"},
		{Legacy: "projectTimeScanner", Status: "removed"},
		{Legacy: "roleDef", Status: "removed"},
		{Legacy: "roleTable", Status: "removed"},
		{Legacy: "roleTimeScanner", Status: "removed"},
	}
	manifest := store.APIManifest()
	require.Equal(t, expected, manifest)
	seen := make(map[string]struct{}, len(manifest))
	for _, mapping := range manifest {
		_, duplicate := seen[mapping.Legacy]
		require.False(t, duplicate, "duplicate manifest legacy symbol %q", mapping.Legacy)
		seen[mapping.Legacy] = struct{}{}
	}

	store.Root, store.Dir = t.TempDir(), "generated"
	plan, err := store.Plan()
	require.NoError(t, err)
	declarations := compactTopLevelDeclarations(t, plan.Files())
	for _, mapping := range manifest {
		if mapping.Status != "replacement" {
			continue
		}
		const packagePrefix = "store."
		require.True(t, len(mapping.Compact) > len(packagePrefix) && mapping.Compact[:len(packagePrefix)] == packagePrefix)
		require.Contains(t, declarations, mapping.Compact[len(packagePrefix):], "replacement %q is not emitted", mapping.Compact)
	}
}

func TestCompactPublicFilesAndDeclarationsAreExact(t *testing.T) {
	store, err := generate.RenderCompact(richCompactInput(t))
	require.NoError(t, err)
	store.TypedQueries = []generate.TypedQuery{richTypedQuery()}
	plan, err := store.Plan()
	require.NoError(t, err)

	expected := map[string][]string{
		"active_users_gen.go": {"ActiveUser", "ActiveUserBalancePageKey", "ActiveUserColumns", "ActiveUserColumns.Bind", "ActiveUserColumns.BindOptional", "ActiveUserExpressions", "ActiveUserIDPageKey", "ActiveUserNicknamePageKey", "ActiveUserProjection", "ActiveUserRecord", "ActiveUserRecord.ScanRow", "ActiveUserTable", "ActiveUserTable.Source", "OptionalActiveUserExpressions"},
		"projects_gen.go":     {"NewProjectCreate", "NewProjectPatch", "OptionalProjectExpressions", "OptionalProjectProjection", "Project", "ProjectColumns", "ProjectColumns.Bind", "ProjectColumns.BindOptional", "ProjectCreate", "ProjectCreate.DefaultTitle", "ProjectCreate.ID", "ProjectCreate.OwnerID", "ProjectCreate.Plan", "ProjectCreate.Title", "ProjectExpressions", "ProjectGraphKey", "ProjectIDPageKey", "ProjectOwnerEdge", "ProjectOwnerIDPageKey", "ProjectPatch", "ProjectPatch.DefaultTitle", "ProjectPatch.ID", "ProjectPatch.OwnerID", "ProjectPatch.Title", "ProjectPatch.Where", "ProjectProjection", "ProjectRecord", "ProjectRecord.ScanRow", "ProjectTable", "ProjectTable.Source", "ProjectTitlePageKey"},
		"query_gen.go":        {"RichDecoder", "RichDecoder.DecodeRow", "RichDecoder.Presence", "RichDecoder.ResultSchema", "RichDecoderSchema", "RichQuery", "RichQueryBindings", "RichQueryBindings.Bind", "RichQueryExpressions", "RichResult"},
		"roles_gen.go":        {"NewRoleCreate", "NewRolePatch", "OptionalRoleExpressions", "OptionalRoleProjection", "Role", "RoleColumns", "RoleColumns.Bind", "RoleColumns.BindOptional", "RoleCreate", "RoleCreate.ID", "RoleCreate.Plan", "RoleExpressions", "RoleGraphKey", "RoleIDPageKey", "RolePatch", "RolePatch.ID", "RolePatch.Where", "RoleProjection", "RoleRecord", "RoleRecord.ScanRow", "RoleTable", "RoleTable.Source", "RoleUserRolesEdge"},
		"schema_gen.go":       {},
		"schema_gen_test.go":  {},
		"user_roles_gen.go":   {"Membership", "MembershipColumns", "MembershipColumns.Bind", "MembershipColumns.BindOptional", "MembershipCreate", "MembershipCreate.Plan", "MembershipCreate.RoleID", "MembershipCreate.UserID", "MembershipExpressions", "MembershipPatch", "MembershipPatch.RoleID", "MembershipPatch.UserID", "MembershipPatch.Where", "MembershipProjection", "MembershipRecord", "MembershipRecord.ScanRow", "MembershipRoleEdge", "MembershipRoleIDPageKey", "MembershipTable", "MembershipTable.Source", "MembershipUserEdge", "MembershipUserIDPageKey", "NewMembershipCreate", "NewMembershipPatch", "OptionalMembershipExpressions"},
		"users_gen.go":        {"Account", "AccountBalancePageKey", "AccountColumns", "AccountColumns.Bind", "AccountColumns.BindOptional", "AccountCreate", "AccountCreate.Balance", "AccountCreate.ClearBalance", "AccountCreate.ClearNickname", "AccountCreate.ID", "AccountCreate.Nickname", "AccountCreate.Plan", "AccountExpressions", "AccountGraphKey", "AccountIDPageKey", "AccountNicknamePageKey", "AccountPatch", "AccountPatch.Balance", "AccountPatch.ClearBalance", "AccountPatch.ClearNickname", "AccountPatch.ID", "AccountPatch.Nickname", "AccountPatch.Where", "AccountProjection", "AccountProjectsEdge", "AccountRecord", "AccountRecord.ScanRow", "AccountRolesEdge", "AccountTable", "AccountTable.Source", "AccountUserRolesEdge", "NewAccountCreate", "NewAccountPatch", "OptionalAccountExpressions", "OptionalAccountProjection"},
	}
	actual := make(map[string][]string, len(plan.Files()))
	for _, file := range plan.Files() {
		actual[filepath.Base(file.Path)] = compactTopLevelDeclarationsFromSource(t, file.Source)
	}
	for name := range actual {
		sort.Strings(actual[name])
	}
	require.Equal(t, expected, actual)
}

func compactTopLevelDeclarations(t *testing.T, files []generate.File) map[string]struct{} {
	t.Helper()
	result := make(map[string]struct{})
	for _, file := range files {
		for _, name := range compactTopLevelDeclarationsFromSource(t, file.Source) {
			result[name] = struct{}{}
		}
	}
	return result
}

func compactTopLevelDeclarationsFromSource(t *testing.T, source []byte) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "generated.go", source, 0)
	require.NoError(t, err)
	result := make([]string, 0)
	for _, declaration := range file.Decls {
		switch declaration := declaration.(type) {
		case *ast.FuncDecl:
			if !declaration.Name.IsExported() {
				continue
			}
			if declaration.Recv == nil {
				result = append(result, declaration.Name.Name)
				continue
			}
			receiver := compactReceiverName(declaration.Recv)
			if !ast.IsExported(receiver) {
				continue
			}
			result = append(result, receiver+"."+declaration.Name.Name)
		case *ast.GenDecl:
			for _, spec := range declaration.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					if spec.Name.IsExported() {
						result = append(result, spec.Name.Name)
					}
				case *ast.ValueSpec:
					for _, name := range spec.Names {
						if name.IsExported() {
							result = append(result, name.Name)
						}
					}
				}
			}
		}
	}
	sort.Strings(result)
	return result
}

func compactReceiverName(field *ast.FieldList) string {
	if field == nil || len(field.List) != 1 {
		return "?"
	}
	expr := field.List[0].Type
	if pointer, ok := expr.(*ast.StarExpr); ok {
		expr = pointer.X
	}
	if identifier, ok := expr.(*ast.Ident); ok {
		return identifier.Name
	}
	return "?"
}

func renamedRichCompactInput(t *testing.T) generate.EmitterInput {
	t.Helper()
	in := richCompactInput(t)
	in.Generation.Objects[0].Create = "AccountInsert"
	in.Generation.Objects[0].Patch = "AccountChange"
	model, diagnostics := compilerir.BuildGo(in.Semantic, in.Generation)
	for _, diagnostic := range diagnostics {
		require.NotEqual(t, compilerir.DiagnosticError, diagnostic.Level, diagnostic.Message)
	}
	result, err := generate.NewEmitterInput(in.Catalog, in.Semantic, model, in.Generation, in.Mappings)
	require.NoError(t, err)
	return result
}

func richTypedQuery() generate.TypedQuery {
	return generate.TypedQuery{
		Function: "RichQuery", Output: "query_gen.go", Engine: "sqlite", SQL: "SELECT id, balance FROM users WHERE id = ?",
		Operation: "select", Cardinality: "many", Result: "RichResult", Decoder: "RichDecoder",
		Parameters:    []querygen.TypedValue{{Go: compilerir.GoField{Name: "after", Type: "int64"}, Semantic: compilerir.SemanticValue{Name: "after", LogicalKind: "integer"}}},
		ArgumentNames: []string{"after"},
		Results: []querygen.TypedValue{
			{Go: compilerir.GoField{Name: "id", Type: "int64"}, Semantic: compilerir.SemanticValue{Name: "id", LogicalKind: "integer"}},
			{Go: compilerir.GoField{Name: "balance", Type: "domain.Money", Codec: "money"}, Semantic: compilerir.SemanticValue{Name: "balance", LogicalKind: "integer"}},
		},
		Imports: []compilerir.GoImport{{Path: "example.com/domain", Alias: "domain"}},
	}
}
