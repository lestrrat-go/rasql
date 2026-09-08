package external_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
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
