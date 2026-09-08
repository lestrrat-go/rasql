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

var goldenCatalogBytes = []byte(`{"engine":{"dialect":"sqlite","version":"3.35.0","profile":"sqlite-3.35"},"objects":[{"id":"starting","kind":"table","schema":"main","name":"users","columns":[{"name":"id","ordinal":0,"logical_kind":"integer","native":null,"nullable":false,"default_sql":"","generated_sql":"","generated_storage":"","identity":"","collation":"","hidden":false,"integer":{"unsigned":false,"display_width":{"value":0,"set":false},"zero_fill":false},"text":null,"decimal":null}],"constraints":[{"name":"","kind":"primary_key","columns":["id"],"reference":null,"expression_sql":"","deferrable":false,"initially_deferred":false,"on_update":"","on_delete":"","deferrability":"","match":"","nulls_not_distinct":false,"include_columns":null,"on_conflict":"","keys":[],"temporal":false,"storage_parameters":[],"tablespace":"","replica_identity":false,"collations":[],"no_inherit":false,"not_valid":false,"not_enforced":false,"delete_set_columns":null}],"indexes":[],"exclusion_constraints":[],"strict":false,"without_rowid":false,"primary_key_autoincrement":false,"primary_key_on_conflict":"","virtual_table_module":"","virtual_table_module_arguments":null}]}`)

func goldenDigest(data []byte) changeplan.Digest { return changeplan.Digest(sha256.Sum256(data)) }

func fullContractPlan(t *testing.T) changeplan.Plan {
	t.Helper()
	profile := testProfile(t)
	profileDigest, err := changeplan.ProfileDigest(profile)
	require.NoError(t, err)
	starting, err := changeplan.NewBaselineObject("starting", "table", "main", "users")
	require.NoError(t, err)
	startingDefinition := schema.TableDef{Schema: "main", Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}, PrimaryKey: []string{"id"}}
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
	createdObject, err := changeplan.NewCatalogObject(future.ID(), createdDefinition)
	require.NoError(t, err)
	userWithName := startingDefinition
	userWithName.Columns = append(userWithName.Columns, schema.ColumnDef{Name: "name", Type: schema.TextType{}})
	userWithLabel := startingDefinition
	userWithLabel.Columns = append(userWithLabel.Columns, schema.ColumnDef{Name: "label", Type: schema.TextType{}})
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
		catalogState(startingDefinition, &createdDefinition), catalogState(userWithLabel, &createdDefinition),
		catalogState(userWithLabel, &createdDefinition), catalogState(userWithLabelIndex, &createdDefinition),
		catalogState(userWithLabel, &createdDefinition), catalogState(userWithLabelUnique, &createdDefinition),
		catalogState(userWithLabel, &createdDefinition), catalogState(userWithLabel, &createdDefinition),
		catalogState(userWithLabel, &createdDefinition), catalogState(userWithLabel, &renameCreatedDefinition),
		catalogState(userWithLabel, &finalCreatedDefinition),
	}
	lastState, err := changeplan.NewCatalog(profile, "fixture-source", []changeplan.CatalogObject{createdObject})
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
	equal, err := changeplan.NewFact(starting.ID(), "/name", changeplan.FactEqual, `"users"`)
	require.NoError(t, err)
	present, err := changeplan.NewFact(starting.ID(), "$", changeplan.FactPresent, "")
	require.NoError(t, err)
	absent, err := changeplan.NewFact(starting.ID(), "$", changeplan.FactAbsent, "")
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
		{id: "create-table", kind: string(changeplan.OperationCreateTable), transaction: string(changeplan.TransactionRequired), object: future.ID(), statement: plain("CREATE TABLE created (id INTEGER)"), after: afterStates[0]},
		{id: "add-column", kind: string(changeplan.OperationAddColumn), transaction: string(changeplan.TransactionForbidden), object: starting.ID(), pre: []changeplan.Fact{present}, statement: plain("ALTER TABLE users ADD COLUMN name TEXT"), after: afterStates[1]},
		{id: "drop-column", kind: string(changeplan.OperationDropColumn), transaction: string(changeplan.TransactionEngineDefault), object: starting.ID(), statement: plain("ALTER TABLE users DROP COLUMN name"), after: afterStates[2]},
		{id: "rename-column", kind: string(changeplan.OperationRenameColumn), transaction: string(changeplan.TransactionRequired), object: starting.ID(), statement: plain("ALTER TABLE users RENAME COLUMN name TO label"), after: afterStates[3]},
		{id: "alter-column", kind: string(changeplan.OperationAlterColumn), transaction: string(changeplan.TransactionForbidden), object: starting.ID(), statement: plain("ALTER TABLE users ALTER COLUMN label TYPE TEXT"), after: afterStates[4]},
		{id: "create-index", kind: string(changeplan.OperationCreateIndex), transaction: string(changeplan.TransactionEngineDefault), object: starting.ID(), statement: plain("CREATE INDEX users_label ON users(label)"), after: afterStates[5]},
		{id: "drop-index", kind: string(changeplan.OperationDropIndex), transaction: string(changeplan.TransactionRequired), object: starting.ID(), statement: plain("DROP INDEX users_label"), after: afterStates[6]},
		{id: "add-constraint", kind: string(changeplan.OperationAddConstraint), transaction: string(changeplan.TransactionForbidden), object: starting.ID(), statement: plain("ALTER TABLE users ADD CONSTRAINT users_label_unique UNIQUE(label)"), after: afterStates[7]},
		{id: "drop-constraint", kind: string(changeplan.OperationDropConstraint), transaction: string(changeplan.TransactionEngineDefault), object: starting.ID(), statement: plain("ALTER TABLE users DROP CONSTRAINT users_label_unique"), after: afterStates[8]},
		{id: "backfill", kind: string(changeplan.OperationBackfill), transaction: string(changeplan.TransactionRequired), object: starting.ID(), post: []changeplan.Fact{equal}, statement: statementWithArgs, after: afterStates[9]},
		{id: "native-sql", kind: string(changeplan.OperationNativeSQL), transaction: string(changeplan.TransactionForbidden), object: starting.ID(), pre: []changeplan.Fact{absent}, statement: statementWithArgs, reversible: true, reverse: []stmt.Statement{plain("SELECT 0")}, after: afterStates[10]},
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
	}
	plan, err := changeplan.NewPlan(profile, baseline, history, decisions, operations)
	require.NoError(t, err)
	return plan
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
			Kind        string `json:"kind"`
			Transaction string `json:"transaction"`
			Reversible  bool   `json:"reversible"`
			Statements  []struct {
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
	var rawRoot map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(fixture, &rawRoot))
	var rawProfile json.RawMessage
	require.NoError(t, json.Unmarshal(rawRoot["profile"], &rawProfile))
	require.Equal(t, goldenDigest(bytes.TrimSpace(rawProfile)).String(), wire.Baseline.ProfileDigest)
	require.Equal(t, goldenDigest(goldenCatalogBytes).String(), wire.Baseline.CatalogDigest)
	require.Equal(t, goldenDigest([]byte("fixture-source")).String(), wire.Baseline.SourceDigest)
	noID := bytes.Replace(fixture, []byte(`"id":"`+wire.ID+`",`), []byte{}, 1)
	planDigest := sha256.Sum256(bytes.TrimSuffix(noID, []byte{'\n'}))
	require.Equal(t, hex.EncodeToString(planDigest[:]), wire.ID)
	require.Equal(t, plan.ID().String(), wire.ID)
	require.Equal(t, goldenDigest(goldenCatalogBytes), plan.Baseline().Catalog().CatalogDigest())

	wantKinds := map[string]bool{}
	wantTransactions := map[string]bool{}
	for _, operation := range wire.Operations {
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
