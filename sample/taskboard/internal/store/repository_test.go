package store_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"example.com/taskboard/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
)

// openTx returns a repository over a transaction that is rolled back when
// the test ends. rasql.Handle is satisfied by *sql.Tx as well as *sql.DB, so
// every write below reaches a real PostgreSQL server and none of them
// survives the test.
func openTx(t *testing.T) store.Repository {
	t.Helper()
	dsn := os.Getenv("TASKBOARD_TEST_DSN")
	if dsn == "" {
		t.Skip("set TASKBOARD_TEST_DSN to a migrated PostgreSQL database to run this test")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse TASKBOARD_TEST_DSN: %s", err)
	}
	database := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = database.Close() })

	tx, err := database.BeginTx(t.Context(), &sql.TxOptions{})
	if err != nil {
		t.Fatalf("begin: %s", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })

	db, err := rasql.New(tx, dialect.PostgreSQL())
	if err != nil {
		t.Fatalf("create the rasql db: %s", err)
	}
	return store.New(db)
}

// seed writes one member, one project, and returns their ids.
func seed(ctx context.Context, t *testing.T, repository store.Repository) (projectID int64, memberID int64) {
	t.Helper()
	projects, err := repository.AllProjects(ctx)
	if err != nil {
		t.Fatalf("read projects: %s", err)
	}
	members, err := repository.AllMembers(ctx)
	if err != nil {
		t.Fatalf("read members: %s", err)
	}
	if len(projects) == 0 || len(members) == 0 {
		t.Skip("the test database holds no project or member to file a task against")
	}
	return projects[0].ID, members[0].ID
}

func TestAddTaskAndCloseTask(t *testing.T) {
	ctx := t.Context()
	repository := openTx(t)
	projectID, memberID := seed(ctx, t, repository)

	before, err := repository.OpenTasks(ctx)
	if err != nil {
		t.Fatalf("read open tasks: %s", err)
	}
	if err := repository.AddTask(ctx, projectID, &memberID, "Owned task"); err != nil {
		t.Fatalf("add an owned task: %s", err)
	}
	if err := repository.AddTask(ctx, projectID, nil, "Unowned task"); err != nil {
		t.Fatalf("add an unowned task: %s", err)
	}

	after, err := repository.OpenTasks(ctx)
	if err != nil {
		t.Fatalf("read open tasks: %s", err)
	}
	if len(after) != len(before)+2 {
		t.Fatalf("read %d open tasks after adding two to %d", len(after), len(before))
	}

	var owned, unowned *store.OpenTask
	for index := range after {
		switch after[index].Title {
		case "Owned task":
			owned = &after[index]
		case "Unowned task":
			unowned = &after[index]
		}
	}
	if owned == nil || unowned == nil {
		t.Fatal("one of the two new tasks is missing from the open list")
	}
	if owned.AssigneeName == nil {
		t.Error("the owned task came back with no assignee name")
	}
	if unowned.AssigneeName != nil {
		t.Errorf("the unowned task came back with assignee %q, want none", *unowned.AssigneeName)
	}

	if err := repository.CloseTask(ctx, unowned.TaskID); err != nil {
		t.Fatalf("close the unowned task: %s", err)
	}
	closed, err := repository.OpenTasks(ctx)
	if err != nil {
		t.Fatalf("read open tasks: %s", err)
	}
	for _, row := range closed {
		if row.TaskID == unowned.TaskID {
			t.Fatalf("task %d is still open after CloseTask", row.TaskID)
		}
	}
}

func TestCloseTaskOnAMissingTaskIsNotAnError(t *testing.T) {
	repository := openTx(t)
	if err := repository.CloseTask(t.Context(), -1); err != nil {
		t.Fatalf("close a task that does not exist: %s", err)
	}
}

func TestCountOverdue(t *testing.T) {
	ctx := t.Context()
	repository := openTx(t)
	projectID, memberID := seed(ctx, t, repository)

	before, err := repository.CountOverdue(ctx, time.Now())
	if err != nil {
		t.Fatalf("count overdue tasks: %s", err)
	}
	// AddTask files a task with no due date, so the count must not move.
	if err := repository.AddTask(ctx, projectID, &memberID, "No due date"); err != nil {
		t.Fatalf("add a task: %s", err)
	}
	after, err := repository.CountOverdue(ctx, time.Now())
	if err != nil {
		t.Fatalf("count overdue tasks: %s", err)
	}
	if after != before {
		t.Errorf("the overdue count moved from %d to %d after adding a task with no due date", before, after)
	}
}
