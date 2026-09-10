package rasql_test

import (
	"context"
	"database/sql"
	"encoding/binary"
	"math"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/cursorcodec"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// cursorFixture pages over a two-row table, so the first request always hands
// back a cursor carrying the fingerprint the query really produced. A test
// then edits one part of that cursor and asks what the next request makes of
// it, which is the only way to see a metadata rejection rather than the
// fingerprint rejection that any hand-built cursor would hit first.
type cursorFixture struct {
	query    rasql.Query[cursorRow]
	spec     rasql.PageSpec[cursorRow]
	executor rasql.Executor
	counter  *cursorCountingExecutor
}

func cursorFixtureFor(t *testing.T) cursorFixture {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.ExecContext(t.Context(),
		`CREATE TABLE cursor_rows (value REAL NOT NULL); INSERT INTO cursor_rows VALUES (1.0), (2.0)`)
	require.NoError(t, err)

	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	base, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	counter := &cursorCountingExecutor{Executor: base}
	// WithEngineProfile re-attaches the compiler the decorator does not carry,
	// which is what lets a counting wrapper sit in the chain from outside the
	// package.
	executor, err := rasql.WithEngineProfile(counter, profile)
	require.NoError(t, err)

	table, err := rasql.ReadTableOf[cursorRow](schema.TableDef{
		Name:    "cursor_rows",
		Columns: []schema.ColumnDef{{Name: "value", Type: schema.FloatType{}}},
	})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "p")
	require.NoError(t, err)
	value, err := rasql.BindColumn[cursorRow, float64](relation, "value", "")
	require.NoError(t, err)
	resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "value", Type: schema.FloatType{}})
	require.NoError(t, err)
	projection, err := rasql.NewProjection(
		[]rasql.ProjectionItem{rasql.Item("value", value.Expr(), schema.FloatType{}, "")},
		cursorDecoder{schema: resultSchema},
	)
	require.NoError(t, err)
	key := rasql.AscKey[cursorRow](value.Expr(), func(row cursorRow) float64 { return row.Value })
	spec, err := rasql.NewPageSpec([]rasql.PageKey[cursorRow]{key}, key)
	require.NoError(t, err)

	return cursorFixture{
		query:    rasql.Select(relation.Source(), projection),
		spec:     spec,
		executor: executor,
		counter:  counter,
	}
}

func (f cursorFixture) pageAfter(t *testing.T, cursor rasql.Cursor) (rasql.Page[cursorRow], error) {
	t.Helper()
	return rasql.PageAfter(t.Context(), f.executor, f.query, f.spec,
		rasql.PagePolicy{DefaultLimit: 1, MaxLimit: 2}, rasql.PageRequest{Limit: 1, After: cursor})
}

// validCursor returns the cursor a first page hands back, and resets the query
// count so a later assertion measures only the request under test.
func (f cursorFixture) validCursor(t *testing.T) rasql.Cursor {
	t.Helper()
	page, err := f.pageAfter(t, "")
	require.NoError(t, err)
	require.True(t, page.HasMore)
	require.NotEmpty(t, page.Next)
	f.counter.queries = 0
	return page.Next
}

// reframe rebuilds a cursor with edited field metadata, keeping the
// fingerprint and value the original carried.
func reframe(t *testing.T, cursor rasql.Cursor, edit func(*cursorcodec.Field)) rasql.Cursor {
	t.Helper()
	envelope, err := cursorcodec.DecodeEnvelope(string(cursor))
	require.NoError(t, err)
	require.Len(t, envelope.Fields, 1)
	edit(&envelope.Fields[0])
	encoded, err := cursorcodec.EncodeEnvelope(envelope.Fingerprint, envelope.Fields, envelope.Values)
	require.NoError(t, err)
	return rasql.Cursor(encoded)
}

func TestCursorAgainstPageSpec(t *testing.T) {
	t.Run("a first page hands back a cursor its own query accepts", func(t *testing.T) {
		fixture := cursorFixtureFor(t)
		next := fixture.validCursor(t)
		page, err := fixture.pageAfter(t, next)
		require.NoError(t, err)
		require.Equal(t, []cursorRow{{Value: 2}}, page.Values)
		require.False(t, page.HasMore)
	})

	t.Run("rejects metadata that no longer matches the page keys", func(t *testing.T) {
		edits := map[string]func(*cursorcodec.Field){
			"direction":  func(f *cursorcodec.Field) { f.Direction = uint8(rasql.PageDescending) },
			"nullable":   func(f *cursorcodec.Field) { f.Nullable = true },
			"null order": func(f *cursorcodec.Field) { f.Nulls = uint8(rasql.NullsLast) },
			"codec":      func(f *cursorcodec.Field) { f.Codec = "some.codec" },
		}
		for name, edit := range edits {
			t.Run(name, func(t *testing.T) {
				fixture := cursorFixtureFor(t)
				next := fixture.validCursor(t)
				_, err := fixture.pageAfter(t, reframe(t, next, edit))
				require.ErrorIs(t, err, rasql.ErrInvalidCursor)
				require.Zero(t, fixture.counter.queries, "a refused cursor must not reach the database")
			})
		}
	})

	t.Run("rejects a cursor whose arity differs from the spec", func(t *testing.T) {
		fixture := cursorFixtureFor(t)
		next := fixture.validCursor(t)
		envelope, err := cursorcodec.DecodeEnvelope(string(next))
		require.NoError(t, err)
		encoded, err := cursorcodec.EncodeEnvelope(envelope.Fingerprint,
			append(envelope.Fields, envelope.Fields[0]), append(envelope.Values, envelope.Values[0]))
		require.NoError(t, err)

		_, err = fixture.pageAfter(t, rasql.Cursor(encoded))
		require.ErrorIs(t, err, rasql.ErrInvalidCursor)
		require.Zero(t, fixture.counter.queries)
	})

	// Two guards refuse this today, one in decodePageCursor and one in
	// keysetPredicate, so the assertion is on the outcome rather than on
	// either check.
	t.Run("rejects an absent value for a key that is not nullable", func(t *testing.T) {
		fixture := cursorFixtureFor(t)
		next := fixture.validCursor(t)
		envelope, err := cursorcodec.DecodeEnvelope(string(next))
		require.NoError(t, err)
		encoded, err := cursorcodec.EncodeEnvelope(envelope.Fingerprint, envelope.Fields,
			[]cursorcodec.Value{{}})
		require.NoError(t, err)

		_, err = fixture.pageAfter(t, rasql.Cursor(encoded))
		require.ErrorIs(t, err, rasql.ErrInvalidCursor)
		require.Zero(t, fixture.counter.queries)
	})

	// A float whose bits decode to NaN never comes from a real page. It has to
	// be refused while the cursor is read, because a NaN key would compare
	// false against every row and quietly return nothing.
	t.Run("rejects a malformed float cursor before the query runs", func(t *testing.T) {
		fixture := cursorFixtureFor(t)
		next := fixture.validCursor(t)
		envelope, err := cursorcodec.DecodeEnvelope(string(next))
		require.NoError(t, err)
		data := make([]byte, 8)
		binary.BigEndian.PutUint64(data, math.Float64bits(math.NaN())^(1<<63))
		encoded, err := cursorcodec.EncodeEnvelope(envelope.Fingerprint, envelope.Fields,
			[]cursorcodec.Value{{Present: true, Data: data}})
		require.NoError(t, err)

		_, err = fixture.pageAfter(t, rasql.Cursor(encoded))
		require.ErrorIs(t, err, rasql.ErrInvalidCursor)
		require.Zero(t, fixture.counter.queries)
	})

	t.Run("rejects a cursor built for a different query", func(t *testing.T) {
		fixture := cursorFixtureFor(t)
		next := fixture.validCursor(t)
		envelope, err := cursorcodec.DecodeEnvelope(string(next))
		require.NoError(t, err)
		envelope.Fingerprint[0] ^= 0xff
		encoded, err := cursorcodec.EncodeEnvelope(envelope.Fingerprint, envelope.Fields, envelope.Values)
		require.NoError(t, err)

		_, err = fixture.pageAfter(t, rasql.Cursor(encoded))
		require.ErrorIs(t, err, rasql.ErrInvalidCursor)
	})

	t.Run("rejects text that is not a cursor at all", func(t *testing.T) {
		fixture := cursorFixtureFor(t)
		fixture.counter.queries = 0
		_, err := fixture.pageAfter(t, rasql.Cursor("$"))
		require.ErrorIs(t, err, rasql.ErrInvalidCursor)
		require.Zero(t, fixture.counter.queries)
	})
}

type cursorRow struct{ Value float64 }

type cursorDecoder struct{ schema rasql.ResultSchema }

func (d cursorDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (cursorDecoder) Presence() []rasql.Presence         { return nil }
func (cursorDecoder) DecodeRow(source rasql.ScanSource, row *cursorRow) error {
	return source.Scan(&row.Value)
}

// cursorCountingExecutor counts the statements that reach the database, so a
// test can prove a refused cursor never got that far.
type cursorCountingExecutor struct {
	rasql.Executor
	queries int
}

func (e *cursorCountingExecutor) Query(ctx context.Context, statement stmt.Statement) (rasql.ResultRows, error) {
	e.queries++
	return e.Executor.Query(ctx, statement)
}
