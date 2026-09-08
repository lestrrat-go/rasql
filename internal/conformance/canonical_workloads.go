package conformance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dberror"
	"github.com/lestrrat-go/rasql/dberror/mysqlerr"
	"github.com/lestrrat-go/rasql/dberror/pgerr"
	"github.com/lestrrat-go/rasql/dberror/sqliteerr"
	mysqlconsumer "github.com/lestrrat-go/rasql/internal/conformance/testdata/mysql/consumer"
	postgresqlconsumer "github.com/lestrrat-go/rasql/internal/conformance/testdata/postgresql/consumer"
	sqliteconsumer "github.com/lestrrat-go/rasql/internal/conformance/testdata/sqlite/consumer"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type canonicalWorkload struct {
	Name     string
	RunSQL   func(context.Context, *sql.DB, string, *handwrittenObserver) (parityEvidence, error)
	RunRasql func(context.Context, rasql.Executor, rasql.DB, string) (parityEvidence, error)
}

type profileLimitContextKey struct{}

type nullableReportRow struct {
	ProjectID   int64
	ProjectName string
	TaskID      int64
	TaskTitle   string
	AssigneeID  rasql.Nullable[int64]
	MemberName  rasql.Nullable[string]
}

type overdueRow struct {
	ID         int64
	ProjectID  int64
	AssigneeID rasql.Nullable[int64]
	Title      string
	IsOpen     bool
	DueOn      rasql.Nullable[time.Time]
	CreatedAt  time.Time
}

type optionalMemberRow struct {
	ID   int64
	Name rasql.Nullable[string]
}

func nullableValue[T any](value rasql.Nullable[T]) any {
	if !value.Valid {
		return nil
	}
	return value.Value
}

func runCanonicalWorkloads(t *testing.T, engine Engine, database *sql.DB, rawRoot rasql.DB, _ rasql.Executor, environment Environment) {
	t.Helper()
	workloads := canonicalWorkloads()
	signatureDocument, err := LoadPortableSignature()
	require.NoError(t, err)
	expectedRows, err := LoadPortableExpected()
	require.NoError(t, err)
	digest, err := PortableSignatureDigestChecked()
	require.NoError(t, err)
	require.Equal(t, digest, expectedRows.PortableSignatureDigest)
	engineExpected, err := LoadEngineExpected(engine.Name)
	require.NoError(t, err)
	require.Equal(t, engine.ProfileID, engineExpected.Profile)
	require.Equal(t, digest, engineExpected.PortableSignatureDigest)
	expectedByWorkload := make(map[string]PortableExpected, len(expectedRows.Workloads))
	for _, expected := range expectedRows.Workloads {
		expectedByWorkload[expected.Workload] = expected
	}
	recorder := NewRecorder(environmentDSN(environment))
	for _, workload := range workloads {
		t.Run(workload.Name, func(t *testing.T) {
			require.NoError(t, resetConformanceDatabase(t.Context(), database, engine.Name))
			require.NoError(t, validateDatabaseSeedIdentity(t.Context(), database, engine.Name, signatureDocument))
			invocations, events := &InvocationRecorder{}, &EventRecorder{}
			profile, err := rasql.DiscoverEngineProfile(t.Context(), rawRoot, engine.ProfileID)
			require.NoError(t, err)
			observedRoot, err := rawRoot.WithInvocationObservers(rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), invocations.Observer())
			require.NoError(t, err)
			executor, err := rasql.AsExecutor(observedRoot, profile)
			require.NoError(t, err)
			executor, err = rasql.WithEventObservers(executor, rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), events.Observer())
			require.NoError(t, err)
			// Profile discovery uses the same observer chain. Keep setup traffic out of workload evidence.
			resetInvocationRecorder(invocations)
			resetEventRecorder(events)
			start := timeNow()
			rasqlEvidence, err := workload.RunRasql(t.Context(), executor, observedRoot, engine.Name)
			require.NoError(t, err)
			rasqlEvidence.Invocations = invocations.Snapshot()
			rasqlEvidence.Events = events.Snapshot()
			correlated, correlationErr := correlateRASQLWorkload(
				engine.ProfileID,
				workload.Name,
				rasqlEvidence.DecodedRows,
				rasqlEvidence.DecodedStatementRows,
				rasqlEvidence.Invocations,
				rasqlEvidence.Events,
			)
			err = correlationErr
			if err == nil {
				rasqlEvidence.Observations = correlated.Statements
				rasqlEvidence.CorrelatedEvents = correlated.Events
			}
			require.NoError(t, err)
			require.NoError(t, applyRASQLMutationStatements(engine.ProfileID, workload.Name, &rasqlEvidence))
			setObservationCounters(&rasqlEvidence)
			require.NotEmpty(t, rasqlEvidence.Invocations, "rasql invocation observer produced no records")
			require.NotEmpty(t, rasqlEvidence.Events, "rasql event observer produced no records")
			if workload.Name == "early_exit" {
				assertEarlyExitLifecycle(t, rasqlEvidence)
			}
			rasqlEvidence.RowsConsumed = normalizeRows(rasqlEvidence.Invocations)
			expected, ok := signatureDocument.Workload(workload.Name)
			require.True(t, ok, "workload %q is absent from the portable signature", workload.Name)
			canonicalExpected, ok := expectedByWorkload[workload.Name]
			require.True(t, ok, "workload %q is absent from portable expected results", workload.Name)
			require.NoError(t, validatePortableEvidence(expected, canonicalExpected, rasqlEvidence))
			physicalExpected, ok := engineExpected.Workload(workload.Name)
			require.True(t, ok, "workload %q is absent from engine expected results", workload.Name)
			require.NoError(t, validateEngineEvidence(physicalExpected, "rasql", rasqlEvidence))
			require.NoError(t, resetConformanceDatabase(t.Context(), database, engine.Name))
			require.NoError(t, validateDatabaseSeedIdentity(t.Context(), database, engine.Name, signatureDocument))
			sqlObserver := newHandwrittenObserver()
			sqlContext := context.WithValue(t.Context(), profileLimitContextKey{}, profile.Limits().MaxBindParameters)
			sqlEvidence, err := workload.RunSQL(sqlContext, database, engine.Name, sqlObserver)
			require.NoError(t, err)
			observations, snapshotErr := sqlObserver.Snapshot()
			require.NoError(t, snapshotErr)
			observations = normalizeObservationParents(observations)
			observations, snapshotErr = validateImplementationObservations(
				engine.ProfileID,
				"database/sql",
				workload.Name,
				observations,
			)
			require.NoError(t, snapshotErr)
			sqlEvidence.Observations = observations
			sqlEvidence.Invocations = invocationRecords(observations)
			setSQLMutationStatements(workload.Name, &sqlEvidence)
			setObservationCounters(&sqlEvidence)
			require.NotEmpty(t, sqlEvidence.Invocations, "database/sql lifecycle recorder produced no records")
			validateGraphEvidence(t, workload.Name, engine.Name, profile.Limits().MaxBindParameters, rasqlEvidence)
			validateGraphEvidence(t, workload.Name, engine.Name, profile.Limits().MaxBindParameters, sqlEvidence)
			require.NoError(t, validatePortableEvidence(expected, canonicalExpected, sqlEvidence))
			require.NoError(t, validateEngineEvidence(physicalExpected, "database/sql", sqlEvidence))
			if err := compareParity(workload.Name, rasqlEvidence, sqlEvidence); err != nil {
				t.Fatal(err)
			}
			measurements := make([]Measurement, 0, 2)
			for implementation, evidence := range map[string]parityEvidence{"rasql": rasqlEvidence, "database/sql": sqlEvidence} {
				measurement := MeasurementFromEnvironment(environment, implementation, engine.Name, engine.ProfileID, workload.Name, 0, digest)
				measurement.SemanticStatus = "pass"
				measurement.Comparable = true
				measurement.SQLDigest = evidence.SQLDigest()
				measurement.Statements = evidence.StatementCount()
				measurement.RowsReturned = evidence.RowsReturned
				measurement.RowsConsumed = evidence.RowsConsumed
				measurement.DurationNS = timeNow().Sub(start).Nanoseconds()
				measurements = append(measurements, measurement)
			}
			require.NoError(t, addMeasurementPair(recorder, measurements...))
		})
	}
	start := timeNow()
	_, profileErr := rasql.EngineProfileFromVersion(engine.ProfileID, 999, 0, 0)
	if !errors.Is(profileErr, engineprofile.ErrUnsupportedVersion) {
		t.Fatalf("unsupported version %q returned %v", engine.ProfileID, profileErr)
	}
	unsupported := parityEvidence{ResultJSON: []byte(`"version_error"`), Outcome: "version_error"}
	signatureExpected, ok := signatureDocument.Workload("unsupported_version")
	require.True(t, ok)
	portableExpected, ok := expectedByWorkload["unsupported_version"]
	require.True(t, ok)
	require.NoError(t, validatePortableEvidence(signatureExpected, portableExpected, unsupported))
	physicalExpected, ok := engineExpected.Workload("unsupported_version")
	require.True(t, ok)
	require.NoError(t, validateEngineEvidence(physicalExpected, "rasql", unsupported))
	measurement := MeasurementFromEnvironment(environment, "rasql", engine.Name, engine.ProfileID, "unsupported_version", 0, digest)
	measurement.SemanticStatus = "pass"
	measurement.Comparable = true
	measurement.SQLDigest = unsupported.SQLDigest()
	measurement.DurationNS = timeNow().Sub(start).Nanoseconds()
	require.NoError(t, recorder.Add(measurement))
	if output := getenv("RASQL_CONFORMANCE_OUTPUT"); output != "" {
		file, err := osCreate(output)
		require.NoError(t, err)
		require.NoError(t, recorder.WriteJSON(file))
		require.NoError(t, file.Close())
	}
}

func cloneRows(rows [][]any) [][]any {
	if rows == nil {
		return nil
	}
	result := make([][]any, len(rows))
	for index, row := range rows {
		result[index] = cloneInvocationArgs(row)
	}
	return result
}

func setObservationCounters(evidence *parityEvidence) {
	if len(evidence.Observations) == 0 {
		return
	}
	var measured, verification int64
	for _, observation := range evidence.Observations {
		if observation.Verification {
			verification += observation.RowsConsumed
		} else {
			measured += observation.RowsConsumed
		}
	}
	evidence.MeasuredRowsConsumed = measured
	evidence.VerificationRowsConsumed = verification
	evidence.RowsConsumed = measured + verification
}

func validateGraphEvidence(t *testing.T, workload, engine string, maxBind int, evidence parityEvidence) {
	t.Helper()
	if workload != "taskboard_graph_page" && workload != "graph_500_parent_limit" {
		return
	}
	observations := measuredObservations(evidence.Observations)
	require.NotEmpty(t, observations, "%s has no measured graph observations", workload)
	for index, observation := range observations {
		require.True(t, observation.Started && observation.Completed, "%s statement %d lifecycle", workload, index)
		require.Equal(t, 1, observation.CompletionCount, "%s statement %d completion count", workload, index)
	}
	if workload == "taskboard_graph_page" {
		require.Len(t, observations, 15, "graph page statement count")
		wantRoles := []statementRole{
			roleRoot, roleTasks, roleAssignees,
			roleRoot, roleTasks, roleAssignees,
			roleRoot, roleTasks, roleAssignees,
			roleRoot, roleTasks, roleAssignees,
			roleRoot, roleTasks, roleAssignees,
		}
		wantRows := []int64{11, 50, 45, 11, 50, 46, 11, 50, 45, 11, 50, 45, 10, 50, 46}
		for index := range observations {
			require.Equal(t, index%3, observations[index].StatementIndex, "graph page statement %d index", index)
			require.Equal(t, wantRoles[index], observations[index].Role, "graph page statement %d role", index)
			require.Equal(t, wantRows[index], observations[index].RowsConsumed, "graph page statement %d rows", index)
			require.Equal(t, fmt.Sprintf("%d", index/3), observations[index].LogicalParent, "graph page statement %d parent", index)
		}
		require.Equal(t, int64(531), evidence.RowsConsumed, "graph page physical rows")
		return
	}
	for index, observation := range observations {
		require.Equal(t, index, observation.StatementIndex, "%s statement %d index", workload, index)
	}

	if engine == "sqlite" {
		require.Len(t, observations, 5, "sqlite graph statement count")
		wantRows := []int64{500, 2500, 998, 998, 276}
		wantArgs := []int{0, 502, 999, 999, 277}
		for index, observation := range observations {
			require.Equal(t, wantRows[index], observation.RowsConsumed, "sqlite graph statement %d rows", index)
			require.Len(t, observation.Args, wantArgs[index], "sqlite graph statement %d args", index)
		}
	} else {
		require.Len(t, observations, 3, "%s graph statement count", engine)
		require.Equal(t, int64(500), observations[0].RowsConsumed)
		require.Equal(t, int64(2500), observations[1].RowsConsumed)
		require.Equal(t, int64(2272), observations[2].RowsConsumed)
	}
	require.Equal(t, int64(5272), evidence.RowsConsumed, "graph physical rows")
	require.Equal(t, maxBind, graphProfileMaxBind(engine), "graph profile bind limit")
}

func graphProfileMaxBind(engine string) int {
	if engine == "sqlite" {
		return 999
	}
	return 65535
}

// These variables keep the runner easy to test without changing production APIs.
var timeNow = time.Now
var getenv = os.Getenv
var osCreate = os.Create

func resetInvocationRecorder(recorder *InvocationRecorder) {
	recorder.mu.Lock()
	recorder.Records = nil
	recorder.mu.Unlock()
}

func resetEventRecorder(recorder *EventRecorder) {
	recorder.mu.Lock()
	recorder.Records = nil
	recorder.mu.Unlock()
}

func assertEarlyExitLifecycle(t *testing.T, evidence parityEvidence) {
	t.Helper()
	consumptions := make([]InvocationRecord, 0, 2)
	for _, record := range evidence.Invocations {
		if record.Phase == "consumption" {
			consumptions = append(consumptions, record)
		}
	}
	require.Len(t, consumptions, 2)
	require.Equal(t, int64(1), consumptions[0].Rows)
	require.True(t, consumptions[0].EarlyClose)
	require.False(t, consumptions[1].EarlyClose)
	terminals := make([]rasql.Event, 0, 2)
	for _, record := range evidence.Events {
		if record.Event.Kind == rasql.EventStatement && record.Event.Phase == rasql.EventTerminal {
			terminals = append(terminals, record.Event)
		}
	}
	require.Len(t, terminals, 2)
	require.Equal(t, int64(1), terminals[0].Rows)
	require.True(t, terminals[0].EarlyClose)
	require.False(t, terminals[1].EarlyClose)
}

func environmentDSN(Environment) string { return "" }

func resetConformanceDatabase(ctx context.Context, db *sql.DB, engine string) error {
	for _, table := range []string{"task_labels", "tasks", "members", "projects"} {
		if _, err := db.ExecContext(ctx, BindSQL(engine, "DROP TABLE IF EXISTS "+table)); err != nil {
			return err
		}
	}
	return SeedDatabaseForEngine(ctx, db, engine)
}

func sqlExec(ctx context.Context, observer *handwrittenObserver, execer handwrittenExecer, role statementRole, parent string, index int, statement string, args ...any) (sql.Result, error) {
	metadata, err := measuredStatement(role, parent, index)
	if err != nil {
		return nil, err
	}
	return observer.ExecContext(ctx, execer, metadata, statement, args...)
}

func sqlScopeExec(ctx context.Context, observer *handwrittenObserver, execer handwrittenExecer, role statementRole, parent, statement string) (sql.Result, error) {
	metadata, err := scopeStatement(role, parent)
	if err != nil {
		return nil, err
	}
	return observer.ExecContext(ctx, execer, metadata, statement)
}

func sqlQuery(ctx context.Context, observer *handwrittenObserver, querier handwrittenQuerier, role statementRole, parent string, index int, statement string, args ...any) (*handwrittenRows, error) {
	metadata, err := measuredStatement(role, parent, index)
	if err != nil {
		return nil, err
	}
	return observer.QueryContext(ctx, querier, metadata, statement, args...)
}

func sqlVerifyQuery(ctx context.Context, observer *handwrittenObserver, querier handwrittenQuerier, parent string, index int, statement string, destination []any, args ...any) error {
	metadata, err := verificationStatement(parent+"/verification", index)
	if err != nil {
		return err
	}
	return observer.QueryOne(ctx, querier, metadata, statement, destination, args...)
}

func sqlVerifyRows(ctx context.Context, observer *handwrittenObserver, querier handwrittenQuerier, parent string, index int, statement string, args ...any) (*handwrittenRows, error) {
	metadata, err := verificationStatement(parent+"/verification", index)
	if err != nil {
		return nil, err
	}
	return observer.QueryContext(ctx, querier, metadata, statement, args...)
}

func canonicalWorkloads() []canonicalWorkload {
	return []canonicalWorkload{
		{Name: "single_row_read", RunSQL: sqlSingleRow, RunRasql: rasqlSingleRow},
		{Name: "nullable_join_report", RunSQL: sqlNullableReport, RunRasql: rasqlNullableReport},
		{Name: "typed_sql_report", RunSQL: sqlTypedReport, RunRasql: rasqlTypedReport},
		{Name: "create_patch", RunSQL: sqlCreatePatch, RunRasql: rasqlCreatePatch},
		{Name: "bulk_write", RunSQL: sqlBulkWrite, RunRasql: rasqlBulkWrite},
		{Name: "rollback", RunSQL: sqlRollback, RunRasql: rasqlRollback},
		{Name: "bulk_rollback", RunSQL: sqlBulkRollback, RunRasql: rasqlBulkRollback},
		{Name: "early_exit", RunSQL: sqlEarlyExit, RunRasql: rasqlEarlyExit},
		{Name: "cancellation", RunSQL: sqlCancellation, RunRasql: rasqlCancellation},
		{Name: "taskboard_graph_page", RunSQL: sqlGraphPage, RunRasql: rasqlGraphPage},
		{Name: "graph_500_parent_limit", RunSQL: sqlGraphAll, RunRasql: rasqlGraphAll},
	}
}

func sqlSingleRow(ctx context.Context, db *sql.DB, engine string, observer *handwrittenObserver) (parityEvidence, error) {
	statement := BindSQL(engine, "SELECT id, name FROM projects WHERE id = ? ORDER BY id")
	var row projectRow
	metadata, err := measuredStatement(roleRead, "single_row_read", 0)
	if err != nil {
		return parityEvidence{}, err
	}
	if err := observer.QueryOne(ctx, db, metadata, statement, []any{&row.ID, &row.Name}, int64(1)); err != nil {
		return parityEvidence{}, err
	}
	data, err := marshalResult(row)
	return parityEvidence{ResultJSON: data, Outcome: "one", RowsReturned: 1, RowsConsumed: 1, Invocations: []InvocationRecord{{SQL: statement, Kind: "query", Phase: "consumption", Rows: 1, Args: []any{int64(1)}}}}, err
}

func rasqlSingleRow(ctx context.Context, executor rasql.Executor, _ rasql.DB, _ string) (parityEvidence, error) {
	fixture, err := newTypedFixture()
	if err != nil {
		return parityEvidence{}, err
	}
	query, err := typedProjectQuery(fixture, 1)
	if err != nil {
		return parityEvidence{}, err
	}
	row, err := rasql.One(ctx, executor, query)
	if err != nil {
		return parityEvidence{}, err
	}
	data, err := marshalResult(row)
	return parityEvidence{ResultJSON: data, Outcome: "one", RowsReturned: 1, RowsConsumed: 1, DecodedRows: [][]any{{row.ID, row.Name}}}, err
}

func sqlNullableReport(ctx context.Context, db *sql.DB, engine string, observer *handwrittenObserver) (parityEvidence, error) {
	statement := BindSQL(engine, "SELECT p.id, p.name, t.id, t.title, t.assignee_id, m.name FROM projects p JOIN tasks t ON t.project_id = p.id LEFT JOIN members m ON m.id = t.assignee_id WHERE p.id = ? ORDER BY t.id")
	metadata, err := measuredStatement(roleReport, "nullable_join_report", 0)
	if err != nil {
		return parityEvidence{}, err
	}
	rows, err := observer.QueryContext(ctx, db, metadata, statement, int64(2))
	if err != nil {
		return parityEvidence{}, err
	}
	values := make([]nullableReportRow, 0, 7)
	for rows.Next() {
		var value nullableReportRow
		var assignee sql.NullInt64
		var member sql.NullString
		if err := rows.Scan(&value.ProjectID, &value.ProjectName, &value.TaskID, &value.TaskTitle, &assignee, &member); err != nil {
			return parityEvidence{}, errors.Join(err, rows.Close())
		}
		value.AssigneeID = rasql.Nullable[int64]{Value: assignee.Int64, Valid: assignee.Valid}
		value.MemberName = rasql.Nullable[string]{Value: member.String, Valid: member.Valid}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return parityEvidence{}, errors.Join(err, rows.Close())
	}
	if err := rows.Close(); err != nil {
		return parityEvidence{}, err
	}
	data, err := marshalResult(values)
	return parityEvidence{ResultJSON: data, Outcome: "ordered", RowsReturned: int64(len(values)), RowsConsumed: int64(len(values)), Invocations: []InvocationRecord{{SQL: statement, Kind: "query", Phase: "consumption", Rows: int64(len(values)), Args: []any{int64(2)}}}}, err
}

func rasqlNullableReport(ctx context.Context, executor rasql.Executor, _ rasql.DB, _ string) (parityEvidence, error) {
	f, err := newTypedFixture()
	if err != nil {
		return parityEvidence{}, err
	}
	p := mustSource(f.projects, "p")
	tSource := mustSource(f.tasks, "t")
	optionalMembers, err := rasql.ReadTableOf[optionalMemberRow](schema.TableDef{Name: "members", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "name", Type: schema.TextType{}, Nullable: true}}})
	if err != nil {
		return parityEvidence{}, err
	}
	m, err := rasql.SourceOf(optionalMembers, "m")
	if err != nil {
		return parityEvidence{}, err
	}
	pid, err := rasql.BindColumn[projectRow, int64](p, "id", "")
	if err != nil {
		return parityEvidence{}, err
	}
	pname, err := rasql.BindColumn[projectRow, string](p, "name", "")
	if err != nil {
		return parityEvidence{}, err
	}
	tid, err := rasql.BindColumn[taskRow, int64](tSource, "id", "")
	if err != nil {
		return parityEvidence{}, err
	}
	tproject, err := rasql.BindColumn[taskRow, int64](tSource, "project_id", "")
	if err != nil {
		return parityEvidence{}, err
	}
	title, err := rasql.BindColumn[taskRow, string](tSource, "title", "")
	if err != nil {
		return parityEvidence{}, err
	}
	assignee, err := rasql.BindNullColumn[taskRow, int64](tSource, "assignee_id", "")
	if err != nil {
		return parityEvidence{}, err
	}
	memberName, err := rasql.BindNullColumn[optionalMemberRow, string](m, "name", "")
	if err != nil {
		return parityEvidence{}, err
	}
	memberID, err := rasql.BindColumn[optionalMemberRow, int64](m, "id", "")
	if err != nil {
		return parityEvidence{}, err
	}
	resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "project_id", Type: schema.IntegerType{}}, rasql.ResultColumn{Name: "project_name", Type: schema.TextType{}}, rasql.ResultColumn{Name: "task_id", Type: schema.IntegerType{}}, rasql.ResultColumn{Name: "task_title", Type: schema.TextType{}}, rasql.ResultColumn{Name: "assignee_id", Type: schema.IntegerType{}, Nullable: true}, rasql.ResultColumn{Name: "member_name", Type: schema.TextType{}, Nullable: true})
	if err != nil {
		return parityEvidence{}, err
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("project_id", pid.Expr(), schema.IntegerType{}, ""), rasql.Item("project_name", pname.Expr(), schema.TextType{}, ""), rasql.Item("task_id", tid.Expr(), schema.IntegerType{}, ""), rasql.Item("task_title", title.Expr(), schema.TextType{}, ""), rasql.NullItem("assignee_id", assignee.NullExpr(), schema.IntegerType{}, ""), rasql.NullItem("member_name", memberName.NullExpr(), schema.TextType{}, "")}, resultDecoder[nullableReportRow]{schema: resultSchema, scan: func(src rasql.ScanSource, row *nullableReportRow) error {
		return src.Scan(&row.ProjectID, &row.ProjectName, &row.TaskID, &row.TaskTitle, &row.AssigneeID, &row.MemberName)
	}})
	if err != nil {
		return parityEvidence{}, err
	}
	queryValue := rasql.Select(p.Source(), projection).Join(tSource.Source(), rasql.EqualExpr(pid.Expr(), tproject.Expr())).LeftJoin(m.Source(), rasql.EqualOptional(memberID.Expr(), assignee.NullExpr())).Where(rasql.EqualValue(pid.Expr(), int64(2))).OrderBy(rasql.AscExpr(tid.Expr()))
	values, err := rasql.All(ctx, executor, queryValue)
	if err != nil {
		return parityEvidence{}, err
	}
	data, err := marshalResult(values)
	decoded := make([][]any, len(values))
	for index, value := range values {
		decoded[index] = []any{value.ProjectID, value.ProjectName, value.TaskID, value.TaskTitle, nullableValue(value.AssigneeID), nullableValue(value.MemberName)}
	}
	return parityEvidence{ResultJSON: data, Outcome: "ordered", RowsReturned: int64(len(values)), RowsConsumed: int64(len(values)), DecodedRows: decoded}, err
}

func sqlTypedReport(ctx context.Context, db *sql.DB, engine string, observer *handwrittenObserver) (parityEvidence, error) {
	cutoff := time.Date(2024, 1, 4, 0, 0, 0, 0, time.UTC)
	statement := BindSQL(engine, "SELECT id, project_id, assignee_id, title, is_open, due_on, created_at FROM tasks WHERE project_id = ? AND is_open = ? AND due_on IS NOT NULL AND due_on < ? ORDER BY id")
	metadata, err := measuredStatement(roleReport, "typed_sql_report", 0)
	if err != nil {
		return parityEvidence{}, err
	}
	rows, err := observer.QueryContext(ctx, db, metadata, statement, int64(1), true, cutoff)
	if err != nil {
		return parityEvidence{}, err
	}
	values := make([]overdueRow, 0, 7)
	var consumed int64
	for rows.Next() {
		consumed++
		var value overdueRow
		var assignee sql.NullInt64
		var dueOn sql.NullTime
		var createdAt sql.NullTime
		if err := rows.Scan(&value.ID, &value.ProjectID, &assignee, &value.Title, &value.IsOpen, &dueOn, &createdAt); err != nil {
			return parityEvidence{}, errors.Join(err, rows.Close())
		}
		value.AssigneeID = rasql.Nullable[int64]{Value: assignee.Int64, Valid: assignee.Valid}
		value.DueOn = rasql.Nullable[time.Time]{Value: dueOn.Time, Valid: dueOn.Valid}
		value.CreatedAt = createdAt.Time
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return parityEvidence{}, errors.Join(err, rows.Close())
	}
	if err := rows.Close(); err != nil {
		return parityEvidence{}, err
	}
	data, err := marshalResult(values)
	return parityEvidence{ResultJSON: data, Outcome: "ordered", RowsReturned: int64(len(values)), RowsConsumed: consumed, Invocations: []InvocationRecord{{SQL: statement, Kind: "query", Phase: "consumption", Rows: consumed, Args: []any{int64(1), true, cutoff}}}}, err
}

func rasqlTypedReport(ctx context.Context, executor rasql.Executor, _ rasql.DB, engine string) (parityEvidence, error) {
	cutoff := time.Date(2024, 1, 4, 0, 0, 0, 0, time.UTC)
	var values []overdueRow
	var err error
	switch engine {
	case "sqlite":
		rows, runErr := sqliteconsumer.RunOverdueTasks(ctx, executor, 1, true, cutoff)
		err = runErr
		for _, row := range rows {
			values = append(values, overdueRow{ID: row.ID, ProjectID: row.ProjectID, AssigneeID: row.AssigneeID, Title: row.Title, IsOpen: row.IsOpen, DueOn: row.DueOn, CreatedAt: row.CreatedAt})
		}
	case "postgresql":
		rows, runErr := postgresqlconsumer.RunOverdueTasks(ctx, executor, 1, true, cutoff)
		err = runErr
		for _, row := range rows {
			values = append(values, overdueRow{ID: row.ID, ProjectID: row.ProjectID, AssigneeID: row.AssigneeID, Title: row.Title, IsOpen: row.IsOpen, DueOn: row.DueOn, CreatedAt: row.CreatedAt})
		}
	case "mysql":
		rows, runErr := mysqlconsumer.RunOverdueTasks(ctx, executor, 1, true, cutoff)
		err = runErr
		for _, row := range rows {
			values = append(values, overdueRow{ID: row.ID, ProjectID: row.ProjectID, AssigneeID: row.AssigneeID, Title: row.Title, IsOpen: row.IsOpen, DueOn: row.DueOn, CreatedAt: row.CreatedAt})
		}
	default:
		return parityEvidence{}, fmt.Errorf("unknown engine %q", engine)
	}
	if err != nil {
		return parityEvidence{}, err
	}
	data, err := marshalResult(values)
	decoded := make([][]any, len(values))
	for index, value := range values {
		decoded[index] = []any{value.ID, value.ProjectID, nullableValue(value.AssigneeID), value.Title, value.IsOpen, nullableValue(value.DueOn), value.CreatedAt}
	}
	return parityEvidence{ResultJSON: data, Outcome: "ordered", RowsReturned: int64(len(values)), RowsConsumed: int64(len(values)), DecodedRows: decoded}, err
}

func sqlCreatePatch(ctx context.Context, db *sql.DB, engine string, observer *handwrittenObserver) (parityEvidence, error) {
	insert := BindSQL(engine, "INSERT INTO tasks(id, project_id, assignee_id, title) VALUES (?, ?, ?, ?)")
	createMeta, err := measuredStatement(roleMutation, "create_patch/create", 0)
	if err != nil {
		return parityEvidence{}, err
	}
	if _, err := observer.ExecContext(ctx, db, createMeta, insert, int64(4001), int64(1), int64(1), "created"); err != nil {
		return parityEvidence{}, err
	}
	update := BindSQL(engine, "UPDATE tasks SET assignee_id = ?, title = ?, is_open = ? WHERE id = ?")
	patchMeta, err := measuredStatement(roleMutation, "create_patch/patch", 0)
	if err != nil {
		return parityEvidence{}, err
	}
	if _, err := observer.ExecContext(ctx, db, patchMeta, update, nil, "patched", false, int64(4001)); err != nil {
		return parityEvidence{}, err
	}
	var row taskRow
	selectSQL := BindSQL(engine, "SELECT id, project_id, assignee_id, title, is_open, due_on, created_at FROM tasks WHERE id = ? ORDER BY id")
	var assignee sql.NullInt64
	var dueOn sql.NullTime
	var createdAt sql.NullTime
	verifyMeta, err := verificationStatement("create_patch/verification", 0)
	if err != nil {
		return parityEvidence{}, err
	}
	if err := observer.QueryOne(ctx, db, verifyMeta, selectSQL, []any{&row.ID, &row.ProjectID, &assignee, &row.Title, &row.Open, &dueOn, &createdAt}, int64(4001)); err != nil {
		return parityEvidence{}, err
	}
	row.AssigneeID = rasql.Nullable[int64]{Value: assignee.Int64, Valid: assignee.Valid}
	row.DueOn = rasql.Nullable[time.Time]{Value: dueOn.Time, Valid: dueOn.Valid}
	row.CreatedAt = createdAt.Time
	data, err := marshalResult(row)
	return parityEvidence{ResultJSON: data, Outcome: "committed", RowsReturned: 1, RowsConsumed: 1, Invocations: []InvocationRecord{{SQL: insert, Kind: "exec", Args: []any{int64(4001), int64(1), int64(1), "created"}}, {SQL: update, Kind: "exec", Args: []any{nil, "patched", false, int64(4001)}}, {SQL: selectSQL, Kind: "query", Rows: 1, Args: []any{int64(4001)}}}}, err
}

func rasqlCreatePatch(ctx context.Context, executor rasql.Executor, db rasql.DB, _ string) (parityEvidence, error) {
	f, err := newTypedFixture()
	if err != nil {
		return parityEvidence{}, err
	}
	id := query.TypedColumnOf[taskRow, int64](f.tasks.Column("id"))
	projectID := query.TypedColumnOf[taskRow, int64](f.tasks.Column("project_id"))
	assigneeID := query.NullableColumnOf[taskRow, int64](f.tasks.Column("assignee_id"))
	title := query.TypedColumnOf[taskRow, string](f.tasks.Column("title"))
	isOpen := query.TypedColumnOf[taskRow, bool](f.tasks.Column("is_open"))
	create, err := rasql.NewCreatePlan(f.tasks, rasql.SetField(id, int64(4001)), rasql.SetField(projectID, int64(1)), rasql.SetNullableField(assigneeID, int64(1)), rasql.SetField(title, "created"))
	if err != nil {
		return parityEvidence{}, err
	}
	createOutcome, err := rasql.ExecMutation(ctx, executor, create)
	if err != nil {
		return parityEvidence{}, err
	}
	patch, err := rasql.NewPatchPlan(f.tasks, query.EqualValue(id, int64(4001)), rasql.SetField(title, "patched"), rasql.SetField(isOpen, false), rasql.ClearField(assigneeID))
	if err != nil {
		return parityEvidence{}, err
	}
	patchOutcome, err := rasql.ExecMutation(ctx, executor, patch)
	if err != nil {
		return parityEvidence{}, err
	}
	read, err := typedTaskByID(f, 4001)
	if err != nil {
		return parityEvidence{}, err
	}
	values, err := rasql.All(ctx, executor, read)
	if err != nil {
		return parityEvidence{}, err
	}
	if len(values) != 1 {
		return parityEvidence{}, fmt.Errorf("create patch returned %d rows", len(values))
	}
	if createOutcome.Durability != rasql.DurabilityCommitted || patchOutcome.Durability != createOutcome.Durability {
		return parityEvidence{}, fmt.Errorf("create patch durability differs: create=%d patch=%d", createOutcome.Durability, patchOutcome.Durability)
	}
	if values[0].Open || values[0].AssigneeID.Valid || values[0].CreatedAt.IsZero() {
		return parityEvidence{}, fmt.Errorf("create patch did not preserve defaults and clear state: %#v", values[0])
	}
	data, err := marshalResult(values[0])
	_ = db
	return parityEvidence{
		ResultJSON: data, Outcome: "committed", RowsReturned: 1, RowsConsumed: 1,
		DecodedStatementRows: map[int][][]any{2: {
			{
				values[0].ID,
				values[0].ProjectID,
				nullableValue(values[0].AssigneeID),
				values[0].Title,
				values[0].Open,
				nullableValue(values[0].DueOn),
				values[0].CreatedAt,
			},
		}},
		Mutation: &mutationEvidence{
			Durability: createOutcome.Durability,
			Statements: []mutationStatementEvidence{
				{AffectedValid: true, Affected: createOutcome.Affected},
				{AffectedValid: true, Affected: patchOutcome.Affected},
			},
		},
	}, err
}

func sqlBulkWrite(ctx context.Context, db *sql.DB, engine string, observer *handwrittenObserver) (parityEvidence, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return parityEvidence{}, err
	}
	insertionStatements := make([]InvocationRecord, 0, 2)
	for start := int64(4100); start < 4600; {
		end := start + 500
		if engine == "sqlite" && end-start > 499 {
			end = start + 499
		}
		if end > 4600 {
			end = 4600
		}
		placeholders := make([]string, 0, (end-start)*2)
		args := make([]any, 0, (end-start)*2)
		for id := start; id < end; id++ {
			placeholders = append(placeholders, "(?, ?)")
			args = append(args, id, fmt.Sprintf("bulk-%d", id))
		}
		statement := BindSQL(engine, "INSERT INTO members(id, name) VALUES "+strings.Join(placeholders, ","))
		if _, err := sqlExec(ctx, observer, tx, roleMutation, "bulk_write", len(insertionStatements), statement, args...); err != nil {
			_ = tx.Rollback()
			return parityEvidence{}, err
		}
		insertionStatements = append(insertionStatements, InvocationRecord{SQL: statement, Kind: "exec", Args: args})
		start = end
	}
	if err := tx.Commit(); err != nil {
		return parityEvidence{}, err
	}
	verifyIDs := make([]int64, 0, 500)
	for id := int64(4100); id < 4600; id++ {
		verifyIDs = append(verifyIDs, id)
	}
	verifyPredicates := make([]string, len(verifyIDs))
	verifyArgs := make([]any, len(verifyIDs))
	for index, id := range verifyIDs {
		verifyPredicates[index] = "id = ?"
		verifyArgs[index] = id
	}
	verifySQL := BindSQL(engine, "SELECT id, name FROM members WHERE ("+strings.Join(verifyPredicates, " OR ")+") ORDER BY id")
	verifyRows, err := sqlVerifyRows(ctx, observer, db, "bulk_write", 0, verifySQL, verifyArgs...)
	if err != nil {
		return parityEvidence{}, err
	}
	var count int64
	for verifyRows.Next() {
		var id int64
		var name string
		if err := verifyRows.Scan(&id, &name); err != nil {
			_ = verifyRows.Close()
			return parityEvidence{}, err
		}
		if id != 4100+count || name != fmt.Sprintf("bulk-%d", id) {
			_ = verifyRows.Close()
			return parityEvidence{}, fmt.Errorf("bulk verification row %d is %d/%q", count, id, name)
		}
		count++
	}
	if err := verifyRows.Err(); err != nil {
		_ = verifyRows.Close()
		return parityEvidence{}, err
	}
	_ = verifyRows.Close()
	data, err := marshalResult(count)
	insertionStatements = append(insertionStatements, InvocationRecord{SQL: verifySQL, Kind: "query", Rows: count, Args: verifyArgs})
	inputs := make([]rasql.InputOutcome, 500)
	for index := range inputs {
		inputs[index] = rasql.InputApplied
	}
	return parityEvidence{ResultJSON: data, Outcome: "committed", RowsReturned: 0, RowsConsumed: count, Invocations: insertionStatements,
		Mutation: &mutationEvidence{Durability: rasql.DurabilityCommitted, Inputs: inputs}}, err
}

func rasqlBulkWrite(ctx context.Context, executor rasql.Executor, _ rasql.DB, engine string) (parityEvidence, error) {
	f, err := newTypedFixture()
	if err != nil {
		return parityEvidence{}, err
	}
	id := query.TypedColumnOf[memberRow, int64](f.members.Column("id"))
	name := query.TypedColumnOf[memberRow, string](f.members.Column("name"))
	plans := make([]rasql.MutationPlan, 0, 500)
	for value := int64(4100); value < 4600; value++ {
		plan, planErr := rasql.NewCreatePlan(f.members, rasql.SetField(id, value), rasql.SetField(name, fmt.Sprintf("bulk-%d", value)))
		if planErr != nil {
			return parityEvidence{}, planErr
		}
		plans = append(plans, plan)
	}
	outcome, err := rasql.ExecMutationBatch(ctx, executor, plans, rasql.MutationBatchOptions{MaxRows: 500, Atomic: true})
	if err != nil {
		return parityEvidence{}, err
	}
	if len(outcome.Inputs) != 500 {
		return parityEvidence{}, fmt.Errorf("bulk outcome has %d inputs", len(outcome.Inputs))
	}
	ids := make([]int64, 0, 500)
	for value := int64(4100); value < 4600; value++ {
		ids = append(ids, value)
	}
	read, err := typedMemberQuery(f, ids)
	if err != nil {
		return parityEvidence{}, err
	}
	values, err := rasql.All(ctx, executor, read)
	if err != nil {
		return parityEvidence{}, err
	}
	var count int64
	for _, value := range values {
		if value.ID >= 4100 && value.ID < 4600 {
			count++
		}
	}
	data, err := marshalResult(count)
	verificationOrdinal := 1
	if engine == "sqlite" {
		verificationOrdinal = 2
	}
	verificationRows := make([][]any, len(values))
	for index, value := range values {
		verificationRows[index] = []any{value.ID, value.Name}
	}
	return parityEvidence{ResultJSON: data, Outcome: "committed", RowsReturned: 0, RowsConsumed: int64(len(values)),
		DecodedStatementRows: map[int][][]any{verificationOrdinal: verificationRows},
		Mutation:             &mutationEvidence{Durability: outcome.Durability, Inputs: append([]rasql.InputOutcome(nil), outcome.Inputs...)}}, err
}

func sqlRollback(ctx context.Context, db *sql.DB, engine string, observer *handwrittenObserver) (parityEvidence, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return parityEvidence{}, err
	}
	if _, err := sqlExec(ctx, observer, tx, roleMutation, "rollback", 0, BindSQL(engine, "INSERT INTO members(id, name) VALUES (?, ?)"), int64(4700), "rollback"); err != nil {
		_ = tx.Rollback()
		return parityEvidence{}, err
	}
	if err := tx.Rollback(); err != nil {
		return parityEvidence{}, err
	}
	rows, err := sqlVerifyRows(ctx, observer, db, "rollback", 0, BindSQL(engine, "SELECT id, name FROM members WHERE id = ? ORDER BY id"), int64(4700))
	if err != nil {
		return parityEvidence{}, err
	}
	var values []memberRow
	for rows.Next() {
		var value memberRow
		if err := rows.Scan(&value.ID, &value.Name); err != nil {
			_ = rows.Close()
			return parityEvidence{}, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return parityEvidence{}, err
	}
	if err := rows.Close(); err != nil {
		return parityEvidence{}, err
	}
	data, err := marshalResult(len(values))
	return parityEvidence{ResultJSON: data, Outcome: "rolled_back", RowsReturned: 0, RowsConsumed: int64(len(values)), Invocations: []InvocationRecord{{SQL: BindSQL(engine, "INSERT INTO members(id, name) VALUES (?, ?)"), Kind: "exec", Args: []any{int64(4700), "rollback"}}, {SQL: "SELECT id, name FROM members WHERE id = ? ORDER BY id", Kind: "query", Rows: int64(len(values)), Args: []any{int64(4700)}}},
		Mutation: &mutationEvidence{Affected: 1, Durability: rasql.DurabilityPending}}, err
}

func rasqlRollback(ctx context.Context, executor rasql.Executor, _ rasql.DB, _ string) (parityEvidence, error) {
	f, err := newTypedFixture()
	if err != nil {
		return parityEvidence{}, err
	}
	id := query.TypedColumnOf[memberRow, int64](f.members.Column("id"))
	name := query.TypedColumnOf[memberRow, string](f.members.Column("name"))
	sentinel := errors.New("rollback sentinel")
	var mutation rasql.MutationOutcome
	err = rasql.Within(ctx, executor, nil, func(scopeCtx context.Context, scoped rasql.Executor) error {
		plan, planErr := rasql.NewCreatePlan(f.members, rasql.SetField(id, int64(4700)), rasql.SetField(name, "rollback"))
		if planErr != nil {
			return planErr
		}
		var execErr error
		mutation, execErr = rasql.ExecMutation(scopeCtx, scoped, plan)
		if execErr != nil {
			return execErr
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		return parityEvidence{}, fmt.Errorf("rollback error: %w", err)
	}
	read, err := typedMemberQuery(f, []int64{4700})
	if err != nil {
		return parityEvidence{}, err
	}
	values, err := rasql.All(ctx, executor, read)
	if err != nil {
		return parityEvidence{}, err
	}
	data, err := marshalResult(len(values))
	return parityEvidence{ResultJSON: data, Outcome: "rolled_back", RowsReturned: 0, RowsConsumed: int64(len(values)),
		DecodedStatementRows: map[int][][]any{1: {}},
		Mutation: &mutationEvidence{Affected: mutation.Affected, Durability: mutation.Durability,
			Statements: []mutationStatementEvidence{{AffectedValid: true, Affected: mutation.Affected}}}}, err
}

type bulkRollbackResult struct {
	Sentinel  int64 `json:"sentinel"`
	Survivors int64 `json:"survivors"`
}

func sqlBulkRollback(ctx context.Context, db *sql.DB, engine string, observer *handwrittenObserver) (parityEvidence, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return parityEvidence{}, err
	}
	if _, err := sqlScopeExec(ctx, observer, tx, roleSavepointBegin, "bulk_rollback/savepoint", "SAVEPOINT d4_bulk"); err != nil {
		_ = tx.Rollback()
		return parityEvidence{}, err
	}
	invocations := []InvocationRecord{{SQL: "SAVEPOINT d4_bulk", Kind: "exec"}}
	insertBatch := func(first, count int64, statementIndex int) (InvocationRecord, error) {
		placeholders := make([]string, 0, count*2)
		args := make([]any, 0, count*2)
		for offset := int64(0); offset < count; offset++ {
			id := first + offset
			if first == 5200 && offset == 0 {
				id = 5000
			}
			placeholders = append(placeholders, "(?, ?)")
			args = append(args, id, fmt.Sprintf("bulk-rollback-%d", id))
		}
		statement := BindSQL(engine, "INSERT INTO members(id, name) VALUES "+strings.Join(placeholders, ","))
		_, execErr := sqlExec(ctx, observer, tx, roleMutation, "bulk_rollback/batch", statementIndex-1, statement, args...)
		return InvocationRecord{SQL: statement, Kind: "exec", Args: args}, execErr
	}
	first, err := insertBatch(5000, 200, 1)
	if err != nil {
		_ = tx.Rollback()
		return parityEvidence{}, err
	}
	invocations = append(invocations, first)
	second, err := insertBatch(5200, 200, 2)
	invocations = append(invocations, second)
	if err == nil {
		_ = tx.Rollback()
		return parityEvidence{}, errors.New("bulk rollback duplicate unexpectedly succeeded")
	}
	if _, rollbackErr := sqlScopeExec(ctx, observer, tx, roleSavepointRollback, "bulk_rollback/savepoint", "ROLLBACK TO SAVEPOINT d4_bulk"); rollbackErr != nil {
		_ = tx.Rollback()
		return parityEvidence{}, rollbackErr
	}
	if _, releaseErr := sqlScopeExec(ctx, observer, tx, roleSavepointRelease, "bulk_rollback/savepoint", "RELEASE SAVEPOINT d4_bulk"); releaseErr != nil {
		_ = tx.Rollback()
		return parityEvidence{}, releaseErr
	}
	invocations = append(invocations,
		InvocationRecord{SQL: "ROLLBACK TO SAVEPOINT d4_bulk", Kind: "exec"},
		InvocationRecord{SQL: "RELEASE SAVEPOINT d4_bulk", Kind: "exec"},
	)
	sentinelSQL := BindSQL(engine, "INSERT INTO members(id, name) VALUES (?, ?)")
	if _, err := sqlExec(ctx, observer, tx, roleSentinel, "bulk_rollback/outer", 0, sentinelSQL, int64(5400), "outer-sentinel"); err != nil {
		_ = tx.Rollback()
		return parityEvidence{}, err
	}
	invocations = append(invocations, InvocationRecord{SQL: sentinelSQL, Kind: "exec", Args: []any{int64(5400), "outer-sentinel"}})
	if err := tx.Commit(); err != nil {
		return parityEvidence{}, err
	}
	ids := make([]int64, 0, 401)
	for id := int64(5000); id <= 5400; id++ {
		ids = append(ids, id)
	}
	predicates := make([]string, len(ids))
	args := make([]any, len(ids))
	for index, id := range ids {
		predicates[index] = "id = ?"
		args[index] = id
	}
	verifySQL := BindSQL(engine, "SELECT id, name FROM members WHERE ("+strings.Join(predicates, " OR ")+") ORDER BY id")
	rows, err := sqlVerifyRows(ctx, observer, db, "bulk_rollback", 0, verifySQL, args...)
	if err != nil {
		return parityEvidence{}, err
	}
	var summary bulkRollbackResult
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			_ = rows.Close()
			return parityEvidence{}, err
		}
		if id == 5400 && name == "outer-sentinel" {
			summary.Sentinel++
		} else if id >= 5000 && id < 5400 {
			summary.Survivors++
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return parityEvidence{}, err
	}
	if err := rows.Close(); err != nil {
		return parityEvidence{}, err
	}
	invocations = append(invocations, InvocationRecord{SQL: verifySQL, Kind: "query", Rows: summary.Sentinel + summary.Survivors, Args: args})
	data, err := marshalResult(summary)
	inputs := make([]rasql.InputOutcome, 500)
	for index := 0; index < 200; index++ {
		inputs[index] = rasql.InputRolledBack
	}
	for index := 200; index < 400; index++ {
		inputs[index] = rasql.InputRejected
	}
	failed := make([]int, 200)
	for index := range failed {
		failed[index] = index + 200
	}
	return parityEvidence{ResultJSON: data, Outcome: "rolled_back", RowsReturned: summary.Survivors, RowsConsumed: summary.Sentinel + summary.Survivors, Invocations: invocations,
		Mutation: &mutationEvidence{Durability: rasql.DurabilityPending, Inputs: inputs, Failed: failed}}, err
}

func rasqlBulkRollback(ctx context.Context, executor rasql.Executor, _ rasql.DB, engine string) (parityEvidence, error) {
	f, err := newTypedFixture()
	if err != nil {
		return parityEvidence{}, err
	}
	id := query.TypedColumnOf[memberRow, int64](f.members.Column("id"))
	name := query.TypedColumnOf[memberRow, string](f.members.Column("name"))
	plans := make([]rasql.MutationPlan, 0, 500)
	for index := 0; index < 500; index++ {
		value := int64(5000 + index)
		if index == 200 {
			value = 5000
		}
		plan, planErr := rasql.NewCreatePlan(f.members, rasql.SetField(id, value), rasql.SetField(name, fmt.Sprintf("bulk-rollback-%d", value)))
		if planErr != nil {
			return parityEvidence{}, planErr
		}
		plans = append(plans, plan)
	}
	classifier := rasql.ConstraintFailureClassifier{Classifiers: constraintClassifiers(engine)}
	var batch rasql.MutationBatchOutcome
	var sentinelOutcome rasql.MutationOutcome
	err = rasql.Within(ctx, executor, nil, func(scopeCtx context.Context, scoped rasql.Executor) error {
		var execErr error
		batch, execErr = rasql.ExecMutationBatch(scopeCtx, scoped, plans, rasql.MutationBatchOptions{MaxRows: 200, Atomic: true, Classifier: classifier})
		if execErr == nil {
			return errors.New("bulk rollback duplicate unexpectedly succeeded")
		}
		if len(batch.Inputs) != 500 || batch.Durability != rasql.DurabilityPending || len(batch.FailedBatch) != 200 {
			return fmt.Errorf("unexpected bulk rollback outcome: %#v", batch)
		}
		for index := 0; index < 200; index++ {
			if batch.Inputs[index] != rasql.InputRolledBack {
				return fmt.Errorf("input %d has state %d", index, batch.Inputs[index])
			}
		}
		for index := 200; index < 400; index++ {
			if batch.Inputs[index] != rasql.InputRejected {
				return fmt.Errorf("input %d has state %d", index, batch.Inputs[index])
			}
		}
		for index := 400; index < 500; index++ {
			if batch.Inputs[index] != rasql.InputUnattempted {
				return fmt.Errorf("input %d has state %d", index, batch.Inputs[index])
			}
		}
		sentinelPlan, planErr := rasql.NewCreatePlan(f.members, rasql.SetField(id, int64(5400)), rasql.SetField(name, "outer-sentinel"))
		if planErr != nil {
			return planErr
		}
		sentinelOutcome, planErr = rasql.ExecMutation(scopeCtx, scoped, sentinelPlan)
		if planErr != nil {
			return planErr
		}
		return nil
	})
	if err != nil {
		return parityEvidence{}, err
	}
	ids := make([]int64, 0, 401)
	for value := int64(5000); value <= 5400; value++ {
		ids = append(ids, value)
	}
	read, err := typedMemberQuery(f, ids)
	if err != nil {
		return parityEvidence{}, err
	}
	values, err := rasql.All(ctx, executor, read)
	if err != nil {
		return parityEvidence{}, err
	}
	var summary bulkRollbackResult
	for _, value := range values {
		if value.ID == 5400 && value.Name == "outer-sentinel" {
			summary.Sentinel++
		} else if value.ID >= 5000 && value.ID < 5400 {
			summary.Survivors++
		}
	}
	if summary.Sentinel != 1 || summary.Survivors != 0 {
		return parityEvidence{}, fmt.Errorf("unexpected surviving rows: %#v", summary)
	}
	data, err := marshalResult(summary)
	verificationRows := make([][]any, len(values))
	for index, value := range values {
		verificationRows[index] = []any{value.ID, value.Name}
	}
	return parityEvidence{ResultJSON: data, Outcome: "rolled_back", RowsReturned: summary.Survivors, RowsConsumed: int64(len(values)),
		DecodedStatementRows: map[int][][]any{6: verificationRows},
		Mutation: &mutationEvidence{Durability: batch.Durability, Inputs: append([]rasql.InputOutcome(nil), batch.Inputs...),
			Failed: append([]int(nil), batch.FailedBatch...), Statements: []mutationStatementEvidence{
				{AffectedValid: true, Affected: 200}, {},
				{AffectedValid: true, Affected: sentinelOutcome.Affected},
			}}}, err
}

func constraintClassifiers(engine string) []dberror.Classifier {
	switch engine {
	case "postgresql":
		return []dberror.Classifier{pgerr.New()}
	case "mysql":
		return []dberror.Classifier{mysqlerr.New()}
	default:
		return []dberror.Classifier{sqliteerr.New()}
	}
}

func sqlEarlyExit(ctx context.Context, db *sql.DB, engine string, observer *handwrittenObserver) (parityEvidence, error) {
	taskSQL := BindSQL(engine, "SELECT id, project_id, assignee_id, title, is_open, due_on, created_at FROM tasks WHERE project_id = ? ORDER BY id")
	rows, err := sqlQuery(ctx, observer, db, roleRead, "early_exit", 0, taskSQL, int64(1))
	if err != nil {
		return parityEvidence{}, err
	}
	if !rows.Next() {
		_ = rows.Close()
		return parityEvidence{}, sql.ErrNoRows
	}
	var id, projectID int64
	var assignee sql.NullInt64
	var title string
	var open bool
	var dueOn, createdAt sql.NullTime
	if err := rows.Scan(&id, &projectID, &assignee, &title, &open, &dueOn, &createdAt); err != nil {
		_ = rows.Close()
		return parityEvidence{}, err
	}
	if err := rows.Close(); err != nil {
		return parityEvidence{}, err
	}
	projectSQL := BindSQL(engine, "SELECT id, name FROM projects WHERE id = ? ORDER BY id")
	var recoveredID int64
	var name string
	if err := sqlVerifyQuery(ctx, observer, db, "early_exit", 0, projectSQL, []any{&recoveredID, &name}, int64(1)); err != nil {
		return parityEvidence{}, err
	}
	if recoveredID != 1 || name != "project-001" {
		return parityEvidence{}, fmt.Errorf("early exit recovery returned %d/%q", recoveredID, name)
	}
	data, err := marshalResult(id)
	return parityEvidence{ResultJSON: data, Outcome: "early_close", RowsReturned: 1, RowsConsumed: 2, Invocations: []InvocationRecord{{SQL: taskSQL, Kind: "query", Rows: 1, Args: []any{int64(1)}}, {SQL: projectSQL, Kind: "query", Rows: 1, Args: []any{int64(1)}}}}, err
}

func rasqlEarlyExit(ctx context.Context, executor rasql.Executor, db rasql.DB, _ string) (parityEvidence, error) {
	f, err := newTypedFixture()
	if err != nil {
		return parityEvidence{}, err
	}
	queryValue, err := typedTaskQuery(f, 1, false)
	if err != nil {
		return parityEvidence{}, err
	}
	sequence, err := rasql.Rows(ctx, executor, queryValue)
	if err != nil {
		return parityEvidence{}, err
	}
	var first taskRow
	consumed := int64(0)
	sequence(func(row taskRow, rowErr error) bool {
		if rowErr == nil {
			first = row
			consumed++
		}
		return false
	})
	next, err := rasql.One(ctx, executor, typedProjectQueryMust(f, 1))
	if err != nil {
		return parityEvidence{}, err
	}
	_ = db
	data, err := marshalResult(first.ID)
	return parityEvidence{
		ResultJSON: data, Outcome: "early_close", RowsReturned: 1, RowsConsumed: consumed + 1,
		DecodedStatementRows: map[int][][]any{
			0: {{first.ID, first.ProjectID, nullableValue(first.AssigneeID), first.Title, first.Open, nullableValue(first.DueOn), first.CreatedAt}},
			1: {{next.ID, next.Name}},
		},
	}, err
}

func typedProjectQueryMust(f typedFixture, id int64) rasql.Query[projectRow] {
	value, err := typedProjectQuery(f, id)
	if err != nil {
		panic(err)
	}
	return value
}

type cancellationResult struct {
	Stage              string `json:"stage"`
	Started            []int  `json:"started"`
	Error              string `json:"error"`
	RowsClosed         bool   `json:"rows_closed"`
	Partial            bool   `json:"partial"`
	RecoveredProjectID int64  `json:"recovered_project_id"`
}

var cancellationStages = []string{"root", "tasks", "assignees"}

func sqlCancellation(ctx context.Context, db *sql.DB, engine string, observer *handwrittenObserver) (parityEvidence, error) {
	results := make([]cancellationResult, 0, len(cancellationStages))
	for target, stage := range cancellationStages {
		parent := "cancellation/" + stage + "/graph"
		projects, taskRows, assigneeIDs, err := runSQLGraphUntilCancellation(ctx, db, engine, observer, parent, target)
		if !errors.Is(err, context.Canceled) {
			return parityEvidence{}, fmt.Errorf("SQL cancellation at %s: %w", stage, err)
		}
		var recovered projectRow
		statement := BindSQL(engine, "SELECT id, name FROM projects WHERE id = ? ORDER BY id")
		if err := sqlVerifyQuery(ctx, observer, db, "cancellation/"+stage+"/recovery", 0, statement,
			[]any{&recovered.ID, &recovered.Name}, int64(1)); err != nil {
			return parityEvidence{}, err
		}
		if recovered.ID != 1 || recovered.Name != "project-001" {
			return parityEvidence{}, fmt.Errorf("SQL cancellation recovery returned %#v", recovered)
		}
		results = append(results, cancellationResult{
			Stage: stage, Started: cancellationStarted(target), Error: "canceled", RowsClosed: true,
			Partial:            len(projects) != 0 || len(taskRows) != 0 || len(assigneeIDs) != 0,
			RecoveredProjectID: recovered.ID,
		})
	}
	data, err := marshalResult(results)
	return parityEvidence{ResultJSON: data, Outcome: "canceled", RowsReturned: 0, RowsConsumed: 3503}, err
}

func runSQLGraphUntilCancellation(
	ctx context.Context,
	db *sql.DB,
	engine string,
	observer *handwrittenObserver,
	parent string,
	target int,
) ([]projectRow, []taskRow, []int64, error) {
	rootContext, rootCancel := cancellationContext(ctx, target == 0)
	defer rootCancel()
	rootRows, err := sqlQuery(rootContext, observer, db, roleRoot, parent, 0,
		BindSQL(engine, "SELECT id, name FROM projects ORDER BY id"))
	if err != nil {
		return nil, nil, nil, err
	}
	projects := make([]projectRow, 0, 500)
	for rootRows.Next() {
		var project projectRow
		if err := rootRows.Scan(&project.ID, &project.Name); err != nil {
			_ = rootRows.Close()
			return nil, nil, nil, err
		}
		projects = append(projects, project)
	}
	if err := errors.Join(rootRows.Err(), rootRows.Close()); err != nil {
		return nil, nil, nil, err
	}

	projectIDs := make([]int64, len(projects))
	for index, project := range projects {
		projectIDs[index] = project.ID
	}
	taskSQL, taskArgs := graphTaskSelection(engine, projectIDs)
	taskContext, taskCancel := cancellationContext(ctx, target == 1)
	defer taskCancel()
	rows, err := sqlQuery(taskContext, observer, db, roleTasks, parent, 1, taskSQL, taskArgs...)
	if err != nil {
		return nil, nil, nil, err
	}
	tasks := make([]taskRow, 0, 2500)
	assigneeIDs := make([]int64, 0, 2500)
	for rows.Next() {
		var task taskRow
		var assignee sql.NullInt64
		if err := rows.Scan(&task.ProjectID, &task.ID, &task.Title, &assignee); err != nil {
			_ = rows.Close()
			return nil, nil, nil, err
		}
		task.AssigneeID = rasql.Nullable[int64]{Value: assignee.Int64, Valid: assignee.Valid}
		tasks = append(tasks, task)
		if task.AssigneeID.Valid {
			assigneeIDs = append(assigneeIDs, task.AssigneeID.Value)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, nil, nil, err
	}

	assigneeContext, assigneeCancel := cancellationContext(ctx, target == 2)
	defer assigneeCancel()
	var invocations []InvocationRecord
	if _, err := sqlMembers(assigneeContext, observer, db, engine, assigneeIDs, parent, 2, &invocations); err != nil {
		return nil, nil, nil, err
	}
	return projects, tasks, assigneeIDs, errors.New("SQL graph cancellation target was not reached")
}

func cancellationContext(ctx context.Context, cancel bool) (context.Context, context.CancelFunc) {
	if cancel {
		callContext, stop := context.WithCancel(ctx)
		stop()
		return callContext, func() {}
	}
	return ctx, func() {}
}

func rasqlCancellation(ctx context.Context, executor rasql.Executor, _ rasql.DB, _ string) (parityEvidence, error) {
	fixture, err := newTypedFixture()
	if err != nil {
		return parityEvidence{}, err
	}
	results := make([]cancellationResult, 0, len(cancellationStages))
	decoded := make(map[int][][]any)
	physicalOrdinal := 0
	for target, stage := range cancellationStages {
		capture := &graphRowCapture{}
		plan, err := typedGraphPlan(fixture, 0, capture)
		if err != nil {
			return parityEvidence{}, err
		}
		trace := &cancellationTrace{Target: target}
		callContext := context.WithValue(ctx, cancellationTraceKey{}, trace)
		callContext = context.WithValue(callContext, cancellationStatementKey{}, target)
		values, runErr := rasql.LoadGraph(callContext, executor, plan)
		if !errors.Is(runErr, context.Canceled) {
			return parityEvidence{}, fmt.Errorf("rasql cancellation at %s: %w", stage, runErr)
		}
		if len(values) != 0 || !trace.TargetCanceled || !trace.GraphCanceled {
			return parityEvidence{}, fmt.Errorf("rasql cancellation at %s returned partial output or incomplete terminals", stage)
		}
		if !equalInts(trace.Started, cancellationStarted(target)) {
			return parityEvidence{}, fmt.Errorf("rasql cancellation at %s started %v", stage, trace.Started)
		}
		if target > 0 {
			decoded[physicalOrdinal] = decodedProjectRows(capture.Projects)
		}
		if target > 1 {
			decoded[physicalOrdinal+1] = decodedTaskGraphRows(capture.Tasks)
		}
		physicalOrdinal += target + 1
		recovered, err := rasql.One(ctx, executor, typedProjectQueryMust(fixture, 1))
		if err != nil {
			return parityEvidence{}, err
		}
		if recovered.ID != 1 || recovered.Name != "project-001" {
			return parityEvidence{}, fmt.Errorf("rasql cancellation recovery returned %#v", recovered)
		}
		decoded[physicalOrdinal] = [][]any{{recovered.ID, recovered.Name}}
		physicalOrdinal++
		results = append(results, cancellationResult{
			Stage: stage, Started: append([]int(nil), trace.Started...), Error: "canceled", RowsClosed: true,
			Partial: false, RecoveredProjectID: recovered.ID,
		})
	}
	data, err := marshalResult(results)
	return parityEvidence{
		ResultJSON: data, Outcome: "canceled", RowsReturned: 0, RowsConsumed: 3503,
		DecodedStatementRows: decoded,
	}, err
}

func cancellationStarted(target int) []int {
	result := make([]int, target+1)
	for index := range result {
		result[index] = index
	}
	return result
}

func equalInts(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sqlGraphPage(ctx context.Context, db *sql.DB, engine string, observer *handwrittenObserver) (parityEvidence, error) {
	values := make([]projectGraph, 0, 50)
	invocations := make([]InvocationRecord, 0, 15)
	for after := int64(0); len(values) < 50; {
		rootArgs := make([]any, 0, 52)
		rootPredicates := make([]string, 0, 50)
		for id := int64(1); id <= 50; id++ {
			rootPredicates = append(rootPredicates, "id = ?")
			rootArgs = append(rootArgs, id)
		}
		rootSQL := "SELECT id, name FROM projects WHERE (" + strings.Join(rootPredicates, " OR ") + ")"
		if after > 0 {
			rootSQL += " AND id > ?"
			rootArgs = append(rootArgs, after)
		}
		rootSQL += " ORDER BY id LIMIT ?"
		rootArgs = append(rootArgs, 11)
		rootSQL = BindSQL(engine, rootSQL)
		parent := fmt.Sprintf("taskboard_graph_page/page-%d", len(values)/10)
		statementIndex := 0
		rootRows, err := sqlQuery(ctx, observer, db, roleRoot, parent, statementIndex, rootSQL, rootArgs...)
		if err != nil {
			return parityEvidence{}, err
		}
		pageRoots := make([]projectGraph, 0, 10)
		for len(pageRoots) < 11 && rootRows.Next() {
			var row projectGraph
			if err := rootRows.Scan(&row.ID, &row.Name); err != nil {
				_ = rootRows.Close()
				return parityEvidence{}, err
			}
			pageRoots = append(pageRoots, row)
		}
		if err := rootRows.Err(); err != nil {
			_ = rootRows.Close()
			return parityEvidence{}, err
		}
		_ = rootRows.Close()
		if len(pageRoots) == 0 {
			break
		}
		kept := pageRoots
		if len(kept) > 10 {
			kept = kept[:10]
		}
		invocations = append(invocations, InvocationRecord{SQL: rootSQL, Kind: "query", Phase: "consumption", Rows: int64(len(pageRoots)), Args: rootArgs})
		projectIDs := make([]int64, 0, len(kept))
		for _, project := range kept {
			projectIDs = append(projectIDs, project.ID)
		}
		taskSQL, taskArgs := graphTaskSelection(engine, projectIDs)
		taskRows, err := sqlQuery(ctx, observer, db, roleTasks, parent, statementIndex+1, taskSQL, taskArgs...)
		if err != nil {
			return parityEvidence{}, err
		}
		assigneeIDs := make([]int64, 0, 50)
		byID := make(map[int64]int, len(kept))
		for index := range kept {
			byID[kept[index].ID] = index
			kept[index].Tasks.Loaded = true
		}
		for taskRows.Next() {
			var projectID, taskID int64
			var title string
			var assignee sql.NullInt64
			if err := taskRows.Scan(&projectID, &taskID, &title, &assignee); err != nil {
				_ = taskRows.Close()
				return parityEvidence{}, err
			}
			value := taskGraph{ID: taskID, Title: title}
			if assignee.Valid {
				value.Assignee = rasql.LoadedOne[memberRow]{Loaded: true, Present: true, Value: &memberRow{ID: assignee.Int64}}
				assigneeIDs = append(assigneeIDs, assignee.Int64)
			} else {
				value.Assignee = rasql.LoadedOne[memberRow]{Loaded: true}
			}
			kept[byID[projectID]].Tasks.Values = append(kept[byID[projectID]].Tasks.Values, value)
		}
		if err := taskRows.Err(); err != nil {
			_ = taskRows.Close()
			return parityEvidence{}, err
		}
		_ = taskRows.Close()
		invocations = append(invocations, InvocationRecord{SQL: taskSQL, Kind: "query", Phase: "consumption", Rows: int64(len(assigneeIDs) + countTasks(kept) - len(assigneeIDs)), Args: taskArgs})
		members, err := sqlMembers(ctx, observer, db, engine, assigneeIDs, parent, statementIndex+2, &invocations)
		if err != nil {
			return parityEvidence{}, err
		}
		for index := range kept {
			for taskIndex := range kept[index].Tasks.Values {
				id := kept[index].Tasks.Values[taskIndex].Assignee
				if id.Present && id.Value != nil {
					value := members[id.Value.ID]
					kept[index].Tasks.Values[taskIndex].Assignee.Value = &value
				}
			}
		}
		values = append(values, kept...)
		after = kept[len(kept)-1].ID
	}
	data, err := marshalResult(values)
	return parityEvidence{ResultJSON: data, Outcome: "50 parents", RowsReturned: int64(len(values)), RowsConsumed: sumInvocationRows(invocations), Invocations: invocations}, err
}

func rasqlGraphPage(ctx context.Context, executor rasql.Executor, _ rasql.DB, _ string) (parityEvidence, error) {
	fixture, err := newTypedFixture()
	if err != nil {
		return parityEvidence{}, err
	}
	capture := &graphRowCapture{}
	plan, err := typedGraphPlan(fixture, 50, capture)
	if err != nil {
		return parityEvidence{}, err
	}
	projects := mustSource(fixture.projects, "p")
	projectID, err := rasql.BindColumn[projectRow, int64](projects, "id", "")
	if err != nil {
		return parityEvidence{}, err
	}
	pageKey := rasql.AscKey(projectID.Expr(), func(row projectRow) int64 { return row.ID })
	spec, err := rasql.NewPageSpec([]rasql.PageKey[projectRow]{pageKey}, pageKey)
	if err != nil {
		return parityEvidence{}, err
	}
	values := make([]projectGraph, 0, 50)
	decoded := make(map[int][][]any, 15)
	physicalOrdinal := 0
	var after rasql.Cursor
	for len(values) < 50 {
		capture.Projects = nil
		capture.Tasks = nil
		capture.Members = nil
		page, pageErr := rasql.PageGraphAfter(ctx, executor, plan, spec,
			rasql.PagePolicy{DefaultLimit: 10, MaxLimit: 10}, rasql.PageRequest{Limit: 10, After: after})
		if pageErr != nil {
			return parityEvidence{}, pageErr
		}
		decoded[physicalOrdinal] = decodedProjectRows(capture.Projects)
		decoded[physicalOrdinal+1] = decodedTaskGraphRows(capture.Tasks)
		decoded[physicalOrdinal+2] = decodedMemberRows(capture.Members)
		physicalOrdinal += 3
		values = append(values, page.Values...)
		if !page.HasMore {
			break
		}
		after = page.Next
	}
	data, err := marshalResult(values)
	return parityEvidence{
		ResultJSON: data, Outcome: "50 parents", RowsReturned: int64(len(values)), RowsConsumed: int64(len(values)),
		DecodedStatementRows: decoded,
	}, err
}

func sqlGraphAll(ctx context.Context, db *sql.DB, engine string, observer *handwrittenObserver) (parityEvidence, error) {
	values := make([]projectGraph, 0, 500)
	invocations := make([]InvocationRecord, 0, 5)
	rootSQL := BindSQL(engine, "SELECT id, name FROM projects ORDER BY id")
	rootRows, err := sqlQuery(ctx, observer, db, roleRoot, "graph_500_parent_limit", 0, rootSQL)
	if err != nil {
		return parityEvidence{}, err
	}
	for rootRows.Next() {
		var row projectGraph
		if err := rootRows.Scan(&row.ID, &row.Name); err != nil {
			_ = rootRows.Close()
			return parityEvidence{}, err
		}
		row.Tasks.Loaded = true
		values = append(values, row)
	}
	if err := rootRows.Err(); err != nil {
		_ = rootRows.Close()
		return parityEvidence{}, err
	}
	if err := rootRows.Close(); err != nil {
		return parityEvidence{}, err
	}
	invocations = append(invocations, InvocationRecord{SQL: rootSQL, Kind: "query", Phase: "consumption", Rows: int64(len(values))})
	projectIDs := make([]int64, len(values))
	for index := range values {
		projectIDs[index] = values[index].ID
	}
	taskSQL, taskArgs := graphTaskSelection(engine, projectIDs)
	rows, err := sqlQuery(ctx, observer, db, roleTasks, "graph_500_parent_limit", 1, taskSQL, taskArgs...)
	if err != nil {
		return parityEvidence{}, err
	}
	assigneeIDs := make([]int64, 0, 2500)
	var taskCount int64
	for rows.Next() {
		var projectID, taskID int64
		var title string
		var assignee sql.NullInt64
		if err := rows.Scan(&projectID, &taskID, &title, &assignee); err != nil {
			_ = rows.Close()
			return parityEvidence{}, err
		}
		taskCount++
		value := taskGraph{ID: taskID, Title: title}
		if assignee.Valid {
			value.Assignee = rasql.LoadedOne[memberRow]{Loaded: true, Present: true, Value: &memberRow{ID: assignee.Int64}}
			assigneeIDs = append(assigneeIDs, assignee.Int64)
		} else {
			value.Assignee = rasql.LoadedOne[memberRow]{Loaded: true}
		}
		if projectID < 1 || projectID > int64(len(values)) {
			_ = rows.Close()
			return parityEvidence{}, fmt.Errorf("task row references unknown project %d", projectID)
		}
		values[projectID-1].Tasks.Values = append(values[projectID-1].Tasks.Values, value)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return parityEvidence{}, err
	}
	_ = rows.Close()
	invocations = append(invocations, InvocationRecord{SQL: taskSQL, Kind: "query", Phase: "consumption", Rows: taskCount, Args: taskArgs})
	members, err := sqlMembers(ctx, observer, db, engine, assigneeIDs, "graph_500_parent_limit", 2, &invocations)
	if err != nil {
		return parityEvidence{}, err
	}
	for index := range values {
		for taskIndex := range values[index].Tasks.Values {
			assignee := values[index].Tasks.Values[taskIndex].Assignee
			if assignee.Present && assignee.Value != nil {
				value := members[assignee.Value.ID]
				values[index].Tasks.Values[taskIndex].Assignee.Value = &value
			}
		}
	}
	data, err := marshalResult(values)
	return parityEvidence{ResultJSON: data, Outcome: "500 parents", RowsReturned: int64(len(values)), RowsConsumed: sumInvocationRows(invocations), Invocations: invocations}, err
}

func graphTaskSelection(engine string, projectIDs []int64) (string, []any) {
	placeholders := make([]string, 0, len(projectIDs))
	args := make([]any, 0, len(projectIDs)+2)
	args = append(args, true)
	for _, projectID := range projectIDs {
		placeholders = append(placeholders, "?")
		args = append(args, projectID)
	}
	args = append(args, int64(5))
	statement := "SELECT project_id, id, title, assignee_id FROM (SELECT project_id, id, title, assignee_id, ROW_NUMBER() OVER (PARTITION BY project_id ORDER BY id) AS d4_rank FROM tasks WHERE is_open = ? AND project_id IN (" + strings.Join(placeholders, ",") + ")) AS d4_tasks WHERE d4_rank <= ? ORDER BY project_id, id"
	return BindSQL(engine, statement), args
}

func countTasks(values []projectGraph) int {
	total := 0
	for _, value := range values {
		total += len(value.Tasks.Values)
	}
	return total
}
func sumInvocationRows(records []InvocationRecord) int64 {
	var total int64
	for _, record := range records {
		total += record.Rows
	}
	return total
}

func sqlMembers(ctx context.Context, observer *handwrittenObserver, db *sql.DB, engine string, ids []int64, parent string, statementIndex int, invocations *[]InvocationRecord) (map[int64]memberRow, error) {
	result := make(map[int64]memberRow, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	unique := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			unique = append(unique, id)
		}
	}
	chunkSize := len(unique)
	if maxBind, ok := ctx.Value(profileLimitContextKey{}).(int); ok {
		chunkSize = graphBindChunkSize(maxBind, 1, 1)
		if chunkSize == 0 {
			return nil, fmt.Errorf("profile bind limit %d cannot fit member query", maxBind)
		}
	}
	for start := 0; start < len(unique); start += chunkSize {
		end := start + chunkSize
		if end > len(unique) {
			end = len(unique)
		}
		placeholders := make([]string, end-start)
		args := make([]any, end-start)
		for index := range placeholders {
			placeholders[index] = "?"
			args[index] = unique[start+index]
		}
		args = append(args, int64(2))
		statement := "SELECT id, name FROM members WHERE id IN (" + strings.Join(placeholders, ",") + ") AND ? > 0 ORDER BY id"
		rows, err := sqlQuery(ctx, observer, db, roleAssignees, parent, statementIndex, BindSQL(engine, statement), args...)
		if err != nil {
			return nil, err
		}
		var count int64
		for rows.Next() {
			var row memberRow
			if err := rows.Scan(&row.ID, &row.Name); err != nil {
				_ = rows.Close()
				return nil, err
			}
			result[row.ID] = row
			count++
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
		*invocations = append(*invocations, InvocationRecord{SQL: BindSQL(engine, statement), Kind: "query", Phase: "consumption", Rows: count, Args: args})
		statementIndex++
	}
	return result, nil
}

func graphBindChunkSize(maxBind, fixedBinds, keyWidth int) int {
	if maxBind <= 0 || fixedBinds < 0 || keyWidth <= 0 || fixedBinds >= maxBind {
		return 0
	}
	return (maxBind - fixedBinds) / keyWidth
}

func rasqlGraphAll(ctx context.Context, executor rasql.Executor, _ rasql.DB, engine string) (parityEvidence, error) {
	fixture, err := newTypedFixture()
	if err != nil {
		return parityEvidence{}, err
	}
	capture := &graphRowCapture{}
	plan, err := typedGraphPlan(fixture, 0, capture)
	if err != nil {
		return parityEvidence{}, err
	}
	values, err := rasql.LoadGraph(ctx, executor, plan)
	if err != nil {
		return parityEvidence{}, err
	}
	data, err := marshalResult(values)
	decoded := map[int][][]any{
		0: decodedProjectRows(capture.Projects),
		1: decodedTaskGraphRows(capture.Tasks),
	}
	memberChunk := len(capture.Members)
	if engine == "sqlite" {
		memberChunk = 998
	}
	for start, ordinal := 0, 2; start < len(capture.Members); ordinal++ {
		end := start + memberChunk
		if end > len(capture.Members) {
			end = len(capture.Members)
		}
		decoded[ordinal] = decodedMemberRows(capture.Members[start:end])
		start = end
	}
	return parityEvidence{
		ResultJSON: data, Outcome: "500 parents", RowsReturned: int64(len(values)), RowsConsumed: int64(len(values)),
		DecodedStatementRows: decoded,
	}, err
}

func decodedProjectRows(values []projectRow) [][]any {
	rows := make([][]any, len(values))
	for index, value := range values {
		rows[index] = []any{value.ID, value.Name}
	}
	return rows
}

func decodedTaskGraphRows(values []taskRow) [][]any {
	rows := make([][]any, len(values))
	for index, value := range values {
		rows[index] = []any{value.ProjectID, value.ID, value.Title, nullableValue(value.AssigneeID)}
	}
	return rows
}

func decodedMemberRows(values []memberRow) [][]any {
	rows := make([][]any, len(values))
	for index, value := range values {
		rows[index] = []any{value.ID, value.Name}
	}
	return rows
}
