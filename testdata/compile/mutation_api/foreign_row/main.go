package main
import ( "github.com/lestrrat-go/rasql"; "github.com/lestrrat-go/rasql/query"; "github.com/lestrrat-go/rasql/schema" )
func main() { a, _ := rasql.TableOf[struct{}](schema.TableDef{Name:"a", Columns:[]schema.ColumnDef{{Name:"name",Type:schema.TextType{}}}}); b, _ := rasql.TableOf[struct{ X int }](schema.TableDef{Name:"b", Columns:[]schema.ColumnDef{{Name:"name",Type:schema.TextType{}}}}); other := query.TypedColumnOf[struct{ X int }, string](b.Column("name")); _, _ = rasql.NewCreatePlan(a, rasql.SetField(other, "x")) }
