package rasql

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
)

func graphPreencodeBaseOccurrences(base compiledQuery, encoded stmt.Statement, final compiledQuery) (compiledQuery, error) {
	occurrences, err := matchBaseOccurrences(base, final)
	if err != nil {
		return compiledQuery{}, err
	}
	args := final.statement.Args()
	baseArgs := encoded.Args()
	for i, position := range occurrences {
		if position >= len(args) || i >= len(baseArgs) {
			return compiledQuery{}, planError("internal_plan", "binds", "encoded occurrence is out of range")
		}
		value := baseArgs[i]
		args[position] = value
		final.bindSlots[position].preEncoded = true
		final.copyArgs[position] = func() (any, error) { return graphCloneEncoded(value), nil }
	}
	final.statement = stmt.New(sqltext.Text(final.statement.SQL()), args...)
	return final, nil
}

func graphCloneEncoded(value any) any {
	switch value := value.(type) {
	case []byte:
		return append([]byte(nil), value...)
	case sql.NamedArg:
		value.Value = graphCloneEncoded(value.Value)
		return value
	default:
		return value
	}
}

func graphCodecs(executor Executor) CodecRegistry {
	if provider, ok := executor.(CodecProvider); ok && provider.Codecs() != nil {
		return provider.Codecs()
	}
	return builtinCodecs
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
		if _, ok := done[edge.child.id]; ok {
			continue
		}
		if _, ok := active[edge.child.id]; ok {
			return planError("graph_cycle", path, "graph plan identity is active")
		}
		if err := validateGraphEdge(edge, executor, path); err != nil {
			return err
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
	if edge.childKey == nil || len(edge.childKey.parts) == 0 {
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
	if budget <= 0 || budget-len(compiled.bindSlots) < len(edge.childKey.parts) {
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
	if budget-len(junctionCompiled.bindSlots) < len(edge.junctionParent.parts) {
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
	if budget-len(targetCompiled.bindSlots) < len(edge.childKey.parts) {
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
			for _, part := range key.parts {
				if part.codec != "" {
					if _, ok := codecs.Lookup(CodecID(part.codec)); !ok {
						return planError("codec_unavailable", "graph.key", part.codec)
					}
				}
			}
		}
	}
	return nil
}

func executorCompilerProfile(executor Executor) engineProfileSnapshot {
	provider, ok := executor.(compilerProvider)
	if !ok || provider.queryCompiler() == nil {
		return engineProfileSnapshot{}
	}
	p := provider.queryCompiler().EngineProfile()
	return engineProfileSnapshot{ID: p.ID, Engine: uint8(p.Engine), Capabilities: p.Capabilities, MaxBind: p.Limits.MaxBindParameters}
}

type engineProfileSnapshot struct {
	ID           string
	Engine       uint8
	VersionMajor int
	VersionMinor int
	VersionPatch int
	Capabilities EngineCapabilities
	MaxBind      int
}

func LoadGraph[R, G any](ctx context.Context, executor Executor, plan GraphPlan[R, G]) ([]G, error) {
	if executor == nil || plan.node == nil {
		return nil, planError("invalid_graph_plan", "graph", "executor and plan are required")
	}
	var rootCompiled compiledQuery
	if err := graphValidate(plan.node, executor, &rootCompiled); err != nil {
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
	graphs := make([]G, len(rootRows))
	queue := make([]graphWork, len(rootRows))
	deferred := make([]graphDeferred, 0)
	cache := make(map[graphCacheKey]graphCacheEntry)
	for i, row := range rootRows {
		graphs[i] = row.graph.(G)
		queue[i] = graphWork{node: plan.node, parent: &graphs[i], row: row.row}
	}
	for len(queue) > 0 {
		current := queue
		queue = nil
		for _, edge := range planEdges(current) {
			next, err := executeGraphEdge(callCtx, observed, edge.edge, edge.parents, &observedRows, &deferred, cache)
			if err != nil {
				finalErr = err
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

type graphCacheFingerprint struct {
	stage  string
	digest [sha256.Size]byte
}

func graphInvocationFingerprint(edge *graphEdgeSpec, stage string, compiled compiledQuery) (graphCacheFingerprint, error) {
	var key strings.Builder
	writeGraphFingerprintPart := func(value string) {
		var size [binary.MaxVarintLen64]byte
		n := binary.PutUvarint(size[:], uint64(len(value)))
		key.Write(size[:n])
		key.WriteString(value)
	}
	writeGraphFingerprintPart(string([]byte{stage[0]}))
	writeGraphFingerprintPart(edge.child.query.sourceName())
	writeGraphFingerprintPart(compiled.statement.SQL())
	writeGraphFingerprintPart(strconv.FormatInt(int64(edge.options.PerParentLimit), 10))
	writeGraphFingerprintPart(strconv.FormatInt(int64(edge.options.BindLimit), 10))
	for _, column := range edge.child.query.schemaValue().Columns() {
		writeGraphFingerprintPart(column.Name)
		if column.Type != nil {
			writeGraphFingerprintPart(string(column.Type.Kind()))
		}
		writeGraphFingerprintPart(strconv.FormatBool(column.Nullable))
		writeGraphFingerprintPart(column.Codec)
	}
	keySpec := edge.childKey
	if stage == "junction" {
		keySpec = edge.junctionParent
	}
	for _, part := range keySpec.parts {
		writeGraphFingerprintPart(part.column.Source().QualifiedName())
		writeGraphFingerprintPart(part.column.Name())
		if part.columnType != nil {
			writeGraphFingerprintPart(string(part.columnType.Kind()))
		}
		writeGraphFingerprintPart(part.typ.String())
		writeGraphFingerprintPart(part.codec)
	}
	args := compiled.statement.Args()
	for index, slot := range compiled.bindSlots {
		writeGraphFingerprintPart(slot.codec)
		writeGraphFingerprintPart(strconv.FormatBool(slot.preEncoded))
		if index < len(args) {
			if err := writeGraphFingerprintValue(&key, args[index]); err != nil {
				return graphCacheFingerprint{}, err
			}
		}
	}
	return graphCacheFingerprint{stage: stage, digest: sha256.Sum256([]byte(key.String()))}, nil
}

func writeGraphFingerprintValue(key *strings.Builder, value any) error {
	if named, ok := value.(sql.NamedArg); ok {
		if err := writeGraphFingerprintValue(key, named.Name); err != nil {
			return err
		}
		value = named.Value
	}
	normalized, err := normalizeGraphValue(value)
	if err != nil {
		return err
	}
	frame, err := frameGraphValue(normalized)
	if err != nil {
		return err
	}
	write := func(tag byte, data []byte) {
		key.WriteByte(tag)
		var size [binary.MaxVarintLen64]byte
		n := binary.PutUvarint(size[:], uint64(len(data)))
		key.Write(size[:n])
		key.Write(data)
	}
	write('v', frame)
	return nil
}

type graphCacheKey struct {
	fingerprint graphCacheFingerprint
	tuple       string
}

func graphCacheKeyFor(fingerprint graphCacheFingerprint, tuple keyTuple) graphCacheKey {
	return graphCacheKey{fingerprint: fingerprint, tuple: tuple.identity}
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
		identity.Write(frame)
	}
	result.identity = identity.String()
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
		fixed = len(probeCompiled.bindSlots)
	} else {
		return nil, err
	}
	basePrepared := graphPreparedQuery{}
	cacheable := true
	for _, slot := range probeCompiled.bindSlots {
		if slot.id == 0 {
			cacheable = false
			break
		}
	}
	if cacheable {
		basePrepared, err = probe.prepareCompiled(executor, probeCompiled)
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
	width := len(edge.childKey.parts)
	if budget <= fixed || (budget-fixed)/width == 0 {
		return nil, planError("bind_limit", "graph."+edge.name, "bind budget cannot fit one key")
	}
	batchSize := (budget - fixed) / width
	fingerprintCompiled := probeCompiled
	fingerprintCompiled.statement = basePrepared.statement
	childFingerprint, err := graphInvocationFingerprint(edge, "child", fingerprintCompiled)
	if err != nil {
		return nil, err
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
				loaded[tuple.identity] = append(loaded[tuple.identity], graphRow{row: row.row, graph: edge.child.query.mapRow(row.row)})
			}
		}
		if len(missing) == 0 {
			continue
		}
		membership, err := buildGraphMembership(edge.childKey, missing)
		if err != nil {
			return nil, err
		}
		limit := probeLimit
		childQuery, err := edge.child.query.with(membership, edge.childKey, edge.options, limit)
		if err != nil {
			return nil, err
		}
		compiled, err := childQuery.compile(executor)
		if err != nil {
			return nil, err
		} else if len(compiled.bindSlots) > budget || (profile.MaxBind > 0 && len(compiled.bindSlots) > profile.MaxBind) {
			return nil, planError("bind_limit", "graph."+edge.name, "compiled query exceeds bind budget")
		}
		fingerprint := childFingerprint
		if basePrepared.run != nil {
			compiled, err = graphPreencodeBaseOccurrences(probeCompiled, basePrepared.statement, compiled)
			if err != nil {
				return nil, err
			}
		}
		prepared, err := edge.child.query.prepareCompiled(executor, compiled)
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
			tuple, present, tupleErr := edge.childKey.tuple(row.row, codecs)
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
		}
		for _, tuple := range missing {
			for _, row := range edgeCache[graphCacheKeyFor(fingerprint, tuple)].rows {
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
				loaded[tuple.identity] = append(loaded[tuple.identity], graphRow{row: row.row, graph: edge.child.query.mapRow(row.row)})
			}
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
			if values == nil {
				values = []any{}
			}
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
	junctionCacheable := true
	for _, slot := range compiled.bindSlots {
		if slot.id == 0 {
			junctionCacheable = false
			break
		}
	}
	junctionPrepared := graphPreparedQuery{}
	if junctionCacheable {
		junctionPrepared, err = junctionPlan.prepareCompiled(executor, compiled)
		if err != nil {
			return nil, err
		}
	}
	fixed = len(compiled.bindSlots)
	fingerprintCompiled := compiled
	fingerprintCompiled.statement = junctionPrepared.statement
	junctionFingerprint, err := graphInvocationFingerprint(edge, "junction", fingerprintCompiled)
	if err != nil {
		return nil, err
	}
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
	addJunctionRows := func(rows []graphRow) error {
		for _, row := range rows {
			junctionRow, ok := row.row.(graphJunctionRow)
			if !ok {
				return planError("internal_plan", "graph."+edge.name, "junction row type mismatch")
			}
			parentTuple, present, err := graphTupleValues(edge.junctionParent.parts, junctionRow.values[:len(edge.junctionParent.parts)], codecs)
			if err != nil {
				return err
			}
			if !present {
				return planError("foreign_key_result", "graph."+edge.name, "junction parent is absent")
			}
			if _, requested := parentIndex[parentTuple.identity]; !requested {
				return planError("foreign_key_result", "graph."+edge.name, "junction parent was not requested")
			}
			targetTuple, present, err := graphTupleValues(edge.junctionChild.parts, junctionRow.values[len(edge.junctionParent.parts):], codecs)
			if err != nil {
				return err
			}
			if !present {
				return planError("foreign_key_result", "graph."+edge.name, "junction target is absent")
			}
			duplicate := false
			for _, prior := range byParent[parentTuple.identity] {
				if prior.identity == targetTuple.identity {
					duplicate = true
					break
				}
			}
			if duplicate {
				continue
			}
			byParent[parentTuple.identity] = append(byParent[parentTuple.identity], targetTuple)
			if _, ok := targetSeen[targetTuple.identity]; !ok {
				targetSeen[targetTuple.identity] = struct{}{}
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
			entry, ok := cache[graphCacheKeyFor(junctionFingerprint, tuple)]
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
			limited, err := junctionPlan.with(membership, edge.junctionParent, edge.options, edge.options.PerParentLimit)
			if err != nil {
				return nil, err
			}
			finalCompiled, err := limited.compile(executor)
			if err != nil {
				return nil, err
			} else if len(finalCompiled.bindSlots) > budget || (profile.MaxBind > 0 && len(finalCompiled.bindSlots) > profile.MaxBind) {
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
				cache[graphCacheKeyFor(junctionFingerprint, tuple)] = graphCacheEntry{decoder: junctionPlan.decoderValue()}
			}
			for _, row := range rows {
				junctionRow, ok := row.row.(graphJunctionRow)
				if !ok {
					return nil, planError("internal_plan", "graph."+edge.name, "junction row type mismatch")
				}
				parentTuple, present, err := graphTupleValues(edge.junctionParent.parts, junctionRow.values[:len(edge.junctionParent.parts)], codecs)
				if err != nil {
					return nil, err
				}
				if !present {
					return nil, planError("foreign_key_result", "graph."+edge.name, "junction parent is absent")
				}
				if _, requested := parentIndex[parentTuple.identity]; !requested {
					return nil, planError("foreign_key_result", "graph."+edge.name, "junction parent was not requested")
				}
				entry := cache[graphCacheKeyFor(junctionFingerprint, parentTuple)]
				entry.rows = append(entry.rows, graphRow{row: row.row})
				cache[graphCacheKeyFor(junctionFingerprint, parentTuple)] = entry
				entry.decoder = junctionPlan.decoderValue()
				cache[graphCacheKeyFor(junctionFingerprint, parentTuple)] = entry
			}
		}
		rows := make([]graphRow, 0)
		for _, tuple := range parentTuples[start:end] {
			rows = append(rows, cache[graphCacheKeyFor(junctionFingerprint, tuple)].rows...)
		}
		if err := addJunctionRows(rows); err != nil {
			return nil, err
		}
	}
	targets := make(map[string]graphRow)
	if len(targetOrder) > 0 {
		fixed = 0
		targetBase, err := edge.child.query.withOptions(EdgeOptions{}, edge.childKey, 0)
		if err != nil {
			return nil, err
		}
		compiled, err = targetBase.compile(executor)
		if err != nil {
			return nil, err
		}
		targetCacheable := true
		for _, slot := range compiled.bindSlots {
			if slot.id == 0 {
				targetCacheable = false
				break
			}
		}
		targetPrepared := graphPreparedQuery{}
		if targetCacheable {
			targetPrepared, err = edge.child.query.prepareCompiled(executor, compiled)
			if err != nil {
				return nil, err
			}
		}
		fixed = len(compiled.bindSlots)
		fingerprintCompiled := compiled
		fingerprintCompiled.statement = targetPrepared.statement
		targetFingerprint, err := graphInvocationFingerprint(edge, "target", fingerprintCompiled)
		if err != nil {
			return nil, err
		}
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
			missing := make([]keyTuple, 0, end-start)
			for _, tuple := range targetOrder[start:end] {
				entry, ok := cache[graphCacheKeyFor(targetFingerprint, tuple)]
				if ok && !reflect.DeepEqual(entry.decoder, edge.child.query.decoderValue()) {
					ok = false
				}
				if !ok {
					missing = append(missing, tuple)
					continue
				}
				for _, row := range entry.rows {
					targets[tuple.identity] = graphRow{row: row.row, graph: edge.child.query.mapRow(row.row)}
				}
			}
			if len(missing) == 0 {
				continue
			}
			membership, err := buildGraphMembership(edge.childKey, missing)
			if err != nil {
				return nil, err
			}
			childQuery, err := edge.child.query.with(membership, edge.childKey, EdgeOptions{}, 0)
			if err != nil {
				return nil, err
			}
			finalCompiled, err := childQuery.compile(executor)
			if err != nil {
				return nil, err
			} else if len(finalCompiled.bindSlots) > budget || (profile.MaxBind > 0 && len(finalCompiled.bindSlots) > profile.MaxBind) {
				return nil, planError("bind_limit", "graph."+edge.name, "compiled target query exceeds bind budget")
			}
			if targetCacheable {
				finalCompiled, err = graphPreencodeBaseOccurrences(compiled, targetPrepared.statement, finalCompiled)
				if err != nil {
					return nil, err
				}
			}
			prepared, err := edge.child.query.prepareCompiled(executor, finalCompiled)
			if err != nil {
				return nil, err
			}
			rows, err := prepared.run(ctx, executor, func() (int64, error) { *rowCount++; return *rowCount, nil })
			if err != nil {
				return nil, err
			}
			for _, tuple := range missing {
				cache[graphCacheKeyFor(targetFingerprint, tuple)] = graphCacheEntry{decoder: edge.child.query.decoderValue()}
			}
			for _, row := range rows {
				tuple, present, tupleErr := edge.childKey.tuple(row.row, codecs)
				if tupleErr != nil {
					return nil, tupleErr
				}
				if !present {
					return nil, planError("foreign_key_result", "graph."+edge.name, "target key is absent")
				}
				if _, ok := targetSeen[tuple.identity]; !ok {
					return nil, planError("foreign_key_result", "graph."+edge.name, "target key was not requested")
				}
				entry := cache[graphCacheKeyFor(targetFingerprint, tuple)]
				entry.rows = append(entry.rows, graphRow{row: row.row})
				cache[graphCacheKeyFor(targetFingerprint, tuple)] = entry
				entry.decoder = edge.child.query.decoderValue()
				cache[graphCacheKeyFor(targetFingerprint, tuple)] = entry
			}
			rows = rows[:0]
			for _, tuple := range targetOrder[start:end] {
				rows = append(rows, cache[graphCacheKeyFor(targetFingerprint, tuple)].rows...)
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
				targets[tuple.identity] = graphRow{row: row.row, graph: edge.child.query.mapRow(row.row)}
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
			_, callback, err := edge.attach.attach(parent.parent, []any{}, true, false)
			if err != nil {
				return nil, err
			}
			*deferred = append(*deferred, graphDeferred{depth: parent.depth, fn: callback})
			continue
		}
		junctionTargets := byParent[parentTuple.identity]
		values := make([]any, 0, len(junctionTargets))
		for _, target := range junctionTargets {
			row, ok := targets[target.identity]
			if !ok {
				if !edge.child.query.hasPredicates() {
					return nil, planError("foreign_key_result", "graph."+edge.name, "junction target is missing")
				}
				continue
			}
			values = append(values, row.graph)
			// The attachment below copies values into the parent's relation slice.
		}
		attached, callback, err := edge.attach.attach(parent.parent, values, true, true)
		if err != nil {
			return nil, err
		}
		*deferred = append(*deferred, graphDeferred{depth: parent.depth, fn: callback})
		attachedIndex := 0
		for _, target := range junctionTargets {
			row, exists := targets[target.identity]
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
