package web

import (
	"fmt"
	"html/template"
	"log/slog"
	"net/http"

	"github.com/lestrrat-go/rasql/sample/taskboard/internal/taskboard"
)

const taskboardTemplate = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Taskboard</title></head>
<body>
<main>
<h1>Website refresh</h1>
<ul>{{range .Tasks}}<li>P{{.Priority}} {{.Title}}</li>{{end}}</ul>
</main>
</body>
</html>`

type taskboardPage struct {
	Tasks []taskboard.Summary
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
	tasks, err := handler.tasks.OpenTasks(request.Context(), 100)
	if err != nil {
		handler.logger.Error("read open tasks", "error", err)
		http.Error(response, "taskboard is unavailable", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := handler.page.Execute(response, taskboardPage{Tasks: tasks}); err != nil {
		return
	}
}

func health(response http.ResponseWriter, _ *http.Request) {
	if _, err := fmt.Fprintln(response, "ok"); err != nil {
		return
	}
}
