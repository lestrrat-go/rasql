package graphfingerprint_test

import (
	"database/sql"
	"database/sql/driver"
	"math"
	"reflect"
	"testing"

	"github.com/lestrrat-go/rasql/internal/bindplan"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/graphfingerprint"
	"github.com/lestrrat-go/rasql/internal/graphkey"
	"github.com/lestrrat-go/rasql/internal/planerr"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

type graphFingerprintNamedID int64

func stageFor(t *testing.T) graphfingerprint.Stage {
	t.Helper()
	table := query.MustTableRef(schema.TableDef{
		Name:    "fingerprint_rows",
		Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
	})
	source := query.Relation(table)
	column := source.Column("id")
	columns := []query.ResultColumn{{Name: "id", Type: schema.IntegerType{}}}
	key := &graphkey.Spec{Parts: []*graphkey.PartSpec{{
		Column: column, Source: source.QualifiedName(), Type: reflect.TypeOf(graphFingerprintNamedID(0)),
		ColumnType: schema.IntegerType{},
	}}}
	return graphfingerprint.Stage{
		Name: "child", Source: source.QualifiedName(), Columns: columns, Keys: []*graphkey.Spec{key},
		Compiled: bindplan.Compiled{Statement: stmt.New(sqltext.Text("SELECT id FROM fingerprint_rows"))}, BindLimit: 999,
	}
}

func testProfile() graphfingerprint.Profile {
	return graphfingerprint.Profile{
		Dialect: "sqlite", ID: "sqlite-3.35", Engine: engineprofile.SQLite,
		Version: engineprofile.Version{Known: true, Major: 3, Minor: 35},
		Limits:  engineprofile.Limits{MaxBindParameters: 999}, MaxBind: 999,
		Capabilities: engineprofile.Capabilities{WindowFunctions: true, PerParentLimit: engineprofile.PerParentLimitWindow},
	}
}

func TestGraphFingerprint(t *testing.T) {
	t.Run("separates profile, dialect and capabilities", func(t *testing.T) {
		stage := stageFor(t)
		base, err := graphfingerprint.Invocation(stage, testProfile())
		require.NoError(t, err)

		dialectProfile := testProfile()
		dialectProfile.Dialect = "sqlite-extension"
		dialectKey, err := graphfingerprint.Invocation(stage, dialectProfile)
		require.NoError(t, err)
		require.NotEqual(t, base, dialectKey)

		capabilityProfile := testProfile()
		capabilityProfile.Capabilities.WindowFunctions = false
		capabilityKey, err := graphfingerprint.Invocation(stage, capabilityProfile)
		require.NoError(t, err)
		require.NotEqual(t, base, capabilityKey)

		updateDefaultProfile := testProfile()
		updateDefaultProfile.Capabilities.UpdateDefault = engineprofile.UpdateDefaultExpression
		updateDefaultKey, err := graphfingerprint.Invocation(stage, updateDefaultProfile)
		require.NoError(t, err)
		require.NotEqual(t, base, updateDefaultKey)
	})

	t.Run("separates the logical key and stage metadata", func(t *testing.T) {
		stage := stageFor(t)
		base, err := graphfingerprint.Invocation(stage, testProfile())
		require.NoError(t, err)

		logicalStage := stage
		logicalStage.Columns = []query.ResultColumn{{Name: "id", Type: schema.IntegerType{Unsigned: true}}}
		logicalKey, err := graphfingerprint.Invocation(logicalStage, testProfile())
		require.NoError(t, err)
		require.NotEqual(t, base, logicalKey)

		nullableStage := stage
		nullableStage.Keys = []*graphkey.Spec{{Parts: []*graphkey.PartSpec{{
			Column: stage.Keys[0].Parts[0].Column, Source: stage.Keys[0].Parts[0].Source,
			Type: stage.Keys[0].Parts[0].Type, ColumnType: stage.Keys[0].Parts[0].ColumnType, Nullable: true,
		}}}}
		nullableKey, err := graphfingerprint.Invocation(nullableStage, testProfile())
		require.NoError(t, err)
		require.NotEqual(t, base, nullableKey)

		junctionStage := stage
		junctionStage.Name = "junction"
		junctionStage.Source = "fingerprint_junction"
		junctionStage.Keys = []*graphkey.Spec{stage.Keys[0], nullableStage.Keys[0]}
		junctionKey, err := graphfingerprint.Invocation(junctionStage, testProfile())
		require.NoError(t, err)
		require.NotEqual(t, base, junctionKey)
	})

	t.Run("rejects a bind argument count mismatch", func(t *testing.T) {
		stage := stageFor(t)
		stage.Compiled.Slots = []bindplan.Slot{{Codec: ""}}
		_, err := graphfingerprint.Invocation(stage, testProfile())
		var planErr *planerr.Error
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "internal_plan", planErr.Code)

		stage.Compiled.Statement = stmt.New(sqltext.Text("SELECT id FROM fingerprint_rows"), int64(1), int64(2))
		_, err = graphfingerprint.Invocation(stage, testProfile())
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "internal_plan", planErr.Code)
	})

	t.Run("frames canonical fixed values", func(t *testing.T) {
		stage := stageFor(t)
		stage.Compiled.Statement = stmt.New(sqltext.Text("SELECT id FROM fingerprint_rows"),
			float64(0), float64(math.Copysign(0, -1)), sql.Named("payload", math.Float64frombits(0x7ff8000000000042)),
		)
		stage.Compiled.Slots = []bindplan.Slot{{PreEncoded: true}, {PreEncoded: true}, {PreEncoded: true}}
		base, err := graphfingerprint.Invocation(stage, testProfile())
		require.NoError(t, err)

		zero := stage
		zero.Compiled.Statement = stmt.New(sqltext.Text("SELECT id FROM fingerprint_rows"),
			float64(0), float64(0), sql.Named("payload", math.Float64frombits(0x7ff8000000000042)),
		)
		zeroKey, err := graphfingerprint.Invocation(zero, testProfile())
		require.NoError(t, err)
		require.NotEqual(t, base.Digest, zeroKey.Digest)

		nan := stage
		nan.Compiled.Statement = stmt.New(sqltext.Text("SELECT id FROM fingerprint_rows"),
			float64(0), float64(math.Copysign(0, -1)), sql.Named("payload", math.Float64frombits(0x7ff8000000000043)),
		)
		nanKey, err := graphfingerprint.Invocation(nan, testProfile())
		require.NoError(t, err)
		require.NotEqual(t, base.Digest, nanKey.Digest)

		changed := stage
		changed.Compiled.Statement = stmt.New(sqltext.Text("SELECT id FROM fingerprint_rows"),
			float64(0), float64(math.Copysign(0, -1)), sql.Named("other", math.Float64frombits(0x7ff8000000000042)),
		)
		changedKey, err := graphfingerprint.Invocation(changed, testProfile())
		require.NoError(t, err)
		require.NotEqual(t, base.Digest, changedKey.Digest)

		invalid := stage
		invalid.Compiled.Statement = stmt.New(sqltext.Text("SELECT id FROM fingerprint_rows"), 1, float64(0), float64(0))
		_, err = graphfingerprint.Invocation(invalid, testProfile())
		var planErr *planerr.Error
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "internal_plan", planErr.Code)

		valuer := stage
		valuer.Compiled.Statement = stmt.New(sqltext.Text("SELECT id FROM fingerprint_rows"), graphFingerprintValueValuer{}, float64(0), float64(0))
		_, err = graphfingerprint.Invocation(valuer, testProfile())
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "internal_plan", planErr.Code)
	})

	t.Run("encodes every profile capability field", func(t *testing.T) {
		stage := stageFor(t)
		baseProfile := testProfile()
		base, err := graphfingerprint.Invocation(stage, baseProfile)
		require.NoError(t, err)
		capabilities := baseProfile.Capabilities
		values := []func(*engineprofile.Capabilities){
			func(value *engineprofile.Capabilities) { value.Returning = engineprofile.ReturningInsert },
			func(value *engineprofile.Capabilities) { value.Upsert = engineprofile.UpsertOnConflict },
			func(value *engineprofile.Capabilities) { value.ConflictTarget = true },
			func(value *engineprofile.Capabilities) { value.DefaultValues = true },
			func(value *engineprofile.Capabilities) { value.EmptyInsert = true },
			func(value *engineprofile.Capabilities) { value.DefaultValuesUpsert = true },
			func(value *engineprofile.Capabilities) { value.SubqueryLimit = true },
			func(value *engineprofile.Capabilities) { value.WriteSubqueryTarget = true },
			func(value *engineprofile.Capabilities) { value.PartialIndex = true },
			func(value *engineprofile.Capabilities) { value.AggregateFilter = true },
			func(value *engineprofile.Capabilities) { value.QualifiedReference = true },
			func(value *engineprofile.Capabilities) { value.QualifiedIndexTarget = true },
			func(value *engineprofile.Capabilities) { value.QualifiedIndexName = true },
			func(value *engineprofile.Capabilities) { value.MatchOperator = true },
			func(value *engineprofile.Capabilities) { value.SelectForUpdate = true },
			func(value *engineprofile.Capabilities) { value.SelectForShare = true },
			func(value *engineprofile.Capabilities) { value.SelectLockOf = true },
			func(value *engineprofile.Capabilities) { value.SelectLockNoWait = true },
			func(value *engineprofile.Capabilities) { value.SelectLockSkipLocked = true },
			func(value *engineprofile.Capabilities) { value.UpsertConflictWhere = true },
			func(value *engineprofile.Capabilities) { value.UpsertUpdateWhere = true },
			func(value *engineprofile.Capabilities) { value.WindowFunctions = false },
			func(value *engineprofile.Capabilities) { value.LateralJoins = true },
			func(value *engineprofile.Capabilities) { value.Savepoints = true },
			func(value *engineprofile.Capabilities) { value.TransactionalDDL = true },
			func(value *engineprofile.Capabilities) { value.ExplicitNullOrdering = true },
			func(value *engineprofile.Capabilities) { value.TupleComparison = true },
			func(value *engineprofile.Capabilities) { value.UpdateDefault = engineprofile.UpdateDefaultExpression },
		}
		for index, change := range values {
			profile := baseProfile
			profile.Capabilities = capabilities
			change(&profile.Capabilities)
			key, keyErr := graphfingerprint.Invocation(stage, profile)
			require.NoError(t, keyErr, "capability %d", index)
			require.NotEqual(t, base.Digest, key.Digest, "capability %d", index)
		}
	})
}

type graphFingerprintValueValuer struct{}

func (graphFingerprintValueValuer) Value() (driver.Value, error) { return int64(1), nil }
