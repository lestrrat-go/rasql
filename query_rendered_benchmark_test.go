package rasql

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/statement"
)

// BenchmarkQueryRenderedBoundArgs pins the allocation win of QueryRendered
// handing database/sql the statement's own argument slice through
// statement.Statement.BoundArgs instead of a fresh copy through Args. Against
// this benchmark's fake driver, which pays no allocations of its own inside
// QueryContext, every alloc/op this benchmark reports comes from
// QueryRendered's own path. If QueryRendered ever goes back to calling Args
// here, that copy reappears as exactly one extra alloc/op (16 bytes, the
// three-element arg slice's backing array) — the regression this benchmark
// exists to catch.
func BenchmarkQueryRenderedBoundArgs(b *testing.B) {
	database, err := sql.Open(benchmarkDriverName, "")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := database.Close(); err != nil {
			b.Error(err)
		}
	})

	db, err := New(database, dialect.SQLite())
	if err != nil {
		b.Fatal(err)
	}

	stmt := statement.New(benchmarkFullQuery, int64(7), "Ada Lovelace", "ada@example.com")

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		rows, err := db.QueryRendered(b.Context(), stmt)
		if err != nil {
			b.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
