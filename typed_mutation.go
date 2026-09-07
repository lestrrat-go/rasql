package rasql

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
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
	state  mutationState
	value  any
}

func SetField[T, V any](column query.TypedColumn[T, V], value V) MutationField[T] {
	return MutationField[T]{column: column.Ref(), state: mutationSet, value: value}
}

func SetNullableField[T, V any](column query.NullableColumn[T, V], value V) MutationField[T] {
	return MutationField[T]{column: column.Ref(), state: mutationSet, value: value}
}

func ClearField[T, V any](column query.NullableColumn[T, V]) MutationField[T] {
	return MutationField[T]{column: column.Ref(), state: mutationClear}
}

func DefaultField[T, V any](column query.TypedColumn[T, V]) MutationField[T] {
	return MutationField[T]{column: column.Ref(), state: mutationDefault}
}

func DefaultNullableField[T, V any](column query.NullableColumn[T, V]) MutationField[T] {
	return MutationField[T]{column: column.Ref(), state: mutationDefault}
}

// CreatePlan is an immutable typed INSERT plan.
type CreatePlan[T any] struct {
	table  Table[T]
	fields []MutationField[T]
	err    error
}

// PatchPlan is an immutable typed UPDATE plan.
type PatchPlan[T any] struct {
	table  Table[T]
	fields []MutationField[T]
	where  query.Predicate
	err    error
}

func NewCreatePlan[T any](table Table[T], fields ...MutationField[T]) (CreatePlan[T], error) {
	plan := CreatePlan[T]{table: table, fields: append([]MutationField[T](nil), fields...)}
	plan.err = validateMutationPlan(table, plan.fields, false, query.Predicate{})
	return plan, plan.err
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
	}
	return nil
}

func (p CreatePlan[T]) lower() (query.Insert, error) {
	if p.err != nil {
		return query.Insert{}, p.err
	}
	columns := p.table.Ref().Definition().Columns
	byName := make(map[string]MutationField[T], len(p.fields))
	for _, field := range p.fields {
		byName[field.column.Name()] = field
	}
	values := make([]query.InsertValues, 0, len(p.fields))
	for _, column := range columns {
		field, ok := byName[column.Name]
		if !ok || field.state == mutationDefault {
			continue
		}
		value := field.value
		if field.state == mutationClear {
			value = query.Bind(nil)
		}
		values = append(values, query.Set(p.table.Ref().Column(column.Name), value))
	}
	if len(values) == 0 {
		values = append(values, query.Defaults())
	}
	return query.NewInsert(p.table.Ref(), values...)
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
		value := field.value
		if field.state == mutationClear {
			value = query.Bind(nil)
		}
		assignments = append(assignments, query.Set(p.table.Ref().Column(column.Name), value))
	}
	statement, err := query.NewUpdate(p.table.Ref(), assignments...)
	if err != nil {
		return query.Update{}, err
	}
	return statement.WithWhere(p.where.Expression())
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
