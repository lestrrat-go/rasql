package examples_test

import (
	"context"
	"fmt"

	"github.com/lestrrat-go/rasql/row"
	"github.com/lestrrat-go/rasql/runtime"
)

func queryGeneratedUser(ctx context.Context, client runtime.Client) {
	// Create client once at application startup with
	// runtime.New(db, dialect.PostgreSQL()). Users is the reusable query.TableRef
	// emitted by rasqlgen.
	rows, err := client.SelectFrom(users).
		Select("id", "email").
		WhereEqual("id", 42).
		Query(ctx)
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	if len(rows) == 0 {
		fmt.Println("no user found")
		return
	}
	id, err := row.Int64("id")
	if err != nil {
		fmt.Printf("failed to create id column: %s\n", err)
		return
	}
	email, err := row.String("email")
	if err != nil {
		fmt.Printf("failed to create email column: %s\n", err)
		return
	}
	userID, err := id.Get(rows[0])
	if err != nil {
		fmt.Printf("failed to read id: %s\n", err)
		return
	}
	userEmail, err := email.Get(rows[0])
	if err != nil {
		fmt.Printf("failed to read email: %s\n", err)
		return
	}

	fmt.Printf("%d %s\n", userID, userEmail)

	// OUTPUT:
	// 42 ada@example.com
}
