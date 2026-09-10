# 9. Test and operate the workflow

The application has three verification layers. Presentation tests exercise
loaded-state conversion. Repository tests exercise writes, paging, NULL, and
the overdue boundary. A PostgreSQL fixture exercises graph row and statement
bounds on the real engine profile.

## Test loaded states

The view model accepts a loaded empty task collection, an absent assignee, and
a present assignee. It rejects an unloaded collection or unloaded assignee.
These tests keep a partial graph from becoming a misleading empty page.

## Test HTTP behavior

The web tests pass a fake reader and writer through interfaces. They assert that
the handler forwards the cursor and limit, renders an unassigned task, and
returns a client error for malformed IDs.

<!-- INCLUDE(sample/taskboard/internal/web/taskboard_test.go#add_no_owner) -->
```go
func TestAddTaskWithNoOwner(t *testing.T) {
	repository := &fakeRepository{}
	form := url.Values{"project_id": {"1"}, "assignee_id": {""}, "title": {"Find an owner"}}
	request := httptest.NewRequest(http.MethodPost, "/tasks", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	newTestHandler(repository).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST /tasks returned %d, want 303", recorder.Code)
	}
	if repository.addedTitle != "Find an owner" {
		t.Errorf("AddTask got title %q, want \"Find an owner\"", repository.addedTitle)
	}
	if repository.addedOwner != nil {
		t.Errorf("AddTask got owner %v, want nil for an empty assignee_id", *repository.addedOwner)
	}
}
```
source: [sample/taskboard/internal/web/taskboard_test.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/web/taskboard_test.go)
<!-- END INCLUDE -->

## Test writes and boundaries

The live tests use one transaction and roll it back. The transaction setup wraps
its own SQL transaction in a discovered executor, so no parent executor leaks
into the child scope.

Generated create plans cover database defaults, an explicit assignee, an explicit
NULL, and a nullable due date:

The overdue test fixes a caller location and compares today, yesterday, and a
closed late task.

## Exercise the bounded graph

The PostgreSQL fixture creates exactly 50 projects and 500 tasks per project.
Each project has more than five open tasks. It walks keyset pages and observes
physical statement completions and decoded rows. The assertions require every
project exactly once, no more than five task values per project, at most 250
task rows consumed, stable IDs, and correct optional assignee state.

The fixture also cancels before the root query and before each child stage. A
cancelled page returns `context.Canceled`, closes its rows, and attaches no
partial graph. Each case builds a fresh plan and uses a fresh transaction.

## Run the gates

The command keeps process errors at the entry point:

```go
func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "taskboard: %s\n", err)
		os.Exit(1)
	}
}
```

Run the unit and integration checks from the project root:

```sh
go test ./...
go test -race ./...
```

The live PostgreSQL command is opt-in through `TASKBOARD_TEST_DSN`; it must
point to a newly created disposable database. The application never drops or
reuses a shared database.

Two more commands gate a change before it ships. The first needs no
database at all:

```sh
env -u TASKBOARD_DSN -u TASKBOARD_TEST_DSN rasql codegen check
```

```text
internal/store is up to date; no database was consulted
```

The second needs `TASKBOARD_DSN`, and proves the checked-in store still
matches what the database holds today: regenerating it changes nothing.

```sh
./scripts/generate.sh
git diff --exit-code
```

```text
migration apply completed: 0 applied
generated internal/store
```

A clean `git diff` after that run is the whole point: if the database and
the checked-in files had drifted apart, regenerating would have rewritten
something for the reviewer to see.
