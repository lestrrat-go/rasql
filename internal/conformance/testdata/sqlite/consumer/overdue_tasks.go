package consumer

import (
	"context"
	"time"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/internal/conformance/testdata/sqlite/internal/store"
)

type OverdueRow struct {
	ID         int64
	ProjectID  int64
	AssigneeID rasql.Nullable[int64]
	Title      string
	IsOpen     bool
	DueOn      rasql.Nullable[time.Time]
	CreatedAt  time.Time
}

func RunOverdueTask(ctx context.Context, executor rasql.Executor, projectID int64, open bool, cutoff time.Time) (OverdueRow, error) {
	query, err := store.OverdueTask(projectID, open, cutoff)
	if err != nil {
		return OverdueRow{}, err
	}
	row, err := rasql.One(ctx, executor, query)
	if err != nil {
		return OverdueRow{}, err
	}
	return OverdueRow{ID: row.ID, ProjectID: row.ProjectID, AssigneeID: row.AssigneeID, Title: row.Title, IsOpen: row.IsOpen, DueOn: row.DueOn, CreatedAt: row.CreatedAt}, nil
}

func RunMaybeOverdueTask(ctx context.Context, executor rasql.Executor, projectID int64, open bool, cutoff time.Time) (OverdueRow, bool, error) {
	query, err := store.MaybeOverdueTask(projectID, open, cutoff)
	if err != nil {
		return OverdueRow{}, false, err
	}
	row, found, err := rasql.Maybe(ctx, executor, query)
	if err != nil {
		return OverdueRow{}, false, err
	}
	if !found {
		return OverdueRow{}, false, nil
	}
	return OverdueRow{ID: row.ID, ProjectID: row.ProjectID, AssigneeID: row.AssigneeID, Title: row.Title, IsOpen: row.IsOpen, DueOn: row.DueOn, CreatedAt: row.CreatedAt}, true, nil
}

func RunOverdueTasks(ctx context.Context, executor rasql.Executor, projectID int64, open bool, cutoff time.Time) ([]OverdueRow, error) {
	query, err := store.OverdueTasks(projectID, open, cutoff)
	if err != nil {
		return nil, err
	}
	rows, err := rasql.All(ctx, executor, query)
	if err != nil {
		return nil, err
	}
	result := make([]OverdueRow, len(rows))
	for index, row := range rows {
		result[index] = OverdueRow{ID: row.ID, ProjectID: row.ProjectID, AssigneeID: row.AssigneeID, Title: row.Title, IsOpen: row.IsOpen, DueOn: row.DueOn, CreatedAt: row.CreatedAt}
	}
	return result, nil
}
