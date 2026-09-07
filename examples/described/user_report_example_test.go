package described_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/described"
	_ "modernc.org/sqlite"
)

func ExampleUserReport() {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("open database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	_, err = database.Exec(`CREATE TABLE users(id INTEGER); CREATE TABLE profiles(user_id INTEGER, nickname TEXT); INSERT INTO users VALUES (1),(2); INSERT INTO profiles VALUES (1,'one')`)
	if err != nil {
		fmt.Printf("create fixture: %s\n", err)
		return
	}
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("configure database: %s\n", err)
		return
	}
	rows, err := described.QueryUserReport(context.Background(), db)
	if err != nil {
		fmt.Printf("query report: %s\n", err)
		return
	}
	for row, rowErr := range rows {
		if rowErr != nil {
			fmt.Printf("scan report: %s\n", rowErr)
			return
		}
		nickname := "<nil>"
		if row.Nickname != nil {
			nickname = *row.Nickname
		}
		fmt.Printf("%d %s %d\n", *row.UserID, nickname, row.ProfileCount)
	}
	// Output:
	// 1 one 1
	// 2 <nil> 0
}
