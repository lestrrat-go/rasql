package external_test

import (
	"bytes"
	"fmt"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

var _ changeplan.ProfileSource = rasql.EngineProfile{}

type externalPlanFixture struct {
	lockBytes     []byte
	profile       rasql.EngineProfile
	baseline      changeplan.Catalog
	after         changeplan.Catalog
	definition    schema.TableDef
	afterObject   changeplan.CatalogObject
	afterObjects  []changeplan.CatalogObject
	precondition  changeplan.Fact
	postcondition changeplan.Fact
	statement     stmt.Statement
	operation     changeplan.Operation
	history       changeplan.HistoryIdentity
	resolvedSteps []changeplan.ResolvedCatalogStep
	resolvedOps   []changeplan.Operation
	resolved      changeplan.ResolvedChanges
	plan          changeplan.Plan
	encoded       []byte
}

func makeExternalPlan(t *testing.T) externalPlanFixture {
	t.Helper()
	lockBytes, err := os.ReadFile("lock.json")
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 45, 0)
	require.NoError(t, err)
	baseline, err := changeplan.CatalogFromLock(lockBytes)
	require.NoError(t, err)
	startingID, ok := baseline.ObjectID(schema.ObjectTable, "main", "tasks")
	require.True(t, ok)
	definition := schema.TableDef{Schema: "main", Name: "tasks", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "payload", Type: schema.BytesType{}},
		{Name: "title", Type: schema.TextType{}},
	}, PrimaryKey: []string{"id"}}
	afterObject, err := changeplan.NewCatalogObject(startingID, definition)
	require.NoError(t, err)
	afterObjects := []changeplan.CatalogObject{afterObject}
	after, err := changeplan.NewCatalogLike(baseline, afterObjects)
	require.NoError(t, err)
	precondition, err := changeplan.NewFact(startingID, "/columns/0/name", changeplan.FactOperatorEqual, `"id"`)
	require.NoError(t, err)
	postcondition, err := changeplan.NewFact(startingID, "/columns/2/name", changeplan.FactOperatorEqual, `"title"`)
	require.NoError(t, err)
	statement := stmt.New(sqltext.Text(`ALTER TABLE "main"."tasks" ADD COLUMN "title" TEXT NOT NULL`))
	resultDigest, err := changeplan.CatalogDigest(after)
	require.NoError(t, err)
	operation, err := changeplan.NewOperation("add-title", changeplan.OperationAddColumn,
		[]changeplan.OperationID{}, []changeplan.ObjectID{startingID},
		[]changeplan.Fact{precondition}, []changeplan.Fact{postcondition}, resultDigest,
		[]stmt.Statement{statement}, changeplan.TransactionEngineDefault, false, []stmt.Statement{})
	require.NoError(t, err)
	step, err := changeplan.NewResolvedCatalogStep(operation.ID(), after)
	require.NoError(t, err)
	resolvedSteps := []changeplan.ResolvedCatalogStep{step}
	resolvedOps := []changeplan.Operation{operation}
	resolved, err := changeplan.NewResolvedChanges(baseline, resolvedSteps,
		[]changeplan.Decision{}, resolvedOps, []changeplan.BaselineObject{},
		[]changeplan.BaselineRename{})
	require.NoError(t, err)
	history, err := changeplan.NewHistoryIdentity("main", "schema_migrations")
	require.NoError(t, err)
	plan, err := changeplan.FromLock(lockBytes, profile, history, resolved)
	require.NoError(t, err)
	encoded, err := changeplan.Encode(plan)
	require.NoError(t, err)
	return externalPlanFixture{
		lockBytes: lockBytes, profile: profile, baseline: baseline, after: after, definition: definition,
		afterObject: afterObject, afterObjects: afterObjects,
		precondition: precondition, postcondition: postcondition,
		statement: statement, operation: operation, history: history,
		resolvedSteps: resolvedSteps, resolvedOps: resolvedOps, resolved: resolved,
		plan: plan, encoded: encoded,
	}
}

func TestExternalNonemptyPlanContract(t *testing.T) {
	fixture := makeExternalPlan(t)
	profile := fixture.profile
	baseline := fixture.baseline
	plan := fixture.plan
	operations := plan.Operations()
	require.Len(t, operations, 1)
	operation := operations[0]

	_, err := changeplan.CatalogFromLock(append(append([]byte(nil), fixture.lockBytes...), []byte(`{}`)...))
	require.Error(t, err)
	_, err = changeplan.CatalogFromLock([]byte(`{"format":2}`))
	require.Error(t, err)

	profileDigest, err := changeplan.ProfileDigest(profile)
	require.NoError(t, err)
	wantProfileDigest, err := changeplan.ParseDigest("305f23d085bd16011c322c292eebf0edbec28fae5332aee52d0ff268f1e298a4")
	require.NoError(t, err)
	require.Equal(t, wantProfileDigest, profileDigest)
	catalogDigest, err := changeplan.CatalogDigest(baseline)
	require.NoError(t, err)
	afterDigest, err := changeplan.CatalogDigest(fixture.after)
	require.NoError(t, err)
	require.Equal(t, afterDigest, operation.ResultDigest())
	wrongVersion, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 44, 0)
	require.NoError(t, err)
	_, err = changeplan.FromLock(fixture.lockBytes, wrongVersion, fixture.history, fixture.resolved)
	require.Error(t, err)

	gotProfile := plan.Profile()
	require.Equal(t, profile.ID(), gotProfile.ID())
	require.Equal(t, profile.Engine(), gotProfile.Engine())
	require.Empty(t, gotProfile.CustomName())
	require.Equal(t, profile.Version(), gotProfile.Version())
	require.Equal(t, profile.Capabilities(), gotProfile.Capabilities())
	require.Equal(t, profile.Limits(), gotProfile.Limits())
	require.Equal(t, profileDigest, mustProfileDigest(t, gotProfile))
	require.Equal(t, "main", plan.History().Schema())
	require.Equal(t, "schema_migrations", plan.History().Table())

	baselineIdentity := plan.Baseline()
	require.Equal(t, profile.Engine(), baselineIdentity.Catalog().Engine())
	require.Equal(t, profileDigest, baselineIdentity.Catalog().ProfileDigest())
	require.Equal(t, catalogDigest, baselineIdentity.Catalog().CatalogDigest())
	sourceDigest, err := changeplan.ParseDigest(strings.Repeat("1", 64))
	require.NoError(t, err)
	require.Equal(t, sourceDigest, baselineIdentity.Catalog().SourceDigest())
	require.Equal(t, baseline.SourceIdentity(), baselineIdentity.SourceIdentity())
	objects := baselineIdentity.Objects()
	require.Len(t, objects, 1)
	require.Equal(t, changeplan.ObjectID("tasks"), objects[0].ID())
	require.Equal(t, "table", objects[0].Kind())
	require.Equal(t, "main", objects[0].Schema())
	require.Equal(t, "tasks", objects[0].Name())
	require.Empty(t, objects[0].IntroducedBy())
	require.Empty(t, baselineIdentity.Renames())

	require.NotEqual(t, changeplan.PlanID{}, plan.ID())
	require.Equal(t, "add-title", string(operation.ID()))
	require.Equal(t, changeplan.OperationAddColumn, operation.Kind())
	require.Empty(t, operation.DependsOn())
	require.Equal(t, []changeplan.ObjectID{changeplan.ObjectID("tasks")}, operation.Objects())
	require.False(t, operation.Reversible())
	require.Equal(t, changeplan.TransactionEngineDefault, operation.Transaction())
	statements := operation.Statements()
	require.Len(t, statements, 1)
	require.Equal(t, fixture.statement.SQL(), statements[0].SQL())
	require.Empty(t, statements[0].Args())
	require.Empty(t, operation.ReverseStatements())
	preconditions := operation.Preconditions()
	require.Len(t, preconditions, 1)
	require.Equal(t, fixture.precondition.Object(), preconditions[0].Object())
	require.Equal(t, fixture.precondition.Path(), preconditions[0].Path())
	require.Equal(t, fixture.precondition.Operator(), preconditions[0].Operator())
	require.Equal(t, fixture.precondition.CanonicalValue(), preconditions[0].CanonicalValue())
	postconditions := operation.Postconditions()
	require.Len(t, postconditions, 1)
	require.Equal(t, fixture.postcondition.Object(), postconditions[0].Object())
	require.Equal(t, fixture.postcondition.Path(), postconditions[0].Path())
	require.Equal(t, fixture.postcondition.Operator(), postconditions[0].Operator())
	require.Equal(t, fixture.postcondition.CanonicalValue(), postconditions[0].CanonicalValue())

	steps := fixture.resolved.CatalogSteps()
	require.Len(t, steps, 1)
	require.Equal(t, operation.ID(), steps[0].Operation())
	gotAfterDigest, err := changeplan.CatalogDigest(steps[0].Catalog())
	require.NoError(t, err)
	require.Equal(t, operation.ResultDigest(), gotAfterDigest)
	require.Equal(t, operation, plan.Operations()[0])
	require.Equal(t, baselineIdentity, plan.Baseline())

	decoded, err := changeplan.Decode(fixture.encoded)
	require.NoError(t, err)
	require.Equal(t, baselineIdentity, decoded.Baseline())
	require.Equal(t, plan.ID(), decoded.ID())
	reencoded, err := changeplan.Encode(decoded)
	require.NoError(t, err)
	require.True(t, bytes.Equal(fixture.encoded, reencoded))

	identityDigest, err := changeplan.CatalogDigest(baseline)
	require.NoError(t, err)
	profileIdentity, err := changeplan.NewCatalogIdentity(profile.Engine(), profileDigest, identityDigest, changeplan.Digest{1})
	require.NoError(t, err)
	starting, err := changeplan.NewBaselineObject(changeplan.ObjectID("tasks"), "table", "main", "tasks")
	require.NoError(t, err)
	directBaseline, err := changeplan.NewBaselineIdentity(profileIdentity, baseline.SourceIdentity(),
		[]changeplan.BaselineObject{starting}, []changeplan.BaselineRename{})
	require.NoError(t, err)
	directPlan, err := changeplan.NewPlan(profile, directBaseline, fixture.history, []changeplan.Decision{}, []changeplan.Operation{operation})
	require.NoError(t, err)
	require.Equal(t, profile.ID(), directPlan.Profile().ID())
	require.Equal(t, profileDigest, mustProfileDigest(t, directPlan.Profile()))
}

func TestExternalCapabilityFieldTypesAreUsable(t *testing.T) {
	var returning changeplan.EngineReturningForms = changeplan.EngineReturningInsert
	var upsert changeplan.EngineUpsertForm = changeplan.EngineUpsertOnConflict
	var perParent changeplan.EnginePerParentLimitStrategy = changeplan.EnginePerParentLimitWindow
	var updateDefault changeplan.EngineUpdateDefaultSupport = changeplan.EngineUpdateDefaultExpression
	capabilities := changeplan.EngineCapabilities{
		Returning: returning, Upsert: upsert, PerParentLimit: perParent, UpdateDefault: updateDefault,
	}
	require.Equal(t, returning, capabilities.Returning)
	require.Equal(t, upsert, capabilities.Upsert)
	require.Equal(t, perParent, capabilities.PerParentLimit)
	require.Equal(t, updateDefault, capabilities.UpdateDefault)
}

func mustProfileDigest(t *testing.T, profile changeplan.Profile) changeplan.Digest {
	t.Helper()
	digest, err := changeplan.ProfileDigest(profile)
	require.NoError(t, err)
	return digest
}

func TestExternalPlanInputOwnership(t *testing.T) {
	fixture := makeExternalPlan(t)
	originalID := fixture.plan.ID()
	originalBytes := append([]byte(nil), fixture.encoded...)
	fixture.lockBytes[0] = 'X'
	require.Equal(t, originalID, fixture.plan.ID())
	encoded, err := changeplan.Encode(fixture.plan)
	require.NoError(t, err)
	require.Equal(t, originalBytes, encoded)

	fixture.definition.Columns[0].Name = "changed"
	fixture.afterObjects[0] = changeplan.CatalogObject{}
	require.Equal(t, "id", fixture.afterObject.Definition().Columns[0].Name)
	_, ok := fixture.after.ObjectID(schema.ObjectTable, "main", "tasks")
	require.True(t, ok)
	afterDigest, err := changeplan.CatalogDigest(fixture.after)
	require.NoError(t, err)
	gotDigest, err := changeplan.CatalogDigest(fixture.after)
	require.NoError(t, err)
	require.Equal(t, afterDigest, gotDigest)

	dependsOn := []changeplan.OperationID{}
	objects := []changeplan.ObjectID{changeplan.ObjectID("tasks")}
	preconditions := []changeplan.Fact{fixture.precondition}
	postconditions := []changeplan.Fact{fixture.postcondition}
	argument := []byte("payload")
	forward := []stmt.Statement{stmt.New(sqltext.Text("SELECT ?"), argument)}
	reverse := []stmt.Statement{stmt.New(sqltext.Text("SELECT ?"), argument)}
	native, err := changeplan.NewOperation("native-input", changeplan.OperationNativeSQL, dependsOn, objects,
		preconditions, postconditions, changeplan.Digest{9}, forward, changeplan.TransactionForbidden, true, reverse)
	require.NoError(t, err)
	dependsOn = append(dependsOn, "later")
	objects[0] = "changed"
	preconditions[0] = fixture.postcondition
	postconditions[0] = fixture.precondition
	forward[0] = stmt.New(sqltext.Text("SELECT changed"))
	reverse[0] = stmt.New(sqltext.Text("SELECT changed"))
	argument[0] = 'X'
	fixture.resolvedSteps[0] = changeplan.ResolvedCatalogStep{}
	fixture.resolvedOps[0] = native
	require.Empty(t, native.DependsOn())
	require.Equal(t, []changeplan.ObjectID{changeplan.ObjectID("tasks")}, native.Objects())
	require.Equal(t, fixture.precondition, native.Preconditions()[0])
	require.Equal(t, fixture.postcondition, native.Postconditions()[0])
	require.Equal(t, "SELECT ?", native.Statements()[0].SQL())
	require.Equal(t, []byte("payload"), native.Statements()[0].Args()[0])
	require.Equal(t, "SELECT ?", native.ReverseStatements()[0].SQL())
	require.Equal(t, []byte("payload"), native.ReverseStatements()[0].Args()[0])
	require.Equal(t, originalID, fixture.plan.ID())
	require.Equal(t, originalBytes, mustEncode(t, fixture.plan))

	decoded, err := changeplan.Decode(originalBytes)
	require.NoError(t, err)
	decodedBytes, err := changeplan.Encode(decoded)
	require.NoError(t, err)
	decodedBytes[0] = 'X'
	third, err := changeplan.Encode(decoded)
	require.NoError(t, err)
	require.Equal(t, originalBytes, third)
}

func mustEncode(t *testing.T, plan changeplan.Plan) []byte {
	t.Helper()
	encoded, err := changeplan.Encode(plan)
	require.NoError(t, err)
	return encoded
}

func TestPublicAPIHasNoInaccessibleTypes(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root := filepath.Join(filepath.Dir(filename), "../../../..")
	config := &packages.Config{Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
		packages.NeedImports | packages.NeedDeps | packages.NeedTypes | packages.NeedTypesInfo, Dir: root}
	loaded, err := packages.Load(config, "github.com/lestrrat-go/rasql/migrate/changeplan")
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Equal(t, "github.com/lestrrat-go/rasql/migrate/changeplan", loaded[0].PkgPath)
	require.Empty(t, loaded[0].Errors)
	require.Equal(t, "github.com/lestrrat-go/rasql/migrate/changeplan", loaded[0].Types.Path())
	pkg := loaded[0].Types
	approvedAliases := approvedPublicAliasDeclarations(t, pkg)
	for _, name := range pkg.Scope().Names() {
		object := pkg.Scope().Lookup(name)
		if !object.Exported() {
			continue
		}
		require.NoError(t, walkPublicType(object.Type(), map[types.Type]bool{}, approvedAliases), name)
	}

	internalPkg := types.NewPackage("example.com/rasql/internal/synthetic", "synthetic")
	publicPkg := types.NewPackage("example.com/rasql/publicfixture", "publicfixture")
	hidden := types.NewNamed(types.NewTypeName(token.NoPos, internalPkg, "Hidden", nil), types.Typ[types.String], nil)
	syntheticPkg := types.NewPackage("example.com/synthetic", "synthetic")
	parameter := types.NewVar(token.NoPos, syntheticPkg, "value", hidden)
	synthetic := types.NewSignatureType(nil, nil, nil, types.NewTuple(parameter), types.NewTuple(), false)
	t.Run("direct hidden signature", func(t *testing.T) {
		require.Error(t, walkPublicType(synthetic, map[types.Type]bool{}, approvedAliases))
	})

	t.Run("unapproved alias", func(t *testing.T) {
		alias := types.NewAlias(types.NewTypeName(token.NoPos, publicPkg, "LeakedAlias", nil), hidden)
		require.Error(t, walkPublicType(alias, map[types.Type]bool{}, approvedAliases))
	})
	t.Run("pointer receiver exported method", func(t *testing.T) {
		public := types.NewNamed(types.NewTypeName(token.NoPos, publicPkg, "Container", nil), types.NewStruct(nil, nil), nil)
		receiver := types.NewVar(token.NoPos, publicPkg, "receiver", types.NewPointer(public))
		parameter := types.NewVar(token.NoPos, publicPkg, "value", hidden)
		signature := types.NewSignatureType(receiver, nil, nil, types.NewTuple(parameter), types.NewTuple(), false)
		public.AddMethod(types.NewFunc(token.NoPos, publicPkg, "Set", signature))
		require.Error(t, walkPublicType(public, map[types.Type]bool{}, approvedAliases))
	})
	t.Run("unrelated internal type with approved spelling", func(t *testing.T) {
		collision := types.NewNamed(types.NewTypeName(token.NoPos, internalPkg, "EngineID", nil), types.Typ[types.String], nil)
		require.Error(t, walkPublicType(collision, map[types.Type]bool{}, approvedAliases))
	})
	t.Run("named generic constraint", func(t *testing.T) {
		constraint := types.NewInterfaceType(nil, []types.Type{hidden}).Complete()
		parameter := types.NewTypeParam(types.NewTypeName(token.NoPos, publicPkg, "T", nil), constraint)
		public := types.NewNamed(types.NewTypeName(token.NoPos, publicPkg, "Generic", nil), types.NewStruct(nil, nil), nil)
		public.SetTypeParams([]*types.TypeParam{parameter})
		require.Error(t, walkPublicType(public, map[types.Type]bool{}, approvedAliases))
	})
	t.Run("embedded constraint union", func(t *testing.T) {
		union := types.NewUnion([]*types.Term{types.NewTerm(false, hidden), types.NewTerm(false, types.Typ[types.Int])})
		constraint := types.NewInterfaceType(nil, []types.Type{union}).Complete()
		require.Error(t, walkPublicType(constraint, map[types.Type]bool{}, approvedAliases))
	})
	t.Run("spoofed approved alias identity", func(t *testing.T) {
		aliasPkg := types.NewPackage(pkg.Path(), pkg.Name())
		targetPkg := types.NewPackage("github.com/lestrrat-go/rasql/internal/engineprofile", "engineprofile")
		target := types.NewNamed(types.NewTypeName(token.NoPos, targetPkg, "EngineID", nil), types.Typ[types.Uint8], nil)
		alias := types.NewAlias(types.NewTypeName(token.NoPos, aliasPkg, "EngineID", nil), target)
		require.Error(t, walkPublicType(alias, map[types.Type]bool{}, approvedAliases))
	})
	t.Run("approved alias target does not exempt nested internal types", func(t *testing.T) {
		aliasName := types.NewTypeName(token.NoPos, publicPkg, "Approved", nil)
		targetPkg := types.NewPackage("example.com/rasql/internal/approved", "approved")
		field := types.NewVar(token.NoPos, publicPkg, "Hidden", hidden)
		target := types.NewNamed(types.NewTypeName(token.NoPos, targetPkg, "Target", nil),
			types.NewStruct([]*types.Var{field}, []string{""}), nil)
		alias := types.NewAlias(aliasName, target)
		localApprovals := publicAliasApprovals{aliasName: target.Obj()}
		require.Error(t, walkPublicType(alias, map[types.Type]bool{}, localApprovals))
	})

	t.Run("approved aliases", func(t *testing.T) {
		for name := range approvedPublicAliases {
			object := pkg.Scope().Lookup(name)
			require.NotNil(t, object)
			require.NoError(t, walkPublicType(object.Type(), map[types.Type]bool{}, approvedAliases), name)
		}
	})
	t.Run("safe pointer receiver", func(t *testing.T) {
		public := types.NewNamed(types.NewTypeName(token.NoPos, publicPkg, "SafeContainer", nil), types.NewStruct(nil, nil), nil)
		receiver := types.NewVar(token.NoPos, publicPkg, "receiver", types.NewPointer(public))
		parameter := types.NewVar(token.NoPos, publicPkg, "value", types.Typ[types.String])
		signature := types.NewSignatureType(receiver, nil, nil, types.NewTuple(parameter), types.NewTuple(), false)
		public.AddMethod(types.NewFunc(token.NoPos, publicPkg, "Set", signature))
		require.NoError(t, walkPublicType(public, map[types.Type]bool{}, approvedAliases))
	})
	t.Run("public generic constraint", func(t *testing.T) {
		term := types.NewUnion([]*types.Term{types.NewTerm(true, types.Typ[types.String])})
		constraint := types.NewInterfaceType(nil, []types.Type{term}).Complete()
		parameter := types.NewTypeParam(types.NewTypeName(token.NoPos, publicPkg, "T", nil), constraint)
		public := types.NewNamed(types.NewTypeName(token.NoPos, publicPkg, "GenericPublic", nil), types.NewStruct(nil, nil), nil)
		public.SetTypeParams([]*types.TypeParam{parameter})
		require.NoError(t, walkPublicType(public, map[types.Type]bool{}, approvedAliases))
	})
	t.Run("public union", func(t *testing.T) {
		union := types.NewUnion([]*types.Term{types.NewTerm(false, types.Typ[types.Int]), types.NewTerm(false, types.Typ[types.String])})
		constraint := types.NewInterfaceType(nil, []types.Type{union}).Complete()
		require.NoError(t, walkPublicType(constraint, map[types.Type]bool{}, approvedAliases))
	})
}

type publicAliasTarget struct {
	packagePath string
	name        string
}

var approvedPublicAliases = map[string]publicAliasTarget{
	"EngineID":                     {packagePath: "github.com/lestrrat-go/rasql/internal/engineprofile", name: "EngineID"},
	"EngineVersion":                {packagePath: "github.com/lestrrat-go/rasql/internal/engineprofile", name: "Version"},
	"EngineCapabilities":           {packagePath: "github.com/lestrrat-go/rasql/internal/engineprofile", name: "Capabilities"},
	"EngineLimits":                 {packagePath: "github.com/lestrrat-go/rasql/internal/engineprofile", name: "Limits"},
	"EngineReturningForms":         {packagePath: "github.com/lestrrat-go/rasql/internal/engineprofile", name: "ReturningForms"},
	"EngineUpsertForm":             {packagePath: "github.com/lestrrat-go/rasql/internal/engineprofile", name: "UpsertForm"},
	"EnginePerParentLimitStrategy": {packagePath: "github.com/lestrrat-go/rasql/internal/engineprofile", name: "PerParentLimitStrategy"},
	"EngineUpdateDefaultSupport":   {packagePath: "github.com/lestrrat-go/rasql/internal/engineprofile", name: "UpdateDefaultSupport"},
}

type publicAliasApprovals map[*types.TypeName]*types.TypeName

func approvedPublicAliasDeclarations(t *testing.T, pkg *types.Package) publicAliasApprovals {
	t.Helper()
	approvals := make(publicAliasApprovals, len(approvedPublicAliases))
	for name, want := range approvedPublicAliases {
		object, ok := pkg.Scope().Lookup(name).(*types.TypeName)
		require.True(t, ok, name)
		alias, ok := object.Type().(*types.Alias)
		require.True(t, ok, name)
		target, ok := types.Unalias(alias.Rhs()).(*types.Named)
		require.True(t, ok, name)
		require.NotNil(t, target.Obj().Pkg(), name)
		require.Equal(t, want.packagePath, target.Obj().Pkg().Path(), name)
		require.Equal(t, want.name, target.Obj().Name(), name)
		approvals[object] = target.Obj()
	}
	return approvals
}

func walkPublicType(value types.Type, seen map[types.Type]bool, approvals publicAliasApprovals) error {
	return walkPublicTypeState(value, seen, approvals, nil)
}

func walkPublicTypeState(
	value types.Type,
	seen map[types.Type]bool,
	approvals publicAliasApprovals,
	allowedNamed *types.TypeName,
) error {
	if value == nil || seen[value] {
		return nil
	}
	seen[value] = true
	defer delete(seen, value)
	switch current := value.(type) {
	case *types.Alias:
		if err := walkTypeParams(current.TypeParams(), seen, approvals); err != nil {
			return err
		}
		if err := walkTypeArgs(current.TypeArgs(), seen, approvals); err != nil {
			return err
		}
		return walkPublicTypeState(current.Rhs(), seen, approvals, approvedAliasTarget(current, approvals))
	case *types.Named:
		object := current.Obj()
		if inaccessiblePackage(object.Pkg()) && object != allowedNamed && !approvedAliasTargetObject(object, approvals) {
			return fmt.Errorf("inaccessible type %s", types.TypeString(value, nil))
		}
		if err := walkTypeArgs(current.TypeArgs(), seen, approvals); err != nil {
			return err
		}
		if err := walkTypeParams(current.TypeParams(), seen, approvals); err != nil {
			return err
		}
		if err := walkPublicTypeState(current.Underlying(), seen, approvals, nil); err != nil {
			return err
		}
		for _, methodSet := range []*types.MethodSet{types.NewMethodSet(current), types.NewMethodSet(types.NewPointer(current))} {
			for index := 0; index < methodSet.Len(); index++ {
				method := methodSet.At(index).Obj()
				if method.Exported() {
					if err := walkPublicTypeState(method.Type(), seen, approvals, nil); err != nil {
						return err
					}
				}
			}
		}
	case *types.Pointer:
		return walkPublicTypeState(current.Elem(), seen, approvals, nil)
	case *types.Slice:
		return walkPublicTypeState(current.Elem(), seen, approvals, nil)
	case *types.Array:
		return walkPublicTypeState(current.Elem(), seen, approvals, nil)
	case *types.Map:
		if err := walkPublicTypeState(current.Key(), seen, approvals, nil); err != nil {
			return err
		}
		return walkPublicTypeState(current.Elem(), seen, approvals, nil)
	case *types.Chan:
		return walkPublicTypeState(current.Elem(), seen, approvals, nil)
	case *types.Struct:
		for index := 0; index < current.NumFields(); index++ {
			field := current.Field(index)
			if field.Exported() {
				if err := walkPublicTypeState(field.Type(), seen, approvals, nil); err != nil {
					return err
				}
			}
		}
	case *types.Signature:
		if current.Recv() != nil {
			if err := walkPublicTypeState(current.Recv().Type(), seen, approvals, nil); err != nil {
				return err
			}
		}
		if err := walkTuple(current.Params(), seen, approvals); err != nil {
			return err
		}
		if err := walkTuple(current.Results(), seen, approvals); err != nil {
			return err
		}
		if err := walkTypeParams(current.TypeParams(), seen, approvals); err != nil {
			return err
		}
		return walkTypeParams(current.RecvTypeParams(), seen, approvals)
	case *types.Interface:
		for index := 0; index < current.NumExplicitMethods(); index++ {
			if err := walkPublicTypeState(current.ExplicitMethod(index).Type(), seen, approvals, nil); err != nil {
				return err
			}
		}
		for index := 0; index < current.NumEmbeddeds(); index++ {
			if err := walkPublicTypeState(current.EmbeddedType(index), seen, approvals, nil); err != nil {
				return err
			}
		}
		for index := 0; index < current.NumMethods(); index++ {
			if err := walkPublicTypeState(current.Method(index).Type(), seen, approvals, nil); err != nil {
				return err
			}
		}
	case *types.Union:
		for index := 0; index < current.Len(); index++ {
			if err := walkPublicTypeState(current.Term(index).Type(), seen, approvals, nil); err != nil {
				return err
			}
		}
	case *types.TypeParam:
		return walkPublicTypeState(current.Constraint(), seen, approvals, nil)
	}
	return nil
}

func approvedAliasTargetObject(object *types.TypeName, approvals publicAliasApprovals) bool {
	for _, target := range approvals {
		if object == target {
			return true
		}
	}
	return false
}

func approvedAliasTarget(value *types.Alias, approvals publicAliasApprovals) *types.TypeName {
	want, ok := approvals[value.Obj()]
	if !ok {
		return nil
	}
	target, ok := types.Unalias(value.Rhs()).(*types.Named)
	if !ok || target.Obj() != want {
		return nil
	}
	return want
}

func inaccessiblePackage(pkg *types.Package) bool {
	if pkg == nil {
		return false
	}
	path := pkg.Path()
	return path == "internal" || strings.Contains(path, "/internal/")
}

func walkTuple(tuple *types.Tuple, seen map[types.Type]bool, approvals publicAliasApprovals) error {
	if tuple == nil {
		return nil
	}
	for index := 0; index < tuple.Len(); index++ {
		if err := walkPublicTypeState(tuple.At(index).Type(), seen, approvals, nil); err != nil {
			return err
		}
	}
	return nil
}

func walkTypeParams(params *types.TypeParamList, seen map[types.Type]bool, approvals publicAliasApprovals) error {
	if params == nil {
		return nil
	}
	for index := 0; index < params.Len(); index++ {
		if err := walkPublicTypeState(params.At(index).Constraint(), seen, approvals, nil); err != nil {
			return err
		}
	}
	return nil
}

func walkTypeArgs(args *types.TypeList, seen map[types.Type]bool, approvals publicAliasApprovals) error {
	if args == nil {
		return nil
	}
	for index := 0; index < args.Len(); index++ {
		if err := walkPublicTypeState(args.At(index), seen, approvals, nil); err != nil {
			return err
		}
	}
	return nil
}
