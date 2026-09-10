package conformance

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/lestrrat-go/rasql"
)

type sqlDescriptor struct {
	Operation  string
	Source     string
	Joins      []string
	Projection []string
	Predicates []string
	Group      string
	Partition  string
	Order      string
	Limit      string
	Bind       []int
}

type statementContract struct {
	Implementation      string
	Verification        bool
	Role                statementRole
	PhysicalOrdinal     int
	CorrelatedEventKind rasql.EventKind
	NormalizedParent    int
	ParentValid         bool
	StatementIndex      int
	IndexValid          bool
	OperationKind       string
	ExpectedSQL         string
	SQL                 sqlDescriptor
	Arguments           []any
	RequiredPhases      []string
	RowValuesValid      bool
	RowValues           [][]any
	AffectedValid       bool
	Affected            int64
	ConsumedRows        int64
	EarlyClose          bool
	ErrorClass          string
	ContextReturned     bool
	Complete            bool
}

type correlatedEvent struct {
	Kind           rasql.EventKind
	LogicalID      string
	ParentID       string
	StatementIndex int
	Start          rasql.Event
	Terminal       rasql.Event
	Invocations    []InvocationRecord
}

type rasqlObservationSet struct {
	Statements []statementObservation
	Events     []correlatedEvent
}

func rasqlObservations(workload string, records []InvocationRecord, events []EventRecord) ([]statementObservation, error) {
	result, err := correlateRASQLObservations(workload, records, events)
	if err != nil {
		return nil, err
	}
	if isCompleteReadWorkload(workload) {
		for _, observation := range result.Statements {
			if err := validateObservedSQL(workload, observation.SQL); err != nil {
				return nil, err
			}
		}
	}
	return result.Statements, nil
}

func correlateRASQLWorkload(
	profileID, workload string,
	decodedRows [][]any,
	decodedStatementRows map[int][][]any,
	records []InvocationRecord,
	events []EventRecord,
) (rasqlObservationSet, error) {
	result, err := correlateRASQLObservations(workload, records, events)
	if err != nil {
		return rasqlObservationSet{}, err
	}
	if workload == "bulk_rollback" {
		result.Statements, err = expandRASQLBulkRollbackStatements(result)
		if err != nil {
			return rasqlObservationSet{}, err
		}
	}
	result.Statements = normalizeObservationParents(result.Statements)
	for physicalOrdinal, rows := range decodedStatementRows {
		if physicalOrdinal < 0 || physicalOrdinal >= len(result.Statements) {
			return rasqlObservationSet{}, fmt.Errorf(
				"%s decoded statement ordinal %d is invalid for %d statements (%s)",
				workload,
				physicalOrdinal,
				len(result.Statements),
				correlatedEventShape(result.Events),
			)
		}
		if result.Statements[physicalOrdinal].RowValues != nil {
			return rasqlObservationSet{}, fmt.Errorf("%s decoded statement ordinal %d is duplicated", workload, physicalOrdinal)
		}
		result.Statements[physicalOrdinal].RowValues = cloneRows(rows)
	}
	if isCompleteReadWorkload(workload) {
		if len(result.Statements) != 1 {
			return rasqlObservationSet{}, fmt.Errorf("%s has %d statements, want 1", workload, len(result.Statements))
		}
		if decodedRows == nil {
			return rasqlObservationSet{}, fmt.Errorf("%s decoded rows are missing", workload)
		}
		result.Statements[0].RowValues = cloneRows(decodedRows)
	}
	validated, err := validateImplementationObservations(profileID, "rasql", workload, result.Statements)
	if err != nil {
		return rasqlObservationSet{}, err
	}
	result.Statements = validated
	return result, nil
}

func expandRASQLBulkRollbackStatements(result rasqlObservationSet) ([]statementObservation, error) {
	type orderedObservation struct {
		ordinal     int
		observation statementObservation
	}
	byLogicalID := make(map[string]statementObservation, len(result.Statements))
	statementIndex := 0
	for _, event := range result.Events {
		if event.Kind != rasql.EventStatement {
			continue
		}
		if statementIndex >= len(result.Statements) {
			return nil, errors.New("bulk_rollback statement event count exceeds observations")
		}
		byLogicalID[event.LogicalID] = result.Statements[statementIndex]
		statementIndex++
	}
	if statementIndex != len(result.Statements) {
		return nil, errors.New("bulk_rollback observation count exceeds statement events")
	}

	ordered := make([]orderedObservation, 0, len(result.Statements)+3)
	for _, event := range result.Events {
		switch event.Kind {
		case rasql.EventStatement:
			observation := byLogicalID[event.LogicalID]
			ordered = append(ordered, orderedObservation{ordinal: event.Invocations[0].Ordinal, observation: observation})
		case rasql.EventScope:
			for _, invocation := range event.Invocations {
				role, ok := savepointOperationRole(invocation.SQL)
				if !ok {
					continue
				}
				completion := invocation.Completions[0]
				ordered = append(ordered, orderedObservation{ordinal: invocation.Ordinal, observation: statementObservation{
					Role: role, LogicalParent: event.LogicalID, StatementIndex: event.StatementIndex,
					SQL: invocation.SQL, Args: cloneInvocationArgs(invocation.Args), Kind: invocation.Kind,
					Phase: completion.Phase, Started: true, Completed: true, CompletionCount: 1,
					RowsConsumed: completion.Rows, EarlyClose: completion.EarlyClose, Err: completion.Err,
				}})
			}
		}
	}
	sort.SliceStable(ordered, func(left, right int) bool { return ordered[left].ordinal < ordered[right].ordinal })
	observations := make([]statementObservation, len(ordered))
	for index, item := range ordered {
		observations[index] = item.observation
	}
	return observations, nil
}

func savepointOperationRole(statement string) (statementRole, bool) {
	statement = strings.ToUpper(strings.Join(strings.Fields(statement), " "))
	switch {
	case strings.HasPrefix(statement, "SAVEPOINT "):
		return roleSavepointBegin, true
	case strings.HasPrefix(statement, "ROLLBACK TO SAVEPOINT "):
		return roleSavepointRollback, true
	case strings.HasPrefix(statement, "RELEASE SAVEPOINT "):
		return roleSavepointRelease, true
	default:
		return "", false
	}
}

func correlatedEventShape(events []correlatedEvent) string {
	parts := make([]string, 0, len(events))
	for _, event := range events {
		operations := make([]string, 0, len(event.Invocations))
		for _, invocation := range event.Invocations {
			sqlText := strings.Join(strings.Fields(invocation.SQL), " ")
			if len(sqlText) > 48 {
				sqlText = sqlText[:48]
			}
			operations = append(operations, fmt.Sprintf("%d:%s:%s", invocation.Ordinal, invocation.Kind, sqlText))
		}
		parts = append(parts, fmt.Sprintf(
			"%d:%s:%s:%d[%s]",
			event.Kind,
			event.LogicalID,
			event.ParentID,
			event.StatementIndex,
			strings.Join(operations, ","),
		))
	}
	return strings.Join(parts, ";")
}

func correlateRASQLObservations(workload string, records []InvocationRecord, events []EventRecord) (rasqlObservationSet, error) {
	starts := make(map[string]rasql.Event)
	terminals := make(map[string]rasql.Event)
	orderedStarts := make([]rasql.Event, 0, len(events)/2)
	for _, record := range events {
		event := record.Event
		if strings.TrimSpace(event.LogicalID) == "" {
			return rasqlObservationSet{}, errors.New("rasql event has no logical identity")
		}
		if event.Kind != rasql.EventScope && event.Kind != rasql.EventStatement && event.Kind != rasql.EventMutationBatch && event.Kind != rasql.EventGraph {
			return rasqlObservationSet{}, fmt.Errorf("rasql event %q has unknown kind %d", event.LogicalID, event.Kind)
		}
		switch event.Phase {
		case rasql.EventStart:
			if event.StatementIndex < 0 {
				return rasqlObservationSet{}, fmt.Errorf("rasql event %q has negative statement index", event.LogicalID)
			}
			if _, exists := starts[event.LogicalID]; exists {
				return rasqlObservationSet{}, fmt.Errorf("rasql event %q has duplicate start", event.LogicalID)
			}
			starts[event.LogicalID] = event
			if event.Kind == rasql.EventStatement {
				if (workload == "taskboard_graph_page" || workload == "graph_500_parent_limit") && strings.TrimSpace(event.ParentID) == "" {
					return rasqlObservationSet{}, fmt.Errorf("rasql statement %q has no logical parent", event.LogicalID)
				}
				orderedStarts = append(orderedStarts, event)
			}
		case rasql.EventTerminal:
			start, exists := starts[event.LogicalID]
			if !exists {
				return rasqlObservationSet{}, fmt.Errorf("rasql event %q has terminal before start", event.LogicalID)
			}
			if _, exists := terminals[event.LogicalID]; exists {
				return rasqlObservationSet{}, fmt.Errorf("rasql event %q has duplicate terminal", event.LogicalID)
			}
			if event.ParentID != start.ParentID || event.StatementIndex != start.StatementIndex || event.Kind != start.Kind {
				return rasqlObservationSet{}, fmt.Errorf("rasql event %q terminal identity differs from start", event.LogicalID)
			}
			terminals[event.LogicalID] = event
		default:
			return rasqlObservationSet{}, fmt.Errorf("rasql event %q has unknown event phase %d", event.LogicalID, event.Phase)
		}
	}
	for logicalID := range starts {
		if _, ok := terminals[logicalID]; !ok {
			return rasqlObservationSet{}, fmt.Errorf("rasql event %q has no terminal", logicalID)
		}
	}
	parentIndexes := make(map[string]int)
	for _, start := range orderedStarts {
		if strings.TrimSpace(start.ParentID) == "" {
			continue
		}
		want := parentIndexes[start.ParentID]
		if start.StatementIndex != want {
			return rasqlObservationSet{}, fmt.Errorf("rasql parent %q statement index %d, want %d", start.ParentID, start.StatementIndex, want)
		}
		parentIndexes[start.ParentID] = want + 1
	}

	orderedRecords := append([]InvocationRecord(nil), records...)
	sort.SliceStable(orderedRecords, func(left, right int) bool { return orderedRecords[left].Ordinal < orderedRecords[right].Ordinal })
	seenOrdinals := make(map[int]struct{}, len(orderedRecords))
	byID := make(map[string][]InvocationRecord)
	for _, record := range orderedRecords {
		if err := record.validate(); err != nil {
			return rasqlObservationSet{}, err
		}
		if _, exists := seenOrdinals[record.Ordinal]; exists {
			return rasqlObservationSet{}, fmt.Errorf("invocation ordinal %d is duplicated", record.Ordinal)
		}
		seenOrdinals[record.Ordinal] = struct{}{}
		start, exists := starts[record.LogicalID]
		if !exists {
			return rasqlObservationSet{}, fmt.Errorf("invocation %q has no event start", record.LogicalID)
		}
		if record.EventKind != start.Kind || record.ParentID != start.ParentID || record.StatementIndex != start.StatementIndex {
			return rasqlObservationSet{}, fmt.Errorf("invocation %d identity differs from event %q", record.Ordinal, record.LogicalID)
		}
		byID[record.LogicalID] = append(byID[record.LogicalID], record)
	}

	correlated := make([]correlatedEvent, 0, len(starts))
	for _, record := range events {
		if record.Event.Phase != rasql.EventStart {
			continue
		}
		start := record.Event
		correlated = append(correlated, correlatedEvent{
			Kind: start.Kind, LogicalID: start.LogicalID, ParentID: start.ParentID, StatementIndex: start.StatementIndex,
			Start: start, Terminal: terminals[start.LogicalID], Invocations: append([]InvocationRecord(nil), byID[start.LogicalID]...),
		})
	}
	nonstatementPositions := make([]int, 0, len(correlated))
	nonstatements := make([]correlatedEvent, 0, len(correlated))
	for index, event := range correlated {
		if event.Kind != rasql.EventStatement {
			nonstatementPositions = append(nonstatementPositions, index)
			nonstatements = append(nonstatements, event)
		}
	}
	sort.SliceStable(nonstatements, func(left, right int) bool {
		leftHasInvocation := len(nonstatements[left].Invocations) > 0
		rightHasInvocation := len(nonstatements[right].Invocations) > 0
		if leftHasInvocation && rightHasInvocation {
			return nonstatements[left].Invocations[0].Ordinal < nonstatements[right].Invocations[0].Ordinal
		}
		return leftHasInvocation && !rightHasInvocation
	})
	for index, position := range nonstatementPositions {
		correlated[position] = nonstatements[index]
	}

	observations := make([]statementObservation, 0, len(orderedStarts))
	usedStatements := make(map[string]struct{}, len(orderedStarts))
	for physical, start := range orderedStarts {
		terminal := terminals[start.LogicalID]
		sessions := append([]InvocationRecord(nil), byID[start.LogicalID]...)
		if len(sessions) == 0 {
			return rasqlObservationSet{}, fmt.Errorf("statement %q has no invocation", start.LogicalID)
		}
		for index, session := range sessions {
			if session.Kind != "query" && session.Kind != "exec" {
				return rasqlObservationSet{}, fmt.Errorf("statement %q invocation %d has unknown operation kind %q", start.LogicalID, index, session.Kind)
			}
			if index > 0 && (session.Kind != sessions[0].Kind || session.SQL != sessions[0].SQL || !sameInvocationArguments(session.Args, sessions[0].Args)) {
				return rasqlObservationSet{}, fmt.Errorf("statement %q invocation %d operation differs", start.LogicalID, index)
			}
		}
		first := sessions[0]
		var rows int64
		var early bool
		var phase string
		switch first.Kind {
		case "query":
			switch len(sessions) {
			case 1:
				completion := first.Completions[0]
				if completion.Phase != "execution" || completion.Err == nil || !sameError(completion.Err, terminal.Err) {
					return rasqlObservationSet{}, fmt.Errorf("statement %q query execution failure does not match terminal", start.LogicalID)
				}
				rows, early, phase = completion.Rows, completion.EarlyClose, completion.Phase
			case 2:
				execution, consumption := sessions[0].Completions[0], sessions[1].Completions[0]
				if execution.Phase != "execution" || execution.Err != nil || consumption.Phase != "consumption" || !sameError(consumption.Err, terminal.Err) {
					return rasqlObservationSet{}, fmt.Errorf("statement %q query phase order or error differs", start.LogicalID)
				}
				if consumption.Rows != terminal.Rows || consumption.EarlyClose != terminal.EarlyClose {
					return rasqlObservationSet{}, fmt.Errorf("statement %q consumption completion differs from terminal", start.LogicalID)
				}
				rows, early, phase = consumption.Rows, consumption.EarlyClose, consumption.Phase
			default:
				return rasqlObservationSet{}, fmt.Errorf("statement %q has invalid query invocation count %d", start.LogicalID, len(sessions))
			}
		case "exec":
			if len(sessions) != 1 || sessions[0].Completions[0].Phase != "execution" || !sameError(sessions[0].Completions[0].Err, terminal.Err) {
				return rasqlObservationSet{}, fmt.Errorf("statement %q exec lifecycle differs from terminal", start.LogicalID)
			}
			rows, early, phase = sessions[0].Completions[0].Rows, sessions[0].Completions[0].EarlyClose, sessions[0].Completions[0].Phase
		}
		logicalParent := start.ParentID
		if strings.TrimSpace(logicalParent) == "" {
			logicalParent = start.LogicalID
		}
		observation := statementObservation{
			Role: rasqlStatementRole(workload, physical), LogicalParent: logicalParent, StatementIndex: start.StatementIndex,
			Verification: rasqlStatementVerification(workload, physical),
			SQL:          first.SQL, Args: cloneInvocationArgs(first.Args), Kind: first.Kind, Phase: phase,
			Started: true, Completed: true, CompletionCount: 1, RowsConsumed: rows, EarlyClose: early,
			Err: terminal.Err,
		}
		observations = append(observations, observation)
		usedStatements[start.LogicalID] = struct{}{}
	}
	for logicalID, sessions := range byID {
		if starts[logicalID].Kind == rasql.EventStatement {
			if _, ok := usedStatements[logicalID]; !ok || len(sessions) == 0 {
				return rasqlObservationSet{}, fmt.Errorf("statement %q invocation was not consumed", logicalID)
			}
		}
	}
	return rasqlObservationSet{Statements: observations, Events: correlated}, nil
}

func sameError(left, right error) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left == right || errors.Is(left, right) || errors.Is(right, left)
}

func sameInvocationArguments(left, right []any) bool {
	if (left == nil) != (right == nil) {
		return false
	}
	return sameArguments(left, right)
}

func rasqlStatementVerification(workload string, index int) bool {
	switch workload {
	case "create_patch":
		return index == 2
	case "bulk_write":
		return index == 2
	case "rollback":
		return index == 1
	case "bulk_rollback":
		return index == 6
	case "early_exit":
		return index == 1
	case "cancellation":
		return index == 1 || index == 4 || index == 8
	default:
		return false
	}
}

func rasqlStatementRole(workload string, index int) statementRole {
	switch workload {
	case "create_patch", "bulk_write", "rollback":
		return roleMutation
	case "bulk_rollback":
		switch index {
		case 0:
			return roleSavepointBegin
		case 3:
			return roleSavepointRollback
		case 4:
			return roleSavepointRelease
		case 5:
			return roleSentinel
		default:
			return roleMutation
		}
	case "graph_500_parent_limit":
		if index == 0 {
			return roleRoot
		}
		if index == 1 {
			return roleTasks
		}
		return roleAssignees
	case "cancellation":
		switch index {
		case 0, 2, 5:
			return roleRoot
		case 3, 6:
			return roleTasks
		case 7:
			return roleAssignees
		default:
			return roleVerification
		}
	case "taskboard_graph_page":
		switch index % 3 {
		case 0:
			return roleRoot
		case 1:
			return roleTasks
		default:
			return roleAssignees
		}
	case "nullable_join_report", "typed_sql_report":
		return roleReport
	default:
		return roleRead
	}
}

func describeSQL(statement string) string {
	statement = strings.ToLower(strings.TrimSpace(statement))
	statement = strings.ReplaceAll(statement, "`", "")
	statement = strings.ReplaceAll(statement, `"`, "")
	statement = regexp.MustCompile(`\s+as\s+[a-z_][a-z0-9_]*`).ReplaceAllString(statement, "")
	for _, alias := range []string{"projects p", "projects as p", "tasks t", "tasks as t", "members m", "members as m"} {
		parts := strings.SplitN(alias, " ", 2)
		statement = strings.ReplaceAll(statement, "from "+alias, "from "+parts[0])
		statement = strings.ReplaceAll(statement, "join "+alias, "join "+parts[0])
		statement = strings.ReplaceAll(statement, "left join "+alias, "left join "+parts[0])
	}
	statement = regexp.MustCompile(`\b[a-z_][a-z0-9_]*\.`).ReplaceAllString(statement, "")
	statement = strings.ReplaceAll(statement, "on project_id = id", "on id = project_id")
	statement = strings.ReplaceAll(statement, "inner join", "join")
	statement = strings.ReplaceAll(statement, "(", " ")
	statement = strings.ReplaceAll(statement, ")", " ")
	statement = strings.Join(strings.Fields(statement), " ")
	for index := 1; index < 32; index++ {
		statement = strings.ReplaceAll(statement, fmt.Sprintf("$%d", index), "?")
		statement = strings.ReplaceAll(statement, fmt.Sprintf("@p%d", index), "?")
	}
	return statement
}

func validateObservedSQL(workload, statement string) error {
	if strings.TrimSpace(statement) == "" {
		return errors.New("observed SQL is empty")
	}
	expected, ok := logicalReadSQL[workload]
	if !ok {
		return nil
	}
	if normalizeStatementSQL(statement) != normalizeStatementSQL(expected) {
		return fmt.Errorf("observed SQL differs from %s contract", workload)
	}
	return nil
}

func validateStatementContract(contract statementContract, observation statementObservation) error {
	if strings.TrimSpace(contract.Implementation) == "" {
		return errors.New("statement contract implementation is empty")
	}
	if contract.Verification != observation.Verification || contract.Role != observation.Role {
		return errors.New("statement contract role or verification differs")
	}
	if contract.CorrelatedEventKind != rasql.EventStatement {
		return errors.New("statement contract has no event identity")
	}
	if !observation.Started || !observation.Completed || observation.CompletionCount != 1 {
		return errors.New("statement lifecycle is incomplete")
	}
	observationParentValid := strings.TrimSpace(observation.LogicalParent) != ""
	if contract.ParentValid != observationParentValid ||
		(contract.ParentValid && contract.NormalizedParent != observationParentOrdinal(observation.LogicalParent)) {
		return errors.New("statement contract parent differs")
	}
	if contract.IndexValid != (observation.StatementIndex >= 0) ||
		(contract.IndexValid && contract.StatementIndex != observation.StatementIndex) {
		return errors.New("statement contract index differs")
	}
	if contract.OperationKind != observation.Kind {
		return errors.New("statement contract operation differs")
	}
	if contract.Complete && normalizeStatementSQL(contract.ExpectedSQL) != normalizeStatementSQL(observation.SQL) {
		return errors.New("statement contract SQL differs")
	}
	if !sameInvocationArguments(contract.Arguments, observation.Args) {
		return errors.New("statement contract arguments differ")
	}
	if !equalStrings(contract.RequiredPhases, observationPhases(observation)) {
		return errors.New("statement contract phases differ")
	}
	rowValuesValid := observation.RowValues != nil
	if contract.RowValuesValid != rowValuesValid ||
		(contract.RowValuesValid && !sameRows(contract.RowValues, observation.RowValues)) {
		return errors.New("statement contract row values differ")
	}
	if contract.AffectedValid != observation.RowsAffectedValid ||
		(contract.AffectedValid && contract.Affected != observation.RowsAffected) {
		return errors.New("statement contract affected rows differ")
	}
	if contract.ConsumedRows != observation.RowsConsumed || contract.EarlyClose != observation.EarlyClose {
		return errors.New("statement contract consumption differs")
	}
	if contract.ErrorClass != errorClass(observation.Err) {
		return errors.New("statement contract error class differs")
	}
	if !contract.ContextReturned {
		return errors.New("statement invocation lost returned context")
	}
	return nil
}

func normalizeStatementSQL(statement string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(statement, "\r\n", "\n")), " ")
}

func sameRows(left, right [][]any) bool {
	if (left == nil) != (right == nil) || len(left) != len(right) {
		return false
	}
	for index := range left {
		if !sameInvocationArguments(left[index], right[index]) {
			return false
		}
	}
	return true
}

func observationParentOrdinal(parent string) int {
	var ordinal int
	_, _ = fmt.Sscanf(parent, "%d", &ordinal)
	return ordinal
}

func equalStrings(left, right []string) bool {
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
