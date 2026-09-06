package inspect

import (
	"testing"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestMySQLNativeTypeParser(t *testing.T) {
	tests := []struct {
		declaration string
		wantKind    schema.NativeTypeKind
		want        []string
	}{
		{"enum('needs,comma','quote''s','  spaced  ','back\\\\slash','')", schema.NativeEnum, []string{"needs,comma", "quote's", "  spaced  ", "back\\slash", ""}},
		{"set('one','two')", schema.NativeSet, []string{"one", "two"}},
	}
	for _, test := range tests {
		t.Run(test.declaration, func(t *testing.T) {
			native, ok, err := mysqlNativeType(test.declaration)
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, test.wantKind, native.Kind)
			require.Equal(t, test.want, native.Arguments)
		})
	}
	for _, declaration := range []string{"enum('unterminated)", "enum(foo)", "enum('ok') trailing", "enum('ok',)"} {
		t.Run(declaration, func(t *testing.T) {
			_, _, err := mysqlNativeType(declaration)
			require.Error(t, err)
		})
	}
}
