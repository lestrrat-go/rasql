package store

import (
	"context"
	"time"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
)

// These compile-only examples are the source for short snippets in the API
// documentation. The runnable Taskboard repository exercises the same paths.

func docsReadTasks(ctx context.Context, executor rasql.Executor) error {
	// BEGIN(canonical_read)
	source, err := Tasks().Source("tasks")
	if err != nil {
		return err
	}
	expressions, err := (TasksColumns{}).Bind(source)
	if err != nil {
		return err
	}
	projection, err := TasksProjection(expressions)
	if err != nil {
		return err
	}
	q := rasql.Select(source.Source(), projection).
		Where(rasql.EqualValue(expressions.IsOpen.Expr(), true)).
		OrderBy(rasql.AscExpr(expressions.ID.Expr()))
	rows, err := rasql.All(ctx, executor, q)
	// END(canonical_read)
	_ = rows
	return err
}

func docsCreateTask(ctx context.Context, executor rasql.Executor, projectID int64) error {
	// BEGIN(canonical_create)
	plan, err := NewTasksCreate().
		ProjectID(projectID).
		ClearAssigneeID().
		Title("document canonical mutations").
		DefaultIsOpen().
		DefaultCreatedAt().
		Plan()
	if err != nil {
		return err
	}
	outcome, err := rasql.ExecMutation(ctx, executor, plan)
	// END(canonical_create)
	_ = outcome
	return err
}

func docsPatchTask(ctx context.Context, executor rasql.Executor, taskID int64) error {
	// BEGIN(canonical_patch)
	source, err := Tasks().Source("")
	if err != nil {
		return err
	}
	expressions, err := (TasksColumns{}).Bind(source)
	if err != nil {
		return err
	}
	plan, err := NewTasksPatch().IsOpen(false).
		Where(rasql.EqualValue(expressions.ID.Expr(), taskID))
	if err != nil {
		return err
	}
	outcome, err := rasql.ExecMutation(ctx, executor, plan)
	// END(canonical_patch)
	_ = outcome
	return err
}

func docsStatementPlan(ctx context.Context, executor rasql.Executor) error {
	// BEGIN(statement_plan)
	tasks := Tasks().Table
	statement, err := query.NewInsert(tasks.Ref(),
		query.Set(tasks.Column("project_id"), int64(1)),
		query.Set(tasks.Column("title"), "write the guide"),
	)
	if err != nil {
		return err
	}
	plan, err := rasql.NewStatementPlan(statement)
	if err != nil {
		return err
	}
	outcome, err := rasql.ExecMutation(ctx, executor, plan)
	// END(statement_plan)
	_ = outcome
	return err
}

func docsReturning(ctx context.Context, executor rasql.Executor, plan rasql.MutationPlan) error {
	source, err := Tasks().Source("")
	if err != nil {
		return err
	}
	expressions, err := (TasksColumns{}).Bind(source)
	if err != nil {
		return err
	}
	projection, err := TasksProjection(expressions)
	if err != nil {
		return err
	}
	// BEGIN(mutation_returning)
	returned, err := rasql.Returning(plan, projection)
	if err != nil {
		return err
	}
	saved, err := rasql.One(ctx, executor, returned)
	// END(mutation_returning)
	_ = saved
	return err
}

func docsBatch(ctx context.Context, executor rasql.Executor, plans []rasql.MutationPlan) error {
	// BEGIN(mutation_batch)
	outcome, err := rasql.ExecMutationBatch(ctx, executor, plans, rasql.BulkOptions{
		MaxRows:           500,
		MaxBindParameters: 32000,
		Atomic:            true,
	})
	// END(mutation_batch)
	_ = outcome
	return err
}

func docsWithin(ctx context.Context, executor rasql.Executor, first, second rasql.MutationPlan) error {
	// BEGIN(transaction_scope)
	err := rasql.Within(ctx, executor, nil, func(ctx context.Context, scoped rasql.Executor) error {
		if _, err := rasql.ExecMutation(ctx, scoped, first); err != nil {
			return err
		}
		_, err := rasql.ExecMutation(ctx, scoped, second)
		return err
	})
	// END(transaction_scope)
	return err
}

type docsReportRow struct {
	DisplayName string `rasql:"display_name"`
	OpenTasks   int64  `rasql:"open_tasks"`
}

func docsDynamic(ctx context.Context, executor rasql.Executor, reportSQL string, args []rasql.NativeArgument) error {
	// BEGIN(dynamic_projection)
	result, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "display_name", Type: schema.TextType{}},
		rasql.ResultColumn{Name: "open_tasks", Type: schema.IntegerType{}},
	)
	if err != nil {
		return err
	}
	projection, err := rasql.DynamicProjection[docsReportRow](result)
	// END(dynamic_projection)
	if err != nil {
		return err
	}
	// BEGIN(dynamic_native_query)
	q, err := rasql.Native(
		rasql.NativeStatement{Engine: "postgresql", SQL: reportSQL, Args: args},
		projection,
		rasql.Many,
	)
	if err != nil {
		return err
	}
	rows, err := rasql.All(ctx, executor, q)
	// END(dynamic_native_query)
	_ = rows
	return err
}

func docsNamedQuery(ctx context.Context, executor rasql.Executor, today time.Time) error {
	// BEGIN(named_query)
	q, err := OverdueCount(today)
	if err != nil {
		return err
	}
	row, err := rasql.One(ctx, executor, q)
	// END(named_query)
	_ = row
	return err
}

func docsNativeQuery(projection rasql.Projection[int64], dueBefore time.Time) error {
	// BEGIN(direct_native_query)
	q, err := rasql.Native(
		rasql.NativeStatement{
			Engine: "postgresql",
			SQL:    "SELECT count(*) AS total FROM tasks WHERE due_on < $1",
			Args:   []rasql.NativeArgument{{Value: dueBefore}},
		},
		projection,
		rasql.ExactlyOne,
	)
	// END(direct_native_query)
	_ = q
	return err
}
