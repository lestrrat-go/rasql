package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite"
)

// userIDDecoder decodes the single id column a trivial native query below
// projects, which is enough to exercise a query's execution and consumption
// phases without depending on the shape read.
type userIDDecoder struct{ result rasql.ResultSchema }

func (d userIDDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d userIDDecoder) Presence() []rasql.Presence       { return nil }
func (d userIDDecoder) DecodeRow(src rasql.ScanSource, row *int64) error {
	return src.Scan(row)
}

// Example_rasql_lifecycle_observer reports execution and row consumption as
// separate events, so a successful query does not imply successful scanning.
func Example_rasql_lifecycle_observer() {
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Println("open error")
		return
	}
	defer func() { _ = database.Close() }()
	database.SetMaxOpenConns(1)

	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Println("new error")
		return
	}
	users := store.Users()
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Println("table error")
		return
	}
	events := make([]string, 0, 2)
	db, err = db.WithInvocationObservers(rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.InvocationObserverFunc(func(ctx context.Context, operation rasql.Operation) (context.Context, rasql.CompletionObserver) {
		return ctx, rasql.CompletionObserverFunc(func(_ context.Context, completion rasql.Completion) error {
			switch completion.Phase {
			case rasql.ExecutionPhase:
				events = append(events, "execution")
			case rasql.ConsumptionPhase:
				events = append(events, fmt.Sprintf("consumption rows=%d", completion.RowsRead))
			}
			return nil
		})
	}))
	if err != nil {
		fmt.Println("observer error")
		return
	}
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 40, 0)
	if err != nil {
		fmt.Println("profile error")
		return
	}
	executor, err := rasql.AsExecutor(db, profile)
	if err != nil {
		fmt.Println("executor error")
		return
	}

	result, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	if err != nil {
		fmt.Println("schema error")
		return
	}
	projection, err := rasql.NativeProjection[int64](userIDDecoder{result: result})
	if err != nil {
		fmt.Println("projection error")
		return
	}
	q, err := rasql.Native(rasql.NativeStatement{Engine: "sqlite", SQL: "SELECT id FROM users"}, projection, rasql.Many)
	if err != nil {
		fmt.Println("query error")
		return
	}

	rows, err := rasql.Rows(ctx, executor, q)
	if err != nil {
		fmt.Println("query error")
		return
	}
	for _, err := range rows {
		if err != nil {
			fmt.Println("scan error")
			return
		}
	}
	fmt.Println(events)

	// Output:
	// [execution consumption rows=0]
}
