package conformance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/schema"
)

type projectRow struct {
	ID   int64  `rasql:"id"`
	Name string `rasql:"name"`
}

type taskRow struct {
	ID         int64
	ProjectID  int64
	AssigneeID rasql.Nullable[int64]
	Title      string
	Open       bool
	DueOn      rasql.Nullable[time.Time]
	CreatedAt  time.Time
}

type memberRow struct {
	ID   int64  `rasql:"id"`
	Name string `rasql:"name"`
}

type projectGraph struct {
	ID    int64
	Name  string
	Tasks rasql.LoadedMany[taskGraph]
}

type taskGraph struct {
	ID       int64
	Title    string
	Assignee rasql.LoadedOne[memberRow]
}

type resultDecoder[R any] struct {
	schema  rasql.ResultSchema
	scan    func(rasql.ScanSource, *R) error
	observe func(R)
}

type overdueDecoder struct{}

func (overdueDecoder) ResultSchema() rasql.ResultSchema {
	schemaValue, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "title", Type: schema.TextType{}},
	)
	if err != nil {
		panic(err)
	}
	return schemaValue
}

func (overdueDecoder) Presence() []rasql.Presence { return nil }
func (overdueDecoder) DecodeRow(src rasql.ScanSource, row *overdueRow) error {
	return src.Scan(&row.ID, &row.Title)
}

func OverdueTasks(engine string, projectID int64, open bool, cutoff time.Time) (rasql.Query[overdueRow], error) {
	sqlText := "SELECT id, title FROM tasks WHERE project_id = ? AND is_open = ? AND due_on IS NOT NULL AND due_on < ?"
	projection, err := rasql.NativeProjection[overdueRow](overdueDecoder{})
	if err != nil {
		return rasql.Query[overdueRow]{}, err
	}
	sqlText += " ORDER BY id"
	return rasql.Native(rasql.NativeStatement{Engine: engine, SQL: BindSQL(engine, sqlText), Args: []rasql.NativeArgument{{Value: projectID}, {Value: open}, {Value: cutoff}}}, projection, rasql.Many)
}

func generatedOverdueQuery(engine string, cardinality rasql.Cardinality, variant string) (rasql.Query[overdueRow], error) {
	if variant == "many" {
		return generatedOverdueNativeQuery(engine, cardinality, "many")
	}
	sqlText := "SELECT id, title FROM tasks WHERE project_id = ? AND is_open = TRUE AND due_on IS NOT NULL AND due_on < '2024-01-04'"
	switch variant {
	case "one":
		sqlText += " AND id = 1"
	case "maybe":
		sqlText += " AND id < 0"
	case "many":
	default:
		return rasql.Query[overdueRow]{}, fmt.Errorf("unknown overdue query variant %q", variant)
	}
	sqlText += " ORDER BY id"
	projection, err := rasql.NativeProjection[overdueRow](overdueDecoder{})
	if err != nil {
		return rasql.Query[overdueRow]{}, err
	}
	return rasql.Native(rasql.NativeStatement{Engine: engine, SQL: BindSQL(engine, sqlText), Args: []rasql.NativeArgument{{Value: int64(1)}}}, projection, cardinality)
}

func generatedOverdueNativeQuery(engine string, cardinality rasql.Cardinality, variant string) (rasql.Query[overdueRow], error) {
	_ = variant
	sqlText := "SELECT id, title FROM tasks WHERE project_id = ? AND is_open = TRUE AND due_on IS NOT NULL AND due_on < '2024-01-04' ORDER BY id"
	projection, err := rasql.NativeProjection[overdueRow](overdueDecoder{})
	if err != nil {
		return rasql.Query[overdueRow]{}, err
	}
	return rasql.Native(rasql.NativeStatement{Engine: engine, SQL: BindSQL(engine, sqlText), Args: []rasql.NativeArgument{{Value: int64(1)}}}, projection, cardinality)
}

func (d resultDecoder[R]) ResultSchema() rasql.ResultSchema { return d.schema }
func (d resultDecoder[R]) Presence() []rasql.Presence       { return nil }
func (d resultDecoder[R]) DecodeRow(src rasql.ScanSource, row *R) error {
	if err := d.scan(src, row); err != nil {
		return err
	}
	if d.observe != nil {
		d.observe(*row)
	}
	return nil
}

type parityEvidence struct {
	ResultJSON               []byte
	Outcome                  string
	Invocations              []InvocationRecord
	Observations             []statementObservation
	Events                   []EventRecord
	CorrelatedEvents         []correlatedEvent
	RowsReturned             int64
	RowsConsumed             int64
	MeasuredRowsConsumed     int64
	VerificationRowsConsumed int64
	DecodedRows              [][]any
	DecodedStatementRows     map[int][][]any
	Mutation                 *mutationEvidence
}

type mutationEvidence struct {
	Affected   int64
	Durability rasql.Durability
	Inputs     []rasql.InputOutcome
	Failed     []int
	Statements []mutationStatementEvidence
}

type mutationStatementEvidence struct {
	AffectedValid bool
	Affected      int64
}

func invocationRecords(observations []statementObservation) []InvocationRecord {
	records := make([]InvocationRecord, len(observations))
	for index, observation := range observations {
		kind := observation.Kind
		// The legacy parity projection still compares verification reads. The
		// strict statement model will exclude them after both paths expose roles.
		records[index] = InvocationRecord{
			SQL: observation.SQL, Args: append([]any(nil), observation.Args...), Kind: kind,
			Phase: observation.Phase, Rows: observation.RowsConsumed, EarlyClose: observation.EarlyClose,
			Err: observation.Err,
		}
	}
	return records
}

// WorkloadEvidence is the shared semantic result used by conformance and benchmark lanes.
// Callers should populate canonical result bytes, outcome, ordered invocations, and row counts.
type WorkloadEvidence = parityEvidence

// CompareWorkloadEvidence rejects any semantic or ordered invocation difference before timing.
func CompareWorkloadEvidence(workload string, left, right WorkloadEvidence) error {
	return compareParity(workload, left, right)
}

func (e parityEvidence) StatementCount() int {
	if len(e.Observations) > 0 {
		return len(measuredObservations(e.Observations))
	}
	return len(invocationStatements(e.Invocations))
}

func (e parityEvidence) SQLDigest() string {
	hash := sha256.New()
	records := invocationStatements(e.Invocations)
	if len(e.Observations) > 0 {
		records = make([]InvocationRecord, 0, len(e.Observations))
		for _, observation := range measuredObservations(e.Observations) {
			records = append(records, InvocationRecord{SQL: observation.SQL})
		}
	}
	for _, record := range records {
		var length [8]byte
		for index := range length {
			length[len(length)-1-index] = byte(len(record.SQL) >> (8 * index))
		}
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(record.SQL))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func invocationStatements(records []InvocationRecord) []InvocationRecord {
	result := make([]InvocationRecord, 0, len(records))
	for _, record := range records {
		if record.Kind != "query" && record.Kind != "exec" {
			continue
		}
		result = append(result, record)
	}
	return result
}

func (e parityEvidence) validate() error {
	if len(e.ResultJSON) == 0 {
		return fmt.Errorf("empty canonical result")
	}
	if e.RowsReturned < 0 || e.RowsConsumed < 0 || e.MeasuredRowsConsumed < 0 || e.VerificationRowsConsumed < 0 {
		return fmt.Errorf("negative row count")
	}
	if e.RowsConsumed != e.MeasuredRowsConsumed+e.VerificationRowsConsumed {
		return fmt.Errorf("row counters do not sum: measured=%d verification=%d total=%d", e.MeasuredRowsConsumed, e.VerificationRowsConsumed, e.RowsConsumed)
	}
	for index, record := range e.Invocations {
		if record.Phase == "" {
			return fmt.Errorf("invocation %d has no terminal phase", index)
		}
	}
	var measuredRows, verificationRows int64
	for index, observation := range e.Observations {
		if observation.Verification != (observation.Role == roleVerification) {
			return fmt.Errorf("observation %d role and verification differ", index)
		}
		if strings.TrimSpace(observation.LogicalParent) == "" || observation.StatementIndex < 0 {
			return fmt.Errorf("observation %d parent or index is invalid", index)
		}
		if observation.Kind != "query" && observation.Kind != "exec" {
			return fmt.Errorf("observation %d kind %q is invalid", index, observation.Kind)
		}
		if !observation.Started || !observation.Completed || observation.CompletionCount != 1 {
			return fmt.Errorf("observation %d lifecycle is incomplete", index)
		}
		phases := observationPhases(observation)
		if len(phases) == 0 || (observation.Kind == "exec" && len(phases) != 1) ||
			(observation.Kind == "query" && observation.Err == nil && !observation.EarlyClose && len(phases) != 2) {
			return fmt.Errorf("observation %d phases are invalid", index)
		}
		if observation.RowsConsumed < 0 {
			return fmt.Errorf("observation %d has negative consumed rows", index)
		}
		if observation.Verification {
			verificationRows += observation.RowsConsumed
			continue
		}
		if observation.Role != roleSavepointBegin && observation.Role != roleSavepointRollback && observation.Role != roleSavepointRelease {
			measuredRows += observation.RowsConsumed
		}
	}
	if len(e.Observations) > 0 && measuredRows != e.MeasuredRowsConsumed {
		return fmt.Errorf("measured observation rows %d differ from counter %d", measuredRows, e.MeasuredRowsConsumed)
	}
	if len(e.Observations) > 0 && verificationRows != e.VerificationRowsConsumed {
		return fmt.Errorf("verification observation rows %d differ from counter %d", verificationRows, e.VerificationRowsConsumed)
	}
	return nil
}

func compareParity(workload string, left, right parityEvidence) error {
	if err := left.validate(); err != nil {
		return fmt.Errorf("%s rasql evidence: %w", workload, err)
	}
	if err := right.validate(); err != nil {
		return fmt.Errorf("%s database/sql evidence: %w", workload, err)
	}
	if string(left.ResultJSON) != string(right.ResultJSON) {
		return fmt.Errorf("%s canonical result differs: rasql=%s sql=%s", workload, left.ResultJSON, right.ResultJSON)
	}
	if left.Outcome != right.Outcome {
		return fmt.Errorf("%s outcome differs: rasql=%q sql=%q", workload, left.Outcome, right.Outcome)
	}
	if left.RowsReturned != right.RowsReturned || left.RowsConsumed != right.RowsConsumed {
		return fmt.Errorf("%s row evidence differs: rasql=%d/%d sql=%d/%d", workload, left.RowsReturned, left.RowsConsumed, right.RowsReturned, right.RowsConsumed)
	}
	if left.MeasuredRowsConsumed != right.MeasuredRowsConsumed || left.VerificationRowsConsumed != right.VerificationRowsConsumed {
		return fmt.Errorf("%s measured and verification rows differ: rasql=%d/%d sql=%d/%d", workload, left.MeasuredRowsConsumed, left.VerificationRowsConsumed, right.MeasuredRowsConsumed, right.VerificationRowsConsumed)
	}
	if !reflect.DeepEqual(left.Mutation, right.Mutation) {
		return fmt.Errorf("%s mutation evidence differs: %#v versus %#v", workload, left.Mutation, right.Mutation)
	}
	if left.StatementCount() == 0 || right.StatementCount() == 0 {
		return fmt.Errorf("%s has no observed statements", workload)
	}
	if len(left.Observations) > 0 || len(right.Observations) > 0 {
		if len(left.Observations) == 0 || len(right.Observations) == 0 {
			return fmt.Errorf("%s observation model is missing on one side", workload)
		}
		if err := compareObservations(workload, measuredObservations(left.Observations), measuredObservations(right.Observations)); err != nil {
			return err
		}
		if err := compareObservations(workload, scopeObservations(left.Observations), scopeObservations(right.Observations)); err != nil {
			return fmt.Errorf("%s scope: %w", workload, err)
		}
		if err := compareObservations(workload, verificationObservations(left.Observations), verificationObservations(right.Observations)); err != nil {
			return fmt.Errorf("%s verification: %w", workload, err)
		}
		return nil
	}
	leftStatements, rightStatements := invocationStatements(left.Invocations), invocationStatements(right.Invocations)
	if len(leftStatements) != len(rightStatements) {
		return fmt.Errorf("%s invocation count differs: rasql=%d sql=%d", workload, len(leftStatements), len(rightStatements))
	}
	for index := range leftStatements {
		if leftStatements[index].Kind != rightStatements[index].Kind {
			return fmt.Errorf("%s invocation %d differs", workload, index)
		}
		if !reflect.DeepEqual(leftStatements[index].Args, rightStatements[index].Args) {
			return fmt.Errorf("%s invocation %d arguments differ: %s", workload, index, argumentMismatch(leftStatements[index].Args, rightStatements[index].Args))
		}
		if (leftStatements[index].Err != nil) != (rightStatements[index].Err != nil) {
			return fmt.Errorf("%s invocation %d error completion differs", workload, index)
		}
	}
	return nil
}

func measuredObservations(observations []statementObservation) []statementObservation {
	result := make([]statementObservation, 0, len(observations))
	for _, observation := range observations {
		if !observation.Verification && observation.Role != roleSavepointBegin && observation.Role != roleSavepointRollback && observation.Role != roleSavepointRelease {
			result = append(result, observation)
		}
	}
	return result
}

func verificationObservations(observations []statementObservation) []statementObservation {
	result := make([]statementObservation, 0, len(observations))
	for _, observation := range observations {
		if observation.Verification {
			result = append(result, observation)
		}
	}
	return result
}

func scopeObservations(observations []statementObservation) []statementObservation {
	result := make([]statementObservation, 0, len(observations))
	for _, observation := range observations {
		if isScopeRole(observation.Role) {
			result = append(result, observation)
		}
	}
	return result
}

func compareObservations(workload string, left, right []statementObservation) error {
	if len(left) != len(right) {
		return fmt.Errorf("%s observed statement count differs: rasql=%d sql=%d", workload, len(left), len(right))
	}
	for index := range left {
		l, r := left[index], right[index]
		if l.Role != r.Role || l.Verification != r.Verification ||
			l.LogicalParent != r.LogicalParent || l.StatementIndex != r.StatementIndex {
			return fmt.Errorf("%s statement %d metadata differs: %#v versus %#v", workload, index, l, r)
		}
		if l.Kind != r.Kind || l.Phase != r.Phase || l.RowsConsumed != r.RowsConsumed || l.EarlyClose != r.EarlyClose || !equalStrings(observationPhases(l), observationPhases(r)) {
			return fmt.Errorf("%s statement %d lifecycle differs: %#v versus %#v", workload, index, l, r)
		}
		if l.RowsAffectedValid != r.RowsAffectedValid {
			return fmt.Errorf("%s statement %d affected rows differ: %d versus %d", workload, index, l.RowsAffected, r.RowsAffected)
		}
		if l.RowsAffectedValid && r.RowsAffectedValid && l.RowsAffected != r.RowsAffected {
			return fmt.Errorf("%s statement %d affected rows differ: %d versus %d", workload, index, l.RowsAffected, r.RowsAffected)
		}
		if !l.Started || !l.Completed || l.CompletionCount != 1 || !r.Started || !r.Completed || r.CompletionCount != 1 {
			return fmt.Errorf("%s statement %d is not exactly once", workload, index)
		}
		if !sameArguments(l.Args, r.Args) {
			return fmt.Errorf("%s statement %d arguments differ: %s", workload, index, argumentMismatch(l.Args, r.Args))
		}
		if isScopeRole(l.Role) {
			if !validSavepointOperation(l.Role, l.SQL) || !validSavepointOperation(r.Role, r.SQL) {
				return fmt.Errorf("%s statement %d savepoint SQL differs: %q versus %q", workload, index, l.SQL, r.SQL)
			}
		} else if isGraphRole(l.Role) {
			if err := validateGraphStatementSQL(l.Role, l.SQL, len(l.Args)); err != nil {
				return fmt.Errorf("%s statement %d rasql SQL: %w", workload, index, err)
			}
			if err := validateGraphStatementSQL(r.Role, r.SQL, len(r.Args)); err != nil {
				return fmt.Errorf("%s statement %d database/sql SQL: %w", workload, index, err)
			}
		} else if !reflect.DeepEqual(l.Descriptor, r.Descriptor) {
			return fmt.Errorf("%s statement %d SQL descriptor differs", workload, index)
		} else if reflect.DeepEqual(l.Descriptor, sqlDescriptor{}) {
			leftDescriptor, rightDescriptor := describeSQL(l.SQL), describeSQL(r.SQL)
			if leftDescriptor != rightDescriptor {
				return fmt.Errorf("%s statement %d SQL differs (%s versus %s): %q versus %q", workload, index, leftDescriptor, rightDescriptor, l.SQL, r.SQL)
			}
		}
		if errorClass(l.Err) != errorClass(r.Err) {
			return fmt.Errorf("%s statement %d error completion differs (rasql=%v sql=%v)", workload, index, l.Err, r.Err)
		}
		if !sameRows(l.RowValues, r.RowValues) {
			return fmt.Errorf("%s statement %d row values differ: %#v versus %#v", workload, index, l.RowValues, r.RowValues)
		}
	}
	return nil
}

func isGraphRole(role statementRole) bool {
	return role == roleRoot || role == roleTasks || role == roleAssignees
}

func validateGraphStatementSQL(role statementRole, statement string, argumentCount int) error {
	normalized := describeSQL(statement)
	required := []string{"select ", " order by "}
	switch role {
	case roleRoot:
		required = append(required, " from projects", " id", " name")
	case roleTasks:
		required = append(required,
			" from tasks", " id", " project_id", " title", " assignee_id", "row_number over",
			"partition by project_id", "where", "is_open = ?", "project_id", "<= ?",
		)
	case roleAssignees:
		required = append(required, " from members", " id", " name", "where")
	default:
		return fmt.Errorf("unknown graph role %q", role)
	}
	for _, part := range required {
		if !strings.Contains(normalized, part) {
			return fmt.Errorf("role %q SQL is missing %q", role, part)
		}
	}
	binds := regexp.MustCompile(`\?|\$[0-9]+|@p[0-9]+`).FindAllString(statement, -1)
	if len(binds) != argumentCount {
		return fmt.Errorf("role %q has %d binds for %d arguments", role, len(binds), argumentCount)
	}
	return nil
}

func validSavepointOperation(role statementRole, statement string) bool {
	actual, ok := savepointOperationRole(statement)
	if !ok || actual != role {
		return false
	}
	fields := strings.Fields(statement)
	switch role {
	case roleSavepointBegin:
		return len(fields) == 2 && strings.Trim(fields[1], "`\"") != ""
	case roleSavepointRelease:
		return len(fields) == 3 && strings.EqualFold(fields[1], "SAVEPOINT") &&
			strings.Trim(fields[2], "`\"") != ""
	case roleSavepointRollback:
		return len(fields) == 4 && strings.EqualFold(fields[1], "TO") &&
			strings.EqualFold(fields[2], "SAVEPOINT") && strings.Trim(fields[3], "`\"") != ""
	default:
		return false
	}
}

func observationPhases(observation statementObservation) []string {
	if observation.Kind == "query" && observation.Phase == "consumption" {
		return []string{"execution", "consumption"}
	}
	if observation.Phase == "" {
		return nil
	}
	return []string{observation.Phase}
}

func errorClass(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "cancel"):
		return "canceled"
	case strings.Contains(message, "constraint"), strings.Contains(message, "duplicate"):
		return "constraint"
	case strings.Contains(message, "multiple"):
		return "cardinality"
	default:
		return fmt.Sprintf("%T", err)
	}
}

func sameArguments(left, right []any) bool {
	if (left == nil) != (right == nil) {
		return false
	}
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !reflect.DeepEqual(left[index], right[index]) {
			return false
		}
	}
	return true
}

func argumentMismatch(left, right []any) string {
	if len(left) != len(right) {
		return fmt.Sprintf("length %d/%d", len(left), len(right))
	}
	for index := range left {
		if !reflect.DeepEqual(left[index], right[index]) {
			return fmt.Sprintf("index %d: %T=%#v versus %T=%#v", index, left[index], left[index], right[index], right[index])
		}
	}
	return "unknown"
}

type typedFixture struct {
	projects     rasql.Table[projectRow]
	tasks        rasql.Table[taskRow]
	members      rasql.Table[memberRow]
	projectID    rasql.Column[projectRow, int64]
	projectName  rasql.Column[projectRow, string]
	taskID       rasql.Column[taskRow, int64]
	taskProject  rasql.Column[taskRow, int64]
	taskAssignee rasql.NullColumn[taskRow, int64]
	taskTitle    rasql.Column[taskRow, string]
	taskOpen     rasql.Column[taskRow, bool]
	memberID     rasql.Column[memberRow, int64]
	memberName   rasql.Column[memberRow, string]
}

func newTypedFixture() (typedFixture, error) {
	projects, err := rasql.TableOf[projectRow](schema.TableDef{Name: "projects", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}}, {Name: "name", Type: schema.TextType{}},
	}})
	if err != nil {
		return typedFixture{}, err
	}
	tasks, err := rasql.TableOf[taskRow](schema.TableDef{Name: "tasks", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}}, {Name: "project_id", Type: schema.IntegerType{}},
		{Name: "assignee_id", Type: schema.IntegerType{}, Nullable: true}, {Name: "title", Type: schema.TextType{}},
		{Name: "is_open", Type: schema.BooleanType{}, Default: "true"}, {Name: "due_on", Type: schema.TimeType{}, Nullable: true},
		{Name: "created_at", Type: schema.TimeType{}, Default: "now()"},
	}})
	if err != nil {
		return typedFixture{}, err
	}
	members, err := rasql.TableOf[memberRow](schema.TableDef{Name: "members", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}}, {Name: "name", Type: schema.TextType{}},
	}})
	if err != nil {
		return typedFixture{}, err
	}
	projectID, err := rasql.BindColumn[projectRow, int64](mustSource(projects, "p"), "id", "")
	if err != nil {
		return typedFixture{}, err
	}
	projectName, err := rasql.BindColumn[projectRow, string](mustSource(projects, "p"), "name", "")
	if err != nil {
		return typedFixture{}, err
	}
	taskSource := mustSource(tasks, "t")
	taskID, err := rasql.BindColumn[taskRow, int64](taskSource, "id", "")
	if err != nil {
		return typedFixture{}, err
	}
	taskProject, err := rasql.BindColumn[taskRow, int64](taskSource, "project_id", "")
	if err != nil {
		return typedFixture{}, err
	}
	taskAssignee, err := rasql.BindNullColumn[taskRow, int64](taskSource, "assignee_id", "")
	if err != nil {
		return typedFixture{}, err
	}
	taskTitle, err := rasql.BindColumn[taskRow, string](taskSource, "title", "")
	if err != nil {
		return typedFixture{}, err
	}
	taskOpen, err := rasql.BindColumn[taskRow, bool](taskSource, "is_open", "")
	if err != nil {
		return typedFixture{}, err
	}
	memberSource := mustSource(members, "m")
	memberID, err := rasql.BindColumn[memberRow, int64](memberSource, "id", "")
	if err != nil {
		return typedFixture{}, err
	}
	memberName, err := rasql.BindColumn[memberRow, string](memberSource, "name", "")
	if err != nil {
		return typedFixture{}, err
	}
	return typedFixture{projects: projects, tasks: tasks, members: members,
		projectID: projectID, projectName: projectName, taskID: taskID, taskProject: taskProject,
		taskAssignee: taskAssignee, taskTitle: taskTitle, taskOpen: taskOpen, memberID: memberID, memberName: memberName}, nil
}

func mustSource[T any](table rasql.Table[T], alias string) rasql.TypedRelation[T] {
	relation, err := rasql.SourceOf(table, alias)
	if err != nil {
		panic(err)
	}
	return relation
}

func typedProjectQuery(f typedFixture, id int64) (rasql.Query[projectRow], error) {
	relation := mustSource(f.projects, "p")
	idColumn, err := rasql.BindColumn[projectRow, int64](relation, "id", "")
	if err != nil {
		return rasql.Query[projectRow]{}, err
	}
	nameColumn, err := rasql.BindColumn[projectRow, string](relation, "name", "")
	if err != nil {
		return rasql.Query[projectRow]{}, err
	}
	resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}}, rasql.ResultColumn{Name: "name", Type: schema.TextType{}})
	if err != nil {
		return rasql.Query[projectRow]{}, err
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", idColumn.Expr(), schema.IntegerType{}, ""), rasql.Item("name", nameColumn.Expr(), schema.TextType{}, ""),
	}, resultDecoder[projectRow]{schema: resultSchema, scan: func(src rasql.ScanSource, row *projectRow) error { return src.Scan(&row.ID, &row.Name) }})
	if err != nil {
		return rasql.Query[projectRow]{}, err
	}
	return rasql.Select(relation.Source(), projection).Where(rasql.EqualValue(idColumn.Expr(), id)).OrderBy(rasql.AscExpr(idColumn.Expr())), nil
}

func typedTaskQuery(f typedFixture, projectID int64, openOnly bool, observers ...func(taskRow)) (rasql.Query[taskRow], error) {
	return typedTaskQueryWhere(f, projectID, 0, openOnly, observers...)
}

func typedTaskByID(f typedFixture, idValue int64) (rasql.Query[taskRow], error) {
	return typedTaskQueryWhere(f, 0, idValue, false)
}

func typedTaskQueryWhere(f typedFixture, projectID, idValue int64, openOnly bool, observers ...func(taskRow)) (rasql.Query[taskRow], error) {
	relation := mustSource(f.tasks, "t")
	id, err := rasql.BindColumn[taskRow, int64](relation, "id", "")
	if err != nil {
		return rasql.Query[taskRow]{}, err
	}
	project, err := rasql.BindColumn[taskRow, int64](relation, "project_id", "")
	if err != nil {
		return rasql.Query[taskRow]{}, err
	}
	assignee, err := rasql.BindNullColumn[taskRow, int64](relation, "assignee_id", "")
	if err != nil {
		return rasql.Query[taskRow]{}, err
	}
	title, err := rasql.BindColumn[taskRow, string](relation, "title", "")
	if err != nil {
		return rasql.Query[taskRow]{}, err
	}
	open, err := rasql.BindColumn[taskRow, bool](relation, "is_open", "")
	if err != nil {
		return rasql.Query[taskRow]{}, err
	}
	dueOn, err := rasql.BindNullColumn[taskRow, time.Time](relation, "due_on", "")
	if err != nil {
		return rasql.Query[taskRow]{}, err
	}
	createdAt, err := rasql.BindColumn[taskRow, time.Time](relation, "created_at", "")
	if err != nil {
		return rasql.Query[taskRow]{}, err
	}
	resultSchema, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}}, rasql.ResultColumn{Name: "project_id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "assignee_id", Type: schema.IntegerType{}, Nullable: true}, rasql.ResultColumn{Name: "title", Type: schema.TextType{}},
		rasql.ResultColumn{Name: "is_open", Type: schema.BooleanType{}}, rasql.ResultColumn{Name: "due_on", Type: schema.TimeType{}, Nullable: true},
		rasql.ResultColumn{Name: "created_at", Type: schema.TimeType{}},
	)
	if err != nil {
		return rasql.Query[taskRow]{}, err
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", id.Expr(), schema.IntegerType{}, ""), rasql.Item("project_id", project.Expr(), schema.IntegerType{}, ""),
		rasql.NullItem("assignee_id", assignee.NullExpr(), schema.IntegerType{}, ""), rasql.Item("title", title.Expr(), schema.TextType{}, ""),
		rasql.Item("is_open", open.Expr(), schema.BooleanType{}, ""), rasql.NullItem("due_on", dueOn.NullExpr(), schema.TimeType{}, ""),
		rasql.Item("created_at", createdAt.Expr(), schema.TimeType{}, ""),
	}, resultDecoder[taskRow]{schema: resultSchema, scan: func(src rasql.ScanSource, row *taskRow) error {
		return src.Scan(&row.ID, &row.ProjectID, &row.AssigneeID, &row.Title, &row.Open, &row.DueOn, &row.CreatedAt)
	}, observe: firstObserver(observers)})
	if err != nil {
		return rasql.Query[taskRow]{}, err
	}
	queryValue := rasql.Select(relation.Source(), projection).OrderBy(rasql.AscExpr(id.Expr()))
	if projectID > 0 {
		queryValue = queryValue.Where(rasql.EqualValue(project.Expr(), projectID))
	}
	if idValue > 0 {
		queryValue = queryValue.Where(rasql.EqualValue(id.Expr(), idValue))
	}
	if openOnly {
		queryValue = queryValue.Where(rasql.EqualValue(open.Expr(), true))
	}
	return queryValue, nil
}

func typedMemberQuery(f typedFixture, ids []int64, observers ...func(memberRow)) (rasql.Query[memberRow], error) {
	relation := mustSource(f.members, "m")
	id, err := rasql.BindColumn[memberRow, int64](relation, "id", "")
	if err != nil {
		return rasql.Query[memberRow]{}, err
	}
	name, err := rasql.BindColumn[memberRow, string](relation, "name", "")
	if err != nil {
		return rasql.Query[memberRow]{}, err
	}
	resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}}, rasql.ResultColumn{Name: "name", Type: schema.TextType{}})
	if err != nil {
		return rasql.Query[memberRow]{}, err
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", id.Expr(), schema.IntegerType{}, ""), rasql.Item("name", name.Expr(), schema.TextType{}, "")}, resultDecoder[memberRow]{schema: resultSchema, scan: func(src rasql.ScanSource, row *memberRow) error { return src.Scan(&row.ID, &row.Name) }, observe: firstObserver(observers)})
	if err != nil {
		return rasql.Query[memberRow]{}, err
	}
	queryValue := rasql.Select(relation.Source(), projection).OrderBy(rasql.AscExpr(id.Expr()))
	if len(ids) == 1 {
		queryValue = queryValue.Where(rasql.EqualValue(id.Expr(), ids[0]))
	} else if len(ids) > 1 {
		predicates := make([]rasql.Predicate, 0, len(ids))
		for _, value := range ids {
			predicates = append(predicates, rasql.EqualValue(id.Expr(), value))
		}
		queryValue = queryValue.Where(rasql.Or(predicates...))
	}
	return queryValue, nil
}

type graphRowCapture struct {
	Projects []projectRow
	Tasks    []taskRow
	Members  []memberRow
}

func firstObserver[R any](observers []func(R)) func(R) {
	if len(observers) == 0 {
		return nil
	}
	return observers[0]
}

func typedGraphPlan(f typedFixture, maxProject int64, captures ...*graphRowCapture) (rasql.GraphPlan[projectRow, projectGraph], error) {
	var capture *graphRowCapture
	if len(captures) > 0 {
		capture = captures[0]
	}
	projectQuery, err := typedProjectAllQuery(f, maxProject, func(row projectRow) {
		if capture != nil {
			capture.Projects = append(capture.Projects, row)
		}
	})
	if err != nil {
		return rasql.GraphPlan[projectRow, projectGraph]{}, err
	}
	taskQuery, err := typedTaskQuery(f, 0, true, func(row taskRow) {
		if capture != nil {
			capture.Tasks = append(capture.Tasks, row)
		}
	})
	if err != nil {
		return rasql.GraphPlan[projectRow, projectGraph]{}, err
	}
	memberQuery, err := typedMemberQuery(f, nil, func(row memberRow) {
		if capture != nil {
			capture.Members = append(capture.Members, row)
		}
	})
	if err != nil {
		return rasql.GraphPlan[projectRow, projectGraph]{}, err
	}
	projectRelation := mustSource(f.projects, "p")
	taskRelation := mustSource(f.tasks, "t")
	memberRelation := mustSource(f.members, "m")
	projectID, _ := rasql.BindColumn[projectRow, int64](projectRelation, "id", "")
	taskProject, _ := rasql.BindColumn[taskRow, int64](taskRelation, "project_id", "")
	taskID, _ := rasql.BindColumn[taskRow, int64](taskRelation, "id", "")
	taskAssignee, _ := rasql.BindNullColumn[taskRow, int64](taskRelation, "assignee_id", "")
	memberID, _ := rasql.BindColumn[memberRow, int64](memberRelation, "id", "")
	parentKey, err := rasql.NewGraphKey(rasql.KeyPart(projectID, func(row projectRow) int64 { return row.ID }))
	if err != nil {
		return rasql.GraphPlan[projectRow, projectGraph]{}, err
	}
	taskKey, err := rasql.NewGraphKey(rasql.KeyPart(taskProject, func(row taskRow) int64 { return row.ProjectID }))
	if err != nil {
		return rasql.GraphPlan[projectRow, projectGraph]{}, err
	}
	assigneeKey, err := rasql.NewGraphKey(rasql.NullKeyPart(taskAssignee, func(row taskRow) rasql.Nullable[int64] { return row.AssigneeID }))
	if err != nil {
		return rasql.GraphPlan[projectRow, projectGraph]{}, err
	}
	memberKey, err := rasql.NewGraphKey(rasql.KeyPart(memberID, func(row memberRow) int64 { return row.ID }))
	if err != nil {
		return rasql.GraphPlan[projectRow, projectGraph]{}, err
	}
	memberPlan, err := rasql.NewGraphPlan(memberQuery, func(row memberRow) memberRow { return row })
	if err != nil {
		return rasql.GraphPlan[projectRow, projectGraph]{}, err
	}
	assignee, err := rasql.HasOne("assignee", assigneeKey, memberKey, memberPlan, rasql.EdgeOptions{}, func(parent *taskGraph, loaded rasql.LoadedOne[memberRow]) { parent.Assignee = loaded })
	if err != nil {
		return rasql.GraphPlan[projectRow, projectGraph]{}, err
	}
	tasksPlan, err := rasql.NewGraphPlan(taskQuery, func(row taskRow) taskGraph { return taskGraph{ID: row.ID, Title: row.Title} }, assignee)
	if err != nil {
		return rasql.GraphPlan[projectRow, projectGraph]{}, err
	}
	projectTasks, err := rasql.HasMany("tasks", parentKey, taskKey, tasksPlan, rasql.EdgeOptions{PerParentLimit: 5, Order: []rasql.OrderTerm{rasql.AscExpr(taskID.Expr())}}, func(parent *projectGraph, loaded rasql.LoadedMany[taskGraph]) { parent.Tasks = loaded })
	if err != nil {
		return rasql.GraphPlan[projectRow, projectGraph]{}, err
	}
	return rasql.NewGraphPlan(projectQuery, func(row projectRow) projectGraph { return projectGraph{ID: row.ID, Name: row.Name} }, projectTasks)
}

func typedProjectAllQuery(f typedFixture, maxProject int64, observers ...func(projectRow)) (rasql.Query[projectRow], error) {
	relation := mustSource(f.projects, "p")
	id, err := rasql.BindColumn[projectRow, int64](relation, "id", "")
	if err != nil {
		return rasql.Query[projectRow]{}, err
	}
	name, err := rasql.BindColumn[projectRow, string](relation, "name", "")
	if err != nil {
		return rasql.Query[projectRow]{}, err
	}
	schemaValue, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}}, rasql.ResultColumn{Name: "name", Type: schema.TextType{}})
	if err != nil {
		return rasql.Query[projectRow]{}, err
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", id.Expr(), schema.IntegerType{}, ""), rasql.Item("name", name.Expr(), schema.TextType{}, "")}, resultDecoder[projectRow]{schema: schemaValue, scan: func(src rasql.ScanSource, row *projectRow) error { return src.Scan(&row.ID, &row.Name) }, observe: firstObserver(observers)})
	if err != nil {
		return rasql.Query[projectRow]{}, err
	}
	queryValue := rasql.Select(relation.Source(), projection).OrderBy(rasql.AscExpr(id.Expr()))
	if maxProject > 0 {
		predicates := make([]rasql.Predicate, 0, maxProject)
		for value := int64(1); value <= maxProject; value++ {
			predicates = append(predicates, rasql.EqualValue(id.Expr(), value))
		}
		queryValue = queryValue.Where(rasql.Or(predicates...))
	}
	return queryValue, nil
}

func marshalResult(value any) ([]byte, error) { return json.Marshal(value) }

func normalizeRows(records []InvocationRecord) int64 {
	var total int64
	for _, record := range records {
		if record.Phase == "consumption" {
			total += record.Rows
		}
	}
	return total
}
