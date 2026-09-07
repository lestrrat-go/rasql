package store_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sync"
	"testing"

	"example.com/taskboard/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
)

type statementObserver struct {
	mu          sync.Mutex
	rows        []int64
	logicalErrs []error
	starts      []int
	terminals   []rasql.Event
	invocations []invocationEvidence
}

type invocationEvidence struct {
	sql        string
	args       []any
	kind       rasql.OperationKind
	phase      rasql.Phase
	rows       int64
	earlyClose bool
	err        error
}

func (observer *statementObserver) start(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) {
	if event.Phase != rasql.EventStart {
		return ctx, nil
	}
	if event.Kind == rasql.EventGraph {
		return ctx, rasql.EventCompletionFunc(func(_ context.Context, terminal rasql.Event) error {
			observer.mu.Lock()
			defer observer.mu.Unlock()
			observer.logicalErrs = append(observer.logicalErrs, terminal.Err)
			return nil
		})
	}
	if event.Kind != rasql.EventStatement {
		return ctx, nil
	}
	observer.mu.Lock()
	observer.starts = append(observer.starts, event.StatementIndex)
	observer.mu.Unlock()
	return ctx, rasql.EventCompletionFunc(func(_ context.Context, terminal rasql.Event) error {
		if terminal.Phase != rasql.EventTerminal {
			return nil
		}
		observer.mu.Lock()
		defer observer.mu.Unlock()
		observer.rows = append(observer.rows, terminal.Rows)
		observer.terminals = append(observer.terminals, terminal)
		return nil
	})
}

func (observer *statementObserver) snapshot() []int64 {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return append([]int64(nil), observer.rows...)
}

func (observer *statementObserver) logicalErrors() []error {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return append([]error(nil), observer.logicalErrs...)
}

func (observer *statementObserver) snapshotStarts() []int {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return append([]int(nil), observer.starts...)
}

func (observer *statementObserver) snapshotTerminals() []rasql.Event {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return append([]rasql.Event(nil), observer.terminals...)
}

func (observer *statementObserver) snapshotInvocations() []invocationEvidence {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return append([]invocationEvidence(nil), observer.invocations...)
}

func openFixture(t *testing.T, cancelStatement int) (store.Repository, rasql.Executor, *statementObserver) {
	t.Helper()
	dsn := os.Getenv("TASKBOARD_TEST_DSN")
	if dsn == "" {
		t.Skip("set TASKBOARD_TEST_DSN to a fresh migrated PostgreSQL database to run the graph fixture")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse TASKBOARD_TEST_DSN: %s", err)
	}
	database := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = database.Close() })
	tx, err := database.BeginTx(t.Context(), &sql.TxOptions{})
	if err != nil {
		t.Fatalf("begin fixture transaction: %s", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })

	var projects int
	if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM projects").Scan(&projects); err != nil {
		t.Fatalf("count existing projects: %s", err)
	}
	if projects != 0 {
		t.Fatalf("graph fixture requires an empty disposable database, found %d projects", projects)
	}
	seedFixture(t, tx)

	db, err := rasql.New(tx, dialect.PostgreSQL())
	if err != nil {
		t.Fatalf("create the rasql db: %s", err)
	}
	profile, err := rasql.DiscoverEngineProfile(t.Context(), db, "postgresql-17")
	if err != nil {
		t.Fatalf("discover PostgreSQL engine profile: %s", err)
	}
	observer := new(statementObserver)
	db, err = db.WithInvocationObservers(
		rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}),
		rasql.InvocationObserverFunc(func(ctx context.Context, _ rasql.Operation) (context.Context, rasql.CompletionObserver) {
			return ctx, rasql.CompletionObserverFunc(func(_ context.Context, completion rasql.Completion) error {
				observer.mu.Lock()
				observer.invocations = append(observer.invocations, invocationEvidence{
					sql: completion.Operation.SQL(), args: completion.Operation.Args(), kind: completion.Operation.Kind(),
					phase: completion.Phase, rows: completion.RowsRead, earlyClose: completion.EarlyClose, err: completion.Err,
				})
				observer.mu.Unlock()
				return nil
			})
		}),
	)
	if err != nil {
		t.Fatalf("observe fixture database: %s", err)
	}
	executor, err := rasql.AsExecutor(db, profile)
	if err != nil {
		t.Fatalf("create the rasql executor: %s", err)
	}
	didCancel := false
	observed, err := rasql.WithEventObservers(executor,
		rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}),
		rasql.EventObserverFunc(func(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) {
			if !didCancel && event.Kind == rasql.EventStatement && event.Phase == rasql.EventStart && event.StatementIndex == cancelStatement {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
				didCancel = true
			}
			return observer.start(ctx, event)
		}),
	)
	if err != nil {
		t.Fatalf("observe fixture executor: %s", err)
	}
	return store.New(observed), observed, observer
}

func seedFixture(t *testing.T, tx *sql.Tx) {
	t.Helper()
	ctx := t.Context()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO members (id, name) OVERRIDING SYSTEM VALUE
		VALUES (1, 'D3 Ada'), (2, 'D3 Grace')
	`); err != nil {
		t.Fatalf("seed fixture members: %s", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO projects (id, name) OVERRIDING SYSTEM VALUE
		SELECT project_no, format('D3 fixture project %s', project_no)
		FROM generate_series(1, 50) AS project_no
	`); err != nil {
		t.Fatalf("seed fixture projects: %s", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO tasks (id, project_id, assignee_id, title, is_open) OVERRIDING SYSTEM VALUE
		SELECT ((project_no - 1) * 500) + task_no,
			project_no,
			CASE WHEN task_no = 5 THEN NULL WHEN task_no % 2 = 0 THEN 2 ELSE 1 END,
			format('D3 fixture task %s/%s', project_no, task_no),
			project_no < 50 AND task_no <= 6
		FROM generate_series(1, 50) AS project_no
		CROSS JOIN generate_series(1, 500) AS task_no
	`); err != nil {
		t.Fatalf("seed fixture tasks: %s", err)
	}
}

func TestOpenProjectsBoundedGraphFixture(t *testing.T) {
	repository, _, observer := openFixture(t, -1)
	ctx := t.Context()
	seen := make(map[int64]struct{}, 50)
	var after rasql.Cursor
	pages := 0
	for {
		page, err := repository.OpenProjects(ctx, rasql.PageRequest{After: after, Limit: 10})
		if err != nil {
			t.Fatalf("read fixture page: %s", err)
		}
		pages++
		for projectIndex, project := range page.Values {
			wantProjectID := int64((pages-1)*10 + projectIndex + 1)
			if project.Row.ID != wantProjectID {
				t.Fatalf("page %d project %d has ID %d, want %d", pages, projectIndex, project.Row.ID, wantProjectID)
			}
			if _, exists := seen[project.Row.ID]; exists {
				t.Fatalf("project %d appeared twice", project.Row.ID)
			}
			seen[project.Row.ID] = struct{}{}
			if !project.Tasks.Loaded || project.Tasks.Values == nil {
				t.Fatalf("project %d did not receive a loaded task collection", project.Row.ID)
			}
			if len(project.Tasks.Values) > 5 {
				t.Fatalf("project %d returned %d tasks, want at most five", project.Row.ID, len(project.Tasks.Values))
			}
			if project.Row.ID < 50 && len(project.Tasks.Values) != 5 {
				t.Fatalf("project %d returned %d tasks, want five", project.Row.ID, len(project.Tasks.Values))
			}
			if project.Row.ID == 50 && len(project.Tasks.Values) != 0 {
				t.Fatalf("project 50 returned %d tasks, want loaded empty", len(project.Tasks.Values))
			}
			presentAssignees := 0
			for taskIndex, task := range project.Tasks.Values {
				wantTaskID := (project.Row.ID-1)*500 + int64(taskIndex+1)
				if task.Row.ID != wantTaskID {
					t.Fatalf("project %d task %d has ID %d, want %d", project.Row.ID, taskIndex, task.Row.ID, wantTaskID)
				}
				if task.Row.ProjectID != project.Row.ID {
					t.Fatalf("task %d belongs to project %d, attached to %d", task.Row.ID, task.Row.ProjectID, project.Row.ID)
				}
				if !task.Assignee.Loaded {
					t.Fatalf("task %d has an unloaded assignee", task.Row.ID)
				}
				if task.Assignee.Present && task.Assignee.Value == nil {
					t.Fatalf("task %d has a present nil assignee", task.Row.ID)
				}
				if task.Assignee.Present {
					presentAssignees++
				}
			}
			if project.Row.ID < 50 && presentAssignees != 4 {
				t.Fatalf("project %d has %d present assignees, want four", project.Row.ID, presentAssignees)
			}
			if project.Row.ID < 50 && !project.Tasks.Values[4].Assignee.Loaded || project.Row.ID < 50 && project.Tasks.Values[4].Assignee.Present {
				t.Fatalf("project %d fifth task did not retain a loaded absent assignee", project.Row.ID)
			}
		}
		if !page.HasMore {
			break
		}
		after = page.Next
		if after == "" {
			t.Fatal("fixture page reported more rows without a cursor")
		}
	}
	if got, want := len(seen), 50; got != want {
		t.Fatalf("visited %d projects, want %d", got, want)
	}
	events := observer.snapshot()
	if got, want := pages, 5; got != want {
		t.Fatalf("visited %d pages, want %d", got, want)
	}
	if got, want := len(events), 15; got != want {
		t.Fatalf("observed %d graph statements, want %d", got, want)
	}
	wantRows := []int64{11, 50, 2, 11, 50, 2, 11, 50, 2, 11, 50, 2, 10, 45, 2}
	for index, want := range wantRows {
		if events[index] != want {
			t.Fatalf("statement %d returned %d physical rows, want %d", index, events[index], want)
		}
	}
	invocations := observer.snapshotInvocations()
	if got, want := len(invocations), 15*2; got != want {
		t.Fatalf("recorded %d invocation completions, want execution and consumption for %d statements", got, 15)
	}
	for index, completion := range invocations {
		if completion.kind != rasql.QueryOperation {
			t.Fatalf("invocation %d used %s, want query", index, completion.kind)
		}
		if completion.sql == "" || len(completion.args) == 0 {
			t.Fatalf("invocation %d did not retain SQL", index)
		}
		if index%2 == 1 && completion.rows < 0 {
			t.Fatalf("invocation %d reported invalid row count %d", index, completion.rows)
		}
	}
}

func TestOpenProjectsCancellationBeforeEachStage(t *testing.T) {
	tests := []struct {
		name            string
		statement       int
		cancelBeforeRun bool
	}{
		{name: "root", cancelBeforeRun: true},
		{name: "tasks", statement: 1},
		{name: "assignee", statement: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if test.cancelBeforeRun {
				cancel()
			}
			repository, _, observer := openFixture(t, test.statement)
			page, err := repository.OpenProjects(ctx, rasql.PageRequest{Limit: 10})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("OpenProjects returned %v, want context.Canceled", err)
			}
			if len(page.Values) != 0 || page.Next != "" || page.HasMore {
				t.Fatalf("canceled OpenProjects returned partial page %#v", page)
			}
			logicalErrors := observer.logicalErrors()
			if len(logicalErrors) == 0 || !errors.Is(logicalErrors[len(logicalErrors)-1], context.Canceled) {
				t.Fatalf("observer recorded logical errors %v, want context.Canceled", logicalErrors)
			}
			starts := observer.snapshotStarts()
			for _, statement := range starts {
				if statement > test.statement {
					t.Fatalf("cancellation at statement %d allowed later statement %d", test.statement, statement)
				}
			}
			terminals := observer.snapshotTerminals()
			if !test.cancelBeforeRun {
				foundCanceled := false
				for _, terminal := range terminals {
					if terminal.StatementIndex == test.statement && errors.Is(terminal.Err, context.Canceled) {
						foundCanceled = true
					}
				}
				if !foundCanceled {
					t.Fatalf("target statement %d had no canceled terminal: %#v", test.statement, terminals)
				}
			}
			invocations := observer.snapshotInvocations()
			foundInvocationCancel := false
			for _, completion := range invocations {
				if errors.Is(completion.err, context.Canceled) {
					foundInvocationCancel = true
					if completion.phase == rasql.ConsumptionPhase && !completion.earlyClose && test.statement > 0 {
						t.Fatalf("canceled statement completion did not report early close: %#v", completion)
					}
				}
			}
			if !foundInvocationCancel {
				t.Fatalf("no canceled invocation completion: %#v", invocations)
			}
			projects, recoveryErr := repository.AllProjects(t.Context())
			if recoveryErr != nil {
				t.Fatalf("same transaction executor did not recover after cancellation: %s", recoveryErr)
			}
			if len(projects) != 50 {
				t.Fatalf("recovery query returned %d projects, want 50", len(projects))
			}
		})
	}
}
