package rasql

import (
	"context"
	"fmt"

	"github.com/lestrrat-go/rasql/query"
)

type graphInvocation struct {
	codecs CodecRegistry
	rows   int64
}

func graphCodecs(executor Executor) CodecRegistry {
	if provider, ok := executor.(CodecProvider); ok && provider.Codecs() != nil {
		return provider.Codecs()
	}
	return builtinCodecs
}

func graphValidate(node *graphPlanNode, executor Executor) error {
	if node == nil {
		return planError("invalid_graph_plan", "graph", "must not be zero")
	}
	active := make(map[*graphPlanIdentity]struct{})
	done := make(map[*graphPlanIdentity]struct{})
	var visit func(*graphPlanNode, string) error
	visit = func(current *graphPlanNode, path string) error {
		if current == nil {
			return planError("invalid_graph_plan", path, "node is zero")
		}
		if _, ok := active[current.id]; ok {
			return planError("graph_cycle", path, "graph plan identity is active")
		}
		if _, ok := done[current.id]; ok {
			return nil
		}
		if err := current.query.validate(); err != nil {
			return err
		}
		if _, err := current.query.compile(executor); err != nil {
			return err
		}
		active[current.id] = struct{}{}
		for i, edge := range current.edges {
			if edge == nil || edge.child == nil {
				return planError("invalid_graph_plan", fmt.Sprintf("%s.edges[%d]", path, i), "edge is incomplete")
			}
			if edge.options.PerParentLimit > 0 {
				profile := executorCompilerProfile(executor)
				if profile.Capabilities.PerParentLimit != EnginePerParentLimitWindow || !profile.Capabilities.WindowFunctions {
					return planError("per_parent_limit_unsupported", path, "engine does not support SQL partition limits")
				}
			}
			profile := executorCompilerProfile(executor)
			if len(edge.childKey.parts) == 0 {
				return planError("invalid_graph_plan", path, "child key is empty")
			}
			dummy := graphDummyTuple(edge.childKey)
			membership, err := buildGraphMembership(edge.childKey, []keyTuple{dummy})
			if err != nil {
				return err
			}
			probeLimit := edge.options.PerParentLimit
			if edge.kind == graphHasOne {
				probeLimit = 2
			}
			probe, err := edge.child.query.with(membership, edge.childKey, edge.options, probeLimit)
			if err != nil {
				return err
			}
			compiled, err := probe.compile(executor)
			if err != nil {
				return err
			}
			fixed := len(compiled.bindSlots) - len(edge.childKey.parts)
			budget := edge.options.BindLimit
			if budget == 0 || budget > profile.MaxBind {
				budget = profile.MaxBind
			}
			if budget <= 0 || budget-fixed < len(edge.childKey.parts) {
				return planError("bind_limit", path+"."+edge.name, "bind budget cannot fit one key")
			}
			if err := visit(edge.child, path+"."+edge.name); err != nil {
				return err
			}
		}
		delete(active, current.id)
		done[current.id] = struct{}{}
		return nil
	}
	return visit(node, "graph")
}

func graphDummyTuple(key *graphKeySpec) keyTuple {
	result := keyTuple{components: make([]keyComponent, len(key.parts))}
	for i, part := range key.parts {
		result.components[i] = keyComponent{value: int64(0), codec: part.codec}
	}
	return result
}

func executorCompilerProfile(executor Executor) engineProfileSnapshot {
	provider, ok := executor.(compilerProvider)
	if !ok || provider.queryCompiler() == nil {
		return engineProfileSnapshot{}
	}
	p := provider.queryCompiler().EngineProfile()
	return engineProfileSnapshot{Capabilities: p.Capabilities, MaxBind: p.Limits.MaxBindParameters}
}

type engineProfileSnapshot struct {
	Capabilities EngineCapabilities
	MaxBind      int
}

func LoadGraph[R, G any](ctx context.Context, executor Executor, plan GraphPlan[R, G]) ([]G, error) {
	if executor == nil || plan.node == nil {
		return nil, planError("invalid_graph_plan", "graph", "executor and plan are required")
	}
	if err := graphValidate(plan.node, executor); err != nil {
		return nil, err
	}
	provider, ok := executor.(compilerProvider)
	if !ok || provider.queryCompiler() == nil {
		return nil, planError("engine_profile_unavailable", "executor", "compiler unavailable")
	}
	callCtx, observed, completion := beginLogicalInvocation(ctx, executor, EventGraph)
	var finalErr error
	var early bool
	observedRows := int64(0)
	defer func() { completion.completeLogicalInvocation(finalErr, observedRows, early) }()
	rootRows, err := plan.node.query.run(callCtx, observed, func() (int64, error) { observedRows++; return observedRows, nil })
	if err != nil {
		finalErr = err
		return nil, err
	}
	graphs := make([]G, len(rootRows))
	queue := make([]graphWork, len(rootRows))
	for i, row := range rootRows {
		graphs[i] = row.graph.(G)
		queue[i] = graphWork{node: plan.node, parent: &graphs[i], row: row.row}
	}
	for len(queue) > 0 {
		current := queue
		queue = nil
		for _, edge := range planEdges(current) {
			next, err := executeGraphEdge(callCtx, observed, edge.edge, edge.parents, &observedRows)
			if err != nil {
				finalErr = err
				return nil, err
			}
			queue = append(queue, next...)
		}
	}
	return graphs, nil
}

type graphWork struct {
	node   *graphPlanNode
	parent any
	row    any
}
type graphEdgeWork struct {
	edge    *graphEdgeSpec
	parents []graphWork
}

type graphJunctionRow struct{ values []any }
type graphJunctionDecoder struct {
	schema ResultSchema
	width  int
}

func (d graphJunctionDecoder) ResultSchema() ResultSchema { return d.schema }
func (d graphJunctionDecoder) Presence() []Presence       { return nil }
func (d graphJunctionDecoder) DecodeRow(source ScanSource, result *graphJunctionRow) error {
	values := make([]any, d.width)
	destinations := make([]any, d.width)
	for i := range values {
		destinations[i] = &values[i]
	}
	if err := source.Scan(destinations...); err != nil {
		return err
	}
	result.values = values
	return nil
}

func graphJunctionQuery(source Source, parent, child *graphKeySpec) (Query[graphJunctionRow], error) {
	parts := append(append([]*graphKeyPartSpec(nil), parent.parts...), child.parts...)
	items := make([]ProjectionItem, len(parts))
	columns := make([]ResultColumn, len(parts))
	for i, part := range parts {
		found := false
		for _, column := range part.column.Source().Columns() {
			if column.Name == part.column.Name() {
				columns[i] = ResultColumn{Name: fmt.Sprintf("graph_key_%d", i), Type: column.Type, Nullable: column.Nullable, Codec: part.codec}
				found = true
				break
			}
		}
		if !found {
			return Query[graphJunctionRow]{}, planError("invalid_graph_edge", "junction", "key column is not in source")
		}
		items[i] = ProjectionItem{expression: part.column, source: part.column.Source().QualifiedName(), column: columns[i]}
	}
	schemaValue, err := NewResultSchema(columns...)
	if err != nil {
		return Query[graphJunctionRow]{}, err
	}
	projection, err := NewProjection(items, graphJunctionDecoder{schema: schemaValue, width: len(parts)})
	if err != nil {
		return Query[graphJunctionRow]{}, err
	}
	return Select(source, projection), nil
}

func graphTupleValues(parts []*graphKeyPartSpec, values []any, codecs CodecRegistry) (keyTuple, bool, error) {
	if len(parts) != len(values) {
		return keyTuple{}, false, planError("internal_plan", "graph.key", "tuple width mismatch")
	}
	result := keyTuple{components: make([]keyComponent, len(parts))}
	for i, part := range parts {
		normalized, err := normalizeGraphValue(values[i])
		if err != nil {
			return keyTuple{}, false, err
		}
		if normalized == nil {
			return keyTuple{}, false, nil
		}
		if part.codec != "" {
			codec, ok := codecs.Lookup(CodecID(part.codec))
			if !ok {
				return keyTuple{}, false, planError("codec_unavailable", "graph.key", part.codec)
			}
			normalized, err = codec.Encode(values[i])
			if err != nil {
				return keyTuple{}, false, err
			}
		}
		frame, err := frameGraphValue(normalized)
		if err != nil {
			return keyTuple{}, false, err
		}
		result.components[i] = keyComponent{value: normalized, encoded: frame, codec: part.codec}
		result.identity += string(frame)
	}
	return result, true, nil
}

func planEdges(items []graphWork) []graphEdgeWork {
	result := make([]graphEdgeWork, 0)
	positions := make(map[*graphEdgeSpec]int)
	for _, item := range items {
		for _, edge := range item.node.edges {
			index, ok := positions[edge]
			if !ok {
				index = len(result)
				positions[edge] = index
				result = append(result, graphEdgeWork{edge: edge})
			}
			result[index].parents = append(result[index].parents, item)
		}
	}
	return result
}

func buildGraphMembership(key *graphKeySpec, tuples []keyTuple) (Predicate, error) {
	branches := make([]query.Expression, 0, len(tuples))
	for _, tuple := range tuples {
		parts := make([]query.Expression, len(key.parts))
		for i, part := range key.parts {
			expression, err := graphEncodedBind(tuple.components[i].value, part.codec)
			if err != nil {
				return Predicate{}, err
			}
			parts[i] = query.Equal(part.column, expression)
		}
		if len(parts) == 1 {
			branches = append(branches, parts[0])
		} else {
			branches = append(branches, query.And(parts...))
		}
	}
	if len(branches) == 0 {
		return Predicate{}, planError("invalid_graph_key", "membership", "must not be empty")
	}
	if len(branches) == 1 {
		return Predicate{node: branches[0]}, nil
	}
	return Predicate{node: query.Or(branches...)}, nil
}

func executeGraphEdge(ctx context.Context, executor Executor, edge *graphEdgeSpec, parents []graphWork, rowCount *int64) ([]graphWork, error) {
	if edge.kind == graphManyThrough {
		return executeManyThrough(ctx, executor, edge, parents, rowCount)
	}
	codecs := graphCodecs(executor)
	tuples := make([]keyTuple, 0, len(parents))
	parentKeys := make([]string, len(parents))
	parentPresent := make([]bool, len(parents))
	groups := make(map[string][]int)
	for i, parent := range parents {
		tuple, present, err := edge.parentKey.tuple(parent.row, codecs)
		if err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		parentPresent[i] = true
		parentKeys[i] = tuple.identity
		if _, ok := groups[tuple.identity]; !ok {
			tuples = append(tuples, tuple)
		}
		groups[tuple.identity] = append(groups[tuple.identity], i)
	}
	loaded := make(map[string][]graphRow, len(tuples))
	for _, tuple := range tuples {
		loaded[tuple.identity] = nil
	}
	if len(tuples) == 0 {
		for i := range parents {
			if edge.kind == graphHasMany {
				if _, err := edge.attach.attach(parents[i].parent, []any{}, true, false); err != nil {
					return nil, err
				}
			} else if _, err := edge.attach.attach(parents[i].parent, nil, false, false); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	profile := executorCompilerProfile(executor)
	fixed := 0
	if compiled, err := edge.child.query.compile(executor); err == nil {
		fixed = len(compiled.bindSlots)
	} else {
		return nil, err
	}
	budget := edge.options.BindLimit
	if budget == 0 || budget > profile.MaxBind {
		budget = profile.MaxBind
	}
	width := len(edge.childKey.parts)
	if budget <= fixed || (budget-fixed)/width == 0 {
		return nil, planError("bind_limit", "graph."+edge.name, "bind budget cannot fit one key")
	}
	batchSize := (budget - fixed) / width
	for start := 0; start < len(tuples); start += batchSize {
		end := start + batchSize
		if end > len(tuples) {
			end = len(tuples)
		}
		membership, err := buildGraphMembership(edge.childKey, tuples[start:end])
		if err != nil {
			return nil, err
		}
		limit := edge.options.PerParentLimit
		if edge.kind == graphHasOne {
			limit = 2
		}
		childQuery, err := edge.child.query.with(membership, edge.childKey, edge.options, limit)
		if err != nil {
			return nil, err
		}
		rows, err := childQuery.run(ctx, executor, func() (int64, error) { *rowCount++; return *rowCount, nil })
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			tuple, present, err := edge.childKey.tuple(row.row, codecs)
			if err != nil {
				return nil, err
			}
			if !present {
				return nil, planError("foreign_key_result", "graph."+edge.name, "child key is absent")
			}
			if _, ok := groups[tuple.identity]; !ok {
				return nil, planError("foreign_key_result", "graph."+edge.name, "child key was not requested")
			}
			loaded[tuple.identity] = append(loaded[tuple.identity], row)
		}
	}
	next := make([]graphWork, 0)
	for i, parent := range parents {
		children := loaded[parentKeys[i]]
		if !parentPresent[i] {
			children = nil
		}
		values := make([]any, len(children))
		for j := range children {
			values[j] = children[j].graph
		}
		if edge.kind == graphHasOne {
			if len(values) > 1 {
				return nil, planError("cardinality", "graph."+edge.name, "has-one returned multiple rows")
			}
			if len(values) == 0 {
				if _, err := edge.attach.attach(parent.parent, nil, false, false); err != nil {
					return nil, err
				}
			} else {
				attached, err := edge.attach.attach(parent.parent, values[0], false, true)
				if err != nil {
					return nil, err
				}
				next = append(next, graphWork{node: edge.child, parent: attached[0], row: children[0].row})
			}
		} else {
			if values == nil {
				values = []any{}
			}
			attached, err := edge.attach.attach(parent.parent, values, true, true)
			if err != nil {
				return nil, err
			}
			for j, value := range children {
				next = append(next, graphWork{node: edge.child, parent: attached[j], row: value.row})
			}
		}
	}
	return next, nil
}

func executeManyThrough(ctx context.Context, executor Executor, edge *graphEdgeSpec, parents []graphWork, rowCount *int64) ([]graphWork, error) {
	codecs := graphCodecs(executor)
	parentTuples := make([]keyTuple, 0, len(parents))
	parentIndex := make(map[string][]int)
	for i, parent := range parents {
		tuple, present, err := edge.parentKey.tuple(parent.row, codecs)
		if err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		if _, ok := parentIndex[tuple.identity]; !ok {
			parentTuples = append(parentTuples, tuple)
		}
		parentIndex[tuple.identity] = append(parentIndex[tuple.identity], i)
	}
	if len(parentTuples) == 0 {
		for _, parent := range parents {
			if _, err := edge.attach.attach(parent.parent, []any{}, true, false); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	junctionQ, err := graphJunctionQuery(edge.junction, edge.junctionParent, edge.junctionChild)
	if err != nil {
		return nil, err
	}
	junctionPlan := graphQuery[graphJunctionRow, graphJunctionRow]{value: junctionQ, mapFn: func(row graphJunctionRow) graphJunctionRow { return row }}
	profile := executorCompilerProfile(executor)
	fixed := 0
	compiled, err := junctionPlan.compile(executor)
	if err != nil {
		return nil, err
	}
	fixed = len(compiled.bindSlots)
	budget := edge.options.BindLimit
	if budget == 0 {
		budget = profile.MaxBind
	}
	width := len(edge.junctionParent.parts)
	batchSize := (budget - fixed) / width
	if batchSize <= 0 {
		return nil, planError("bind_limit", "graph."+edge.name, "bind budget cannot fit one key")
	}
	byParent := make(map[string][]keyTuple)
	targetOrder := make([]keyTuple, 0)
	targetSeen := make(map[string]struct{})
	for start := 0; start < len(parentTuples); start += batchSize {
		end := start + batchSize
		if end > len(parentTuples) {
			end = len(parentTuples)
		}
		membership, err := buildGraphMembership(edge.junctionParent, parentTuples[start:end])
		if err != nil {
			return nil, err
		}
		limit := edge.options.PerParentLimit
		if limit == 0 {
			limit = 0
		}
		limited, err := junctionPlan.with(membership, edge.junctionParent, edge.options, limit)
		if err != nil {
			return nil, err
		}
		rows, err := limited.run(ctx, executor, func() (int64, error) { *rowCount++; return *rowCount, nil })
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			junctionRow, ok := row.row.(graphJunctionRow)
			if !ok {
				return nil, planError("internal_plan", "graph."+edge.name, "junction row type mismatch")
			}
			parentTuple, present, err := graphTupleValues(edge.junctionParent.parts, junctionRow.values[:len(edge.junctionParent.parts)], codecs)
			if err != nil || !present {
				return nil, err
			}
			targetTuple, present, err := graphTupleValues(edge.junctionChild.parts, junctionRow.values[len(edge.junctionParent.parts):], codecs)
			if err != nil || !present {
				return nil, err
			}
			exists := false
			for _, prior := range byParent[parentTuple.identity] {
				if prior.identity == targetTuple.identity {
					exists = true
					break
				}
			}
			if exists {
				continue
			}
			byParent[parentTuple.identity] = append(byParent[parentTuple.identity], targetTuple)
			if _, ok := targetSeen[targetTuple.identity]; !ok {
				targetSeen[targetTuple.identity] = struct{}{}
				targetOrder = append(targetOrder, targetTuple)
			}
		}
	}
	targets := make(map[string]graphRow)
	if len(targetOrder) > 0 {
		fixed = 0
		compiled, err = edge.child.query.compile(executor)
		if err != nil {
			return nil, err
		}
		fixed = len(compiled.bindSlots)
		targetWidth := len(edge.childKey.parts)
		batchSize = (budget - fixed) / targetWidth
		if batchSize <= 0 {
			return nil, planError("bind_limit", "graph."+edge.name, "bind budget cannot fit target key")
		}
		for start := 0; start < len(targetOrder); start += batchSize {
			end := start + batchSize
			if end > len(targetOrder) {
				end = len(targetOrder)
			}
			membership, err := buildGraphMembership(edge.childKey, targetOrder[start:end])
			if err != nil {
				return nil, err
			}
			childQuery, err := edge.child.query.with(membership, edge.childKey, edge.options, 0)
			if err != nil {
				return nil, err
			}
			rows, err := childQuery.run(ctx, executor, func() (int64, error) { *rowCount++; return *rowCount, nil })
			if err != nil {
				return nil, err
			}
			for _, row := range rows {
				tuple, present, err := edge.childKey.tuple(row.row, codecs)
				if err != nil || !present {
					return nil, err
				}
				if _, ok := targetSeen[tuple.identity]; !ok {
					return nil, planError("foreign_key_result", "graph."+edge.name, "target key was not requested")
				}
				if _, duplicate := targets[tuple.identity]; duplicate {
					return nil, planError("cardinality", "graph."+edge.name, "target returned duplicate rows")
				}
				targets[tuple.identity] = row
			}
		}
	}
	next := make([]graphWork, 0)
	for i, parent := range parents {
		parentTuple, present, err := edge.parentKey.tuple(parent.row, codecs)
		if err != nil {
			return nil, err
		}
		if !present {
			if _, err := edge.attach.attach(parent.parent, []any{}, true, false); err != nil {
				return nil, err
			}
			continue
		}
		junctionTargets := byParent[parentTuple.identity]
		values := make([]any, 0, len(junctionTargets))
		for _, target := range junctionTargets {
			row, ok := targets[target.identity]
			if !ok {
				return nil, planError("cardinality", "graph."+edge.name, "junction target is missing")
			}
			values = append(values, row.graph)
			// The attachment below copies values into the parent's relation slice.
		}
		attached, err := edge.attach.attach(parent.parent, values, true, true)
		if err != nil {
			return nil, err
		}
		for j, target := range junctionTargets {
			row := targets[target.identity]
			next = append(next, graphWork{node: edge.child, parent: attached[j], row: row.row})
		}
		_ = i
	}
	return next, nil
}
