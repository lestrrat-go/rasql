package rasql

import (
	"context"
	"fmt"
	"github.com/lestrrat-go/rasql/query"
	"reflect"
	"strconv"
)

// RelationshipLoadOptions controls filtering, ordering, result caps, and bind batching.
type RelationshipLoadOptions struct {
	Where          query.Expression
	OrderBy        []query.Order
	PerParentLimit int
	BindLimit      int
}

// LoadHasManyPlan loads children for parents in bounded key batches.
func LoadHasManyPlan[Parent, Child any, Key comparable](ctx context.Context, db DB, childTable Table[Child], childKeyColumns []query.ColumnRef, parents []Parent, parentKey func(Parent) Key, childKey func(Child) Key, keyValues func(Key) ([]any, bool), options RelationshipLoadOptions) (map[Key][]Child, error) {
	if err := validateRelationshipPlan(childTable, childKeyColumns, options); err != nil {
		return nil, err
	}
	grouped := make(map[Key][]Child, len(parents))
	keys := make([]Key, 0, len(parents))
	stored := make(map[Key][]any, len(parents))
	seen := make(map[Key]struct{}, len(parents))
	for _, parent := range parents {
		key := parentKey(parent)
		values, present := keyValues(key)
		if !present {
			continue
		}
		if len(values) != len(childKeyColumns) {
			return nil, fmt.Errorf("relationship key has %d values, want %d", len(values), len(childKeyColumns))
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
		stored[key] = relationshipQueryValues(values)
		grouped[key] = nil
	}
	if len(keys) == 0 {
		return grouped, nil
	}
	err := executeRelationshipBatches(ctx, db, childTable, childKeyColumns, keys, stored, options, func(rows []Child) error {
		for _, row := range rows {
			key := childKey(row)
			if _, ok := grouped[key]; !ok {
				continue
			}
			if options.PerParentLimit > 0 && len(grouped[key]) >= options.PerParentLimit {
				continue
			}
			grouped[key] = append(grouped[key], row)
		}
		return nil
	})
	return grouped, err
}

// LoadBelongsToPlan loads scalar parents for children in bounded key batches.
func LoadBelongsToPlan[Child, Parent any, Key comparable](ctx context.Context, db DB, parentTable Table[Parent], parentKeyColumns []query.ColumnRef, children []Child, childKey func(Child) Key, parentKey func(Parent) Key, keyValues func(Key) ([]any, bool), options RelationshipLoadOptions) (map[Key]Parent, error) {
	if err := validateRelationshipPlan(parentTable, parentKeyColumns, options); err != nil {
		return nil, err
	}
	loaded := make(map[Key]Parent, len(children))
	keys := make([]Key, 0, len(children))
	stored := make(map[Key][]any, len(children))
	seen := make(map[Key]struct{}, len(children))
	for _, child := range children {
		key := childKey(child)
		values, present := keyValues(key)
		if !present {
			continue
		}
		if len(values) != len(parentKeyColumns) {
			return nil, fmt.Errorf("relationship key has %d values, want %d", len(values), len(parentKeyColumns))
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
		stored[key] = relationshipQueryValues(values)
	}
	if len(keys) == 0 {
		return loaded, nil
	}
	err := executeRelationshipBatches(ctx, db, parentTable, parentKeyColumns, keys, stored, options, func(rows []Parent) error {
		for _, row := range rows {
			key := parentKey(row)
			if _, exists := loaded[key]; exists {
				return fmt.Errorf("relationship returned duplicate parent for key %v", key)
			}
			loaded[key] = row
		}
		return nil
	})
	return loaded, err
}

// LoadHasMany is the compatibility scalar adapter.
func LoadHasMany[Parent, Child any, Key comparable](ctx context.Context, db DB, childTable Table[Child], childKeyColumn query.ColumnRef, parents []Parent, parentKey func(Parent) Key, childKey func(Child) Key) (map[Key][]Child, error) {
	return LoadHasManyPlan(ctx, db, childTable, []query.ColumnRef{childKeyColumn}, parents, parentKey, childKey, func(key Key) ([]any, bool) { return []any{relationshipQueryKey(key)}, true }, RelationshipLoadOptions{})
}

// LoadBelongsTo is the compatibility scalar adapter.
func LoadBelongsTo[Child, Parent any, Key comparable](ctx context.Context, db DB, parentTable Table[Parent], parentKeyColumn query.ColumnRef, children []Child, childKey func(Child) Key, parentKey func(Parent) Key) (map[Key]Parent, error) {
	return LoadBelongsToPlan(ctx, db, parentTable, []query.ColumnRef{parentKeyColumn}, children, childKey, parentKey, func(key Key) ([]any, bool) { return []any{relationshipQueryKey(key)}, true }, RelationshipLoadOptions{})
}

func validateRelationshipPlan[T any](table Table[T], columns []query.ColumnRef, options RelationshipLoadOptions) error {
	if len(columns) == 0 {
		return fmt.Errorf("relationship key columns must not be empty")
	}
	if options.PerParentLimit < 0 || options.BindLimit < 0 {
		return fmt.Errorf("relationship load limits must not be negative")
	}
	for _, column := range columns {
		if err := column.Validate(); err != nil {
			return err
		}
		if column.Source().QualifiedName() != table.Ref().QualifiedName() {
			return fmt.Errorf("relationship key column %q is not from loaded table", column.Name())
		}
	}
	return nil
}

func executeRelationshipBatches[Row any, Key comparable](ctx context.Context, db DB, table Table[Row], columns []query.ColumnRef, keys []Key, stored map[Key][]any, options RelationshipLoadOptions, consume func([]Row) error) error {
	budget := options.BindLimit
	if budget == 0 {
		budget = db.RelationshipBindLimit()
	}
	if budget == 0 {
		budget = relationshipDefaultBindLimit(db)
	}
	width := len(columns)
	probeValues := stored[keys[0]]
	probe := SelectFrom(table).Where(relationshipMembership(columns, [][]any{probeValues}))
	if len(options.OrderBy) > 0 {
		for _, column := range columns {
			probe = probe.OrderAsc(column)
		}
	}
	if options.Where != nil {
		probe = probe.Where(options.Where)
	}
	for _, order := range options.OrderBy {
		probe = probe.Order(order)
	}
	statement, err := probe.Build(db.Dialect())
	if err != nil {
		return err
	}
	fixed := len(statement.BoundArgs()) - width
	if budget-fixed < width {
		return fmt.Errorf("relationship bind limit %d cannot fit one key", budget)
	}
	batchSize := (budget - fixed) / width
	for start := 0; start < len(keys); start += batchSize {
		end := start + batchSize
		if end > len(keys) {
			end = len(keys)
		}
		values := make([][]any, 0, end-start)
		for _, key := range keys[start:end] {
			values = append(values, stored[key])
		}
		builder := SelectFrom(table).Where(relationshipMembership(columns, values))
		if len(options.OrderBy) > 0 {
			for _, column := range columns {
				builder = builder.OrderAsc(column)
			}
		}
		if options.Where != nil {
			builder = builder.Where(options.Where)
		}
		for _, order := range options.OrderBy {
			builder = builder.Order(order)
		}
		rows, err := builder.All(ctx, db)
		if err != nil {
			return err
		}
		if err := consume(rows); err != nil {
			return err
		}
	}
	return nil
}

func relationshipQueryValues(values []any) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = relationshipQueryKey(value)
	}
	return result
}

func relationshipMembership(columns []query.ColumnRef, values [][]any) query.Expression {
	if len(columns) == 1 {
		flat := make([]any, 0, len(values))
		for _, value := range values {
			flat = append(flat, value[0])
		}
		return query.In(columns[0], flat...)
	}
	return query.TupleIn(columns, values)
}

func relationshipDefaultBindLimit(db DB) int {
	if db.Dialect().Name() == "sqlite" {
		return 999
	}
	return 65535
}
func relationshipQueryKey(key any) any {
	value := reflect.ValueOf(key)
	if value.IsValid() && value.Kind() == reflect.Uint64 && value.Uint() > 1<<63-1 {
		return strconv.FormatUint(value.Uint(), 10)
	}
	return key
}
