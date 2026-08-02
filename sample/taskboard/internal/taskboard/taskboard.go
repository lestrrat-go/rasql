package taskboard

import (
	"context"
	"fmt"
	"io"
)

// Summary is an unfinished task shown in the Taskboard view.
type Summary struct {
	Title    string
	Priority int64
}

// Store supplies the Taskboard workflow's data.
type Store interface {
	SeedDemo(context.Context) error
	OpenTasks(context.Context, int64) ([]Summary, error)
}

// Run seeds the demo data and writes its open tasks view.
func Run(ctx context.Context, store Store, output io.Writer) error {
	if err := store.SeedDemo(ctx); err != nil {
		return fmt.Errorf("seed taskboard: %w", err)
	}

	const projectID = int64(100)
	openTasks, err := store.OpenTasks(ctx, projectID)
	if err != nil {
		return fmt.Errorf("find open tasks: %w", err)
	}
	if _, err := fmt.Fprintln(output, "Open tasks for Website refresh:"); err != nil {
		return fmt.Errorf("write heading: %w", err)
	}
	for _, task := range openTasks {
		if _, err := fmt.Fprintf(output, "- P%d %s\n", task.Priority, task.Title); err != nil {
			return fmt.Errorf("write task: %w", err)
		}
	}
	return nil
}
