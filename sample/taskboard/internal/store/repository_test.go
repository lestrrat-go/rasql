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

func openTx(t *testing.T) (store.Repository, rasql.DB, rasql.Executor) {
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
	profile, err := rasql.DiscoverEngineProfile(t.Context(), db, "postgresql-17")
	if err != nil {
		t.Fatalf("discover PostgreSQL engine profile: %s", err)
	}
	executor, err := rasql.AsExecutor(db, profile)
	if err != nil {
		t.Fatalf("create the rasql executor: %s", err)
	}
	return store.New(executor), db, executor
}

func seed(ctx context.Context, t *testing.T, db rasql.DB) (int64, int64) {
	t.Helper()
	memberPlan, err := store.NewMembersCreate().Name("D3 test member").Plan()
	if err != nil {
		t.Fatalf("plan member: %s", err)
	}
	member, err := rasql.QueryCreate(ctx, db, memberPlan)
	if err != nil {
		t.Fatalf("create member: %s", err)
	}
	projectPlan, err := store.NewProjectsCreate().Name("D3 test project").Plan()
	if err != nil {
		t.Fatalf("plan project: %s", err)
	}
	project, err := rasql.QueryCreate(ctx, db, projectPlan)
	if err != nil {
		t.Fatalf("create project: %s", err)
	}
	return project.ID, member.ID
}

func addTaskDueOn(ctx context.Context, t *testing.T, executor rasql.Executor, projectID, assigneeID int64, title string, dueOn time.Time) {
	t.Helper()
	create := store.NewTasksCreate().ProjectID(projectID).
		AssigneeID(assigneeID).
		Title(title).
		DueOn(dueOn).
		DefaultIsOpen().DefaultCreatedAt()
	plan, err := create.Plan()
	if err != nil {
		t.Fatalf("plan task %q: %s", title, err)
	}
	if _, err := rasql.ExecMutation(ctx, executor, plan); err != nil {
		t.Fatalf("insert task %q: %s", title, err)
	}
}

func openProjects(ctx context.Context, t *testing.T, repository store.Repository) store.OpenProjectsPage {
	t.Helper()
	page, err := repository.OpenProjects(ctx, rasql.PageRequest{Limit: 50})
	if err != nil {
		t.Fatalf("read open projects: %s", err)
	}
	return page
}

func openTaskID(ctx context.Context, t *testing.T, repository store.Repository, title string) int64 {
	t.Helper()
	page := openProjects(ctx, t, repository)
	for _, project := range page.Values {
		for _, task := range project.Tasks.Values {
			if task.Row.Title == title {
				return task.Row.ID
			}
		}
	}
	t.Fatalf("no open task titled %q", title)
	return 0
}

func countOpenTasks(page store.OpenProjectsPage) int {
	count := 0
	for _, project := range page.Values {
		count += len(project.Tasks.Values)
	}
	return count
}

func TestAddTaskAndCloseTask(t *testing.T) {
	ctx := t.Context()
	repository, db, executor := openTx(t)
	projectID, memberID := seed(ctx, t, db)
	before := openProjects(ctx, t, repository)
	if err := repository.AddTask(ctx, projectID, &memberID, "Owned task"); err != nil {
		t.Fatalf("add an owned task: %s", err)
	}
	if err := repository.AddTask(ctx, projectID, nil, "Unowned task"); err != nil {
		t.Fatalf("add an unowned task: %s", err)
	}
	after := openProjects(ctx, t, repository)
	if got, want := countOpenTasks(after), countOpenTasks(before)+2; got != want {
		t.Fatalf("read %d open tasks after adding two to %d", got, want-2)
	}
	var owned, unowned *store.OpenTask
	for _, project := range after.Values {
		for index := range project.Tasks.Values {
			task := &project.Tasks.Values[index]
			switch task.Row.Title {
			case "Owned task":
				owned = task
			case "Unowned task":
				unowned = task
			}
		}
	}
	if owned == nil || unowned == nil {
		t.Fatal("one of the two new tasks is missing from the open list")
	}
	if !owned.Assignee.Loaded || !owned.Assignee.Present || owned.Assignee.Value == nil {
		t.Fatal("the owned task came back with no assignee")
	}
	if !unowned.Assignee.Loaded || unowned.Assignee.Present {
		t.Fatal("the unowned task came back with an assignee")
	}
	if !owned.Row.IsOpen || owned.Row.CreatedAt.IsZero() || !unowned.Row.IsOpen || unowned.Row.CreatedAt.IsZero() {
		t.Fatalf("new task defaults were not stored: owned=%#v unowned=%#v", owned.Row, unowned.Row)
	}
	closedCreate := store.NewTasksCreate().ProjectID(projectID).Title("Explicitly closed").IsOpen(false).DefaultCreatedAt()
	closedPlan, err := closedCreate.Plan()
	if err != nil {
		t.Fatalf("plan explicitly closed task: %s", err)
	}
	if _, err := rasql.ExecMutation(ctx, executor, closedPlan); err != nil {
		t.Fatalf("insert explicitly closed task: %s", err)
	}
	closedRow := readTaskByTitle(ctx, t, executor, "Explicitly closed")
	if closedRow.IsOpen || closedRow.CreatedAt.IsZero() || closedRow.AssigneeID.Valid {
		t.Fatalf("explicit false or nullable omission was not stored: %#v", closedRow)
	}
	if err := repository.CloseTask(ctx, unowned.Row.ID); err != nil {
		t.Fatalf("close the unowned task: %s", err)
	}
	if err := repository.CloseTask(ctx, unowned.Row.ID); err != nil {
		t.Fatalf("close the already closed task: %s", err)
	}
	closed := openProjects(ctx, t, repository)
	for _, project := range closed.Values {
		for _, task := range project.Tasks.Values {
			if task.Row.ID == unowned.Row.ID {
				t.Fatalf("task %d is still open after CloseTask", task.Row.ID)
			}
		}
	}
}

func readTaskByTitle(ctx context.Context, t *testing.T, executor rasql.Executor, title string) store.TasksRow {
	t.Helper()
	source, err := store.Tasks().Source("tasks")
	if err != nil {
		t.Fatalf("create task source: %s", err)
	}
	expressions, err := (store.TasksColumns{}).Bind(source)
	if err != nil {
		t.Fatalf("bind task columns: %s", err)
	}
	projection, err := store.TasksProjection(expressions)
	if err != nil {
		t.Fatalf("build task projection: %s", err)
	}
	row, err := rasql.One(ctx, executor, rasql.Select(source.Source(), projection).Where(rasql.EqualValue(expressions.Title.Expr(), title)))
	if err != nil {
		t.Fatalf("read task %q: %s", title, err)
	}
	return row
}

func TestCloseTaskOnAMissingTaskIsNotAnError(t *testing.T) {
	repository, _, _ := openTx(t)
	if err := repository.CloseTask(t.Context(), -1); err != nil {
		t.Fatalf("close a task that does not exist: %s", err)
	}
}

func TestCountOverdueCountsATaskOnlyAfterItsDueDate(t *testing.T) {
	ctx := t.Context()
	repository, db, executor := openTx(t)
	projectID, memberID := seed(ctx, t, db)
	zone := time.FixedZone("UTC-9", -9*60*60)
	on := time.Date(2026, 3, 16, 20, 0, 0, 0, zone)
	today := time.Date(2026, 3, 16, 0, 0, 0, 0, zone)
	yesterday := today.AddDate(0, 0, -1)
	before, err := repository.CountOverdue(ctx, on)
	if err != nil {
		t.Fatalf("count overdue tasks: %s", err)
	}
	addTaskDueOn(ctx, t, executor, projectID, memberID, "Due today", today)
	afterToday, err := repository.CountOverdue(ctx, on)
	if err != nil {
		t.Fatalf("count overdue tasks: %s", err)
	}
	if afterToday != before {
		t.Errorf("the overdue count moved from %d to %d for a task due today", before, afterToday)
	}
	addTaskDueOn(ctx, t, executor, projectID, memberID, "Due yesterday", yesterday)
	afterYesterday, err := repository.CountOverdue(ctx, on)
	if err != nil {
		t.Fatalf("count overdue tasks: %s", err)
	}
	if afterYesterday != before+1 {
		t.Errorf("the overdue count went from %d to %d, want %d", before, afterYesterday, before+1)
	}
	addTaskDueOn(ctx, t, executor, projectID, memberID, "Closed and late", yesterday)
	if err := repository.CloseTask(ctx, openTaskID(ctx, t, repository, "Closed and late")); err != nil {
		t.Fatalf("close the late task: %s", err)
	}
	afterClosed, err := repository.CountOverdue(ctx, on)
	if err != nil {
		t.Fatalf("count overdue tasks: %s", err)
	}
	if afterClosed != afterYesterday {
		t.Errorf("the overdue count moved from %d to %d after closing a late task", afterYesterday, afterClosed)
	}
}
