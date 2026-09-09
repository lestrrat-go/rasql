package rasql

import (
	"database/sql"
	"database/sql/driver"
	"math"
	"reflect"
	"testing"

	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

type graphFingerprintNamedID int64

func graphFingerprintStageFor(t *testing.T) graphFingerprintStage {
	t.Helper()
	table := query.MustTableRef(schema.TableDef{
		Name:    "fingerprint_rows",
		Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
	})
	source := query.Relation(table)
	column := source.Column("id")
	resultSchema, err := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	key := &graphKeySpec{parts: []*graphKeyPartSpec{{
		column: column, source: source.QualifiedName(), typ: reflect.TypeOf(graphFingerprintNamedID(0)),
		columnType: schema.IntegerType{},
	}}}
	return graphFingerprintStage{
		name: "child", source: source.QualifiedName(), schema: resultSchema, keys: []*graphKeySpec{key},
		compiled: compiledQuery{statement: stmt.New(sqltext.Text("SELECT id FROM fingerprint_rows"))}, bindLimit: 999,
	}
}

func graphFingerprintProfile() engineProfileSnapshot {
	return engineProfileSnapshot{
		Dialect: "sqlite", ID: "sqlite-3.35", Engine: SQLiteEngine,
		Version: EngineVersion{Known: true, Major: 3, Minor: 35},
		Limits:  EngineLimits{MaxBindParameters: 999}, MaxBind: 999,
		Capabilities: EngineCapabilities{WindowFunctions: true, PerParentLimit: EnginePerParentLimitWindow},
	}
}

func TestGraphFingerprintSeparatesProfileDialectAndCapabilities(t *testing.T) {
	stage := graphFingerprintStageFor(t)
	base, err := graphInvocationFingerprint(stage, graphFingerprintProfile())
	require.NoError(t, err)

	dialectProfile := graphFingerprintProfile()
	dialectProfile.Dialect = "sqlite-extension"
	dialectKey, err := graphInvocationFingerprint(stage, dialectProfile)
	require.NoError(t, err)
	require.NotEqual(t, base, dialectKey)

	capabilityProfile := graphFingerprintProfile()
	capabilityProfile.Capabilities.WindowFunctions = false
	capabilityKey, err := graphInvocationFingerprint(stage, capabilityProfile)
	require.NoError(t, err)
	require.NotEqual(t, base, capabilityKey)

	updateDefaultProfile := graphFingerprintProfile()
	updateDefaultProfile.Capabilities.UpdateDefault = engineprofile.UpdateDefaultExpression
	updateDefaultKey, err := graphInvocationFingerprint(stage, updateDefaultProfile)
	require.NoError(t, err)
	require.NotEqual(t, base, updateDefaultKey)
}

func TestGraphFingerprintSeparatesLogicalKeyAndStageMetadata(t *testing.T) {
	stage := graphFingerprintStageFor(t)
	base, err := graphInvocationFingerprint(stage, graphFingerprintProfile())
	require.NoError(t, err)

	logicalSchema, err := NewResultSchema(ResultColumn{
		Name: "id", Type: schema.IntegerType{Unsigned: true},
	})
	require.NoError(t, err)
	logicalStage := stage
	logicalStage.schema = logicalSchema
	logicalKey, err := graphInvocationFingerprint(logicalStage, graphFingerprintProfile())
	require.NoError(t, err)
	require.NotEqual(t, base, logicalKey)

	nullableStage := stage
	nullableStage.keys = []*graphKeySpec{{parts: []*graphKeyPartSpec{{
		column: stage.keys[0].parts[0].column, source: stage.keys[0].parts[0].source,
		typ: stage.keys[0].parts[0].typ, columnType: stage.keys[0].parts[0].columnType, nullable: true,
	}}}}
	nullableKey, err := graphInvocationFingerprint(nullableStage, graphFingerprintProfile())
	require.NoError(t, err)
	require.NotEqual(t, base, nullableKey)

	junctionStage := stage
	junctionStage.name = "junction"
	junctionStage.source = "fingerprint_junction"
	junctionStage.keys = []*graphKeySpec{stage.keys[0], nullableStage.keys[0]}
	junctionKey, err := graphInvocationFingerprint(junctionStage, graphFingerprintProfile())
	require.NoError(t, err)
	require.NotEqual(t, base, junctionKey)
}

func TestGraphFingerprintRejectsBindArgumentCountMismatch(t *testing.T) {
	stage := graphFingerprintStageFor(t)
	stage.compiled.bindSlots = []bindSlot{{codec: ""}}
	_, err := graphInvocationFingerprint(stage, graphFingerprintProfile())
	var planErr *PlanError
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "internal_plan", planErr.Code)

	stage.compiled.statement = stmt.New(sqltext.Text("SELECT id FROM fingerprint_rows"), int64(1), int64(2))
	_, err = graphInvocationFingerprint(stage, graphFingerprintProfile())
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "internal_plan", planErr.Code)
}

type graphFingerprintValueValuer struct{}

func (graphFingerprintValueValuer) Value() (driver.Value, error) { return int64(1), nil }

func TestGraphFingerprintFramesCanonicalFixedValues(t *testing.T) {
	stage := graphFingerprintStageFor(t)
	stage.compiled.statement = stmt.New(sqltext.Text("SELECT id FROM fingerprint_rows"),
		float64(0), float64(math.Copysign(0, -1)), sql.Named("payload", math.Float64frombits(0x7ff8000000000042)),
	)
	stage.compiled.bindSlots = []bindSlot{{preEncoded: true}, {preEncoded: true}, {preEncoded: true}}
	base, err := graphInvocationFingerprint(stage, graphFingerprintProfile())
	require.NoError(t, err)

	zero := stage
	zero.compiled.statement = stmt.New(sqltext.Text("SELECT id FROM fingerprint_rows"),
		float64(0), float64(0), sql.Named("payload", math.Float64frombits(0x7ff8000000000042)),
	)
	zeroKey, err := graphInvocationFingerprint(zero, graphFingerprintProfile())
	require.NoError(t, err)
	require.NotEqual(t, base.digest, zeroKey.digest)

	nan := stage
	nan.compiled.statement = stmt.New(sqltext.Text("SELECT id FROM fingerprint_rows"),
		float64(0), float64(math.Copysign(0, -1)), sql.Named("payload", math.Float64frombits(0x7ff8000000000043)),
	)
	nanKey, err := graphInvocationFingerprint(nan, graphFingerprintProfile())
	require.NoError(t, err)
	require.NotEqual(t, base.digest, nanKey.digest)

	changed := stage
	changed.compiled.statement = stmt.New(sqltext.Text("SELECT id FROM fingerprint_rows"),
		float64(0), float64(math.Copysign(0, -1)), sql.Named("other", math.Float64frombits(0x7ff8000000000042)),
	)
	changedKey, err := graphInvocationFingerprint(changed, graphFingerprintProfile())
	require.NoError(t, err)
	require.NotEqual(t, base.digest, changedKey.digest)

	invalid := stage
	invalid.compiled.statement = stmt.New(sqltext.Text("SELECT id FROM fingerprint_rows"), 1, float64(0), float64(0))
	_, err = graphInvocationFingerprint(invalid, graphFingerprintProfile())
	var planErr *PlanError
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "internal_plan", planErr.Code)

	valuer := stage
	valuer.compiled.statement = stmt.New(sqltext.Text("SELECT id FROM fingerprint_rows"), graphFingerprintValueValuer{}, float64(0), float64(0))
	_, err = graphInvocationFingerprint(valuer, graphFingerprintProfile())
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "internal_plan", planErr.Code)
}

func TestGraphFingerprintEncodesAllProfileCapabilityFields(t *testing.T) {
	stage := graphFingerprintStageFor(t)
	baseProfile := graphFingerprintProfile()
	base, err := graphInvocationFingerprint(stage, baseProfile)
	require.NoError(t, err)
	capabilities := baseProfile.Capabilities
	values := []func(*EngineCapabilities){
		func(value *EngineCapabilities) { value.Returning = engineprofile.ReturningInsert },
		func(value *EngineCapabilities) { value.Upsert = engineprofile.UpsertOnConflict },
		func(value *EngineCapabilities) { value.ConflictTarget = true },
		func(value *EngineCapabilities) { value.DefaultValues = true },
		func(value *EngineCapabilities) { value.EmptyInsert = true },
		func(value *EngineCapabilities) { value.DefaultValuesUpsert = true },
		func(value *EngineCapabilities) { value.SubqueryLimit = true },
		func(value *EngineCapabilities) { value.WriteSubqueryTarget = true },
		func(value *EngineCapabilities) { value.PartialIndex = true },
		func(value *EngineCapabilities) { value.AggregateFilter = true },
		func(value *EngineCapabilities) { value.QualifiedReference = true },
		func(value *EngineCapabilities) { value.QualifiedIndexTarget = true },
		func(value *EngineCapabilities) { value.QualifiedIndexName = true },
		func(value *EngineCapabilities) { value.MatchOperator = true },
		func(value *EngineCapabilities) { value.SelectForUpdate = true },
		func(value *EngineCapabilities) { value.SelectForShare = true },
		func(value *EngineCapabilities) { value.SelectLockOf = true },
		func(value *EngineCapabilities) { value.SelectLockNoWait = true },
		func(value *EngineCapabilities) { value.SelectLockSkipLocked = true },
		func(value *EngineCapabilities) { value.UpsertConflictWhere = true },
		func(value *EngineCapabilities) { value.UpsertUpdateWhere = true },
		func(value *EngineCapabilities) { value.WindowFunctions = false },
		func(value *EngineCapabilities) { value.LateralJoins = true },
		func(value *EngineCapabilities) { value.Savepoints = true },
		func(value *EngineCapabilities) { value.TransactionalDDL = true },
		func(value *EngineCapabilities) { value.ExplicitNullOrdering = true },
		func(value *EngineCapabilities) { value.TupleComparison = true },
		func(value *EngineCapabilities) { value.UpdateDefault = engineprofile.UpdateDefaultExpression },
	}
	for index, change := range values {
		profile := baseProfile
		profile.Capabilities = capabilities
		change(&profile.Capabilities)
		key, keyErr := graphInvocationFingerprint(stage, profile)
		require.NoError(t, keyErr, "capability %d", index)
		require.NotEqual(t, base.digest, key.digest, "capability %d", index)
	}
}
