package query

import (
	"fmt"
	"sync/atomic"

	"github.com/lestrrat-go/rasql/schema"
)

// ResultColumn describes one column exposed by a reusable query result.
type ResultColumn struct {
	Name     string
	Type     schema.ColumnType
	Nullable bool
}

// QueryBody is a validated relational query body.
type QueryBody interface {
	Validate() error
	queryBody()
}

// ResultQuery pairs a query body with the result metadata callers supplied.
type ResultQuery struct {
	body    QueryBody
	columns []ResultColumn
}

// ResultOf makes body reusable as a relation after validating its output
// metadata against the body's projection count.
func ResultOf(body QueryBody, columns ...ResultColumn) (ResultQuery, error) {
	if body == nil {
		return ResultQuery{}, fmt.Errorf("result query body must not be nil")
	}
	if err := body.Validate(); err != nil {
		return ResultQuery{}, err
	}
	for i, column := range columns {
		if err := schema.ValidateIdentifier(column.Name); err != nil {
			return ResultQuery{}, fmt.Errorf("result column %d: %w", i, err)
		}
		if err := schema.ValidateColumnType(column.Type); err != nil {
			return ResultQuery{}, fmt.Errorf("result column %q: %w", column.Name, err)
		}
		for _, existing := range columns[:i] {
			if existing.Name == column.Name {
				return ResultQuery{}, fmt.Errorf("result columns contain duplicate name %q", column.Name)
			}
		}
	}
	switch typed := body.(type) {
	case Select:
		if len(typed.projections) != len(columns) {
			return ResultQuery{}, fmt.Errorf("result columns count %d does not match SELECT projection count %d", len(columns), len(typed.projections))
		}
	case *Select:
		if typed == nil {
			return ResultQuery{}, fmt.Errorf("result query body must not be nil")
		}
		if len(typed.projections) != len(columns) {
			return ResultQuery{}, fmt.Errorf("result columns count %d does not match SELECT projection count %d", len(columns), len(typed.projections))
		}
	case Compound:
		if len(typed.left.columns) != len(columns) {
			return ResultQuery{}, fmt.Errorf("result columns count %d does not match compound output count %d", len(columns), len(typed.left.columns))
		}
	case *Compound:
		if typed == nil {
			return ResultQuery{}, fmt.Errorf("result query body must not be nil")
		}
		if len(typed.left.columns) != len(columns) {
			return ResultQuery{}, fmt.Errorf("result columns count %d does not match compound output count %d", len(columns), len(typed.left.columns))
		}
	}
	return ResultQuery{body: body, columns: cloneResultColumns(columns)}, nil
}

func (q ResultQuery) Body() QueryBody { return q.body }

func (q ResultQuery) Columns() []ResultColumn {
	return cloneResultColumns(q.columns)
}

func cloneResultColumns(columns []ResultColumn) []ResultColumn {
	copy := make([]ResultColumn, len(columns))
	for i, column := range columns {
		copy[i] = column
		copy[i].Type = schema.CloneColumnType(column.Type)
	}
	return copy
}

type relationKind uint8

const (
	relationTable relationKind = iota + 1
	relationResult
	relationCTE
)

// RelationSource is a normalized source accepted by SELECT and JOIN builders.
type RelationSource interface {
	relationRef() RelationRef
}

// RelationRef identifies a physical table, derived result, or CTE source.
type RelationRef struct {
	kind   relationKind
	table  *TableRef
	result *ResultQuery
	cte    *CTE
	alias  string
}

func Relation(table TableRef) RelationRef {
	copy := table
	return RelationRef{kind: relationTable, table: &copy, alias: table.Alias()}
}

func Derived(result ResultQuery, alias string) (RelationRef, error) {
	if err := schema.ValidateIdentifier(alias); err != nil {
		return RelationRef{}, fmt.Errorf("derived relation alias: %w", err)
	}
	if result.body == nil {
		return RelationRef{}, fmt.Errorf("derived relation query must not be empty")
	}
	copy := result
	copy.columns = cloneResultColumns(result.columns)
	return RelationRef{kind: relationResult, result: &copy, alias: alias}, nil
}

func (r RelationRef) relationRef() RelationRef { return r }

func (t TableRef) relationRef() RelationRef { return Relation(t) }

func (r RelationRef) Table() (TableRef, bool) {
	if r.kind != relationTable || r.table == nil {
		return TableRef{}, false
	}
	return *r.table, true
}

func (r RelationRef) Column(name string) ColumnRef { return ColumnRef{source: r, name: name} }

func (r RelationRef) Columns() []ResultColumn {
	if r.kind == relationTable && r.table != nil {
		definition := r.table.Definition()
		columns := make([]ResultColumn, len(definition.Columns))
		for i, column := range definition.Columns {
			columns[i] = ResultColumn{Name: column.Name, Type: schema.CloneColumnType(column.Type), Nullable: column.Nullable}
		}
		return columns
	}
	if r.result == nil {
		return nil
	}
	return cloneResultColumns(r.result.columns)
}

func (r RelationRef) Alias() string { return r.alias }

func (r RelationRef) Qualifier() string {
	if r.alias != "" {
		return r.alias
	}
	if table, ok := r.Table(); ok {
		return table.Qualifier()
	}
	return ""
}

func (r RelationRef) QualifierSchema() string {
	if r.alias != "" {
		return ""
	}
	if table, ok := r.Table(); ok {
		return table.QualifierSchema()
	}
	return ""
}

func (r RelationRef) Schema() string {
	if table, ok := r.Table(); ok {
		return table.Schema()
	}
	return ""
}

func (r RelationRef) Name() string {
	if table, ok := r.Table(); ok {
		return table.Name()
	}
	return r.Qualifier()
}

func (r RelationRef) Definition() schema.TableDef {
	if table, ok := r.Table(); ok {
		return table.Definition()
	}
	return schema.TableDef{}
}

func (r RelationRef) ResultBody() QueryBody {
	if r.result == nil {
		return nil
	}
	return r.result.body
}

func (r RelationRef) CTEName() string {
	if r.cte == nil {
		return ""
	}
	return r.cte.name
}

func (r RelationRef) cteID() uint64 {
	if r.cte == nil {
		return 0
	}
	return r.cte.id
}

func (r RelationRef) QualifiedName() string {
	if r.alias != "" {
		return r.alias
	}
	if table, ok := r.Table(); ok {
		return table.QualifiedName()
	}
	return ""
}

func (r RelationRef) key() string {
	switch r.kind {
	case relationTable:
		if table, ok := r.Table(); ok {
			return "table\x00" + table.key()
		}
	case relationResult:
		return fmt.Sprintf("result\x00%p\x00%s", r.result, r.alias)
	case relationCTE:
		return fmt.Sprintf("cte\x00%p\x00%s", r.cte, r.alias)
	}
	return "relation\x00" + r.alias
}

func (r RelationRef) validate() error {
	if r.kind == 0 {
		return ErrNilTable
	}
	if r.kind == relationTable {
		if table, ok := r.Table(); ok {
			return table.validate()
		}
		return ErrNilTable
	}
	if r.kind == relationResult || r.kind == relationCTE {
		if r.alias == "" {
			return fmt.Errorf("relation alias must not be empty")
		}
		return nil
	}
	return fmt.Errorf("invalid relation source")
}

func (r RelationRef) reference() sourceReference {
	descriptor := r.QualifiedName()
	if table, ok := r.Table(); ok {
		descriptor = table.Definition().QualifiedName()
	}
	return sourceReference{qualifier: r.Qualifier(), schema: r.QualifierSchema(), descriptor: descriptor}
}

func normalizeSources(sources []RelationSource) []RelationRef {
	if sources == nil {
		return nil
	}
	result := make([]RelationRef, len(sources))
	for i, source := range sources {
		if source != nil {
			result[i] = source.relationRef()
		}
	}
	return result
}

// RelationRefOf resolves a source to the relation reference used by query
// builders. It is primarily for packages that provide fluent adapters around
// the query package.
func RelationRefOf(source RelationSource) RelationRef {
	if source == nil {
		return RelationRef{}
	}
	return source.relationRef()
}

// CompoundOperator joins two result queries into one query body.
type CompoundOperator uint8

const (
	Union CompoundOperator = iota + 1
	UnionAll
	Intersect
	Except
)

type Compound struct {
	left, right ResultQuery
	operator    CompoundOperator
}

func (c Compound) Left() ResultQuery          { return cloneResultQuery(c.left) }
func (c Compound) Right() ResultQuery         { return cloneResultQuery(c.right) }
func (c Compound) Operator() CompoundOperator { return c.operator }

func (c CTE) Name() string       { return c.name }
func (c CTE) Query() ResultQuery { return cloneResultQuery(c.query) }

func cloneResultQuery(result ResultQuery) ResultQuery {
	result.columns = cloneResultColumns(result.columns)
	return result
}

func CompoundQuery(left ResultQuery, operator CompoundOperator, right ResultQuery) (Compound, error) {
	if left.body == nil || right.body == nil {
		return Compound{}, fmt.Errorf("compound operands must not be empty")
	}
	if operator < Union || operator > Except {
		return Compound{}, fmt.Errorf("unsupported compound operator %d", operator)
	}
	if len(left.columns) != len(right.columns) {
		return Compound{}, fmt.Errorf("compound operands expose different column counts")
	}
	return Compound{left: left, right: right, operator: operator}, nil
}

func (c Compound) Validate() error {
	if c.left.body == nil || c.right.body == nil {
		return fmt.Errorf("compound operands must not be empty")
	}
	if err := c.left.body.Validate(); err != nil {
		return err
	}
	return c.right.body.Validate()
}

func (Compound) queryBody() {}

type CTE struct {
	name  string
	query ResultQuery
	id    uint64
}

var nextCTEID uint64

func CommonTable(name string, result ResultQuery) (CTE, error) {
	if err := schema.ValidateIdentifier(name); err != nil {
		return CTE{}, fmt.Errorf("CTE name: %w", err)
	}
	if result.body == nil {
		return CTE{}, fmt.Errorf("CTE query must not be empty")
	}
	return CTE{name: name, query: result, id: atomic.AddUint64(&nextCTEID, 1)}, nil
}

func (c CTE) Ref(alias string) (RelationRef, error) {
	if alias == "" {
		alias = c.name
	}
	if err := schema.ValidateIdentifier(alias); err != nil {
		return RelationRef{}, fmt.Errorf("CTE alias: %w", err)
	}
	copy := c
	result := c.query
	result.columns = cloneResultColumns(c.query.columns)
	return RelationRef{kind: relationCTE, cte: &copy, result: &result, alias: alias}, nil
}

func (s Select) queryBody() {}
