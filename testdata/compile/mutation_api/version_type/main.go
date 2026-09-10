package main

import (
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
)

type row struct{}

func main() {
	table, _ := rasql.TableOf[row](schema.TableDef{Name: "items", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}}, {Name: "version", Type: schema.TextType{}},
	}, PrimaryKey: []string{"id"}})
	id := query.TypedColumnOf[row, int64](table.Column("id"))
	version := query.TypedColumnOf[row, string](table.Column("version"))
	plan, _ := rasql.NewPatchPlan(table, query.EqualValue(id, int64(1)), rasql.SetField(id, int64(2)))
	_, _ = plan.WithVersion(version, int64(1))
}
