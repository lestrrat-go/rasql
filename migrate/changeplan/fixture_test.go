package changeplan_test

import (
	"bytes"
	"crypto/sha256"
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
	encoded, err := os.ReadFile(filepath.Join("testdata", "v1", "valid", "empty.json"))
	require.NoError(t, err)
	encoded = bytes.TrimSuffix(encoded, []byte{'\n'})
	start := bytes.Index(encoded, []byte(`,"id":"`))
	end := bytes.Index(encoded[start+1:], []byte(`,"profile":`))
	require.GreaterOrEqual(t, start, 0)
	require.Greater(t, end, 0)
	withoutID := append([]byte{}, encoded[:start]...)
	withoutID = append(withoutID, encoded[start+1+end:]...)
	require.Equal(t, changeplan.Digest(sha256.Sum256(withoutID)), changeplan.Digest(valid.ID()))
	for _, name := range []string{"null_arrays.json", "unknown_field.json"} {
		data, readErr := os.ReadFile(filepath.Join("testdata", "v1", "invalid", name))
		require.NoError(t, readErr)
		_, decodeErr := changeplan.Decode(data)
		require.Error(t, decodeErr, name)
	}
}
