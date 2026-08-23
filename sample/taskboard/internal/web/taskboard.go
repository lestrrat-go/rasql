// Package web turns HTTP requests into repository calls and draws the
// Taskboard page from the view model.
package web

import (
	"context"
	_ "embed"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"example.com/taskboard/internal/store"
	"example.com/taskboard/internal/taskboard"
)

// BEGIN(template)

//go:embed page.html
var pageSource string

var pageTemplate = template.Must(template.New("page").Parse(pageSource))

// END(template)

// Reader supplies everything the page shows.
type Reader interface {
	OpenTasks(context.Context) ([]store.OpenTask, error)
	AllProjects(context.Context) ([]store.ProjectsRow, error)
	AllMembers(context.Context) ([]store.MembersRow, error)
	CountOverdue(ctx context.Context, on time.Time) (int64, error)
}

// Writer takes the two changes the page can make.
type Writer interface {
	AddTask(ctx context.Context, projectID int64, assigneeID *int64, title string) error
	CloseTask(ctx context.Context, taskID int64) error
}

// Handler serves the Taskboard page and its two forms.
type Handler struct {
	reader Reader
	writer Writer
	logger *slog.Logger
}

// BEGIN(newhandler)

// NewHandler creates a handler over a reader and a writer. store.Repository
// satisfies both, so an application passes it twice; a test passes whatever
// it needs to stand in for either half.
func NewHandler(reader Reader, writer Writer, logger *slog.Logger) Handler {
	return Handler{reader: reader, writer: writer, logger: logger}
}

// END(newhandler)

// BEGIN(routes)

// Routes returns the mux serving the application.
func (h Handler) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", h.showPage)
	mux.HandleFunc("POST /tasks", h.addTask)
	mux.HandleFunc("POST /tasks/{id}/close", h.closeTask)
	return mux
}

// END(routes)

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
	// BEGIN(overdue_read)
	overdue, err := h.reader.CountOverdue(ctx, time.Now())
	if err != nil {
		h.fail(w, r, "count overdue tasks", err)
		return
	}
	// END(overdue_read)
	page := taskboard.Page{
		Groups:   taskboard.GroupByProject(rows),
		Overdue:  overdue,
		Projects: taskboard.ProjectChoices(projects),
		Members:  taskboard.MemberChoices(members),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTemplate.Execute(w, page); err != nil {
		h.logger.ErrorContext(ctx, "failed to draw the taskboard page", slog.String("error", err.Error()))
	}
}

func (h Handler) addTask(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(r.FormValue("project_id"), 10, 64)
	if err != nil {
		http.Error(w, "project_id must be a number", http.StatusBadRequest)
		return
	}
	// BEGIN(empty_assignee)
	// An empty assignee_id is the form's way of saying nobody owns this yet.
	var assigneeID *int64
	if raw := r.FormValue("assignee_id"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			http.Error(w, "assignee_id must be a number", http.StatusBadRequest)
			return
		}
		assigneeID = &parsed
	}
	// END(empty_assignee)
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

func (h Handler) closeTask(w http.ResponseWriter, r *http.Request) {
	taskID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "task id must be a number", http.StatusBadRequest)
		return
	}
	if err := h.writer.CloseTask(r.Context(), taskID); err != nil {
		h.fail(w, r, "close task", err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// BEGIN(fail)

// fail logs the cause and returns a response that repeats none of it, so a
// database error never reaches the browser.
func (h Handler) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.logger.ErrorContext(r.Context(), "taskboard request failed",
		slog.String("operation", what),
		slog.String("error", err.Error()),
	)
	http.Error(w, "taskboard is unavailable", http.StatusInternalServerError)
}

// END(fail)
