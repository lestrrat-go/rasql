package main

import (
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/schema"
)

type user struct{ ID int64 }
type decoder struct{ schema rasql.ResultSchema }

func (d decoder) ResultSchema() rasql.ResultSchema                    { return d.schema }
func (decoder) Presence() []rasql.Presence                            { return nil }
func (decoder) DecodeRow(source rasql.ScanSource, result *user) error { return source.Scan(&result.ID) }

func main() {
	schemaValue, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	if err != nil {
		panic(err)
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", rasql.Value(int64(1)), schema.IntegerType{}, ""),
	}, decoder{schema: schemaValue})
	if err != nil {
		panic(err)
	}
	table, err := rasql.ReadTableOf[user](schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	if err != nil {
		panic(err)
	}
	relation, err := rasql.SourceOf(table, "u")
	if err != nil {
		panic(err)
	}
	query := rasql.Select(relation.Source(), projection).Where(rasql.EqualValue(rasql.Value(int64(1)), int64(1)))
	if err := query.Validate(); err != nil {
		panic(err)
	}
}
