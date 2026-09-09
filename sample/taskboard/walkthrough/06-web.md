# 6. Draw a loaded graph

The repository returns graph state, and the view model turns that state into
HTML values. The conversion keeps SQL concerns out of the handler while still
checking every loaded-state bit.

Every task in this version has an owner, so the view model has no due date
and nothing to say about an absent one yet:

```go
// Task is one open task as the page prints it.
type Task struct {
	ID       int64
	Title    string
	Assignee string
}

// Unassigned is what the page prints where an owner's name would go.
const Unassigned = "unassigned"

func assigneeText(loaded rasql.LoadedOne[store.MembersRow]) (string, error) {
	if !loaded.Loaded {
		return "", fmt.Errorf("assignee state is unloaded")
	}
	if !loaded.Present || loaded.Value == nil {
		return Unassigned, nil
	}
	return loaded.Value.Name, nil
}
```

```go
// Page is everything one drawing of the page needs.
type Page struct {
	Groups   []Group
	Projects []Choice
	Members  []Choice
	Limit    int
	Next     string
	HasMore  bool
}
```

`Tasks.Loaded` distinguishes a project with no matching open tasks from a
project whose tasks were never expanded. An optional assignee has
`Loaded=true, Present=false` when the task is unassigned. A present assignee
must include a nonnil value. Any unloaded state is an error.

## Pass the page request through HTTP

The handler parses the cursor and limit, calls the one repository page method,
and places the returned cursor in the next link. It makes a separate bounded
read for the form choices.

<!-- INCLUDE(sample/taskboard/internal/web/taskboard.go#template) -->
```go
//go:embed page.html
var pageSource string

var pageTemplate = template.Must(template.New("page").Parse(pageSource))
```
source: [sample/taskboard/internal/web/taskboard.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/web/taskboard.go)
<!-- END INCLUDE -->

<!-- INCLUDE(sample/taskboard/internal/web/taskboard.go#newhandler) -->
```go
// NewHandler creates a handler over a reader and a writer. store.Repository
// satisfies both, so an application passes it twice; a test passes whatever
// it needs to stand in for either half.
func NewHandler(reader Reader, writer Writer, logger *slog.Logger) Handler {
	return Handler{reader: reader, writer: writer, logger: logger}
}
```
source: [sample/taskboard/internal/web/taskboard.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/web/taskboard.go)
<!-- END INCLUDE -->

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

The add-task form requires a real `assignee_id`; chapter 7 is where an empty
one first means something. The page never reconstructs graph keys or joins.
It receives project groups and cursor state from the repository, then renders
them.

## Start the service

The process opens a SQL handle, discovers the retained PostgreSQL 17 profile,
wraps it as an executor, and passes that executor to the repository.

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
profile, err := rasql.DiscoverEngineProfile(context.Background(), db, "postgresql-17")
if err != nil {
	return fmt.Errorf("discover PostgreSQL engine profile: %w", err)
}
executor, err := rasql.AsExecutor(db, profile)
if err != nil {
	return fmt.Errorf("create the rasql executor: %w", err)
}
```
source: [sample/taskboard/cmd/taskboard/main.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/cmd/taskboard/main.go)
<!-- END INCLUDE -->

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

Apply migrations before starting the binary:

```sh
./scripts/migrate.sh apply
go run ./cmd/taskboard
```
