package main

import (
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
)

type row struct{}

func main() {
	table, err := rasql.TableOf[row](schema.TableDef{Name: "items", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	if err != nil {
		panic(err)
	}
	id := query.TypedColumnOf[row, int64](table.Column("id"))
	_, err = rasql.NewCreatePlan(table, rasql.SetField(id, int64(1)))
	if err != nil {
		panic(err)
	}
}
