package changeplan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lestrrat-go/rasql/internal/compilerir"
)

func validOperationKind(kind OperationKind) bool {
	switch kind {
	case OperationCreateTable, OperationDropTable, OperationRenameTable,
		OperationAddColumn, OperationDropColumn, OperationRenameColumn,
		OperationAlterColumn, OperationCreateIndex, OperationDropIndex,
		OperationAddConstraint, OperationDropConstraint, OperationBackfill,
		OperationNativeSQL:
		return true
	default:
		return false
	}
}
func validTransaction(mode TransactionMode) bool {
	return mode == TransactionRequired || mode == TransactionForbidden || mode == TransactionEngineDefault
}
func validateFact(f Fact) error {
	if f.object == "" || (f.path != "$" && !strings.HasPrefix(f.path, "/")) {
		return fmt.Errorf("%w: object and path are required", ErrInvalidFact)
	}
	switch FactOperator(f.operator) {
	case FactOperatorEqual:
		_, err := canonicalJSON([]byte(f.canonicalValue))
		if err != nil {
			return fmt.Errorf("%w: invalid canonical value: %v", ErrInvalidFact, err)
		}
	case FactOperatorAbsent, FactOperatorPresent:
		if f.canonicalValue != "" {
			return fmt.Errorf("%w: absent/present values are empty", ErrInvalidFact)
		}
	default:
		return fmt.Errorf("%w: unknown operator %q", ErrInvalidFact, f.operator)
	}
	return nil
}
func validateBaseline(b BaselineIdentity) error {
	if b.catalog.engine < 1 || b.catalog.engine > 4 || strings.TrimSpace(b.sourceIdentity) == "" {
		return fmt.Errorf("%w: catalog or source identity is empty", ErrInvalidIdentity)
	}
	ids := make(map[ObjectID]struct{}, len(b.objects))
	names := make(map[string]struct{}, len(b.objects))
	for _, object := range b.objects {
		if object.id == "" || object.kind == "" || object.name == "" {
			return fmt.Errorf("%w: incomplete baseline object", ErrInvalidIdentity)
		}
		if _, ok := ids[object.id]; ok {
			return fmt.Errorf("%w: duplicate object ID %q", ErrInvalidIdentity, object.id)
		}
		ids[object.id] = struct{}{}
		if object.introducedBy != "" {
			continue
		}
		key := object.schema + "\x00" + object.name
		if _, ok := names[key]; ok {
			return fmt.Errorf("%w: duplicate qualified name %q", ErrInvalidIdentity, object.name)
		}
		names[key] = struct{}{}
	}
	seenRename := make(map[OperationID]struct{}, len(b.renames))
	for _, rename := range b.renames {
		if rename.operation == "" || rename.object == "" || rename.toName == "" {
			return fmt.Errorf("%w: incomplete baseline rename", ErrInvalidIdentity)
		}
		if _, ok := ids[rename.object]; !ok {
			return fmt.Errorf("%w: rename object %q is absent", ErrInvalidIdentity, rename.object)
		}
		if _, ok := seenRename[rename.operation]; ok {
			return fmt.Errorf("%w: duplicate rename operation", ErrInvalidIdentity)
		}
		seenRename[rename.operation] = struct{}{}
	}
	return nil
}

func validatePlanParts(baseline BaselineIdentity, decisions []Decision, operations []Operation) error {
	decisionIDs := make(map[DecisionID]struct{}, len(decisions))
	for _, decision := range decisions {
		if _, ok := decisionIDs[decision.id]; ok {
			return fmt.Errorf("%w: duplicate decision ID", ErrInvalidDecision)
		}
		decisionIDs[decision.id] = struct{}{}
		if _, err := NewDecision(decision.id, decision.kind, decision.object, decision.from, decision.to, decision.accepted, decision.reason); err != nil {
			return err
		}
	}
	opIDs := make(map[OperationID]int, len(operations))
	for index, operation := range operations {
		if _, ok := opIDs[operation.id]; ok {
			return fmt.Errorf("%w: duplicate operation ID", ErrInvalidOperation)
		}
		opIDs[operation.id] = index
		if _, err := NewOperation(operation.id, operation.kind, operation.dependsOn,
			operation.objects, operation.preconditions, operation.postconditions,
			operation.resultDigest, operation.statements, operation.transaction, operation.reversible,
			operation.reverseStatements); err != nil {
			return err
		}
		for _, dependency := range operation.dependsOn {
			if dependency == operation.id {
				return fmt.Errorf("%w: operation depends on itself", ErrInvalidOperation)
			}
			if _, ok := opIDs[dependency]; !ok {
				// Dependencies may refer to a later operation. The complete set is checked below.
				found := false
				for _, candidate := range operations {
					if candidate.id == dependency {
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("%w: missing dependency %q", ErrInvalidOperation, dependency)
				}
			}
		}
	}
	order, err := stableOrder(operations)
	if err != nil {
		return err
	}
	operationByID := make(map[OperationID]Operation, len(operations))
	for _, operation := range operations {
		operationByID[operation.id] = operation
	}
	decisionsByObjectKind := make(map[string]struct{}, len(decisions))
	for _, decision := range decisions {
		decisionsByObjectKind[string(decision.kind)+"\x00"+string(decision.object)] = struct{}{}
	}
	for _, object := range baseline.objects {
		if object.introducedBy == "" {
			continue
		}
		operation, ok := operationByID[object.introducedBy]
		if !ok || operation.kind != OperationCreateTable || len(operation.objects) != 1 || operation.objects[0] != object.id {
			return fmt.Errorf("%w: future object %q does not name its create_table operation", ErrInvalidIdentity, object.id)
		}
	}
	for _, operation := range operations {
		if operation.kind == OperationCreateTable {
			if len(operation.objects) != 1 {
				return fmt.Errorf("%w: create_table needs one object", ErrInvalidOperation)
			}
			found := false
			for _, object := range baseline.objects {
				if object.id == operation.objects[0] && object.introducedBy == operation.id {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("%w: create_table %q lacks a future baseline object", ErrInvalidIdentity, operation.id)
			}
		}
	}
	for _, operation := range operations {
		if operation.kind == OperationDropTable || operation.kind == OperationDropColumn || operation.kind == OperationDropIndex || operation.kind == OperationDropConstraint {
			for _, object := range operation.objects {
				if _, ok := decisionsByObjectKind[string(DecisionAcceptDestructive)+"\x00"+string(object)]; !ok {
					return fmt.Errorf("%w: operation %q lacks destructive decision", ErrInvalidDecision, operation.id)
				}
			}
		}
		if operation.kind == OperationBackfill {
			for _, object := range operation.objects {
				if _, ok := decisionsByObjectKind[string(DecisionSupplyBackfill)+"\x00"+string(object)]; !ok {
					return fmt.Errorf("%w: operation %q lacks backfill decision", ErrInvalidDecision, operation.id)
				}
			}
		}
		if operation.kind == OperationNativeSQL {
			for _, object := range operation.objects {
				if _, ok := decisionsByObjectKind[string(DecisionAcceptNativeSQL)+"\x00"+string(object)]; !ok {
					return fmt.Errorf("%w: operation %q lacks native SQL decision", ErrInvalidDecision, operation.id)
				}
			}
		}
	}
	for object, renameIDs := range renameOperations(operations, order) {
		for i := 1; i < len(renameIDs); i++ {
			if !dependsTransitively(operationByID, renameIDs[i], renameIDs[i-1], make(map[OperationID]struct{})) {
				return fmt.Errorf("%w: rename chain for %q is not dependency ordered", ErrInvalidIdentity, object)
			}
		}
	}
	if err := validateRenameDecisions(baseline, decisions, operations); err != nil {
		return err
	}
	return validatePrefixes(baseline, operations, order)
}

func validateFutureObjectIDs(baseline BaselineIdentity, operations []Operation) error {
	creates := make(map[OperationID]Operation)
	for _, operation := range operations {
		if operation.kind == OperationCreateTable {
			creates[operation.id] = operation
		}
	}
	seen := make(map[ObjectID]struct{}, len(baseline.objects))
	for _, object := range baseline.objects {
		if _, ok := seen[object.id]; ok {
			return fmt.Errorf("%w: duplicate occurrence ID %q", ErrInvalidIdentity, object.id)
		}
		seen[object.id] = struct{}{}
		if object.introducedBy == "" {
			continue
		}
		operation, ok := creates[object.introducedBy]
		if !ok || len(operation.objects) != 1 || operation.objects[0] != object.id {
			return fmt.Errorf("%w: future object %q has no matching create operation", ErrInvalidIdentity, object.id)
		}
		want, err := introducedObjectID(baseline.sourceIdentity, object.introducedBy, object.kind, object.schema, object.name)
		if err != nil || want != object.id {
			return fmt.Errorf("%w: future object %q has an invalid deterministic ID", ErrInvalidIdentity, object.id)
		}
	}
	for _, operation := range operations {
		if operation.kind != OperationCreateTable {
			continue
		}
		if len(operation.objects) != 1 {
			return fmt.Errorf("%w: create_table needs one object", ErrInvalidOperation)
		}
		found := false
		for _, object := range baseline.objects {
			if object.introducedBy == operation.id && object.id == operation.objects[0] {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: create operation %q has no future occurrence", ErrInvalidIdentity, operation.id)
		}
	}
	return nil
}

func validateResolvedState(resolved ResolvedChanges, baseline BaselineIdentity) error {
	if err := validatePlanParts(baseline, resolved.decisions, resolved.operations); err != nil {
		return err
	}
	if err := validateFutureObjectIDs(baseline, resolved.operations); err != nil {
		return err
	}
	order, err := stableOrder(resolved.operations)
	if err != nil {
		return err
	}
	if err := validateResolvedResultDigests(resolved, order); err != nil {
		return err
	}
	if err := validateResolvedRenameDecisions(resolved, baseline, order); err != nil {
		return err
	}
	active := make(map[ObjectID]BaselineObject)
	for _, object := range baseline.objects {
		if object.introducedBy == "" {
			active[object.id] = object
		}
	}
	if err := matchActiveCatalog(resolved.baseline, active); err != nil {
		return err
	}
	futureByCreate := make(map[OperationID]BaselineObject)
	for _, object := range baseline.objects {
		if object.introducedBy != "" {
			futureByCreate[object.introducedBy] = object
		}
	}
	renameByOperation := make(map[OperationID]BaselineRename)
	for _, rename := range baseline.renames {
		renameByOperation[rename.operation] = rename
	}
	for i, index := range order {
		operation := resolved.operations[index]
		switch operation.kind {
		case OperationCreateTable:
			object, ok := futureByCreate[operation.id]
			if !ok {
				return fmt.Errorf("%w: missing future occurrence for %q", ErrInvalidIdentity, operation.id)
			}
			if _, exists := active[object.id]; exists {
				return fmt.Errorf("%w: occurrence %q is already active", ErrInvalidIdentity, object.id)
			}
			active[object.id] = object
		case OperationRenameTable:
			rename, ok := renameByOperation[operation.id]
			if !ok {
				return fmt.Errorf("%w: rename %q is missing binding", ErrInvalidIdentity, operation.id)
			}
			object, ok := active[rename.object]
			if !ok {
				return fmt.Errorf("%w: rename %q uses inactive occurrence", ErrInvalidIdentity, operation.id)
			}
			object.schema, object.name = rename.toSchema, rename.toName
			active[rename.object] = object
		case OperationDropTable:
			for _, id := range operation.objects {
				if _, ok := active[id]; !ok {
					return fmt.Errorf("%w: drop uses inactive occurrence %q", ErrInvalidIdentity, id)
				}
				delete(active, id)
			}
		}
		if err := matchActiveCatalog(resolved.steps[i].after, active); err != nil {
			return err
		}
	}
	return nil
}

func validateResolvedResultDigests(resolved ResolvedChanges, order []int) error {
	if len(resolved.steps) != len(resolved.operations) {
		return fmt.Errorf("%w: resolved catalog step count does not match operations", ErrInvalidPlan)
	}
	for i, index := range order {
		digest, err := CatalogDigest(resolved.steps[i].after)
		if err != nil {
			return err
		}
		if digest != resolved.operations[index].resultDigest {
			return fmt.Errorf("%w: operation %q result digest does not match resolved catalog", ErrInvalidPlan, resolved.operations[index].id)
		}
	}
	return nil
}

func validateRenameDecisions(baseline BaselineIdentity, decisions []Decision, operations []Operation) error {
	order, err := stableOrder(operations)
	if err != nil {
		return err
	}
	renames := make(map[OperationID]BaselineRename, len(baseline.renames))
	operationByID := make(map[OperationID]Operation, len(operations))
	for _, operation := range operations {
		operationByID[operation.id] = operation
	}
	for _, rename := range baseline.renames {
		if _, exists := renames[rename.operation]; exists {
			return fmt.Errorf("%w: duplicate rename binding", ErrInvalidIdentity)
		}
		operation, exists := operationByID[rename.operation]
		if !exists || operation.kind != OperationRenameTable || len(operation.objects) != 1 || operation.objects[0] != rename.object {
			return fmt.Errorf("%w: rename binding mismatch", ErrInvalidIdentity)
		}
		renames[rename.operation] = rename
	}
	active := make(map[ObjectID]BaselineObject)
	for _, object := range baseline.objects {
		if object.introducedBy == "" {
			active[object.id] = object
		}
	}
	used := make([]bool, len(decisions))
	expectedTables := make([]renameDecisionKey, 0)
	for _, index := range order {
		operation := operations[index]
		if operation.kind == OperationCreateTable {
			for _, object := range baseline.objects {
				if object.introducedBy == operation.id {
					active[object.id] = object
				}
			}
		}
		if operation.kind == OperationRenameTable {
			rename, ok := renames[operation.id]
			if !ok || len(operation.objects) != 1 || rename.object != operation.objects[0] {
				return fmt.Errorf("%w: rename binding mismatch", ErrInvalidDecision)
			}
			object, ok := active[rename.object]
			if !ok {
				return fmt.Errorf("%w: rename object is inactive", ErrInvalidDecision)
			}
			expectedTables = append(expectedTables, renameDecisionKey{
				object: rename.object, from: decisionName(object.schema, object.name),
				to: decisionName(rename.toSchema, rename.toName), operation: operation.id,
			})
			object.schema, object.name = rename.toSchema, rename.toName
			active[rename.object] = object
		}
		if operation.kind == OperationDropTable {
			for _, id := range operation.objects {
				delete(active, id)
			}
		}
	}
	if err := consumeRenameDecisions(decisions, used, expectedTables); err != nil {
		return err
	}
	columnCounts := make(map[ObjectID]int)
	for _, operation := range operations {
		if operation.kind != OperationRenameColumn {
			continue
		}
		for _, objectID := range operation.objects {
			columnCounts[objectID]++
		}
	}
	for objectID, count := range columnCounts {
		matches := 0
		for i, decision := range decisions {
			if !used[i] && decision.kind == DecisionRenameObject && decision.object == objectID {
				matches++
			}
		}
		if matches != count {
			return fmt.Errorf("%w: rename for %q has %d matching decisions, want %d", ErrInvalidDecision, objectID, matches, count)
		}
		for i, decision := range decisions {
			if !used[i] && decision.kind == DecisionRenameObject && decision.object == objectID {
				used[i] = true
			}
		}
	}
	return rejectUnusedRenameDecisions(decisions, used)
}

type renameDecisionKey struct {
	operation OperationID
	object    ObjectID
	from      string
	to        string
}

func consumeRenameDecisions(decisions []Decision, used []bool, expected []renameDecisionKey) error {
	for _, want := range expected {
		matchingIndex := -1
		for i, decision := range decisions {
			if used[i] || decision.kind != DecisionRenameObject || decision.object != want.object ||
				decision.from != want.from || decision.to != want.to {
				continue
			}
			matchingIndex = i
			break
		}
		if matchingIndex < 0 {
			return fmt.Errorf("%w: rename %q has no matching decision", ErrInvalidDecision, want.operation)
		}
		used[matchingIndex] = true
	}
	return nil
}

func rejectUnusedRenameDecisions(decisions []Decision, used []bool) error {
	for i, decision := range decisions {
		if decision.kind == DecisionRenameObject && !used[i] {
			return fmt.Errorf("%w: rename decision %q is unused", ErrInvalidDecision, decision.id)
		}
	}
	return nil
}

func validateResolvedRenameDecisions(resolved ResolvedChanges, baseline BaselineIdentity, order []int) error {
	used := make([]bool, len(resolved.decisions))
	active := make(map[ObjectID]BaselineObject)
	futureByCreate := make(map[OperationID]BaselineObject)
	for _, object := range baseline.objects {
		if object.introducedBy == "" {
			active[object.id] = object
			continue
		}
		futureByCreate[object.introducedBy] = object
	}
	renamed := make(map[OperationID]BaselineRename, len(baseline.renames))
	for _, rename := range baseline.renames {
		renamed[rename.operation] = rename
	}
	expected := make([]renameDecisionKey, 0)
	for stepIndex, operationIndex := range order {
		operation := resolved.operations[operationIndex]
		switch operation.kind {
		case OperationCreateTable:
			object, ok := futureByCreate[operation.id]
			if !ok {
				return fmt.Errorf("%w: create %q has no future object", ErrInvalidDecision, operation.id)
			}
			active[object.id] = object
		case OperationRenameTable:
			rename, ok := renamed[operation.id]
			if !ok || len(operation.objects) != 1 || operation.objects[0] != rename.object {
				return fmt.Errorf("%w: rename binding mismatch", ErrInvalidDecision)
			}
			object, ok := active[rename.object]
			if !ok {
				return fmt.Errorf("%w: rename object is inactive", ErrInvalidDecision)
			}
			expected = append(expected, renameDecisionKey{operation: operation.id, object: rename.object,
				from: decisionName(object.schema, object.name), to: decisionName(rename.toSchema, rename.toName)})
			object.schema, object.name = rename.toSchema, rename.toName
			active[rename.object] = object
		case OperationRenameColumn:
			before := resolved.baseline
			if stepIndex > 0 {
				before = resolved.steps[stepIndex-1].after
			}
			after := resolved.steps[stepIndex].after
			for _, objectID := range operation.objects {
				if _, ok := active[objectID]; !ok {
					return fmt.Errorf("%w: rename object is inactive", ErrInvalidDecision)
				}
				from, to, ok := adjacentColumnRename(before, after, objectID)
				if !ok {
					return fmt.Errorf("%w: rename %q has no single adjacent column change", ErrInvalidDecision, operation.id)
				}
				expected = append(expected, renameDecisionKey{operation: operation.id, object: objectID, from: from, to: to})
			}
		case OperationDropTable:
			for _, objectID := range operation.objects {
				delete(active, objectID)
			}
		}
	}
	if err := consumeRenameDecisions(resolved.decisions, used, expected); err != nil {
		return err
	}
	return rejectUnusedRenameDecisions(resolved.decisions, used)
}

func adjacentColumnRename(before, after Catalog, objectID ObjectID) (string, string, bool) {
	var beforeObject, afterObject *compilerir.PhysicalObject
	for i := range before.physical.Objects {
		if ObjectID(before.physical.Objects[i].ID) == objectID {
			beforeObject = &before.physical.Objects[i]
			break
		}
	}
	for i := range after.physical.Objects {
		if ObjectID(after.physical.Objects[i].ID) == objectID {
			afterObject = &after.physical.Objects[i]
			break
		}
	}
	if beforeObject == nil || afterObject == nil || len(beforeObject.Columns) != len(afterObject.Columns) {
		return "", "", false
	}
	from, to := "", ""
	for i := range beforeObject.Columns {
		if beforeObject.Columns[i].Name == afterObject.Columns[i].Name {
			continue
		}
		if from != "" {
			return "", "", false
		}
		from, to = beforeObject.Columns[i].Name, afterObject.Columns[i].Name
	}
	return from, to, from != "" && to != ""
}

func decisionName(schemaName, name string) string {
	if schemaName == "" {
		return name
	}
	return schemaName + "." + name
}

func matchActiveCatalog(catalog Catalog, active map[ObjectID]BaselineObject) error {
	if err := catalog.validate(); err != nil {
		return err
	}
	if len(catalog.physical.Objects) != len(active) {
		return fmt.Errorf("%w: catalog active occurrence count differs", ErrInvalidPlan)
	}
	for _, object := range catalog.physical.Objects {
		want, ok := active[ObjectID(object.ID)]
		if !ok || want.kind != object.Kind || want.schema != object.Schema || want.name != object.Name {
			return fmt.Errorf("%w: catalog occurrence %q differs", ErrInvalidPlan, object.ID)
		}
	}
	return nil
}

func renameOperations(operations []Operation, order []int) map[ObjectID][]OperationID {
	out := make(map[ObjectID][]OperationID)
	for _, index := range order {
		operation := operations[index]
		if operation.kind != OperationRenameTable {
			continue
		}
		for _, object := range operation.objects {
			out[object] = append(out[object], operation.id)
		}
	}
	return out
}
func dependsTransitively(operations map[OperationID]Operation, operation, dependency OperationID, seen map[OperationID]struct{}) bool {
	if operation == dependency {
		return true
	}
	if _, ok := seen[operation]; ok {
		return false
	}
	seen[operation] = struct{}{}
	for _, parent := range operations[operation].dependsOn {
		if dependsTransitively(operations, parent, dependency, seen) {
			return true
		}
	}
	return false
}

func stableOrder(operations []Operation) ([]int, error) {
	index := make(map[OperationID]int, len(operations))
	for i, operation := range operations {
		index[operation.id] = i
	}
	indegree := make([]int, len(operations))
	dependents := make([][]int, len(operations))
	for i, operation := range operations {
		indegree[i] = len(operation.dependsOn)
		for _, dependency := range operation.dependsOn {
			j, ok := index[dependency]
			if !ok {
				return nil, fmt.Errorf("%w: missing dependency %q", ErrInvalidOperation, dependency)
			}
			dependents[j] = append(dependents[j], i)
		}
	}
	ready := make([]int, 0, len(operations))
	for i, degree := range indegree {
		if degree == 0 {
			ready = append(ready, i)
		}
	}
	order := make([]int, 0, len(operations))
	for len(ready) > 0 {
		sort.Ints(ready)
		i := ready[0]
		ready = ready[1:]
		order = append(order, i)
		for _, dependent := range dependents[i] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				ready = append(ready, dependent)
			}
		}
	}
	if len(order) != len(operations) {
		return nil, ErrDependencyCycle
	}
	return order, nil
}

func validatePrefixes(baseline BaselineIdentity, operations []Operation, order []int) error {
	objects := make(map[ObjectID]BaselineObject, len(baseline.objects))
	active := make(map[string]ObjectID, len(baseline.objects))
	for _, object := range baseline.objects {
		if object.introducedBy == "" {
			objects[object.id] = object
			active[qualifiedKey(object.kind, object.schema, object.name)] = object.id
		}
	}
	renamed := make(map[ObjectID]struct{})
	for _, index := range order {
		operation := operations[index]
		switch operation.kind {
		case OperationCreateTable:
			if len(operation.objects) != 1 {
				return fmt.Errorf("%w: create_table needs one object", ErrInvalidOperation)
			}
			id := operation.objects[0]
			for _, object := range baseline.objects {
				if object.id == id && object.introducedBy == operation.id {
					objects[id] = object
					if _, ok := active[qualifiedKey(object.kind, object.schema, object.name)]; ok {
						return fmt.Errorf("%w: create destination collision", ErrInvalidIdentity)
					}
					active[qualifiedKey(object.kind, object.schema, object.name)] = id
				}
			}
		case OperationRenameTable:
			if len(operation.objects) != 1 {
				return fmt.Errorf("%w: rename_table needs one object", ErrInvalidOperation)
			}
			id := operation.objects[0]
			object, ok := objects[id]
			if !ok {
				return fmt.Errorf("%w: rename before create or baseline", ErrInvalidIdentity)
			}
			var destination *BaselineRename
			for i := range baseline.renames {
				if baseline.renames[i].operation == operation.id {
					destination = &baseline.renames[i]
					break
				}
			}
			if destination == nil || destination.object != id {
				return fmt.Errorf("%w: rename destination is missing", ErrInvalidIdentity)
			}
			delete(active, qualifiedKey(object.kind, object.schema, object.name))
			key := qualifiedKey(object.kind, destination.toSchema, destination.toName)
			if _, collision := active[key]; collision {
				return fmt.Errorf("%w: rename destination collision", ErrInvalidIdentity)
			}
			object.schema, object.name = destination.toSchema, destination.toName
			objects[id] = object
			active[key] = id
			renamed[id] = struct{}{}
		default:
			for _, id := range operation.objects {
				if _, ok := objects[id]; !ok {
					return fmt.Errorf("%w: operation references inactive object %q", ErrInvalidIdentity, id)
				}
			}
		}
		if operation.kind == OperationDropTable {
			for _, id := range operation.objects {
				if object, ok := objects[id]; ok {
					delete(active, qualifiedKey(object.kind, object.schema, object.name))
					delete(objects, id)
				}
			}
		}
	}
	return nil
}
func qualifiedKey(kind, schema, name string) string { return schema + "\x00" + name }

func (p Plan) StableOperationOrder() ([]OperationID, error) {
	order, err := stableOrder(p.operations)
	if err != nil {
		return nil, err
	}
	out := make([]OperationID, len(order))
	for i, index := range order {
		out[i] = p.operations[index].id
	}
	return out, nil
}
func (p Plan) TopologicalOrder() ([]OperationID, error) { return p.StableOperationOrder() }
func (p Plan) TopologicalOperations() ([]Operation, error) {
	order, err := stableOrder(p.operations)
	if err != nil {
		return nil, err
	}
	out := make([]Operation, len(order))
	for i, index := range order {
		out[i] = cloneOperation(p.operations[index])
	}
	return out, nil
}
