package rasqlgen_test

import (
	"errors"
	"flag"
	"testing"

	"github.com/lestrrat-go/rasql/cli/rasqlgen"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/stretchr/testify/require"
)

func TestExitCodeClassifiesSuccessStaleAndFailure(t *testing.T) {
	require.Equal(t, 0, rasqlgen.ExitCode(nil))
	require.Equal(t, 0, rasqlgen.ExitCode(flag.ErrHelp))
	require.Equal(t, 1, rasqlgen.ExitCode(generate.ErrStale))
	require.Equal(t, 2, rasqlgen.ExitCode(errors.New("configuration failed")))
}
