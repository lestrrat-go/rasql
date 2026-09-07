package rasql

import (
	"context"
	"fmt"
	"reflect"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
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
	attach(parent any, value any, many bool, present bool) ([]any, func(), error)
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
	prepare(Executor) (graphPreparedQuery, error)
	prepareCompiled(Executor, compiledQuery) (graphPreparedQuery, error)
	validateCompiled(Executor, compiledQuery) error
	run(context.Context, Executor, func() (int64, error)) ([]graphRow, error)
	mapRow(any) any
	sourceName() string
	with(edge Predicate, key *graphKeySpec, options EdgeOptions, limit int) (graphQueryOps, error)
	withOptions(options EdgeOptions, key *graphKeySpec, limit int) (graphQueryOps, error)
}
type graphRow struct {
	row   any
	graph any
}
type graphPreparedQuery struct {
	run func(context.Context, Executor, func() (int64, error)) ([]graphRow, error)
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
func (q graphQuery[R, G]) prepare(executor Executor) (graphPreparedQuery, error) {
	compiled, err := q.compile(executor)
	if err != nil {
		return graphPreparedQuery{}, err
	}
	return q.prepareCompiled(executor, compiled)
}
func (q graphQuery[R, G]) prepareCompiled(executor Executor, compiled compiledQuery) (graphPreparedQuery, error) {
	prepared, err := prepareRows(executor, q.value, compiled)
	if err != nil {
		return graphPreparedQuery{}, err
	}
	return graphPreparedQuery{run: func(ctx context.Context, executor Executor, count func() (int64, error)) ([]graphRow, error) {
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
	}}, nil
}
func (q graphQuery[R, G]) validateCompiled(executor Executor, compiled compiledQuery) error {
	registry := graphCodecs(executor)
	for index, column := range q.value.Schema().Columns() {
		if _, err := codecFor(registry, column.Codec); err != nil {
			return planError("codec_unavailable", fmt.Sprintf("result.columns[%d].codec", index), column.Codec)
		}
	}
	for index, slot := range compiled.bindSlots {
		if _, err := codecFor(registry, slot.codec); err != nil {
			return planError("codec_unavailable", fmt.Sprintf("binds[%d].codec", index), slot.codec)
		}
	}
	return nil
}
func (q graphQuery[R, G]) mapRow(row any) any {
	value := row.(R)
	mapped := q.mapFn(value)
	return mapped
}
func (q graphQuery[R, G]) sourceName() string {
	if len(q.value.plan.sources) == 0 {
		return ""
	}
	return q.value.plan.sources[0].ref.QualifiedName()
}
func (q graphQuery[R, G]) with(edge Predicate, key *graphKeySpec, options EdgeOptions, limit int) (graphQueryOps, error) {
	child := q.value.Where(edge)
	result, err := (graphQuery[R, G]{value: child, mapFn: q.mapFn}).withOptions(options, key, limit)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (q graphQuery[R, G]) withOptions(options EdgeOptions, key *graphKeySpec, limit int) (graphQueryOps, error) {
	child := q.value
	if options.Where.node != nil {
		child = child.Where(options.Where)
	}
	order := append([]OrderTerm(nil), options.Order...)
	if len(order) == 0 {
		order = append(order, child.plan.order...)
	}
	if limit > 0 {
		final, ok := graphUniqueOrder(child, order)
		if !ok {
			return nil, planError("order_not_unique", "order", "per-parent limit requires a declared unique key")
		}
		order = final
	}
	if len(order) > 0 {
		child = child.OrderBy(order...)
	}
	if limit > 0 {
		if key == nil || len(key.parts) == 0 {
			return nil, planError("invalid_graph_key", "partition", "must not be empty")
		}
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

func graphUniqueOrder[R any](q Query[R], order []OrderTerm) ([]OrderTerm, bool) {
	if len(order) == 0 {
		return nil, false
	}
	if len(q.plan.sources) == 0 {
		return nil, false
	}
	table, ok := q.plan.sources[0].ref.Table()
	if !ok {
		return nil, false
	}
	definition := table.Definition()
	candidates := make([][]string, 0, 1+len(definition.UniqueConstraints)+len(definition.Indexes))
	if len(definition.PrimaryKey) > 0 {
		candidates = append(candidates, definition.PrimaryKey)
	}
	for _, unique := range definition.UniqueConstraints {
		if unique.Deferrable != "" || len(unique.Columns) == 0 {
			continue
		}
		if !unique.NullsNotDistinct && graphAnyNullable(definition.Columns, unique.Columns) {
			continue
		}
		candidates = append(candidates, unique.Columns)
	}
	for _, index := range definition.Indexes {
		if index.Unique && index.Predicate == "" && len(index.Expressions) == 0 && len(index.Columns) > 0 {
			if graphAnyNullable(definition.Columns, index.Columns) {
				continue
			}
			candidates = append(candidates, index.Columns)
		}
	}
	for _, candidate := range candidates {
		if len(order) < len(candidate) {
			continue
		}
		match := true
		for index, name := range candidate {
			column, ok := order[len(order)-len(candidate)+index].node.(query.ColumnRef)
			if !ok || column.Source().QualifiedName() != q.plan.sources[0].ref.QualifiedName() || column.Name() != name {
				match = false
				break
			}
		}
		if match {
			return order, true
		}
	}
	if len(candidates) == 0 {
		return nil, false
	}
	return nil, false
}

func graphAnyNullable(columns []schema.ColumnDef, names []string) bool {
	for _, name := range names {
		for _, column := range columns {
			if column.Name == name && column.Nullable {
				return true
			}
		}
	}
	return false
}
func (q graphQuery[R, G]) run(ctx context.Context, executor Executor, count func() (int64, error)) ([]graphRow, error) {
	prepared, err := q.prepare(executor)
	if err != nil {
		return nil, err
	}
	return prepared.run(ctx, executor, count)
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
		for partIndex := range spec.parentKey.parts {
			parentPart, childPart := spec.parentKey.parts[partIndex], spec.childKey.parts[partIndex]
			if parentPart.typ != childPart.typ || parentPart.codec != childPart.codec || !reflect.DeepEqual(parentPart.columnType, childPart.columnType) {
				return GraphPlan[R, G]{}, planError("graph_key_mismatch", "edges", "key component types differ")
			}
		}
		if spec.parentKey.parts[0].source != node.query.sourceName() || spec.childKey.parts[0].source != spec.child.query.sourceName() {
			return GraphPlan[R, G]{}, planError("graph_key_mismatch", "edges", "key source differs from graph stage")
		}
		if spec.kind == graphManyThrough {
			if spec.junctionParent.parts[0].source != spec.junction.ref.QualifiedName() || spec.junctionChild.parts[0].source != spec.junction.ref.QualifiedName() {
				return GraphPlan[R, G]{}, planError("graph_key_mismatch", "edges", "junction key source differs from junction")
			}
		}
		optionSource := spec.child.query.sourceName()
		if spec.kind == graphManyThrough {
			optionSource = spec.junction.ref.QualifiedName()
		}
		if spec.options.Where.source != "" {
			if optionSource != "" && spec.options.Where.source != optionSource {
				return GraphPlan[R, G]{}, planError("invalid_graph_plan", "edge.where", "predicate source differs from child source")
			}
		}
		if spec.options.Where.source2 != "" && optionSource != "" && spec.options.Where.source2 != optionSource {
			return GraphPlan[R, G]{}, planError("invalid_graph_plan", "edge.where", "predicate source differs from child source")
		}
		for _, term := range spec.options.Order {
			if term.source != "" && optionSource != "" && term.source != optionSource {
				return GraphPlan[R, G]{}, planError("invalid_graph_plan", "edge.order", "order source differs from child source")
			}
		}
		if spec.kind == graphManyThrough && (spec.junction == (Source{}) || spec.junctionParent == nil || spec.junctionChild == nil) {
			return GraphPlan[R, G]{}, planError("invalid_graph_plan", "edges", "through metadata is incomplete")
		}
		node.edges[i] = spec
	}
	return GraphPlan[R, G]{node: node}, nil
}

type graphAttachMany[G, CG any] struct{ fn func(*G, LoadedMany[CG]) }

func (a graphAttachMany[G, CG]) attach(parent any, value any, _ bool, _ bool) ([]any, func(), error) {
	p, ok := parent.(*G)
	if !ok {
		return nil, nil, planError("internal_plan", "attach", "parent type mismatch")
	}
	rawValues, ok := value.([]any)
	if !ok {
		return nil, nil, planError("internal_plan", "attach", "child type mismatch")
	}
	copied := make([]CG, len(rawValues))
	for i, raw := range rawValues {
		child, ok := raw.(CG)
		if !ok {
			return nil, nil, planError("internal_plan", "attach", "child type mismatch")
		}
		copied[i] = child
	}
	result := make([]any, len(copied))
	for i := range copied {
		result[i] = &copied[i]
	}
	return result, func() { a.fn(p, LoadedMany[CG]{Loaded: true, Values: copied}) }, nil
}

type graphAttachOne[G, CG any] struct{ fn func(*G, LoadedOne[CG]) }

func (a graphAttachOne[G, CG]) attach(parent any, value any, _ bool, present bool) ([]any, func(), error) {
	p, ok := parent.(*G)
	if !ok {
		return nil, nil, planError("internal_plan", "attach", "parent type mismatch")
	}
	if !present {
		return nil, func() { a.fn(p, LoadedOne[CG]{Loaded: true}) }, nil
	}
	child, ok := value.(CG)
	if !ok {
		return nil, nil, planError("internal_plan", "attach", "child type mismatch")
	}
	copy := child
	return []any{&copy}, func() { a.fn(p, LoadedOne[CG]{Loaded: true, Present: true, Value: &copy}) }, nil
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
