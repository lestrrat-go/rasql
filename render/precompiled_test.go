package render_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/render"
	"github.com/stretchr/testify/require"
)

func TestPrecompiled(t *testing.T) {
	t.Run("empty string", func(t *testing.T) {
		_, err := render.Precompiled("")
		require.Error(t, err)

		var renderErr *render.Error
		require.ErrorAs(t, err, &renderErr)
		require.EqualError(t, err, "render: precompiled SQL must not be empty")
	})

	t.Run("whitespace only", func(t *testing.T) {
		_, err := render.Precompiled("   ")
		require.Error(t, err)

		var renderErr *render.Error
		require.ErrorAs(t, err, &renderErr)
		require.EqualError(t, err, "render: precompiled SQL must not be empty")
	})

	t.Run("success", func(t *testing.T) {
		stmt, err := render.Precompiled("SELECT * FROM users WHERE id = ?", 1)
		require.NoError(t, err)
		require.Equal(t, "SELECT * FROM users WHERE id = ?", stmt.SQL())
		require.Equal(t, []any{1}, stmt.Args())
	})
}
