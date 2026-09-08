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

func fullContractPlan(t *testing.T) changeplan.Plan {
	t.Helper()
	profile := testProfile(t)
	profileDigest, err := changeplan.ProfileDigest(profile)
	require.NoError(t, err)
	catalogIdentity, err := changeplan.NewCatalogIdentity(profile.Engine(), profileDigest, changeplan.Digest{2}, changeplan.Digest{3})
	require.NoError(t, err)
	starting, err := changeplan.NewBaselineObject("starting", "table", "main", "users")
	require.NoError(t, err)
	future, err := changeplan.NewIntroducedBaselineObject("fixture-source", "create-table", schema.TableDef{
		Schema: "main", Name: "created", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
	})
	require.NoError(t, err)
	rename, err := changeplan.NewBaselineRename("rename-table", starting.ID(), "main", "accounts")
	require.NoError(t, err)
	baseline, err := changeplan.NewBaselineIdentity(catalogIdentity, "fixture-source", []changeplan.BaselineObject{starting, future}, []changeplan.BaselineRename{rename})
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
		reversible            bool
		reverse               []stmt.Statement
	}{
		{id: "create-table", kind: string(changeplan.OperationCreateTable), transaction: string(changeplan.TransactionRequired), object: future.ID(), statement: plain("CREATE TABLE created (id INTEGER)")},
		{id: "add-column", kind: string(changeplan.OperationAddColumn), transaction: string(changeplan.TransactionForbidden), object: starting.ID(), pre: []changeplan.Fact{present}, statement: plain("ALTER TABLE users ADD COLUMN name TEXT")},
		{id: "drop-column", kind: string(changeplan.OperationDropColumn), transaction: string(changeplan.TransactionEngineDefault), object: starting.ID(), statement: plain("ALTER TABLE users DROP COLUMN name")},
		{id: "rename-column", kind: string(changeplan.OperationRenameColumn), transaction: string(changeplan.TransactionRequired), object: starting.ID(), statement: plain("ALTER TABLE users RENAME COLUMN name TO label")},
		{id: "alter-column", kind: string(changeplan.OperationAlterColumn), transaction: string(changeplan.TransactionForbidden), object: starting.ID(), statement: plain("ALTER TABLE users ALTER COLUMN label TYPE TEXT")},
		{id: "create-index", kind: string(changeplan.OperationCreateIndex), transaction: string(changeplan.TransactionEngineDefault), object: starting.ID(), statement: plain("CREATE INDEX users_label ON users(label)")},
		{id: "drop-index", kind: string(changeplan.OperationDropIndex), transaction: string(changeplan.TransactionRequired), object: starting.ID(), statement: plain("DROP INDEX users_label")},
		{id: "add-constraint", kind: string(changeplan.OperationAddConstraint), transaction: string(changeplan.TransactionForbidden), object: starting.ID(), statement: plain("ALTER TABLE users ADD CONSTRAINT users_label_unique UNIQUE(label)")},
		{id: "drop-constraint", kind: string(changeplan.OperationDropConstraint), transaction: string(changeplan.TransactionEngineDefault), object: starting.ID(), statement: plain("ALTER TABLE users DROP CONSTRAINT users_label_unique")},
		{id: "backfill", kind: string(changeplan.OperationBackfill), transaction: string(changeplan.TransactionRequired), object: starting.ID(), post: []changeplan.Fact{equal}, statement: statementWithArgs},
		{id: "native-sql", kind: string(changeplan.OperationNativeSQL), transaction: string(changeplan.TransactionForbidden), object: starting.ID(), pre: []changeplan.Fact{absent}, statement: statementWithArgs, reversible: true, reverse: []stmt.Statement{plain("SELECT 0")}},
		{id: "rename-table", kind: string(changeplan.OperationRenameTable), transaction: string(changeplan.TransactionEngineDefault), object: starting.ID(), statement: plain("ALTER TABLE users RENAME TO accounts")},
		{id: "drop-table", kind: string(changeplan.OperationDropTable), transaction: string(changeplan.TransactionRequired), object: starting.ID(), statement: plain("DROP TABLE accounts")},
	}
	decisions := []changeplan.Decision{
		mustDecision(t, "rename", changeplan.DecisionRenameObject, starting.ID(), "main.users", "main.accounts", ""),
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
		operation, operationErr := changeplan.NewOperation(changeplan.OperationID(spec.id), changeplan.OperationKind(spec.kind), depends,
			[]changeplan.ObjectID{spec.object}, spec.pre, spec.post, []stmt.Statement{spec.statement},
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
	profileDigest, err := changeplan.ProfileDigest(plan.Profile())
	require.NoError(t, err)
	require.Equal(t, profileDigest.String(), wire.Baseline.ProfileDigest)
	var catalogDigest, sourceDigest changeplan.Digest
	catalogDigest[0] = 2
	sourceDigest[0] = 3
	require.Equal(t, catalogDigest.String(), wire.Baseline.CatalogDigest)
	require.Equal(t, sourceDigest.String(), wire.Baseline.SourceDigest)
	noID := bytes.Replace(fixture, []byte(`"id":"`+wire.ID+`",`), []byte{}, 1)
	planDigest := sha256.Sum256(bytes.TrimSuffix(noID, []byte{'\n'}))
	require.Equal(t, hex.EncodeToString(planDigest[:]), wire.ID)
	require.Equal(t, plan.ID().String(), wire.ID)
	require.NotEqual(t, changeplan.Digest{}, plan.Baseline().Catalog().CatalogDigest())

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
	for _, argument := range wire.Operations[9].Statements[0].Args {
		wireArgumentKinds = append(wireArgumentKinds, argument.Kind)
	}
	require.Equal(t, []string{"null", "bool", "int64", "uint64", "float64", "string", "bytes_base64", "time_rfc3339nano"}, wireArgumentKinds)
}

func TestFullContractArgumentsAndReversibility(t *testing.T) {
	plan := fullContractPlan(t)
	operations := plan.Operations()
	require.Len(t, operations, 13)
	var argumentKinds []string
	for _, argument := range operations[9].Statements()[0].Args() {
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
	require.True(t, operations[10].Reversible())
	require.Len(t, operations[10].ReverseStatements(), 1)
	for _, operation := range operations {
		require.NotEmpty(t, operation.Statements())
	}
}
