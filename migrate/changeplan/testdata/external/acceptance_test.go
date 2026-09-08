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
	require.Equal(t, fixture.statement.SQL(), operation.Statements()[0].SQL())
	require.Empty(t, operation.Statements()[0].Args())
	require.Empty(t, operation.ReverseStatements())
	require.Equal(t, fixture.precondition.Object(), operation.Preconditions()[0].Object())
	require.Equal(t, fixture.precondition.Path(), operation.Preconditions()[0].Path())
	require.Equal(t, fixture.precondition.Operator(), operation.Preconditions()[0].Operator())
	require.Equal(t, fixture.precondition.CanonicalValue(), operation.Preconditions()[0].CanonicalValue())
	require.Equal(t, fixture.postcondition.Object(), operation.Postconditions()[0].Object())
	require.Equal(t, fixture.postcondition.Path(), operation.Postconditions()[0].Path())
	require.Equal(t, fixture.postcondition.Operator(), operation.Postconditions()[0].Operator())
	require.Equal(t, fixture.postcondition.CanonicalValue(), operation.Postconditions()[0].CanonicalValue())

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
	require.NotEqual(t, changeplan.PlanID{}, decoded.ID())
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
	for _, name := range pkg.Scope().Names() {
		object := pkg.Scope().Lookup(name)
		if !object.Exported() {
			continue
		}
		require.NoError(t, walkPublicType(object.Type(), map[types.Type]bool{}), name)
	}

	internalPkg := types.NewPackage("example.com/rasql/internal/synthetic", "synthetic")
	publicPkg := types.NewPackage("example.com/rasql/publicfixture", "publicfixture")
	hidden := types.NewNamed(types.NewTypeName(token.NoPos, internalPkg, "Hidden", nil), types.Typ[types.String], nil)
	syntheticPkg := types.NewPackage("example.com/synthetic", "synthetic")
	parameter := types.NewVar(token.NoPos, syntheticPkg, "value", hidden)
	synthetic := types.NewSignatureType(nil, nil, nil, types.NewTuple(parameter), types.NewTuple(), false)
	t.Run("direct hidden signature", func(t *testing.T) {
		require.Error(t, walkPublicType(synthetic, map[types.Type]bool{}))
	})

	t.Run("unapproved alias", func(t *testing.T) {
		alias := types.NewAlias(types.NewTypeName(token.NoPos, publicPkg, "LeakedAlias", nil), hidden)
		require.Error(t, walkPublicType(alias, map[types.Type]bool{}))
	})
	t.Run("pointer receiver exported method", func(t *testing.T) {
		public := types.NewNamed(types.NewTypeName(token.NoPos, publicPkg, "Container", nil), types.NewStruct(nil, nil), nil)
		receiver := types.NewVar(token.NoPos, publicPkg, "receiver", types.NewPointer(public))
		parameter := types.NewVar(token.NoPos, publicPkg, "value", hidden)
		signature := types.NewSignatureType(receiver, nil, nil, types.NewTuple(parameter), types.NewTuple(), false)
		public.AddMethod(types.NewFunc(token.NoPos, publicPkg, "Set", signature))
		require.Error(t, walkPublicType(public, map[types.Type]bool{}))
	})
	t.Run("unrelated internal type with approved spelling", func(t *testing.T) {
		collision := types.NewNamed(types.NewTypeName(token.NoPos, internalPkg, "EngineID", nil), types.Typ[types.String], nil)
		require.Error(t, walkPublicType(collision, map[types.Type]bool{}))
	})
	t.Run("named generic constraint", func(t *testing.T) {
		constraint := types.NewInterfaceType(nil, []types.Type{hidden}).Complete()
		parameter := types.NewTypeParam(types.NewTypeName(token.NoPos, publicPkg, "T", nil), constraint)
		public := types.NewNamed(types.NewTypeName(token.NoPos, publicPkg, "Generic", nil), types.NewStruct(nil, nil), nil)
		public.SetTypeParams([]*types.TypeParam{parameter})
		require.Error(t, walkPublicType(public, map[types.Type]bool{}))
	})
	t.Run("embedded constraint union", func(t *testing.T) {
		union := types.NewUnion([]*types.Term{types.NewTerm(false, hidden), types.NewTerm(false, types.Typ[types.Int])})
		constraint := types.NewInterfaceType(nil, []types.Type{union}).Complete()
		require.Error(t, walkPublicType(constraint, map[types.Type]bool{}))
	})

	t.Run("approved aliases", func(t *testing.T) {
		for name := range approvedPublicAliases {
			object := pkg.Scope().Lookup(name)
			require.NotNil(t, object)
			require.NoError(t, walkPublicType(object.Type(), map[types.Type]bool{}), name)
		}
	})
	t.Run("safe pointer receiver", func(t *testing.T) {
		public := types.NewNamed(types.NewTypeName(token.NoPos, publicPkg, "SafeContainer", nil), types.NewStruct(nil, nil), nil)
		receiver := types.NewVar(token.NoPos, publicPkg, "receiver", types.NewPointer(public))
		parameter := types.NewVar(token.NoPos, publicPkg, "value", types.Typ[types.String])
		signature := types.NewSignatureType(receiver, nil, nil, types.NewTuple(parameter), types.NewTuple(), false)
		public.AddMethod(types.NewFunc(token.NoPos, publicPkg, "Set", signature))
		require.NoError(t, walkPublicType(public, map[types.Type]bool{}))
	})
	t.Run("public generic constraint", func(t *testing.T) {
		term := types.NewUnion([]*types.Term{types.NewTerm(true, types.Typ[types.String])})
		constraint := types.NewInterfaceType(nil, []types.Type{term}).Complete()
		parameter := types.NewTypeParam(types.NewTypeName(token.NoPos, publicPkg, "T", nil), constraint)
		public := types.NewNamed(types.NewTypeName(token.NoPos, publicPkg, "GenericPublic", nil), types.NewStruct(nil, nil), nil)
		public.SetTypeParams([]*types.TypeParam{parameter})
		require.NoError(t, walkPublicType(public, map[types.Type]bool{}))
	})
	t.Run("public union", func(t *testing.T) {
		union := types.NewUnion([]*types.Term{types.NewTerm(false, types.Typ[types.Int]), types.NewTerm(false, types.Typ[types.String])})
		constraint := types.NewInterfaceType(nil, []types.Type{union}).Complete()
		require.NoError(t, walkPublicType(constraint, map[types.Type]bool{}))
	})
}

type publicAliasTarget struct {
	packagePath string
	name        string
}

var approvedPublicAliases = map[string]publicAliasTarget{
	"EngineID":           {packagePath: "github.com/lestrrat-go/rasql/internal/engineprofile", name: "EngineID"},
	"EngineVersion":      {packagePath: "github.com/lestrrat-go/rasql/internal/engineprofile", name: "Version"},
	"EngineCapabilities": {packagePath: "github.com/lestrrat-go/rasql/internal/engineprofile", name: "Capabilities"},
	"EngineLimits":       {packagePath: "github.com/lestrrat-go/rasql/internal/engineprofile", name: "Limits"},
}

func walkPublicType(value types.Type, seen map[types.Type]bool) error {
	return walkPublicTypeState(value, seen, false)
}

func walkPublicTypeState(value types.Type, seen map[types.Type]bool, allowAliasTarget bool) error {
	if value == nil || seen[value] {
		return nil
	}
	seen[value] = true
	defer delete(seen, value)
	switch current := value.(type) {
	case *types.Alias:
		approved := approvedAliasTarget(current)
		if err := walkTypeParams(current.TypeParams(), seen, allowAliasTarget); err != nil {
			return err
		}
		if err := walkTypeArgs(current.TypeArgs(), seen, allowAliasTarget); err != nil {
			return err
		}
		if err := walkPublicTypeState(current.Rhs(), seen, approved); err != nil {
			return err
		}
		return walkPublicTypeState(types.Unalias(current), seen, approved)
	case *types.Named:
		object := current.Obj()
		if inaccessiblePackage(object.Pkg()) && !allowAliasTarget {
			return fmt.Errorf("inaccessible type %s", types.TypeString(value, nil))
		}
		if err := walkTypeArgs(current.TypeArgs(), seen, allowAliasTarget); err != nil {
			return err
		}
		if err := walkTypeParams(current.TypeParams(), seen, allowAliasTarget); err != nil {
			return err
		}
		if err := walkPublicTypeState(current.Underlying(), seen, allowAliasTarget); err != nil {
			return err
		}
		for _, methodSet := range []*types.MethodSet{types.NewMethodSet(current), types.NewMethodSet(types.NewPointer(current))} {
			for index := 0; index < methodSet.Len(); index++ {
				method := methodSet.At(index).Obj()
				if method.Exported() {
					if err := walkPublicTypeState(method.Type(), seen, allowAliasTarget); err != nil {
						return err
					}
				}
			}
		}
	case *types.Pointer:
		return walkPublicTypeState(current.Elem(), seen, allowAliasTarget)
	case *types.Slice:
		return walkPublicTypeState(current.Elem(), seen, allowAliasTarget)
	case *types.Array:
		return walkPublicTypeState(current.Elem(), seen, allowAliasTarget)
	case *types.Map:
		if err := walkPublicTypeState(current.Key(), seen, allowAliasTarget); err != nil {
			return err
		}
		return walkPublicTypeState(current.Elem(), seen, allowAliasTarget)
	case *types.Chan:
		return walkPublicTypeState(current.Elem(), seen, allowAliasTarget)
	case *types.Struct:
		for index := 0; index < current.NumFields(); index++ {
			field := current.Field(index)
			if field.Exported() {
				if err := walkPublicTypeState(field.Type(), seen, allowAliasTarget); err != nil {
					return err
				}
			}
		}
	case *types.Signature:
		if current.Recv() != nil {
			if err := walkPublicTypeState(current.Recv().Type(), seen, allowAliasTarget); err != nil {
				return err
			}
		}
		if err := walkTuple(current.Params(), seen, allowAliasTarget); err != nil {
			return err
		}
		if err := walkTuple(current.Results(), seen, allowAliasTarget); err != nil {
			return err
		}
		if err := walkTypeParams(current.TypeParams(), seen, allowAliasTarget); err != nil {
			return err
		}
		return walkTypeParams(current.RecvTypeParams(), seen, allowAliasTarget)
	case *types.Interface:
		for index := 0; index < current.NumExplicitMethods(); index++ {
			if err := walkPublicTypeState(current.ExplicitMethod(index).Type(), seen, allowAliasTarget); err != nil {
				return err
			}
		}
		for index := 0; index < current.NumEmbeddeds(); index++ {
			if err := walkPublicTypeState(current.EmbeddedType(index), seen, allowAliasTarget); err != nil {
				return err
			}
		}
		for index := 0; index < current.NumMethods(); index++ {
			if err := walkPublicTypeState(current.Method(index).Type(), seen, allowAliasTarget); err != nil {
				return err
			}
		}
	case *types.Union:
		for index := 0; index < current.Len(); index++ {
			if err := walkPublicTypeState(current.Term(index).Type(), seen, allowAliasTarget); err != nil {
				return err
			}
		}
	case *types.TypeParam:
		return walkPublicTypeState(current.Constraint(), seen, allowAliasTarget)
	}
	return nil
}

func approvedAliasTarget(value *types.Alias) bool {
	object := value.Obj()
	want, ok := approvedPublicAliases[object.Name()]
	if !ok || object.Pkg() == nil || object.Pkg().Path() != "github.com/lestrrat-go/rasql/migrate/changeplan" {
		return false
	}
	target, ok := types.Unalias(value.Rhs()).(*types.Named)
	return ok && target.Obj().Pkg() != nil && target.Obj().Pkg().Path() == want.packagePath && target.Obj().Name() == want.name
}

func inaccessiblePackage(pkg *types.Package) bool {
	if pkg == nil {
		return false
	}
	path := pkg.Path()
	return path == "internal" || strings.Contains(path, "/internal/")
}

func walkTuple(tuple *types.Tuple, seen map[types.Type]bool, allowAliasTarget bool) error {
	if tuple == nil {
		return nil
	}
	for index := 0; index < tuple.Len(); index++ {
		if err := walkPublicTypeState(tuple.At(index).Type(), seen, allowAliasTarget); err != nil {
			return err
		}
	}
	return nil
}

func walkTypeParams(params *types.TypeParamList, seen map[types.Type]bool, allowAliasTarget bool) error {
	if params == nil {
		return nil
	}
	for index := 0; index < params.Len(); index++ {
		if err := walkPublicTypeState(params.At(index).Constraint(), seen, allowAliasTarget); err != nil {
			return err
		}
	}
	return nil
}

func walkTypeArgs(args *types.TypeList, seen map[types.Type]bool, allowAliasTarget bool) error {
	if args == nil {
		return nil
	}
	for index := 0; index < args.Len(); index++ {
		if err := walkPublicTypeState(args.At(index), seen, allowAliasTarget); err != nil {
			return err
		}
	}
	return nil
}
