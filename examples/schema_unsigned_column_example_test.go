package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
)

func Example_schema_unsigned_column() {
	// This example declares an unsigned integer column and renders its DDL for
	// each dialect. MySQL is the only supported engine with an unsigned
	// integer type, so it is the only one that renders the table.
	events := schema.MustTable("events",
		// An unsigned column reaches 18446744073709551615, where a signed one
		// stops at 9223372036854775807. rasqlgen generates a uint64 field for
		// it rather than an int64 one.
		schema.Integer("id", schema.Unsigned()),
		schema.Integer("sequence"),
		schema.PrimaryKey("id"),
	)

	mysql, err := render.CreateTable(dialect.MySQL(), events)
	if err != nil {
		fmt.Printf("failed to render MySQL DDL: %s\n", err)
		return
	}
	fmt.Println(mysql.SQL())

	// PostgreSQL has no unsigned integer type, and SQLite stores a signed
	// 64-bit value whatever a column is declared. Both report an error naming
	// the column rather than render a signed BIGINT, which would reject the
	// values above 9223372036854775807 that the descriptor permits.
	for _, d := range []dialect.Dialect{dialect.PostgreSQL(), dialect.SQLite()} {
		if _, err := render.CreateTable(d, events); err != nil {
			fmt.Printf("%s refuses the column: %s\n", d.Name(), err)
		}
	}

	// Output:
	// CREATE TABLE `events` (`id` BIGINT UNSIGNED NOT NULL, `sequence` BIGINT NOT NULL, PRIMARY KEY (`id`))
	// postgresql refuses the column: render postgresql: column "id": dialect postgresql: unsigned integer column "id" cannot be represented: this dialect has no unsigned integer type, and rendering the column signed would narrow the values it permits
	// sqlite refuses the column: render sqlite: column "id": dialect sqlite: unsigned integer column "id" cannot be represented: this dialect has no unsigned integer type, and rendering the column signed would narrow the values it permits
}
