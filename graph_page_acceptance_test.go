package rasql

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/querycompile"
	"github.com/lestrrat-go/rasql/query"
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
	Tasks LoadedMany[pageTaskGraph]
}
type pageTaskGraph struct {
	ID       int64
	Assignee LoadedOne[pageAssigneeGraph]
	Labels   LoadedMany[pageLabelGraph]
}
type pageAssigneeGraph struct {
	ID   int64
	Name string
}
type pageLabelGraph struct {
	ID   int64
	Name string
}

type pageParentDecoder struct{ schema ResultSchema }

func (d pageParentDecoder) ResultSchema() ResultSchema                   { return d.schema }
func (pageParentDecoder) Presence() []Presence                           { return nil }
func (pageParentDecoder) DecodeRow(s ScanSource, v *pageParentRow) error { return s.Scan(&v.ID) }

type pageTaskDecoder struct{ schema ResultSchema }

func (d pageTaskDecoder) ResultSchema() ResultSchema { return d.schema }
func (pageTaskDecoder) Presence() []Presence         { return nil }
func (pageTaskDecoder) DecodeRow(s ScanSource, v *pageTaskRow) error {
	return s.Scan(&v.ID, &v.Parent, &v.Assignee)
}

type pageAssigneeDecoder struct{ schema ResultSchema }

func (d pageAssigneeDecoder) ResultSchema() ResultSchema { return d.schema }
func (pageAssigneeDecoder) Presence() []Presence         { return nil }
func (pageAssigneeDecoder) DecodeRow(s ScanSource, v *pageAssigneeRow) error {
	return s.Scan(&v.ID, &v.Name)
}

type pageLabelDecoder struct{ schema ResultSchema }

func (d pageLabelDecoder) ResultSchema() ResultSchema                  { return d.schema }
func (pageLabelDecoder) Presence() []Presence                          { return nil }
func (pageLabelDecoder) DecodeRow(s ScanSource, v *pageLabelRow) error { return s.Scan(&v.ID, &v.Name) }

type pageCountingExecutor struct {
	Executor
	compiler   *querycompile.Compiler
	statements atomic.Int64
	rows       atomic.Int64
	queries    []stmt.Statement
}

func (e *pageCountingExecutor) queryCompiler() *querycompile.Compiler { return e.compiler }
func (e *pageCountingExecutor) Query(ctx context.Context, s stmt.Statement) (ResultRows, error) {
	e.queries = append(e.queries, s)
	rows, err := e.Executor.Query(ctx, s)
	if err != nil {
		return nil, err
	}
	e.statements.Add(1)
	return &pageCountingRows{ResultRows: rows, rows: &e.rows}, nil
}

func TestPageGraphAfterSQLiteUsesOneRootReadAndRetainedRoots(t *testing.T) {
	base, rootQuery, rootKey, taskKey, _, _, _, _, _, _, _, tasks, _, _ := pageFixture(t)
	provider, ok := base.(compilerProvider)
	require.True(t, ok)
	executor := &pageCountingExecutor{Executor: base, compiler: provider.queryCompiler()}
	rootExpr := Expr[int64]{node: rootQuery.plan.projection[0].expression, source: rootQuery.plan.projection[0].source}
	rootQuery = rootQuery.Where(Predicate{node: query.LessThan(rootExpr.node, Value(int64(4)).node)})
	rootGraphEdge, err := HasMany("tasks", rootKey, taskKey, tasks, EdgeOptions{BindLimit: 1000}, func(parent *pageRootGraph, loaded LoadedMany[pageTaskGraph]) {
		parent.Tasks = loaded
	})
	require.NoError(t, err)
	plan, err := NewGraphPlan(rootQuery, func(row pageParentRow) pageRootGraph { return pageRootGraph{ID: row.ID} }, rootGraphEdge)
	require.NoError(t, err)
	rootPageKey := AscKey(rootExpr, func(row pageParentRow) int64 { return row.ID })
	spec, err := NewPageSpec([]PageKey[pageParentRow]{rootPageKey}, rootPageKey)
	require.NoError(t, err)

	first, err := PageGraphAfter(t.Context(), executor, plan, spec, PagePolicy{DefaultLimit: 2, MaxLimit: 2}, PageRequest{Limit: 2})
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

	second, err := PageGraphAfter(t.Context(), executor, plan, spec, PagePolicy{DefaultLimit: 2, MaxLimit: 2}, PageRequest{Limit: 2, After: first.Next})
	require.NoError(t, err)
	require.Equal(t, []int64{3}, []int64{second.Values[0].ID})
	require.False(t, second.HasMore)
	require.Len(t, executor.queries, 4)
	require.Contains(t, executor.queries[3].Args(), int64(3))

	queryCount := len(executor.queries)
	_, err = PageGraphAfter(t.Context(), executor, plan, spec, PagePolicy{DefaultLimit: 2, MaxLimit: 2}, PageRequest{Limit: 2, After: Cursor("invalid")})
	require.ErrorIs(t, err, ErrInvalidCursor)
	require.Len(t, executor.queries, queryCount)

	extractErr := errors.New("page key extraction")
	badKey := &pageKey[pageParentRow]{term: rootQuery.plan.order[0], direction: PageAscending, extract: func(pageParentRow) (bool, any, error) {
		return false, nil, extractErr
	}}
	badSpec, err := NewPageSpec([]PageKey[pageParentRow]{badKey}, badKey)
	require.NoError(t, err)
	_, err = PageGraphAfter(t.Context(), executor, plan, badSpec, PagePolicy{DefaultLimit: 2, MaxLimit: 2}, PageRequest{Limit: 2})
	require.ErrorIs(t, err, extractErr)
	require.Len(t, executor.queries, queryCount+1)
}

func TestPageGraphAfterEmitsOneLogicalGraphEventWithLookaheadRows(t *testing.T) {
	base, rootQuery, rootKey, taskKey, _, _, _, _, _, _, _, tasks, _, _ := pageFixture(t)
	provider, ok := base.(compilerProvider)
	require.True(t, ok)
	counting := &pageCountingExecutor{Executor: base, compiler: provider.queryCompiler()}
	var mapped atomic.Int64
	rootGraphEdge, err := HasMany("tasks", rootKey, taskKey, tasks, EdgeOptions{BindLimit: 1000}, func(parent *pageRootGraph, loaded LoadedMany[pageTaskGraph]) {
		parent.Tasks = loaded
	})
	require.NoError(t, err)
	plan, err := NewGraphPlan(rootQuery, func(row pageParentRow) pageRootGraph {
		mapped.Add(1)
		return pageRootGraph{ID: row.ID}
	}, rootGraphEdge)
	require.NoError(t, err)
	rootExpr := Expr[int64]{node: rootQuery.plan.projection[0].expression, source: rootQuery.plan.projection[0].source}
	pageKey := AscKey(rootExpr, func(row pageParentRow) int64 { return row.ID })
	spec, err := NewPageSpec([]PageKey[pageParentRow]{pageKey}, pageKey)
	require.NoError(t, err)

	var mu sync.Mutex
	var events []Event
	observed, err := WithEventObservers(counting, ExtensionErrorHandlerFunc(func(context.Context, ExtensionError) {}), EventObserverFunc(func(ctx context.Context, event Event) (context.Context, EventCompletion) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		return ctx, EventCompletionFunc(func(_ context.Context, terminal Event) error {
			mu.Lock()
			events = append(events, terminal)
			mu.Unlock()
			return nil
		})
	}))
	require.NoError(t, err)
	page, err := PageGraphAfter(t.Context(), observed, plan, spec, PagePolicy{DefaultLimit: 2, MaxLimit: 2}, PageRequest{Limit: 2})
	require.NoError(t, err)
	require.Len(t, page.Values, 2)
	require.Equal(t, int64(2), mapped.Load())

	mu.Lock()
	defer mu.Unlock()
	var graphStarts, graphTerminals int
	for _, event := range events {
		if event.Kind != EventGraph {
			continue
		}
		if event.Phase == EventStart {
			graphStarts++
		}
		if event.Phase == EventTerminal {
			graphTerminals++
			require.Equal(t, int64(7), event.Rows)
		}
	}
	require.Equal(t, 1, graphStarts)
	require.Equal(t, 1, graphTerminals)
}

type pageCountingRows struct {
	ResultRows
	rows *atomic.Int64
}

func (r *pageCountingRows) Next() bool {
	if !r.ResultRows.Next() {
		return false
	}
	r.rows.Add(1)
	return true
}

func TestGraphSQLitePagePreservesRootAndLoadsNestedRelations(t *testing.T) {
	base, rootQuery, rootKey, taskKey, taskIDKey, taskAssigneeKey, assigneeKey, labelKey, junctionParent, junctionChild, junction, tasks, assignees, labels := pageFixture(t)
	expected, err := All(t.Context(), base, rootQuery)
	require.NoError(t, err)
	provider, ok := base.(compilerProvider)
	require.True(t, ok)
	executor := &pageCountingExecutor{Executor: base, compiler: provider.queryCompiler()}
	assigneeEdge, err := HasOne("assignee", taskAssigneeKey, assigneeKey, assignees, EdgeOptions{BindLimit: 1000}, func(task *pageTaskGraph, loaded LoadedOne[pageAssigneeGraph]) { task.Assignee = loaded })
	require.NoError(t, err)
	labelEdge, err := ManyThrough("labels", taskIDKey, junctionParent, junctionChild, labelKey, junction, labels, EdgeOptions{Order: pageJunctionOrder(junction), BindLimit: 1000}, func(task *pageTaskGraph, loaded LoadedMany[pageLabelGraph]) { task.Labels = loaded })
	require.NoError(t, err)
	taskNode, err := NewGraphPlan(tasksQuery(tasks), func(row pageTaskRow) pageTaskGraph { return pageTaskGraph{ID: row.ID} }, assigneeEdge, labelEdge)
	require.NoError(t, err)
	_ = taskNode
	rootEdge, err := HasMany("tasks", rootKey, taskKey, taskNode, EdgeOptions{BindLimit: 1000}, func(parent *pageRootGraph, loaded LoadedMany[pageTaskGraph]) { parent.Tasks = loaded })
	require.NoError(t, err)
	plan, err := NewGraphPlan(rootQuery, func(row pageParentRow) pageRootGraph { return pageRootGraph{ID: row.ID} }, rootEdge)
	require.NoError(t, err)
	values, err := LoadGraph(t.Context(), executor, plan)
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
}

func tasksQuery(plan GraphPlan[pageTaskRow, pageTaskGraph]) Query[pageTaskRow] {
	return plan.node.query.(graphQuery[pageTaskRow, pageTaskGraph]).value
}

func pageFixture(t *testing.T) (Executor, Query[pageParentRow], GraphKey[pageParentRow], GraphKey[pageTaskRow], GraphKey[pageTaskRow], GraphKey[pageTaskRow], GraphKey[pageAssigneeRow], GraphKey[pageLabelRow], GraphKey[pageJunctionRow], GraphKey[pageJunctionRow], Source, GraphPlan[pageTaskRow, pageTaskGraph], GraphPlan[pageAssigneeRow, pageAssigneeGraph], GraphPlan[pageLabelRow, pageLabelGraph]) {
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
	db, err := New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := AsExecutor(db, profile)
	require.NoError(t, err)
	parentTable := MustReadTableOf[pageParentRow](schema.TableDef{Name: "page_parents", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}, PrimaryKey: []string{"id"}})
	taskTable := MustReadTableOf[pageTaskRow](schema.TableDef{Name: "page_tasks", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "parent", Type: schema.IntegerType{}}, {Name: "assignee", Type: schema.IntegerType{}}}, PrimaryKey: []string{"id"}})
	assigneeTable := MustReadTableOf[pageAssigneeRow](schema.TableDef{Name: "page_assignees", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "name", Type: schema.TextType{}}}, PrimaryKey: []string{"id"}})
	labelTable := MustReadTableOf[pageLabelRow](schema.TableDef{Name: "page_labels", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "name", Type: schema.TextType{}}}, PrimaryKey: []string{"id"}})
	junctionTable := MustReadTableOf[pageJunctionRow](schema.TableDef{Name: "page_task_labels", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "task", Type: schema.IntegerType{}}, {Name: "label", Type: schema.IntegerType{}}, {Name: "rank", Type: schema.IntegerType{}}}, PrimaryKey: []string{"id"}})
	parents, err := SourceOf(parentTable, "p")
	require.NoError(t, err)
	tasksSource, err := SourceOf(taskTable, "t")
	require.NoError(t, err)
	assigneesSource, err := SourceOf(assigneeTable, "a")
	require.NoError(t, err)
	labelsSource, err := SourceOf(labelTable, "l")
	require.NoError(t, err)
	junctionSource, err := SourceOf(junctionTable, "j")
	require.NoError(t, err)
	pid, err := BindColumn[pageParentRow, int64](parents, "id", "")
	require.NoError(t, err)
	tid, err := BindColumn[pageTaskRow, int64](tasksSource, "id", "")
	require.NoError(t, err)
	tparent, err := BindColumn[pageTaskRow, int64](tasksSource, "parent", "")
	require.NoError(t, err)
	tassignee, err := BindColumn[pageTaskRow, int64](tasksSource, "assignee", "")
	require.NoError(t, err)
	aid, err := BindColumn[pageAssigneeRow, int64](assigneesSource, "id", "")
	require.NoError(t, err)
	aname, err := BindColumn[pageAssigneeRow, string](assigneesSource, "name", "")
	require.NoError(t, err)
	lid, err := BindColumn[pageLabelRow, int64](labelsSource, "id", "")
	require.NoError(t, err)
	lname, err := BindColumn[pageLabelRow, string](labelsSource, "name", "")
	require.NoError(t, err)
	jtask, err := BindColumn[pageJunctionRow, int64](junctionSource, "task", "")
	require.NoError(t, err)
	jlabel, err := BindColumn[pageJunctionRow, int64](junctionSource, "label", "")
	require.NoError(t, err)
	parentSchema, _ := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}})
	parentProjection, _ := NewProjection([]ProjectionItem{Item("id", pid.Expr(), schema.IntegerType{}, "")}, pageParentDecoder{schema: parentSchema})
	taskSchema, _ := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}}, ResultColumn{Name: "parent", Type: schema.IntegerType{}}, ResultColumn{Name: "assignee", Type: schema.IntegerType{}})
	taskProjection, _ := NewProjection([]ProjectionItem{Item("id", tid.Expr(), schema.IntegerType{}, ""), Item("parent", tparent.Expr(), schema.IntegerType{}, ""), Item("assignee", tassignee.Expr(), schema.IntegerType{}, "")}, pageTaskDecoder{schema: taskSchema})
	aSchema, _ := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}}, ResultColumn{Name: "name", Type: schema.TextType{}})
	aProjection, _ := NewProjection([]ProjectionItem{Item("id", aid.Expr(), schema.IntegerType{}, ""), Item("name", aname.Expr(), schema.TextType{}, "")}, pageAssigneeDecoder{schema: aSchema})
	lSchema, _ := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}}, ResultColumn{Name: "name", Type: schema.TextType{}})
	lProjection, _ := NewProjection([]ProjectionItem{Item("id", lid.Expr(), schema.IntegerType{}, ""), Item("name", lname.Expr(), schema.TextType{}, "")}, pageLabelDecoder{schema: lSchema})
	parentQuery := Select(parents.Source(), parentProjection).OrderBy(AscExpr(pid.Expr()))
	taskQuery := Select(tasksSource.Source(), taskProjection).OrderBy(AscExpr(tparent.Expr()), AscExpr(tid.Expr()))
	assigneeQuery := Select(assigneesSource.Source(), aProjection).OrderBy(AscExpr(aid.Expr()))
	labelQuery := Select(labelsSource.Source(), lProjection).OrderBy(AscExpr(lid.Expr()))
	parentKey, _ := NewGraphKey(KeyPart(pid, func(v pageParentRow) int64 { return v.ID }))
	taskParentKey, _ := NewGraphKey(KeyPart(tparent, func(v pageTaskRow) int64 { return v.Parent }))
	taskIDKey, _ := NewGraphKey(KeyPart(tid, func(v pageTaskRow) int64 { return v.ID }))
	taskAssigneeKey, _ := NewGraphKey(KeyPart(tassignee, func(v pageTaskRow) int64 { return v.Assignee }))
	assigneeIDKey, _ := NewGraphKey(KeyPart(aid, func(v pageAssigneeRow) int64 { return v.ID }))
	labelIDKey, _ := NewGraphKey(KeyPart(lid, func(v pageLabelRow) int64 { return v.ID }))
	junctionParent, _ := NewGraphKey(KeyPart(jtask, func(v pageJunctionRow) int64 { return v.Task }))
	junctionChild, _ := NewGraphKey(KeyPart(jlabel, func(v pageJunctionRow) int64 { return v.Label }))
	tasksPlan, _ := NewGraphPlan(taskQuery, func(v pageTaskRow) pageTaskGraph { return pageTaskGraph{ID: v.ID} })
	assigneesPlan, _ := NewGraphPlan(assigneeQuery, func(v pageAssigneeRow) pageAssigneeGraph { return pageAssigneeGraph(v) })
	labelsPlan, _ := NewGraphPlan(labelQuery, func(v pageLabelRow) pageLabelGraph { return pageLabelGraph(v) })
	return executor, parentQuery, parentKey, taskParentKey, taskIDKey, taskAssigneeKey, assigneeIDKey, labelIDKey, junctionParent, junctionChild, junctionSource.source, tasksPlan, assigneesPlan, labelsPlan
}

type pageJunctionRow struct{ ID, Task, Label int64 }

func pageJunctionOrder(source Source) []OrderTerm {
	relation := TypedRelation[pageJunctionRow]{source: source}
	task, _ := BindColumn[pageJunctionRow, int64](relation, "task", "")
	id, _ := BindColumn[pageJunctionRow, int64](relation, "id", "")
	return []OrderTerm{AscExpr(task.Expr()), AscExpr(id.Expr())}
}
