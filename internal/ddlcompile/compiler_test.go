package ddlcompile

import (
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestZeroCompilerRejectsEveryEntryPoint(t *testing.T) {
	var compiler Compiler
	_, err := compiler.DropTable(schema.ObjectName{Name: "items"})
	require.ErrorIs(t, err, engineprofile.ErrInvalidProfile)
}
