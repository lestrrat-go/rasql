package rasql

import (
	"fmt"
	"reflect"
	"regexp"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
)

// Nullable represents a SQL value which may be NULL.
type Nullable[T any] struct {
	Value T
	Valid bool
}

// PlanError identifies an invalid immutable query plan.
type PlanError struct{ Code, Path, Detail string }

func (e *PlanError) Error() string {
	if e.Path == "" {
		return e.Code + ": " + e.Detail
	}
	return e.Code + " at " + e.Path + ": " + e.Detail
}
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
	ref := table.Ref()
	var err error
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
	return OptionalRelation[R]{source: source.source}
}
func (r TypedRelation[R]) Source() Source    { return r.source }
func (r OptionalRelation[R]) Source() Source { return r.source }

type QueryPlan struct {
	sources       []Source
	projection    []ProjectionItem
	where         []Predicate
	joins         []query.Join
	group         []GroupKey
	having        []Predicate
	order         []OrderTerm
	distinct      bool
	limit, offset *int
}
type Query[R any] struct {
	plan       QueryPlan
	projection Projection[R]
}

func (p QueryPlan) Validate() error {
	if len(p.sources) == 0 {
		return planError("invalid_source", "plan.sources", "must not be empty")
	}
	seen := make(map[string]struct{}, len(p.sources))
	for i, source := range p.sources {
		if source.ref.QualifiedName() == "" {
			return planError("invalid_source", fmt.Sprintf("plan.sources[%d]", i), "source is zero")
		}
		name := source.ref.QualifiedName()
		if _, ok := seen[name]; ok {
			return planError("invalid_source", fmt.Sprintf("plan.sources[%d]", i), "duplicate source")
		}
		seen[name] = struct{}{}
	}
	allowed := seen
	for i, item := range p.projection {
		if item.bindErr != nil {
			return planError("unsnapshotable_bind", fmt.Sprintf("plan.projection[%d]", i), item.bindErr.Error())
		}
		if item.expression == nil {
			return planError("invalid_projection", fmt.Sprintf("plan.projection[%d]", i), "expression is zero")
		}
		if item.source != "" {
			if _, ok := allowed[item.source]; !ok {
				return planError("invalid_source", fmt.Sprintf("plan.projection[%d]", i), "expression source is outside plan")
			}
		}
	}
	for i, join := range p.joins {
		if join.On() == nil {
			return planError("invalid_source", fmt.Sprintf("plan.joins[%d].on", i), "condition is zero")
		}
		if join.Source().QualifiedName() == "" {
			return planError("invalid_source", fmt.Sprintf("plan.joins[%d].source", i), "source is zero")
		}
	}
	for i, predicate := range append(append([]Predicate(nil), p.where...), p.having...) {
		if predicate.node == nil {
			return planError("invalid_projection", fmt.Sprintf("plan.predicates[%d]", i), "predicate is zero")
		}
		if predicate.bindErr != nil {
			return planError("unsnapshotable_bind", fmt.Sprintf("plan.predicates[%d]", i), predicate.bindErr.Error())
		}
		if predicate.source != "" {
			if _, ok := allowed[predicate.source]; !ok {
				return planError("invalid_source", fmt.Sprintf("plan.predicates[%d]", i), "expression source is outside plan")
			}
		}
	}
	for i, key := range p.group {
		if key.node == nil {
			return planError("invalid_projection", fmt.Sprintf("plan.group[%d]", i), "group key is zero")
		}
		if key.source != "" {
			if _, ok := allowed[key.source]; !ok {
				return planError("invalid_source", fmt.Sprintf("plan.group[%d]", i), "expression source is outside plan")
			}
		}
	}
	for i, term := range p.order {
		if term.node == nil {
			return planError("invalid_projection", fmt.Sprintf("plan.order[%d]", i), "order term is zero")
		}
		if term.source != "" {
			if _, ok := allowed[term.source]; !ok {
				return planError("invalid_source", fmt.Sprintf("plan.order[%d]", i), "expression source is outside plan")
			}
		}
	}
	return nil
}
func (q Query[R]) Validate() error {
	if len(q.projection.items) == 0 || q.projection.decoder == nil {
		return planError("invalid_projection", "projection", "projection is zero")
	}
	if err := q.plan.Validate(); err != nil {
		return err
	}
	return nil
}

func Select[R any](from Source, projection Projection[R]) Query[R] {
	return Query[R]{plan: QueryPlan{sources: []Source{from}, projection: cloneItems(projection.items)}, projection: projection}
}
func Project[R any](base QueryPlan, projection Projection[R]) Query[R] {
	base.projection = cloneItems(projection.items)
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
	return p
}
func (q Query[R]) Where(p Predicate) Query[R] {
	q.plan = clonePlan(q.plan)
	q.plan.where = append(q.plan.where, p)
	return q
}
func (q Query[R]) Join(s Source, on Predicate) Query[R] {
	q.plan = clonePlan(q.plan)
	q.plan.sources = append(q.plan.sources, s)
	q.plan.joins = append(q.plan.joins, query.InnerJoin(s.ref, on.node))
	return q
}
func (q Query[R]) LeftJoin(s Source, on Predicate) Query[R] {
	q.plan = clonePlan(q.plan)
	q.plan.sources = append(q.plan.sources, s)
	q.plan.joins = append(q.plan.joins, query.LeftJoin(s.ref, on.node))
	return q
}
func (q Query[R]) GroupBy(keys ...GroupKey) Query[R] {
	q.plan = clonePlan(q.plan)
	q.plan.group = append(q.plan.group, keys...)
	return q
}
func (q Query[R]) Having(p Predicate) Query[R] {
	q.plan = clonePlan(q.plan)
	q.plan.having = append(q.plan.having, p)
	return q
}
func (q Query[R]) OrderBy(terms ...OrderTerm) Query[R] {
	q.plan = clonePlan(q.plan)
	q.plan.order = append(q.plan.order, terms...)
	return q
}
func (q Query[R]) Distinct() Query[R] { q.plan = clonePlan(q.plan); q.plan.distinct = true; return q }
func (q Query[R]) Limit(n int) (Query[R], error) {
	if n < 0 {
		return q, planError("invalid_projection", "limit", "must not be negative")
	}
	q.plan = clonePlan(q.plan)
	q.plan.limit = &n
	return q, nil
}
func (q Query[R]) Offset(n int) (Query[R], error) {
	if n < 0 {
		return q, planError("invalid_projection", "offset", "must not be negative")
	}
	q.plan = clonePlan(q.plan)
	q.plan.offset = &n
	return q, nil
}

type scalarDecoder[T any] struct{ schema ResultSchema }

func (d scalarDecoder[T]) ResultSchema() ResultSchema                   { return d.schema }
func (d scalarDecoder[T]) Presence() []Presence                         { return nil }
func (d scalarDecoder[T]) DecodeRow(source ScanSource, result *T) error { return source.Scan(result) }
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
	return NewProjection([]ProjectionItem{NullItem(name, value, logical, codec)}, scalarDecoder[Nullable[T]]{schema: resultSchema})
}
