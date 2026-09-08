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

func TestExternalNonemptyPlanContract(t *testing.T) {
	lockBytes, err := os.ReadFile("lock.json")
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 45, 0)
	require.NoError(t, err)
	baseline, err := changeplan.CatalogFromLock(lockBytes)
	require.NoError(t, err)
	_, err = changeplan.CatalogFromLock(append(append([]byte(nil), lockBytes...), []byte(`{}`)...))
	require.Error(t, err)
	_, err = changeplan.CatalogFromLock([]byte(`{"format":2}`))
	require.Error(t, err)
	startingID, ok := baseline.ObjectID(schema.ObjectTable, "main", "tasks")
	require.True(t, ok)
	require.Equal(t, changeplan.ObjectID("tasks"), startingID)
	definition := schema.TableDef{Schema: "main", Name: "tasks", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "payload", Type: schema.BytesType{}},
		{Name: "title", Type: schema.TextType{}},
	}, PrimaryKey: []string{"id"}}
	afterObject, err := changeplan.NewCatalogObject(startingID, definition)
	require.NoError(t, err)
	after, err := changeplan.NewCatalogLike(baseline, []changeplan.CatalogObject{afterObject})
	require.NoError(t, err)
	precondition, err := changeplan.NewFact(startingID, "/columns/0/name", changeplan.FactOperatorEqual, `"id"`)
	require.NoError(t, err)
	postcondition, err := changeplan.NewFact(startingID, "/columns/2/name", changeplan.FactOperatorEqual, `"title"`)
	require.NoError(t, err)
	statement := stmt.New(sqltext.Text(`ALTER TABLE "main"."tasks" ADD COLUMN "title" TEXT NOT NULL`))
	operation, err := changeplan.NewOperation("add-title", changeplan.OperationAddColumn, []changeplan.OperationID{}, []changeplan.ObjectID{startingID},
		[]changeplan.Fact{precondition}, []changeplan.Fact{postcondition}, []stmt.Statement{statement}, changeplan.TransactionEngineDefault, false, []stmt.Statement{})
	require.NoError(t, err)
	require.Empty(t, statement.Args())
	step, err := changeplan.NewResolvedCatalogStep(operation.ID(), after)
	require.NoError(t, err)
	resolved, err := changeplan.NewResolvedChanges(baseline, []changeplan.ResolvedCatalogStep{step}, []changeplan.Decision{}, []changeplan.Operation{operation}, []changeplan.BaselineObject{}, []changeplan.BaselineRename{})
	require.NoError(t, err)
	history, err := changeplan.NewHistoryIdentity("main", "schema_migrations")
	require.NoError(t, err)
	plan, err := changeplan.FromLock(lockBytes, profile, history, resolved)
	require.NoError(t, err)
	wrongVersion, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 44, 0)
	require.NoError(t, err)
	_, err = changeplan.FromLock(lockBytes, wrongVersion, history, resolved)
	require.Error(t, err)
	require.NotEqual(t, changeplan.PlanID{}, plan.ID())
	require.Equal(t, changeplan.ObjectID("tasks"), plan.Baseline().Objects()[0].ID())
	require.Equal(t, baseline.SourceIdentity(), plan.Baseline().SourceIdentity())
	require.Equal(t, operation.ID(), plan.Operations()[0].ID())
	require.Equal(t, operation.Kind(), plan.Operations()[0].Kind())
	require.Equal(t, statement.SQL(), plan.Operations()[0].Statements()[0].SQL())
	require.Empty(t, plan.Operations()[0].Statements()[0].Args())
	require.Equal(t, []changeplan.Fact{precondition}, plan.Operations()[0].Preconditions())
	require.Equal(t, []changeplan.Fact{postcondition}, plan.Operations()[0].Postconditions())
	require.Equal(t, baseline.SourceIdentity(), plan.Baseline().SourceIdentity())
	wantBaselineID, wantBaselineOK := baseline.ObjectID(schema.ObjectTable, "main", "tasks")
	gotBaselineID, gotBaselineOK := func() (changeplan.ObjectID, bool) {
		objects := plan.Baseline().Objects()
		if len(objects) == 0 {
			return "", false
		}
		return objects[0].ID(), true
	}()
	require.Equal(t, wantBaselineOK, gotBaselineOK)
	require.Equal(t, wantBaselineID, gotBaselineID)
	encoded, err := changeplan.Encode(plan)
	require.NoError(t, err)
	decoded, err := changeplan.Decode(encoded)
	require.NoError(t, err)
	reencoded, err := changeplan.Encode(decoded)
	require.NoError(t, err)
	require.True(t, bytes.Equal(encoded, reencoded))
}

func TestPublicAPIHasNoInaccessibleTypes(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root := filepath.Join(filepath.Dir(filename), "../../../..")
	config := &packages.Config{Mode: packages.NeedTypes, Dir: root}
	loaded, err := packages.Load(config, "github.com/lestrrat-go/rasql/migrate/changeplan")
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Empty(t, loaded[0].Errors)
	pkg := loaded[0].Types
	for _, name := range pkg.Scope().Names() {
		object := pkg.Scope().Lookup(name)
		if !object.Exported() {
			continue
		}
		require.NoError(t, walkPublicType(object.Type(), map[types.Type]bool{}), name)
	}

	internalPackage := types.NewPackage("example.com/rasql/internal/synthetic", "synthetic")
	hidden := types.NewNamed(types.NewTypeName(token.NoPos, internalPackage, "Hidden", nil), types.Typ[types.String], nil)
	syntheticPackage := types.NewPackage("example.com/synthetic", "synthetic")
	parameter := types.NewVar(token.NoPos, syntheticPackage, "value", hidden)
	synthetic := types.NewSignatureType(nil, nil, nil, types.NewTuple(parameter), types.NewTuple(), false)
	require.Error(t, walkPublicType(synthetic, map[types.Type]bool{}))
}

var approvedPublicAliases = map[string]bool{
	"EngineID":           true,
	"EngineVersion":      true,
	"EngineCapabilities": true,
	"EngineLimits":       true,
}

func walkPublicType(value types.Type, seen map[types.Type]bool) error {
	if value == nil || seen[value] {
		return nil
	}
	seen[value] = true
	switch current := value.(type) {
	case *types.Named:
		object := current.Obj()
		if object.Pkg() != nil && strings.Contains(object.Pkg().Path(), "/internal/") && !approvedPublicAliases[object.Name()] {
			return fmt.Errorf("inaccessible type %s", types.TypeString(value, nil))
		}
		for index := 0; index < current.TypeArgs().Len(); index++ {
			if err := walkPublicType(current.TypeArgs().At(index), seen); err != nil {
				return err
			}
		}
		if err := walkPublicType(current.Underlying(), seen); err != nil {
			return err
		}
		methodSet := types.NewMethodSet(current)
		for index := 0; index < methodSet.Len(); index++ {
			method := methodSet.At(index).Obj()
			if method.Exported() {
				if err := walkPublicType(method.Type(), seen); err != nil {
					return err
				}
			}
		}
	case *types.Pointer:
		return walkPublicType(current.Elem(), seen)
	case *types.Slice:
		return walkPublicType(current.Elem(), seen)
	case *types.Array:
		return walkPublicType(current.Elem(), seen)
	case *types.Map:
		if err := walkPublicType(current.Key(), seen); err != nil {
			return err
		}
		return walkPublicType(current.Elem(), seen)
	case *types.Chan:
		return walkPublicType(current.Elem(), seen)
	case *types.Struct:
		for index := 0; index < current.NumFields(); index++ {
			field := current.Field(index)
			if field.Exported() {
				if err := walkPublicType(field.Type(), seen); err != nil {
					return err
				}
			}
		}
	case *types.Signature:
		if current.Recv() != nil {
			if err := walkPublicType(current.Recv().Type(), seen); err != nil {
				return err
			}
		}
		if err := walkTuple(current.Params(), seen); err != nil {
			return err
		}
		if err := walkTuple(current.Results(), seen); err != nil {
			return err
		}
		if current.TypeParams() != nil {
			for index := 0; index < current.TypeParams().Len(); index++ {
				if err := walkPublicType(current.TypeParams().At(index).Constraint(), seen); err != nil {
					return err
				}
			}
		}
	case *types.Interface:
		for index := 0; index < current.NumMethods(); index++ {
			method := current.Method(index)
			if method.Exported() {
				if err := walkPublicType(method.Type(), seen); err != nil {
					return err
				}
			}
		}
	case *types.TypeParam:
		return walkPublicType(current.Constraint(), seen)
	}
	return nil
}

func walkTuple(tuple *types.Tuple, seen map[types.Type]bool) error {
	for index := 0; index < tuple.Len(); index++ {
		if err := walkPublicType(tuple.At(index).Type(), seen); err != nil {
			return err
		}
	}
	return nil
}
