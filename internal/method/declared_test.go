package method_test

import (
	"reflect"
	"testing"

	"github.com/lestrrat-go/rasql/internal/method"
	"github.com/stretchr/testify/require"
)

// valueMapper declares Map with a value receiver.
type valueMapper struct{}

func (valueMapper) Map() {}

// pointerMapper declares Map with a pointer receiver.
type pointerMapper struct{}

func (*pointerMapper) Map() {}

// valuePromoter declares nothing and promotes valueMapper's Map.
type valuePromoter struct {
	valueMapper
}

// pointerPromoter declares nothing and promotes pointerMapper's Map, which
// reaches only its pointer type.
type pointerPromoter struct {
	pointerMapper
}

// redeclaringPromoter embeds valueMapper and declares Map of its own, so Go
// dispatches to the declared method rather than to the promoted one.
type redeclaringPromoter struct {
	valueMapper
}

func (m redeclaringPromoter) Map() {
	m.valueMapper.Map()
}

// TestDeclared covers the answer Declared gives for each shape a mapping rule
// routes by. The toolchain premise underneath it -- that a generated method is
// reported as coming from another file than a declared one -- is stated by
// TestGeneratedMethodsReportGeneratedFile in the rasql package, over a table of
// row types that covers these shapes and the embedded pointer and embedded
// interface as well.
func TestDeclared(t *testing.T) {
	for _, test := range []struct {
		name     string
		rowType  reflect.Type
		method   string
		declared bool
	}{
		{name: "value receiver", rowType: reflect.TypeFor[valueMapper](), method: "Map", declared: true},
		{name: "pointer receiver", rowType: reflect.TypeFor[pointerMapper](), method: "Map", declared: true},
		{name: "promoted from a value", rowType: reflect.TypeFor[valuePromoter](), method: "Map", declared: false},
		{name: "promoted from a pointer", rowType: reflect.TypeFor[pointerPromoter](), method: "Map", declared: false},
		{name: "declared over a promoted one", rowType: reflect.TypeFor[redeclaringPromoter](), method: "Map", declared: true},
		{name: "no such method", rowType: reflect.TypeFor[valueMapper](), method: "Missing", declared: false},
		{name: "not a struct", rowType: reflect.TypeFor[int](), method: "Map", declared: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			declared, err := method.Declared(test.rowType, test.method)
			require.NoError(t, err)
			require.Equal(t, test.declared, declared)
		})
	}
}
