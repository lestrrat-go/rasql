package query

import "fmt"

// WriteStatement is a validated statement that changes database rows.
type WriteStatement interface {
	Validate() error
	writeStatement()
}

// Assignment sets column to expression in an UPDATE statement.
type Assignment struct {
	column Column
	value  Expression
}

// Set assigns value to column.
func Set(column Column, value Expression) Assignment {
	return Assignment{column: column, value: value}
}

// Column returns the assigned column.
func (a Assignment) Column() Column {
	return a.column
}

// Value returns the assigned expression.
func (a Assignment) Value() Expression {
	return a.value
}

// Insert is an immutable single-row INSERT statement.
type Insert struct {
	into      TableRef
	columns   []Column
	values    []Expression
	returning []Projection
}

func (Insert) writeStatement() {}

// NewInsert creates a validated INSERT statement.
func NewInsert(into TableRef, columns []Column, values []Expression) (Insert, error) {
	statement := Insert{
		into:    into,
		columns: append([]Column(nil), columns...),
		values:  append([]Expression(nil), values...),
	}
	if err := statement.Validate(); err != nil {
		return Insert{}, err
	}
	return statement, nil
}

// WithReturning returns a copy of s with returning projections.
func (s Insert) WithReturning(projections ...Projection) (Insert, error) {
	copy := s.clone()
	copy.returning = append(copy.returning, projections...)
	if err := copy.Validate(); err != nil {
		return Insert{}, err
	}
	return copy, nil
}

// Into returns the target table.
func (s Insert) Into() TableRef {
	return s.into
}

// Columns returns a copy of the inserted columns.
func (s Insert) Columns() []Column {
	return append([]Column(nil), s.columns...)
}

// Values returns a copy of the inserted expressions.
func (s Insert) Values() []Expression {
	return append([]Expression(nil), s.values...)
}

// Returning returns a copy of the returning projections.
func (s Insert) Returning() []Projection {
	return append([]Projection(nil), s.returning...)
}

// Validate reports whether s is internally consistent.
func (s Insert) Validate() error {
	sources, err := validateWriteTarget(s.into, "into")
	if err != nil {
		return err
	}
	if len(s.columns) == 0 {
		return validationError("columns", "must not be empty")
	}
	if len(s.columns) != len(s.values) {
		return validationError("values", "has %d values for %d columns", len(s.values), len(s.columns))
	}
	seen := make(map[string]struct{}, len(s.columns))
	for i, column := range s.columns {
		path := fmt.Sprintf("columns[%d]", i)
		if err := validateTargetColumn(column, s.into, path); err != nil {
			return err
		}
		if _, exists := seen[column.Name()]; exists {
			return validationError(path, "duplicates column %q", column.Name())
		}
		seen[column.Name()] = struct{}{}
		if err := validateExpression(s.values[i], sources, fmt.Sprintf("values[%d]", i)); err != nil {
			return err
		}
	}
	return validateProjections(s.returning, sources, "returning")
}

func (s Insert) clone() Insert {
	copy := s
	copy.columns = append([]Column(nil), s.columns...)
	copy.values = append([]Expression(nil), s.values...)
	copy.returning = append([]Projection(nil), s.returning...)
	return copy
}

// Update is an immutable UPDATE statement.
type Update struct {
	table     TableRef
	sets      []Assignment
	where     Expression
	returning []Projection
}

func (Update) writeStatement() {}

// NewUpdate creates a validated UPDATE statement.
func NewUpdate(table TableRef, assignments ...Assignment) (Update, error) {
	statement := Update{table: table, sets: append([]Assignment(nil), assignments...)}
	if err := statement.Validate(); err != nil {
		return Update{}, err
	}
	return statement, nil
}

// WithWhere returns a copy of s with expression as its predicate.
func (s Update) WithWhere(expression Expression) (Update, error) {
	copy := s.clone()
	copy.where = expression
	if err := copy.Validate(); err != nil {
		return Update{}, err
	}
	return copy, nil
}

// WithReturning returns a copy of s with returning projections.
func (s Update) WithReturning(projections ...Projection) (Update, error) {
	copy := s.clone()
	copy.returning = append(copy.returning, projections...)
	if err := copy.Validate(); err != nil {
		return Update{}, err
	}
	return copy, nil
}

// Table returns the target table.
func (s Update) Table() TableRef {
	return s.table
}

// Assignments returns a copy of the assignments.
func (s Update) Assignments() []Assignment {
	return append([]Assignment(nil), s.sets...)
}

// Where returns the predicate, or nil when no predicate is set.
func (s Update) Where() Expression {
	return s.where
}

// Returning returns a copy of the returning projections.
func (s Update) Returning() []Projection {
	return append([]Projection(nil), s.returning...)
}

// Validate reports whether s is internally consistent.
func (s Update) Validate() error {
	sources, err := validateWriteTarget(s.table, "table")
	if err != nil {
		return err
	}
	if len(s.sets) == 0 {
		return validationError("assignments", "must not be empty")
	}
	seen := make(map[string]struct{}, len(s.sets))
	for i, assignment := range s.sets {
		path := fmt.Sprintf("assignments[%d]", i)
		if err := validateTargetColumn(assignment.column, s.table, path+".column"); err != nil {
			return err
		}
		if _, exists := seen[assignment.column.Name()]; exists {
			return validationError(path+".column", "duplicates column %q", assignment.column.Name())
		}
		seen[assignment.column.Name()] = struct{}{}
		if err := validateExpression(assignment.value, sources, path+".value"); err != nil {
			return err
		}
	}
	if s.where != nil {
		if err := validateExpression(s.where, sources, "where"); err != nil {
			return err
		}
	}
	return validateProjections(s.returning, sources, "returning")
}

func (s Update) clone() Update {
	copy := s
	copy.sets = append([]Assignment(nil), s.sets...)
	copy.returning = append([]Projection(nil), s.returning...)
	return copy
}

// Delete is an immutable DELETE statement.
type Delete struct {
	from      TableRef
	where     Expression
	returning []Projection
}

func (Delete) writeStatement() {}

// NewDelete creates a validated DELETE statement.
func NewDelete(from TableRef) (Delete, error) {
	statement := Delete{from: from}
	if err := statement.Validate(); err != nil {
		return Delete{}, err
	}
	return statement, nil
}

// WithWhere returns a copy of s with expression as its predicate.
func (s Delete) WithWhere(expression Expression) (Delete, error) {
	copy := s
	copy.where = expression
	if err := copy.Validate(); err != nil {
		return Delete{}, err
	}
	return copy, nil
}

// WithReturning returns a copy of s with returning projections.
func (s Delete) WithReturning(projections ...Projection) (Delete, error) {
	copy := s
	copy.returning = append([]Projection(nil), s.returning...)
	copy.returning = append(copy.returning, projections...)
	if err := copy.Validate(); err != nil {
		return Delete{}, err
	}
	return copy, nil
}

// From returns the target table.
func (s Delete) From() TableRef {
	return s.from
}

// Where returns the predicate, or nil when no predicate is set.
func (s Delete) Where() Expression {
	return s.where
}

// Returning returns a copy of the returning projections.
func (s Delete) Returning() []Projection {
	return append([]Projection(nil), s.returning...)
}

// Validate reports whether s is internally consistent.
func (s Delete) Validate() error {
	sources, err := validateWriteTarget(s.from, "from")
	if err != nil {
		return err
	}
	if s.where != nil {
		if err := validateExpression(s.where, sources, "where"); err != nil {
			return err
		}
	}
	return validateProjections(s.returning, sources, "returning")
}

func validateWriteTarget(table TableRef, path string) (map[string]struct{}, error) {
	if err := table.validate(); err != nil {
		return nil, validationError(path, "%s", err)
	}
	if table.Alias() != "" {
		return nil, validationError(path+".alias", "write targets must not use an alias")
	}
	return map[string]struct{}{table.key(): {}}, nil
}

func validateTargetColumn(column Column, table TableRef, path string) error {
	if err := column.source.validate(); err != nil {
		return validationError(path, "%s", err)
	}
	if column.source.key() != table.key() {
		return validationError(path, "belongs to table %q instead of target %q", column.source.Qualifier(), table.Qualifier())
	}
	if _, exists := table.table.Column(column.name); !exists {
		return validationError(path, "references unknown column %q", column.name)
	}
	return nil
}

func validateProjections(projections []Projection, sources map[string]struct{}, path string) error {
	for i, projection := range projections {
		itemPath := fmt.Sprintf("%s[%d]", path, i)
		if projection.alias != "" {
			if err := validateAlias(projection.alias); err != nil {
				return validationError(itemPath+".alias", "%s", err)
			}
		}
		if err := validateExpression(projection.expression, sources, itemPath+".expression"); err != nil {
			return err
		}
	}
	return nil
}
