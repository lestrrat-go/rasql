package changeplan_test

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

func TestTypedFactWalkerTraversesD2CatalogFields(t *testing.T) {
	definition := schema.TableDef{
		Schema: "public",
		Name:   "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
		},
		ExclusionConstraints: []schema.ExclusionDef{{
			Name:     "orders_exclusion",
			Method:   "gist",
			Elements: []schema.ExclusionElementDef{{Expression: "id", Operator: "="}},
		}},
	}
	object, err := changeplan.NewCatalogObject("orders-id", definition)
	require.NoError(t, err)
	catalog, err := changeplan.NewCatalog(testProfile(t), "exclusion-source", []changeplan.CatalogObject{object})
	require.NoError(t, err)

	for _, test := range []struct {
		path  string
		value string
	}{
		{path: "/id", value: `"orders-id"`},
		{path: "/columns/0/name", value: `"id"`},
		{path: "/exclusion_constraints/0/name", value: `"orders_exclusion"`},
		{path: "/exclusion_constraints/0/elements/0/expression_sql", value: `"id"`},
		{path: "/exclusion_constraints/0/elements/0/operator", value: `"="`},
	} {
		fact, factErr := changeplan.NewFact(object.ID(), test.path, changeplan.FactEqual, test.value)
		require.NoError(t, factErr)
		require.NoError(t, changeplan.EvaluateFact(catalog, fact), test.path)
	}

	structFact, err := changeplan.NewFact(object.ID(), "/columns/0", changeplan.FactEqual, `{"name":"id"}`)
	require.NoError(t, err)
	err = changeplan.EvaluateFact(catalog, structFact)
	require.ErrorIs(t, err, changeplan.ErrInvalidFact)
}

func TestPlanRenameDecisionUsesStructuredTableBinding(t *testing.T) {
	starting, err := changeplan.NewBaselineObject("starting", "table", "main", "users")
	require.NoError(t, err)
	profile := testProfile(t)
	profileDigest, err := changeplan.ProfileDigest(profile)
	require.NoError(t, err)
	identity, err := changeplan.NewCatalogIdentity(profile.Engine(), profileDigest, changeplan.Digest{1}, changeplan.Digest{2})
	require.NoError(t, err)
	rename, err := changeplan.NewBaselineRename("rename", starting.ID(), "main", "accounts")
	require.NoError(t, err)
	baseline, err := changeplan.NewBaselineIdentity(identity, "source", []changeplan.BaselineObject{starting}, []changeplan.BaselineRename{rename})
	require.NoError(t, err)
	history, err := changeplan.NewHistoryIdentity("main", "schema_migrations")
	require.NoError(t, err)
	decision, err := changeplan.NewDecision("rename-decision", changeplan.DecisionRenameObject, starting.ID(), "main.wrong", "main.other", true, "")
	require.NoError(t, err)
	operation, err := changeplan.NewOperation("rename", changeplan.OperationRenameTable, nil, []changeplan.ObjectID{starting.ID()}, nil, nil,
		[]stmt.Statement{stmt.New(sqltext.Text("ALTER TABLE users RENAME TO accounts"))}, changeplan.TransactionEngineDefault, false, nil)
	require.NoError(t, err)
	_, err = changeplan.NewPlan(profile, baseline, history, []changeplan.Decision{decision}, []changeplan.Operation{operation})
	require.ErrorIs(t, err, changeplan.ErrInvalidDecision)
}

func TestResolvedColumnRenameUsesAdjacentCatalogs(t *testing.T) {
	profile := testProfile(t)
	beforeObject, err := changeplan.NewCatalogObject("task", schema.TableDef{Schema: "main", Name: "tasks", Columns: []schema.ColumnDef{{Name: "old_name", Type: schema.TextType{}}}})
	require.NoError(t, err)
	baseline, err := changeplan.NewCatalog(profile, "source", []changeplan.CatalogObject{beforeObject})
	require.NoError(t, err)
	afterObject, err := changeplan.NewCatalogObject("task", schema.TableDef{Schema: "main", Name: "tasks", Columns: []schema.ColumnDef{{Name: "new_name", Type: schema.TextType{}}}})
	require.NoError(t, err)
	after, err := changeplan.NewCatalogLike(baseline, []changeplan.CatalogObject{afterObject})
	require.NoError(t, err)
	operation, err := changeplan.NewOperation("rename-column", changeplan.OperationRenameColumn, nil, []changeplan.ObjectID{"task"}, nil, nil,
		[]stmt.Statement{stmt.New(sqltext.Text("ALTER TABLE tasks RENAME COLUMN old_name TO new_name"))}, changeplan.TransactionEngineDefault, false, nil)
	require.NoError(t, err)
	step, err := changeplan.NewResolvedCatalogStep(operation.ID(), after)
	require.NoError(t, err)
	decision, err := changeplan.NewDecision("rename-column-decision", changeplan.DecisionRenameObject, "task", "old_name", "new_name", true, "")
	require.NoError(t, err)
	_, err = changeplan.NewResolvedChanges(baseline, []changeplan.ResolvedCatalogStep{step}, []changeplan.Decision{decision}, []changeplan.Operation{operation}, []changeplan.BaselineObject{}, []changeplan.BaselineRename{})
	require.NoError(t, err)

	wrong, err := changeplan.NewDecision("wrong", changeplan.DecisionRenameObject, "task", "wrong", "new_name", true, "")
	require.NoError(t, err)
	_, err = changeplan.NewResolvedChanges(baseline, []changeplan.ResolvedCatalogStep{step}, []changeplan.Decision{wrong}, []changeplan.Operation{operation}, []changeplan.BaselineObject{}, []changeplan.BaselineRename{})
	require.ErrorIs(t, err, changeplan.ErrInvalidDecision)
}

func TestDecodeRejectsNonStringOperationArrayElements(t *testing.T) {
	fixture, err := os.ReadFile("testdata/v1/valid/full.json")
	require.NoError(t, err)
	for _, field := range []string{"depends_on", "objects"} {
		for _, element := range [][]byte{[]byte("null"), []byte("true"), []byte("1"), []byte(`{}`), []byte(`[]`)} {
			needle := []byte(`"` + field + `":[]`)
			replacement := []byte(`"` + field + `":[` + string(element) + `]`)
			if field == "objects" {
				needle = []byte(`"objects":["starting"]`)
				replacement = []byte(`"objects":[` + string(element) + `]`)
			}
			data := bytes.Replace(fixture, needle, replacement, 1)
			_, decodeErr := changeplan.Decode(data)
			require.Error(t, decodeErr, field)
			require.True(t, errors.Is(decodeErr, changeplan.ErrInvalidWire), field)
		}
	}
}
