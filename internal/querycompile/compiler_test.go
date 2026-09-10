package querycompile

import (
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestNewRejectsForgedProfile(t *testing.T) {
	_, err := New(engineprofile.Profile{ID: "forged", Engine: engineprofile.SQLite, Limits: engineprofile.Limits{MaxBindParameters: 1}})
	require.ErrorIs(t, err, engineprofile.ErrInvalidProfile)
}
