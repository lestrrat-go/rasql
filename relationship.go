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

// LoadHasOnePlan loads at most one child for each source key and rejects duplicate targets.
func LoadHasOnePlan[Parent, Child any, Key comparable](ctx context.Context, db DB, childTable Table[Child], childKeyColumns []query.ColumnRef, parents []Parent, parentKey func(Parent) Key, childKey func(Child) Key, keyValues func(Key) ([]any, bool), options RelationshipLoadOptions) (map[Key]Child, error) {
	grouped, err := LoadHasManyPlan(ctx, db, childTable, childKeyColumns, parents, parentKey, childKey, keyValues, options)
	if err != nil {
		return nil, err
	}
	result := make(map[Key]Child, len(grouped))
	for key, rows := range grouped {
		if len(rows) > 1 {
			return nil, fmt.Errorf("relationship returned duplicate child for key %v", key)
		}
		if len(rows) == 1 {
			result[key] = rows[0]
		}
	}
	return result, nil
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

// LoadManyToManyPlan loads target rows through an ordered join table in bounded key batches.
func LoadManyToManyPlan[Source, Through, Target any, Key comparable](ctx context.Context, db DB, through Table[Through], target Table[Target], throughSourceColumns, throughTargetColumns, targetJoinColumns, targetColumns []query.ColumnRef, sources []Source, sourceKey func(Source) Key, keyValues func(Key) ([]any, bool), decode func(ScanSource) (Key, Target, error), options RelationshipLoadOptions) (map[Key][]Target, error) {
	if len(throughSourceColumns) == 0 || len(throughSourceColumns) != len(throughTargetColumns) {
		return nil, fmt.Errorf("many-to-many through key widths must match and be non-empty")
	}
	if len(targetColumns) == 0 {
		return nil, fmt.Errorf("many-to-many target columns must not be empty")
	}
	if options.PerParentLimit < 0 || options.BindLimit < 0 {
		return nil, fmt.Errorf("relationship load limits must not be negative")
	}
	if len(throughTargetColumns) != len(targetJoinColumns) {
		return nil, fmt.Errorf("many-to-many join key widths must match")
	}
	for _, column := range append(append(append(append([]query.ColumnRef{}, throughSourceColumns...), throughTargetColumns...), targetJoinColumns...), targetColumns...) {
		if err := column.Validate(); err != nil {
			return nil, err
		}
	}
	keys := make([]Key, 0, len(sources))
	stored := make(map[Key][]any, len(sources))
	result := make(map[Key][]Target, len(sources))
	seen := make(map[Key]struct{}, len(sources))
	for _, source := range sources {
		key := sourceKey(source)
		values, present := keyValues(key)
		if !present {
			continue
		}
		if len(values) != len(throughSourceColumns) {
			return nil, fmt.Errorf("relationship key has %d values, want %d", len(values), len(throughSourceColumns))
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
		stored[key] = relationshipQueryValues(values)
		result[key] = nil
	}
	if len(keys) == 0 {
		return result, nil
	}
	load := func(values [][]any) error {
		builder := DecodeFrom[Target](target)
		projections := make([]query.Projection, 0, len(throughSourceColumns)+len(targetColumns))
		for _, column := range throughSourceColumns {
			projections = append(projections, column)
		}
		for _, column := range targetColumns {
			projections = append(projections, column)
		}
		builder = builder.Project(projections...).Join(InnerJoin(through, relationshipJoinExpression(equalColumns(throughTargetColumns, targetJoinColumns))))
		builder = builder.Where(relationshipMembership(throughSourceColumns, values))
		for _, column := range throughSourceColumns {
			builder = builder.OrderAsc(column)
		}
		for _, order := range options.OrderBy {
			builder = builder.Order(order)
		}
		if options.Where != nil {
			builder = builder.Where(options.Where)
		}
		statement, err := builder.Build(db.Dialect())
		if err != nil {
			return err
		}
		rows, err := db.QueryRendered(ctx, statement)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			key, row, err := decode(rows)
			if err != nil {
				return err
			}
			if _, ok := result[key]; !ok {
				continue
			}
			if options.PerParentLimit > 0 && len(result[key]) >= options.PerParentLimit {
				continue
			}
			result[key] = append(result[key], row)
		}
		return rows.Err()
	}
	budget := options.BindLimit
	if budget == 0 {
		budget = db.RelationshipBindLimit()
	}
	if budget == 0 {
		budget = relationshipDefaultBindLimit(db)
	}
	width := len(throughSourceColumns)
	batchSize := budget / width
	if batchSize < 1 {
		return nil, fmt.Errorf("relationship bind limit %d cannot fit one key", budget)
	}
	for start := 0; start < len(keys); start += batchSize {
		end := start + batchSize
		if end > len(keys) {
			end = len(keys)
		}
		values := make([][]any, 0, end-start)
		for _, key := range keys[start:end] {
			values = append(values, stored[key])
		}
		if err := load(values); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func equalColumns(left, right []query.ColumnRef) []query.Expression {
	result := make([]query.Expression, len(left))
	for index := range left {
		result[index] = query.Equal(left[index], right[index])
	}
	return result
}

func relationshipJoinExpression(expressions []query.Expression) query.Expression {
	if len(expressions) == 1 {
		return expressions[0]
	}
	return query.And(expressions...)
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
	keys := make([]query.Expression, 0, len(values))
	for _, value := range values {
		components := make([]query.Expression, 0, len(columns))
		for index, column := range columns {
			components = append(components, query.Equal(column, value[index]))
		}
		keys = append(keys, query.And(components...))
	}
	if len(keys) == 1 {
		return keys[0]
	}
	return query.Or(keys...)
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
