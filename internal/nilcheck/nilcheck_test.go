package nilcheck_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/internal/nilcheck"
	"github.com/stretchr/testify/require"
)

type nilcheckStruct struct {
	Field int
}

func TestIs(t *testing.T) {
	var nilPointer *nilcheckStruct
	nonNilPointer := &nilcheckStruct{}
	var nilMap map[string]int
	var nilSlice []int
	var nilFunc func()
	var nilChan chan int
	var nilInterface error

	testCases := map[string]struct {
		value any
		want  bool
	}{
		"untyped nil":                     {value: nil, want: true},
		"nil pointer behind an interface": {value: nilPointer, want: true},
		"non-nil pointer":                 {value: nonNilPointer, want: false},
		"nil map":                         {value: nilMap, want: true},
		"nil slice":                       {value: nilSlice, want: true},
		"nil func":                        {value: nilFunc, want: true},
		"nil chan":                        {value: nilChan, want: true},
		"nil interface value":             {value: nilInterface, want: true},
		"non-pointer struct":              {value: nilcheckStruct{}, want: false},
	}

	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, testCase.want, nilcheck.Is(testCase.value))
		})
	}
}
