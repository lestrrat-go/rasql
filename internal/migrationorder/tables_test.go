package migrationorder

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOrderTables(t *testing.T) {
	tests := map[string]struct {
		tables []TableDependency
		want   []string
	}{
		"chain": {
			tables: []TableDependency{{Key: "child", Display: "child", DependsOn: []string{"parent"}}, {Key: "parent", Display: "parent"}},
			want:   []string{"parent", "child"},
		},
		"diamond": {
			tables: []TableDependency{{Key: "d", Display: "d", DependsOn: []string{"b", "c"}}, {Key: "c", Display: "c", DependsOn: []string{"a"}}, {Key: "b", Display: "b", DependsOn: []string{"a"}}, {Key: "a", Display: "a"}},
			want:   []string{"a", "b", "c", "d"},
		},
		"missing and self": {
			tables: []TableDependency{{Key: "self", Display: "self", DependsOn: []string{"self", "outside"}}},
			want:   []string{"self"},
		},
		"duplicate edge": {
			tables: []TableDependency{{Key: "child", Display: "child", DependsOn: []string{"parent", "parent"}}, {Key: "parent", Display: "parent"}},
			want:   []string{"parent", "child"},
		},
		"stable ties": {
			tables: []TableDependency{{Key: "z", Display: "z"}, {Key: "a", Display: "a"}},
			want:   []string{"a", "z"},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := OrderTables(test.tables)
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}

func TestOrderTablesRejectsInvalidAndCycle(t *testing.T) {
	_, err := OrderTables([]TableDependency{{Key: "", Display: "empty"}})
	require.Error(t, err)
	_, err = OrderTables([]TableDependency{{Key: "same"}, {Key: "same"}})
	require.Error(t, err)
	_, err = OrderTables([]TableDependency{{Key: "a", Display: "aaa", DependsOn: []string{"b"}}, {Key: "b", Display: "bbb", DependsOn: []string{"a"}}})
	require.ErrorContains(t, err, "aaa, bbb")
	require.ErrorContains(t, err, "no CREATE TABLE order")
}
