package rasql

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/mutationcolumn"
	"github.com/lestrrat-go/rasql/internal/nilcheck"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
)

type mutationState uint8

const (
	mutationSet mutationState = iota + 1
	mutationClear
	mutationDefault
)

// MutationField is an opaque generated-plan field. Generated code should use
// SetField, SetNullableField, ClearField, and DefaultField to create one.
type MutationField[T any] struct {
	column query.ColumnRef
	codec  string
	state  mutationState
	value  any
}

type mutationColumn[R, V any] interface {
	RasqlMutationColumn() mutationcolumn.NonNull[R, V]
}

type mutationNullColumn[R, V any] interface {
	RasqlMutationNullColumn() mutationcolumn.Nullable[R, V]
}

func mutationColumnInfo[C any](column C) (query.ColumnRef, string) {
	switch value := any(column).(type) {
	case interface {
		mutationColumnRef() query.ColumnRef
		mutationColumnCodec() string
	}:
		return value.mutationColumnRef(), value.mutationColumnCodec()
	case interface{ mutationColumnRef() query.ColumnRef }:
		return value.mutationColumnRef(), ""
	case interface{ Ref() query.ColumnRef }:
		return value.Ref(), ""
	default:
		panic("rasql: unsupported mutation column")
	}
}

func SetField[T, V any, C mutationColumn[T, V]](column C, value V) MutationField[T] {
	ref, codec := mutationColumnInfo(column)
	return MutationField[T]{column: ref, codec: codec, state: mutationSet, value: value}
}

func SetNullableField[T, V any, C mutationNullColumn[T, V]](column C, value V) MutationField[T] {
	ref, codec := mutationColumnInfo(column)
	return MutationField[T]{column: ref, codec: codec, state: mutationSet, value: value}
}

func ClearField[T, V any, C mutationNullColumn[T, V]](column C) MutationField[T] {
	ref, codec := mutationColumnInfo(column)
	return MutationField[T]{column: ref, codec: codec, state: mutationClear}
}

func DefaultField[T, V any, C mutationColumn[T, V]](column C) MutationField[T] {
	ref, codec := mutationColumnInfo(column)
	return MutationField[T]{column: ref, codec: codec, state: mutationDefault}
}

func DefaultNullableField[T, V any, C mutationNullColumn[T, V]](column C) MutationField[T] {
	ref, codec := mutationColumnInfo(column)
	return MutationField[T]{column: ref, codec: codec, state: mutationDefault}
}

// CreatePlan is an immutable typed INSERT plan.
type CreatePlan[T any] struct {
	table  Table[T]
	fields []MutationField[T]
	err    error
}

// PatchPlan is an immutable typed UPDATE plan.
type PatchPlan[T any] struct {
	table   Table[T]
	fields  []MutationField[T]
	where   query.Predicate
	err     error
	version *versionMutation
}

type versionMutation struct {
	column   query.ColumnRef
	expected int64
}

func (p PatchPlan[T]) WithVersion(column Column[T, int64], expected int64) (PatchPlan[T], error) {
	if p.version != nil {
		return p, fmt.Errorf("rasql: version predicate is already configured")
	}
	if p.err != nil {
		return p, p.err
	}
	ref := column.ref
	definition := p.table.Ref().Definition()
	sourceTable, ok := ref.Source().Table()
	if !ok || !reflect.DeepEqual(sourceTable.Definition(), definition) {
		return p, fmt.Errorf("rasql: version column must belong to the patch table")
	}
	columnDef, ok := definition.Column(ref.Name())
	if !ok || ref.Source().Definition().QualifiedName() != definition.QualifiedName() {
		return p, fmt.Errorf("rasql: version column %q belongs to another table", ref.Name())
	}
	if _, integer := columnDef.Type.(schema.IntegerType); !integer || columnDef.Nullable || columnDef.GeneratedExpression != "" || columnDef.Identity != "" {
		return p, fmt.Errorf("rasql: version column %q must be a non-null ordinary integer", ref.Name())
	}
	for _, field := range p.fields {
		if field.column.Name() == ref.Name() {
			return p, fmt.Errorf("rasql: version column %q is already assigned", ref.Name())
		}
	}
	p.version = &versionMutation{column: p.table.Ref().Column(ref.Name()), expected: expected}
	return p, nil
}

type normalizedCreate[T any] struct {
	table       query.TableRef
	columns     []query.ColumnRef
	values      []any
	defaultOnly bool
}

func NewCreatePlan[T any](table Table[T], fields ...MutationField[T]) (CreatePlan[T], error) {
	plan := CreatePlan[T]{table: table, fields: append([]MutationField[T](nil), fields...)}
	plan.err = validateMutationPlan(table, plan.fields, false, query.Predicate{})
	if plan.err == nil {
		plan.err = validateCreateRequired(table, plan.fields)
	}
	return plan, plan.err
}

func validateCreateRequired[T any](table Table[T], fields []MutationField[T]) error {
	if isNilTable(table) {
		return nil
	}
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		seen[field.column.Name()] = struct{}{}
	}
	definition := table.Ref().Definition()
	for _, column := range definition.Columns {
		if _, ok := seen[column.Name]; ok || createColumnOmissible(definition, column) {
			continue
		}
		return fmt.Errorf("rasql: create plan is missing required column %q", column.Name)
	}
	return nil
}

func createColumnOmissible(definition schema.TableDef, column schema.ColumnDef) bool {
	if column.Default != "" || column.Nullable || column.Identity != "" || column.GeneratedExpression != "" {
		return true
	}
	if !definition.PrimaryKeyAutoincrement || len(definition.PrimaryKey) != 1 || definition.PrimaryKey[0] != column.Name {
		return false
	}
	_, integer := column.Type.(schema.IntegerType)
	return integer
}

func NewPatchPlan[T any](table Table[T], where query.Predicate, fields ...MutationField[T]) (PatchPlan[T], error) {
	plan := PatchPlan[T]{table: table, fields: append([]MutationField[T](nil), fields...), where: where}
	plan.err = validateMutationPlan(table, plan.fields, true, where)
	return plan, plan.err
}

func validateMutationPlan[T any](table Table[T], fields []MutationField[T], patch bool, where query.Predicate) error {
	if isNilTable(table) {
		return fmt.Errorf("rasql: mutation plan table must not be nil")
	}
	target := table.Ref()
	definition := target.Definition()
	if len(fields) == 0 {
		return fmt.Errorf("rasql: mutation plan requires at least one field")
	}
	if patch && nilcheck.Is(where.Expression()) {
		return fmt.Errorf("rasql: patch plan requires a predicate")
	}
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if field.state == 0 || field.column.Name() == "" || field.column.Source().QualifiedName() == "" {
			return fmt.Errorf("rasql: mutation plan contains a zero field")
		}
		column, ok := definition.Column(field.column.Name())
		if !ok || field.column.Source().Definition().QualifiedName() != definition.QualifiedName() {
			return fmt.Errorf("rasql: mutation field %q belongs to another table", field.column.Name())
		}
		if _, duplicate := seen[column.Name]; duplicate {
			return fmt.Errorf("rasql: mutation plan contains duplicate column %q", column.Name)
		}
		seen[column.Name] = struct{}{}
		if field.state == mutationDefault && patch {
			return fmt.Errorf("rasql: DEFAULT field %s is not supported in a patch", column.Name)
		}
		if column.GeneratedExpression != "" || column.Identity == schema.IdentityAlways {
			return fmt.Errorf("rasql: mutation field %q is not writable", column.Name)
		}
		if field.state == mutationClear && !column.Nullable {
			return fmt.Errorf("rasql: mutation field %q does not accept NULL", column.Name)
		}
	}
	return nil
}

func (p CreatePlan[T]) lower() (query.Insert, error) {
	lowered, err := p.lowerNormalized()
	if err != nil {
		return query.Insert{}, err
	}
	if lowered.defaultOnly {
		return query.NewInsert(lowered.table, query.Defaults())
	}
	return query.NewInsertRows(lowered.table, lowered.columns, [][]any{lowered.values})
}

func (p CreatePlan[T]) lowerNormalized() (normalizedCreate[T], error) {
	if p.err != nil {
		return normalizedCreate[T]{}, p.err
	}
	columns := p.table.Ref().Definition().Columns
	byName := make(map[string]MutationField[T], len(p.fields))
	for _, field := range p.fields {
		byName[field.column.Name()] = field
	}
	lowered := normalizedCreate[T]{table: p.table.Ref()}
	for _, column := range columns {
		field, ok := byName[column.Name]
		if !ok {
			if !createColumnOmissible(p.table.Ref().Definition(), column) {
				return normalizedCreate[T]{}, fmt.Errorf("rasql: create plan is missing required column %q", column.Name)
			}
			continue
		}
		if field.state == mutationDefault {
			continue
		}
		value := field.value
		if field.state == mutationClear {
			value = nil
		} else {
			bound, err := mutationBind(value, field.codec)
			if err != nil {
				return normalizedCreate[T]{}, err
			}
			value = bound
		}
		lowered.columns = append(lowered.columns, p.table.Ref().Column(column.Name))
		lowered.values = append(lowered.values, value)
	}
	lowered.defaultOnly = len(lowered.columns) == 0
	return lowered, nil
}

func isPrimaryKeyColumn(definition schema.TableDef, name string) bool {
	for _, key := range definition.PrimaryKey {
		if key == name {
			return true
		}
	}
	return false
}

func (p PatchPlan[T]) lower() (query.Update, error) {
	if p.err != nil {
		return query.Update{}, p.err
	}
	columns := p.table.Ref().Definition().Columns
	byName := make(map[string]MutationField[T], len(p.fields))
	for _, field := range p.fields {
		byName[field.column.Name()] = field
	}
	assignments := make([]query.Assignment, 0, len(p.fields))
	for _, column := range columns {
		field, ok := byName[column.Name]
		if !ok {
			continue
		}
		value := any(field.value)
		if field.state == mutationClear {
			value = query.Bind(nil)
		} else {
			bound, err := mutationBind(value, field.codec)
			if err != nil {
				return query.Update{}, err
			}
			value = bound
		}
		assignments = append(assignments, query.Set(p.table.Ref().Column(column.Name), value))
	}
	if p.version != nil {
		assignments = append(assignments, query.Set(p.version.column, query.Add(p.version.column, 1)))
	}
	statement, err := query.NewUpdate(p.table.Ref(), assignments...)
	if err != nil {
		return query.Update{}, err
	}
	where := p.where.Expression()
	if p.version != nil {
		where = query.And(where, query.Equal(p.version.column, p.version.expected))
	}
	return statement.WithWhere(where)
}

func returning[T any](table Table[T]) []query.Projection {
	columns := table.Ref().Definition().Columns
	result := make([]query.Projection, len(columns))
	for i, column := range columns {
		result[i] = table.Ref().Column(column.Name)
	}
	return result
}

func validateReturningDB(db DB) error {
	if err := db.Validate(); err != nil {
		return err
	}
	if !db.Dialect().Supports(dialect.CapabilityReturning) {
		return fmt.Errorf("rasql: dialect %s does not support RETURNING", db.Dialect().Name())
	}
	return nil
}

func ExecCreate[T any](ctx context.Context, db DB, plan CreatePlan[T]) (sql.Result, error) {
	statement, err := plan.lower()
	if err != nil {
		return nil, err
	}
	return Exec(ctx, db, statement)
}

func QueryCreate[T any](ctx context.Context, db DB, plan CreatePlan[T]) (T, error) {
	var zero T
	if plan.err != nil {
		return zero, plan.err
	}
	if err := validateReturningDB(db); err != nil {
		return zero, err
	}
	statement, err := plan.lower()
	if err != nil {
		return zero, err
	}
	statement, err = statement.WithReturning(returning(plan.table)...)
	if err != nil {
		return zero, err
	}
	return QueryWriteOne[T](ctx, db, statement)
}

func ExecPatch[T any](ctx context.Context, db DB, plan PatchPlan[T]) (sql.Result, error) {
	statement, err := plan.lower()
	if err != nil {
		return nil, err
	}
	return Exec(ctx, db, statement)
}

func QueryPatchAll[T any](ctx context.Context, db DB, plan PatchPlan[T]) ([]T, error) {
	if plan.err != nil {
		return nil, plan.err
	}
	if err := validateReturningDB(db); err != nil {
		return nil, err
	}
	statement, err := plan.lower()
	if err != nil {
		return nil, err
	}
	statement, err = statement.WithReturning(returning(plan.table)...)
	if err != nil {
		return nil, err
	}
	return QueryWriteAll[T](ctx, db, statement)
}

func QueryPatchOne[T any](ctx context.Context, db DB, plan PatchPlan[T]) (T, error) {
	var zero T
	if plan.err != nil {
		return zero, plan.err
	}
	if err := validateReturningDB(db); err != nil {
		return zero, err
	}
	statement, err := plan.lower()
	if err != nil {
		return zero, err
	}
	statement, err = statement.WithReturning(returning(plan.table)...)
	if err != nil {
		return zero, err
	}
	return QueryWriteOne[T](ctx, db, statement)
}

func mutationBind(value any, codec string) (query.Expression, error) {
	if codec == "" {
		return Value(value).node, nil
	}
	bound, err := ValueWithCodec(value, codec)
	if err != nil {
		return nil, err
	}
	return bound.node, nil
}
