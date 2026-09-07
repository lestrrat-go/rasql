package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
)

// BEGIN(row_lock)

func Example_query_rowLock() {
	queue := query.MustTableRef(schema.MustTableDef("queue", schema.Integer("id"), schema.Integer("claimed")))
	statement, err := query.NewSelect(queue, queue.Column("id"))
	if err != nil {
		fmt.Println(err)
		return
	}
	statement, err = statement.WithWhere(query.Equal(queue.Column("claimed"), 0))
	if err != nil {
		fmt.Println(err)
		return
	}
	statement, err = statement.WithOrder(query.Asc(queue.Column("id")))
	if err != nil {
		fmt.Println(err)
		return
	}
	statement, err = statement.WithLimit(1)
	if err != nil {
		fmt.Println(err)
		return
	}
	statement, err = statement.WithLock(query.RowLock(query.LockUpdate).Wait(query.LockWaitSkipLocked))
	if err != nil {
		fmt.Println(err)
		return
	}
	rendered, err := render.Select(dialect.PostgreSQL(), statement)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(rendered.SQL())
	fmt.Println(rendered.Args()...)
	// Output:
	// SELECT "queue"."id" FROM "queue" WHERE ("queue"."claimed" = $1) ORDER BY "queue"."id" LIMIT $2 FOR UPDATE SKIP LOCKED
	// 0 1
}

// END(row_lock)

// BEGIN(conditional_upsert)

func Example_query_conditionalUpsert() {
	items := query.MustTableRef(schema.MustTableDef("items", schema.Integer("id"), schema.Integer("version"), schema.Text("payload")))
	id, version, payload := items.Column("id"), items.Column("version"), items.Column("payload")
	insert, err := query.NewInsert(items, query.Set(id, 1), query.Set(version, 3), query.Set(payload, "new"))
	if err != nil {
		fmt.Println(err)
		return
	}
	statement, err := query.NewUpsert(insert, []query.ColumnRef{id}, []query.Assignment{
		query.Set(version, query.Excluded(version)), query.Set(payload, query.Excluded(payload)),
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	statement, err = statement.WithUpdateWhere(query.LessThan(version, query.Excluded(version)))
	if err != nil {
		fmt.Println(err)
		return
	}
	rendered, err := render.Upsert(dialect.SQLite(), statement)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(rendered.SQL())
	fmt.Println(rendered.Args()...)
	// Output:
	// INSERT INTO "items" ("id", "version", "payload") VALUES (?, ?, ?) ON CONFLICT ("id") DO UPDATE SET "version" = EXCLUDED."version", "payload" = EXCLUDED."payload" WHERE ("items"."version" < EXCLUDED."version")
	// 1 3 new
}

// END(conditional_upsert)
