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
	mu   sync.Mutex
	rows []int64
}

func (observer *statementObserver) start(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) {
	if event.Kind != rasql.EventStatement || event.Phase != rasql.EventStart {
		return ctx, nil
	}
	return ctx, rasql.EventCompletionFunc(func(_ context.Context, terminal rasql.Event) error {
		if terminal.Phase != rasql.EventTerminal {
			return nil
		}
		observer.mu.Lock()
		defer observer.mu.Unlock()
		observer.rows = append(observer.rows, terminal.Rows)
		return nil
	})
}

func (observer *statementObserver) snapshot() []int64 {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return append([]int64(nil), observer.rows...)
}

func openFixture(t *testing.T, cancelChild context.CancelFunc) (store.Repository, rasql.Executor, *statementObserver) {
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
		t.Skip("graph fixture requires an empty disposable database")
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
	executor, err := rasql.AsExecutor(db, profile)
	if err != nil {
		t.Fatalf("create the rasql executor: %s", err)
	}
	observer := new(statementObserver)
	observed, err := rasql.WithEventObservers(executor,
		rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}),
		rasql.EventObserverFunc(func(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) {
			if cancelChild != nil && event.Kind == rasql.EventStatement && event.Phase == rasql.EventStart && event.StatementIndex == 1 {
				cancelChild()
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
		INSERT INTO members (name)
		VALUES ('D3 Ada'), ('D3 Grace')
	`); err != nil {
		t.Fatalf("seed fixture members: %s", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO projects (name)
		SELECT format('D3 fixture project %s', project_no)
		FROM generate_series(1, 50) AS project_no
	`); err != nil {
		t.Fatalf("seed fixture projects: %s", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO tasks (project_id, assignee_id, title, is_open)
		SELECT p.id,
			CASE WHEN task_no % 10 = 0 THEN NULL ELSE (SELECT min(id) FROM members) END,
			format('D3 fixture task %s/%s', project_number.project_no, task_no),
			task_no % 17 <> 0
		FROM projects AS p
		CROSS JOIN generate_series(1, 500) AS task_no
		CROSS JOIN LATERAL (
			SELECT substring(p.name FROM '[0-9]+$') AS project_no
		) AS project_number
		WHERE p.name LIKE 'D3 fixture project %'
	`); err != nil {
		t.Fatalf("seed fixture tasks: %s", err)
	}
}

func TestOpenProjectsBoundedGraphFixture(t *testing.T) {
	repository, _, observer := openFixture(t, nil)
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
		for _, project := range page.Values {
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
			for _, task := range project.Tasks.Values {
				if task.Row.ProjectID != project.Row.ID {
					t.Fatalf("task %d belongs to project %d, attached to %d", task.Row.ID, task.Row.ProjectID, project.Row.ID)
				}
				if !task.Assignee.Loaded {
					t.Fatalf("task %d has an unloaded assignee", task.Row.ID)
				}
				if task.Assignee.Present && task.Assignee.Value == nil {
					t.Fatalf("task %d has a present nil assignee", task.Row.ID)
				}
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
	if got, want := len(events), pages*3; got != want {
		t.Fatalf("observed %d graph statements across %d pages, want %d", got, pages, want)
	}
	var taskRows int64
	for page := 0; page < pages; page++ {
		taskRows += events[page*3+1]
	}
	if taskRows > 250 {
		t.Fatalf("consumed %d task rows, want at most 250", taskRows)
	}
}

func TestOpenProjectsCancellationBeforeChildStage(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	repository, _, _ := openFixture(t, cancel)
	_, err := repository.OpenProjects(ctx, rasql.PageRequest{Limit: 10})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("OpenProjects returned %v, want context.Canceled", err)
	}
}
