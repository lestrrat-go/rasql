package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
)

// BEGIN(render_write)

func Example_query_render_write() {
	// A table description carries everything the DDL and the write
	// statements need. No row type and no database handle appear here.
	definition := schema.MustTableDef("accounts",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
	)
	accounts := query.MustTableRef(definition)
	id := accounts.Column("id")
	email := accounts.Column("email")

	// render.CreateTable turns the description into DDL for one dialect.
	ddl, err := render.CreateTable(dialect.PostgreSQL(), definition)
	if err != nil {
		fmt.Printf("failed to render the DDL: %s\n", err)
		return
	}
	fmt.Println(ddl.SQL())

	// query.NewInsert builds the statement; render.Insert turns it into SQL
	// text and the arguments that go with it. A plain Go value is bound
	// automatically, so the row values need no Bind wrapper.
	insert, err := query.NewInsert(accounts,
		[]query.ColumnRef{id, email},
		1, "ada@example.com",
	)
	if err != nil {
		fmt.Printf("failed to build the insert: %s\n", err)
		return
	}
	rendered, err := render.Insert(dialect.PostgreSQL(), insert)
	if err != nil {
		fmt.Printf("failed to render the insert: %s\n", err)
		return
	}
	fmt.Println(rendered.SQL())
	fmt.Println(rendered.Args()...)

	// An update is the same two steps. WithWhere keeps it off every row.
	update, err := query.NewUpdate(accounts, query.Set(email, "grace@example.com"))
	if err != nil {
		fmt.Printf("failed to build the update: %s\n", err)
		return
	}
	update, err = update.WithWhere(query.Equal(id, 1))
	if err != nil {
		fmt.Printf("failed to add the predicate: %s\n", err)
		return
	}
	rendered, err = render.Update(dialect.PostgreSQL(), update)
	if err != nil {
		fmt.Printf("failed to render the update: %s\n", err)
		return
	}
	fmt.Println(rendered.SQL())
	fmt.Println(rendered.Args()...)

	// Output:
	// CREATE TABLE "accounts" ("id" BIGINT NOT NULL, "email" TEXT NOT NULL, PRIMARY KEY ("id"))
	// INSERT INTO "accounts" ("id", "email") VALUES ($1, $2)
	// 1 ada@example.com
	// UPDATE "accounts" SET "email" = $1 WHERE ("accounts"."id" = $2)
	// grace@example.com 1
}

// END(render_write)
