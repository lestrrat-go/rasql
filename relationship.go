package rasql

import (
	"context"

	"github.com/lestrrat-go/rasql/query"
)

// LoadHasMany loads children for a set of parents in one query and groups the
// result by the parent's key. It is used by generated one-to-many relations.
// The key functions must return the same comparable key type for both rows.
// An empty parent slice returns an empty map without executing a query.
func LoadHasMany[Parent, Child any, Key comparable](
	ctx context.Context,
	x Executor,
	childTable Table[Child],
	childKeyColumn query.Column,
	parents []Parent,
	parentKey func(Parent) Key,
	childKey func(Child) Key,
) (map[Key][]Child, error) {
	grouped := make(map[Key][]Child, len(parents))
	if len(parents) == 0 {
		return grouped, nil
	}

	keys := make([]any, 0, len(parents))
	seen := make(map[Key]struct{}, len(parents))
	for _, parent := range parents {
		key := parentKey(parent)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
		grouped[key] = nil
	}
	children, err := SelectFrom(childTable).WhereIn(childKeyColumn, keys...).All(ctx, x)
	if err != nil {
		return nil, err
	}
	for _, child := range children {
		key := childKey(child)
		grouped[key] = append(grouped[key], child)
	}
	return grouped, nil
}

// LoadBelongsTo loads parents for a set of children in one query and groups
// the result by the child's foreign-key value. It is used by generated
// many-to-one relations. The key functions must return the same comparable key
// type for both rows. An empty child slice returns an empty map without
// executing a query.
func LoadBelongsTo[Child, Parent any, Key comparable](
	ctx context.Context,
	x Executor,
	parentTable Table[Parent],
	parentKeyColumn query.Column,
	children []Child,
	childKey func(Child) Key,
	parentKey func(Parent) Key,
) (map[Key]Parent, error) {
	loaded := make(map[Key]Parent, len(children))
	if len(children) == 0 {
		return loaded, nil
	}

	keys := make([]any, 0, len(children))
	seen := make(map[Key]struct{}, len(children))
	for _, child := range children {
		key := childKey(child)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	parents, err := SelectFrom(parentTable).WhereIn(parentKeyColumn, keys...).All(ctx, x)
	if err != nil {
		return nil, err
	}
	for _, parent := range parents {
		loaded[parentKey(parent)] = parent
	}
	return loaded, nil
}
