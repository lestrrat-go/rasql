package query_test

import (
	"fmt"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
)

// benchmarkTableDef builds a descriptor with n integer columns named c0..cN-1
// and c0 as the primary key, so BenchmarkTableRefColumn can measure how
// TableRef.Column scales with table width.
func benchmarkTableDef(n int) schema.TableDef {
	columns := make([]schema.ColumnDef, n)
	for i := range columns {
		columns[i] = schema.ColumnDef{Name: fmt.Sprintf("c%d", i), Type: schema.IntegerType{}}
	}
	return schema.TableDef{
		Name:       "bench",
		Columns:    columns,
		PrimaryKey: []string{"c0"},
	}
}

// BenchmarkTableRefColumn measures TableRef.Column's cost at increasing table
// width, for the first column, the last column, and a name the table does not
// have. A lookup that walked the columns would grow with the table on the last
// two, so the three probes together are what shows the cost is the same at any
// width and at any position; measuring only the first column cannot show it,
// because the first column is where a walk stops immediately.
//
// The missing probe is expected to allocate, and its allocations are not a
// finding: they come from the fmt.Errorf that builds the error, which is part
// of what TableRef.Column returns.
func BenchmarkTableRefColumn(b *testing.B) {
	for _, columns := range []int{2, 8, 32, 256, 1024} {
		table := query.MustTableRef(benchmarkTableDef(columns))
		probes := []struct {
			name    string
			column  string
			present bool
		}{
			{name: "first", column: "c0", present: true},
			{name: "last", column: fmt.Sprintf("c%d", columns-1), present: true},
			{name: "missing", column: "absent", present: false},
		}
		for _, probe := range probes {
			b.Run(fmt.Sprintf("columns=%d/%s", columns, probe.name), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					_, err := table.Column(probe.column)
					if (err == nil) != probe.present {
						b.Fatalf("looking up %q returned err %v", probe.column, err)
					}
				}
			})
		}
	}
}

// BenchmarkTableRefFromColumn measures the same three probes against a ref built
// by TableRefFrom, which reads the caller's columns in place instead of its own
// clone. A hit costs the same map lookup there, and a miss falls back to a walk
// of the live columns, because the caller can still rename a column after the
// index is built.
func BenchmarkTableRefFromColumn(b *testing.B) {
	for _, columns := range []int{2, 1024} {
		table := query.TableRefFrom(benchmarkTableDef(columns))
		probes := []struct {
			name    string
			column  string
			present bool
		}{
			{name: "first", column: "c0", present: true},
			{name: "last", column: fmt.Sprintf("c%d", columns-1), present: true},
			{name: "missing", column: "absent", present: false},
		}
		for _, probe := range probes {
			b.Run(fmt.Sprintf("columns=%d/%s", columns, probe.name), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					_, err := table.Column(probe.column)
					if (err == nil) != probe.present {
						b.Fatalf("looking up %q returned err %v", probe.column, err)
					}
				}
			})
		}
	}
}

// BenchmarkSelectBuild measures a full SELECT build over a three-column table,
// end to end from a validated TableRef through rendered SQL.
func BenchmarkSelectBuild(b *testing.B) {
	orders := query.MustTableRef(ordersTable())
	b.ReportAllocs()
	for b.Loop() {
		if _, err := render.SelectFrom(dialect.PostgreSQL(), orders).Select("id", "user_id", "amount").Build(); err != nil {
			b.Fatal(err)
		}
	}
}
