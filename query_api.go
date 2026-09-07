package rasql

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
)

// Nullable represents a SQL value which may be NULL.
type Nullable[T any] struct {
	Value T
	Valid bool
}

type nullableScanDestination interface {
	nullableValue() any
	nullableClear()
	nullableValid()
}

func (n *Nullable[T]) nullableValue() any { return &n.Value }
func (n *Nullable[T]) nullableClear()     { var zero T; n.Value = zero; n.Valid = false }
func (n *Nullable[T]) nullableValid()     { n.Valid = true }

// PlanError identifies an invalid immutable query plan.
type PlanError struct {
	Code, Path, Detail string
	cause              error
}

func (e *PlanError) Error() string {
	if e.Path == "" {
		return e.Code + ": " + e.Detail
	}
	return e.Code + " at " + e.Path + ": " + e.Detail
}
func (e *PlanError) Unwrap() error { return e.cause }
func planError(code, path, detail string) *PlanError {
	return &PlanError{Code: code, Path: path, Detail: detail}
}

type ResultSchema struct{ columns []ResultColumn }

func cloneQ1ResultColumns(columns []ResultColumn) []ResultColumn {
	result := make([]ResultColumn, len(columns))
	for i, column := range columns {
		result[i] = column
		result[i].Type = schema.CloneColumnType(column.Type)
	}
	return result
}

var codecPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)

func NewResultSchema(columns ...ResultColumn) (ResultSchema, error) {
	if len(columns) == 0 {
		return ResultSchema{}, planError("invalid_schema", "columns", "must not be empty")
	}
	copyColumns := cloneQ1ResultColumns(columns)
	seen := make(map[string]struct{}, len(copyColumns))
	for i, column := range copyColumns {
		path := fmt.Sprintf("columns[%d]", i)
		if err := schema.ValidateIdentifier(column.Name); err != nil || column.Name == "" {
			return ResultSchema{}, planError("invalid_schema", path+".name", "must be a valid non-empty identifier")
		}
		if _, ok := seen[column.Name]; ok {
			return ResultSchema{}, planError("invalid_schema", path+".name", "duplicates "+column.Name)
		}
		seen[column.Name] = struct{}{}
		if err := schema.ValidateColumnType(column.Type); err != nil {
			return ResultSchema{}, planError("invalid_schema", path+".type", err.Error())
		}
		if column.Codec != "" && !codecPattern.MatchString(column.Codec) {
			return ResultSchema{}, planError("invalid_schema", path+".codec", "malformed codec identifier")
		}
	}
	return ResultSchema{columns: copyColumns}, nil
}
func (s ResultSchema) Columns() []ResultColumn { return cloneQ1ResultColumns(s.columns) }

type Presence struct {
	component string
	columns   []string
}

func NewPresence(component string, columns ...string) (Presence, error) {
	if err := schema.ValidateSimpleIdentifier(component); err != nil {
		return Presence{}, planError("invalid_projection", "presence.component", err.Error())
	}
	if len(columns) == 0 {
		return Presence{}, planError("invalid_projection", "presence.columns", "must not be empty")
	}
	copyColumns := append([]string(nil), columns...)
	seen := make(map[string]struct{}, len(copyColumns))
	for _, column := range copyColumns {
		if err := schema.ValidateIdentifier(column); err != nil {
			return Presence{}, planError("invalid_projection", "presence.columns", err.Error())
		}
		if _, ok := seen[column]; ok {
			return Presence{}, planError("invalid_projection", "presence.columns", "duplicate column")
		}
		seen[column] = struct{}{}
	}
	return Presence{component: component, columns: copyColumns}, nil
}
func (p Presence) Component() string { return p.component }
func (p Presence) Columns() []string { return append([]string(nil), p.columns...) }

type RowDecoder[R any] interface {
	ResultSchema() ResultSchema
	Presence() []Presence
	DecodeRow(ScanSource, *R) error
}

type ProjectionItem struct {
	expression query.Expression
	column     ResultColumn
	source     string
	bindErr    error
}

func Item[T any](name string, value Expr[T], logical schema.ColumnType, codec string) ProjectionItem {
	return ProjectionItem{expression: value.node, source: value.source, bindErr: value.bindErr, column: ResultColumn{Name: name, Type: logical, Codec: codec}}
}
func NullItem[T any](name string, value NullExpr[T], logical schema.ColumnType, codec string) ProjectionItem {
	return ProjectionItem{expression: value.node, source: value.source, bindErr: value.bindErr, column: ResultColumn{Name: name, Type: logical, Nullable: true, Codec: codec}}
}

type Projection[R any] struct {
	items   []ProjectionItem
	schema  ResultSchema
	decoder RowDecoder[R]
}

func NewProjection[R any](items []ProjectionItem, decoder RowDecoder[R]) (Projection[R], error) {
	if decoder == nil || (reflect.ValueOf(decoder).Kind() == reflect.Pointer && reflect.ValueOf(decoder).IsNil()) {
		return Projection[R]{}, planError("invalid_projection", "decoder", "must not be zero")
	}
	if len(items) == 0 {
		return Projection[R]{}, planError("invalid_projection", "items", "must not be empty")
	}
	columns := make([]ResultColumn, len(items))
	for i, item := range items {
		if item.expression == nil {
			return Projection[R]{}, planError("invalid_projection", fmt.Sprintf("items[%d]", i), "expression is zero")
		}
		columns[i] = item.column
	}
	schemaValue, err := NewResultSchema(columns...)
	if err != nil {
		return Projection[R]{}, err
	}
	if !reflect.DeepEqual(schemaValue.Columns(), decoder.ResultSchema().Columns()) {
		return Projection[R]{}, planError("invalid_projection", "decoder", "schema does not match projection")
	}
	if err := validateQ1DecoderMetadata(schemaValue, decoder); err != nil {
		return Projection[R]{}, err
	}
	seenComponents := make(map[string]struct{})
	seenColumns := make(map[string]struct{})
	for i, presence := range decoder.Presence() {
		if presence.component == "" || len(presence.columns) == 0 {
			return Projection[R]{}, planError("invalid_projection", fmt.Sprintf("decoder.presence[%d]", i), "presence metadata is incomplete")
		}
		if _, ok := seenComponents[presence.component]; ok {
			return Projection[R]{}, planError("invalid_projection", fmt.Sprintf("decoder.presence[%d]", i), "duplicate component")
		}
		seenComponents[presence.component] = struct{}{}
		for _, name := range presence.columns {
			if _, ok := seenColumns[name]; ok {
				return Projection[R]{}, planError("invalid_projection", fmt.Sprintf("decoder.presence[%d]", i), "duplicate presence column")
			}
			seenColumns[name] = struct{}{}
			found := false
			for _, column := range schemaValue.columns {
				if column.Name == name {
					if !column.Nullable {
						return Projection[R]{}, planError("invalid_projection", fmt.Sprintf("decoder.presence[%d]", i), "presence column must be nullable")
					}
					found = true
					break
				}
			}
			if !found {
				return Projection[R]{}, planError("invalid_projection", fmt.Sprintf("decoder.presence[%d]", i), "presence column is not projected")
			}
		}
	}
	return Projection[R]{items: cloneItems(items), schema: schemaValue, decoder: decoder}, nil
}
func (p Projection[R]) Schema() ResultSchema   { return p.schema }
func (p Projection[R]) Decoder() RowDecoder[R] { return p.decoder }
func cloneItems(items []ProjectionItem) []ProjectionItem {
	return append([]ProjectionItem(nil), items...)
}

type Source struct{ ref query.RelationRef }

func SourceOf[R any](table ReadTable[R], alias string) (TypedRelation[R], error) {
	if isNilReadTable(table) {
		return TypedRelation[R]{}, planError("invalid_source", "table", "must not be nil")
	}
	originalRef := table.Ref()
	definition := originalRef.Definition()
	detached, detachErr := ReadTableOf[R](definition)
	if detachErr != nil {
		return TypedRelation[R]{}, planError("invalid_source", "table", detachErr.Error())
	}
	ref := detached.Ref()
	var err error
	if alias == "" {
		alias = originalRef.Alias()
	}
	if alias != "" {
		ref, err = ref.As(alias)
		if err != nil {
			return TypedRelation[R]{}, planError("invalid_source", "alias", err.Error())
		}
	}
	return TypedRelation[R]{source: Source{ref: query.Relation(ref)}}, nil
}

type TypedRelation[R any] struct{ source Source }
type OptionalRelation[R any] struct{ source Source }

func Optional[R any](source TypedRelation[R]) OptionalRelation[R] {
	return OptionalRelation[R](source)
}
func (r TypedRelation[R]) Source() Source    { return r.source }
func (r OptionalRelation[R]) Source() Source { return r.source }

type QueryPlan struct {
	sources             []Source
	projection          []ProjectionItem
	where               []Predicate
	joins               []query.Join
	group               []GroupKey
	having              []Predicate
	order               []OrderTerm
	distinct            bool
	limit, offset       *int
	ctes                []query.CTE
	body                query.QueryBody
	partition           []GroupKey
	partitionOrder      []OrderTerm
	partitionLimit      int
	partitionLimitValue Expr[int64]
	err                 error
	projected           bool
	native              *nativeQueryPlan
	mutation            query.WriteStatement
	planErr             error
}
type Query[R any] struct {
	plan              QueryPlan
	projection        Projection[R]
	resultRequirement queryResultRequirement
}

func (p QueryPlan) Validate() error {
	if p.err != nil {
		return p.err
	}
	if p.planErr != nil {
		return p.planErr
	}
	if p.partitionLimit > 0 {
		if err := validatePartitionLimitValue(p.partitionLimitValue, p.partitionLimit); err != nil {
			return err
		}
	}
	if p.native != nil {
		if p.native.engine == "" || strings.TrimSpace(p.native.statement.SQL()) == "" {
			return planError("invalid_projection", "native", "native plan is incomplete")
		}
		if len(p.sources) != 0 || p.body != nil || p.mutation != nil {
			return planError("unsupported_feature", "native", "native plans cannot be composed")
		}
		return nil
	}
	if p.mutation != nil {
		return p.mutation.Validate()
	}
	if p.body != nil {
		return p.body.Validate()
	} else if len(p.sources) == 0 {
		return planError("invalid_source", "plan.sources", "must not be empty")
	}
	seen := make(map[string]struct{}, len(p.sources))
	qualifiers := make(map[string]struct{}, len(p.sources))
	for i, source := range p.sources {
		if source.ref.QualifiedName() == "" {
			return planError("invalid_source", fmt.Sprintf("plan.sources[%d]", i), "source is zero")
		}
		name := q1SourceIdentity(source.ref)
		qualifier := source.ref.QualifiedName()
		if _, ok := qualifiers[qualifier]; ok {
			return planError("invalid_source", fmt.Sprintf("plan.sources[%d]", i), "duplicate SQL qualifier")
		}
		qualifiers[qualifier] = struct{}{}
		if _, ok := seen[name]; ok {
			return planError("invalid_source", fmt.Sprintf("plan.sources[%d]", i), "duplicate source")
		}
		seen[name] = struct{}{}
	}
	allowed := make(map[string]struct{}, len(seen)+len(p.joins))
	for identity := range seen {
		allowed[identity] = struct{}{}
	}
	for _, join := range p.joins {
		allowed[q1SourceIdentity(join.Source())] = struct{}{}
	}
	for i, item := range p.projection {
		if item.bindErr != nil {
			result := planError("unsnapshotable_bind", fmt.Sprintf("plan.projection[%d]", i), item.bindErr.Error())
			result.cause = item.bindErr
			return result
		}
		if err := validateQ1Expression(item.expression, allowed, fmt.Sprintf("plan.projection[%d]", i)); err != nil {
			return err
		}
		if item.expression == nil {
			return planError("invalid_projection", fmt.Sprintf("plan.projection[%d]", i), "expression is zero")
		}
	}
	joinAllowed := make(map[string]struct{})
	if len(p.sources) > 0 {
		joinAllowed[q1SourceIdentity(p.sources[0].ref)] = struct{}{}
	}
	for i, join := range p.joins {
		if join.On() == nil {
			return planError("invalid_source", fmt.Sprintf("plan.joins[%d].on", i), "condition is zero")
		}
		if join.Source().QualifiedName() == "" {
			return planError("invalid_source", fmt.Sprintf("plan.joins[%d].source", i), "source is zero")
		}
		qualifier := join.Source().QualifiedName()
		if _, ok := qualifiers[qualifier]; ok {
			return planError("invalid_source", fmt.Sprintf("plan.joins[%d].source", i), "duplicate SQL qualifier")
		}
		qualifiers[qualifier] = struct{}{}
		joinIdentity := q1SourceIdentity(join.Source())
		if _, exists := seen[joinIdentity]; exists {
			return planError("invalid_source", fmt.Sprintf("plan.joins[%d].source", i), "duplicate source")
		}
		seen[joinIdentity] = struct{}{}
		joinAllowed[joinIdentity] = struct{}{}
		if err := validateQ1Expression(join.On(), joinAllowed, fmt.Sprintf("plan.joins[%d].on", i)); err != nil {
			return err
		}
	}
	for i, predicate := range append(append([]Predicate(nil), p.where...), p.having...) {
		if predicate.node == nil {
			return planError("invalid_projection", fmt.Sprintf("plan.predicates[%d]", i), "predicate is zero")
		}
		if predicate.bindErr != nil {
			result := planError("unsnapshotable_bind", fmt.Sprintf("plan.predicates[%d]", i), predicate.bindErr.Error())
			result.cause = predicate.bindErr
			return result
		}
		if err := validateQ1Expression(predicate.node, allowed, fmt.Sprintf("plan.predicates[%d]", i)); err != nil {
			return err
		}
	}
	for i, key := range p.group {
		if key.node == nil {
			return planError("invalid_projection", fmt.Sprintf("plan.group[%d]", i), "group key is zero")
		}
		if err := validateQ1Expression(key.node, allowed, fmt.Sprintf("plan.group[%d]", i)); err != nil {
			return err
		}
	}
	for i, term := range p.order {
		if term.node == nil {
			return planError("invalid_projection", fmt.Sprintf("plan.order[%d]", i), "order term is zero")
		}
		if err := validateQ1Expression(term.node, allowed, fmt.Sprintf("plan.order[%d]", i)); err != nil {
			return err
		}
	}
	return nil
}

func q1SourceIdentity(ref query.RelationRef) string {
	if table, ok := ref.Table(); ok {
		return fmt.Sprintf("table:%s|schema:%s|name:%s|alias:%s", table.QualifierSchema(), table.Schema(), table.Name(), ref.Alias())
	}
	return fmt.Sprintf("relation:%s|alias:%s", ref.QualifiedName(), ref.Alias())
}

func validateQ1Expression(expression query.Expression, allowed map[string]struct{}, path string) error {
	if expression == nil {
		return planError("invalid_projection", path, "expression is zero")
	}
	switch node := expression.(type) {
	case query.ColumnRef:
		if err := node.Validate(); err != nil {
			return planError("invalid_source", path, err.Error())
		}
		name := q1SourceIdentity(node.Source())
		if _, ok := allowed[name]; !ok {
			return planError("invalid_source", path, "expression source is outside plan")
		}
	case query.Value:
		if token, ok := node.Argument().(bindToken); ok && token.err != nil {
			result := planError("unsnapshotable_bind", path, token.err.Error())
			result.cause = token.err
			return result
		}
	case query.Binary:
		if err := validateQ1Expression(node.Left(), allowed, path+".left"); err != nil {
			return err
		}
		return validateQ1Expression(node.Right(), allowed, path+".right")
	case query.Logical:
		for i, child := range node.Expressions() {
			if err := validateQ1Expression(child, allowed, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case query.Not:
		return validateQ1Expression(node.Expression(), allowed, path+".expression")
	case query.NullTest:
		return validateQ1Expression(node.Expression(), allowed, path+".expression")
	case query.Function:
		for i, child := range node.Arguments() {
			if err := validateQ1Expression(child, allowed, fmt.Sprintf("%s.arguments[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}
func (q Query[R]) Validate() error {
	if err := q.plan.Validate(); err != nil {
		return err
	}
	if len(q.projection.items) == 0 || q.projection.decoder == nil {
		return planError("invalid_projection", "projection", "projection is zero")
	}
	decoderSchema := q.projection.decoder.ResultSchema()
	if !reflect.DeepEqual(decoderSchema.Columns(), q.projection.schema.Columns()) {
		return planError("invalid_projection", "decoder", "schema changed after projection construction")
	}
	if err := validateQ1DecoderMetadata(q.projection.schema, q.projection.decoder); err != nil {
		return err
	}
	return nil
}

func validateQ1DecoderMetadata(resultSchema ResultSchema, decoder interface{ Presence() []Presence }) error {
	seenComponents := map[string]struct{}{}
	seenColumns := map[string]struct{}{}
	for i, presence := range decoder.Presence() {
		if presence.component == "" || len(presence.columns) == 0 {
			return planError("invalid_projection", fmt.Sprintf("decoder.presence[%d]", i), "presence metadata is incomplete")
		}
		if _, ok := seenComponents[presence.component]; ok {
			return planError("invalid_projection", fmt.Sprintf("decoder.presence[%d]", i), "duplicate component")
		}
		seenComponents[presence.component] = struct{}{}
		for _, name := range presence.columns {
			if _, ok := seenColumns[name]; ok {
				return planError("invalid_projection", fmt.Sprintf("decoder.presence[%d]", i), "duplicate presence column")
			}
			seenColumns[name] = struct{}{}
			found := false
			for _, column := range resultSchema.columns {
				if column.Name == name {
					found = true
					if !column.Nullable {
						return planError("invalid_projection", fmt.Sprintf("decoder.presence[%d]", i), "presence column must be nullable")
					}
					break
				}
			}
			if !found {
				return planError("invalid_projection", fmt.Sprintf("decoder.presence[%d]", i), "presence column is not projected")
			}
		}
	}
	return nil
}

func Select[R any](from Source, projection Projection[R]) Query[R] {
	return Query[R]{plan: QueryPlan{sources: []Source{from}, projection: cloneItems(projection.items)}, projection: projection}
}
func Project[R any](base QueryPlan, projection Projection[R]) Query[R] {
	base.projection = cloneItems(projection.items)
	base.projected = true
	if base.native != nil {
		base.planErr = planError("unsupported_feature", "native", "native plans cannot be composed")
	}
	return Query[R]{plan: base, projection: projection}
}
func (q Query[R]) Schema() ResultSchema      { return q.projection.Schema() }
func (q Query[R]) Projection() Projection[R] { return q.projection }
func (q Query[R]) Plan() QueryPlan           { return clonePlan(q.plan) }
func clonePlan(p QueryPlan) QueryPlan {
	p.sources = append([]Source(nil), p.sources...)
	p.projection = cloneItems(p.projection)
	p.where = append([]Predicate(nil), p.where...)
	p.joins = append([]query.Join(nil), p.joins...)
	p.group = append([]GroupKey(nil), p.group...)
	p.having = append([]Predicate(nil), p.having...)
	p.order = append([]OrderTerm(nil), p.order...)
	p.ctes = append([]query.CTE(nil), p.ctes...)
	p.partition = append([]GroupKey(nil), p.partition...)
	p.partitionOrder = append([]OrderTerm(nil), p.partitionOrder...)
	p.native = cloneNativePlan(p.native)
	return p
}
func (q Query[R]) Where(p Predicate) Query[R] {
	q.plan = clonePlan(q.plan)
	if q.plan.native != nil {
		q.plan.planErr = planError("unsupported_feature", "native", "native plans cannot be composed")
		return q
	}
	q.plan.where = append(q.plan.where, p)
	return q
}
func (q Query[R]) Join(s Source, on Predicate) Query[R] {
	q.plan = clonePlan(q.plan)
	if q.plan.native != nil {
		q.plan.planErr = planError("unsupported_feature", "native", "native plans cannot be composed")
		return q
	}
	q.plan.joins = append(q.plan.joins, query.InnerJoin(s.ref, on.node))
	return q
}
func (q Query[R]) LeftJoin(s Source, on Predicate) Query[R] {
	q.plan = clonePlan(q.plan)
	if q.plan.native != nil {
		q.plan.planErr = planError("unsupported_feature", "native", "native plans cannot be composed")
		return q
	}
	q.plan.joins = append(q.plan.joins, query.LeftJoin(s.ref, on.node))
	return q
}
func (q Query[R]) GroupBy(keys ...GroupKey) Query[R] {
	q.plan = clonePlan(q.plan)
	if q.plan.native != nil {
		q.plan.planErr = planError("unsupported_feature", "native", "native plans cannot be composed")
		return q
	}
	q.plan.group = append(q.plan.group, keys...)
	return q
}
func (q Query[R]) Having(p Predicate) Query[R] {
	q.plan = clonePlan(q.plan)
	if q.plan.native != nil {
		q.plan.planErr = planError("unsupported_feature", "native", "native plans cannot be composed")
		return q
	}
	q.plan.having = append(q.plan.having, p)
	return q
}
func (q Query[R]) OrderBy(terms ...OrderTerm) Query[R] {
	q.plan = clonePlan(q.plan)
	if q.plan.native != nil {
		q.plan.planErr = planError("unsupported_feature", "native", "native plans cannot be composed")
		return q
	}
	q.plan.order = append(q.plan.order, terms...)
	return q
}
func (q Query[R]) Distinct() Query[R] {
	q.plan = clonePlan(q.plan)
	if q.plan.native != nil {
		q.plan.planErr = planError("unsupported_feature", "native", "native plans cannot be composed")
		return q
	}
	q.plan.distinct = true
	return q
}
func (q Query[R]) Limit(n int) (Query[R], error) {
	if n < 0 {
		return q, planError("invalid_projection", "limit", "must not be negative")
	}
	q.plan = clonePlan(q.plan)
	if q.plan.native != nil {
		q.plan.planErr = planError("unsupported_feature", "native", "native plans cannot be composed")
		return q, nil
	}
	q.plan.limit = &n
	return q, nil
}
func (q Query[R]) Offset(n int) (Query[R], error) {
	if n < 0 {
		return q, planError("invalid_projection", "offset", "must not be negative")
	}
	q.plan = clonePlan(q.plan)
	if q.plan.native != nil {
		q.plan.planErr = planError("unsupported_feature", "native", "native plans cannot be composed")
		return q, nil
	}
	q.plan.offset = &n
	return q, nil
}

// withKeysetOrder installs the canonical order for keyset pagination. It is
// intentionally private: generated/runtime page helpers are the only callers.
func (q Query[R]) withKeysetOrder(order []OrderTerm) (Query[R], error) {
	if q.plan.limit != nil {
		return q, planError("keyset_limit_conflict", "query.limit", "base query must not have a limit")
	}
	if q.plan.offset != nil {
		return q, planError("keyset_offset_conflict", "query.offset", "base query must not have an offset")
	}
	if len(order) == 0 {
		return q, planError("invalid_keyset_order", "query.order", "must not be empty")
	}
	for i, term := range order {
		if term.node == nil {
			return q, planError("invalid_keyset_order", fmt.Sprintf("query.order[%d]", i), "must not be zero")
		}
		if term.nulls > NullsLast {
			return q, planError("invalid_keyset_order", fmt.Sprintf("query.order[%d]", i), "invalid NULL placement")
		}
	}
	if len(q.plan.order) > 0 {
		if len(q.plan.order) != len(order) {
			return q, planError("keyset_order_mismatch", "query.order", "existing order differs")
		}
		for i := range order {
			a, b := q.plan.order[i], order[i]
			if !reflect.DeepEqual(a.node, b.node) || a.source != b.source || a.descending != b.descending || a.nulls != b.nulls {
				return q, planError("keyset_order_mismatch", "query.order", "existing order differs")
			}
		}
		return q, nil
	}
	q.plan = clonePlan(q.plan)
	q.plan.order = append([]OrderTerm(nil), order...)
	return q, nil
}

func (q Query[R]) withPartitionLimit(partition []GroupKey, order []OrderTerm, limit int) (Query[R], error) {
	if q.plan.native != nil {
		return q, planError("unsupported_feature", "native", "native plans cannot be composed")
	}
	if len(partition) == 0 {
		return q, planError("invalid_partition_limit", "partition", "must not be empty")
	}
	for i, key := range partition {
		if key.node == nil {
			return q, planError("invalid_partition_limit", fmt.Sprintf("partition[%d]", i), "must not be zero")
		}
	}
	if len(order) == 0 {
		return q, planError("invalid_partition_limit", "order", "must not be empty")
	}
	for i, term := range order {
		if term.node == nil {
			return q, planError("invalid_partition_limit", fmt.Sprintf("order[%d]", i), "must not be zero")
		}
		if term.nulls > NullsLast {
			return q, planError("invalid_partition_limit", fmt.Sprintf("order[%d]", i), "invalid NULL placement")
		}
	}
	if limit <= 0 {
		return q, planError("invalid_partition_limit", "limit", "must be positive")
	}
	limitValue := Value(int64(limit))
	q.plan = clonePlan(q.plan)
	q.plan.partition = append([]GroupKey(nil), partition...)
	q.plan.partitionOrder = append([]OrderTerm(nil), order...)
	q.plan.partitionLimit = limit
	q.plan.partitionLimitValue = limitValue
	return q, nil
}

func validatePartitionLimitValue(expression Expr[int64], limit int) error {
	if expression.bindErr != nil {
		return planError("unsnapshotable_bind", "plan.partition_limit", expression.bindErr.Error())
	}
	node, ok := expression.node.(query.Value)
	if !ok {
		return planError("internal_plan", "plan.partition_limit", "partition limit bind is missing")
	}
	token, ok := node.Argument().(bindToken)
	if !ok || token.id == 0 || token.codec != "" || token.preEncoded || token.copy == nil || token.err != nil {
		return planError("internal_plan", "plan.partition_limit", "partition limit bind is invalid")
	}
	value, ok := token.value.(int64)
	if !ok || value != int64(limit) {
		return planError("internal_plan", "plan.partition_limit", "partition limit bind value differs")
	}
	return nil
}

type scalarDecoder[T any] struct{ schema ResultSchema }

func (d scalarDecoder[T]) ResultSchema() ResultSchema                   { return d.schema }
func (d scalarDecoder[T]) Presence() []Presence                         { return nil }
func (d scalarDecoder[T]) DecodeRow(source ScanSource, result *T) error { return source.Scan(result) }

type nullableScalarDecoder[T any] struct{ schema ResultSchema }

func (d nullableScalarDecoder[T]) ResultSchema() ResultSchema { return d.schema }
func (d nullableScalarDecoder[T]) Presence() []Presence       { return nil }
func (d nullableScalarDecoder[T]) DecodeRow(source ScanSource, result *Nullable[T]) error {
	var raw any
	if err := source.Scan(&raw); err != nil {
		return err
	}
	if raw == nil {
		result.Valid = false
		var zero T
		result.Value = zero
		return nil
	}
	if err := ScanValue(&result.Value, raw); err != nil {
		return err
	}
	result.Valid = true
	return nil
}
func Scalar[T any](name string, value Expr[T], logical schema.ColumnType, codec string) (Projection[T], error) {
	resultSchema, err := NewResultSchema(ResultColumn{Name: name, Type: logical, Codec: codec})
	if err != nil {
		return Projection[T]{}, err
	}
	return NewProjection([]ProjectionItem{Item(name, value, logical, codec)}, scalarDecoder[T]{schema: resultSchema})
}
func NullableScalar[T any](name string, value NullExpr[T], logical schema.ColumnType, codec string) (Projection[Nullable[T]], error) {
	resultSchema, err := NewResultSchema(ResultColumn{Name: name, Type: logical, Nullable: true, Codec: codec})
	if err != nil {
		return Projection[Nullable[T]]{}, err
	}
	return NewProjection([]ProjectionItem{NullItem(name, value, logical, codec)}, nullableScalarDecoder[T]{schema: resultSchema})
}
