package changeplan_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/stretchr/testify/require"
)

func TestV1Fixtures(t *testing.T) {
	valid, err := changeplan.Read(filepath.Join("testdata", "v1", "valid", "empty.json"))
	require.NoError(t, err)
	require.NotEqual(t, changeplan.PlanID{}, valid.ID())
	for _, name := range []string{"null_arrays.json", "unknown_field.json"} {
		data, readErr := os.ReadFile(filepath.Join("testdata", "v1", "invalid", name))
		require.NoError(t, readErr)
		_, decodeErr := changeplan.Decode(data)
		require.Error(t, decodeErr, name)
	}
}
