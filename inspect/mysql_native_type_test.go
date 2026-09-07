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

func TestSQLiteNativeTypeParser(t *testing.T) {
	tests := []struct {
		declaration string
		want        *schema.NativeTypeDef
	}{
		{"VARCHAR(0012)", &schema.NativeTypeDef{Dialect: "sqlite", Name: "VARCHAR", Kind: schema.NativeOther, Arguments: []string{"12"}}},
		{"DOUBLE PRECISION", &schema.NativeTypeDef{Dialect: "sqlite", Name: "DOUBLE PRECISION", Kind: schema.NativeOther}},
		{`"Custom Type"`, &schema.NativeTypeDef{Dialect: "sqlite", Name: "Custom Type", Kind: schema.NativeOther}},
	}
	for _, test := range tests {
		t.Run(test.declaration, func(t *testing.T) {
			got, err := sqliteNativeType(test.declaration)
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
	for _, declaration := range []string{"VARCHAR(", "VARCHAR()", "VARCHAR(1,x)", "bad;type", `"unterminated`} {
		t.Run(declaration, func(t *testing.T) {
			_, err := sqliteNativeType(declaration)
			require.Error(t, err)
		})
	}
}
