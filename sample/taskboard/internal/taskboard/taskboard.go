package taskboard

import (
	"context"
)

// Summary is an unfinished task shown in the Taskboard view.
type Summary struct {
	Title    string
	Priority int64
}

// TaskReader supplies the open tasks shown by the Taskboard view.
type TaskReader interface {
	OpenTasks(context.Context, int64) ([]Summary, error)
}
