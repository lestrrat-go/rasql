package conformance

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lestrrat-go/rasql"
)

var logicalReadSQL = map[string]string{
	"single_row_read": "SELECT id, name FROM projects WHERE id = ? ORDER BY id",
	"nullable_join_report": "SELECT p.id, p.name, t.id, t.title, t.assignee_id, m.name " +
		"FROM projects p JOIN tasks t ON t.project_id = p.id " +
		"LEFT JOIN members m ON m.id = t.assignee_id WHERE p.id = ? ORDER BY t.id",
	"typed_sql_report": "SELECT id, project_id, assignee_id, title, is_open, due_on, created_at " +
		"FROM tasks WHERE project_id = ? AND is_open = ? AND due_on IS NOT NULL AND due_on < ? ORDER BY id",
}

func isCompleteReadWorkload(workload string) bool {
	_, ok := logicalReadSQL[workload]
	return ok
}

func statementContracts(profileID, implementation, workload string) ([]statementContract, error) {
	if err := validateContractSelector(profileID, implementation); err != nil {
		return nil, err
	}
	if isCompleteReadWorkload(workload) {
		contract, err := completeReadContract(profileID, implementation, workload)
		if err != nil {
			return nil, err
		}
		contracts := []statementContract{contract}
		if err := validateStatementContracts(contracts); err != nil {
			return nil, err
		}
		return cloneStatementContracts(contracts), nil
	}
	contracts, err := transitionalStatementContracts(profileID, implementation, workload)
	if err != nil {
		return nil, err
	}
	if err := validateStatementContracts(contracts); err != nil {
		return nil, err
	}
	return cloneStatementContracts(contracts), nil
}

func validateContractSelector(profileID, implementation string) error {
	switch profileID {
	case "sqlite-3.35", "postgresql-17", "mysql-8.4":
	default:
		return fmt.Errorf("unsupported statement-contract profile %q", profileID)
	}
	switch implementation {
	case "rasql", "database/sql":
		return nil
	default:
		return fmt.Errorf("unsupported statement-contract implementation %q", implementation)
	}
}

func completeReadContract(profileID, implementation, workload string) (statementContract, error) {
	expectedSQL, err := expectedStatementSQL(profileID, implementation, workload, 0)
	if err != nil {
		return statementContract{}, err
	}
	arguments, rows, role, descriptor := readContractValues(workload)
	return statementContract{
		Implementation: implementation, Role: role, PhysicalOrdinal: 0,
		CorrelatedEventKind: rasql.EventStatement, NormalizedParent: 0, ParentValid: true,
		StatementIndex: 0, IndexValid: true, OperationKind: "query", ExpectedSQL: expectedSQL,
		SQL: descriptor, Arguments: arguments, RequiredPhases: []string{"execution", "consumption"},
		RowValuesValid: true, RowValues: rows, ConsumedRows: int64(len(rows)), ContextReturned: true,
		Complete: true,
	}, nil
}

func readContractValues(workload string) ([]any, [][]any, statementRole, sqlDescriptor) {
	utc := time.UTC
	switch workload {
	case "single_row_read":
		return []any{int64(1)}, [][]any{{int64(1), "project-001"}}, roleRead, sqlDescriptor{
			Operation: "query", Source: "projects",
			Projection: []string{"projects.id", "projects.name"},
			Predicates: []string{"projects.id = bind:1"}, Order: "projects.id ASC", Bind: []int{1},
		}
	case "nullable_join_report":
		return []any{int64(2)}, [][]any{
				{int64(2), "project-002", int64(8), "task-0008", int64(8), "member-08"},
				{int64(2), "project-002", int64(9), "task-0009", int64(9), "member-09"},
				{int64(2), "project-002", int64(10), "task-0010", int64(10), "member-10"},
				{int64(2), "project-002", int64(11), "task-0011", nil, nil},
				{int64(2), "project-002", int64(12), "task-0012", int64(12), "member-12"},
				{int64(2), "project-002", int64(13), "task-0013", int64(13), "member-13"},
				{int64(2), "project-002", int64(14), "task-0014", int64(14), "member-14"},
			}, roleReport, sqlDescriptor{
				Operation: "query", Source: "projects",
				Joins:      []string{"INNER tasks ON tasks.project_id = projects.id", "LEFT members ON members.id = tasks.assignee_id"},
				Projection: []string{"projects.id", "projects.name", "tasks.id", "tasks.title", "tasks.assignee_id", "members.name"},
				Predicates: []string{"projects.id = bind:1"}, Order: "tasks.id ASC", Bind: []int{1},
			}
	case "typed_sql_report":
		cutoff := time.Date(2024, 1, 4, 0, 0, 0, 0, utc)
		created := time.Date(2024, 1, 1, 0, 0, 0, 0, utc)
		return []any{int64(1), true, cutoff}, [][]any{
				{int64(1), int64(1), int64(1), "task-0001", true, time.Date(2024, 1, 1, 0, 0, 0, 0, utc), created},
				{int64(3), int64(1), int64(3), "task-0003", true, time.Date(2024, 1, 3, 0, 0, 0, 0, utc), created},
			}, roleReport, sqlDescriptor{
				Operation: "query", Source: "tasks",
				Projection: []string{"tasks.id", "tasks.project_id", "tasks.assignee_id", "tasks.title", "tasks.is_open", "tasks.due_on", "tasks.created_at"},
				Predicates: []string{"tasks.project_id = bind:1", "tasks.is_open = bind:2", "tasks.due_on IS NOT NULL", "tasks.due_on < bind:3"},
				Order:      "tasks.id ASC", Bind: []int{1, 2, 3},
			}
	default:
		return nil, nil, "", sqlDescriptor{}
	}
}

func expectedStatementSQL(profileID, implementation, workload string, physicalOrdinal int) (string, error) {
	if err := validateContractSelector(profileID, implementation); err != nil {
		return "", err
	}
	if physicalOrdinal != 0 || !isCompleteReadWorkload(workload) {
		return "", fmt.Errorf("no complete SQL contract for %s statement %d", workload, physicalOrdinal)
	}
	if implementation == "database/sql" || workload == "typed_sql_report" {
		return bindExpectedSQL(profileID, logicalReadSQL[workload]), nil
	}
	quote := `"`
	if profileID == "mysql-8.4" {
		quote = "`"
	}
	qualified := func(alias, name string) string {
		return quote + alias + quote + "." + quote + name + quote
	}
	table := func(name, alias string) string {
		return quote + name + quote + " AS " + quote + alias + quote
	}
	bind := "?"
	if profileID == "postgresql-17" {
		bind = "$1"
	}
	switch workload {
	case "single_row_read":
		return "SELECT " + qualified("p", "id") + " AS " + quote + "id" + quote + ", " +
			qualified("p", "name") + " AS " + quote + "name" + quote + " FROM " + table("projects", "p") +
			" WHERE (" + qualified("p", "id") + " = " + bind + ") ORDER BY " + qualified("p", "id"), nil
	case "nullable_join_report":
		return "SELECT " + qualified("p", "id") + " AS " + quote + "project_id" + quote + ", " +
			qualified("p", "name") + " AS " + quote + "project_name" + quote + ", " +
			qualified("t", "id") + " AS " + quote + "task_id" + quote + ", " +
			qualified("t", "title") + " AS " + quote + "task_title" + quote + ", " +
			qualified("t", "assignee_id") + " AS " + quote + "assignee_id" + quote + ", " +
			qualified("m", "name") + " AS " + quote + "member_name" + quote + " FROM " + table("projects", "p") +
			" INNER JOIN " + table("tasks", "t") + " ON (" + qualified("p", "id") + " = " + qualified("t", "project_id") + ")" +
			" LEFT JOIN " + table("members", "m") + " ON (" + qualified("m", "id") + " = " + qualified("t", "assignee_id") + ")" +
			" WHERE (" + qualified("p", "id") + " = " + bind + ") ORDER BY " + qualified("t", "id"), nil
	default:
		return "", errors.New("complete read SQL contract is missing")
	}
}

func bindExpectedSQL(profileID, statement string) string {
	if profileID != "postgresql-17" {
		return statement
	}
	ordinal := 0
	var result strings.Builder
	for _, runeValue := range statement {
		if runeValue != '?' {
			result.WriteRune(runeValue)
			continue
		}
		ordinal++
		result.WriteByte('$')
		result.WriteString(strconv.Itoa(ordinal))
	}
	return result.String()
}

func transitionalStatementContracts(profileID, implementation, workload string) ([]statementContract, error) {
	count := 0
	switch workload {
	case "create_patch":
		count = 3
	case "bulk_write":
		count = 2
		if profileID == "sqlite-3.35" {
			count = 3
		}
	case "rollback", "early_exit":
		count = 2
	case "bulk_rollback":
		count = 7
	case "cancellation":
		count = 9
	case "taskboard_graph_page":
		count = 15
	case "graph_500_parent_limit":
		count = 3
		if profileID == "sqlite-3.35" {
			count = 5
		}
	default:
		return nil, fmt.Errorf("unknown statement-contract workload %q", workload)
	}
	contracts := make([]statementContract, count)
	for index := range contracts {
		role, verification := transitionalStatementRole(workload, count, index)
		contracts[index] = statementContract{
			Implementation: implementation, Verification: verification, Role: role,
			PhysicalOrdinal: index, CorrelatedEventKind: rasql.EventStatement,
			NormalizedParent: 0, ParentValid: true, StatementIndex: index, IndexValid: true,
			OperationKind: "query", SQL: sqlDescriptor{Operation: "transitional"},
			RequiredPhases: []string{"execution"}, ContextReturned: true,
		}
	}
	return contracts, nil
}

func transitionalStatementRole(workload string, count, index int) (statementRole, bool) {
	switch workload {
	case "create_patch", "bulk_write", "rollback":
		if index == count-1 {
			return roleVerification, true
		}
		return roleMutation, false
	case "bulk_rollback":
		switch index {
		case 0:
			return roleSavepointBegin, false
		case 1, 2:
			return roleMutation, false
		case 3:
			return roleSavepointRollback, false
		case 4:
			return roleSavepointRelease, false
		case 5:
			return roleSentinel, false
		case 6:
			return roleVerification, true
		}
		return "", false
	case "early_exit":
		if index == 1 {
			return roleVerification, true
		}
		return roleRead, false
	case "cancellation":
		switch index {
		case 0, 2, 5:
			return roleRoot, false
		case 3, 6:
			return roleTasks, false
		case 7:
			return roleAssignees, false
		case 1, 4, 8:
			return roleVerification, true
		}
		return "", false
	case "graph_500_parent_limit":
		if index == 0 {
			return roleRoot, false
		}
		if index == 1 {
			return roleTasks, false
		}
		return roleAssignees, false
	case "taskboard_graph_page":
		switch index % 3 {
		case 0:
			return roleRoot, false
		case 1:
			return roleTasks, false
		default:
			return roleAssignees, false
		}
	default:
		return roleRead, false
	}
}

func validateStatementContracts(contracts []statementContract) error {
	seen := make(map[int]struct{}, len(contracts))
	for index, contract := range contracts {
		if strings.TrimSpace(contract.Implementation) == "" {
			return fmt.Errorf("statement contract %d implementation is empty", index)
		}
		if contract.PhysicalOrdinal != index {
			return fmt.Errorf("statement contract %d physical ordinal is %d", index, contract.PhysicalOrdinal)
		}
		if _, ok := seen[contract.PhysicalOrdinal]; ok {
			return fmt.Errorf("statement contract physical ordinal %d is duplicated", contract.PhysicalOrdinal)
		}
		seen[contract.PhysicalOrdinal] = struct{}{}
		if contract.Role != roleVerification && !validMeasuredRole(contract.Role) {
			return fmt.Errorf("statement contract %d role %q is invalid", index, contract.Role)
		}
		if contract.Verification != (contract.Role == roleVerification) {
			return fmt.Errorf("statement contract %d verification role differs", index)
		}
		if contract.CorrelatedEventKind != rasql.EventStatement {
			return fmt.Errorf("statement contract %d event kind is invalid", index)
		}
		if !contract.ParentValid || !contract.IndexValid || contract.StatementIndex < 0 {
			return fmt.Errorf("statement contract %d parent or index is invalid", index)
		}
		if strings.TrimSpace(contract.OperationKind) == "" || strings.TrimSpace(contract.SQL.Operation) == "" {
			return fmt.Errorf("statement contract %d operation is empty", index)
		}
		if contract.Complete && strings.TrimSpace(contract.ExpectedSQL) == "" {
			return fmt.Errorf("statement contract %d expected SQL is empty", index)
		}
	}
	return nil
}

func validateImplementationObservations(
	profileID, implementation, workload string,
	observations []statementObservation,
) ([]statementObservation, error) {
	contracts, err := statementContracts(profileID, implementation, workload)
	if err != nil {
		return nil, err
	}
	if len(contracts) != len(observations) {
		return nil, fmt.Errorf("%s contract count %d differs from observation count %d", workload, len(contracts), len(observations))
	}
	result := cloneStatementObservations(observations)
	for index := range contracts {
		if implementation == "rasql" {
			result[index].Role = contracts[index].Role
			result[index].Verification = contracts[index].Verification
		}
		if !contracts[index].Complete {
			continue
		}
		if err := validateStatementContract(contracts[index], result[index]); err != nil {
			return nil, fmt.Errorf("%s statement %d: %w", workload, index, err)
		}
		result[index].Descriptor = cloneSQLDescriptor(contracts[index].SQL)
	}
	return result, nil
}

func normalizeObservationParents(observations []statementObservation) []statementObservation {
	result := cloneStatementObservations(observations)
	ordinals := make(map[string]int)
	for index := range result {
		parent := result[index].LogicalParent
		if strings.TrimSpace(parent) == "" {
			continue
		}
		ordinal, ok := ordinals[parent]
		if !ok {
			ordinal = len(ordinals)
			ordinals[parent] = ordinal
		}
		result[index].LogicalParent = strconv.Itoa(ordinal)
	}
	return result
}

func cloneStatementObservations(observations []statementObservation) []statementObservation {
	result := make([]statementObservation, len(observations))
	for index, observation := range observations {
		result[index] = observation
		result[index].Args = cloneInvocationArgs(observation.Args)
		result[index].RowValues = cloneRows(observation.RowValues)
		result[index].Descriptor = cloneSQLDescriptor(observation.Descriptor)
	}
	return result
}

func cloneStatementContracts(contracts []statementContract) []statementContract {
	result := make([]statementContract, len(contracts))
	for index, contract := range contracts {
		result[index] = contract
		result[index].Arguments = cloneInvocationArgs(contract.Arguments)
		result[index].RequiredPhases = append([]string(nil), contract.RequiredPhases...)
		result[index].RowValues = cloneRows(contract.RowValues)
		result[index].SQL = cloneSQLDescriptor(contract.SQL)
	}
	return result
}

func cloneSQLDescriptor(descriptor sqlDescriptor) sqlDescriptor {
	descriptor.Joins = append([]string(nil), descriptor.Joins...)
	descriptor.Projection = append([]string(nil), descriptor.Projection...)
	descriptor.Predicates = append([]string(nil), descriptor.Predicates...)
	descriptor.Bind = append([]int(nil), descriptor.Bind...)
	return descriptor
}
