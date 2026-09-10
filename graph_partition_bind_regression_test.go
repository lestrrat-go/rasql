package rasql

import (
	"testing"

	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/querycompile"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

func TestGraphStageCacheabilityRequiresStableBindMetadata(t *testing.T) {
	copyArg := func() (any, error) { return int64(1), nil }
	base := compiledQuery{
		statement: stmt.New(sqltext.Text("SELECT ?"), int64(1)),
		bindSlots: []bindSlot{{id: bindID(1)}},
		copyArgs:  []bindValueCopy{copyArg},
	}
	require.True(t, graphStageCacheable(base))

	base.bindSlots[0].id = 0
	require.False(t, graphStageCacheable(base))
	base.bindSlots[0].id = bindID(1)
	base.copyArgs = nil
	require.False(t, graphStageCacheable(base))
}

func TestGraphPreencodeBaseOccurrencesMatchesByIdentityAndDetaches(t *testing.T) {
	base := compiledQuery{
		statement: stmt.New(sqltext.Text("SELECT ? AND ?"), int64(1), []byte("base")),
		bindSlots: []bindSlot{{id: bindID(11)}, {id: bindID(12)}},
		copyArgs: []bindValueCopy{
			func() (any, error) { return int64(1), nil },
			func() (any, error) { return []byte("base"), nil },
		},
	}
	encoded := stmt.New(sqltext.Text("SELECT ? AND ?"), int64(7), []byte("encoded"))
	final := compiledQuery{
		statement: stmt.New(sqltext.Text("SELECT ? AND ? AND ?"), int64(98), int64(99), []byte("old")),
		bindSlots: []bindSlot{{id: bindID(11)}, {id: bindID(99)}, {id: bindID(12)}},
		copyArgs: []bindValueCopy{
			func() (any, error) { return int64(98), nil },
			func() (any, error) { return int64(99), nil },
			func() (any, error) { return []byte("old"), nil },
		},
	}
	result, err := graphPreencodeBaseOccurrences(base, encoded, final)
	require.NoError(t, err)
	require.Equal(t, []any{int64(7), int64(99), []byte("encoded")}, result.statement.Args())
	require.False(t, result.bindSlots[1].preEncoded)
	require.True(t, result.bindSlots[2].preEncoded)
	require.True(t, result.bindSlots[0].preEncoded)
	require.False(t, final.bindSlots[0].preEncoded)

	value := result.statement.Args()[2].([]byte)
	value[0] = 'x'
	copyArgs, err := result.statementCopy()
	require.NoError(t, err)
	require.Equal(t, []byte("encoded"), copyArgs.Args()[2])

	reordered := final
	reordered.bindSlots = []bindSlot{{id: bindID(12)}, {id: bindID(99)}, {id: bindID(11)}}
	_, err = graphPreencodeBaseOccurrences(base, encoded, reordered)
	var planErr *PlanError
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "unsupported_keyset_bind", planErr.Code)
}

func TestPartitionLimitUsesOneStableBindToken(t *testing.T) {
	q := partitionQuery(t)
	node, ok := q.plan.partitionLimitValue.node.(interface{ Argument() any })
	require.True(t, ok)
	token, ok := node.Argument().(bindToken)
	require.True(t, ok)
	require.NotZero(t, token.id)
	require.Equal(t, int64(2), token.value)

	profile, err := engineprofile.Builtin("sqlite-3.35", engineprofile.Version{Known: true, Major: 3, Minor: 35})
	require.NoError(t, err)
	compiler, err := querycompile.New(profile)
	require.NoError(t, err)
	first, err := compileQuery(&compiler, q)
	require.NoError(t, err)
	second, err := compileQuery(&compiler, q)
	require.NoError(t, err)
	findLimit := func(compiled compiledQuery) bindID {
		for index, value := range compiled.statement.Args() {
			if value == int64(2) {
				return compiled.bindSlots[index].id
			}
		}
		return 0
	}
	require.Equal(t, token.id, findLimit(first))
	require.Equal(t, token.id, findLimit(second))
}
