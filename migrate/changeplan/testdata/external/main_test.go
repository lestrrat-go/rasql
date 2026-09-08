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

func TestPublicProfileAndLockBoundary(t *testing.T) {
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 45, 0)
	require.NoError(t, err)
	history, err := changeplan.NewHistoryIdentity("main", "schema_migrations")
	require.NoError(t, err)
	lockBytes, err := os.ReadFile("lock.json")
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
	after, err := changeplan.NewCatalogLike(baseline, []changeplan.CatalogObject{afterObject})
	require.NoError(t, err)
	precondition, err := changeplan.NewFact(startingID, "/columns/0/name", changeplan.FactOperatorEqual, `"id"`)
	require.NoError(t, err)
	postcondition, err := changeplan.NewFact(startingID, "/columns/2/name", changeplan.FactOperatorEqual, `"title"`)
	require.NoError(t, err)
	resultDigest, err := changeplan.CatalogDigest(after)
	require.NoError(t, err)
	operation, err := changeplan.NewOperation("add-title", changeplan.OperationAddColumn, []changeplan.OperationID{}, []changeplan.ObjectID{startingID}, []changeplan.Fact{precondition}, []changeplan.Fact{postcondition}, resultDigest, []stmt.Statement{stmt.New(sqltext.Text(`ALTER TABLE "main"."tasks" ADD COLUMN "title" TEXT NOT NULL`))}, changeplan.TransactionEngineDefault, false, []stmt.Statement{})
	require.NoError(t, err)
	step, err := changeplan.NewResolvedCatalogStep(operation.ID(), after)
	require.NoError(t, err)
	resolved, err := changeplan.NewResolvedChanges(baseline, []changeplan.ResolvedCatalogStep{step}, []changeplan.Decision{}, []changeplan.Operation{operation}, []changeplan.BaselineObject{}, []changeplan.BaselineRename{})
	require.NoError(t, err)
	plan, err := changeplan.FromLock(lockBytes, profile, history, resolved)
	require.NoError(t, err)
	require.NotEqual(t, changeplan.PlanID{}, plan.ID())
	require.Equal(t, 1, len(plan.Operations()))
	require.Equal(t, operation.Statements()[0].SQL(), plan.Operations()[0].Statements()[0].SQL())
	encoded, err := changeplan.Encode(plan)
	require.NoError(t, err)
	nonCanonical := bytes.Replace(encoded, []byte(`"value":"\"title\""`), []byte(`"value":" \"title\" "`), 1)
	_, err = changeplan.Decode(nonCanonical)
	require.Error(t, err)
	decoded, err := changeplan.Decode(encoded)
	require.NoError(t, err)
	reencoded, err := changeplan.Encode(decoded)
	require.NoError(t, err)
	require.Equal(t, encoded, reencoded)
	digest, err := changeplan.ProfileDigest(profile)
	require.NoError(t, err)
	custom, err := rasql.NewCustomEngineProfile("acme", profile.Version(), profile.Capabilities(), profile.Limits())
	require.NoError(t, err)
	customDigest, err := changeplan.ProfileDigest(custom)
	require.NoError(t, err)
	require.NotEqual(t, digest, customDigest)
	_, err = rasql.NewCustomEngineProfile("", profile.Version(), profile.Capabilities(), profile.Limits())
	require.Error(t, err)
}
