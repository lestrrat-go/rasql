package rasql

import (
	"context"
	"fmt"
	"iter"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
)

// SelectFrom starts a typed fluent SELECT builder for table.
// It selects every table column by default so All and One can decode T.
func SelectFrom[T any](table Table[T]) TypedSelectBuilder[T] {
	if isNilTable(table) {
		return TypedSelectBuilder[T]{
			builder: render.SelectFrom(nil, query.TableRef{}),
			err:     fmt.Errorf("rasql: table must not be nil"),
		}
	}
	reference := table.Ref()
	definition := reference.Definition()
	columns := make([]string, len(definition.Columns))
	for index, column := range definition.Columns {
		columns[index] = column.Name
	}
	return TypedSelectBuilder[T]{
		builder:       render.SelectFrom(nil, reference).Select(columns...),
		staticScan:    true,
		resultColumns: tableResultColumns(definition.Columns),
	}
}

// DecodeFrom starts a typed fluent SELECT builder for a custom result shape.
// R is explicit and T is inferred from table. R's fields are mapped by their
// rasql tags, or by their snake-cased names when untagged; a row type
// carrying generated scan methods is filled through those instead.
func DecodeFrom[R any, T any](table Table[T]) TypedSelectBuilder[R] {
	if isNilTable(table) {
		return TypedSelectBuilder[R]{
			builder: render.SelectFrom(nil, query.TableRef{}),
			err:     fmt.Errorf("rasql: table must not be nil"),
		}
	}
	return TypedSelectBuilder[R]{builder: render.SelectFrom(nil, table.Ref())}
}

// DecodeFromRef starts a typed fluent SELECT builder for a table with no Go row
// type. R's fields are mapped by their rasql tags, or by their snake-cased
// names when untagged; a row type carrying generated scan methods is filled
// through those instead.
func DecodeFromRef[R any](table query.TableRef) TypedSelectBuilder[R] {
	return TypedSelectBuilder[R]{builder: render.SelectFrom(nil, table)}
}

// InnerJoin returns an INNER JOIN on table with on as its condition.
// It adapts a typed table for the dialect-neutral query API, which cannot
// import this package.
func InnerJoin[T any](table Table[T], on query.Expression) query.Join {
	if isNilTable(table) {
		return query.InnerJoin(query.TableRef{}, on)
	}
	return query.InnerJoin(table.Ref(), on)
}

// LeftJoin returns a LEFT JOIN on table with on as its condition.
// It adapts a typed table for the dialect-neutral query API, which cannot
// import this package.
func LeftJoin[T any](table Table[T], on query.Expression) query.Join {
	if isNilTable(table) {
		return query.LeftJoin(query.TableRef{}, on)
	}
	return query.LeftJoin(table.Ref(), on)
}

// TypedSelectBuilder builds a SELECT that decodes rows as T. It carries no
// handle and no dialect, so one builder can be assembled once and run against a
// DB and a transaction started from it alike.
type TypedSelectBuilder[T any] struct {
	builder       render.SelectBuilder
	resultColumns []query.ResultColumn
	// limit and hasLimit shadow the same state inside builder, which render
	// keeps unexported with no getter. All reads them for a collection
	// capacity. Limit is the only method that sets a limit; a second one would
	// have to set these too.
	limit     int
	hasLimit  bool
	hasOffset bool
	// err holds the first error this package rejects a builder with.
	// render.SelectBuilder carries an error of its own but exposes no way to
	// set one, so a nil table or an empty IN list is held here and checked by
	// Build before the render builder runs.
	err error
	// staticScan records that the builder owns the whole table projection, so
	// a generated row type can be scanned directly into its fields.
	staticScan bool
}

// Project adds projections created through the basic query API.
func (b TypedSelectBuilder[T]) Project(projections ...query.Projection) TypedSelectBuilder[T] {
	b = b.clone()
	b.builder = b.builder.Project(projections...)
	if len(projections) > 0 {
		b.staticScan = false
		b.resultColumns = nil
	}
	return b
}

// Join adds joins created through the basic query API.
func (b TypedSelectBuilder[T]) Join(joins ...query.Join) TypedSelectBuilder[T] {
	b = b.clone()
	b.builder = b.builder.Join(joins...)
	return b
}

// Where adds a predicate created through the basic query API.
// Repeated calls combine with AND in the order they were made. Use one call
// with query.Or for a top-level OR.
func (b TypedSelectBuilder[T]) Where(expression query.Expression) TypedSelectBuilder[T] {
	b = b.clone()
	b.builder = b.builder.Where(expression)
	return b
}

// WhereEqual adds an equality predicate for column and binds value.
// Repeated calls combine with AND in the order they were made, including calls to Where.
// Build and Query reject a column whose table is not part of the statement.
func (b TypedSelectBuilder[T]) WhereEqual(column query.ColumnRef, value any) TypedSelectBuilder[T] {
	b = b.clone()
	b.builder = b.builder.Where(query.Equal(column, value))
	return b
}

// WhereIn adds an IN predicate for column and binds each value.
// Repeated calls combine with AND in the order they were made, including calls to
// Where and WhereEqual. Build, Query, All, and One reject an empty value list,
// and reject a column whose table is not part of the statement.
func (b TypedSelectBuilder[T]) WhereIn(column query.ColumnRef, values ...any) TypedSelectBuilder[T] {
	b = b.clone()
	if len(values) == 0 {
		return b.withError(fmt.Errorf("rasql: IN requires at least one value"))
	}
	b.builder = b.builder.Where(query.In(column, values...))
	return b
}

// GroupBy adds grouping expressions created through the basic query API.
// Repeated calls append in the order they were made.
func (b TypedSelectBuilder[T]) GroupBy(expressions ...query.Expression) TypedSelectBuilder[T] {
	b = b.clone()
	b.builder = b.builder.GroupBy(expressions...)
	return b
}

// Having adds a grouped predicate created through the basic query API.
// Repeated calls combine with AND in the order they were made, exactly as Where
// does. Use one call with query.Or for a top-level OR.
func (b TypedSelectBuilder[T]) Having(expression query.Expression) TypedSelectBuilder[T] {
	b = b.clone()
	b.builder = b.builder.Having(expression)
	return b
}

// Order adds ordering created through the basic query API.
func (b TypedSelectBuilder[T]) Order(orders ...query.Order) TypedSelectBuilder[T] {
	b = b.clone()
	b.builder = b.builder.Order(orders...)
	return b
}

// OrderAsc adds ascending ordering for column.
func (b TypedSelectBuilder[T]) OrderAsc(column query.ColumnRef) TypedSelectBuilder[T] {
	b = b.clone()
	b.builder = b.builder.Order(query.Asc(column))
	return b
}

// OrderDesc adds descending ordering for column.
func (b TypedSelectBuilder[T]) OrderDesc(column query.ColumnRef) TypedSelectBuilder[T] {
	b = b.clone()
	b.builder = b.builder.Order(query.Desc(column))
	return b
}

// Distinct de-duplicates the result rows. SelectFrom projects every column of
// the table, including its primary key, which already makes each row unique,
// so Distinct is meaningful mainly beside a narrowed projection: DecodeFrom
// with Project, or dynamic.SelectBuilder's Select with specific column names.
func (b TypedSelectBuilder[T]) Distinct() TypedSelectBuilder[T] {
	b = b.clone()
	b.builder = b.builder.Distinct()
	return b
}

// Limit sets the maximum number of result rows.
func (b TypedSelectBuilder[T]) Limit(limit int) TypedSelectBuilder[T] {
	b = b.clone()
	b.builder = b.builder.Limit(limit)
	b.limit = limit
	b.hasLimit = true
	return b
}

// Offset sets the number of result rows to skip.
func (b TypedSelectBuilder[T]) Offset(offset int) TypedSelectBuilder[T] {
	b = b.clone()
	b.builder = b.builder.Offset(offset)
	b.hasOffset = true
	return b
}

// rowLimitHint reports the most rows the statement can return, or 0 when that
// is not bounded. An OFFSET shifts the result window rather than widening it,
// so it does not enter the hint.
func (b TypedSelectBuilder[T]) rowLimitHint() int {
	if !b.hasLimit || b.limit < 0 {
		return 0
	}
	return b.limit
}

// Build validates the statement and renders it for d without executing it.
func (b TypedSelectBuilder[T]) Build(d dialect.Dialect) (stmt.Statement, error) {
	if b.err != nil {
		return stmt.Statement{}, b.err
	}
	statement, err := b.Select()
	if err != nil {
		return stmt.Statement{}, err
	}
	if len(b.resultColumns) > 0 {
		if _, err := query.ResultOf(statement, b.resultColumns...); err != nil {
			return stmt.Statement{}, err
		}
	}
	return render.Select(d, statement)
}

func (b TypedSelectBuilder[T]) Select() (query.Select, error) {
	if b.err != nil {
		return query.Select{}, b.err
	}
	return b.builder.Query()
}

func (b TypedSelectBuilder[T]) Result(columns ...query.ResultColumn) (query.ResultQuery, error) {
	statement, err := b.Select()
	if err != nil {
		return query.ResultQuery{}, err
	}
	if len(columns) == 0 {
		columns = b.resultColumns
	}
	return query.ResultOf(statement, columns...)
}

func RebindResult[R any, T any](b TypedSelectBuilder[T], columns []query.ResultColumn, projections ...query.Projection) TypedSelectBuilder[R] {
	copy := b.clone()
	copy.builder = copy.builder.ReplaceProject(projections...)
	copy.resultColumns = append([]query.ResultColumn(nil), columns...)
	copy.staticScan = false
	return TypedSelectBuilder[R]{builder: copy.builder, resultColumns: copy.resultColumns, limit: copy.limit, hasLimit: copy.hasLimit, hasOffset: copy.hasOffset, err: copy.err, staticScan: false}
}

// Query returns a rangeable sequence that decodes each result row as T.
// It reports validation and rendering errors before iteration starts and yields
// an execution error instead of a row once iteration begins. The statement runs
// when the sequence is first ranged over, not when Query returns, so a sequence
// that is never ranged opens no cursor to leak.
func (b TypedSelectBuilder[T]) Query(ctx context.Context, db DB) (iter.Seq2[T, error], error) {
	if err := db.Validate(); err != nil {
		return nil, err
	}
	s, err := b.Build(db.Dialect())
	if err != nil {
		return nil, fmt.Errorf("rasql: render SELECT: %w", err)
	}
	if b.staticScan {
		return scanTypedRenderedStatic[T](ctx, db, s), nil
	}
	return scanTypedRendered[T](ctx, db, s), nil
}

// All collects every row from Query.
func (b TypedSelectBuilder[T]) All(ctx context.Context, db DB) ([]T, error) {
	rows, err := b.Query(ctx, db)
	if err != nil {
		return nil, err
	}
	return collectAll(rows, b.rowLimitHint())
}

// Count executes COUNT(*) over the rows the statement matches.
// It ignores the projections that decode T, ignores ordering, and reports an
// error when the builder sets a limit or an offset: count an unpaged builder,
// then page a copy of it for the rows.
// Like [TypedSelectBuilder.One], it reports [ErrNoRows] or [ErrMultipleRows]
// when the database returns anything other than the one row COUNT(*) produces.
func (b TypedSelectBuilder[T]) Count(ctx context.Context, db DB) (int64, error) {
	if err := db.Validate(); err != nil {
		return 0, err
	}
	if b.err != nil {
		return 0, b.err
	}
	s, err := b.countStatement(db.Dialect(), false)
	if err != nil {
		return 0, fmt.Errorf("rasql: render SELECT: %w", err)
	}
	// Count consumes the sequence itself, so the statement runs before Count
	// returns either way. It reads through the same static-scan path a
	// generated row type takes, so the counted value never becomes an any this
	// package owns.
	counted, err := exactlyOne(scanTypedRenderedStatic[countRow](ctx, db, s))
	if err != nil {
		return 0, err
	}
	return counted.Count, nil
}

// CountPage counts rows retained by the current page.
func (b TypedSelectBuilder[T]) CountPage(ctx context.Context, db DB) (int64, error) {
	if err := db.Validate(); err != nil {
		return 0, err
	}
	if b.err != nil {
		return 0, b.err
	}
	s, err := b.countStatement(db.Dialect(), true)
	if err != nil {
		return 0, fmt.Errorf("rasql: render SELECT: %w", err)
	}
	counted, err := exactlyOne(scanTypedRenderedStatic[countRow](ctx, db, s))
	if err != nil {
		return 0, err
	}
	return counted.Count, nil
}

func (b TypedSelectBuilder[T]) countStatement(d dialect.Dialect, keepPaging bool) (stmt.Statement, error) {
	statement, err := b.builder.WithDialect(d).QueryForCount(keepPaging)
	if err != nil {
		return stmt.Statement{}, err
	}
	if !keepPaging && !b.hasLimit && !b.hasOffset && !statement.Distinct() && len(statement.GroupBy()) == 0 && statement.Having() == nil {
		return b.builder.WithDialect(d).BuildCount()
	}
	metadata, err := resultMetadata(statement, b.resultColumns)
	if err != nil {
		return stmt.Statement{}, err
	}
	result, err := query.ResultOf(statement, metadata...)
	if err != nil {
		return stmt.Statement{}, err
	}
	relation, err := query.Derived(result, "count_source")
	if err != nil {
		return stmt.Statement{}, err
	}
	outer, err := query.NewSelect(relation, query.CountAll().As("count"))
	if err != nil {
		return stmt.Statement{}, err
	}
	return render.Select(d, outer)
}

func resultMetadata(statement query.Select, supplied []query.ResultColumn) ([]query.ResultColumn, error) {
	if len(supplied) > 0 {
		return append([]query.ResultColumn(nil), supplied...), nil
	}
	projections := statement.Projections()
	metadata := make([]query.ResultColumn, len(projections))
	for i, projection := range projections {
		column, ok := projection.(query.ColumnRef)
		if !ok {
			return nil, fmt.Errorf("count requires result metadata for projection %d; call Result or RebindResult", i)
		}
		for _, candidate := range column.Source().Columns() {
			if candidate.Name == column.Name() {
				metadata[i] = query.ResultColumn{Name: column.Name(), Type: candidate.Type, Nullable: candidate.Nullable}
				break
			}
		}
		if metadata[i].Type == nil {
			return nil, fmt.Errorf("count cannot derive metadata for projection %q; call Result or RebindResult", column.Name())
		}
	}
	return metadata, nil
}

// One returns exactly one row from Query.
// It returns [ErrNoRows] when the statement matched no rows and
// [ErrMultipleRows] when it matched more than one.
func (b TypedSelectBuilder[T]) One(ctx context.Context, db DB) (T, error) {
	var zero T
	rows, err := b.Query(ctx, db)
	if err != nil {
		return zero, err
	}
	return exactlyOne(rows)
}

func (b TypedSelectBuilder[T]) withError(err error) TypedSelectBuilder[T] {
	if b.err == nil {
		b.err = err
	}
	return b
}

func (b TypedSelectBuilder[T]) clone() TypedSelectBuilder[T] {
	b.resultColumns = append([]query.ResultColumn(nil), b.resultColumns...)
	return b
}

func tableResultColumns(columns []schema.ColumnDef) []query.ResultColumn {
	result := make([]query.ResultColumn, len(columns))
	for i, column := range columns {
		result[i] = query.ResultColumn{Name: column.Name, Type: column.Type, Nullable: column.Nullable}
	}
	return result
}

// countRow reads the single value a COUNT(*) statement returns. BuildCount
// projects it under the result name "count", and the statement has exactly one
// column, so the field order is the projection order and ScanRow is enough.
type countRow struct {
	Count int64
}

func (r *countRow) ScanRow(src ScanSource) error {
	return src.Scan(&r.Count)
}
