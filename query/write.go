package query

import "fmt"

// WriteStatement is a validated statement that changes database rows.
// Returning reports the projections of its RETURNING clause, and is empty when
// the statement has none. Only this package implements it.
type WriteStatement interface {
	Validate() error
	Returning() []Projection
	writeStatement()
}

// Upsert is an immutable INSERT statement with conflict handling.
type Upsert struct {
	insert      Insert
	conflict    []ColumnRef
	assignments []Assignment
	returning   []Projection
}

func (Upsert) writeStatement() {}

// NewUpsert creates a validated upsert statement.
// Conflict columns identify an optional explicit conflict target. Assignments set
// target columns when a conflict occurs. Rendering a non-empty conflict target
// requires dialect.CapabilityConflictTarget; dialects that lack it (for example
// MySQL) reject the statement rather than silently drop the target.
func NewUpsert(insert Insert, conflict []ColumnRef, assignments []Assignment) (Upsert, error) {
	statement := Upsert{
		insert:      insert.clone(),
		conflict:    append([]ColumnRef(nil), conflict...),
		assignments: append([]Assignment(nil), assignments...),
	}
	if err := statement.Validate(); err != nil {
		return Upsert{}, err
	}
	return statement, nil
}

// WithReturning returns a copy of s with returning projections.
func (s Upsert) WithReturning(projections ...Projection) (Upsert, error) {
	copy := s.clone()
	copy.returning = append(copy.returning, projections...)
	if err := copy.Validate(); err != nil {
		return Upsert{}, err
	}
	return copy, nil
}

// Insert returns the underlying insert operation.
func (s Upsert) Insert() Insert {
	return s.insert.clone()
}

// ConflictColumns returns a copy of the explicit conflict target.
func (s Upsert) ConflictColumns() []ColumnRef {
	return append([]ColumnRef(nil), s.conflict...)
}

// Assignments returns a copy of conflict-update assignments.
func (s Upsert) Assignments() []Assignment {
	return append([]Assignment(nil), s.assignments...)
}

// Returning returns a copy of the returning projections.
func (s Upsert) Returning() []Projection {
	return append([]Projection(nil), s.returning...)
}

// Validate reports whether s is internally consistent.
func (s Upsert) Validate() error {
	if len(s.insert.returning) > 0 {
		return validationError("insert.returning", "must be set on the upsert")
	}
	if err := s.insert.Validate(); err != nil {
		return validationError("insert", "%s", err)
	}
	if len(s.conflict) == 0 && len(s.assignments) == 0 {
		return validationError("upsert", "requires conflict columns or assignments")
	}
	sources, err := validateWriteTarget(s.insert.into, "insert.into")
	if err != nil {
		return err
	}
	conflicts := make(map[string]struct{}, len(s.conflict))
	for i, column := range s.conflict {
		path := fmt.Sprintf("conflict[%d]", i)
		if err := validateTargetColumn(column, s.insert.into, path); err != nil {
			return err
		}
		if _, exists := conflicts[column.Name()]; exists {
			return validationError(path, "duplicates column %q", column.Name())
		}
		conflicts[column.Name()] = struct{}{}
	}
	assigned := make(map[string]struct{}, len(s.assignments))
	for i, assignment := range s.assignments {
		path := fmt.Sprintf("assignments[%d]", i)
		if err := validateTargetColumn(assignment.column, s.insert.into, path+".column"); err != nil {
			return err
		}
		if _, exists := assigned[assignment.column.Name()]; exists {
			return validationError(path+".column", "duplicates column %q", assignment.column.Name())
		}
		assigned[assignment.column.Name()] = struct{}{}
		if err := validateClauseExpression(assignment.value, sources, "a conflict-update assignment", path+".value"); err != nil {
			return err
		}
	}
	return validateProjections(s.returning, sources, "returning")
}

func (s Upsert) clone() Upsert {
	copy := s
	copy.insert = s.insert.clone()
	copy.conflict = append([]ColumnRef(nil), s.conflict...)
	copy.assignments = append([]Assignment(nil), s.assignments...)
	copy.returning = append([]Projection(nil), s.returning...)
	return copy
}

// Assignment sets column to expression in an UPDATE statement.
type Assignment struct {
	column ColumnRef
	value  Expression
}

// Set assigns value to column.
func Set(column ColumnRef, value Expression) Assignment {
	return Assignment{column: column, value: value}
}

// Column returns the assigned column.
func (a Assignment) Column() ColumnRef {
	return a.column
}

// Value returns the assigned expression.
func (a Assignment) Value() Expression {
	return a.value
}

// Insert is an immutable INSERT statement. It inserts one or more rows, or the
// database defaults for every column.
type Insert struct {
	into          TableRef
	columns       []ColumnRef
	rows          [][]Expression
	defaultValues bool
	returning     []Projection
}

func (Insert) writeStatement() {}

// NewInsert creates a validated INSERT statement for one row. It is the one-row
// form of NewInsertRows.
func NewInsert(into TableRef, columns []ColumnRef, values []Expression) (Insert, error) {
	return NewInsertRows(into, columns, [][]Expression{values})
}

// NewInsertRows creates a validated INSERT statement for one or more rows.
// The rows are rectangular: every row supplies exactly one expression per
// column, in the order of columns, and each expression renders in that column's
// position. Validation reports an error for an empty rows slice, because
// VALUES with no row is not valid SQL in any supported dialect.
//
// The rows render as a single statement, but that on its own does not make the
// insert atomic. Transaction scope, and whether a statement that fails partway
// rolls back the rows it already wrote, remain the caller's and the database's
// responsibility: a non-transactional MySQL table keeps the earlier rows.
// Wrap the call in a transaction when every row has to land or none of them.
// Bound parameters are capped by the database. This insert costs R*C parameters
// over R rows of C columns only when every row value is a single [Bind]; a row
// value may be any Expression, and one that is not a single [Bind] costs
// however many [Bind] values are nested inside it. See the Parameter limits
// section of the package documentation for the caps and for how to stay under
// them.
func NewInsertRows(into TableRef, columns []ColumnRef, rows [][]Expression) (Insert, error) {
	statement := Insert{
		into:    into,
		columns: append([]ColumnRef(nil), columns...),
		rows:    cloneRows(rows),
	}
	if err := statement.Validate(); err != nil {
		return Insert{}, err
	}
	return statement, nil
}

// WithRows returns a copy of s with rows appended to the rows it already
// inserts. Each row must supply one expression per column of s, and a
// default-values insert accepts no rows at all.
func (s Insert) WithRows(rows ...[]Expression) (Insert, error) {
	copy := s.clone()
	copy.rows = append(copy.rows, cloneRows(rows)...)
	if err := copy.Validate(); err != nil {
		return Insert{}, err
	}
	return copy, nil
}

// cloneRows deep-copies rows so a caller's later append to the outer slice or
// to one of its inner row slices cannot reach inside a validated statement.
func cloneRows(rows [][]Expression) [][]Expression {
	copy := make([][]Expression, len(rows))
	for i, row := range rows {
		copy[i] = append([]Expression(nil), row...)
	}
	return copy
}

// NewDefaultInsert creates a validated INSERT statement that uses the database
// defaults for every column.
func NewDefaultInsert(into TableRef) (Insert, error) {
	statement := Insert{into: into, defaultValues: true}
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
func (s Insert) Columns() []ColumnRef {
	return append([]ColumnRef(nil), s.columns...)
}

// Rows returns a copy of the inserted rows, one slice of expressions per row, in
// insertion order. It is empty for a default-values insert.
func (s Insert) Rows() [][]Expression {
	return cloneRows(s.rows)
}

// UsesDefaultValues reports whether s uses the database defaults for every
// column.
func (s Insert) UsesDefaultValues() bool {
	return s.defaultValues
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
	if s.defaultValues {
		if len(s.columns) > 0 {
			return validationError("columns", "must be empty for a default-values insert")
		}
		if len(s.rows) > 0 {
			return validationError("rows", "must be empty for a default-values insert")
		}
		return validateProjections(s.returning, sources, "returning")
	}
	if len(s.columns) == 0 {
		return validationError("columns", "must not be empty")
	}
	if len(s.rows) == 0 {
		return validationError("rows", "must not be empty")
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
	}
	for i, row := range s.rows {
		if len(row) != len(s.columns) {
			return validationError(fmt.Sprintf("rows[%d]", i), "has %d values for %d columns", len(row), len(s.columns))
		}
		for j, value := range row {
			if err := validateRowValueExpression(value, sources, "an INSERT value", fmt.Sprintf("rows[%d].values[%d]", i, j)); err != nil {
				return err
			}
		}
	}
	return validateProjections(s.returning, sources, "returning")
}

func (s Insert) clone() Insert {
	copy := s
	copy.columns = append([]ColumnRef(nil), s.columns...)
	copy.rows = cloneRows(s.rows)
	copy.returning = append([]Projection(nil), s.returning...)
	return copy
}

// Update is an immutable UPDATE statement.
type Update struct {
	table     TableRef
	sets      []Assignment
	where     Expression
	returning []Projection
	allowAll  bool
}

func (Update) writeStatement() {}

// NewUpdate creates a validated UPDATE statement. A statement with no
// predicate requires AllowAll before it can be rendered or executed.
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
	if expression != nil && copy.allowAll {
		return Update{}, fmt.Errorf("query: UPDATE AllowAll must not be combined with a WHERE predicate")
	}
	if err := copy.Validate(); err != nil {
		return Update{}, err
	}
	return copy, nil
}

// AllowAll returns a copy of s that explicitly permits updating every row.
// It returns an error when s already carries a predicate.
func (s Update) AllowAll() (Update, error) {
	if s.where != nil {
		return Update{}, fmt.Errorf("query: UPDATE AllowAll must not be combined with a WHERE predicate")
	}
	copy := s.clone()
	copy.allowAll = true
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

// AllowsAll reports whether s explicitly permits updating every row.
func (s Update) AllowsAll() bool {
	return s.allowAll
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
		if err := validateClauseExpression(assignment.value, sources, "a SET assignment", path+".value"); err != nil {
			return err
		}
	}
	if s.where != nil {
		if err := validateClauseExpression(s.where, sources, "a WHERE clause", "where"); err != nil {
			return err
		}
	}
	if s.allowAll && s.where != nil {
		return validationError("allowAll", "must not be combined with a WHERE predicate")
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
	allowAll  bool
}

func (Delete) writeStatement() {}

// NewDelete creates a validated DELETE statement.
// A statement with no predicate requires AllowAll before it can be rendered or executed.
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
	if expression != nil && copy.allowAll {
		return Delete{}, fmt.Errorf("query: DELETE AllowAll must not be combined with a WHERE predicate")
	}
	if err := copy.Validate(); err != nil {
		return Delete{}, err
	}
	return copy, nil
}

// AllowAll returns a copy of s that explicitly permits deleting every row.
// It returns an error when s already carries a predicate.
func (s Delete) AllowAll() (Delete, error) {
	if s.where != nil {
		return Delete{}, fmt.Errorf("query: DELETE AllowAll must not be combined with a WHERE predicate")
	}
	copy := s
	copy.allowAll = true
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

// AllowsAll reports whether s explicitly permits deleting every row.
func (s Delete) AllowsAll() bool {
	return s.allowAll
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
		if err := validateClauseExpression(s.where, sources, "a WHERE clause", "where"); err != nil {
			return err
		}
	}
	if s.allowAll && s.where != nil {
		return validationError("allowAll", "must not be combined with a WHERE predicate")
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

func validateTargetColumn(column ColumnRef, table TableRef, path string) error {
	if err := column.source.validate(); err != nil {
		return validationError(path, "%s", err)
	}
	if column.source.key() != table.key() {
		return validationError(path, "belongs to table %q instead of target %q", column.source.QualifiedName(), table.QualifiedName())
	}
	if _, exists := table.column(column.name); !exists {
		return validationError(path, "references unknown column %q", column.name)
	}
	return nil
}

// validateProjections validates the RETURNING projections of a write statement.
// RETURNING reports the rows the statement changed, so it may not aggregate.
func validateProjections(projections []Projection, sources map[string]struct{}, path string) error {
	for i, projection := range projections {
		itemPath := fmt.Sprintf("%s[%d]", path, i)
		if projection.alias != "" {
			if err := validateAlias(projection.alias); err != nil {
				return validationError(itemPath+".alias", "%s", err)
			}
		}
		if err := validateClauseExpression(projection.expression, sources, "a RETURNING projection", itemPath+".expression"); err != nil {
			return err
		}
	}
	return nil
}
