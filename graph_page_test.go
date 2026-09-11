package rasql_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type pageParentRow struct{ ID int64 }
type pageTaskRow struct{ ID, Parent, Assignee int64 }
type pageAssigneeRow struct {
	ID   int64
	Name string
}
type pageLabelRow struct {
	ID   int64
	Name string
}
type pageRootGraph struct {
	ID    int64
	Tasks rasql.LoadedMany[pageTaskGraph]
}
type pageTaskGraph struct {
	ID       int64
	Assignee rasql.LoadedOne[pageAssigneeGraph]
	Labels   rasql.LoadedMany[pageLabelGraph]
}
type pageAssigneeGraph struct {
	ID   int64
	Name string
}
type pageLabelGraph struct {
	ID   int64
	Name string
}

type pageParentDecoder struct{ schema rasql.ResultSchema }

func (d pageParentDecoder) ResultSchema() rasql.ResultSchema                   { return d.schema }
func (pageParentDecoder) Presence() []rasql.Presence                           { return nil }
func (pageParentDecoder) DecodeRow(s rasql.ScanSource, v *pageParentRow) error { return s.Scan(&v.ID) }

type pageTaskDecoder struct{ schema rasql.ResultSchema }

func (d pageTaskDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (pageTaskDecoder) Presence() []rasql.Presence         { return nil }
func (pageTaskDecoder) DecodeRow(s rasql.ScanSource, v *pageTaskRow) error {
	return s.Scan(&v.ID, &v.Parent, &v.Assignee)
}

type pageAssigneeDecoder struct{ schema rasql.ResultSchema }

func (d pageAssigneeDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (pageAssigneeDecoder) Presence() []rasql.Presence         { return nil }
func (pageAssigneeDecoder) DecodeRow(s rasql.ScanSource, v *pageAssigneeRow) error {
	return s.Scan(&v.ID, &v.Name)
}

type pageLabelDecoder struct{ schema rasql.ResultSchema }

func (d pageLabelDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (pageLabelDecoder) Presence() []rasql.Presence         { return nil }
func (pageLabelDecoder) DecodeRow(s rasql.ScanSource, v *pageLabelRow) error {
	return s.Scan(&v.ID, &v.Name)
}

type pageCountingExecutor struct {
	rasql.Executor
	statements atomic.Int64
	rows       atomic.Int64
	queries    []stmt.Statement
}

func (e *pageCountingExecutor) Query(ctx context.Context, s stmt.Statement) (rasql.ResultRows, error) {
	e.queries = append(e.queries, s)
	rows, err := e.Executor.Query(ctx, s)
	if err != nil {
		return nil, err
	}
	e.statements.Add(1)
	return &pageCountingRows{ResultRows: rows, rows: &e.rows}, nil
}

func TestGraphPage(t *testing.T) {
	t.Run("one root read retains its roots", func(t *testing.T) {
		base, rootQuery, rootKey, taskKey, _, _, _, _, _, _, _, tasks, _, _ := pageFixture(t)
		executor := &pageCountingExecutor{Executor: base}
		executorProfiled := pageProfiled(t, executor)
		var attachments atomic.Int64
		rootExpr := rasql.Q1ProjectedExpr[pageParentRow, int64](rootQuery, 0)
		rootQuery = rootQuery.Where(rasql.LessValue(rootExpr, int64(4)))
		rootGraphEdge, err := rasql.HasMany("tasks", rootKey, taskKey, tasks, rasql.EdgeOptions{BindLimit: 1000}, func(parent *pageRootGraph, loaded rasql.LoadedMany[pageTaskGraph]) {
			attachments.Add(1)
			parent.Tasks = loaded
		})
		require.NoError(t, err)
		plan, err := rasql.NewGraphPlan(rootQuery, func(row pageParentRow) pageRootGraph { return pageRootGraph{ID: row.ID} }, rootGraphEdge)
		require.NoError(t, err)
		rootPageKey := rasql.AscKey(rootExpr, func(row pageParentRow) int64 { return row.ID })
		spec, err := rasql.NewPageSpec([]rasql.PageKey[pageParentRow]{rootPageKey}, rootPageKey)
		require.NoError(t, err)
		var mu sync.Mutex
		var events []rasql.Event
		observed, err := rasql.WithEventObservers(executorProfiled, rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.EventObserverFunc(func(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
			return ctx, rasql.EventCompletionFunc(func(_ context.Context, terminal rasql.Event) error {
				mu.Lock()
				events = append(events, terminal)
				mu.Unlock()
				return nil
			})
		}))
		require.NoError(t, err)
		snapshotEvents := func() []rasql.Event {
			mu.Lock()
			defer mu.Unlock()
			return append([]rasql.Event(nil), events...)
		}

		first, err := rasql.PageGraphAfter(t.Context(), observed, plan, spec, rasql.PagePolicy{DefaultLimit: 2, MaxLimit: 2}, rasql.PageRequest{Limit: 2})
		require.NoError(t, err)
		require.Equal(t, []int64{1, 2}, []int64{first.Values[0].ID, first.Values[1].ID})
		require.True(t, first.HasMore)
		require.NotEmpty(t, first.Next)
		require.Len(t, executor.queries, 2)
		require.Equal(t, int64(3), executor.rows.Load()-int64(len(first.Values[0].Tasks.Values))-int64(len(first.Values[1].Tasks.Values)))
		for _, graph := range first.Values {
			require.NotEmpty(t, graph.Tasks.Values)
			for _, task := range graph.Tasks.Values {
				require.LessOrEqual(t, task.ID, int64(4))
			}
		}
		childArgs := executor.queries[1].Args()
		require.Contains(t, childArgs, int64(1))
		require.Contains(t, childArgs, int64(2))
		require.NotContains(t, childArgs, int64(3))

		secondEventOffset := len(snapshotEvents())
		second, err := rasql.PageGraphAfter(t.Context(), observed, plan, spec, rasql.PagePolicy{DefaultLimit: 2, MaxLimit: 2}, rasql.PageRequest{Limit: 2, After: first.Next})
		require.NoError(t, err)
		require.Equal(t, []int64{3}, []int64{second.Values[0].ID})
		require.False(t, second.HasMore)
		require.Len(t, executor.queries, 4)
		require.Contains(t, executor.queries[3].Args(), int64(3))
		secondEvents := snapshotEvents()[secondEventOffset:]
		var secondGraphTerminal, secondRootStatementTerminal int
		for _, event := range secondEvents {
			switch {
			case event.Kind == rasql.EventGraph && event.Phase == rasql.EventTerminal:
				secondGraphTerminal++
				require.False(t, event.EarlyClose)
			case event.Kind == rasql.EventStatement && event.Phase == rasql.EventTerminal && event.StatementIndex == 0:
				secondRootStatementTerminal++
				require.False(t, event.EarlyClose)
			}
		}
		require.Equal(t, 1, secondGraphTerminal)
		require.Equal(t, 1, secondRootStatementTerminal)

		queryCount := len(executor.queries)
		_, err = rasql.PageGraphAfter(t.Context(), observed, plan, spec, rasql.PagePolicy{DefaultLimit: 2, MaxLimit: 2}, rasql.PageRequest{Limit: 2, After: rasql.Cursor("invalid")})
		require.ErrorIs(t, err, rasql.ErrInvalidCursor)
		require.Len(t, executor.queries, queryCount)

		extractErr := errors.New("page key extraction")
		badKey := rasql.Q1FailingPageKey[pageParentRow](rootQuery, extractErr)
		badSpec, err := rasql.NewPageSpec([]rasql.PageKey[pageParentRow]{badKey}, badKey)
		require.NoError(t, err)
		attachmentCount := attachments.Load()
		extractEventOffset := len(snapshotEvents())
		_, err = rasql.PageGraphAfter(t.Context(), observed, plan, badSpec, rasql.PagePolicy{DefaultLimit: 2, MaxLimit: 2}, rasql.PageRequest{Limit: 2})
		require.ErrorIs(t, err, extractErr)
		require.Len(t, executor.queries, queryCount+1)
		require.Equal(t, attachmentCount, attachments.Load())
		extractEvents := snapshotEvents()[extractEventOffset:]
		var extractGraphTerminal, extractRootStatementTerminal int
		for _, event := range extractEvents {
			switch {
			case event.Kind == rasql.EventGraph && event.Phase == rasql.EventTerminal:
				extractGraphTerminal++
				require.Equal(t, int64(3), event.Rows)
				require.True(t, event.EarlyClose)
				require.ErrorIs(t, event.Err, extractErr)
			case event.Kind == rasql.EventStatement && event.Phase == rasql.EventTerminal && event.StatementIndex == 0:
				extractRootStatementTerminal++
				require.Equal(t, int64(3), event.Rows)
				require.True(t, event.EarlyClose)
			}
		}
		require.Equal(t, 1, extractGraphTerminal)
		require.Equal(t, 1, extractRootStatementTerminal)
	})

	t.Run("one logical graph event carries the lookahead rows", func(t *testing.T) {
		base, rootQuery, rootKey, taskKey, _, _, _, _, _, _, _, tasks, _, _ := pageFixture(t)
		counting := &pageCountingExecutor{Executor: base}
		countingProfiled := pageProfiled(t, counting)
		var mapped atomic.Int64
		rootGraphEdge, err := rasql.HasMany("tasks", rootKey, taskKey, tasks, rasql.EdgeOptions{BindLimit: 1000}, func(parent *pageRootGraph, loaded rasql.LoadedMany[pageTaskGraph]) {
			parent.Tasks = loaded
		})
		require.NoError(t, err)
		plan, err := rasql.NewGraphPlan(rootQuery, func(row pageParentRow) pageRootGraph {
			mapped.Add(1)
			return pageRootGraph{ID: row.ID}
		}, rootGraphEdge)
		require.NoError(t, err)
		rootExpr := rasql.Q1ProjectedExpr[pageParentRow, int64](rootQuery, 0)
		pageKey := rasql.AscKey(rootExpr, func(row pageParentRow) int64 { return row.ID })
		spec, err := rasql.NewPageSpec([]rasql.PageKey[pageParentRow]{pageKey}, pageKey)
		require.NoError(t, err)

		var mu sync.Mutex
		var events []rasql.Event
		observed, err := rasql.WithEventObservers(countingProfiled, rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.EventObserverFunc(func(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
			return ctx, rasql.EventCompletionFunc(func(_ context.Context, terminal rasql.Event) error {
				mu.Lock()
				events = append(events, terminal)
				mu.Unlock()
				return nil
			})
		}))
		require.NoError(t, err)
		page, err := rasql.PageGraphAfter(t.Context(), observed, plan, spec, rasql.PagePolicy{DefaultLimit: 2, MaxLimit: 2}, rasql.PageRequest{Limit: 2})
		require.NoError(t, err)
		require.Len(t, page.Values, 2)
		require.Equal(t, int64(2), mapped.Load())

		mu.Lock()
		defer mu.Unlock()
		var graphStarts, graphTerminals, rootStatementTerminals int
		for _, event := range events {
			switch {
			case event.Kind == rasql.EventGraph && event.Phase == rasql.EventStart:
				graphStarts++
			case event.Kind == rasql.EventGraph && event.Phase == rasql.EventTerminal:
				graphTerminals++
				require.Equal(t, int64(7), event.Rows)
				require.True(t, event.EarlyClose)
			case event.Kind == rasql.EventStatement && event.Phase == rasql.EventTerminal && event.StatementIndex == 0:
				rootStatementTerminals++
				require.Equal(t, int64(3), event.Rows)
				require.True(t, event.EarlyClose)
			}
		}
		require.Equal(t, 1, graphStarts)
		require.Equal(t, 1, graphTerminals)
		require.Equal(t, 1, rootStatementTerminals)
	})

	t.Run("a mapper panic reports no logical early close", func(t *testing.T) {
		base, rootQuery, _, _, _, _, _, _, _, _, _, _, _, _ := pageFixture(t)
		counting := &pageCountingExecutor{Executor: base}
		countingProfiled := pageProfiled(t, counting)
		plan, err := rasql.NewGraphPlan(rootQuery, func(pageParentRow) pageRootGraph {
			panic("mapper panic")
		})
		require.NoError(t, err)
		rootExpr := rasql.Q1ProjectedExpr[pageParentRow, int64](rootQuery, 0)
		pageKey := rasql.AscKey(rootExpr, func(row pageParentRow) int64 { return row.ID })
		spec, err := rasql.NewPageSpec([]rasql.PageKey[pageParentRow]{pageKey}, pageKey)
		require.NoError(t, err)

		var mu sync.Mutex
		var events []rasql.Event
		observed, err := rasql.WithEventObservers(countingProfiled, rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.EventObserverFunc(func(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
			return ctx, rasql.EventCompletionFunc(func(_ context.Context, terminal rasql.Event) error {
				mu.Lock()
				events = append(events, terminal)
				mu.Unlock()
				return nil
			})
		}))
		require.NoError(t, err)
		require.Panics(t, func() {
			_, _ = rasql.PageGraphAfter(t.Context(), observed, plan, spec, rasql.PagePolicy{DefaultLimit: 2, MaxLimit: 2}, rasql.PageRequest{Limit: 2})
		})

		mu.Lock()
		defer mu.Unlock()
		var graphTerminals int
		for _, event := range events {
			if event.Kind == rasql.EventGraph && event.Phase == rasql.EventTerminal {
				graphTerminals++
				require.Equal(t, int64(1), event.Rows)
				require.False(t, event.EarlyClose)
			}
		}
		require.Equal(t, 1, graphTerminals)
	})

	t.Run("a page preserves its root and loads nested relations", func(t *testing.T) {
		base, rootQuery, rootKey, taskKey, taskIDKey, taskAssigneeKey, assigneeKey, labelKey, junctionParent, junctionChild, junction, tasks, assignees, labels := pageFixture(t)
		expected, err := rasql.All(t.Context(), base, rootQuery)
		require.NoError(t, err)
		executor := &pageCountingExecutor{Executor: base}
		executorProfiled := pageProfiled(t, executor)
		assigneeEdge, err := rasql.HasOne("assignee", taskAssigneeKey, assigneeKey, assignees, rasql.EdgeOptions{BindLimit: 1000}, func(task *pageTaskGraph, loaded rasql.LoadedOne[pageAssigneeGraph]) { task.Assignee = loaded })
		require.NoError(t, err)
		labelEdge, err := rasql.ManyThrough("labels", taskIDKey, junctionParent, junctionChild, labelKey, junction, labels, rasql.EdgeOptions{Order: pageJunctionOrder(junction), BindLimit: 1000}, func(task *pageTaskGraph, loaded rasql.LoadedMany[pageLabelGraph]) { task.Labels = loaded })
		require.NoError(t, err)
		taskNode, err := rasql.NewGraphPlan(tasksQuery(tasks), func(row pageTaskRow) pageTaskGraph { return pageTaskGraph{ID: row.ID} }, assigneeEdge, labelEdge)
		require.NoError(t, err)
		_ = taskNode
		rootEdge, err := rasql.HasMany("tasks", rootKey, taskKey, taskNode, rasql.EdgeOptions{BindLimit: 1000}, func(parent *pageRootGraph, loaded rasql.LoadedMany[pageTaskGraph]) { parent.Tasks = loaded })
		require.NoError(t, err)
		plan, err := rasql.NewGraphPlan(rootQuery, func(row pageParentRow) pageRootGraph { return pageRootGraph{ID: row.ID} }, rootEdge)
		require.NoError(t, err)
		values, err := rasql.LoadGraph(t.Context(), executorProfiled, plan)
		require.NoError(t, err)
		require.Len(t, values, 50)
		for i, value := range values {
			require.Equal(t, expected[i].ID, value.ID)
			require.Len(t, value.Tasks.Values, 2)
			for _, task := range value.Tasks.Values {
				require.True(t, task.Assignee.Loaded)
				require.True(t, task.Assignee.Present)
				require.NotNil(t, task.Assignee.Value)
				require.Len(t, task.Labels.Values, 2)
			}
		}
		require.Equal(t, int64(1), values[0].Tasks.Values[0].Assignee.Value.ID)
		require.Equal(t, int64(1), values[1].Tasks.Values[1].Assignee.Value.ID)
		firstAssignee := values[0].Tasks.Values[0].Assignee.Value
		secondAssignee := values[1].Tasks.Values[1].Assignee.Value
		require.NotSame(t, firstAssignee, secondAssignee)
		firstAssignee.Name = "changed"
		require.Equal(t, "assignee", secondAssignee.Name)
		firstLabel := &values[0].Tasks.Values[0].Labels.Values[1]
		secondLabel := &values[0].Tasks.Values[1].Labels.Values[0]
		require.Equal(t, int64(2), firstLabel.ID)
		require.Equal(t, int64(2), secondLabel.ID)
		require.NotSame(t, firstLabel, secondLabel)
		firstLabel.Name = "changed"
		require.Equal(t, "label", secondLabel.Name)
		require.Equal(t, int64(5), executor.statements.Load())
		require.Equal(t, int64(357), executor.rows.Load())
	})
}

type pageCountingRows struct {
	rasql.ResultRows
	rows *atomic.Int64
}

func (r *pageCountingRows) Next() bool {
	if !r.ResultRows.Next() {
		return false
	}
	r.rows.Add(1)
	return true
}

func tasksQuery(plan rasql.GraphPlan[pageTaskRow, pageTaskGraph]) rasql.Query[pageTaskRow] {
	return rasql.Q1GraphChildQuery[pageTaskRow, pageTaskGraph, pageTaskRow, pageTaskGraph](plan)
}

func pageFixture(t *testing.T) (rasql.Executor, rasql.Query[pageParentRow], rasql.GraphKey[pageParentRow], rasql.GraphKey[pageTaskRow], rasql.GraphKey[pageTaskRow], rasql.GraphKey[pageTaskRow], rasql.GraphKey[pageAssigneeRow], rasql.GraphKey[pageLabelRow], rasql.GraphKey[pageJunctionRow], rasql.GraphKey[pageJunctionRow], rasql.Source, rasql.GraphPlan[pageTaskRow, pageTaskGraph], rasql.GraphPlan[pageAssigneeRow, pageAssigneeGraph], rasql.GraphPlan[pageLabelRow, pageLabelGraph]) {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = database.Close() })
	_, err = database.Exec(`CREATE TABLE page_parents (id INTEGER PRIMARY KEY); CREATE TABLE page_tasks (id INTEGER PRIMARY KEY, parent INTEGER NOT NULL, assignee INTEGER NOT NULL); CREATE TABLE page_assignees (id INTEGER PRIMARY KEY, name TEXT NOT NULL); CREATE TABLE page_labels (id INTEGER PRIMARY KEY, name TEXT NOT NULL); CREATE TABLE page_task_labels (id INTEGER PRIMARY KEY, task INTEGER NOT NULL, label INTEGER NOT NULL, rank INTEGER NOT NULL);`)
	require.NoError(t, err)
	for i := 1; i <= 50; i++ {
		_, err = database.Exec(`INSERT INTO page_parents VALUES (?)`, i)
		require.NoError(t, err)
	}
	for i := 1; i <= 100; i++ {
		_, err = database.Exec(`INSERT INTO page_tasks VALUES (?, ?, ?)`, i, (i-1)/2+1, (i-1)%3+1)
		require.NoError(t, err)
	}
	for i := 1; i <= 3; i++ {
		_, err = database.Exec(`INSERT INTO page_assignees VALUES (?, ?)`, i, "assignee")
		require.NoError(t, err)
	}
	for i := 1; i <= 4; i++ {
		_, err = database.Exec(`INSERT INTO page_labels VALUES (?, ?)`, i, "label")
		require.NoError(t, err)
	}
	for i := 1; i <= 100; i++ {
		_, err = database.Exec(`INSERT INTO page_task_labels VALUES (?, ?, ?, ?), (?, ?, ?, ?)`, i*2-1, i, (i-1)%4+1, 1, i*2, i, i%4+1, 2)
		require.NoError(t, err)
	}
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	parentTable := rasql.MustReadTableOf[pageParentRow](schema.TableDef{Name: "page_parents", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}, PrimaryKey: []string{"id"}})
	taskTable := rasql.MustReadTableOf[pageTaskRow](schema.TableDef{Name: "page_tasks", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "parent", Type: schema.IntegerType{}}, {Name: "assignee", Type: schema.IntegerType{}}}, PrimaryKey: []string{"id"}})
	assigneeTable := rasql.MustReadTableOf[pageAssigneeRow](schema.TableDef{Name: "page_assignees", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "name", Type: schema.TextType{}}}, PrimaryKey: []string{"id"}})
	labelTable := rasql.MustReadTableOf[pageLabelRow](schema.TableDef{Name: "page_labels", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "name", Type: schema.TextType{}}}, PrimaryKey: []string{"id"}})
	junctionTable := rasql.MustReadTableOf[pageJunctionRow](schema.TableDef{Name: "page_task_labels", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "task", Type: schema.IntegerType{}}, {Name: "label", Type: schema.IntegerType{}}, {Name: "rank", Type: schema.IntegerType{}}}, PrimaryKey: []string{"id"}})
	parents, err := rasql.SourceOf(parentTable, "p")
	require.NoError(t, err)
	tasksSource, err := rasql.SourceOf(taskTable, "t")
	require.NoError(t, err)
	assigneesSource, err := rasql.SourceOf(assigneeTable, "a")
	require.NoError(t, err)
	labelsSource, err := rasql.SourceOf(labelTable, "l")
	require.NoError(t, err)
	junctionSource, err := rasql.SourceOf(junctionTable, "j")
	require.NoError(t, err)
	pid, err := rasql.BindColumn[pageParentRow, int64](parents, "id", "")
	require.NoError(t, err)
	tid, err := rasql.BindColumn[pageTaskRow, int64](tasksSource, "id", "")
	require.NoError(t, err)
	tparent, err := rasql.BindColumn[pageTaskRow, int64](tasksSource, "parent", "")
	require.NoError(t, err)
	tassignee, err := rasql.BindColumn[pageTaskRow, int64](tasksSource, "assignee", "")
	require.NoError(t, err)
	aid, err := rasql.BindColumn[pageAssigneeRow, int64](assigneesSource, "id", "")
	require.NoError(t, err)
	aname, err := rasql.BindColumn[pageAssigneeRow, string](assigneesSource, "name", "")
	require.NoError(t, err)
	lid, err := rasql.BindColumn[pageLabelRow, int64](labelsSource, "id", "")
	require.NoError(t, err)
	lname, err := rasql.BindColumn[pageLabelRow, string](labelsSource, "name", "")
	require.NoError(t, err)
	jtask, err := rasql.BindColumn[pageJunctionRow, int64](junctionSource, "task", "")
	require.NoError(t, err)
	jlabel, err := rasql.BindColumn[pageJunctionRow, int64](junctionSource, "label", "")
	require.NoError(t, err)
	parentSchema, _ := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	parentProjection, _ := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", pid.Expr(), schema.IntegerType{}, "")}, pageParentDecoder{schema: parentSchema})
	taskSchema, _ := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}}, rasql.ResultColumn{Name: "parent", Type: schema.IntegerType{}}, rasql.ResultColumn{Name: "assignee", Type: schema.IntegerType{}})
	taskProjection, _ := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", tid.Expr(), schema.IntegerType{}, ""), rasql.Item("parent", tparent.Expr(), schema.IntegerType{}, ""), rasql.Item("assignee", tassignee.Expr(), schema.IntegerType{}, "")}, pageTaskDecoder{schema: taskSchema})
	aSchema, _ := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}}, rasql.ResultColumn{Name: "name", Type: schema.TextType{}})
	aProjection, _ := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", aid.Expr(), schema.IntegerType{}, ""), rasql.Item("name", aname.Expr(), schema.TextType{}, "")}, pageAssigneeDecoder{schema: aSchema})
	lSchema, _ := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}}, rasql.ResultColumn{Name: "name", Type: schema.TextType{}})
	lProjection, _ := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", lid.Expr(), schema.IntegerType{}, ""), rasql.Item("name", lname.Expr(), schema.TextType{}, "")}, pageLabelDecoder{schema: lSchema})
	parentQuery := rasql.Select(parents.Source(), parentProjection).OrderBy(rasql.AscExpr(pid.Expr()))
	taskQuery := rasql.Select(tasksSource.Source(), taskProjection).OrderBy(rasql.AscExpr(tparent.Expr()), rasql.AscExpr(tid.Expr()))
	assigneeQuery := rasql.Select(assigneesSource.Source(), aProjection).OrderBy(rasql.AscExpr(aid.Expr()))
	labelQuery := rasql.Select(labelsSource.Source(), lProjection).OrderBy(rasql.AscExpr(lid.Expr()))
	parentKey, _ := rasql.NewGraphKey(rasql.KeyPart(pid, func(v pageParentRow) int64 { return v.ID }))
	taskParentKey, _ := rasql.NewGraphKey(rasql.KeyPart(tparent, func(v pageTaskRow) int64 { return v.Parent }))
	taskIDKey, _ := rasql.NewGraphKey(rasql.KeyPart(tid, func(v pageTaskRow) int64 { return v.ID }))
	taskAssigneeKey, _ := rasql.NewGraphKey(rasql.KeyPart(tassignee, func(v pageTaskRow) int64 { return v.Assignee }))
	assigneeIDKey, _ := rasql.NewGraphKey(rasql.KeyPart(aid, func(v pageAssigneeRow) int64 { return v.ID }))
	labelIDKey, _ := rasql.NewGraphKey(rasql.KeyPart(lid, func(v pageLabelRow) int64 { return v.ID }))
	junctionParent, _ := rasql.NewGraphKey(rasql.KeyPart(jtask, func(v pageJunctionRow) int64 { return v.Task }))
	junctionChild, _ := rasql.NewGraphKey(rasql.KeyPart(jlabel, func(v pageJunctionRow) int64 { return v.Label }))
	tasksPlan, _ := rasql.NewGraphPlan(taskQuery, func(v pageTaskRow) pageTaskGraph { return pageTaskGraph{ID: v.ID} })
	assigneesPlan, _ := rasql.NewGraphPlan(assigneeQuery, func(v pageAssigneeRow) pageAssigneeGraph { return pageAssigneeGraph(v) })
	labelsPlan, _ := rasql.NewGraphPlan(labelQuery, func(v pageLabelRow) pageLabelGraph { return pageLabelGraph(v) })
	return executor, parentQuery, parentKey, taskParentKey, taskIDKey, taskAssigneeKey, assigneeIDKey, labelIDKey, junctionParent, junctionChild, junctionSource.Source(), tasksPlan, assigneesPlan, labelsPlan
}

type pageJunctionRow struct{ ID, Task, Label int64 }

func pageJunctionOrder(source rasql.Source) []rasql.OrderTerm {
	relation := rasql.Q1TypedRelation[pageJunctionRow](source)
	task, _ := rasql.BindColumn[pageJunctionRow, int64](relation, "task", "")
	id, _ := rasql.BindColumn[pageJunctionRow, int64](relation, "id", "")
	return []rasql.OrderTerm{rasql.AscExpr(task.Expr()), rasql.AscExpr(id.Expr())}
}

// pageProfiled re-attaches the compiler a decorator does not carry, which is
// how a counting wrapper sits in the chain from outside the package.
func pageProfiled(t *testing.T, executor rasql.Executor) rasql.Executor {
	t.Helper()
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	profiled, err := rasql.WithEngineProfile(executor, profile)
	require.NoError(t, err)
	return profiled
}
