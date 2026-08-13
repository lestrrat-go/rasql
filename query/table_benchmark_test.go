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
// width. It looks up a fixed column name so the benchmark measures the nil
// check plus the lookup itself, not the position of the hit.
func BenchmarkTableRefColumn(b *testing.B) {
	for _, columns := range []int{2, 8, 32} {
		b.Run(fmt.Sprintf("columns=%d", columns), func(b *testing.B) {
			table := query.MustTableRef(benchmarkTableDef(columns))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := table.Column("c0"); err != nil {
					b.Fatal(err)
				}
			}
		})
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
