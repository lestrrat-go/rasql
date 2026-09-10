package rasql

import (
	"database/sql"
	"database/sql/driver"
	"math"
	"reflect"
	"testing"

	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/querycompile"
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

func TestGraphFingerprint(t *testing.T) {
	t.Run("separates profile, dialect and capabilities", func(t *testing.T) {
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
	})

	t.Run("separates the logical key and stage metadata", func(t *testing.T) {
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
	})

	t.Run("rejects a bind argument count mismatch", func(t *testing.T) {
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
	})

	t.Run("frames canonical fixed values", func(t *testing.T) {
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
	})

	t.Run("encodes every profile capability field", func(t *testing.T) {
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
	})
}

type graphFingerprintValueValuer struct{}

func (graphFingerprintValueValuer) Value() (driver.Value, error) { return int64(1), nil }

func TestGraphStageBindMetadata(t *testing.T) {
	t.Run("cacheability requires stable bind metadata", func(t *testing.T) {
		copyArg := func() (any, error) { return int64(1), nil }
		base := compiledQuery{
			statement: stmt.New(sqltext.Text("SELECT ?"), int64(1)),
			bindSlots: []bindSlot{{id: bindID(1)}},
			copyArgs:  []bindValueCopy{copyArg},
		}
		require.True(t, graphStageCacheable(base))

		base.bindSlots[0].id = 0
		require.False(t, graphStageCacheable(base))
		base.bindSlots[0].id = bindID(1)
		base.copyArgs = nil
		require.False(t, graphStageCacheable(base))
	})

	t.Run("pre-encoded base occurrences match by identity and detach", func(t *testing.T) {
		base := compiledQuery{
			statement: stmt.New(sqltext.Text("SELECT ? AND ?"), int64(1), []byte("base")),
			bindSlots: []bindSlot{{id: bindID(11)}, {id: bindID(12)}},
			copyArgs: []bindValueCopy{
				func() (any, error) { return int64(1), nil },
				func() (any, error) { return []byte("base"), nil },
			},
		}
		encoded := stmt.New(sqltext.Text("SELECT ? AND ?"), int64(7), []byte("encoded"))
		final := compiledQuery{
			statement: stmt.New(sqltext.Text("SELECT ? AND ? AND ?"), int64(98), int64(99), []byte("old")),
			bindSlots: []bindSlot{{id: bindID(11)}, {id: bindID(99)}, {id: bindID(12)}},
			copyArgs: []bindValueCopy{
				func() (any, error) { return int64(98), nil },
				func() (any, error) { return int64(99), nil },
				func() (any, error) { return []byte("old"), nil },
			},
		}
		result, err := graphPreencodeBaseOccurrences(base, encoded, final)
		require.NoError(t, err)
		require.Equal(t, []any{int64(7), int64(99), []byte("encoded")}, result.statement.Args())
		require.False(t, result.bindSlots[1].preEncoded)
		require.True(t, result.bindSlots[2].preEncoded)
		require.True(t, result.bindSlots[0].preEncoded)
		require.False(t, final.bindSlots[0].preEncoded)

		value := result.statement.Args()[2].([]byte)
		value[0] = 'x'
		copyArgs, err := result.statementCopy()
		require.NoError(t, err)
		require.Equal(t, []byte("encoded"), copyArgs.Args()[2])

		reordered := final
		reordered.bindSlots = []bindSlot{{id: bindID(12)}, {id: bindID(99)}, {id: bindID(11)}}
		_, err = graphPreencodeBaseOccurrences(base, encoded, reordered)
		var planErr *PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "unsupported_keyset_bind", planErr.Code)
	})

	t.Run("a partition limit uses one stable bind token", func(t *testing.T) {
		q := partitionQuery(t)
		node, ok := q.plan.partitionLimitValue.node.(interface{ Argument() any })
		require.True(t, ok)
		token, ok := node.Argument().(bindToken)
		require.True(t, ok)
		require.NotZero(t, token.id)
		require.Equal(t, int64(2), token.value)

		profile, err := engineprofile.Builtin("sqlite-3.35", engineprofile.Version{Known: true, Major: 3, Minor: 35})
		require.NoError(t, err)
		compiler, err := querycompile.New(profile)
		require.NoError(t, err)
		first, err := compileQuery(&compiler, q)
		require.NoError(t, err)
		second, err := compileQuery(&compiler, q)
		require.NoError(t, err)
		findLimit := func(compiled compiledQuery) bindID {
			for index, value := range compiled.statement.Args() {
				if value == int64(2) {
					return compiled.bindSlots[index].id
				}
			}
			return 0
		}
		require.Equal(t, token.id, findLimit(first))
		require.Equal(t, token.id, findLimit(second))
	})
}
