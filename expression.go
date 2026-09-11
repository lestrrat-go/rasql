package rasql

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"sync/atomic"
	"time"

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

var nextBindID uint64

type bindID uint64
type bindValueCopy func() (any, error)
type bindToken struct {
	id         bindID
	value      any
	codec      string
	err        error
	copy       bindValueCopy
	preEncoded bool
}

func graphEncodedBind(value driver.Value, codec string) (query.Expression, error) {
	if codec != "" && !codecPattern.MatchString(codec) {
		return nil, planError("internal_plan", "bind", "malformed codec identifier")
	}
	if err := validateDriverValue(value); err != nil {
		return nil, planError("internal_plan", "bind", err.Error())
	}
	id := bindID(atomic.AddUint64(&nextBindID, 1))
	snapshot, copier, err := adoptBind(value, false)
	if err != nil {
		return nil, err
	}
	return query.Bind(bindToken{id: id, value: snapshot, codec: codec, copy: copier, preEncoded: true}), nil
}

func validateDriverValue(value driver.Value) error { return graphkey.ValidateDriverValue(value) }

func Value[T any](value T) Expr[T] {
	id := bindID(atomic.AddUint64(&nextBindID, 1))
	snapshot, copier, err := adoptBind(value, true)
	return Expr[T]{node: query.Bind(bindToken{id: id, value: snapshot, copy: copier, err: err}), bindErr: err}
}
func ValueWithCodec[T any](value T, codec string) (Expr[T], error) {
	if codec != "" && !codecPattern.MatchString(codec) {
		return Expr[T]{}, planError("invalid_schema", "codec", "malformed codec identifier")
	}
	id := bindID(atomic.AddUint64(&nextBindID, 1))
	snapshot, copier, err := adoptBind(value, true)
	if err != nil {
		return Expr[T]{}, err
	}
	return Expr[T]{node: query.Bind(bindToken{id: id, value: snapshot, codec: codec, copy: copier}), codec: codec}, nil
}
func EqualExpr[T comparable](left, right Expr[T]) Predicate {
	return Predicate{node: query.Equal(left.node, right.node), source: left.source, source2: right.source}
}
func EqualValue[T comparable](left Expr[T], right T) Predicate {
	id := bindID(atomic.AddUint64(&nextBindID, 1))
	snapshot, copier, err := adoptBind(right, true)
	return Predicate{node: query.Equal(left.node, query.Bind(bindToken{id: id, value: snapshot, codec: left.codec, copy: copier, err: err})), source: left.source, bindErr: err}
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

type snapshotIdentity struct {
	typ  reflect.Type
	kind reflect.Kind
	ptr  uintptr
	len  int
	cap  int
}

func snapshotError(err error) error {
	if err == nil {
		return nil
	}
	var planErr *PlanError
	if errors.As(err, &planErr) && planErr.Code == "unsnapshotable_bind" {
		return err
	}
	return &PlanError{Code: "unsnapshotable_bind", Path: "bind", Detail: err.Error(), cause: err}
}

func adoptBind[T any](value T, allowSnapshotter bool) (any, bindValueCopy, error) {
	owned, copier, err := adoptBindValue(reflect.ValueOf(value), make(map[snapshotIdentity]bool), allowSnapshotter)
	if err != nil {
		return nil, nil, err
	}
	if !owned.IsValid() {
		return nil, func() (any, error) { return nil, nil }, nil
	}
	return owned.Interface(), func() (any, error) {
		copy, err := copier()
		if err != nil {
			return nil, err
		}
		return copy.Interface(), nil
	}, nil
}

func adoptBindValue(value reflect.Value, active map[snapshotIdentity]bool, allowSnapshotter bool) (reflect.Value, func() (reflect.Value, error), error) {
	if !value.IsValid() {
		return value, func() (reflect.Value, error) { return value, nil }, nil
	}
	if allowSnapshotter {
		if snapshot, ok := snapshotMethod(value); ok {
			adopted, err := snapshot()
			if err != nil {
				return reflect.Value{}, nil, err
			}
			owned := reflect.ValueOf(adopted)
			return owned, func() (reflect.Value, error) { return owned, nil }, nil
		}
	}
	if value.Type() == reflect.TypeOf(time.Time{}) || value.Kind() == reflect.Bool || value.Kind() >= reflect.Int && value.Kind() <= reflect.Float64 || value.Kind() == reflect.String {
		owned := reflect.New(value.Type()).Elem()
		owned.Set(value)
		return owned, func() (reflect.Value, error) { return owned, nil }, nil
	}
	if value.Kind() == reflect.Func || value.Kind() == reflect.Chan || value.Kind() == reflect.UnsafePointer {
		return reflect.Value{}, nil, planError("unsnapshotable_bind", "bind", "mutable value is unsupported")
	}
	if value.Kind() == reflect.Pointer || value.Kind() == reflect.Map || value.Kind() == reflect.Slice {
		if value.IsNil() {
			zero := reflect.Zero(value.Type())
			return zero, func() (reflect.Value, error) { return zero, nil }, nil
		}
		key := snapshotKey(value)
		if active[key] {
			return reflect.Value{}, nil, planError("unsnapshotable_bind", "bind", "cycle detected")
		}
		active[key] = true
		defer delete(active, key)
	}
	assign := func(dst, src reflect.Value) error {
		if !src.IsValid() {
			dst.Set(reflect.Zero(dst.Type()))
			return nil
		}
		if src.Type().AssignableTo(dst.Type()) {
			dst.Set(src)
			return nil
		}
		if src.Type().ConvertibleTo(dst.Type()) {
			dst.Set(src.Convert(dst.Type()))
			return nil
		}
		return planError("unsnapshotable_bind", "bind", "incompatible adopted value")
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			zero := reflect.Zero(value.Type())
			return zero, func() (reflect.Value, error) { return zero, nil }, nil
		}
		child, childCopy, err := adoptBindValue(value.Elem(), active, allowSnapshotter)
		if err != nil {
			return reflect.Value{}, nil, err
		}
		owned := reflect.New(value.Type()).Elem()
		if err := assign(owned, child); err != nil {
			return reflect.Value{}, nil, err
		}
		return owned, func() (reflect.Value, error) {
			fresh := reflect.New(value.Type()).Elem()
			child, err := childCopy()
			if err != nil {
				return reflect.Value{}, err
			}
			if err := assign(fresh, child); err != nil {
				return reflect.Value{}, err
			}
			return fresh, nil
		}, nil
	case reflect.Pointer:
		child, childCopy, err := adoptBindValue(value.Elem(), active, allowSnapshotter)
		if err != nil {
			return reflect.Value{}, nil, err
		}
		owned := reflect.New(value.Type().Elem())
		if err := assign(owned.Elem(), child); err != nil {
			return reflect.Value{}, nil, err
		}
		return owned, func() (reflect.Value, error) {
			fresh := reflect.New(value.Type().Elem())
			child, err := childCopy()
			if err != nil {
				return reflect.Value{}, err
			}
			if err := assign(fresh.Elem(), child); err != nil {
				return reflect.Value{}, err
			}
			return fresh, nil
		}, nil
	case reflect.Slice:
		children := make([]func() (reflect.Value, error), value.Len())
		owned := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := 0; i < value.Len(); i++ {
			child, childCopy, err := adoptBindValue(value.Index(i), active, allowSnapshotter)
			if err != nil {
				return reflect.Value{}, nil, err
			}
			if err := assign(owned.Index(i), child); err != nil {
				return reflect.Value{}, nil, err
			}
			children[i] = childCopy
		}
		return owned, func() (reflect.Value, error) {
			fresh := reflect.MakeSlice(value.Type(), len(children), len(children))
			for i, childCopy := range children {
				child, err := childCopy()
				if err != nil {
					return reflect.Value{}, err
				}
				if err := assign(fresh.Index(i), child); err != nil {
					return reflect.Value{}, err
				}
			}
			return fresh, nil
		}, nil
	case reflect.Array:
		children := make([]func() (reflect.Value, error), value.Len())
		owned := reflect.New(value.Type()).Elem()
		for i := 0; i < value.Len(); i++ {
			child, childCopy, err := adoptBindValue(value.Index(i), active, allowSnapshotter)
			if err != nil {
				return reflect.Value{}, nil, err
			}
			if err := assign(owned.Index(i), child); err != nil {
				return reflect.Value{}, nil, err
			}
			children[i] = childCopy
		}
		return owned, func() (reflect.Value, error) {
			fresh := reflect.New(value.Type()).Elem()
			for i, childCopy := range children {
				child, err := childCopy()
				if err != nil {
					return reflect.Value{}, err
				}
				if err := assign(fresh.Index(i), child); err != nil {
					return reflect.Value{}, err
				}
			}
			return fresh, nil
		}, nil
	case reflect.Map:
		type mapEntry struct {
			key  reflect.Value
			copy func() (reflect.Value, error)
		}
		entries := make([]mapEntry, 0, value.Len())
		owned := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			if err := validateSnapshotMapKey(iter.Key()); err != nil {
				return reflect.Value{}, nil, err
			}
			child, childCopy, err := adoptBindValue(iter.Value(), active, allowSnapshotter)
			if err != nil {
				return reflect.Value{}, nil, err
			}
			if err := assignMapValue(owned, iter.Key(), child); err != nil {
				return reflect.Value{}, nil, err
			}
			entries = append(entries, mapEntry{key: iter.Key(), copy: childCopy})
		}
		return owned, func() (reflect.Value, error) {
			fresh := reflect.MakeMapWithSize(value.Type(), len(entries))
			for _, entry := range entries {
				child, err := entry.copy()
				if err != nil {
					return reflect.Value{}, err
				}
				if err := assignMapValue(fresh, entry.key, child); err != nil {
					return reflect.Value{}, err
				}
			}
			return fresh, nil
		}, nil
	case reflect.Struct:
		if value.Type() == reflect.TypeOf(sql.NamedArg{}) {
			child, childCopy, err := adoptBindValue(reflect.ValueOf(value.Interface().(sql.NamedArg).Value), active, allowSnapshotter)
			if err != nil {
				return reflect.Value{}, nil, err
			}
			owned := reflect.New(value.Type()).Elem()
			owned.FieldByName("Name").SetString(value.FieldByName("Name").String())
			if err := assign(owned.FieldByName("Value"), child); err != nil {
				return reflect.Value{}, nil, err
			}
			return owned, func() (reflect.Value, error) {
				fresh := reflect.New(value.Type()).Elem()
				fresh.FieldByName("Name").SetString(value.FieldByName("Name").String())
				child, err := childCopy()
				if err != nil {
					return reflect.Value{}, err
				}
				if err := assign(fresh.FieldByName("Value"), child); err != nil {
					return reflect.Value{}, err
				}
				return fresh, nil
			}, nil
		}
		children := make([]func() (reflect.Value, error), value.NumField())
		owned := reflect.New(value.Type()).Elem()
		for i := 0; i < value.NumField(); i++ {
			if value.Type().Field(i).PkgPath != "" {
				return reflect.Value{}, nil, planError("unsnapshotable_bind", "bind", "unexported field")
			}
			child, childCopy, err := adoptBindValue(value.Field(i), active, allowSnapshotter)
			if err != nil {
				return reflect.Value{}, nil, err
			}
			if err := assign(owned.Field(i), child); err != nil {
				return reflect.Value{}, nil, err
			}
			children[i] = childCopy
		}
		return owned, func() (reflect.Value, error) {
			fresh := reflect.New(value.Type()).Elem()
			for i, childCopy := range children {
				child, err := childCopy()
				if err != nil {
					return reflect.Value{}, err
				}
				if err := assign(fresh.Field(i), child); err != nil {
					return reflect.Value{}, err
				}
			}
			return fresh, nil
		}, nil
	default:
		owned := reflect.New(value.Type()).Elem()
		owned.Set(value)
		return owned, func() (reflect.Value, error) { return owned, nil }, nil
	}
}

func snapshotKey(value reflect.Value) snapshotIdentity {
	key := snapshotIdentity{typ: value.Type(), kind: value.Kind(), ptr: value.Pointer()}
	if value.Kind() == reflect.Slice {
		key.len, key.cap = value.Len(), value.Cap()
	}
	return key
}

func snapshotMethod(value reflect.Value) (func() (any, error), bool) {
	method := value.MethodByName("SnapshotBind")
	if !method.IsValid() {
		return nil, false
	}
	methodType := method.Type()
	if methodType.NumIn() != 0 || methodType.NumOut() != 2 || methodType.Out(1) != reflect.TypeOf((*error)(nil)).Elem() || methodType.Out(0) != value.Type() {
		return nil, false
	}
	return func() (any, error) {
		results := method.Call(nil)
		if !results[1].IsNil() {
			return nil, snapshotError(results[1].Interface().(error))
		}
		return results[0].Interface(), nil
	}, true
}

func validateSnapshotMapKey(value reflect.Value) error {
	if !value.IsValid() {
		return nil
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return planError("unsnapshotable_bind", "bind", "mutable map key")
	case reflect.Array:
		for i := 0; i < value.Len(); i++ {
			if err := validateSnapshotMapKey(value.Index(i)); err != nil {
				return err
			}
		}
	case reflect.Struct:
		if value.Type() == reflect.TypeOf(time.Time{}) {
			return nil
		}
		for i := 0; i < value.NumField(); i++ {
			if value.Type().Field(i).PkgPath != "" {
				return planError("unsnapshotable_bind", "bind", "mutable map key")
			}
			if err := validateSnapshotMapKey(value.Field(i)); err != nil {
				return err
			}
		}
	}
	return nil
}
func assignMapValue(dst, key, value reflect.Value) error {
	if !value.IsValid() {
		value = reflect.Zero(dst.Type().Elem())
	}
	if !value.Type().AssignableTo(dst.Type().Elem()) {
		if !value.Type().ConvertibleTo(dst.Type().Elem()) {
			return planError("unsnapshotable_bind", "bind", "incompatible adopted value")
		}
		value = value.Convert(dst.Type().Elem())
	}
	dst.SetMapIndex(key, value)
	return nil
}
