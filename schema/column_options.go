package schema

// ColumnOption configures a column constructor such as Integer or Text.
type ColumnOption interface {
	applyColumn(*Column) error
}

// columnTableOption carries either a fully-built Column or the first error a
// ColumnOption reported while building one. Reporting the error through
// applyTable, rather than from the column constructor itself, is what lets
// Integer, Text, and the rest return a bare TableOption: NewTable surfaces
// the error only once it applies every option, the same place every other
// TableOption error surfaces.
type columnTableOption struct {
	column Column
	err    error
}

func (o columnTableOption) applyTable(b *tableBuilder) error {
	if o.err != nil {
		return o.err
	}
	b.columns = append(b.columns, o.column)
	return nil
}

// column builds a Column of columnType named name, applying opts in order,
// and returns it as a TableOption. Every typed column constructor (Boolean,
// Integer, and so on) is a thin wrapper around this.
func column(name string, columnType ColumnType, opts ...ColumnOption) TableOption {
	built := Column{Name: name, Type: columnType}
	for _, opt := range opts {
		if opt == nil {
			return columnTableOption{err: validationError("columns", "option for column %q must not be nil", name)}
		}
		if err := opt.applyColumn(&built); err != nil {
			return columnTableOption{err: err}
		}
	}
	return columnTableOption{column: built}
}

// Boolean declares a boolean column named name.
func Boolean(name string, opts ...ColumnOption) TableOption {
	return column(name, BooleanType{}, opts...)
}

// Integer declares an integer column named name. Unsigned marks it unsigned.
func Integer(name string, opts ...ColumnOption) TableOption {
	return column(name, IntegerType{}, opts...)
}

// Float declares a floating-point column named name.
func Float(name string, opts ...ColumnOption) TableOption {
	return column(name, FloatType{}, opts...)
}

// Text declares a text column named name.
func Text(name string, opts ...ColumnOption) TableOption {
	return column(name, TextType{}, opts...)
}

// Bytes declares a binary column named name.
func Bytes(name string, opts ...ColumnOption) TableOption {
	return column(name, BytesType{}, opts...)
}

// Time declares a time column named name.
func Time(name string, opts ...ColumnOption) TableOption {
	return column(name, TimeType{}, opts...)
}

// JSON declares a JSON column named name.
func JSON(name string, opts ...ColumnOption) TableOption {
	return column(name, JSONType{}, opts...)
}

// UUID declares a UUID column named name.
func UUID(name string, opts ...ColumnOption) TableOption {
	return column(name, UUIDType{}, opts...)
}

// Decimal declares an exact decimal column named name with the given
// precision and scale. Precision and scale are positional, rather than
// options, because Table.Validate rejects a decimal column that lacks
// either: stating both here makes an incomplete decimal column impossible to
// construct in the first place instead of merely rejected once assembled.
func Decimal(name string, precision, scale int, opts ...ColumnOption) TableOption {
	return column(name, DecimalType{Precision: precision, Scale: NewDecimalScale(scale)}, opts...)
}

// nullableColumnOption marks a column nullable.
type nullableColumnOption struct{}

// Nullable marks a column as accepting NULL.
func Nullable() ColumnOption {
	return nullableColumnOption{}
}

func (nullableColumnOption) applyColumn(c *Column) error {
	c.Nullable = true
	return nil
}

// defaultColumnOption states a column's default expression.
type defaultColumnOption string

// Default states expr as the column's default expression, rendered by a
// dialect exactly as given.
func Default(expr string) ColumnOption {
	return defaultColumnOption(expr)
}

func (o defaultColumnOption) applyColumn(c *Column) error {
	c.Default = string(o)
	return nil
}

// unsignedColumnOption marks an integer column unsigned.
type unsignedColumnOption struct{}

// Unsigned marks an integer column as unsigned. Applying it to any other
// column type is rejected: only IntegerType carries this option.
func Unsigned() ColumnOption {
	return unsignedColumnOption{}
}

func (unsignedColumnOption) applyColumn(c *Column) error {
	integer, ok := c.Type.(IntegerType)
	if !ok {
		return validationError("columns", "column %q: Unsigned only applies to integer columns", c.Name)
	}
	integer.Unsigned = true
	c.Type = integer
	return nil
}
