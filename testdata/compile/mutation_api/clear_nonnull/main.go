package main
import ( "github.com/lestrrat-go/rasql"; "github.com/lestrrat-go/rasql/query"; "github.com/lestrrat-go/rasql/schema" )
func main() { table, _ := rasql.TableOf[struct{}](schema.TableDef{Name:"items", Columns:[]schema.ColumnDef{{Name:"name",Type:schema.TextType{}}}}); name := query.TypedColumnOf[struct{}, string](table.Column("name")); _, _ = rasql.NewCreatePlan(table, rasql.ClearField(name)) }
