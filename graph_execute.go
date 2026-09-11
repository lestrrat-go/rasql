package rasql

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"slices"

	"github.com/lestrrat-go/rasql/query"
)

func graphCodecs(executor Executor) CodecRegistry {
	if provider, ok := executor.(CodecProvider); ok && provider.Codecs() != nil {
		return provider.Codecs()
	}
	return builtinCodecs
}

func prepareGraphPlan[R, G any](executor Executor, plan GraphPlan[R, G]) (compiledQuery, error) {
	var rootCompiled compiledQuery
	if isNilExecutor(executor) || plan.node == nil {
		return rootCompiled, planError("invalid_graph_plan", "graph", "executor and plan are required")
	}
	if err := graphValidate(plan.node, executor, &rootCompiled); err != nil {
		return compiledQuery{}, err
	}
	return rootCompiled, nil
}

func graphValidate(node *graphPlanNode, executor Executor, rootCompiled *compiledQuery) error {
	if node == nil {
		return planError("invalid_graph_plan", "graph", "must not be zero")
	}
	active := make(map[*graphPlanIdentity]struct{})
	done := make(map[*graphPlanIdentity]struct{})
	type frame struct {
		node *graphPlanNode
		path string
		next int
	}
	stack := []frame{{node: node, path: "graph"}}
	for len(stack) > 0 {
		last := len(stack) - 1
		current := &stack[last]
		if current.node == nil {
			return planError("invalid_graph_plan", current.path, "node is zero")
		}
		if current.next == 0 {
			if _, ok := active[current.node.id]; ok {
				return planError("graph_cycle", current.path, "graph plan identity is active")
			}
			if _, ok := done[current.node.id]; ok {
				stack = stack[:last]
				continue
			}
			if err := current.node.query.validate(); err != nil {
				return err
			}
			compiled, err := current.node.query.compile(executor)
			if err != nil {
				return err
			}
			if current.node == node {
				*rootCompiled = compiled
			}
			if err := current.node.query.validateCompiled(executor, compiled); err != nil {
				return err
			}
			if err := validateGraphKeys(current.node, executor); err != nil {
				return err
			}
			active[current.node.id] = struct{}{}
		}
		if current.next >= len(current.node.edges) {
			delete(active, current.node.id)
			done[current.node.id] = struct{}{}
			stack = stack[:last]
			continue
		}
		index := current.next
		current.next++
		edge := current.node.edges[index]
		path := fmt.Sprintf("%s.edges[%d]", current.path, index)
		if edge == nil || edge.child == nil {
			return planError("invalid_graph_plan", path, "edge is incomplete")
		}
		if _, ok := active[edge.child.id]; ok {
			return planError("graph_cycle", path, "graph plan identity is active")
		}
		if err := validateGraphEdge(edge, executor, path); err != nil {
			return err
		}
		if _, ok := done[edge.child.id]; ok {
			continue
		}
		stack = append(stack, frame{node: edge.child, path: current.path + "." + edge.name})
	}
	return nil
}

func validateGraphEdge(edge *graphEdgeSpec, executor Executor, path string) error {
	if edge == nil || edge.child == nil {
		return planError("invalid_graph_plan", path, "edge is incomplete")
	}
	if edge.options.PerParentLimit > 0 {
		profile := executorCompilerProfile(executor)
		if profile.Capabilities.PerParentLimit != EnginePerParentLimitWindow || !profile.Capabilities.WindowFunctions {
			return planError("per_parent_limit_unsupported", path, "engine does not support SQL partition limits")
		}
	}
	if edge.childKey == nil || len(edge.childKey.Parts) == 0 {
		return planError("invalid_graph_plan", path, "child key is empty")
	}
	if edge.kind == graphManyThrough {
		return validateManyThroughEdge(edge, executor, path)
	}
	limit := edge.options.PerParentLimit
	if edge.kind == graphHasOne {
		limit = 2
	}
	probe, err := edge.child.query.withOptions(edge.options, edge.childKey, limit)
	if err != nil {
		return err
	}
	compiled, err := probe.compile(executor)
	if err != nil {
		return err
	}
	if err := edge.child.query.validateCompiled(executor, compiled); err != nil {
		return err
	}
	profile := executorCompilerProfile(executor)
	budget := edge.options.BindLimit
	if budget == 0 || budget > profile.MaxBind {
		budget = profile.MaxBind
	}
	if budget <= 0 || budget-len(compiled.Slots) < len(edge.childKey.Parts) {
		return planError("bind_limit", path, "bind budget cannot fit one key")
	}
	return nil
}

func validateManyThroughEdge(edge *graphEdgeSpec, executor Executor, path string) error {
	profile := executorCompilerProfile(executor)
	budget := edge.options.BindLimit
	if budget == 0 || budget > profile.MaxBind {
		budget = profile.MaxBind
	}
	junctionQ, err := graphJunctionQuery(edge.junction, edge.junctionParent, edge.junctionChild)
	if err != nil {
		return err
	}
	junction := graphQueryOps(graphQuery[graphJunctionRow, graphJunctionRow]{value: junctionQ, mapFn: func(row graphJunctionRow) graphJunctionRow { return row }})
	junction, err = junction.withOptions(edge.options, edge.junctionParent, edge.options.PerParentLimit)
	if err != nil {
		return err
	}
	junctionCompiled, err := junction.compile(executor)
	if err != nil {
		return err
	}
	if err := junction.validateCompiled(executor, junctionCompiled); err != nil {
		return err
	}
	if budget-len(junctionCompiled.Slots) < len(edge.junctionParent.Parts) {
		return planError("bind_limit", path, "bind budget cannot fit one junction key")
	}
	target, err := edge.child.query.withOptions(EdgeOptions{}, edge.childKey, 0)
	if err != nil {
		return err
	}
	targetCompiled, err := target.compile(executor)
	if err != nil {
		return err
	}
	if err := target.validateCompiled(executor, targetCompiled); err != nil {
		return err
	}
	if budget-len(targetCompiled.Slots) < len(edge.childKey.Parts) {
		return planError("bind_limit", path, "bind budget cannot fit one target key")
	}
	return nil
}

func validateGraphKeys(node *graphPlanNode, executor Executor) error {
	codecs := graphCodecs(executor)
	for _, edge := range node.edges {
		if edge == nil {
			continue
		}
		for _, key := range []*graphKeySpec{edge.parentKey, edge.childKey, edge.junctionParent, edge.junctionChild} {
			if key == nil {
				continue
			}
			for _, part := range key.Parts {
				if part.Codec != "" {
					if _, ok := codecs.Lookup(CodecID(part.Codec)); !ok {
						return planError("codec_unavailable", "graph.key", part.Codec)
					}
				}
			}
		}
	}
	return nil
}

func LoadGraph[R, G any](ctx context.Context, executor Executor, plan GraphPlan[R, G]) ([]G, error) {
	rootCompiled, err := prepareGraphPlan(executor, plan)
	if err != nil {
		return nil, err
	}
	rootPrepared, err := plan.node.query.prepareCompiled(executor, rootCompiled)
	if err != nil {
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
	defer func() {
		if value := recover(); value != nil {
			finalErr = fmt.Errorf("graph load panicked: %v", value)
			completion.completeLogicalInvocation(finalErr, observedRows, early)
			panic(value)
		}
		completion.completeLogicalInvocation(finalErr, observedRows, early)
	}()
	rootRows, err := rootPrepared.run(callCtx, observed, func() (int64, error) { observedRows++; return observedRows, nil })
	if err != nil {
		finalErr = err
		return nil, err
	}
	graphs, err := expandGraphRoots[R, G](callCtx, observed, plan.node, rootRows, &observedRows)
	if err != nil {
		finalErr = err
		return nil, err
	}
	return graphs, nil
}

func expandGraphRoots[R, G any](ctx context.Context, executor Executor, node *graphPlanNode, rootRows []graphRow, rowCount *int64) ([]G, error) {
	graphs := make([]G, len(rootRows))
	queue := make([]graphWork, len(rootRows))
	deferred := make([]graphDeferred, 0)
	cache := make(map[graphCacheKey]graphCacheEntry)
	for i, row := range rootRows {
		graphs[i] = row.graph.(G)
		queue[i] = graphWork{node: node, parent: &graphs[i], row: row.row}
	}
	for len(queue) > 0 {
		current := queue
		queue = nil
		for _, edge := range planEdges(current) {
			next, err := executeGraphEdge(ctx, executor, edge.edge, edge.parents, rowCount, &deferred, cache)
			if err != nil {
				return nil, err
			}
			queue = append(queue, next...)
		}
	}
	slices.SortStableFunc(deferred, func(a, b graphDeferred) int {
		if a.depth > b.depth {
			return -1
		}
		if a.depth < b.depth {
			return 1
		}
		return 0
	})
	for _, callback := range deferred {
		callback.fn()
	}
	return graphs, nil
}

type graphWork struct {
	node   *graphPlanNode
	parent any
	row    any
	depth  int
}
type graphEdgeWork struct {
	edge    *graphEdgeSpec
	parents []graphWork
}
type graphDeferred struct {
	depth int
	fn    func()
}
type graphCacheEntry struct {
	rows    []graphRow
	decoder any
}

type graphCacheKey struct {
	fingerprint graphCacheFingerprint
	tuple       string
}

func graphCacheKeyFor(fingerprint graphCacheFingerprint, tuple keyTuple) graphCacheKey {
	return graphCacheKey{fingerprint: fingerprint, tuple: tuple.Identity}
}

type graphJunctionRow struct {
	values      []any
	parentTuple keyTuple
	targetTuple keyTuple
}
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
	parts := append(append([]*graphKeyPartSpec(nil), parent.Parts...), child.Parts...)
	items := make([]ProjectionItem, len(parts))
	columns := make([]ResultColumn, len(parts))
	for i, part := range parts {
		found := false
		for _, column := range part.Column.Source().Columns() {
			if column.Name == part.Column.Name() {
				columns[i] = ResultColumn{Name: fmt.Sprintf("graph_key_%d", i), Type: column.Type, Nullable: column.Nullable, Codec: part.Codec}
				found = true
				break
			}
		}
		if !found {
			return Query[graphJunctionRow]{}, planError("invalid_graph_edge", "junction", "key column is not in source")
		}
		items[i] = ProjectionItem{expression: part.Column, source: part.Column.Source().QualifiedName(), column: columns[i]}
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
	result := keyTuple{Components: make([]keyComponent, len(parts))}
	var identity bytes.Buffer
	identity.WriteByte(byte(len(parts)))
	for i, part := range parts {
		normalized, err := normalizeGraphValue(values[i])
		if err != nil {
			return keyTuple{}, false, err
		}
		if normalized == nil {
			return keyTuple{}, false, nil
		}
		if part.Codec != "" {
			codec, ok := codecs.Lookup(CodecID(part.Codec))
			if !ok {
				return keyTuple{}, false, planError("codec_unavailable", "graph.key", part.Codec)
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
		result.Components[i] = keyComponent{Value: normalized, Encoded: frame, Codec: part.Codec}
		identity.Write(frame)
	}
	result.Identity = identity.String()
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
		parts := make([]query.Expression, len(key.Parts))
		for i, part := range key.Parts {
			expression, err := graphEncodedBind(tuple.Components[i].Value, part.Codec)
			if err != nil {
				return Predicate{}, err
			}
			parts[i] = query.Equal(part.Column, expression)
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
	for len(branches) > 1 {
		next := make([]query.Expression, 0, (len(branches)+1)/2)
		for index := 0; index < len(branches); index += 2 {
			if index+1 == len(branches) {
				next = append(next, branches[index])
				continue
			}
			next = append(next, query.Or(branches[index], branches[index+1]))
		}
		branches = next
	}
	return Predicate{node: branches[0]}, nil
}

func executeGraphEdge(ctx context.Context, executor Executor, edge *graphEdgeSpec, parents []graphWork, rowCount *int64, deferred *[]graphDeferred, cache map[graphCacheKey]graphCacheEntry) ([]graphWork, error) {
	if edge.kind == graphManyThrough {
		return executeManyThrough(ctx, executor, edge, parents, rowCount, deferred, cache)
	}
	codecs := graphCodecs(executor)
	tuples := make([]keyTuple, 0, len(parents))
	parentKeys := make([]string, len(parents))
	parentPresent := make([]bool, len(parents))
	groups := make(map[string][]int)
	for i, parent := range parents {
		tuple, present, err := edge.parentKey.Tuple(parent.row, graphKeyEncoder{codecs: codecs})
		if err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		parentPresent[i] = true
		parentKeys[i] = tuple.Identity
		if _, ok := groups[tuple.Identity]; !ok {
			tuples = append(tuples, tuple)
		}
		groups[tuple.Identity] = append(groups[tuple.Identity], i)
	}
	loaded := make(map[string][]graphRow, len(tuples))
	for _, tuple := range tuples {
		loaded[tuple.Identity] = nil
	}
	if len(tuples) == 0 {
		for i := range parents {
			if edge.kind == graphHasMany {
				_, callback, err := edge.attach.attach(parents[i].parent, []any{}, true, false)
				if err != nil {
					return nil, err
				}
				*deferred = append(*deferred, graphDeferred{depth: parents[i].depth, fn: callback})
			} else {
				_, callback, err := edge.attach.attach(parents[i].parent, nil, false, false)
				if err != nil {
					return nil, err
				}
				*deferred = append(*deferred, graphDeferred{depth: parents[i].depth, fn: callback})
			}
		}
		return nil, nil
	}
	profile := executorCompilerProfile(executor)
	probeLimit := edge.options.PerParentLimit
	if edge.kind == graphHasOne {
		probeLimit = 2
	}
	probe, err := edge.child.query.withOptions(edge.options, edge.childKey, probeLimit)
	if err != nil {
		return nil, err
	}
	fixed := 0
	probeCompiled, err := probe.compile(executor)
	if err == nil {
		fixed = len(probeCompiled.Slots)
	} else {
		return nil, err
	}
	basePrepared := graphPreparedQuery{}
	cacheable := graphStageCacheable(probeCompiled)
	if cacheable {
		basePrepared, err = probe.prepareCompiledRaw(executor, probeCompiled)
		if err != nil {
			return nil, err
		}
	}
	edgeCache := cache
	if basePrepared.run == nil {
		edgeCache = make(map[graphCacheKey]graphCacheEntry)
	}
	budget := edge.options.BindLimit
	if budget == 0 || budget > profile.MaxBind {
		budget = profile.MaxBind
	}
	width := len(edge.childKey.Parts)
	if budget <= fixed || (budget-fixed)/width == 0 {
		return nil, planError("bind_limit", "graph."+edge.name, "bind budget cannot fit one key")
	}
	batchSize := (budget - fixed) / width
	fingerprintCompiled := probeCompiled
	fingerprintCompiled.Statement = basePrepared.statement
	childFingerprint := graphCacheFingerprint{Stage: "child"}
	if basePrepared.run != nil {
		childFingerprint, err = graphInvocationFingerprint(graphFingerprintStage{
			Name: "child", Source: probe.sourceName(), Columns: probe.schemaValue().Columns(), Keys: []*graphKeySpec{edge.childKey},
			Compiled: fingerprintCompiled, PerParentLimit: probeLimit, BindLimit: budget,
		}, profile)
		if err != nil {
			return nil, err
		}
	}
	for start := 0; start < len(tuples); start += batchSize {
		end := start + batchSize
		if end > len(tuples) {
			end = len(tuples)
		}
		missing := make([]keyTuple, 0, end-start)
		for _, tuple := range tuples[start:end] {
			entry, ok := edgeCache[graphCacheKeyFor(childFingerprint, tuple)]
			if ok && !reflect.DeepEqual(entry.decoder, edge.child.query.decoderValue()) {
				ok = false
			}
			if !ok {
				missing = append(missing, tuple)
				continue
			}
			for _, row := range entry.rows {
				loaded[tuple.Identity] = append(loaded[tuple.Identity], graphRow{row: row.row})
			}
		}
		if len(missing) == 0 {
			continue
		}
		membership, err := buildGraphMembership(edge.childKey, missing)
		if err != nil {
			return nil, err
		}
		childQuery := probe.withMembership(membership)
		compiled, err := childQuery.compile(executor)
		if err != nil {
			return nil, err
		} else if len(compiled.Slots) > budget || (profile.MaxBind > 0 && len(compiled.Slots) > profile.MaxBind) {
			return nil, planError("bind_limit", "graph."+edge.name, "compiled query exceeds bind budget")
		}
		fingerprint := childFingerprint
		if basePrepared.run != nil {
			compiled, err = graphPreencodeBaseOccurrences(probeCompiled, basePrepared.statement, compiled)
			if err != nil {
				return nil, err
			}
		}
		prepared, err := edge.child.query.prepareCompiledRaw(executor, compiled)
		if err != nil {
			return nil, err
		}
		rows, err := prepared.run(ctx, executor, func() (int64, error) { *rowCount++; return *rowCount, nil })
		if err != nil {
			return nil, err
		}
		for _, tuple := range missing {
			edgeCache[graphCacheKeyFor(fingerprint, tuple)] = graphCacheEntry{decoder: edge.child.query.decoderValue()}
		}
		for _, row := range rows {
			tuple, present, tupleErr := edge.childKey.Tuple(row.row, graphKeyEncoder{codecs: codecs})
			if tupleErr != nil {
				return nil, tupleErr
			}
			if !present {
				return nil, planError("foreign_key_result", "graph."+edge.name, "child key is absent")
			}
			entry := edgeCache[graphCacheKeyFor(fingerprint, tuple)]
			entry.rows = append(entry.rows, graphRow{row: row.row})
			entry.decoder = edge.child.query.decoderValue()
			edgeCache[graphCacheKeyFor(fingerprint, tuple)] = entry
			if _, ok := groups[tuple.Identity]; !ok {
				return nil, planError("foreign_key_result", "graph."+edge.name, "child key was not requested")
			}
			loaded[tuple.Identity] = append(loaded[tuple.Identity], graphRow{row: row.row})
		}
	}
	next := make([]graphWork, 0)
	for i, parent := range parents {
		children := loaded[parentKeys[i]]
		if !parentPresent[i] {
			children = nil
		}
		if edge.kind == graphHasOne && len(children) > 1 {
			return nil, planError("cardinality", "graph."+edge.name, "has-one returned multiple rows")
		}
		values := make([]any, len(children))
		for j := range children {
			values[j] = edge.child.query.mapRow(graphCloneValue(children[j].row))
		}
		if edge.kind == graphHasOne {
			if len(values) == 0 {
				_, callback, err := edge.attach.attach(parent.parent, nil, false, false)
				if err != nil {
					return nil, err
				}
				*deferred = append(*deferred, graphDeferred{depth: parent.depth, fn: callback})
			} else {
				attached, callback, err := edge.attach.attach(parent.parent, values[0], false, true)
				if err != nil {
					return nil, err
				}
				*deferred = append(*deferred, graphDeferred{depth: parent.depth, fn: callback})
				next = append(next, graphWork{node: edge.child, parent: attached[0], row: children[0].row, depth: parent.depth + 1})
			}
		} else {
			attached, callback, err := edge.attach.attach(parent.parent, values, true, true)
			if err != nil {
				return nil, err
			}
			*deferred = append(*deferred, graphDeferred{depth: parent.depth, fn: callback})
			for j, value := range children {
				next = append(next, graphWork{node: edge.child, parent: attached[j], row: value.row, depth: parent.depth + 1})
			}
		}
	}
	return next, nil
}

func executeManyThrough(ctx context.Context, executor Executor, edge *graphEdgeSpec, parents []graphWork, rowCount *int64, deferred *[]graphDeferred, cache map[graphCacheKey]graphCacheEntry) ([]graphWork, error) {
	codecs := graphCodecs(executor)
	parentTuples := make([]keyTuple, 0, len(parents))
	parentKeys := make([]string, len(parents))
	parentPresent := make([]bool, len(parents))
	parentIndex := make(map[string][]int)
	for i, parent := range parents {
		tuple, present, err := edge.parentKey.Tuple(parent.row, graphKeyEncoder{codecs: codecs})
		if err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		parentPresent[i] = true
		parentKeys[i] = tuple.Identity
		if _, ok := parentIndex[tuple.Identity]; !ok {
			parentTuples = append(parentTuples, tuple)
		}
		parentIndex[tuple.Identity] = append(parentIndex[tuple.Identity], i)
	}
	if len(parentTuples) == 0 {
		for _, parent := range parents {
			_, callback, err := edge.attach.attach(parent.parent, []any{}, true, false)
			if err != nil {
				return nil, err
			}
			*deferred = append(*deferred, graphDeferred{depth: parent.depth, fn: callback})
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
	junctionBase, err := junctionPlan.withOptions(edge.options, edge.junctionParent, edge.options.PerParentLimit)
	if err != nil {
		return nil, err
	}
	compiled, err := junctionBase.compile(executor)
	if err != nil {
		return nil, err
	}
	junctionCacheable := graphStageCacheable(compiled)
	junctionCache := cache
	if !junctionCacheable {
		junctionCache = make(map[graphCacheKey]graphCacheEntry)
	}
	junctionPrepared := graphPreparedQuery{}
	if junctionCacheable {
		junctionPrepared, err = junctionPlan.prepareCompiled(executor, compiled)
		if err != nil {
			return nil, err
		}
	}
	fixed = len(compiled.Slots)
	budget := edge.options.BindLimit
	if budget == 0 || budget > profile.MaxBind {
		budget = profile.MaxBind
	}
	fingerprintCompiled := compiled
	fingerprintCompiled.Statement = junctionPrepared.statement
	junctionFingerprint := graphCacheFingerprint{Stage: "junction"}
	if junctionCacheable {
		junctionFingerprint, err = graphInvocationFingerprint(graphFingerprintStage{
			Name: "junction", Source: edge.junction.ref.QualifiedName(), Columns: junctionBase.schemaValue().Columns(),
			Keys: []*graphKeySpec{edge.junctionParent, edge.junctionChild}, Compiled: fingerprintCompiled,
			PerParentLimit: edge.options.PerParentLimit, BindLimit: budget,
		}, profile)
		if err != nil {
			return nil, err
		}
	}
	width := len(edge.junctionParent.Parts)
	batchSize := (budget - fixed) / width
	if batchSize <= 0 {
		return nil, planError("bind_limit", "graph."+edge.name, "bind budget cannot fit one key")
	}
	byParent := make(map[string][]keyTuple)
	targetOrder := make([]keyTuple, 0)
	targetSeen := make(map[string]struct{})
	addJunctionRows := func(rows []graphRow) error {
		for _, row := range rows {
			junctionRow, ok := row.row.(graphJunctionRow)
			if !ok {
				return planError("internal_plan", "graph."+edge.name, "junction row type mismatch")
			}
			if len(junctionRow.parentTuple.Components) == 0 {
				return planError("foreign_key_result", "graph."+edge.name, "junction parent is absent")
			}
			parentTuple := junctionRow.parentTuple
			if _, requested := parentIndex[parentTuple.Identity]; !requested {
				return planError("foreign_key_result", "graph."+edge.name, "junction parent was not requested")
			}
			if len(junctionRow.targetTuple.Components) == 0 {
				return planError("foreign_key_result", "graph."+edge.name, "junction target is absent")
			}
			targetTuple := junctionRow.targetTuple
			duplicate := false
			for _, prior := range byParent[parentTuple.Identity] {
				if prior.Identity == targetTuple.Identity {
					duplicate = true
					break
				}
			}
			if duplicate {
				continue
			}
			byParent[parentTuple.Identity] = append(byParent[parentTuple.Identity], targetTuple)
			if _, ok := targetSeen[targetTuple.Identity]; !ok {
				targetSeen[targetTuple.Identity] = struct{}{}
				targetOrder = append(targetOrder, targetTuple)
			}
		}
		return nil
	}
	for start := 0; start < len(parentTuples); start += batchSize {
		end := start + batchSize
		if end > len(parentTuples) {
			end = len(parentTuples)
		}
		missing := make([]keyTuple, 0, end-start)
		for _, tuple := range parentTuples[start:end] {
			entry, ok := junctionCache[graphCacheKeyFor(junctionFingerprint, tuple)]
			if ok && !reflect.DeepEqual(entry.decoder, junctionPlan.decoderValue()) {
				ok = false
			}
			if !ok {
				missing = append(missing, tuple)
			}
		}
		if len(missing) > 0 {
			membership, err := buildGraphMembership(edge.junctionParent, missing)
			if err != nil {
				return nil, err
			}
			limited := junctionBase.withMembership(membership)
			finalCompiled, err := limited.compile(executor)
			if err != nil {
				return nil, err
			} else if len(finalCompiled.Slots) > budget || (profile.MaxBind > 0 && len(finalCompiled.Slots) > profile.MaxBind) {
				return nil, planError("bind_limit", "graph."+edge.name, "compiled junction query exceeds bind budget")
			}
			if junctionCacheable {
				finalCompiled, err = graphPreencodeBaseOccurrences(compiled, junctionPrepared.statement, finalCompiled)
				if err != nil {
					return nil, err
				}
			}
			prepared, err := junctionPlan.prepareCompiled(executor, finalCompiled)
			if err != nil {
				return nil, err
			}
			rows, err := prepared.run(ctx, executor, func() (int64, error) { *rowCount++; return *rowCount, nil })
			if err != nil {
				return nil, err
			}
			for _, tuple := range missing {
				junctionCache[graphCacheKeyFor(junctionFingerprint, tuple)] = graphCacheEntry{decoder: junctionPlan.decoderValue()}
			}
			for _, row := range rows {
				junctionRow, ok := row.row.(graphJunctionRow)
				if !ok {
					return nil, planError("internal_plan", "graph."+edge.name, "junction row type mismatch")
				}
				parentTuple, present, err := graphTupleValues(edge.junctionParent.Parts, junctionRow.values[:len(edge.junctionParent.Parts)], codecs)
				if err != nil {
					return nil, err
				}
				if !present {
					return nil, planError("foreign_key_result", "graph."+edge.name, "junction parent is absent")
				}
				if _, requested := parentIndex[parentTuple.Identity]; !requested {
					return nil, planError("foreign_key_result", "graph."+edge.name, "junction parent was not requested")
				}
				targetTuple, present, err := graphTupleValues(edge.junctionChild.Parts, junctionRow.values[len(edge.junctionParent.Parts):], codecs)
				if err != nil {
					return nil, err
				}
				if !present {
					return nil, planError("foreign_key_result", "graph."+edge.name, "junction target is absent")
				}
				junctionRow.parentTuple = parentTuple
				junctionRow.targetTuple = targetTuple
				entry := junctionCache[graphCacheKeyFor(junctionFingerprint, parentTuple)]
				entry.rows = append(entry.rows, graphRow{row: junctionRow})
				junctionCache[graphCacheKeyFor(junctionFingerprint, parentTuple)] = entry
				entry.decoder = junctionPlan.decoderValue()
				junctionCache[graphCacheKeyFor(junctionFingerprint, parentTuple)] = entry
			}
		}
		rows := make([]graphRow, 0)
		for _, tuple := range parentTuples[start:end] {
			rows = append(rows, junctionCache[graphCacheKeyFor(junctionFingerprint, tuple)].rows...)
		}
		if err := addJunctionRows(rows); err != nil {
			return nil, err
		}
	}
	targets := make(map[string]graphRow)
	if len(targetOrder) > 0 {
		targetBase, err := edge.child.query.withOptions(EdgeOptions{}, edge.childKey, 0)
		if err != nil {
			return nil, err
		}
		compiled, err = targetBase.compile(executor)
		if err != nil {
			return nil, err
		}
		targetCacheable := graphStageCacheable(compiled)
		targetCache := cache
		if !targetCacheable {
			targetCache = make(map[graphCacheKey]graphCacheEntry)
		}
		targetPrepared := graphPreparedQuery{}
		if targetCacheable {
			targetPrepared, err = edge.child.query.prepareCompiledRaw(executor, compiled)
			if err != nil {
				return nil, err
			}
		}
		fixed = len(compiled.Slots)
		fingerprintCompiled := compiled
		fingerprintCompiled.Statement = targetPrepared.statement
		targetFingerprint := graphCacheFingerprint{Stage: "target"}
		if targetCacheable {
			targetFingerprint, err = graphInvocationFingerprint(graphFingerprintStage{
				Name: "target", Source: targetBase.sourceName(), Columns: targetBase.schemaValue().Columns(), Keys: []*graphKeySpec{edge.childKey},
				Compiled: fingerprintCompiled, PerParentLimit: 0, BindLimit: budget,
			}, profile)
			if err != nil {
				return nil, err
			}
		}
		targetWidth := len(edge.childKey.Parts)
		batchSize = (budget - fixed) / targetWidth
		if batchSize <= 0 {
			return nil, planError("bind_limit", "graph."+edge.name, "bind budget cannot fit target key")
		}
		for start := 0; start < len(targetOrder); start += batchSize {
			end := start + batchSize
			if end > len(targetOrder) {
				end = len(targetOrder)
			}
			missing := make([]keyTuple, 0, end-start)
			for _, tuple := range targetOrder[start:end] {
				entry, ok := targetCache[graphCacheKeyFor(targetFingerprint, tuple)]
				if ok && !reflect.DeepEqual(entry.decoder, edge.child.query.decoderValue()) {
					ok = false
				}
				if !ok {
					missing = append(missing, tuple)
					continue
				}
				for _, row := range entry.rows {
					targets[tuple.Identity] = graphRow{row: row.row}
				}
			}
			if len(missing) == 0 {
				continue
			}
			membership, err := buildGraphMembership(edge.childKey, missing)
			if err != nil {
				return nil, err
			}
			childQuery := targetBase.withMembership(membership)
			finalCompiled, err := childQuery.compile(executor)
			if err != nil {
				return nil, err
			} else if len(finalCompiled.Slots) > budget || (profile.MaxBind > 0 && len(finalCompiled.Slots) > profile.MaxBind) {
				return nil, planError("bind_limit", "graph."+edge.name, "compiled target query exceeds bind budget")
			}
			if targetCacheable {
				finalCompiled, err = graphPreencodeBaseOccurrences(compiled, targetPrepared.statement, finalCompiled)
				if err != nil {
					return nil, err
				}
			}
			prepared, err := edge.child.query.prepareCompiledRaw(executor, finalCompiled)
			if err != nil {
				return nil, err
			}
			rows, err := prepared.run(ctx, executor, func() (int64, error) { *rowCount++; return *rowCount, nil })
			if err != nil {
				return nil, err
			}
			for _, tuple := range missing {
				targetCache[graphCacheKeyFor(targetFingerprint, tuple)] = graphCacheEntry{decoder: edge.child.query.decoderValue()}
			}
			for _, row := range rows {
				tuple, present, tupleErr := edge.childKey.Tuple(row.row, graphKeyEncoder{codecs: codecs})
				if tupleErr != nil {
					return nil, tupleErr
				}
				if !present {
					return nil, planError("foreign_key_result", "graph."+edge.name, "target key is absent")
				}
				if _, ok := targetSeen[tuple.Identity]; !ok {
					return nil, planError("foreign_key_result", "graph."+edge.name, "target key was not requested")
				}
				entry := targetCache[graphCacheKeyFor(targetFingerprint, tuple)]
				entry.rows = append(entry.rows, graphRow{row: row.row})
				targetCache[graphCacheKeyFor(targetFingerprint, tuple)] = entry
				entry.decoder = edge.child.query.decoderValue()
				targetCache[graphCacheKeyFor(targetFingerprint, tuple)] = entry
				if _, duplicate := targets[tuple.Identity]; duplicate {
					return nil, planError("cardinality", "graph."+edge.name, "target returned duplicate rows")
				}
				targets[tuple.Identity] = graphRow{row: row.row}
			}
		}
	}
	next := make([]graphWork, 0)
	for i, parent := range parents {
		if !parentPresent[i] {
			_, callback, err := edge.attach.attach(parent.parent, []any{}, true, false)
			if err != nil {
				return nil, err
			}
			*deferred = append(*deferred, graphDeferred{depth: parent.depth, fn: callback})
			continue
		}
		junctionTargets := byParent[parentKeys[i]]
		values := make([]any, 0, len(junctionTargets))
		for _, target := range junctionTargets {
			row, ok := targets[target.Identity]
			if !ok {
				if !edge.child.query.hasPredicates() {
					return nil, planError("foreign_key_result", "graph."+edge.name, "junction target is missing")
				}
				continue
			}
			values = append(values, edge.child.query.mapRow(graphCloneValue(row.row)))
			// The attachment below copies values into the parent's relation slice.
		}
		attached, callback, err := edge.attach.attach(parent.parent, values, true, true)
		if err != nil {
			return nil, err
		}
		*deferred = append(*deferred, graphDeferred{depth: parent.depth, fn: callback})
		attachedIndex := 0
		for _, target := range junctionTargets {
			row, exists := targets[target.Identity]
			if !exists {
				continue
			}
			next = append(next, graphWork{node: edge.child, parent: attached[attachedIndex], row: row.row, depth: parent.depth + 1})
			attachedIndex++
		}
		_ = i
	}
	return next, nil
}
