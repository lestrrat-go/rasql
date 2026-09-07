package rasql

import (
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql/schema"
)

type queryAPIDecoder struct {
	resultSchema ResultSchema
	presence     []Presence
}

func (d queryAPIDecoder) ResultSchema() ResultSchema { return d.resultSchema }
func (d queryAPIDecoder) Presence() []Presence       { return append([]Presence(nil), d.presence...) }
func (d queryAPIDecoder) DecodeRow(source ScanSource, result *int64) error {
	return source.Scan(result)
}

type queryAPISource struct {
	value        any
	destinations int
}

func (s *queryAPISource) Scan(destinations ...any) error {
	s.destinations++
	if len(destinations) != 1 {
		return errors.New("wrong destination count")
	}
	pointer, ok := destinations[0].(*int64)
	if !ok {
		return errors.New("wrong destination type")
	}
	*pointer = s.value.(int64)
	return nil
}

func TestResultSchemaDefensiveCopyAndValidation(t *testing.T) {
	columns := []ResultColumn{{Name: "id", Type: schema.IntegerType{}}}
	result, err := NewResultSchema(columns...)
	if err != nil {
		t.Fatal(err)
	}
	columns[0].Name = "changed"
	if got := result.Columns()[0].Name; got != "id" {
		t.Fatalf("schema changed through input: %q", got)
	}
	copy := result.Columns()
	copy[0].Name = "changed"
	if got := result.Columns()[0].Name; got != "id" {
		t.Fatalf("schema changed through accessor: %q", got)
	}
	for _, column := range []ResultColumn{{}, {Name: "id", Type: nil}, {Name: "id", Type: schema.IntegerType{}, Codec: "bad codec"}} {
		if _, err := NewResultSchema(column); err == nil {
			t.Fatalf("accepted invalid column %#v", column)
		}
	}
	if _, err := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}}, ResultColumn{Name: "id", Type: schema.IntegerType{}}); err == nil {
		t.Fatal("accepted duplicate schema name")
	}
}

func TestProjectionValidationAndPresence(t *testing.T) {
	resultSchema, err := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}})
	if err != nil {
		t.Fatal(err)
	}
	decoder := queryAPIDecoder{resultSchema: resultSchema}
	item := Item("id", Value(int64(1)), schema.IntegerType{}, "")
	projection, err := NewProjection([]ProjectionItem{item}, decoder)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Schema().Columns()) != 1 {
		t.Fatal("projection lost schema")
	}
	wrongSchema, _ := NewResultSchema(ResultColumn{Name: "other", Type: schema.IntegerType{}})
	if _, err := NewProjection([]ProjectionItem{item}, queryAPIDecoder{resultSchema: wrongSchema}); err == nil {
		t.Fatal("accepted decoder schema mismatch")
	}
	presence, err := NewPresence("profile", "id")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewProjection([]ProjectionItem{item}, queryAPIDecoder{resultSchema: resultSchema, presence: []Presence{presence}}); err != nil {
		t.Fatal(err)
	}
	unknown, _ := NewPresence("profile", "missing")
	if _, err := NewProjection([]ProjectionItem{item}, queryAPIDecoder{resultSchema: resultSchema, presence: []Presence{unknown}}); err == nil {
		t.Fatal("accepted unknown presence column")
	}
}

func TestScalarDecoderUsesFreshDestination(t *testing.T) {
	projection, err := Scalar("value", Value(int64(1)), schema.IntegerType{}, "")
	if err != nil {
		t.Fatal(err)
	}
	first, second := int64(0), int64(0)
	source := &queryAPISource{value: int64(42)}
	if err := projection.Decoder().DecodeRow(source, &first); err != nil {
		t.Fatal(err)
	}
	source.value = int64(7)
	if err := projection.Decoder().DecodeRow(source, &second); err != nil {
		t.Fatal(err)
	}
	if first != 42 || second != 7 || first == second {
		t.Fatalf("decoded values = %d, %d", first, second)
	}
}

func TestQueryOperationsAreImmutable(t *testing.T) {
	projection, err := Scalar("value", Value(int64(1)), schema.IntegerType{}, "")
	if err != nil {
		t.Fatal(err)
	}
	base := Select(Source{}, projection)
	filtered := base.Where(EqualValue(Value(int64(1)), int64(1)))
	if len(base.Plan().where) != 0 || len(filtered.Plan().where) != 1 {
		t.Fatal("query mutation leaked across values")
	}
	if _, err := base.Limit(-1); err == nil {
		t.Fatal("accepted negative limit")
	}
	if _, err := base.Offset(-1); err == nil {
		t.Fatal("accepted negative offset")
	}
}

func TestSourceBoundColumnsValidateNullabilityAndMembership(t *testing.T) {
	table, err := ReadTableOf[int64](schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "nickname", Type: schema.TextType{}, Nullable: true}}})
	if err != nil {
		t.Fatal(err)
	}
	relation, err := SourceOf(table, "u")
	if err != nil {
		t.Fatal(err)
	}
	column, err := BindColumn[int64, int64](relation, "id", "")
	if err != nil {
		t.Fatal(err)
	}
	if column.Expr().node == nil {
		t.Fatal("column expression is zero")
	}
	nullable, err := BindNullColumn[int64, string](relation, "nickname", "custom.codec")
	if err != nil {
		t.Fatal(err)
	}
	if nullable.NullExpr().node == nil {
		t.Fatal("nullable expression is zero")
	}
	optional := Optional(relation)
	if _, err := BindOptionalColumn[int64, int64](optional, "id", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := BindColumn[int64, int64](relation, "nickname", ""); err == nil {
		t.Fatal("accepted nullable column as required")
	}
	if _, err := BindColumn[int64, int64](relation, "missing", ""); err == nil {
		t.Fatal("accepted unknown column")
	}
}
