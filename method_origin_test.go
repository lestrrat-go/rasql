package rasql

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOriginForFile covers how a method's origin is read out of the file name a
// build reports for it. The classification is unexported, so this test states it
// directly: a name the package was not written against is an origin it refuses
// to guess at rather than one it reads as declared.
func TestOriginForFile(t *testing.T) {
	for _, testCase := range []struct {
		name string
		file string
		want methodOrigin
	}{
		{name: "absolute source path", file: "/home/ada/rasql/typed_write.go", want: methodOriginDeclared},
		{name: "relative source path", file: "typed_write.go", want: methodOriginDeclared},
		{name: "assembly source", file: "asm_amd64.s", want: methodOriginDeclared},
		{name: "generated wrapper", file: generatedMethodFile, want: methodOriginGenerated},
		{name: "another placeholder", file: "<generated>", want: methodOriginUnknown},
		{name: "no line table", file: "?", want: methodOriginUnknown},
		{name: "no name at all", file: "", want: methodOriginUnknown},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			require.Equal(t, testCase.want, originForFile(testCase.file))
		})
	}
}
