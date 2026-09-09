package migrate

import (
	"fmt"
	"sort"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/migrate/changeplan"
)

type activePlanOccurrence struct {
	id           changeplan.ObjectID
	kind         string
	schema, name string
}

func activePlanOccurrences(prepared preparedChangePlan, prefix int) ([]activePlanOccurrence, error) {
	if prefix < 0 || prefix > len(prepared.operations) {
		return nil, fmt.Errorf("migrate: plan prefix %d is outside operation range", prefix)
	}
	active := make(map[changeplan.ObjectID]activePlanOccurrence)
	future := make(map[changeplan.OperationID][]activePlanOccurrence)
	for _, object := range prepared.baseline.Objects() {
		occurrence := activePlanOccurrence{id: object.ID(), kind: object.Kind(), schema: object.Schema(), name: object.Name()}
		if object.IntroducedBy() == "" {
			if _, exists := active[occurrence.id]; exists {
				return nil, fmt.Errorf("migrate: duplicate active object ID %q", occurrence.id)
			}
			active[occurrence.id] = occurrence
			continue
		}
		future[object.IntroducedBy()] = append(future[object.IntroducedBy()], occurrence)
	}
	renamed := make(map[changeplan.OperationID][]changeplan.BaselineRename)
	for _, rename := range prepared.baseline.Renames() {
		renamed[rename.Operation()] = append(renamed[rename.Operation()], rename)
	}
	if err := validateActivePlanOccurrences(active); err != nil {
		return nil, err
	}
	for index := 0; index < prefix; index++ {
		operation := prepared.operations[index].operation
		switch operation.Kind() {
		case changeplan.OperationCreateTable:
			created := future[operation.ID()]
			if len(created) != 1 || !operationNamesObject(operation, created[0].id) {
				return nil, fmt.Errorf("migrate: create operation %q has no unique future object", operation.ID())
			}
			if _, exists := active[created[0].id]; exists {
				return nil, fmt.Errorf("migrate: create operation %q activates existing object %q", operation.ID(), created[0].id)
			}
			active[created[0].id] = created[0]
		case changeplan.OperationDropTable:
			for _, id := range operation.Objects() {
				if _, exists := active[id]; !exists {
					return nil, fmt.Errorf("migrate: drop operation %q names inactive object %q", operation.ID(), id)
				}
				delete(active, id)
			}
		case changeplan.OperationRenameTable:
			renames := renamed[operation.ID()]
			if len(renames) != 1 || !operationNamesObject(operation, renames[0].Object()) {
				return nil, fmt.Errorf("migrate: rename operation %q has no unique destination", operation.ID())
			}
			current, exists := active[renames[0].Object()]
			if !exists {
				return nil, fmt.Errorf("migrate: rename operation %q names inactive object %q", operation.ID(), renames[0].Object())
			}
			current.schema = renames[0].ToSchema()
			current.name = renames[0].ToName()
			active[current.id] = current
		}
		if err := validateActivePlanOccurrences(active); err != nil {
			return nil, fmt.Errorf("migrate: plan prefix %d: %w", index+1, err)
		}
	}
	out := make([]activePlanOccurrence, 0, len(active))
	for _, occurrence := range active {
		out = append(out, occurrence)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].kind != out[j].kind {
			return out[i].kind < out[j].kind
		}
		if out[i].schema != out[j].schema {
			return out[i].schema < out[j].schema
		}
		if out[i].name != out[j].name {
			return out[i].name < out[j].name
		}
		return out[i].id < out[j].id
	})
	return out, nil
}

func operationNamesObject(operation changeplan.Operation, id changeplan.ObjectID) bool {
	for _, object := range operation.Objects() {
		if object == id {
			return true
		}
	}
	return false
}

func validateActivePlanOccurrences(active map[changeplan.ObjectID]activePlanOccurrence) error {
	names := make(map[string]changeplan.ObjectID, len(active))
	for id, occurrence := range active {
		key := occurrence.kind + "\x00" + occurrence.schema + "\x00" + occurrence.name
		if previous, exists := names[key]; exists && previous != id {
			return fmt.Errorf("migrate: active objects %q and %q share a qualified name", previous, id)
		}
		names[key] = id
	}
	return nil
}

func priorObjectsForPrefix(prepared preparedChangePlan, prefix int) ([]compilerir.PriorObject, error) {
	active, err := activePlanOccurrences(prepared, prefix)
	if err != nil {
		return nil, err
	}
	out := make([]compilerir.PriorObject, len(active))
	for index, occurrence := range active {
		out[index] = compilerir.PriorObject{
			ID:   compilerir.ObjectID(occurrence.id),
			Kind: occurrence.kind,
			Name: compilerir.QualifiedName{Schema: occurrence.schema, Name: occurrence.name},
		}
	}
	return out, nil
}
