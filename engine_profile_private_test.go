package rasql

import (
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/stretchr/testify/require"
)

func TestEngineProfileQueryCompilerPrivateBridge(t *testing.T) {
	p, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	compiler, err := p.queryCompiler(dialect.SQLite())
	require.NoError(t, err)
	require.NotNil(t, compiler)
	_, err = p.queryCompiler(dialect.PostgreSQL())
	require.ErrorIs(t, err, ErrEngineProfileMismatch)
}
