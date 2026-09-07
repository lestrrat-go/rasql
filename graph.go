package rasql

import (
	"context"
	"fmt"
	"reflect"
)

type LoadedMany[T any] struct {
	Loaded bool
	Values []T
}
type LoadedOne[T any] struct {
	Loaded  bool
	Present bool
	Value   *T
}

type EdgeOptions struct {
	Where          Predicate
	Order          []OrderTerm
	PerParentLimit int
	BindLimit      int
}

type GraphEdge[P, G any] interface{ graphEdge() *graphEdgeSpec }
type GraphPlan[R, G any] struct{ node *graphPlanNode }

type graphEdgeKind uint8

const (
	graphHasMany graphEdgeKind = iota + 1
	graphHasOne
	graphManyThrough
)

type graphAttachOps interface {
	attach(parent any, value any, many bool, present bool) ([]any, error)
}
type graphEdgeSpec struct {
	kind                          graphEdgeKind
	name                          string
	parentKey, childKey           *graphKeySpec
	junctionParent, junctionChild *graphKeySpec
	junction                      Source
	child                         *graphPlanNode
	options                       EdgeOptions
	attach                        graphAttachOps
}
type graphPlanNode struct {
	id                 *graphPlanIdentity
	query              graphQueryOps
	edges              []*graphEdgeSpec
	rowType, graphType reflect.Type
}
type graphPlanIdentity struct{ marker byte }
type graphQueryOps interface {
	validate() error
	compile(Executor) (compiledQuery, error)
	run(context.Context, Executor, func() (int64, error)) ([]graphRow, error)
	mapRow(any) any
	with(edge Predicate, key *graphKeySpec, options EdgeOptions, limit int) (graphQueryOps, error)
}
type graphRow struct {
	row   any
	graph any
}

type graphQuery[R, G any] struct {
	value Query[R]
	mapFn func(R) G
}

func (q graphQuery[R, G]) validate() error { return q.value.Validate() }
func (q graphQuery[R, G]) compile(executor Executor) (compiledQuery, error) {
	provider, ok := executor.(compilerProvider)
	if !ok || provider.queryCompiler() == nil {
		return compiledQuery{}, planError("engine_profile_unavailable", "executor", "compiler unavailable")
	}
	return compileQuery(provider.queryCompiler(), q.value)
}
func (q graphQuery[R, G]) mapRow(row any) any {
	value := row.(R)
	mapped := q.mapFn(value)
	return mapped
}
func (q graphQuery[R, G]) with(edge Predicate, key *graphKeySpec, options EdgeOptions, limit int) (graphQueryOps, error) {
	child := q.value.Where(edge)
	if options.Where.node != nil {
		child = child.Where(options.Where)
	}
	order := append([]OrderTerm(nil), options.Order...)
	if len(order) == 0 {
		order = append(order, child.plan.order...)
	}
	if len(order) == 0 {
		for _, part := range key.parts {
			order = append(order, OrderTerm{node: part.column, source: part.column.Source().QualifiedName()})
		}
	}
	if len(order) > 0 {
		child = child.OrderBy(order...)
	}
	if limit > 0 {
		partition := make([]GroupKey, len(key.parts))
		for i, part := range key.parts {
			partition[i] = GroupKey{node: part.column, source: part.column.Source().QualifiedName()}
		}
		limited, err := child.withPartitionLimit(partition, order, limit)
		if err != nil {
			return nil, err
		}
		child = limited
	}
	return graphQuery[R, G]{value: child, mapFn: q.mapFn}, nil
}
func (q graphQuery[R, G]) run(ctx context.Context, executor Executor, count func() (int64, error)) ([]graphRow, error) {
	compiled, err := q.compile(executor)
	if err != nil {
		return nil, err
	}
	prepared, err := prepareRows(executor, q.value, compiled)
	if err != nil {
		return nil, err
	}
	seq, err := rowsPrepared(ctx, executor, prepared)
	if err != nil {
		return nil, err
	}
	result := make([]graphRow, 0)
	var sequenceErr error
	seq(func(row R, err error) bool {
		if err != nil {
			sequenceErr = err
			return false
		}
		result = append(result, graphRow{row: row, graph: q.mapFn(row)})
		_, sequenceErr = count()
		return sequenceErr == nil
	})
	return result, sequenceErr
}

func NewGraphPlan[R, G any](q Query[R], mapper func(R) G, edges ...GraphEdge[R, G]) (GraphPlan[R, G], error) {
	if err := q.Validate(); err != nil {
		return GraphPlan[R, G]{}, err
	}
	if mapper == nil {
		return GraphPlan[R, G]{}, planError("invalid_graph_plan", "mapper", "must not be nil")
	}
	node := &graphPlanNode{id: &graphPlanIdentity{}, query: graphQuery[R, G]{value: q, mapFn: mapper}, rowType: reflect.TypeOf((*R)(nil)).Elem(), graphType: reflect.TypeOf((*G)(nil)).Elem()}
	seen := make(map[string]struct{}, len(edges))
	node.edges = make([]*graphEdgeSpec, len(edges))
	for i, edge := range edges {
		if edge == nil || (reflect.ValueOf(edge).Kind() == reflect.Pointer && reflect.ValueOf(edge).IsNil()) {
			return GraphPlan[R, G]{}, planError("invalid_graph_plan", fmt.Sprintf("edges[%d]", i), "must not be nil")
		}
		spec := edge.graphEdge()
		if spec == nil {
			return GraphPlan[R, G]{}, planError("invalid_graph_plan", fmt.Sprintf("edges[%d]", i), "must not be zero")
		}
		if spec.name == "" {
			return GraphPlan[R, G]{}, planError("invalid_graph_plan", fmt.Sprintf("edges[%d]", i), "name must not be empty")
		}
		if _, ok := seen[spec.name]; ok {
			return GraphPlan[R, G]{}, planError("invalid_graph_plan", "edges", "duplicate edge name")
		}
		seen[spec.name] = struct{}{}
		if spec.child == nil || spec.parentKey == nil || spec.childKey == nil || spec.attach == nil {
			return GraphPlan[R, G]{}, planError("invalid_graph_plan", "edges", "edge is incomplete")
		}
		if spec.options.PerParentLimit < 0 || spec.options.BindLimit < 0 {
			return GraphPlan[R, G]{}, planError("invalid_graph_plan", "edges", "limits must not be negative")
		}
		if len(spec.parentKey.parts) != len(spec.childKey.parts) {
			return GraphPlan[R, G]{}, planError("invalid_graph_plan", "edges", "key widths differ")
		}
		if spec.kind == graphManyThrough && (spec.junction == (Source{}) || spec.junctionParent == nil || spec.junctionChild == nil) {
			return GraphPlan[R, G]{}, planError("invalid_graph_plan", "edges", "through metadata is incomplete")
		}
		node.edges[i] = spec
	}
	return GraphPlan[R, G]{node: node}, nil
}

type graphAttachMany[G, CG any] struct{ fn func(*G, LoadedMany[CG]) }

func (a graphAttachMany[G, CG]) attach(parent any, value any, _ bool, _ bool) ([]any, error) {
	p, ok := parent.(*G)
	if !ok {
		return nil, planError("internal_plan", "attach", "parent type mismatch")
	}
	rawValues, ok := value.([]any)
	if !ok {
		return nil, planError("internal_plan", "attach", "child type mismatch")
	}
	copied := make([]CG, len(rawValues))
	for i, raw := range rawValues {
		child, ok := raw.(CG)
		if !ok {
			return nil, planError("internal_plan", "attach", "child type mismatch")
		}
		copied[i] = child
	}
	a.fn(p, LoadedMany[CG]{Loaded: true, Values: copied})
	result := make([]any, len(copied))
	for i := range copied {
		result[i] = &copied[i]
	}
	return result, nil
}

type graphAttachOne[G, CG any] struct{ fn func(*G, LoadedOne[CG]) }

func (a graphAttachOne[G, CG]) attach(parent any, value any, _ bool, present bool) ([]any, error) {
	p, ok := parent.(*G)
	if !ok {
		return nil, planError("internal_plan", "attach", "parent type mismatch")
	}
	if !present {
		a.fn(p, LoadedOne[CG]{Loaded: true})
		return nil, nil
	}
	child, ok := value.(CG)
	if !ok {
		return nil, planError("internal_plan", "attach", "child type mismatch")
	}
	copy := child
	a.fn(p, LoadedOne[CG]{Loaded: true, Present: true, Value: &copy})
	return []any{&copy}, nil
}

type graphEdgeValue[P, G, C, CG any] struct{ spec *graphEdgeSpec }

func (e graphEdgeValue[P, G, C, CG]) graphEdge() *graphEdgeSpec { return e.spec }

func HasMany[P, G, C, CG any](name string, parent GraphKey[P], child GraphKey[C], children GraphPlan[C, CG], options EdgeOptions, attach func(*G, LoadedMany[CG])) (GraphEdge[P, G], error) {
	if attach == nil {
		return nil, planError("invalid_graph_edge", "attach", "must not be nil")
	}
	if children.node == nil || parent.key == nil || child.key == nil {
		return nil, planError("invalid_graph_edge", "edge", "must not be zero")
	}
	options.Order = append([]OrderTerm(nil), options.Order...)
	return graphEdgeValue[P, G, C, CG]{spec: &graphEdgeSpec{kind: graphHasMany, name: name, parentKey: parent.key, childKey: child.key, child: children.node, options: options, attach: graphAttachMany[G, CG]{fn: attach}}}, nil
}
func HasOne[P, G, C, CG any](name string, parent GraphKey[P], child GraphKey[C], children GraphPlan[C, CG], options EdgeOptions, attach func(*G, LoadedOne[CG])) (GraphEdge[P, G], error) {
	if attach == nil {
		return nil, planError("invalid_graph_edge", "attach", "must not be nil")
	}
	if children.node == nil || parent.key == nil || child.key == nil {
		return nil, planError("invalid_graph_edge", "edge", "must not be zero")
	}
	if options.PerParentLimit != 0 && options.PerParentLimit != 2 {
		return nil, planError("invalid_graph_edge", "limit", "has-one limit must be zero or two")
	}
	options.Order = append([]OrderTerm(nil), options.Order...)
	return graphEdgeValue[P, G, C, CG]{spec: &graphEdgeSpec{kind: graphHasOne, name: name, parentKey: parent.key, childKey: child.key, child: children.node, options: options, attach: graphAttachOne[G, CG]{fn: attach}}}, nil
}
func ManyThrough[P, G, J, C, CG any](name string, parent GraphKey[P], junctionParent GraphKey[J], junctionChild GraphKey[J], child GraphKey[C], junction Source, children GraphPlan[C, CG], options EdgeOptions, attach func(*G, LoadedMany[CG])) (GraphEdge[P, G], error) {
	if attach == nil || children.node == nil || parent.key == nil || junctionParent.key == nil || junctionChild.key == nil || child.key == nil || junction.ref.QualifiedName() == "" {
		return nil, planError("invalid_graph_edge", "edge", "is incomplete")
	}
	if len(junctionParent.key.parts) != len(parent.key.parts) || len(junctionChild.key.parts) != len(child.key.parts) {
		return nil, planError("invalid_graph_edge", "edge", "key widths differ")
	}
	options.Order = append([]OrderTerm(nil), options.Order...)
	return graphEdgeValue[P, G, C, CG]{spec: &graphEdgeSpec{kind: graphManyThrough, name: name, parentKey: parent.key, childKey: child.key, junctionParent: junctionParent.key, junctionChild: junctionChild.key, junction: junction, child: children.node, options: options, attach: graphAttachMany[G, CG]{fn: attach}}}, nil
}
