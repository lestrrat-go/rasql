package schema_test

import (
	"encoding/json"
	"testing"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestGoBindingCloneAndJSONRoundTripOwnImports(t *testing.T) {
	table := schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.TextType{}, GoBinding: &schema.GoBinding{
		Type: "ids.ID", Imports: []schema.GoImport{{Path: "example.com/ids", Name: "ids"}},
	}}}}
	clone := table.Clone()
	clone.Columns[0].GoBinding.Imports[0].Name = "renamed"
	require.Equal(t, "ids", table.Columns[0].GoBinding.Imports[0].Name)
	encoded, err := json.Marshal(table)
	require.NoError(t, err)
	var decoded schema.TableDef
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.Equal(t, table.Columns[0].GoBinding, decoded.Columns[0].GoBinding)
	decoded.Columns[0].GoBinding.Imports[0].Path = "changed"
	require.Equal(t, "example.com/ids", table.Columns[0].GoBinding.Imports[0].Path)
}
