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

// BEGIN(open_tx)

// openTx returns a repository over a transaction that is rolled back when
// the test ends, and the rasql.DB it was built on for the tests that write a
// row the repository has no method for. rasql.Handle is satisfied by *sql.Tx
// as well as *sql.DB, so every write below reaches a real PostgreSQL server
// and none of them survives the test.
func openTx(t *testing.T) (store.Repository, rasql.DB) {
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
	return store.New(db), db
}

// END(open_tx)

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

// BEGIN(add_task_due_on)

// addTaskDueOn files one open task with a due date. AddTask leaves due_on
// alone, so a test that needs one writes the row itself, through the same
// generated table the repository uses and over the same rolled-back
// transaction.
func addTaskDueOn(ctx context.Context, t *testing.T, db rasql.DB, projectID int64, assigneeID int64, title string, dueOn time.Time) {
	t.Helper()
	row := store.TasksRow{ProjectID: projectID, AssigneeID: &assigneeID, Title: title, DueOn: &dueOn}
	if _, err := rasql.InsertWithOptions(ctx, db, store.Tasks(), row,
		rasql.DefaultColumns("is_open", "created_at"),
	); err != nil {
		t.Fatalf("insert task %q: %s", title, err)
	}
}

// END(add_task_due_on)

// openTaskID returns the id of the open task titled title.
func openTaskID(ctx context.Context, t *testing.T, repository store.Repository, title string) int64 {
	t.Helper()
	tasks, err := repository.OpenTasks(ctx)
	if err != nil {
		t.Fatalf("read open tasks: %s", err)
	}
	for _, task := range tasks {
		if task.Title == title {
			return task.TaskID
		}
	}
	t.Fatalf("no open task titled %q", title)
	return 0
}

func TestAddTaskAndCloseTask(t *testing.T) {
	ctx := t.Context()
	repository, _ := openTx(t)
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
	// BEGIN(null_assignee)
	if owned.AssigneeName == nil {
		t.Error("the owned task came back with no assignee name")
	}
	if unowned.AssigneeName != nil {
		t.Errorf("the unowned task came back with assignee %q, want none", *unowned.AssigneeName)
	}
	// END(null_assignee)

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
	repository, _ := openTx(t)
	if err := repository.CloseTask(t.Context(), -1); err != nil {
		t.Fatalf("close a task that does not exist: %s", err)
	}
}

func TestCountOverdue(t *testing.T) {
	ctx := t.Context()
	repository, _ := openTx(t)
	projectID, memberID := seed(ctx, t, repository)

	before, err := repository.CountOverdue(ctx, time.Now())
	if err != nil {
		t.Fatalf("count overdue tasks: %s", err)
	}
	// BEGIN(overdue_unmoved)
	// AddTask files a task with no due date, so the count must not move.
	if err := repository.AddTask(ctx, projectID, &memberID, "No due date"); err != nil {
		t.Fatalf("add a task: %s", err)
	}
	after, err := repository.CountOverdue(ctx, time.Now())
	// END(overdue_unmoved)
	if err != nil {
		t.Fatalf("count overdue tasks: %s", err)
	}
	if after != before {
		t.Errorf("the overdue count moved from %d to %d after adding a task with no due date", before, after)
	}
}

// BEGIN(overdue_boundary)

// TestCountOverdueCountsATaskOnlyAfterItsDueDate pins the boundary that
// CountOverdue's cast draws. due_on is a date and on is an instant, so the two
// meet only once one of them is narrowed, and narrowing on is what keeps a
// task due today out of the count until the day is over. Naming the instant
// rather than reading the clock is what the bound parameter is for.
//
// The instant carries a location as well as a day, and the count follows the
// location. Eight in the evening on 2026-03-16 in a zone nine hours behind UTC
// is already 2026-03-17 in UTC, so the day the caller is having is the one the
// count has to use, and an instant that late in its own day is also the case a
// comparison against the raw timestamp gets wrong.
func TestCountOverdueCountsATaskOnlyAfterItsDueDate(t *testing.T) {
	ctx := t.Context()
	repository, db := openTx(t)
	projectID, memberID := seed(ctx, t, repository)

	zone := time.FixedZone("UTC-9", -9*60*60)
	on := time.Date(2026, 3, 16, 20, 0, 0, 0, zone)
	today := time.Date(2026, 3, 16, 0, 0, 0, 0, zone)
	yesterday := today.AddDate(0, 0, -1)

	before, err := repository.CountOverdue(ctx, on)
	if err != nil {
		t.Fatalf("count overdue tasks: %s", err)
	}

	addTaskDueOn(ctx, t, db, projectID, memberID, "Due today", today)
	afterToday, err := repository.CountOverdue(ctx, on)
	if err != nil {
		t.Fatalf("count overdue tasks: %s", err)
	}
	if afterToday != before {
		t.Errorf("the overdue count moved from %d to %d after adding a task due today; a task is past its due date only once the day is over", before, afterToday)
	}

	addTaskDueOn(ctx, t, db, projectID, memberID, "Due yesterday", yesterday)
	afterYesterday, err := repository.CountOverdue(ctx, on)
	if err != nil {
		t.Fatalf("count overdue tasks: %s", err)
	}
	if afterYesterday != before+1 {
		t.Errorf("the overdue count went from %d to %d after adding a task due yesterday, want %d", before, afterYesterday, before+1)
	}

	addTaskDueOn(ctx, t, db, projectID, memberID, "Closed and late", yesterday)
	if err := repository.CloseTask(ctx, openTaskID(ctx, t, repository, "Closed and late")); err != nil {
		t.Fatalf("close the late task: %s", err)
	}
	afterClosed, err := repository.CountOverdue(ctx, on)
	if err != nil {
		t.Fatalf("count overdue tasks: %s", err)
	}
	if afterClosed != afterYesterday {
		t.Errorf("the overdue count moved from %d to %d after a late task was closed; a closed task is nobody's problem", afterYesterday, afterClosed)
	}
}

// END(overdue_boundary)
