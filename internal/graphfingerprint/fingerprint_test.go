package graphfingerprint_test

import (
	"database/sql"
	"database/sql/driver"
	"math"
	"reflect"
	"testing"

	"github.com/lestrrat-go/rasql/internal/bindplan"
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

func stageFor(t *testing.T, node any, decoder any) graphfingerprint.Stage {
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
		Node: node, Decoder: decoder,
	}
}

// requireDistinctIndexes fails if any two of the given indexes are equal.
func requireDistinctIndexes(t *testing.T, indexes ...int) {
	t.Helper()
	seen := make(map[int]struct{}, len(indexes))
	for _, index := range indexes {
		_, duplicate := seen[index]
		require.False(t, duplicate, "index %d repeated", index)
		seen[index] = struct{}{}
	}
}

func TestGraphStageIdentity(t *testing.T) {
	t.Run("separates the logical key and stage metadata", func(t *testing.T) {
		stage := stageFor(t, nil, nil)
		stages := &graphfingerprint.Stages{}
		baseIndex, err := stages.Index(stage)
		require.NoError(t, err)

		logicalStage := stage
		logicalStage.Columns = []query.ResultColumn{{Name: "id", Type: schema.IntegerType{Unsigned: true}}}
		logicalIndex, err := stages.Index(logicalStage)
		require.NoError(t, err)

		nullableStage := stage
		nullableStage.Keys = []*graphkey.Spec{{Parts: []*graphkey.PartSpec{{
			Column: stage.Keys[0].Parts[0].Column, Source: stage.Keys[0].Parts[0].Source,
			Type: stage.Keys[0].Parts[0].Type, ColumnType: stage.Keys[0].Parts[0].ColumnType, Nullable: true,
		}}}}
		nullableIndex, err := stages.Index(nullableStage)
		require.NoError(t, err)

		junctionStage := stage
		junctionStage.Name = "junction"
		junctionStage.Source = "fingerprint_junction"
		junctionStage.Keys = []*graphkey.Spec{stage.Keys[0], nullableStage.Keys[0]}
		junctionIndex, err := stages.Index(junctionStage)
		require.NoError(t, err)

		requireDistinctIndexes(t, baseIndex, logicalIndex, nullableIndex, junctionIndex)
	})

	t.Run("rejects a bind argument count mismatch", func(t *testing.T) {
		stage := stageFor(t, nil, nil)
		stage.Compiled.Slots = []bindplan.Slot{{Codec: ""}}
		_, err := (&graphfingerprint.Stages{}).Index(stage)
		var planErr *planerr.Error
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "internal_plan", planErr.Code)

		stage.Compiled.Statement = stmt.New(sqltext.Text("SELECT id FROM fingerprint_rows"), int64(1), int64(2))
		_, err = (&graphfingerprint.Stages{}).Index(stage)
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "internal_plan", planErr.Code)
	})

	t.Run("frames canonical fixed values", func(t *testing.T) {
		stage := stageFor(t, nil, nil)
		stage.Compiled.Statement = stmt.New(sqltext.Text("SELECT id FROM fingerprint_rows"),
			float64(0), float64(math.Copysign(0, -1)), sql.Named("payload", math.Float64frombits(0x7ff8000000000042)),
		)
		stage.Compiled.Slots = []bindplan.Slot{{PreEncoded: true}, {PreEncoded: true}, {PreEncoded: true}}
		stages := &graphfingerprint.Stages{}
		baseIndex, err := stages.Index(stage)
		require.NoError(t, err)

		zero := stage
		zero.Compiled.Statement = stmt.New(sqltext.Text("SELECT id FROM fingerprint_rows"),
			float64(0), float64(0), sql.Named("payload", math.Float64frombits(0x7ff8000000000042)),
		)
		zeroIndex, err := stages.Index(zero)
		require.NoError(t, err)

		nan := stage
		nan.Compiled.Statement = stmt.New(sqltext.Text("SELECT id FROM fingerprint_rows"),
			float64(0), float64(math.Copysign(0, -1)), sql.Named("payload", math.Float64frombits(0x7ff8000000000043)),
		)
		nanIndex, err := stages.Index(nan)
		require.NoError(t, err)

		changed := stage
		changed.Compiled.Statement = stmt.New(sqltext.Text("SELECT id FROM fingerprint_rows"),
			float64(0), float64(math.Copysign(0, -1)), sql.Named("other", math.Float64frombits(0x7ff8000000000042)),
		)
		changedIndex, err := stages.Index(changed)
		require.NoError(t, err)

		requireDistinctIndexes(t, baseIndex, zeroIndex, nanIndex, changedIndex)

		invalid := stage
		invalid.Compiled.Statement = stmt.New(sqltext.Text("SELECT id FROM fingerprint_rows"), 1, float64(0), float64(0))
		_, err = stages.Index(invalid)
		var planErr *planerr.Error
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "internal_plan", planErr.Code)

		valuer := stage
		valuer.Compiled.Statement = stmt.New(sqltext.Text("SELECT id FROM fingerprint_rows"), graphFingerprintValueValuer{}, float64(0), float64(0))
		_, err = stages.Index(valuer)
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "internal_plan", planErr.Code)
	})
}

// graphStageConfigDecoder is two decoders of one type that differ only in
// configuration, which must not share a cache entry even though their schemas
// match.
type graphStageConfigDecoder struct{ mode string }

// graphStageFuncDecoder carries a func field. reflect.DeepEqual reports two
// non-nil funcs unequal even when they are the same function, which is why a
// record from the same node skips DeepEqual.
type graphStageFuncDecoder struct{ convert func([]byte) []byte }

func graphStageIdentity(value []byte) []byte { return value }

// graphStageNode stands in for a *graphPlanNode: a distinct pointer identifies
// which plan node a stage's decoder came from. It carries a field so two
// separately allocated nodes get distinct addresses; a zero-size struct type
// can have every instance share one address, which would defeat what this type
// exists to test.
type graphStageNode struct{ _ int }

func TestGraphStageEquality(t *testing.T) {
	t.Run("the same node shares a decoder holding a func", func(t *testing.T) {
		node := &graphStageNode{}
		first := stageFor(t, node, graphStageFuncDecoder{convert: graphStageIdentity})
		second := stageFor(t, node, graphStageFuncDecoder{convert: graphStageIdentity})
		stages := &graphfingerprint.Stages{}
		firstIndex, err := stages.Index(first)
		require.NoError(t, err)
		secondIndex, err := stages.Index(second)
		require.NoError(t, err)
		require.Equal(t, firstIndex, secondIndex)
	})

	t.Run("different nodes share an equal decoder", func(t *testing.T) {
		first := stageFor(t, &graphStageNode{}, graphStageConfigDecoder{mode: "same"})
		second := stageFor(t, &graphStageNode{}, graphStageConfigDecoder{mode: "same"})
		stages := &graphfingerprint.Stages{}
		firstIndex, err := stages.Index(first)
		require.NoError(t, err)
		secondIndex, err := stages.Index(second)
		require.NoError(t, err)
		require.Equal(t, firstIndex, secondIndex)
	})

	t.Run("one decoder type with two configurations does not share", func(t *testing.T) {
		first := stageFor(t, &graphStageNode{}, graphStageConfigDecoder{mode: "first"})
		second := stageFor(t, &graphStageNode{}, graphStageConfigDecoder{mode: "second"})
		stages := &graphfingerprint.Stages{}
		firstIndex, err := stages.Index(first)
		require.NoError(t, err)
		secondIndex, err := stages.Index(second)
		require.NoError(t, err)
		require.NotEqual(t, firstIndex, secondIndex)
	})

	t.Run("different nodes do not share a decoder holding a func", func(t *testing.T) {
		first := stageFor(t, &graphStageNode{}, graphStageFuncDecoder{convert: graphStageIdentity})
		second := stageFor(t, &graphStageNode{}, graphStageFuncDecoder{convert: graphStageIdentity})
		stages := &graphfingerprint.Stages{}
		firstIndex, err := stages.Index(first)
		require.NoError(t, err)
		secondIndex, err := stages.Index(second)
		require.NoError(t, err)
		require.NotEqual(t, firstIndex, secondIndex)
	})
}

type graphFingerprintValueValuer struct{}

func (graphFingerprintValueValuer) Value() (driver.Value, error) { return int64(1), nil }
