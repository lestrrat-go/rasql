package rasql_test

import (
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// PlanError is an alias now, so this checks the parts a caller depends on
// still behave: the fields read, errors.As matches, and a wrapped cause is
// still reachable through errors.Is.
func TestPlanErrorStaysUsable(t *testing.T) {
	_, err := rasql.NewPageSpec[planErrorRow](nil)
	require.Error(t, err)

	var planErr *rasql.PlanError
	require.ErrorAs(t, err, &planErr)
	require.NotEmpty(t, planErr.Code)
	require.NotEmpty(t, planErr.Error())
	require.Contains(t, planErr.Error(), planErr.Code)
}

// A projection item whose bind snapshot failed makes Validate report a
// PlanError that wraps the original cause, which is the path the alias has to
// keep working.
func TestPlanErrorUnwrapsItsCause(t *testing.T) {
	result, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "flag", Type: schema.IntegerType{}})
	require.NoError(t, err)
	projection, err := rasql.NewProjection(
		[]rasql.ProjectionItem{rasql.Item("flag", rasql.Value(planErrorBind(1)), schema.IntegerType{}, "")},
		planErrorDecoder{result: result},
	)
	require.NoError(t, err)
	table, err := rasql.ReadTableOf[planErrorRow](schema.TableDef{
		Name:    "plan_error_items",
		Columns: []schema.ColumnDef{{Name: "flag", Type: schema.IntegerType{}}},
	})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "")
	require.NoError(t, err)

	err = rasql.Select(relation.Source(), projection).Validate()
	require.Error(t, err)
	var planErr *rasql.PlanError
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "unsnapshotable_bind", planErr.Code)
	require.ErrorIs(t, err, errPlanErrorBind, "the cause survives the alias")
}

type planErrorDecoder struct{ result rasql.ResultSchema }

func (d planErrorDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (planErrorDecoder) Presence() []rasql.Presence         { return nil }
func (planErrorDecoder) DecodeRow(source rasql.ScanSource, row *planErrorRow) error {
	return source.Scan(&row.Flag)
}

var errPlanErrorBind = errors.New("plan error bind failed")

type planErrorBind int64

func (planErrorBind) SnapshotBind() (planErrorBind, error) { return 0, errPlanErrorBind }

type planErrorRow struct {
	ID   int64
	Flag planErrorBind
}
