package rasql

import (
	"database/sql/driver"

	"github.com/lestrrat-go/rasql/internal/bindplan"
	"github.com/lestrrat-go/rasql/internal/graphkey"
	"github.com/lestrrat-go/rasql/internal/mutationcolumn"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
)

type Expr[T any] struct {
	node    query.Expression
	codec   string
	source  string
	bindErr error
}
type BindSnapshotter[T any] interface{ SnapshotBind() (T, error) }
type NullExpr[T any] struct {
	node    query.Expression
	codec   string
	source  string
	bindErr error
}
type Predicate struct {
	node    query.Expression
	source  string
	source2 string
	bindErr error
}
type Column[Row, T any] struct {
	ref   query.ColumnRef
	codec string
}
type NullColumn[Row, T any] struct {
	ref   query.ColumnRef
	codec string
}

func (c Column[Row, T]) RasqlMutationColumn() mutationcolumn.NonNull[Row, T] {
	return mutationcolumn.NonNull[Row, T]{}
}
func (c Column[Row, T]) mutationColumnRef() query.ColumnRef { return c.ref }
func (c Column[Row, T]) mutationColumnCodec() string        { return c.codec }
func (c NullColumn[Row, T]) RasqlMutationNullColumn() mutationcolumn.Nullable[Row, T] {
	return mutationcolumn.Nullable[Row, T]{}
}
func (c NullColumn[Row, T]) mutationColumnRef() query.ColumnRef { return c.ref }
func (c NullColumn[Row, T]) mutationColumnCodec() string        { return c.codec }

// BindResultColumn binds a non-null column exposed by a typed derived source.
func BindResultColumn[R, T any](source TypedSource[R], name string) (Column[R, T], error) {
	for _, column := range source.source.ref.Columns() {
		if column.Name == name {
			if column.Nullable {
				return Column[R, T]{}, planError("invalid_source", "column", "nullability does not match handle")
			}
			return bindColumn[R, T](source.source, name, column.Codec, false)
		}
	}
	return Column[R, T]{}, planError("invalid_source", "column", "column is not a member of source")
}

// BindNullResultColumn binds a nullable column exposed by a typed derived source.
func BindNullResultColumn[R, T any](source TypedSource[R], name string) (NullColumn[R, T], error) {
	if err := validateBoundColumn(source.source, name, ""); err != nil {
		return NullColumn[R, T]{}, err
	}
	for _, column := range source.source.ref.Columns() {
		if column.Name == name {
			if !column.Nullable {
				return NullColumn[R, T]{}, planError("invalid_source", "column", "nullability does not match handle")
			}
			return NullColumn[R, T]{ref: source.source.ref.Column(name), codec: column.Codec}, nil
		}
	}
	return NullColumn[R, T]{}, planError("invalid_source", "column", "column is not a member of source")
}

func BindColumn[Row, T any](relation TypedRelation[Row], name, codec string) (Column[Row, T], error) {
	return bindColumn[Row, T](relation.source, name, codec, false)
}
func BindNullColumn[Row, T any](relation TypedRelation[Row], name, codec string) (NullColumn[Row, T], error) {
	if err := validateBoundColumn(relation.source, name, codec); err != nil {
		return NullColumn[Row, T]{}, err
	}
	for _, column := range relation.source.ref.Columns() {
		if column.Name == name {
			if !column.Nullable {
				return NullColumn[Row, T]{}, planError("invalid_source", "column", "nullability does not match handle")
			}
			return NullColumn[Row, T]{ref: relation.source.ref.Column(name), codec: codec}, nil
		}
	}
	return NullColumn[Row, T]{}, planError("invalid_source", "column", "column is not a member of source")
}
func BindOptionalColumn[Row, T any](relation OptionalRelation[Row], name, codec string) (NullColumn[Row, T], error) {
	if relation.source.ref.QualifiedName() == "" {
		return NullColumn[Row, T]{}, planError("invalid_source", "relation", "source is zero")
	}
	if err := validateBoundColumn(relation.source, name, codec); err != nil {
		return NullColumn[Row, T]{}, err
	}
	return NullColumn[Row, T]{ref: relation.source.ref.Column(name), codec: codec}, nil
}
func bindColumn[Row, T any](relation Source, name, codec string, nullable bool) (Column[Row, T], error) {
	if err := validateBoundColumn(relation, name, codec); err != nil {
		return Column[Row, T]{}, err
	}
	for _, column := range relation.ref.Columns() {
		if column.Name == name {
			if column.Nullable != nullable {
				return Column[Row, T]{}, planError("invalid_source", "column", "nullability does not match handle")
			}
			return Column[Row, T]{ref: relation.ref.Column(name), codec: codec}, nil
		}
	}
	return Column[Row, T]{}, planError("invalid_source", "column", "column is not a member of source")
}

// BindTypedColumn bridges a generated store accessor's query.TypedColumn
// into the typed Expr/Column layer. BindColumn names the same column as a
// string, which lets a renamed column pass silently instead of failing the
// build; a generated accessor already carries a ColumnRef the compiler
// checks at every call site, and this is the entry point that lets it be
// used as-is instead of falling back to that weaker string form.
func BindTypedColumn[Row, T any](column query.TypedColumn[Row, T]) (Column[Row, T], error) {
	ref := column.Ref()
	definition, err := lookupBoundColumn(ref)
	if err != nil {
		return Column[Row, T]{}, err
	}
	if definition.Nullable {
		return Column[Row, T]{}, planError("invalid_source", "column", "nullability does not match handle")
	}
	return Column[Row, T]{ref: ref, codec: definition.Codec}, nil
}

// BindNullTypedColumn is BindTypedColumn for a nullable column, bridging the
// query.NullableColumn a generated accessor returns for one.
func BindNullTypedColumn[Row, T any](column query.NullableColumn[Row, T]) (NullColumn[Row, T], error) {
	ref := column.Ref()
	definition, err := lookupBoundColumn(ref)
	if err != nil {
		return NullColumn[Row, T]{}, err
	}
	if !definition.Nullable {
		return NullColumn[Row, T]{}, planError("invalid_source", "column", "nullability does not match handle")
	}
	return NullColumn[Row, T]{ref: ref, codec: definition.Codec}, nil
}

// lookupBoundColumn finds ref's column definition in the schema its source
// carries, the same lookup validateBoundColumn does for a column named by
// string. BindTypedColumn and BindNullTypedColumn use it to recover the
// codec a query.TypedColumn does not carry itself, since that type exists to
// be checked by the compiler rather than to describe how its value is
// encoded.
func lookupBoundColumn(ref query.ColumnRef) (ResultColumn, error) {
	if ref.Source().QualifiedName() == "" {
		return ResultColumn{}, planError("invalid_source", "relation", "source is zero")
	}
	for _, column := range ref.Source().Columns() {
		if column.Name == ref.Name() {
			return column, nil
		}
	}
	return ResultColumn{}, planError("invalid_source", "column", "column is not a member of source")
}

func validateBoundColumn(relation Source, name, codec string) error {
	if relation.ref.QualifiedName() == "" {
		return planError("invalid_source", "relation", "source is zero")
	}
	if err := schema.ValidateIdentifier(name); err != nil {
		return planError("invalid_source", "column", err.Error())
	}
	if codec != "" && !codecPattern.MatchString(codec) {
		return planError("invalid_schema", "codec", "malformed codec identifier")
	}
	for _, column := range relation.ref.Columns() {
		if column.Name == name {
			if err := schema.ValidateColumnType(column.Type); err != nil {
				return planError("invalid_schema", "column.type", err.Error())
			}
			return nil
		}
	}
	return planError("invalid_source", "column", "column is not a member of source")
}

// The bind machinery lives in internal/bindplan so that package can raise the
// same errors and be tested on its own. These names stay so the rest of this
// package reads as before.
type bindID = bindplan.ID
type bindValueCopy = bindplan.ValueCopy
type bindToken = bindplan.Token

func adoptBind[T any](value T, allowSnapshotter bool) (any, bindValueCopy, error) {
	return bindplan.Adopt(value, allowSnapshotter)
}

func graphEncodedBind(value driver.Value, codec string) (query.Expression, error) {
	if codec != "" && !codecPattern.MatchString(codec) {
		return nil, planError("internal_plan", "bind", "malformed codec identifier")
	}
	if err := validateDriverValue(value); err != nil {
		return nil, planError("internal_plan", "bind", err.Error())
	}
	id := bindplan.NextID()
	snapshot, copier, err := adoptBind(value, false)
	if err != nil {
		return nil, err
	}
	return query.Bind(bindToken{ID: id, Value: snapshot, Codec: codec, Copy: copier, PreEncoded: true}), nil
}

func validateDriverValue(value driver.Value) error { return graphkey.ValidateDriverValue(value) }

func Value[T any](value T) Expr[T] {
	id := bindplan.NextID()
	snapshot, copier, err := adoptBind(value, true)
	return Expr[T]{node: query.Bind(bindToken{ID: id, Value: snapshot, Copy: copier, Err: err}), bindErr: err}
}
func ValueWithCodec[T any](value T, codec string) (Expr[T], error) {
	if codec != "" && !codecPattern.MatchString(codec) {
		return Expr[T]{}, planError("invalid_schema", "codec", "malformed codec identifier")
	}
	id := bindplan.NextID()
	snapshot, copier, err := adoptBind(value, true)
	if err != nil {
		return Expr[T]{}, err
	}
	return Expr[T]{node: query.Bind(bindToken{ID: id, Value: snapshot, Codec: codec, Copy: copier}), codec: codec}, nil
}
func EqualExpr[T comparable](left, right Expr[T]) Predicate {
	return Predicate{node: query.Equal(left.node, right.node), source: left.source, source2: right.source}
}
func EqualValue[T comparable](left Expr[T], right T) Predicate {
	id := bindplan.NextID()
	snapshot, copier, err := adoptBind(right, true)
	return Predicate{node: query.Equal(left.node, query.Bind(bindToken{ID: id, Value: snapshot, Codec: left.codec, Copy: copier, Err: err})), source: left.source, bindErr: err}
}
func EqualNullable[T comparable](left, right NullExpr[T]) Predicate {
	return Predicate{node: query.Equal(left.node, right.node), source: left.source, source2: right.source}
}
func EqualOptional[T comparable](left Expr[T], right NullExpr[T]) Predicate {
	return Predicate{node: query.Equal(left.node, right.node), source: left.source, source2: right.source}
}
func IsNull[T any](value NullExpr[T]) Predicate {
	return Predicate{node: query.IsNull(value.node), source: value.source}
}
func IsNotNull[T any](value NullExpr[T]) Predicate {
	return Predicate{node: query.IsNotNull(value.node), source: value.source}
}
func And(predicates ...Predicate) Predicate {
	nodes := make([]query.Expression, len(predicates))
	for i, p := range predicates {
		nodes[i] = p.node
	}
	return Predicate{node: query.And(nodes...)}
}
func Or(predicates ...Predicate) Predicate {
	nodes := make([]query.Expression, len(predicates))
	for i, p := range predicates {
		nodes[i] = p.node
	}
	return Predicate{node: query.Or(nodes...)}
}
func Not(predicate Predicate) Predicate { return Predicate{node: query.Negate(predicate.node)} }

func (c Column[Row, T]) Expr() Expr[T] {
	return Expr[T]{node: c.ref, codec: c.codec, source: c.ref.Source().QualifiedName()}
}
func (c NullColumn[Row, T]) NullExpr() NullExpr[T] {
	return NullExpr[T]{node: c.ref, codec: c.codec, source: c.ref.Source().QualifiedName()}
}

type GroupKey struct {
	node   query.Expression
	source string
}

func Group[T any](value Expr[T]) GroupKey { return GroupKey{node: value.node, source: value.source} }
func GroupNull[T any](value NullExpr[T]) GroupKey {
	return GroupKey{node: value.node, source: value.source}
}

type NullOrder uint8

const (
	NullOrderDefault NullOrder = iota
	NullsFirst
	NullsLast
)

type OrderTerm struct {
	node       query.Expression
	source     string
	descending bool
	nulls      NullOrder
	// result is set by AscResult and DescResult, and names a projection of
	// this query rather than an expression to recompute. A term carrying one
	// has no node, which is what the keyset and partition-limit paths already
	// refuse: paging needs the expression itself to build its comparison.
	result *ProjectionItem
}

func AscExpr[T any](value Expr[T]) OrderTerm {
	return OrderTerm{node: value.node, source: value.source}
}
func DescExpr[T any](value Expr[T]) OrderTerm {
	return OrderTerm{node: value.node, source: value.source, descending: true}
}
func AscNull[T any](value NullExpr[T], nulls NullOrder) OrderTerm {
	return OrderTerm{node: value.node, source: value.source, nulls: nulls}
}
func DescNull[T any](value NullExpr[T], nulls NullOrder) OrderTerm {
	return OrderTerm{node: value.node, source: value.source, descending: true, nulls: nulls}
}

func CountRows() Expr[int64]                     { return Expr[int64]{node: query.CountAll()} }
func CountExpr[T any](value Expr[T]) Expr[int64] { return Expr[int64]{node: query.Count(value.node)} }
func CountNullExpr[T any](value NullExpr[T]) Expr[int64] {
	return Expr[int64]{node: query.Count(value.node)}
}
func MinExpr[T any](value Expr[T]) NullExpr[T] {
	return NullExpr[T]{node: query.Min(value.node), codec: value.codec, source: value.source}
}
func MinNullExpr[T any](value NullExpr[T]) NullExpr[T] {
	return NullExpr[T]{node: query.Min(value.node), codec: value.codec, source: value.source}
}
