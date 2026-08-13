// Package schemasource is a schema package: the one file a user maintains
// when they keep their schema as Go and generate from it with
// `rasqlgen schema -source`. It is input only; the generated code never
// imports it.
package schemasource

// BEGIN(schema_source_tables)
import "github.com/lestrrat-go/rasql/schema"

func Tables() []schema.TableDef {
	return []schema.TableDef{
		schema.MustTableDef("users",
			schema.Integer("id"),
			schema.Text("email", schema.Width(255)),
			schema.PrimaryKey("id"),
		),
	}
}

// END(schema_source_tables)
