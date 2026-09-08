package changeplan

import (
	"fmt"
	"sort"
	"strings"
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
			operation.statements, operation.transaction, operation.reversible,
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
	decisionsByObjectKind := make(map[string]struct{}, len(decisions))
	for _, d := range decisions {
		decisionsByObjectKind[string(d.kind)+"\x00"+string(d.object)] = struct{}{}
	}
	operationByID := make(map[OperationID]Operation, len(operations))
	for _, operation := range operations {
		operationByID[operation.id] = operation
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
		if operation.kind == OperationRenameTable || operation.kind == OperationRenameColumn {
			for _, object := range operation.objects {
				if _, ok := decisionsByObjectKind[string(DecisionRenameObject)+"\x00"+string(object)]; !ok {
					return fmt.Errorf("%w: rename operation %q lacks rename decision", ErrInvalidDecision, operation.id)
				}
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
	for object, renameIDs := range renameOperations(operations) {
		for i := 1; i < len(renameIDs); i++ {
			if !dependsTransitively(operationByID, renameIDs[i], renameIDs[i-1], make(map[OperationID]struct{})) {
				return fmt.Errorf("%w: rename chain for %q is not dependency ordered", ErrInvalidIdentity, object)
			}
		}
	}
	return validatePrefixes(baseline, operations, order)
}

func renameOperations(operations []Operation) map[ObjectID][]OperationID {
	out := make(map[ObjectID][]OperationID)
	for _, operation := range operations {
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
				if _, ok := objects[id]; !ok && operation.kind != OperationDropTable && operation.kind != OperationDropColumn && operation.kind != OperationDropIndex && operation.kind != OperationDropConstraint {
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
