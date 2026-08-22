# 6. Draw the page

The repository returns a flat list of open tasks. The page prints them grouped under project headings, with a form below and a button beside each task. This chapter writes the three pieces between those two facts: the view model that does the grouping, the HTTP layer that turns requests into repository calls, and the `main` that wires everything to a real database.

## The view model

`internal/taskboard` holds what the page shows. It imports the store for its row types and knows nothing about HTTP or SQL, which is what makes it the easy part of the application to reason about.

```go
// Task is one open task as the page prints it.
type Task struct {
	ID       int64
	Title    string
	Assignee string
}

// Group is one project's block of open tasks.
type Group struct {
	ProjectID   int64
	ProjectName string
	Tasks       []Task
}

// Choice is one entry of the add-task form's project or member list.
type Choice struct {
	ID   int64
	Name string
}

// Page is everything one drawing of the page needs.
type Page struct {
	Groups   []Group
	Projects []Choice
	Members  []Choice
}
```

The grouping is one pass over the rows:

```go
// GroupByProject folds rows into one Group per project. It relies on the
// rows arriving in project order, which is the order
// store.Repository.OpenTasks returns them in, so it starts a new group
// every time the project changes and never revisits a finished one.
func GroupByProject(rows []store.OpenTask) []Group {
	groups := make([]Group, 0, len(rows))
	for _, row := range rows {
		if len(groups) == 0 || groups[len(groups)-1].ProjectID != row.ProjectID {
			groups = append(groups, Group{ProjectID: row.ProjectID, ProjectName: row.ProjectName})
		}
		group := &groups[len(groups)-1]
		group.Tasks = append(group.Tasks, Task{ID: row.TaskID, Title: row.Title, Assignee: row.AssigneeName})
	}
	return groups
}
```

This is where [chapter 5's `ORDER BY`](05-queries.md#read-the-open-tasks) is spent. The query orders by `project_id`, so every project's rows arrive together, and the fold compares each row against the group it is currently filling instead of keeping a map of projects it has seen. Chapter 5's ordering and this loop are one decision made in two files, which the comment says out loud so that a later change to either one has a reason to check the other.

`ProjectChoices` and `MemberChoices` turn the two whole-table reads into the form's lists.

## The template

`internal/web/page.html` is the whole of the page's markup. `html/template` escapes what it interpolates, so a task titled `<script>` is printed rather than run.

```html
{{range .Groups}}
<h2>{{.ProjectName}}</h2>
<ul>
{{range .Tasks}}
  <li>
    {{.Title}} &mdash; {{.Assignee}}
    <form method="post" action="/tasks/{{.ID}}/close"><button type="submit">close</button></form>
  </li>
{{end}}
</ul>
{{else}}
<p>No open tasks.</p>
{{end}}
```

The `{{else}}` arm belongs to the outer `range` and prints when there are no groups at all, which is what an empty board looks like. Each task's close button is its own single-button form, because a `GET` link must not change anything.

The add form posts the three values `AddTask` takes:

```html
<form method="post" action="/tasks">
  <label>Project
    <select name="project_id">
      {{range .Projects}}<option value="{{.ID}}">{{.Name}}</option>{{end}}
    </select>
  </label>
  <label>Owner
    <select name="assignee_id">
      {{range .Members}}<option value="{{.ID}}">{{.Name}}</option>{{end}}
    </select>
  </label>
  <label>Title <input type="text" name="title" required></label>
  <button type="submit">add</button>
</form>
```

## The HTTP layer

`internal/web` embeds the template at build time and parses it once:

<!-- INCLUDE(sample/taskboard/internal/web/taskboard.go#template) -->
```go
//
//go:embed page.html
var pageSource string

var pageTemplate = template.Must(template.New("page").Parse(pageSource))
```
source: [sample/taskboard/internal/web/taskboard.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/web/taskboard.go)
<!-- END INCLUDE -->

The handler names what it needs from the repository as two interfaces rather than taking the concrete type:

```go
// Reader supplies everything the page shows.
type Reader interface {
	OpenTasks(context.Context) ([]store.OpenTask, error)
	AllProjects(context.Context) ([]store.ProjectsRow, error)
	AllMembers(context.Context) ([]store.MembersRow, error)
}

// Writer takes the two changes the page can make.
type Writer interface {
	AddTask(ctx context.Context, projectID int64, assigneeID int64, title string) error
	CloseTask(ctx context.Context, taskID int64) error
}
```

`store.Repository` satisfies both, and `NewHandler` takes one and uses it for both halves. Splitting the two makes the read path and the write path separately replaceable, which is what a handler test needs.

Routing uses the method-and-pattern form the standard mux takes:

<!-- INCLUDE(sample/taskboard/internal/web/taskboard.go#routes) -->
```go
// Routes returns the mux serving the application.
func (h Handler) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", h.showPage)
	mux.HandleFunc("POST /tasks", h.addTask)
	mux.HandleFunc("POST /tasks/{id}/close", h.closeTask)
	return mux
}
```
source: [sample/taskboard/internal/web/taskboard.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/web/taskboard.go)
<!-- END INCLUDE -->

`GET /{$}` matches the root and nothing below it, so a request for `/favicon.ico` gets a 404 rather than the page. `{id}` in the third pattern is read back with `r.PathValue("id")`.

Drawing the page is three reads and one execute:

```go
func (h Handler) showPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := h.reader.OpenTasks(ctx)
	if err != nil {
		h.fail(w, r, "read open tasks", err)
		return
	}
	projects, err := h.reader.AllProjects(ctx)
	if err != nil {
		h.fail(w, r, "read projects", err)
		return
	}
	members, err := h.reader.AllMembers(ctx)
	if err != nil {
		h.fail(w, r, "read members", err)
		return
	}
	page := taskboard.Page{
		Groups:   taskboard.GroupByProject(rows),
		Projects: taskboard.ProjectChoices(projects),
		Members:  taskboard.MemberChoices(members),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTemplate.Execute(w, page); err != nil {
		h.logger.ErrorContext(ctx, "failed to draw the taskboard page", slog.String("error", err.Error()))
	}
}
```

Each read passes `r.Context()`, so a browser that gives up cancels the query rather than leaving it running.

Both writes parse the form, call the repository, and answer `303 See Other`:

```go
func (h Handler) addTask(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(r.FormValue("project_id"), 10, 64)
	if err != nil {
		http.Error(w, "project_id must be a number", http.StatusBadRequest)
		return
	}
	assigneeID, err := strconv.ParseInt(r.FormValue("assignee_id"), 10, 64)
	if err != nil {
		http.Error(w, "assignee_id must be a number", http.StatusBadRequest)
		return
	}
	title := r.FormValue("title")
	if title == "" {
		http.Error(w, "title must not be empty", http.StatusBadRequest)
		return
	}
	if err := h.writer.AddTask(r.Context(), projectID, assigneeID, title); err != nil {
		h.fail(w, r, "add task", err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
```

A redirect after a successful post is what stops a browser reload from filing the task twice.

The two failure paths are told apart deliberately. A form value that is not a number is the caller's mistake and gets a `400` saying which field. A repository error is the server's problem, and the reply says only that:

<!-- INCLUDE(sample/taskboard/internal/web/taskboard.go#fail) -->
```go
// fail logs the cause and returns a response that repeats none of it, so a
// database error never reaches the browser.
func (h Handler) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.logger.ErrorContext(r.Context(), "taskboard request failed",
		slog.String("operation", what),
		slog.String("error", err.Error()),
	)
	http.Error(w, "taskboard is unavailable", http.StatusInternalServerError)
}
```
source: [sample/taskboard/internal/web/taskboard.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/web/taskboard.go)
<!-- END INCLUDE -->

A rasql error can quote the SQL it failed on, and the browser is the wrong place for that. The log gets the cause; the response gets one sentence.

## The wiring

`cmd/taskboard/main.go` is the only file that knows the application runs on PostgreSQL. It opens the handle, pairs it with the dialect, and hands the result to the repository:

<!-- INCLUDE(sample/taskboard/cmd/taskboard/main.go#open_database) -->
```go
config, err := pgx.ParseConfig(dsn)
if err != nil {
	return fmt.Errorf("parse TASKBOARD_DSN: %w", err)
}
database := stdlib.OpenDB(*config)
defer func() { _ = database.Close() }()

// A rasql.DB pairs the handle with the dialect used to render SQL.
db, err := rasql.New(database, dialect.PostgreSQL())
if err != nil {
	return fmt.Errorf("create the rasql db: %w", err)
}
```
source: [sample/taskboard/cmd/taskboard/main.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/cmd/taskboard/main.go)
<!-- END INCLUDE -->

`rasql.New` wraps a handle somebody else opened, so the driver, the pool settings, and the closing all stay where they were. Changing engines would change `dialect.PostgreSQL()` and the driver above it, and nothing in `internal/store`, `internal/taskboard`, or `internal/web`.

The server is started on a goroutine and shut down on a signal, so an interrupt drains the requests in flight instead of cutting them off:

<!-- INCLUDE(sample/taskboard/cmd/taskboard/main.go#serve) -->
```go
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()

listening := make(chan error, 1)
go func() {
	logger.Info("taskboard is listening", slog.String("address", address))
	listening <- server.ListenAndServe()
}()

select {
case err := <-listening:
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("serve: %w", err)
case <-ctx.Done():
}

shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
if err := server.Shutdown(shutdownCtx); err != nil {
	return fmt.Errorf("shut down: %w", err)
}
```
source: [sample/taskboard/cmd/taskboard/main.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/cmd/taskboard/main.go)
<!-- END INCLUDE -->

Reading from `listening` is what reports a failure to bind. Without that arm, a port already in use would leave the process sitting on `ctx.Done()` forever, listening to nothing.

## Run it

```sh
TASKBOARD_DSN='postgres://rasql:rasql@127.0.0.1:5432/rasql_taskboard?sslmode=disable' \
TASKBOARD_ADDR=127.0.0.1:18080 \
  go run ./cmd/taskboard
```

```text
time=2026-08-21T18:00:52.507+09:00 level=INFO msg="taskboard is listening" address=127.0.0.1:18080
```

Ask for the page:

```sh
curl -s http://127.0.0.1:18080/
```

```text
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Taskboard</title>
</head>
<body>
<h1>Taskboard</h1>


<h2>Website refresh</h2>
<ul>

  <li>
    Draft the rollout plan &mdash; Ada Lovelace
    <form method="post" action="/tasks/1/close"><button type="submit">close</button></form>
  </li>

</ul>

<h2>Billing cleanup</h2>
<ul>

  <li>
    Reconcile March invoices &mdash; Ada Lovelace
    <form method="post" action="/tasks/3/close"><button type="submit">close</button></form>
  </li>

</ul>


<h2>Add a task</h2>
<form method="post" action="/tasks">
  <label>Project
    <select name="project_id">
      <option value="1">Website refresh</option><option value="2">Billing cleanup</option>
    </select>
  </label>
  <label>Owner
    <select name="assignee_id">
      <option value="1">Ada Lovelace</option><option value="2">Grace Hopper</option>
    </select>
  </label>
  <label>Title <input type="text" name="title" required></label>
  <button type="submit">add</button>
</form>
</body>
</html>
```

Two projects, one open task each, the owners' names joined in, and both dropdowns filled from the tables. Chapter 5 left the board in exactly that state.

Post the add form the way the browser would:

```sh
curl -s -i -X POST http://127.0.0.1:18080/tasks \
  -d 'project_id=2' -d 'assignee_id=2' -d 'title=Chase the March gaps'
```

```text
HTTP/1.1 303 See Other
Location: /
Content-Length: 0
```

Close the first task:

```sh
curl -s -i -X POST http://127.0.0.1:18080/tasks/1/close
```

```text
HTTP/1.1 303 See Other
Location: /
Content-Length: 0
```

Ask for the page again, printing just the list this time:

```sh
curl -s http://127.0.0.1:18080/ | sed -n '/Taskboard<\/h1>/,/Add a task/p'
```

```text
<h1>Taskboard</h1>


<h2>Billing cleanup</h2>
<ul>

  <li>
    Reconcile March invoices &mdash; Ada Lovelace
    <form method="post" action="/tasks/3/close"><button type="submit">close</button></form>
  </li>

  <li>
    Chase the March gaps &mdash; Grace Hopper
    <form method="post" action="/tasks/4/close"><button type="submit">close</button></form>
  </li>

</ul>


<h2>Add a task</h2>
```

The new task is there under its project, owned by Grace. The Website refresh heading is gone: its only open task was the one that got closed, so no row came back for it and `GroupByProject` had no group to start. The page shows what is open, not what exists.

```sh
git add internal/taskboard
git commit -m 'add the taskboard view model'
git add internal/web cmd go.mod go.sum
git commit -m 'serve the taskboard page over http'
```

## Finish the README

The project now runs. Write how to run it into `README.md`, beneath the description chapter 2 put there:

```sh
export TASKBOARD_DSN='postgres://rasql:rasql@127.0.0.1:5432/rasql_taskboard?sslmode=disable'
./scripts/migrate.sh apply
go run ./cmd/taskboard
```

That export is a line for the next person to open the project, not a step to run again here. Chapter 2 already set `TASKBOARD_DSN` in this shell.

```sh
git add README.md
git commit -m 'describe how to run the application'
```

## Where the application stands

Six chapters in, the whole thing is eleven commits and about nine hundred lines of Go, SQL, and HTML, of which the generator wrote just over half.

The schema was decided by argument, shaped in a live database, captured into a migration, and proved to rebuild what it came from. The Go that talks to it was generated from that schema, and the only hand-written line in `internal/store` that touches a column name is a generated accessor call. The page is drawn from a view model that knows nothing about SQL, over an HTTP layer that knows nothing about PostgreSQL.

Chapter 7 changes the schema underneath all of that and follows the compiler through what breaks.

## Next

[Change the schema](07-change.md).
