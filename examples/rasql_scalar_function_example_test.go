package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_scalar_function() {
	// This example looks a member up by email regardless of case with LOWER,
	// then reads every member's display name, falling back to their email
	// with COALESCE when no nickname is set.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A DB couples a database handle with the dialect used to render SQL.
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	// A typed descriptor makes members usable with rasql.Insert. Nickname is
	// nullable, so COALESCE below has a real NULL to fall back from.
	type memberRow struct {
		ID       int     `rasql:"id"`
		Email    string  `rasql:"email"`
		Nickname *string `rasql:"nickname"`
	}
	// A local result type holds the decoded id and display name.
	type memberName struct {
		ID   int64
		Name string
	}
	members := rasql.MustTableOf[memberRow](schema.MustTableDef("members",
		schema.Integer("id"),
		schema.Text("email"),
		schema.Text("nickname", schema.Nullable()),
		schema.PrimaryKey("id"),
	))
	if err := rasql.CreateTable(ctx, db, members); err != nil {
		fmt.Printf("failed to create members table: %s\n", err)
		return
	}

	// members has no generated column fields, so its columns are looked up by
	// name. That lookup validates them against the descriptor as the query is
	// assembled.
	id, err := members.Column("id")
	if err != nil {
		fmt.Printf("failed to find members.id: %s\n", err)
		return
	}
	email, err := members.Column("email")
	if err != nil {
		fmt.Printf("failed to find members.email: %s\n", err)
		return
	}
	nickname, err := members.Column("nickname")
	if err != nil {
		fmt.Printf("failed to find members.nickname: %s\n", err)
		return
	}

	nick := "Ada"
	for _, member := range []memberRow{
		{ID: 1, Email: "Ada@Example.com", Nickname: &nick},
		{ID: 2, Email: "bob@example.com", Nickname: nil},
	} {
		if _, err := rasql.Insert(ctx, db, members, member); err != nil {
			fmt.Printf("failed to insert member: %s\n", err)
			return
		}
	}

	// LOWER(email) matches "Ada@Example.com" against the lower-case literal a
	// caller would type, regardless of how the stored value was cased.
	// SQL: SELECT members.id, COALESCE(members.nickname, members.email) AS name FROM members WHERE LOWER(members.email) = ? (argument: "ada@example.com")
	byEmail, err := rasql.DecodeFrom[memberName](members).
		Project(query.Project(id), query.Project(query.Coalesce(nickname, email)).As("name")).
		Where(query.Equal(query.Lower(email), query.Bind("ada@example.com"))).
		Query(ctx, db)
	if err != nil {
		fmt.Printf("failed to query member by email: %s\n", err)
		return
	}
	for member, err := range byEmail {
		if err != nil {
			fmt.Printf("failed to query member by email: %s\n", err)
			return
		}
		fmt.Println(member.Name)
	}

	// COALESCE(nickname, email) reads every member's display name, falling
	// back to the email once nickname is NULL.
	// SQL: SELECT members.id, COALESCE(members.nickname, members.email) AS name FROM members ORDER BY members.id ASC
	names, err := rasql.DecodeFrom[memberName](members).
		Project(query.Project(id), query.Project(query.Coalesce(nickname, email)).As("name")).
		OrderAsc(id).
		Query(ctx, db)
	if err != nil {
		fmt.Printf("failed to query member names: %s\n", err)
		return
	}
	for member, err := range names {
		if err != nil {
			fmt.Printf("failed to query member names: %s\n", err)
			return
		}
		fmt.Println(member.ID, member.Name)
	}

	// Output:
	// Ada
	// 1 Ada
	// 2 bob@example.com
}
