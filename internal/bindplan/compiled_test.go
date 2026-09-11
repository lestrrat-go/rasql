package bindplan_test

import (
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql/internal/bindplan"
	"github.com/lestrrat-go/rasql/internal/planerr"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

func TestMatchBaseOccurrences(t *testing.T) {
	t.Run("finds each base bind in the paged statement", func(t *testing.T) {
		base := bindplan.Compiled{
			Statement: stmt.New(sqltext.Text("base"), 1, 2),
			Slots:     []bindplan.Slot{{ID: 11, Codec: "a"}, {ID: 12, Codec: "b"}},
		}
		paged := bindplan.Compiled{
			Statement: stmt.New(sqltext.Text("paged"), 9, 1, 2, 10),
			Slots:     []bindplan.Slot{{ID: 90}, {ID: 11, Codec: "a"}, {ID: 12, Codec: "b"}, {ID: 91}},
		}
		indexes, err := bindplan.MatchBaseOccurrences(base, paged)
		require.NoError(t, err)
		require.Equal(t, []int{1, 2}, indexes)
	})

	t.Run("rejects a bind with no identity", func(t *testing.T) {
		base := bindplan.Compiled{
			Statement: stmt.New(sqltext.Text("base"), 1),
			Slots:     []bindplan.Slot{{ID: 0, Codec: "a"}},
		}
		paged := bindplan.Compiled{
			Statement: stmt.New(sqltext.Text("paged"), 1),
			Slots:     []bindplan.Slot{{ID: 11, Codec: "a"}},
		}
		_, err := bindplan.MatchBaseOccurrences(base, paged)
		var planErr *planerr.Error
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "unsupported_keyset_bind", planErr.Code)
	})

	t.Run("rejects a bind whose codec changed", func(t *testing.T) {
		base := bindplan.Compiled{
			Statement: stmt.New(sqltext.Text("base"), 1),
			Slots:     []bindplan.Slot{{ID: 11, Codec: "a"}},
		}
		paged := bindplan.Compiled{
			Statement: stmt.New(sqltext.Text("paged"), 1),
			Slots:     []bindplan.Slot{{ID: 11, Codec: "b"}},
		}
		_, err := bindplan.MatchBaseOccurrences(base, paged)
		var planErr *planerr.Error
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "unsupported_keyset_bind", planErr.Code)
	})

	t.Run("rejects a bind the paged statement dropped", func(t *testing.T) {
		base := bindplan.Compiled{
			Statement: stmt.New(sqltext.Text("base"), 1),
			Slots:     []bindplan.Slot{{ID: 11}},
		}
		paged := bindplan.Compiled{
			Statement: stmt.New(sqltext.Text("paged"), 1),
			Slots:     []bindplan.Slot{{ID: 90}},
		}
		_, err := bindplan.MatchBaseOccurrences(base, paged)
		var planErr *planerr.Error
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "unsupported_keyset_bind", planErr.Code)
	})
}

func TestUnwrap(t *testing.T) {
	t.Run("replaces a token with the value it carries", func(t *testing.T) {
		value, copier, err := bindplan.Adopt(42, true)
		require.NoError(t, err)
		statement := stmt.New(sqltext.Text("SELECT ?"),
			bindplan.Token{ID: 7, Value: value, Copy: copier, Codec: "int"})

		compiled, err := bindplan.Unwrap(statement)
		require.NoError(t, err)
		require.Equal(t, []any{42}, compiled.Statement.Args())
		require.Equal(t, []bindplan.Slot{{ID: 7, Codec: "int"}}, compiled.Slots)
	})

	// Copy runs each bind's copier again, so two statements taken from one
	// compiled plan never share a buffer.
	t.Run("each copy detaches its bound values", func(t *testing.T) {
		value, copier, err := bindplan.Adopt([]byte("original"), true)
		require.NoError(t, err)
		compiled, err := bindplan.Unwrap(stmt.New(sqltext.Text("SELECT ?"),
			bindplan.Token{ID: 1, Value: value, Copy: copier}))
		require.NoError(t, err)

		first, err := compiled.Copy()
		require.NoError(t, err)
		require.Equal(t, []byte("original"), first.BoundArgs()[0])
		first.BoundArgs()[0].([]byte)[0] = 'X'

		second, err := compiled.Copy()
		require.NoError(t, err)
		require.Equal(t, []byte("original"), second.BoundArgs()[0])
	})

	// The token's error is reported by its text rather than wrapped, so this
	// asserts what Unwrap does today rather than errors.Is reaching the cause.
	t.Run("rejects a token that carries an error", func(t *testing.T) {
		_, err := bindplan.Unwrap(stmt.New(sqltext.Text("SELECT ?"),
			bindplan.Token{ID: 1, Err: errUnwrapProbe, Copy: func() (any, error) { return nil, nil }}))
		var planErr *planerr.Error
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "unsnapshotable_bind", planErr.Code)
		require.Contains(t, planErr.Detail, errUnwrapProbe.Error())
	})

	t.Run("rejects a token with no identity", func(t *testing.T) {
		value, copier, err := bindplan.Adopt(1, true)
		require.NoError(t, err)
		_, err = bindplan.Unwrap(stmt.New(sqltext.Text("SELECT ?"),
			bindplan.Token{ID: 0, Value: value, Copy: copier}))
		var planErr *planerr.Error
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "internal_plan", planErr.Code)
	})

	t.Run("rejects a malformed codec", func(t *testing.T) {
		value, copier, err := bindplan.Adopt(1, true)
		require.NoError(t, err)
		_, err = bindplan.Unwrap(stmt.New(sqltext.Text("SELECT ?"),
			bindplan.Token{ID: 1, Value: value, Copy: copier, Codec: "not a codec"}))
		require.Error(t, err)
	})
}

var errUnwrapProbe = errors.New("bind carried an error")
