package compilefail

import "example.com/taskboard/internal/store"

func wrongProjectionType() {
	var expressions store.ProjectsExpressions
	_, _ = store.TasksProjection(expressions)
}
