package main

import (
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
)

func main() {
	table, _ := rasql.TableOf[struct{}](schema.TableDef{Name: "items", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	id := query.TypedColumnOf[struct{}, int64](table.Column("id"))
	_, _ = rasql.NewCreatePlan(table, rasql.SetField(id, "wrong"))
}
