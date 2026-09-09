package changeplan_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

var goldenCatalogBytes = []byte(`{"engine":{"dialect":"sqlite","version":"3.35.0","profile":"sqlite-3.35"},"objects":[{"id":"starting","kind":"table","schema":"main","name":"users","columns":[{"name":"id","ordinal":0,"logical_kind":"integer","native":null,"nullable":false,"default_sql":"","generated_sql":"","generated_storage":"","identity":"","collation":"","hidden":false,"integer":{"unsigned":false,"display_width":{"value":0,"set":false},"zero_fill":false},"text":null,"decimal":null},{"name":"obsolete","ordinal":1,"logical_kind":"text","native":null,"nullable":false,"default_sql":"","generated_sql":"","generated_storage":"","identity":"","collation":"","hidden":false,"integer":null,"text":{"width":{"value":0,"set":false},"fixed":false},"decimal":null}],"constraints":[{"name":"","kind":"primary_key","columns":["id"],"reference":null,"expression_sql":"","deferrable":false,"initially_deferred":false,"on_update":"","on_delete":"","deferrability":"","match":"","nulls_not_distinct":false,"include_columns":null,"on_conflict":"","keys":[],"temporal":false,"storage_parameters":[],"tablespace":"","replica_identity":false,"collations":[],"no_inherit":false,"not_valid":false,"not_enforced":false,"delete_set_columns":null}],"indexes":[],"exclusion_constraints":[],"strict":false,"without_rowid":false,"primary_key_autoincrement":false,"primary_key_on_conflict":"","virtual_table_module":"","virtual_table_module_arguments":null}]}`)

var goldenProfileBytes = []byte(`{"engine":"sqlite","custom_name":"","version_known":true,"version":{"major":3,"minor":35,"patch":0},"max_bind_parameters":999,"capabilities":{"returning":"all","upsert":"on_conflict","conflict_target":true,"default_values":true,"empty_insert":false,"default_values_upsert":false,"subquery_limit":true,"write_subquery_target":true,"partial_index":true,"aggregate_filter":true,"qualified_reference":false,"qualified_index_target":false,"qualified_index_name":true,"match_operator":true,"select_for_update":false,"select_for_share":false,"select_lock_of":false,"select_lock_no_wait":false,"select_lock_skip_locked":false,"upsert_conflict_where":true,"upsert_update_where":true,"window_functions":true,"lateral_joins":false,"savepoints":true,"transactional_ddl":true,"explicit_null_ordering":true,"tuple_comparison":true,"per_parent_limit":"window","update_default":"unsupported"}}`)

var goldenResultDigests = map[string]string{
	"create-table":     "32cbd61ccba1d1960ab76dc1a5bc60d0c9c9e83b8466cf0aaef3532e2fb52c3f",
	"add-column":       "b90d0fa234b224995f0025b9fc66853b528ffb8b3a480bcd74c0e38461a6cada",
	"drop-column":      "aa0b1d569af438bd836975964db704ede1faa4ab386dc82afb343ca4f8ed1651",
	"rename-column":    "3d1e48907434b56cf1c2b8ce79a55afb5276affcac0f36a25c6e86d0d7995bb1",
	"alter-column":     "3d1e48907434b56cf1c2b8ce79a55afb5276affcac0f36a25c6e86d0d7995bb1",
	"create-index":     "fb1e53d4f269f65a90faee4fd7544d5c17d0449786eaec790492ac04a3fa64c4",
	"drop-index":       "3d1e48907434b56cf1c2b8ce79a55afb5276affcac0f36a25c6e86d0d7995bb1",
	"add-constraint":   "4ecfb0b7c9362e6ac992adf9a8745a2e2163c737996f655e18dde6b95ab5119f",
	"drop-constraint":  "3d1e48907434b56cf1c2b8ce79a55afb5276affcac0f36a25c6e86d0d7995bb1",
	"backfill":         "3d1e48907434b56cf1c2b8ce79a55afb5276affcac0f36a25c6e86d0d7995bb1",
	"native-sql":       "3d1e48907434b56cf1c2b8ce79a55afb5276affcac0f36a25c6e86d0d7995bb1",
	"rename-created-1": "62f7d874ef7e87a62a96c4d5cf76c6436a609ba72f867894ccfed1f52074b5ca",
	"rename-created-2": "c2b6b441719f9fe7f63b1151cf77e11d01ebeb1635efdf100dedf95b62f08971",
	"drop-table":       "759ac5d2e8de5fd504fb4f413bd4d23ecf5451ea74c4d4db688e6b73ec5c47cf",
}

type fullContractFixture struct {
	plan       changeplan.Plan
	baseline   changeplan.Catalog
	steps      []changeplan.ResolvedCatalogStep
	operations []changeplan.Operation
	decisions  []changeplan.Decision
	future     changeplan.BaselineObject
	renamed    []changeplan.BaselineRename
}

func goldenDigest(data []byte) changeplan.Digest { return changeplan.Digest(sha256.Sum256(data)) }

func fullContractPlan(t *testing.T) changeplan.Plan {
	return fullContractFixtureForTest(t).plan
}

func fullContractFixtureForTest(t *testing.T) fullContractFixture {
	t.Helper()
	profile := testProfile(t)
	profileDigest, err := changeplan.ProfileDigest(profile)
	require.NoError(t, err)
	starting, err := changeplan.NewBaselineObject("starting", "table", "main", "users")
	require.NoError(t, err)
	startingDefinition := schema.TableDef{
		Schema: "main", Name: "users",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "obsolete", Type: schema.TextType{}}},
		PrimaryKey: []string{"id"},
	}
	startingObject, err := changeplan.NewCatalogObject(starting.ID(), startingDefinition)
	require.NoError(t, err)
	future, err := changeplan.NewIntroducedBaselineObject("fixture-source", "create-table", schema.TableDef{
		Schema: "main", Name: "created", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
	})
	require.NoError(t, err)
	createdDefinition := schema.TableDef{Schema: "main", Name: "created", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}, PrimaryKey: []string{"id"}}
	state := func(objects ...changeplan.CatalogObject) changeplan.Catalog {
		catalog, catalogErr := changeplan.NewCatalog(profile, "fixture-source", objects)
		require.NoError(t, catalogErr)
		return catalog
	}
	baselineCatalog := state(startingObject)
	userWithName := startingDefinition
	userWithName.Columns = append(userWithName.Columns, schema.ColumnDef{Name: "name", Type: schema.TextType{}})
	userAfterDrop := startingDefinition
	userAfterDrop.Columns = []schema.ColumnDef{userAfterDrop.Columns[0], schema.ColumnDef{Name: "name", Type: schema.TextType{}}}
	userWithLabel := startingDefinition
	userWithLabel.Columns = []schema.ColumnDef{userWithLabel.Columns[0], schema.ColumnDef{Name: "label", Type: schema.TextType{}}}
	userWithLabelIndex := userWithLabel
	userWithLabelIndex.Indexes = []schema.IndexDef{{Name: "users_label", Columns: []string{"label"}}}
	userWithLabelUnique := userWithLabel
	userWithLabelUnique.UniqueConstraints = []schema.UniqueDef{{Name: "users_label_unique", Columns: []string{"label"}}}
	renameCreatedDefinition := createdDefinition
	renameCreatedDefinition.Name = "renamed"
	finalCreatedDefinition := createdDefinition
	finalCreatedDefinition.Name = "final"
	catalogState := func(user schema.TableDef, created *schema.TableDef) changeplan.Catalog {
		userObject, objectErr := changeplan.NewCatalogObject(starting.ID(), user)
		require.NoError(t, objectErr)
		objects := []changeplan.CatalogObject{userObject}
		if created != nil {
			createdObject, objectErr := changeplan.NewCatalogObject(future.ID(), *created)
			require.NoError(t, objectErr)
			objects = append(objects, createdObject)
		}
		return state(objects...)
	}
	afterStates := []changeplan.Catalog{
		catalogState(startingDefinition, &createdDefinition), catalogState(userWithName, &createdDefinition),
		catalogState(userAfterDrop, &createdDefinition), catalogState(userWithLabel, &createdDefinition),
		catalogState(userWithLabel, &createdDefinition), catalogState(userWithLabelIndex, &createdDefinition),
		catalogState(userWithLabel, &createdDefinition), catalogState(userWithLabelUnique, &createdDefinition),
		catalogState(userWithLabel, &createdDefinition), catalogState(userWithLabel, &createdDefinition),
		catalogState(userWithLabel, &createdDefinition), catalogState(userWithLabel, &renameCreatedDefinition),
		catalogState(userWithLabel, &finalCreatedDefinition),
	}
	finalObject, err := changeplan.NewCatalogObject(future.ID(), finalCreatedDefinition)
	require.NoError(t, err)
	lastState, err := changeplan.NewCatalog(profile, "fixture-source", []changeplan.CatalogObject{finalObject})
	require.NoError(t, err)
	afterStates = append(afterStates, lastState)
	baselineDigest, err := changeplan.CatalogDigest(baselineCatalog)
	require.NoError(t, err)
	catalogIdentity, err := changeplan.NewCatalogIdentity(profile.Engine(), profileDigest, baselineDigest, goldenDigest([]byte("fixture-source")))
	require.NoError(t, err)
	renameOne, err := changeplan.NewBaselineRename("rename-created-1", future.ID(), "main", "renamed")
	require.NoError(t, err)
	renameTwo, err := changeplan.NewBaselineRename("rename-created-2", future.ID(), "main", "final")
	require.NoError(t, err)
	baseline, err := changeplan.NewBaselineIdentity(catalogIdentity, "fixture-source", []changeplan.BaselineObject{starting, future}, []changeplan.BaselineRename{renameOne, renameTwo})
	require.NoError(t, err)
	history, err := changeplan.NewHistoryIdentity("main", "schema_migrations")
	require.NoError(t, err)
	equal, err := changeplan.NewFact(starting.ID(), "/name", changeplan.FactOperatorEqual, `"users"`)
	require.NoError(t, err)
	present, err := changeplan.NewFact(starting.ID(), "$", changeplan.FactOperatorPresent, "")
	require.NoError(t, err)
	absent, err := changeplan.NewFact(future.ID(), "$", changeplan.FactOperatorAbsent, "")
	require.NoError(t, err)
	args := []any{nil, false, int64(-7), uint64(9), float64(1.25), "value", []byte("bytes"), time.Date(2024, 1, 2, 3, 4, 5, 6, time.UTC)}
	statementWithArgs := stmt.New(sqltext.Text("SELECT ?"), args...)
	plain := func(sql string) stmt.Statement { return stmt.New(sqltext.Text(sql)) }
	operationSpec := []struct {
		id, kind, transaction string
		object                changeplan.ObjectID
		pre, post             []changeplan.Fact
		statement             stmt.Statement
		after                 changeplan.Catalog
		reversible            bool
		reverse               []stmt.Statement
	}{
		{id: "create-table", kind: string(changeplan.OperationCreateTable), transaction: string(changeplan.TransactionRequired), object: future.ID(), pre: []changeplan.Fact{absent}, statement: plain("CREATE TABLE created (id INTEGER)"), after: afterStates[0]},
		{id: "add-column", kind: string(changeplan.OperationAddColumn), transaction: string(changeplan.TransactionForbidden), object: starting.ID(), pre: []changeplan.Fact{present}, statement: plain("ALTER TABLE users ADD COLUMN name TEXT"), after: afterStates[1]},
		{id: "drop-column", kind: string(changeplan.OperationDropColumn), transaction: string(changeplan.TransactionEngineDefault), object: starting.ID(), statement: plain("ALTER TABLE users DROP COLUMN obsolete"), after: afterStates[2]},
		{id: "rename-column", kind: string(changeplan.OperationRenameColumn), transaction: string(changeplan.TransactionRequired), object: starting.ID(), statement: plain("ALTER TABLE users RENAME COLUMN name TO label"), after: afterStates[3]},
		{id: "alter-column", kind: string(changeplan.OperationAlterColumn), transaction: string(changeplan.TransactionForbidden), object: starting.ID(), statement: plain("ALTER TABLE users ALTER COLUMN label TYPE TEXT"), after: afterStates[4]},
		{id: "create-index", kind: string(changeplan.OperationCreateIndex), transaction: string(changeplan.TransactionEngineDefault), object: starting.ID(), statement: plain("CREATE INDEX users_label ON users(label)"), after: afterStates[5]},
		{id: "drop-index", kind: string(changeplan.OperationDropIndex), transaction: string(changeplan.TransactionRequired), object: starting.ID(), statement: plain("DROP INDEX users_label"), after: afterStates[6]},
		{id: "add-constraint", kind: string(changeplan.OperationAddConstraint), transaction: string(changeplan.TransactionForbidden), object: starting.ID(), statement: plain("ALTER TABLE users ADD CONSTRAINT users_label_unique UNIQUE(label)"), after: afterStates[7]},
		{id: "drop-constraint", kind: string(changeplan.OperationDropConstraint), transaction: string(changeplan.TransactionEngineDefault), object: starting.ID(), statement: plain("ALTER TABLE users DROP CONSTRAINT users_label_unique"), after: afterStates[8]},
		{id: "backfill", kind: string(changeplan.OperationBackfill), transaction: string(changeplan.TransactionRequired), object: starting.ID(), post: []changeplan.Fact{equal}, statement: statementWithArgs, after: afterStates[9]},
		{id: "native-sql", kind: string(changeplan.OperationNativeSQL), transaction: string(changeplan.TransactionForbidden), object: starting.ID(), pre: []changeplan.Fact{present}, statement: statementWithArgs, reversible: true, reverse: []stmt.Statement{plain("SELECT 0")}, after: afterStates[10]},
		{id: "rename-created-1", kind: string(changeplan.OperationRenameTable), transaction: string(changeplan.TransactionEngineDefault), object: future.ID(), statement: plain("ALTER TABLE created RENAME TO renamed"), after: afterStates[11]},
		{id: "rename-created-2", kind: string(changeplan.OperationRenameTable), transaction: string(changeplan.TransactionRequired), object: future.ID(), statement: plain("ALTER TABLE renamed RENAME TO final"), after: afterStates[12]},
		{id: "drop-table", kind: string(changeplan.OperationDropTable), transaction: string(changeplan.TransactionRequired), object: starting.ID(), statement: plain("DROP TABLE users"), after: afterStates[13]},
	}
	decisions := []changeplan.Decision{
		mustDecision(t, "rename-one", changeplan.DecisionRenameObject, future.ID(), "main.created", "main.renamed", ""),
		mustDecision(t, "rename-two", changeplan.DecisionRenameObject, future.ID(), "main.renamed", "main.final", ""),
		mustDecision(t, "rename-column", changeplan.DecisionRenameObject, starting.ID(), "name", "label", ""),
		mustDecision(t, "destructive", changeplan.DecisionAcceptDestructive, starting.ID(), "", "", "approved"),
		mustDecision(t, "backfill", changeplan.DecisionSupplyBackfill, starting.ID(), "", "", "approved"),
		mustDecision(t, "native", changeplan.DecisionAcceptNativeSQL, starting.ID(), "", "", "approved"),
	}
	operations := make([]changeplan.Operation, 0, len(operationSpec))
	steps := make([]changeplan.ResolvedCatalogStep, 0, len(operationSpec))
	for i, spec := range operationSpec {
		depends := make([]changeplan.OperationID, 0, i)
		if i > 0 {
			depends = append(depends, changeplan.OperationID(operationSpec[i-1].id))
		}
		resultDigest, digestErr := changeplan.CatalogDigest(spec.after)
		require.NoError(t, digestErr)
		operation, operationErr := changeplan.NewOperation(changeplan.OperationID(spec.id), changeplan.OperationKind(spec.kind), depends,
			[]changeplan.ObjectID{spec.object}, spec.pre, spec.post, resultDigest, []stmt.Statement{spec.statement},
			changeplan.TransactionMode(spec.transaction), spec.reversible, spec.reverse)
		require.NoError(t, operationErr)
		operations = append(operations, operation)
		step, stepErr := changeplan.NewResolvedCatalogStep(operation.ID(), spec.after)
		require.NoError(t, stepErr)
		steps = append(steps, step)
	}
	plan, err := changeplan.NewPlan(profile, baseline, history, decisions, operations)
	require.NoError(t, err)
	return fullContractFixture{
		plan: plan, baseline: baselineCatalog, steps: steps, operations: operations, decisions: decisions,
		future: future, renamed: []changeplan.BaselineRename{renameOne, renameTwo},
	}
}

func mustDecision(t *testing.T, id changeplan.DecisionID, kind changeplan.DecisionKind, object changeplan.ObjectID, from, to, reason string) changeplan.Decision {
	t.Helper()
	decision, err := changeplan.NewDecision(id, kind, object, from, to, true, reason)
	require.NoError(t, err)
	return decision
}

func TestV1FullGoldenContract(t *testing.T) {
	fixture, err := os.ReadFile("testdata/v1/valid/full.json")
	require.NoError(t, err)
	plan := fullContractPlan(t)
	encoded, err := changeplan.Encode(plan)
	require.NoError(t, err)
	require.Equal(t, fixture, encoded)
	decoded, err := changeplan.Decode(fixture)
	require.NoError(t, err)
	require.Equal(t, plan.ID(), decoded.ID())
	reencoded, err := changeplan.Encode(decoded)
	require.NoError(t, err)
	require.Equal(t, fixture, reencoded)

	var wire struct {
		ID      string `json:"id"`
		Profile struct {
			Engine string `json:"engine"`
		} `json:"profile"`
		Baseline struct {
			ProfileDigest string `json:"profile_digest"`
			CatalogDigest string `json:"catalog_digest"`
			SourceDigest  string `json:"source_digest"`
		} `json:"baseline"`
		Operations []struct {
			ID           string `json:"id"`
			Kind         string `json:"kind"`
			Transaction  string `json:"transaction"`
			Reversible   bool   `json:"reversible"`
			ResultDigest string `json:"result_digest"`
			Statements   []struct {
				Args []struct {
					Kind string `json:"kind"`
				} `json:"args"`
			} `json:"statements"`
		} `json:"operations"`
	}
	require.NoError(t, json.Unmarshal(fixture, &wire))
	require.Len(t, wire.ID, 64)
	require.Equal(t, "sqlite", wire.Profile.Engine)
	require.Len(t, wire.Baseline.ProfileDigest, 64)
	require.Len(t, wire.Baseline.CatalogDigest, 64)
	require.Len(t, wire.Baseline.SourceDigest, 64)
	require.Equal(t, goldenDigest(goldenProfileBytes).String(), wire.Baseline.ProfileDigest)
	require.Equal(t, goldenDigest(goldenCatalogBytes).String(), wire.Baseline.CatalogDigest)
	require.Equal(t, goldenDigest([]byte("fixture-source")).String(), wire.Baseline.SourceDigest)
	noID := bytes.Replace(fixture, []byte(`"id":"`+wire.ID+`",`), []byte{}, 1)
	planDigest := sha256.Sum256(bytes.TrimSuffix(noID, []byte{'\n'}))
	require.Equal(t, hex.EncodeToString(planDigest[:]), wire.ID)
	require.Equal(t, plan.ID().String(), wire.ID)
	require.Equal(t, goldenDigest(goldenCatalogBytes), plan.Baseline().Catalog().CatalogDigest())
	fixturePlan := fullContractFixtureForTest(t)
	resolved, err := changeplan.NewResolvedChanges(fixturePlan.baseline, fixturePlan.steps, fixturePlan.decisions,
		fixturePlan.operations, []changeplan.BaselineObject{fixturePlan.future}, fixturePlan.renamed)
	require.NoError(t, err)
	require.Len(t, resolved.CatalogSteps(), len(fixturePlan.operations))
	for i, operation := range resolved.Operations() {
		require.Equal(t, fixturePlan.operations[i].ID(), operation.ID())
		require.Equal(t, goldenResultDigests[string(operation.ID())], operation.ResultDigest().String())
		require.Equal(t, fixturePlan.steps[i].Operation(), resolved.CatalogSteps()[i].Operation())
		require.Equal(t, fixturePlan.steps[i].Catalog(), resolved.CatalogSteps()[i].Catalog())
		catalogDigest, digestErr := changeplan.CatalogDigest(resolved.CatalogSteps()[i].Catalog())
		require.NoError(t, digestErr)
		require.Equal(t, operation.ResultDigest(), catalogDigest)
	}
	_, usersPresent := resolved.TargetCatalog().ObjectID(schema.ObjectTable, "main", "users")
	require.False(t, usersPresent)
	finalID, finalPresent := resolved.TargetCatalog().ObjectID(schema.ObjectTable, "main", "final")
	require.True(t, finalPresent)
	require.Equal(t, fixturePlan.future.ID(), finalID)

	wantKinds := map[string]bool{}
	wantTransactions := map[string]bool{}
	for _, operation := range wire.Operations {
		require.Equal(t, goldenResultDigests[operation.ID], operation.ResultDigest)
		wantKinds[operation.Kind] = true
		wantTransactions[operation.Transaction] = true
	}
	for _, kind := range []changeplan.OperationKind{changeplan.OperationCreateTable, changeplan.OperationDropTable, changeplan.OperationRenameTable,
		changeplan.OperationAddColumn, changeplan.OperationDropColumn, changeplan.OperationRenameColumn, changeplan.OperationAlterColumn,
		changeplan.OperationCreateIndex, changeplan.OperationDropIndex, changeplan.OperationAddConstraint, changeplan.OperationDropConstraint,
		changeplan.OperationBackfill, changeplan.OperationNativeSQL} {
		require.True(t, wantKinds[string(kind)], "missing operation kind %s", kind)
	}
	for _, mode := range []changeplan.TransactionMode{changeplan.TransactionRequired, changeplan.TransactionForbidden, changeplan.TransactionEngineDefault} {
		require.True(t, wantTransactions[string(mode)], "missing transaction mode %s", mode)
	}
	var wireArgumentKinds []string
	for _, operation := range wire.Operations {
		if operation.Kind == string(changeplan.OperationBackfill) {
			for _, argument := range operation.Statements[0].Args {
				wireArgumentKinds = append(wireArgumentKinds, argument.Kind)
			}
		}
	}
	require.Equal(t, []string{"null", "bool", "int64", "uint64", "float64", "string", "bytes_base64", "time_rfc3339nano"}, wireArgumentKinds)
}

func TestFullContractArgumentsAndReversibility(t *testing.T) {
	plan := fullContractPlan(t)
	operations := plan.Operations()
	require.Len(t, operations, 14)
	var argumentKinds []string
	var backfill changeplan.Operation
	var native changeplan.Operation
	for _, operation := range operations {
		if operation.ID() == "backfill" {
			backfill = operation
		}
		if operation.ID() == "native-sql" {
			native = operation
		}
	}
	for _, argument := range backfill.Statements()[0].Args() {
		switch argument.(type) {
		case nil:
			argumentKinds = append(argumentKinds, "null")
		case bool:
			argumentKinds = append(argumentKinds, "bool")
		case int64:
			argumentKinds = append(argumentKinds, "int64")
		case uint64:
			argumentKinds = append(argumentKinds, "uint64")
		case float64:
			argumentKinds = append(argumentKinds, "float64")
		case string:
			argumentKinds = append(argumentKinds, "string")
		case []byte:
			argumentKinds = append(argumentKinds, "bytes_base64")
		case time.Time:
			argumentKinds = append(argumentKinds, "time_rfc3339nano")
		}
	}
	require.Equal(t, []string{"null", "bool", "int64", "uint64", "float64", "string", "bytes_base64", "time_rfc3339nano"}, argumentKinds)
	require.False(t, operations[0].Reversible())
	require.True(t, native.Reversible())
	require.Len(t, native.ReverseStatements(), 1)
	for _, operation := range operations {
		require.NotEmpty(t, operation.Statements())
	}
}
