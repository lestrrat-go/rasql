package web

import (
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/lestrrat-go/rasql/sample/taskboard/internal/taskboard"
)

const taskboardTemplate = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Taskboard</title></head>
<body>
<main>
<h1>Website refresh</h1>
<ul>{{range .Tasks}}<li>P{{.Priority}} {{.Title}}</li>{{end}}</ul>
<nav>{{if .PreviousPage}}<a href="/?page={{.PreviousPage}}">Previous</a>{{end}}{{if .NextPage}}<a href="/?page={{.NextPage}}">Next</a>{{end}}</nav>
</main>
</body>
</html>`

const taskboardPageSize = 50

type taskboardPage struct {
	Tasks        []taskboard.Summary
	PreviousPage int
	NextPage     int
}

type taskboardHandler struct {
	tasks  taskboard.TaskReader
	page   *template.Template
	logger *slog.Logger
}

// NewTaskboardHandler returns the Taskboard HTTP routes.
func NewTaskboardHandler(tasks taskboard.TaskReader) http.Handler {
	return newTaskboardHandler(tasks, slog.Default())
}

func newTaskboardHandler(tasks taskboard.TaskReader, logger *slog.Logger) http.Handler {
	handler := taskboardHandler{
		tasks:  tasks,
		page:   template.Must(template.New("taskboard").Parse(taskboardTemplate)),
		logger: logger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", handler.showTasks)
	mux.HandleFunc("GET /healthz", health)
	return mux
}

func (handler taskboardHandler) showTasks(response http.ResponseWriter, request *http.Request) {
	offset, err := taskboardOffset(request)
	if err != nil {
		http.Error(response, "page must be a positive integer", http.StatusBadRequest)
		return
	}
	tasks, err := handler.tasks.OpenTasks(request.Context(), 100, taskboardPageSize+1, offset)
	if err != nil {
		handler.logger.Error("read open tasks", "error", err)
		http.Error(response, "taskboard is unavailable", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	page := taskboardPage{Tasks: tasks}
	if offset > 0 {
		page.PreviousPage = offset / taskboardPageSize
	}
	if len(tasks) > taskboardPageSize {
		page.Tasks = tasks[:taskboardPageSize]
		page.NextPage = offset/taskboardPageSize + 2
	}
	if err := handler.page.Execute(response, page); err != nil {
		return
	}
}

func taskboardOffset(request *http.Request) (int, error) {
	value := request.URL.Query().Get("page")
	if value == "" {
		return 0, nil
	}
	page, err := strconv.Atoi(value)
	if err != nil || page < 1 {
		return 0, fmt.Errorf("invalid page %q", value)
	}
	maxOffset := int(^uint(0) >> 1)
	if page > maxOffset/taskboardPageSize+1 {
		return 0, fmt.Errorf("invalid page %q", value)
	}
	return (page - 1) * taskboardPageSize, nil
}

func health(response http.ResponseWriter, _ *http.Request) {
	if _, err := fmt.Fprintln(response, "ok"); err != nil {
		return
	}
}
