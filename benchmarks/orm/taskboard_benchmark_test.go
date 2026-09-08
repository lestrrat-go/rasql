package orm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/conformance"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite"
)

var (
	taskboardReportSink   []taskboardReportRow
	taskboardGraphSink    []taskboardGraphRow
	taskboardMutationSink taskboardMutationResult
	taskboardBatchSink    taskboardBatchResult
)

type taskboardDatabase struct {
	sql      *sql.DB
	db       rasql.DB
	profile  rasql.EngineProfile
	executor rasql.Executor
}

func taskboardExecutionInvocations(records []conformance.InvocationRecord, kind string) []conformance.InvocationRecord {
	result := make([]conformance.InvocationRecord, 0, len(records))
	for _, invocation := range records {
		if invocation.Kind == kind && invocation.Phase == "execution" {
			result = append(result, invocation)
		}
	}
	return result
}

type taskboardReportRow struct {
	TaskID     int64
	AssigneeID rasql.Nullable[int64]
	MemberName rasql.Nullable[string]
}

type taskboardGraphTask struct {
	ID       int64
	Title    string
	Assignee rasql.LoadedOne[taskboardMember]
}

type taskboardGraphProject struct {
	ID    int64
	Name  string
	Tasks rasql.LoadedMany[taskboardGraphTask]
}

type taskboardGraphRow struct {
	ID    int64
	Name  string
	Tasks []taskboardGraphTaskRow
}

type taskboardGraphTaskRow struct {
	ID              int64
	Title           string
	AssigneeID      rasql.Nullable[int64]
	AssigneeName    rasql.Nullable[string]
	AssigneeLoaded  bool
	AssigneePresent bool
}

type taskboardMutationResult struct {
	Created    int64
	Patched    int64
	RolledBack bool
}

type taskboardBatchResult struct {
	Affected      int64
	RolledBack    bool
	Inputs        []rasql.InputOutcome
	PartitionRows []int64
	Durability    rasql.Durability
	FailedBatch   []int
}

type taskboardGraphStage struct {
	SQL  string
	Args []any
}

type taskboardGraphSQLResult struct {
	Graph  []taskboardGraphRow
	Stages []taskboardGraphStage
}

type taskboardProject struct {
	ID   int64
	Name string
}

type taskboardTask struct {
	ID, ProjectID int64
	AssigneeID    rasql.Nullable[int64]
	Title         string
	IsOpen        bool
	DueOn         rasql.Nullable[time.Time]
	CreatedAt     time.Time
}

type taskboardMember struct {
	ID   int64
	Name string
}

type taskboardProjectsTable struct{ rasql.Table[taskboardProject] }
type taskboardTasksTable struct{ rasql.Table[taskboardTask] }
type taskboardMembersTable struct{ rasql.Table[taskboardMember] }

func (t taskboardProjectsTable) Source(alias string) (rasql.TypedRelation[taskboardProject], error) {
	return rasql.SourceOf(t.Table, alias)
}
func (t taskboardProjectsTable) Column(name string) query.ColumnRef { return t.Table.Column(name) }
func (t taskboardTasksTable) Source(alias string) (rasql.TypedRelation[taskboardTask], error) {
	return rasql.SourceOf(t.Table, alias)
}
func (t taskboardTasksTable) Column(name string) query.ColumnRef { return t.Table.Column(name) }
func (t taskboardMembersTable) Source(alias string) (rasql.TypedRelation[taskboardMember], error) {
	return rasql.SourceOf(t.Table, alias)
}
func (t taskboardMembersTable) Column(name string) query.ColumnRef { return t.Table.Column(name) }

var taskboardProjectTable = rasql.MustTableOf[taskboardProject](schema.TableDef{
	Name: "projects", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}}, {Name: "name", Type: schema.TextType{}},
	},
})
var taskboardTaskTable = rasql.MustTableOf[taskboardTask](schema.TableDef{
	Name: "tasks", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}}, {Name: "project_id", Type: schema.IntegerType{}},
		{Name: "assignee_id", Type: schema.IntegerType{}, Nullable: true}, {Name: "title", Type: schema.TextType{}},
		{Name: "is_open", Type: schema.BooleanType{}, Default: "TRUE"},
		{Name: "due_on", Type: schema.TimeType{}, Nullable: true}, {Name: "created_at", Type: schema.TimeType{}, Default: "'2024-01-01T00:00:00Z'"},
	},
})
var taskboardMemberTable = rasql.MustTableOf[taskboardMember](schema.TableDef{
	Name: "members", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}}, {Name: "name", Type: schema.TextType{}},
	},
})

func taskboardProjects() taskboardProjectsTable {
	return taskboardProjectsTable{Table: taskboardProjectTable}
}
func taskboardTasks() taskboardTasksTable { return taskboardTasksTable{Table: taskboardTaskTable} }
func taskboardMembers() taskboardMembersTable {
	return taskboardMembersTable{Table: taskboardMemberTable}
}

type taskboardProjectColumns struct {
	ID   rasql.Column[taskboardProject, int64]
	Name rasql.Column[taskboardProject, string]
}
type taskboardTaskColumns struct {
	ID, ProjectID rasql.Column[taskboardTask, int64]
	AssigneeID    rasql.NullColumn[taskboardTask, int64]
	Title         rasql.Column[taskboardTask, string]
	IsOpen        rasql.Column[taskboardTask, bool]
	DueOn         rasql.NullColumn[taskboardTask, time.Time]
	CreatedAt     rasql.Column[taskboardTask, time.Time]
}
type taskboardMemberColumns struct {
	ID   rasql.Column[taskboardMember, int64]
	Name rasql.Column[taskboardMember, string]
}

func (taskboardProjectColumns) Bind(source rasql.TypedRelation[taskboardProject]) (taskboardProjectColumns, error) {
	id, err := rasql.BindColumn[taskboardProject, int64](source, "id", "")
	if err != nil {
		return taskboardProjectColumns{}, err
	}
	name, err := rasql.BindColumn[taskboardProject, string](source, "name", "")
	return taskboardProjectColumns{ID: id, Name: name}, err
}
func (taskboardTaskColumns) Bind(source rasql.TypedRelation[taskboardTask]) (taskboardTaskColumns, error) {
	id, err := rasql.BindColumn[taskboardTask, int64](source, "id", "")
	if err != nil {
		return taskboardTaskColumns{}, err
	}
	projectID, err := rasql.BindColumn[taskboardTask, int64](source, "project_id", "")
	if err != nil {
		return taskboardTaskColumns{}, err
	}
	assigneeID, err := rasql.BindNullColumn[taskboardTask, int64](source, "assignee_id", "")
	if err != nil {
		return taskboardTaskColumns{}, err
	}
	title, err := rasql.BindColumn[taskboardTask, string](source, "title", "")
	if err != nil {
		return taskboardTaskColumns{}, err
	}
	isOpen, err := rasql.BindColumn[taskboardTask, bool](source, "is_open", "")
	if err != nil {
		return taskboardTaskColumns{}, err
	}
	createdAt, err := rasql.BindColumn[taskboardTask, time.Time](source, "created_at", "")
	if err != nil {
		return taskboardTaskColumns{}, err
	}
	dueOn, err := rasql.BindNullColumn[taskboardTask, time.Time](source, "due_on", "")
	return taskboardTaskColumns{ID: id, ProjectID: projectID, AssigneeID: assigneeID, Title: title, IsOpen: isOpen, DueOn: dueOn, CreatedAt: createdAt}, err
}
func (taskboardMemberColumns) Bind(source rasql.TypedRelation[taskboardMember]) (taskboardMemberColumns, error) {
	id, err := rasql.BindColumn[taskboardMember, int64](source, "id", "")
	if err != nil {
		return taskboardMemberColumns{}, err
	}
	name, err := rasql.BindColumn[taskboardMember, string](source, "name", "")
	return taskboardMemberColumns{ID: id, Name: name}, err
}

type taskboardProjectDecoder struct{}

func (taskboardProjectDecoder) ResultSchema() rasql.ResultSchema {
	value, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}}, rasql.ResultColumn{Name: "name", Type: schema.TextType{}})
	if err != nil {
		panic(err)
	}
	return value
}
func (taskboardProjectDecoder) Presence() []rasql.Presence { return nil }
func (taskboardProjectDecoder) DecodeRow(source rasql.ScanSource, row *taskboardProject) error {
	return source.Scan(&row.ID, &row.Name)
}

type taskboardTaskDecoder struct{}

func (taskboardTaskDecoder) ResultSchema() rasql.ResultSchema {
	value, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "project_id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "assignee_id", Type: schema.IntegerType{}, Nullable: true},
		rasql.ResultColumn{Name: "title", Type: schema.TextType{}},
		rasql.ResultColumn{Name: "is_open", Type: schema.BooleanType{}},
		rasql.ResultColumn{Name: "due_on", Type: schema.TimeType{}, Nullable: true},
		rasql.ResultColumn{Name: "created_at", Type: schema.TimeType{}},
	)
	if err != nil {
		panic(err)
	}
	return value
}
func (taskboardTaskDecoder) Presence() []rasql.Presence { return nil }
func (taskboardTaskDecoder) DecodeRow(source rasql.ScanSource, row *taskboardTask) error {
	return source.Scan(&row.ID, &row.ProjectID, &row.AssigneeID, &row.Title, &row.IsOpen, &row.DueOn, &row.CreatedAt)
}

type taskboardMemberDecoder struct{}

func (taskboardMemberDecoder) ResultSchema() rasql.ResultSchema {
	value, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}}, rasql.ResultColumn{Name: "name", Type: schema.TextType{}})
	if err != nil {
		panic(err)
	}
	return value
}
func (taskboardMemberDecoder) Presence() []rasql.Presence { return nil }
func (taskboardMemberDecoder) DecodeRow(source rasql.ScanSource, row *taskboardMember) error {
	return source.Scan(&row.ID, &row.Name)
}

func taskboardProjectProjection(value taskboardProjectColumns) (rasql.Projection[taskboardProject], error) {
	items := []rasql.ProjectionItem{
		rasql.Item("id", value.ID.Expr(), schema.IntegerType{}, ""),
		rasql.Item("name", value.Name.Expr(), schema.TextType{}, ""),
	}
	return rasql.NewProjection(items, taskboardProjectDecoder{})
}
func taskboardTaskProjection(value taskboardTaskColumns) (rasql.Projection[taskboardTask], error) {
	items := []rasql.ProjectionItem{
		rasql.Item("id", value.ID.Expr(), schema.IntegerType{}, ""),
		rasql.Item("project_id", value.ProjectID.Expr(), schema.IntegerType{}, ""),
		rasql.NullItem("assignee_id", value.AssigneeID.NullExpr(), schema.IntegerType{}, ""),
		rasql.Item("title", value.Title.Expr(), schema.TextType{}, ""),
		rasql.Item("is_open", value.IsOpen.Expr(), schema.BooleanType{}, ""),
		rasql.NullItem("due_on", value.DueOn.NullExpr(), schema.TimeType{}, ""),
		rasql.Item("created_at", value.CreatedAt.Expr(), schema.TimeType{}, ""),
	}
	return rasql.NewProjection(items, taskboardTaskDecoder{})
}
func taskboardMemberProjection(value taskboardMemberColumns) (rasql.Projection[taskboardMember], error) {
	return rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", value.ID.Expr(), schema.IntegerType{}, ""), rasql.Item("name", value.Name.Expr(), schema.TextType{}, "")}, taskboardMemberDecoder{})
}

func taskboardTaskAssigneeEdge(
	parent rasql.TypedRelation[taskboardTask], child rasql.TypedRelation[taskboardMember],
	plan rasql.GraphPlan[taskboardMember, taskboardMember], options rasql.EdgeOptions,
	attach func(*taskboardGraphTask, rasql.LoadedOne[taskboardMember]),
) (rasql.GraphEdge[taskboardTask, taskboardGraphTask], error) {
	parentValue, err := taskboardTaskColumns{}.Bind(parent)
	if err != nil {
		return nil, err
	}
	childValue, err := taskboardMemberColumns{}.Bind(child)
	if err != nil {
		return nil, err
	}
	parentKey, err := rasql.NewGraphKey(rasql.NullKeyPart(parentValue.AssigneeID, func(row taskboardTask) rasql.Nullable[int64] { return row.AssigneeID }))
	if err != nil {
		return nil, err
	}
	childKey, err := rasql.NewGraphKey(rasql.KeyPart(childValue.ID, func(row taskboardMember) int64 { return row.ID }))
	if err != nil {
		return nil, err
	}
	return rasql.HasOne("Assignee", parentKey, childKey, plan, options, attach)
}
func taskboardProjectTasksEdge(
	parent rasql.TypedRelation[taskboardProject], child rasql.TypedRelation[taskboardTask],
	plan rasql.GraphPlan[taskboardTask, taskboardGraphTask], options rasql.EdgeOptions,
	attach func(*taskboardGraphProject, rasql.LoadedMany[taskboardGraphTask]),
) (rasql.GraphEdge[taskboardProject, taskboardGraphProject], error) {
	parentValue, err := taskboardProjectColumns{}.Bind(parent)
	if err != nil {
		return nil, err
	}
	childValue, err := taskboardTaskColumns{}.Bind(child)
	if err != nil {
		return nil, err
	}
	parentKey, err := rasql.NewGraphKey(rasql.KeyPart(parentValue.ID, func(row taskboardProject) int64 { return row.ID }))
	if err != nil {
		return nil, err
	}
	childKey, err := rasql.NewGraphKey(rasql.KeyPart(childValue.ProjectID, func(row taskboardTask) int64 { return row.ProjectID }))
	if err != nil {
		return nil, err
	}
	return rasql.HasMany("Tasks", parentKey, childKey, plan, options, attach)
}
func taskboardProjectPageKey(source rasql.TypedRelation[taskboardProject]) (rasql.PageKey[taskboardProject], error) {
	value, err := taskboardProjectColumns{}.Bind(source)
	if err != nil {
		return nil, err
	}
	return rasql.AscKey(value.ID.Expr(), func(row taskboardProject) int64 { return row.ID }), nil
}

func taskboardOpen(t testing.TB) *taskboardDatabase {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err := conformance.SeedDatabaseForEngine(t.Context(), database, "sqlite"); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	executor, err := rasql.AsExecutor(db, profile)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	result := &taskboardDatabase{sql: database, db: db, profile: profile, executor: executor}
	t.Cleanup(func() { _ = database.Close() })
	return result
}

func taskboardObserved(t testing.TB, database *taskboardDatabase, recorder *conformance.InvocationRecorder) rasql.Executor {
	t.Helper()
	observed, err := database.db.WithInvocationObservers(
		rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), recorder.Observer(),
	)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := rasql.AsExecutor(observed, database.profile)
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func taskboardProjectQuery() rasql.Query[taskboardProject] {
	source, err := taskboardProjects().Source("p")
	if err != nil {
		panic(err)
	}
	expressions, err := taskboardProjectColumns{}.Bind(source)
	if err != nil {
		panic(err)
	}
	projection, err := taskboardProjectProjection(expressions)
	if err != nil {
		panic(err)
	}
	return rasql.Select(source.Source(), projection).
		Where(rasql.EqualValue(expressions.ID.Expr(), int64(1))).
		OrderBy(rasql.AscExpr(expressions.ID.Expr()))
}

func taskboardTaskReadQuery(idValue int64) rasql.Query[taskboardTask] {
	source, err := taskboardTasks().Source("t")
	if err != nil {
		panic(err)
	}
	columns, err := taskboardTaskColumns{}.Bind(source)
	if err != nil {
		panic(err)
	}
	projection, err := taskboardTaskProjection(columns)
	if err != nil {
		panic(err)
	}
	return rasql.Select(source.Source(), projection).
		Where(rasql.EqualValue(columns.ID.Expr(), idValue)).
		OrderBy(rasql.AscExpr(columns.ID.Expr()))
}

type taskboardReportDecoder struct{}

func (taskboardReportDecoder) ResultSchema() rasql.ResultSchema {
	value, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "task_id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "assignee_id", Type: schema.IntegerType{}, Nullable: true},
		rasql.ResultColumn{Name: "member_name", Type: schema.TextType{}, Nullable: true},
	)
	if err != nil {
		panic(err)
	}
	return value
}

func (taskboardReportDecoder) Presence() []rasql.Presence { return nil }
func (taskboardReportDecoder) DecodeRow(source rasql.ScanSource, row *taskboardReportRow) error {
	return source.Scan(&row.TaskID, &row.AssigneeID, &row.MemberName)
}

func taskboardReportQuery() rasql.Query[taskboardReportRow] {
	projectSource, err := taskboardProjects().Source("p")
	if err != nil {
		panic(err)
	}
	taskSource, err := taskboardTasks().Source("t")
	if err != nil {
		panic(err)
	}
	memberSource, err := taskboardMembers().Source("m")
	if err != nil {
		panic(err)
	}
	projects, err := taskboardProjectColumns{}.Bind(projectSource)
	if err != nil {
		panic(err)
	}
	tasks, err := taskboardTaskColumns{}.Bind(taskSource)
	if err != nil {
		panic(err)
	}
	members, err := taskboardMemberColumns{}.Bind(memberSource)
	if err != nil {
		panic(err)
	}
	optionalName, err := rasql.BindOptionalColumn[taskboardMember, string](rasql.Optional(memberSource), "name", "")
	if err != nil {
		panic(err)
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("task_id", tasks.ID.Expr(), schema.IntegerType{}, ""),
		rasql.NullItem("assignee_id", tasks.AssigneeID.NullExpr(), schema.IntegerType{}, ""),
		rasql.NullItem("member_name", optionalName.NullExpr(), schema.TextType{}, ""),
	}, taskboardReportDecoder{})
	if err != nil {
		panic(err)
	}
	return rasql.Select(projectSource.Source(), projection).
		Join(taskSource.Source(), rasql.EqualExpr(projects.ID.Expr(), tasks.ProjectID.Expr())).
		LeftJoin(memberSource.Source(), rasql.EqualOptional(members.ID.Expr(), tasks.AssigneeID.NullExpr())).
		Where(rasql.EqualValue(projects.ID.Expr(), int64(2))).
		OrderBy(rasql.AscExpr(tasks.ID.Expr()))
}

func taskboardGraphPlan() (rasql.GraphPlan[taskboardProject, taskboardGraphProject], rasql.PageSpec[taskboardProject]) {
	projectSource, err := taskboardProjects().Source("p")
	if err != nil {
		panic(err)
	}
	taskSource, err := taskboardTasks().Source("t")
	if err != nil {
		panic(err)
	}
	memberSource, err := taskboardMembers().Source("m")
	if err != nil {
		panic(err)
	}
	projectExpressions, err := taskboardProjectColumns{}.Bind(projectSource)
	if err != nil {
		panic(err)
	}
	taskExpressions, err := taskboardTaskColumns{}.Bind(taskSource)
	if err != nil {
		panic(err)
	}
	memberExpressions, err := taskboardMemberColumns{}.Bind(memberSource)
	if err != nil {
		panic(err)
	}
	projectProjection, err := taskboardProjectProjection(projectExpressions)
	if err != nil {
		panic(err)
	}
	taskProjection, err := taskboardTaskProjection(taskExpressions)
	if err != nil {
		panic(err)
	}
	memberProjection, err := taskboardMemberProjection(memberExpressions)
	if err != nil {
		panic(err)
	}
	projectQuery := rasql.Select(projectSource.Source(), projectProjection).OrderBy(rasql.AscExpr(projectExpressions.ID.Expr()))
	taskQuery := rasql.Select(taskSource.Source(), taskProjection).
		Where(rasql.EqualValue(taskExpressions.IsOpen.Expr(), true)).
		OrderBy(rasql.AscExpr(taskExpressions.ID.Expr()))
	memberQuery := rasql.Select(memberSource.Source(), memberProjection).OrderBy(rasql.AscExpr(memberExpressions.ID.Expr()))
	memberPlan, err := rasql.NewGraphPlan(memberQuery, func(row taskboardMember) taskboardMember { return row })
	if err != nil {
		panic(err)
	}
	assignee, err := taskboardTaskAssigneeEdge(taskSource, memberSource, memberPlan, rasql.EdgeOptions{}, func(parent *taskboardGraphTask, value rasql.LoadedOne[taskboardMember]) { parent.Assignee = value })
	if err != nil {
		panic(err)
	}
	taskPlan, err := rasql.NewGraphPlan(taskQuery, func(row taskboardTask) taskboardGraphTask { return taskboardGraphTask{ID: row.ID, Title: row.Title} }, assignee)
	if err != nil {
		panic(err)
	}
	options := rasql.EdgeOptions{
		PerParentLimit: 5,
		Order:          []rasql.OrderTerm{rasql.AscExpr(taskExpressions.ID.Expr())},
	}
	tasks, err := taskboardProjectTasksEdge(projectSource, taskSource, taskPlan, options, func(parent *taskboardGraphProject, value rasql.LoadedMany[taskboardGraphTask]) { parent.Tasks = value })
	if err != nil {
		panic(err)
	}
	plan, err := rasql.NewGraphPlan(projectQuery, func(row taskboardProject) taskboardGraphProject {
		return taskboardGraphProject{ID: row.ID, Name: row.Name}
	}, tasks)
	if err != nil {
		panic(err)
	}
	key, err := taskboardProjectPageKey(projectSource)
	if err != nil {
		panic(err)
	}
	spec, err := rasql.NewPageSpec([]rasql.PageKey[taskboardProject]{key}, key)
	if err != nil {
		panic(err)
	}
	return plan, spec
}

func taskboardGraphValues(values []taskboardGraphProject) []taskboardGraphRow {
	result := make([]taskboardGraphRow, 0, len(values))
	for _, project := range values {
		row := taskboardGraphRow{ID: project.ID, Name: project.Name, Tasks: make([]taskboardGraphTaskRow, 0, len(project.Tasks.Values))}
		for _, task := range project.Tasks.Values {
			value := taskboardGraphTaskRow{ID: task.ID, Title: task.Title, AssigneeLoaded: task.Assignee.Loaded, AssigneePresent: task.Assignee.Present}
			if task.Assignee.Present && task.Assignee.Value != nil {
				value.AssigneeID = rasql.Nullable[int64]{Value: task.Assignee.Value.ID, Valid: true}
				value.AssigneeName = rasql.Nullable[string]{Value: task.Assignee.Value.Name, Valid: true}
			}
			row.Tasks = append(row.Tasks, value)
		}
		result = append(result, row)
	}
	return result
}

func taskboardSQLReport(ctx context.Context, database *sql.DB) ([]taskboardReportRow, error) {
	rows, err := database.QueryContext(ctx, "SELECT t.id, t.assignee_id, m.name FROM tasks AS t LEFT JOIN members AS m ON m.id = t.assignee_id WHERE t.project_id = ? ORDER BY t.id", int64(2))
	if err != nil {
		return nil, err
	}
	result := make([]taskboardReportRow, 0, 7)
	for rows.Next() {
		var value taskboardReportRow
		var assignee sql.NullInt64
		var member sql.NullString
		if err := rows.Scan(&value.TaskID, &assignee, &member); err != nil {
			_ = rows.Close()
			return nil, err
		}
		value.AssigneeID = rasql.Nullable[int64]{Value: assignee.Int64, Valid: assignee.Valid}
		value.MemberName = rasql.Nullable[string]{Value: member.String, Valid: member.Valid}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return result, nil
}

func taskboardReadEvidence(value any, count int64, invocations []conformance.InvocationRecord) conformance.WorkloadEvidence {
	result, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return conformance.WorkloadEvidence{
		ResultJSON: result, Outcome: "ok", RowsReturned: count, RowsConsumed: count,
		MeasuredRowsConsumed: count, Invocations: invocations,
	}
}

func taskboardSingleSemantic(t testing.TB, database *taskboardDatabase) {
	t.Helper()
	queryValue := taskboardProjectQuery()
	recorder := &conformance.InvocationRecorder{}
	actual, err := rasql.One(t.Context(), taskboardObserved(t, database, recorder), queryValue)
	if err != nil {
		t.Fatal(err)
	}
	if actual.ID != 1 || actual.Name != "project-001" {
		t.Fatalf("rasql single result = %#v", actual)
	}
	rows, err := database.sql.QueryContext(t.Context(), "SELECT id, name FROM projects WHERE id = ? ORDER BY id", int64(1))
	if err != nil {
		t.Fatal(err)
	}
	var expected taskboardProject
	if !rows.Next() {
		t.Fatal("database/sql single read returned no row")
	}
	if err := rows.Scan(&expected.ID, &expected.Name); err != nil {
		t.Fatal(err)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if expected != actual {
		t.Fatalf("single parity: rasql=%#v sql=%#v", actual, expected)
	}
	left := taskboardReadEvidence(actual, 1, taskboardExecutionInvocations(recorder.Snapshot(), "query"))
	right := taskboardReadEvidence(expected, 1, []conformance.InvocationRecord{{Kind: "query", Phase: "execution", Args: []any{int64(1)}, Rows: 1}})
	if err := conformance.CompareWorkloadEvidence("benchmark-single-read", left, right); err != nil {
		t.Fatal(err)
	}
}

func taskboardReportSemantic(t testing.TB, database *taskboardDatabase) {
	t.Helper()
	recorder := &conformance.InvocationRecorder{}
	actual, err := rasql.All(t.Context(), taskboardObserved(t, database, recorder), taskboardReportQuery())
	if err != nil {
		t.Fatal(err)
	}
	expected, err := taskboardSQLReport(t.Context(), database.sql)
	if err != nil {
		t.Fatal(err)
	}
	want := make([]taskboardReportRow, 0, 7)
	for id := int64(8); id <= 14; id++ {
		row := taskboardReportRow{TaskID: id}
		if id%11 != 0 {
			row.AssigneeID = rasql.Nullable[int64]{Value: id, Valid: true}
			row.MemberName = rasql.Nullable[string]{Value: fmt.Sprintf("member-%02d", id), Valid: true}
		}
		want = append(want, row)
	}
	if fmt.Sprint(actual) != fmt.Sprint(want) || fmt.Sprint(expected) != fmt.Sprint(want) {
		t.Fatalf("nullable report differs: rasql=%#v sql=%#v want=%#v", actual, expected, want)
	}
	left := taskboardReadEvidence(actual, int64(len(actual)), taskboardExecutionInvocations(recorder.Snapshot(), "query"))
	right := taskboardReadEvidence(expected, int64(len(expected)), []conformance.InvocationRecord{{Kind: "query", Phase: "execution", Args: []any{int64(2)}, Rows: int64(len(expected))}})
	if err := conformance.CompareWorkloadEvidence("benchmark-nullable-report", left, right); err != nil {
		t.Fatal(err)
	}
}

func taskboardGraphSemantic(t testing.TB, database *taskboardDatabase) {
	t.Helper()
	plan, spec := taskboardGraphPlan()
	recorder := &conformance.InvocationRecorder{}
	page, err := rasql.PageGraphAfter(t.Context(), taskboardObserved(t, database, recorder), plan, spec, rasql.PagePolicy{DefaultLimit: 10, MaxLimit: 10}, rasql.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	actual := taskboardGraphValues(page.Values)
	if len(actual) != 10 || !page.HasMore || len(actual[0].Tasks) != 5 {
		t.Fatalf("graph page shape = %d roots, more=%t", len(actual), page.HasMore)
	}
	for index, project := range actual {
		if project.ID != int64(index+1) || project.Name != fmt.Sprintf("project-%03d", index+1) || len(project.Tasks) != 5 {
			t.Fatalf("graph project %d = %#v", index, project)
		}
		for taskIndex, task := range project.Tasks {
			expectedID := int64(index*7 + taskIndex + 1)
			expectedName := ""
			if expectedID%11 != 0 {
				expectedName = fmt.Sprintf("member-%02d", expectedID)
			}
			if task.ID != expectedID || task.Title != fmt.Sprintf("task-%04d", expectedID) ||
				!task.AssigneeLoaded ||
				(task.AssigneeID.Valid != (expectedID%11 != 0)) ||
				(task.AssigneePresent != (expectedID%11 != 0)) ||
				(task.AssigneeID.Valid && task.AssigneeID.Value != expectedID) ||
				(task.AssigneeName.Valid != (expectedName != "")) ||
				(task.AssigneeName.Valid && task.AssigneeName.Value != expectedName) {
				t.Fatalf("graph task = %#v", task)
			}
		}
	}
	executions := make([]conformance.InvocationRecord, 0, 3)
	for _, invocation := range recorder.Snapshot() {
		if invocation.Phase == "execution" && invocation.Kind == "query" {
			executions = append(executions, invocation)
		}
	}
	if len(executions) != 3 {
		t.Fatalf("graph execution stages = %d, want 3", len(executions))
	}
	wantRootArgs := []any{int64(11)}
	wantTaskArgs := []any{true}
	for id := int64(1); id <= 10; id++ {
		wantTaskArgs = append(wantTaskArgs, id)
	}
	wantTaskArgs = append(wantTaskArgs, int64(5))
	wantMemberArgs := make([]any, 0, 46)
	for project := int64(1); project <= 10; project++ {
		for offset := int64(1); offset <= 5; offset++ {
			id := (project-1)*7 + offset
			if id%11 != 0 {
				wantMemberArgs = append(wantMemberArgs, id)
			}
		}
	}
	wantMemberArgs = append(wantMemberArgs, int64(2))
	wantArgs := [][]any{wantRootArgs, wantTaskArgs, wantMemberArgs}
	for index := range wantArgs {
		if !taskboardGraphArgsEqual(executions[index].Args, wantArgs[index]) {
			t.Fatalf("rasql graph args %d = %#v, want %#v", index, executions[index].Args, wantArgs[index])
		}
	}
	sqlGraph := taskboardGraphSQLIteration(t, database.sql)
	if len(sqlGraph.Stages) != 3 || fmt.Sprint(sqlGraph.Graph) != fmt.Sprint(actual) {
		t.Fatalf("graph parity differs: rasql=%#v sql=%#v", actual, sqlGraph)
	}
	for index, stage := range sqlGraph.Stages {
		if !taskboardGraphArgsEqual(stage.Args, wantArgs[index]) {
			t.Fatalf("sql graph args %d = %#v, want %#v", index, stage.Args, wantArgs[index])
		}
	}
	if !strings.Contains(sqlGraph.Stages[0].SQL, "LIMIT ?") || !strings.Contains(sqlGraph.Stages[1].SQL, "ROW_NUMBER() OVER (PARTITION BY project_id ORDER BY id)") || !strings.Contains(sqlGraph.Stages[1].SQL, "is_open = ?") || !strings.Contains(sqlGraph.Stages[1].SQL, "task_rank <= ?") || !strings.Contains(sqlGraph.Stages[2].SQL, "ROW_NUMBER() OVER (PARTITION BY id ORDER BY id)") || !strings.Contains(sqlGraph.Stages[2].SQL, "member_rank <= ?") {
		t.Fatal("handwritten graph stages do not match required windows and limits")
	}
	left := taskboardReadEvidence(actual, 10, executions)
	if len(left.ResultJSON) == 0 {
		t.Fatal("empty graph result evidence")
	}
}

func taskboardGraphArgsEqual(left, right []any) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		leftInt, leftOK := graphIntegerArg(left[index])
		rightInt, rightOK := graphIntegerArg(right[index])
		if leftOK || rightOK {
			if !leftOK || !rightOK || leftInt != rightInt {
				return false
			}
			continue
		}
		if !reflect.DeepEqual(left[index], right[index]) {
			return false
		}
	}
	return true
}

func graphIntegerArg(value any) (int64, bool) {
	switch value := value.(type) {
	case int:
		return int64(value), true
	case int8:
		return int64(value), true
	case int16:
		return int64(value), true
	case int32:
		return int64(value), true
	case int64:
		return value, true
	case uint:
		return int64(value), true
	case uint8:
		return int64(value), true
	case uint16:
		return int64(value), true
	case uint32:
		return int64(value), true
	case uint64:
		return int64(value), true
	default:
		return 0, false
	}
}

var errTaskboardRollback = errors.New("taskboard benchmark rollback")

func taskboardCanonicalMutation(ctx context.Context, executor rasql.Executor) (taskboardMutationResult, error) {
	var result taskboardMutationResult
	err := rasql.Within(ctx, executor, nil, func(ctx context.Context, tx rasql.Executor) error {
		source, err := taskboardTasks().Source("")
		if err != nil {
			return err
		}
		columns, err := taskboardTaskColumns{}.Bind(source)
		if err != nil {
			return err
		}
		create, err := rasql.NewCreatePlan(taskboardTasks().Table,
			rasql.SetField(columns.ID, int64(9001)), rasql.SetField(columns.ProjectID, int64(1)),
			rasql.SetField(columns.Title, "created"), rasql.ClearField(columns.AssigneeID),
			rasql.DefaultField(columns.IsOpen), rasql.DefaultField(columns.CreatedAt))
		if err != nil {
			return err
		}
		created, err := rasql.ExecMutation(ctx, tx, create)
		if err != nil {
			return err
		}
		result.Created = created.Affected
		id := query.TypedColumnOf[taskboardTask, int64](taskboardTasks().Column("id"))
		patch, err := rasql.NewPatchPlan(taskboardTasks().Table, query.EqualValue(id, int64(9001)), rasql.SetField(columns.Title, "patched"), rasql.ClearField(columns.AssigneeID))
		if err != nil {
			return err
		}
		patched, err := rasql.ExecMutation(ctx, tx, patch)
		if err != nil {
			return err
		}
		result.Patched = patched.Affected
		stored, err := rasql.One(ctx, tx, taskboardTaskReadQuery(9001))
		if err != nil {
			return err
		}
		if stored.ID != 9001 || stored.ProjectID != 1 || stored.Title != "patched" || stored.IsOpen != true || stored.AssigneeID.Valid || stored.DueOn.Valid || stored.CreatedAt.IsZero() {
			return fmt.Errorf("unexpected canonical mutation state: %#v", stored)
		}
		return errTaskboardRollback
	})
	if !errors.Is(err, errTaskboardRollback) {
		return result, err
	}
	result.RolledBack = true
	return result, nil
}

func taskboardSQLMutation(ctx context.Context, database *sql.DB) (taskboardMutationResult, error) {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return taskboardMutationResult{}, err
	}
	created, err := transaction.ExecContext(ctx, "INSERT INTO tasks (id, project_id, assignee_id, title) VALUES (?, ?, NULL, ?)", int64(9001), int64(1), "created")
	if err != nil {
		_ = transaction.Rollback()
		return taskboardMutationResult{}, err
	}
	createdRows, err := created.RowsAffected()
	if err != nil {
		_ = transaction.Rollback()
		return taskboardMutationResult{}, err
	}
	if createdRows != 1 {
		_ = transaction.Rollback()
		return taskboardMutationResult{}, fmt.Errorf("create affected %d rows", createdRows)
	}
	patched, err := transaction.ExecContext(ctx, "UPDATE tasks SET title = ?, assignee_id = NULL WHERE id = ?", "patched", int64(9001))
	if err != nil {
		_ = transaction.Rollback()
		return taskboardMutationResult{}, err
	}
	patchedRows, err := patched.RowsAffected()
	if err != nil {
		_ = transaction.Rollback()
		return taskboardMutationResult{}, err
	}
	if patchedRows != 1 {
		_ = transaction.Rollback()
		return taskboardMutationResult{}, fmt.Errorf("patch affected %d rows", patchedRows)
	}
	var id, projectID int64
	var assignee sql.NullInt64
	var title string
	var isOpen bool
	var dueOn sql.NullString
	var createdAt sql.NullString
	if err := transaction.QueryRowContext(ctx, "SELECT id, project_id, assignee_id, title, is_open, due_on, created_at FROM tasks WHERE id = ?", int64(9001)).Scan(&id, &projectID, &assignee, &title, &isOpen, &dueOn, &createdAt); err != nil {
		_ = transaction.Rollback()
		return taskboardMutationResult{}, err
	}
	if id != 9001 || projectID != 1 || assignee.Valid || title != "patched" || !isOpen || dueOn.Valid || !createdAt.Valid {
		_ = transaction.Rollback()
		return taskboardMutationResult{}, fmt.Errorf("unexpected SQL mutation state: %d %d %#v %q %t %#v %v", id, projectID, assignee, title, isOpen, dueOn, createdAt)
	}
	if err := transaction.Rollback(); err != nil {
		return taskboardMutationResult{}, err
	}
	return taskboardMutationResult{Created: createdRows, Patched: patchedRows, RolledBack: true}, nil
}

func taskboardBatchPlans() []rasql.MutationPlan {
	source, err := taskboardMembers().Source("")
	if err != nil {
		panic(err)
	}
	columns, err := taskboardMemberColumns{}.Bind(source)
	if err != nil {
		panic(err)
	}
	plans := make([]rasql.MutationPlan, 0, 500)
	for index := int64(0); index < 500; index++ {
		plan, err := rasql.NewCreatePlan(taskboardMembers().Table,
			rasql.SetField(columns.ID, 10000+index), rasql.SetField(columns.Name, fmt.Sprintf("batch-%03d", index)))
		if err != nil {
			panic(err)
		}
		plans = append(plans, plan)
	}
	return plans
}

func taskboardCanonicalBatch(ctx context.Context, executor rasql.Executor, plans []rasql.MutationPlan) (taskboardBatchResult, error) {
	result := taskboardBatchResult{}
	err := rasql.Within(ctx, executor, nil, func(ctx context.Context, tx rasql.Executor) error {
		outcome, err := rasql.ExecMutationBatch(ctx, tx, plans, rasql.MutationBatchOptions{MaxRows: 500, MaxBindParameters: 999})
		if err != nil {
			return err
		}
		result.Inputs = append([]rasql.InputOutcome(nil), outcome.Inputs...)
		for _, input := range outcome.Inputs {
			if input == rasql.InputApplied {
				result.Affected++
			}
		}
		result.Durability = outcome.Durability
		result.FailedBatch = append([]int(nil), outcome.FailedBatch...)
		return errTaskboardRollback
	})
	if !errors.Is(err, errTaskboardRollback) {
		return result, err
	}
	result.RolledBack = true
	return result, nil
}

type taskboardBatchInsert struct {
	Statement string
	Args      []any
}

func taskboardBatchInserts() []taskboardBatchInsert {
	build := func(start, count int64) taskboardBatchInsert {
		values := make([]string, count)
		args := make([]any, 0, count*2)
		for index := int64(0); index < count; index++ {
			values[index] = "(?, ?)"
			args = append(args, 10000+start+index, fmt.Sprintf("batch-%03d", start+index))
		}
		return taskboardBatchInsert{"INSERT INTO members (id, name) VALUES " + strings.Join(values, ","), args}
	}
	return []taskboardBatchInsert{build(0, 499), build(499, 1)}
}

func taskboardSQLBatch(ctx context.Context, database *sql.DB, inserts []taskboardBatchInsert) (taskboardBatchResult, error) {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return taskboardBatchResult{}, err
	}
	result := taskboardBatchResult{Inputs: make([]rasql.InputOutcome, 500)}
	inputStart := 0
	for _, insert := range inserts {
		outcome, err := transaction.ExecContext(ctx, insert.Statement, insert.Args...)
		if err != nil {
			_ = transaction.Rollback()
			return result, err
		}
		affected, err := outcome.RowsAffected()
		if err != nil {
			_ = transaction.Rollback()
			return result, err
		}
		expectedRows := int64(len(insert.Args) / 2)
		if affected != expectedRows {
			_ = transaction.Rollback()
			return result, fmt.Errorf("batch partition affected %d rows, want %d", affected, expectedRows)
		}
		result.Affected += affected
		result.PartitionRows = append(result.PartitionRows, affected)
		for index := 0; index < int(expectedRows); index++ {
			result.Inputs[inputStart+index] = rasql.InputApplied
		}
		inputStart += int(expectedRows)
	}
	if err := transaction.Rollback(); err != nil {
		return result, err
	}
	result.RolledBack = true
	result.Durability = rasql.DurabilityPending
	return result, nil
}

func taskboardAssertNoSurvivors(t testing.TB, database *sql.DB) {
	t.Helper()
	var taskCount, batchCount int
	if err := database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM tasks WHERE id = ?", int64(9001)).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM members WHERE id BETWEEN ? AND ?", int64(10000), int64(10499)).Scan(&batchCount); err != nil {
		t.Fatal(err)
	}
	if taskCount != 0 || batchCount != 0 {
		t.Fatalf("rollback survivors: task=%d batch=%d", taskCount, batchCount)
	}
}

func taskboardValidateBatchPartitions(rows []int64) error {
	if len(rows) != 2 || rows[0] != 499 || rows[1] != 1 {
		return fmt.Errorf("batch partitions = %v, want [499 1]", rows)
	}
	return nil
}

func taskboardValidateBatchExecutions(records []conformance.InvocationRecord) error {
	if len(records) != 2 {
		return fmt.Errorf("batch executions = %d, want 2", len(records))
	}
	wantStart := int64(10000)
	for statement, record := range records {
		upperSQL := strings.ToUpper(record.SQL)
		if !strings.Contains(upperSQL, "INSERT INTO") || !strings.Contains(upperSQL, "MEMBERS") {
			return fmt.Errorf("batch statement %d is %q", statement, record.SQL)
		}
		wantArgs := 998
		if statement == 1 {
			wantArgs = 2
		}
		if len(record.Args) != wantArgs {
			return fmt.Errorf("batch statement %d args = %d, want %d", statement, len(record.Args), wantArgs)
		}
		for index := 0; index < len(record.Args); index += 2 {
			id, ok := record.Args[index].(int64)
			name, nameOK := record.Args[index+1].(string)
			if !ok || !nameOK || id != wantStart || name != fmt.Sprintf("batch-%03d", wantStart-10000) {
				return fmt.Errorf("batch argument %d = %#v/%#v", index, record.Args[index], record.Args[index+1])
			}
			wantStart++
		}
	}
	return nil
}

func taskboardMutationSemantic(t testing.TB, database *taskboardDatabase) {
	t.Helper()
	canonical, err := taskboardCanonicalMutation(t.Context(), database.executor)
	if err != nil {
		t.Fatal(err)
	}
	taskboardAssertNoSurvivors(t, database.sql)
	handwritten, err := taskboardSQLMutation(t.Context(), database.sql)
	if err != nil {
		t.Fatal(err)
	}
	want := taskboardMutationResult{Created: 1, Patched: 1, RolledBack: true}
	if canonical != want || handwritten != want {
		t.Fatalf("mutation parity: rasql=%#v sql=%#v", canonical, handwritten)
	}
	taskboardAssertNoSurvivors(t, database.sql)
	plans := taskboardBatchPlans()
	inserts := taskboardBatchInserts()
	recorder := &conformance.InvocationRecorder{}
	canonicalBatch, err := taskboardCanonicalBatch(t.Context(), taskboardObserved(t, database, recorder), plans)
	if err != nil {
		t.Fatal(err)
	}
	insertStatements := 0
	for _, invocation := range recorder.Snapshot() {
		if invocation.Kind == "exec" && invocation.Phase == "execution" {
			insertStatements++
		}
	}
	if insertStatements != 2 {
		t.Fatalf("rasql batch statements = %d, want 2", insertStatements)
	}
	batchExecutions := make([]conformance.InvocationRecord, 0, 2)
	for _, invocation := range recorder.Snapshot() {
		if invocation.Kind == "exec" && invocation.Phase == "execution" {
			batchExecutions = append(batchExecutions, invocation)
		}
	}
	if err := taskboardValidateBatchExecutions(batchExecutions); err != nil {
		t.Fatal(err)
	}
	canonicalBatch.PartitionRows = []int64{int64(len(batchExecutions[0].Args) / 2), int64(len(batchExecutions[1].Args) / 2)}
	if err := taskboardValidateBatchPartitions(canonicalBatch.PartitionRows); err != nil {
		t.Fatal(err)
	}
	if canonicalBatch.Durability != rasql.DurabilityPending || len(canonicalBatch.FailedBatch) != 0 {
		t.Fatalf("rasql batch outcome = %#v", canonicalBatch)
	}
	taskboardAssertNoSurvivors(t, database.sql)
	handwrittenBatch, err := taskboardSQLBatch(t.Context(), database.sql, inserts)
	if err != nil {
		t.Fatal(err)
	}
	wantInputs := make([]rasql.InputOutcome, 500)
	for index := range wantInputs {
		wantInputs[index] = rasql.InputApplied
	}
	wantBatch := taskboardBatchResult{Affected: 500, RolledBack: true, Inputs: wantInputs, PartitionRows: []int64{499, 1}, Durability: rasql.DurabilityPending}
	if fmt.Sprint(canonicalBatch) != fmt.Sprint(wantBatch) || fmt.Sprint(handwrittenBatch) != fmt.Sprint(wantBatch) {
		t.Fatalf("batch parity: rasql=%#v sql=%#v", canonicalBatch, handwrittenBatch)
	}
	taskboardAssertNoSurvivors(t, database.sql)
}

func TestTaskboardBatchPartitionRejectsInvalidShapes(t *testing.T) {
	for _, rows := range [][]int64{{}, {250, 250}, {499, 2}, {500}} {
		if err := taskboardValidateBatchPartitions(rows); err == nil {
			t.Fatalf("partition %v unexpectedly passed", rows)
		}
	}
}

func BenchmarkConformanceNullableReportHandwritten(b *testing.B) {
	database := taskboardOpen(b)
	taskboardReportSemantic(b, database)
	if _, err := taskboardSQLReport(b.Context(), database.sql); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		value, err := taskboardSQLReport(b.Context(), database.sql)
		if err != nil {
			b.Fatal(err)
		}
		taskboardReportSink = value
	}
}

func BenchmarkConformanceNullableReportRasql(b *testing.B) {
	database := taskboardOpen(b)
	queryValue := taskboardReportQuery()
	taskboardReportSemantic(b, database)
	if _, err := rasql.All(b.Context(), database.executor, queryValue); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		value, err := rasql.All(b.Context(), database.executor, queryValue)
		if err != nil {
			b.Fatal(err)
		}
		taskboardReportSink = value
	}
}

func taskboardGraphSQLIteration(b testing.TB, database *sql.DB) taskboardGraphSQLResult {
	result := taskboardGraphSQLResult{}
	rootSQL := "SELECT id, name FROM projects ORDER BY id LIMIT ?"
	rows, err := database.QueryContext(b.Context(), rootSQL, int64(11))
	if err != nil {
		b.Fatal(err)
		return result
	}
	result.Stages = append(result.Stages, taskboardGraphStage{SQL: rootSQL, Args: []any{int64(11)}})
	projects := make([]taskboardGraphRow, 0, 10)
	rootIDs := make([]int64, 0, 11)
	for rows.Next() {
		var project taskboardGraphRow
		if err := rows.Scan(&project.ID, &project.Name); err != nil {
			b.Fatal(err)
			return result
		}
		rootIDs = append(rootIDs, project.ID)
		if len(projects) < 10 {
			projects = append(projects, project)
		}
	}
	if err := rows.Err(); err != nil {
		b.Fatal(err)
		return result
	}
	if err := rows.Close(); err != nil {
		b.Fatal(err)
		return result
	}
	if len(rootIDs) != 11 {
		b.Fatalf("graph root rows = %d, want 11", len(rootIDs))
		return result
	}

	rootArgs := make([]any, 10)
	rootPlaceholders := make([]string, 10)
	for index, id := range rootIDs[:10] {
		rootArgs[index] = id
		rootPlaceholders[index] = "?"
	}
	taskSQL := "SELECT project_id, id, assignee_id, title, is_open, due_on, created_at FROM (SELECT project_id, id, assignee_id, title, is_open, due_on, created_at, ROW_NUMBER() OVER (PARTITION BY project_id ORDER BY id) AS task_rank FROM tasks WHERE is_open = ? AND project_id IN (" + strings.Join(rootPlaceholders, ",") + ")) WHERE task_rank <= ? ORDER BY project_id, id"
	taskArgs := append([]any{true}, rootArgs...)
	taskArgs = append(taskArgs, int64(5))
	rows, err = database.QueryContext(b.Context(), taskSQL, taskArgs...)
	if err != nil {
		b.Fatal(err)
		return result
	}
	result.Stages = append(result.Stages, taskboardGraphStage{SQL: taskSQL, Args: taskArgs})
	byProject := make(map[int64][]taskboardGraphTaskRow, 10)
	assigneeIDs := make([]int64, 0, 50)
	seen := make(map[int64]struct{}, 50)
	for rows.Next() {
		var task taskboardTask
		var assignee sql.NullInt64
		var dueOn sql.NullString
		var createdAt sql.NullString
		if err := rows.Scan(&task.ProjectID, &task.ID, &assignee, &task.Title, &task.IsOpen, &dueOn, &createdAt); err != nil {
			b.Fatal(err)
			return result
		}
		value := taskboardGraphTaskRow{ID: task.ID, Title: task.Title, AssigneeLoaded: true}
		if assignee.Valid {
			value.AssigneeID = rasql.Nullable[int64]{Value: assignee.Int64, Valid: true}
			if _, ok := seen[assignee.Int64]; !ok {
				seen[assignee.Int64] = struct{}{}
				assigneeIDs = append(assigneeIDs, assignee.Int64)
			}
		}
		byProject[task.ProjectID] = append(byProject[task.ProjectID], value)
	}
	if err := rows.Err(); err != nil {
		b.Fatal(err)
		return result
	}
	if err := rows.Close(); err != nil {
		b.Fatal(err)
		return result
	}
	if len(assigneeIDs) == 0 {
		b.Fatal("graph assignee stage has no keys")
		return result
	}

	placeholders := make([]string, len(assigneeIDs))
	memberArgs := make([]any, 0, len(assigneeIDs)+1)
	for index, id := range assigneeIDs {
		placeholders[index] = "?"
		memberArgs = append(memberArgs, id)
	}
	memberArgs = append(memberArgs, int64(2))
	memberSQL := "SELECT id, name FROM (SELECT id, name, ROW_NUMBER() OVER (PARTITION BY id ORDER BY id) AS member_rank FROM members WHERE id IN (" + strings.Join(placeholders, ",") + ")) WHERE member_rank <= ? ORDER BY id"
	rows, err = database.QueryContext(b.Context(), memberSQL, memberArgs...)
	if err != nil {
		b.Fatal(err)
		return result
	}
	result.Stages = append(result.Stages, taskboardGraphStage{SQL: memberSQL, Args: memberArgs})
	members := make(map[int64]string, len(assigneeIDs))
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			b.Fatal(err)
			return result
		}
		members[id] = name
	}
	if err := rows.Err(); err != nil {
		b.Fatal(err)
		return result
	}
	if err := rows.Close(); err != nil {
		b.Fatal(err)
		return result
	}
	if len(members) != len(assigneeIDs) {
		b.Fatalf("graph assignee rows = %d, want %d", len(members), len(assigneeIDs))
		return result
	}
	for index := range projects {
		projects[index].Tasks = byProject[projects[index].ID]
		for taskIndex := range projects[index].Tasks {
			task := &projects[index].Tasks[taskIndex]
			if task.AssigneeID.Valid {
				name, ok := members[task.AssigneeID.Value]
				if !ok {
					b.Fatalf("missing member %d", task.AssigneeID.Value)
					return result
				}
				task.AssigneeName = rasql.Nullable[string]{Value: name, Valid: true}
				task.AssigneePresent = true
			}
		}
	}
	result.Graph = projects
	taskboardGraphSink = projects
	return result
}

func BenchmarkConformanceGraphPageHandwritten(b *testing.B) {
	database := taskboardOpen(b)
	taskboardGraphSemantic(b, database)
	taskboardGraphSQLIteration(b, database.sql)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		taskboardGraphSQLIteration(b, database.sql)
	}
}

func BenchmarkConformanceGraphPageRasql(b *testing.B) {
	database := taskboardOpen(b)
	plan, spec := taskboardGraphPlan()
	taskboardGraphSemantic(b, database)
	if _, err := rasql.PageGraphAfter(b.Context(), database.executor, plan, spec, rasql.PagePolicy{DefaultLimit: 10, MaxLimit: 10}, rasql.PageRequest{Limit: 10}); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		page, err := rasql.PageGraphAfter(b.Context(), database.executor, plan, spec, rasql.PagePolicy{DefaultLimit: 10, MaxLimit: 10}, rasql.PageRequest{Limit: 10})
		if err != nil {
			b.Fatal(err)
		}
		taskboardGraphSink = taskboardGraphValues(page.Values)
	}
}

func BenchmarkConformanceCreatePatchHandwritten(b *testing.B) {
	database := taskboardOpen(b)
	taskboardMutationSemantic(b, database)
	if _, err := taskboardSQLMutation(b.Context(), database.sql); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		value, err := taskboardSQLMutation(b.Context(), database.sql)
		if err != nil {
			b.Fatal(err)
		}
		taskboardMutationSink = value
	}
}

func BenchmarkConformanceCreatePatchRasql(b *testing.B) {
	database := taskboardOpen(b)
	taskboardMutationSemantic(b, database)
	if _, err := taskboardCanonicalMutation(b.Context(), database.executor); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		value, err := taskboardCanonicalMutation(b.Context(), database.executor)
		if err != nil {
			b.Fatal(err)
		}
		taskboardMutationSink = value
	}
}

func BenchmarkConformanceBatch500Handwritten(b *testing.B) {
	database := taskboardOpen(b)
	inserts := taskboardBatchInserts()
	taskboardMutationSemantic(b, database)
	if _, err := taskboardSQLBatch(b.Context(), database.sql, inserts); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		value, err := taskboardSQLBatch(b.Context(), database.sql, inserts)
		if err != nil {
			b.Fatal(err)
		}
		taskboardBatchSink = value
	}
}

func BenchmarkConformanceBatch500Rasql(b *testing.B) {
	database := taskboardOpen(b)
	plans := taskboardBatchPlans()
	taskboardMutationSemantic(b, database)
	if _, err := taskboardCanonicalBatch(b.Context(), database.executor, plans); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		value, err := taskboardCanonicalBatch(b.Context(), database.executor, plans)
		if err != nil {
			b.Fatal(err)
		}
		taskboardBatchSink = value
	}
}

func TestConformanceSQLiteTaskboardBenchmarks(t *testing.T) {
	database := taskboardOpen(t)
	taskboardSingleSemantic(t, database)
	taskboardReportSemantic(t, database)
	taskboardGraphSemantic(t, database)
	taskboardMutationSemantic(t, database)
}
