package catalog_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/catalog"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// col returns a minimal, otherwise-unremarkable integer column named name,
// for tests that only care about a table's identity and shape.
func col(name string) schema.ColumnDef {
	return schema.ColumnDef{Name: name, Type: schema.IntegerType{}}
}

// tbl returns a minimal table named name with one plain integer column per
// name in columns.
func tbl(name string, columns ...string) schema.TableDef {
	def := schema.TableDef{Name: name}
	for _, columnName := range columns {
		def.Columns = append(def.Columns, col(columnName))
	}
	return def
}

func TestDriftReportsAddedRemovedAndChangedTables(t *testing.T) {
	removedTable := tbl("legacy", "id")
	changedBefore := tbl("users", "id")
	changedAfter := tbl("users", "id", "email")
	addedTable := tbl("orders", "id")

	described := []schema.TableDef{removedTable, changedBefore}
	live := []schema.TableDef{changedAfter, addedTable}

	report := catalog.Drift(described, live)
	require.False(t, report.Empty())

	require.Equal(t, []schema.TableDef{addedTable}, report.Added())
	require.Equal(t, []schema.TableDef{removedTable}, report.Removed())
	require.Len(t, report.Changed(), 1)
	require.Equal(t, changedBefore, report.Changed()[0].Described())
	require.Equal(t, changedAfter, report.Changed()[0].Live())
}

func TestDriftIsEmptyForIdenticalInput(t *testing.T) {
	t.Run("identical tables", func(t *testing.T) {
		tables := []schema.TableDef{tbl("users", "id"), tbl("orders", "id")}
		report := catalog.Drift(tables, tables)
		require.True(t, report.Empty())
	})
	t.Run("two empty slices", func(t *testing.T) {
		report := catalog.Drift([]schema.TableDef{}, []schema.TableDef{})
		require.True(t, report.Empty())
	})
	t.Run("two nil slices", func(t *testing.T) {
		report := catalog.Drift(nil, nil)
		require.True(t, report.Empty())
	})
}

// TestReportZeroValueAndAccessorSummary pins what Report's doc comment
// promises about the zero value and about summarizing through the
// accessors. The subtest that stood here asserting Report has no String
// method was a deliberate tripwire for the change that adds one, and its own
// comment said the doc sentence it guarded had to be rewritten in that same
// change. This is that change: Report now implements fmt.Stringer, and
// drift_report_test.go pins what it renders.
func TestReportZeroValueAndAccessorSummary(t *testing.T) {
	var zero catalog.Report

	t.Run("zero value is empty in every bucket", func(t *testing.T) {
		require.True(t, zero.Empty())
		require.Nil(t, zero.Added())
		require.Nil(t, zero.Removed())
		require.Nil(t, zero.Changed())
		require.Equal(t, "", zero.String())
	})

	t.Run("a caller summarizes through the accessors", func(t *testing.T) {
		report := catalog.Drift(
			[]schema.TableDef{tbl("legacy", "id"), tbl("users", "id")},
			[]schema.TableDef{tbl("users", "id", "email"), tbl("orders", "id")},
		)
		summary := fmt.Sprintf("schema drift: %d table(s) added, %d removed, %d changed",
			len(report.Added()), len(report.Removed()), len(report.Changed()))
		require.Equal(t, "schema drift: 1 table(s) added, 1 removed, 1 changed", summary)
	})
}

// TestDriftKeepsSameNamedTablesInDifferentSchemasApart pins the fix for
// the old description comparison's own bug: that comparison keyed tables by
// Name alone (generate/description_diff.go:93-100) and would have merged
// audit.events with public.events.
func TestDriftKeepsSameNamedTablesInDifferentSchemasApart(t *testing.T) {
	auditEvents := schema.TableDef{Schema: "audit", Name: "events", Columns: []schema.ColumnDef{col("id")}}
	publicEventsBefore := schema.TableDef{Schema: "public", Name: "events", Columns: []schema.ColumnDef{col("id")}}
	publicEventsAfter := schema.TableDef{Schema: "public", Name: "events", Columns: []schema.ColumnDef{col("id"), col("payload")}}

	described := []schema.TableDef{auditEvents, publicEventsBefore}
	live := []schema.TableDef{auditEvents, publicEventsAfter}

	report := catalog.Drift(described, live)
	require.Len(t, report.Changed(), 1)
	require.Equal(t, "public", report.Changed()[0].Schema())
}

// TestDriftDoesNotCollapseADuplicateTableName proves match's step 1 (pairing
// by name) refuses to pair an identity that appears more than once on one
// side, so two same-named tables on the described side are never silently
// merged into one entry: both are reported.
func TestDriftDoesNotCollapseADuplicateTableName(t *testing.T) {
	dupA := schema.TableDef{Name: "dup", Columns: []schema.ColumnDef{col("a")}}
	dupB := schema.TableDef{Name: "dup", Columns: []schema.ColumnDef{col("b")}}

	report := catalog.Drift([]schema.TableDef{dupA, dupB}, nil)
	require.Len(t, report.Removed(), 2)
	require.Empty(t, report.Added())
	require.Empty(t, report.Changed())
}

func TestDriftIgnoresRowName(t *testing.T) {
	described := tbl("users", "id")
	described.RowName = "Account"
	live := tbl("users", "id")
	live.RowName = ""

	require.NotEqual(t, described, live, "the pair must genuinely differ before normalization, or an empty report here would be vacuous")

	report := catalog.Drift([]schema.TableDef{described}, []schema.TableDef{live})
	require.True(t, report.Empty())
}

func TestDriftIgnoresGeneratorDerivedRelationships(t *testing.T) {
	described := tbl("orders", "id")
	described.Relationships = []schema.RelationshipDef{
		{Name: "User", Kind: schema.RelationshipBelongsTo, Columns: []string{"id"}, ReferencedTable: "users", ReferencedColumns: []string{"id"}},
	}
	live := tbl("orders", "id")
	live.Relationships = nil

	require.NotEqual(t, described, live, "the pair must genuinely differ before normalization, or an empty report here would be vacuous")

	report := catalog.Drift([]schema.TableDef{described}, []schema.TableDef{live})
	require.True(t, report.Empty())
}

func TestDriftTreatsAZeroLengthListAsUnset(t *testing.T) {
	t.Run("PrimaryKey", func(t *testing.T) {
		described := tbl("users", "id")
		described.PrimaryKey = []string{}
		live := tbl("users", "id")
		live.PrimaryKey = nil

		report := catalog.Drift([]schema.TableDef{described}, []schema.TableDef{live})
		require.True(t, report.Empty())
	})

	t.Run("nested: UniqueDef.IncludeColumns and IndexDef.StorageParameters", func(t *testing.T) {
		described := tbl("users", "id")
		described.UniqueConstraints = []schema.UniqueDef{{Name: "u", Columns: []string{"id"}, IncludeColumns: []string{}}}
		described.Indexes = []schema.IndexDef{{Name: "ix", Columns: []string{"id"}, StorageParameters: map[string]string{}}}

		live := tbl("users", "id")
		live.UniqueConstraints = []schema.UniqueDef{{Name: "u", Columns: []string{"id"}, IncludeColumns: nil}}
		live.Indexes = []schema.IndexDef{{Name: "ix", Columns: []string{"id"}, StorageParameters: nil}}

		report := catalog.Drift([]schema.TableDef{described}, []schema.TableDef{live})
		require.True(t, report.Empty())
	})
}

// TestDriftReportsAReorderedList pins §2.4's "normalization does not sort
// anything" rule: two checks that only swapped position are drift, because
// regenerating from live would write schema_gen.go's literal in the new
// order.
func TestDriftReportsAReorderedList(t *testing.T) {
	described := tbl("users", "id")
	described.Checks = []schema.CheckDef{{Expression: "a > 0"}, {Expression: "b > 0"}}
	live := tbl("users", "id")
	live.Checks = []schema.CheckDef{{Expression: "b > 0"}, {Expression: "a > 0"}}

	report := catalog.Drift([]schema.TableDef{described}, []schema.TableDef{live})
	require.False(t, report.Empty())
	require.Len(t, report.Changed(), 1)
}

// pointerCol returns an integer column named name whose type is held by
// pointer rather than by value. The rest of the schema package does not
// support that shape -- TableDef.Validate and JSON encoding both reject a
// pointer-backed column type -- but a caller can still build one in Go and
// hand it to Drift, and a pointer is the one thing a plain struct assignment
// leaves shared between a descriptor and its copy. Every case below uses it,
// because a value-backed column type cannot show the difference: copying it
// already copies all of it.
func pointerCol(name string) schema.ColumnDef {
	return schema.ColumnDef{Name: name, Type: &schema.IntegerType{}}
}

// TestReportHandsOutIndependentCopies pins Report's doc claim that nothing a
// caller does to a result can reach back into the Report or into the slices
// Drift was called with, for each of the three buckets and in both
// directions: a write through what an accessor returned must be invisible to
// a later call on the same accessor, and invisible to the caller's own input.
func TestReportHandsOutIndependentCopies(t *testing.T) {
	unsigned := func(t *testing.T, column schema.ColumnDef) bool {
		t.Helper()
		integer, ok := column.Type.(*schema.IntegerType)
		require.True(t, ok, "column %q must still hold its type by pointer", column.Name)
		return integer.Unsigned
	}

	t.Run("added", func(t *testing.T) {
		live := []schema.TableDef{{Name: "users", Columns: []schema.ColumnDef{pointerCol("id")}}}
		report := catalog.Drift(nil, live)
		require.Len(t, report.Added(), 1)

		report.Added()[0].Columns[0].Type.(*schema.IntegerType).Unsigned = true

		require.False(t, unsigned(t, report.Added()[0].Columns[0]))
		require.False(t, unsigned(t, live[0].Columns[0]))
	})

	t.Run("removed", func(t *testing.T) {
		described := []schema.TableDef{{Name: "users", Columns: []schema.ColumnDef{pointerCol("id")}}}
		report := catalog.Drift(described, nil)
		require.Len(t, report.Removed(), 1)

		report.Removed()[0].Columns[0].Type.(*schema.IntegerType).Unsigned = true

		require.False(t, unsigned(t, report.Removed()[0].Columns[0]))
		require.False(t, unsigned(t, described[0].Columns[0]))
	})

	t.Run("changed", func(t *testing.T) {
		described := []schema.TableDef{{Name: "users", Columns: []schema.ColumnDef{pointerCol("id")}}}
		live := []schema.TableDef{{Name: "users", Columns: []schema.ColumnDef{pointerCol("id"), col("email")}}}
		report := catalog.Drift(described, live)
		require.Len(t, report.Changed(), 1)

		report.Changed()[0].Described().Columns[0].Type.(*schema.IntegerType).Unsigned = true
		report.Changed()[0].Live().Columns[0].Type.(*schema.IntegerType).Unsigned = true

		require.False(t, unsigned(t, report.Changed()[0].Described().Columns[0]))
		require.False(t, unsigned(t, report.Changed()[0].Live().Columns[0]))
		require.False(t, unsigned(t, described[0].Columns[0]))
		require.False(t, unsigned(t, live[0].Columns[0]))
	})
}

// --- TestEveryDescriptorFieldIsCompared -------------------------------------
//
// This is the completeness test: it walks schema.TableDef reflectively,
// finds every leaf field reachable from it -- through every struct, every
// slice's element type, every map, and each of the nine schema.ColumnType
// implementations -- and requires each one, changed alone, to make Drift
// report the table changed. RowName and every Relationships field are the
// two exceptions normalize ignores by design (§2.4 of the phase 4
// specification); every other leaf must move the verdict.
//
// The walk is reflective and unconditional, exactly like normalize's own
// fold: it names no field itself (Schema and Name are the only fields
// skipped, and that is because they are what tables are matched on, not
// because this test does not know about them -- changing either produces an
// added table and a removed table, not a changed one, which is a different
// property than the one this test checks). A field added to any descriptor
// type tomorrow is walked into and tested the same day, with two exceptions
// this test cannot make automatic: a wholly new schema.ColumnType
// implementation needs a new entry in columnTypeSamples, the same closed
// list schema/column_type.go's own validColumnType already keeps, and a new
// struct with an unexported field needs a case alongside
// schema.DecimalScale, schema.TextWidth and schema.IntegerDisplayWidth --
// and until either is added, the walk fails loudly rather than silently
// skipping what it cannot construct.

// step navigates from an addressable struct or slice value to an addressable
// child: a struct field by name, or a slice element by index.
type step struct {
	field   string
	index   int
	byIndex bool
}

func fieldStep(name string) step { return step{field: name} }
func indexStep(i int) step       { return step{index: i, byIndex: true} }

func navigate(v reflect.Value, steps []step) reflect.Value {
	for _, s := range steps {
		if s.byIndex {
			v = v.Index(s.index)
		} else {
			v = v.FieldByName(s.field)
		}
	}
	return v
}

var (
	decimalScaleType        = reflect.TypeOf(schema.DecimalScale{})
	textWidthType           = reflect.TypeOf(schema.TextWidth{})
	integerDisplayWidthType = reflect.TypeOf(schema.IntegerDisplayWidth{})
)

// completenessLeaf is one leaf field the walk found: a human-readable path
// for the subtest name, whether that field is one of the two ignored facts,
// and a way to mutate a fresh table so only that leaf differs from an
// unmutated clone of the same baseline.
type completenessLeaf struct {
	path          string
	expectNoDrift bool
	mutate        func(t *testing.T, table *schema.TableDef)
}

// columnTypeSamples returns one populated value of each of the nine
// implementations validColumnType (schema/column_type.go:140) accepts, in
// the same order collectCompletenessLeaves uses to build the baseline's
// Columns list. This is the one place the walk names a closed set by hand:
// Go's interfaces do not let reflection discover a type's implementations,
// so the set has to be given, the same way the package's own encoder,
// decoder and validColumnType already give it.
func columnTypeSamples() []schema.ColumnType {
	return []schema.ColumnType{
		schema.BooleanType{},
		schema.IntegerType{Unsigned: true, DisplayWidth: schema.NewIntegerDisplayWidth(11), ZeroFill: true},
		schema.FloatType{},
		schema.TextType{Width: schema.NewTextWidth(255), Fixed: true},
		schema.BytesType{},
		schema.TimeType{},
		schema.JSONType{},
		schema.UUIDType{},
		schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(2), Unsigned: true, ZeroFill: true},
	}
}

// completenessBaseline returns a single table populated at every depth: one
// column per schema.ColumnType implementation, and at least one element in
// every other slice and map schema.TableDef reaches, so the walk below can
// reach every leaf and mutate it without growing anything.
func completenessBaseline() schema.TableDef {
	var columns []schema.ColumnDef
	for i, sample := range columnTypeSamples() {
		columns = append(columns, schema.ColumnDef{
			Name:                fmt.Sprintf("c%d", i),
			Type:                sample,
			Nullable:            true,
			Default:             "d",
			GeneratedExpression: "expr",
			GeneratedStorage:    "STORED",
			Hidden:              true,
		})
	}

	return schema.TableDef{
		Schema:                      "s",
		Name:                        "t",
		RowName:                     "T",
		Columns:                     columns,
		PrimaryKey:                  []string{"c0"},
		Strict:                      true,
		WithoutRowID:                true,
		PrimaryKeyAutoincrement:     true,
		PrimaryKeyOnConflict:        schema.ConflictResolution("REPLACE"),
		VirtualTableModule:          "fts5",
		VirtualTableModuleArguments: []string{"body"},
		UniqueConstraints: []schema.UniqueDef{
			{
				Name:             "u1",
				Columns:          []string{"c0"},
				Deferrable:       schema.Deferrability("DEFERRABLE"),
				NullsNotDistinct: true,
				IncludeColumns:   []string{"c1"},
				OnConflict:       schema.ConflictResolution("IGNORE"),
				Keys: []schema.IndexKeyDef{
					{Expression: "c0", Descending: true, Collation: "C", OperatorClass: "op", PrefixLength: 3, NullsOrder: schema.NullsFirst},
				},
				Temporal:          true,
				StorageParameters: map[string]string{"fillfactor": "70"},
				Tablespace:        "ts",
				ReplicaIdentity:   true,
				Collations:        map[string]string{"c0": "C"},
			},
		},
		Checks: []schema.CheckDef{
			{Name: "chk1", Expression: "c0 > 0", NoInherit: true, NotValid: true, NotEnforced: true},
		},
		ExclusionConstraints: []schema.ExclusionDef{
			{
				Name:   "e1",
				Method: "gist",
				Elements: []schema.ExclusionElementDef{
					{Expression: "c0", Operator: "="},
				},
				Predicate:  "c0 > 0",
				Deferrable: schema.Deferrability("DEFERRABLE"),
			},
		},
		Indexes: []schema.IndexDef{
			{
				Name:              "ix1",
				Columns:           []string{"c0"},
				Unique:            true,
				Method:            "gin",
				Expressions:       []string{"c0"},
				Predicate:         "c0 > 0",
				IncludeColumns:    []string{"c1"},
				Invisible:         true,
				NotValid:          true,
				StorageParameters: map[string]string{"fillfactor": "70"},
				Tablespace:        "ts",
				ReplicaIdentity:   true,
				Keys: []schema.IndexKeyDef{
					{Expression: "c0", Descending: true, Collation: "C", OperatorClass: "op", PrefixLength: 3, NullsOrder: schema.NullsFirst},
				},
				NullsNotDistinct: true,
			},
		},
		ForeignKeys: []schema.ForeignKeyDef{
			{
				Name:              "fk1",
				Columns:           []string{"c1"},
				ReferencedSchema:  "s2",
				ReferencedTable:   "t2",
				ReferencedColumns: []string{"id"},
				Match:             schema.MatchType("SIMPLE"),
				OnDelete:          schema.ReferenceAction("CASCADE"),
				OnUpdate:          schema.ReferenceAction("CASCADE"),
				Deferrable:        schema.Deferrability("DEFERRABLE"),
				NotValid:          true,
				NotEnforced:       true,
				Temporal:          true,
				DeleteSetColumns:  []string{"c1"},
			},
		},
		Relationships: []schema.RelationshipDef{
			{
				Name:              "R",
				Kind:              schema.RelationshipBelongsTo,
				Columns:           []string{"c1"},
				ReferencedSchema:  "s2",
				ReferencedTable:   "t2",
				ReferencedColumns: []string{"id"},
			},
		},
	}
}

// collectStructLeaves walks probe -- a read-only, fully populated value used
// only to discover shape, never mutated -- and calls onLeaf once per leaf
// position with the navigation steps needed to reach an addressable version
// of that same position starting from a value of probe's own root type.
//
// A struct recurses into every field. A slice of structs recurses into its
// first element, since the walk is enumerating field *positions*, not
// exercising every element a baseline happens to carry; a slice of anything
// else, and a map of anything, is one leaf: §4.4 treats both that way for
// the same reason -- a map's entries are not addressable through reflect at
// all without reconstructing the whole map, and this codebase has no map
// whose value is itself a struct to make that a real limitation today. A
// pointer or interface is not handled generically here: schema.TableDef's
// only interface field is ColumnDef.Type, and it is walked separately by
// columnTypeLeaves because reflection cannot discover an interface's other
// implementations from one value of it. A struct with an unexported field
// that is not one of the three known opaque optional types fails the test
// outright: this walk cannot construct a differing value for a field it
// cannot see, and skipping it would silently reopen exactly the hole this
// phase exists to close.
func collectStructLeaves(t *testing.T, probe reflect.Value, path string, steps []step, skip func(structType reflect.Type, fieldName string) bool, onLeaf func(path string, steps []step)) {
	switch probe.Kind() {
	case reflect.Struct:
		structType := probe.Type()
		if structType == decimalScaleType || structType == textWidthType || structType == integerDisplayWidthType {
			onLeaf(path, steps)
			return
		}
		for i := 0; i < structType.NumField(); i++ {
			field := structType.Field(i)
			if field.PkgPath != "" {
				t.Fatalf("%s: field %s of %s is unexported and is not one of the walk's known opaque optional types (schema.DecimalScale, schema.TextWidth, schema.IntegerDisplayWidth); the completeness walk must fail here rather than silently skip a field it cannot construct a differing value for", path, field.Name, structType)
				return
			}
			if skip != nil && skip(structType, field.Name) {
				continue
			}
			childSteps := append(append([]step{}, steps...), fieldStep(field.Name))
			collectStructLeaves(t, probe.Field(i), path+"."+field.Name, childSteps, skip, onLeaf)
		}
	case reflect.Slice:
		if probe.Type().Elem().Kind() == reflect.Struct {
			if probe.Len() == 0 {
				t.Fatalf("%s: baseline slice is empty; the completeness walk cannot reach its element type's own fields", path)
				return
			}
			childSteps := append(append([]step{}, steps...), indexStep(0))
			collectStructLeaves(t, probe.Index(0), path+"[0]", childSteps, skip, onLeaf)
			return
		}
		onLeaf(path, steps)
	default:
		// A map, or a scalar, is always one leaf. §4.4 treats a slice of
		// non-structs the same way, handled by the case above falling
		// through to onLeaf.
		onLeaf(path, steps)
	}
}

// mutateLeaf changes v, an addressable value obtained by navigating a fresh
// clone of the baseline with a completenessLeaf's own steps, to a value
// different from whatever it already holds. It is generic over every kind
// the walk's leaves can be: it does not know or care which field v is.
func mutateLeaf(t *testing.T, v reflect.Value) {
	t.Helper()
	switch v.Type() {
	case decimalScaleType:
		current := v.Interface().(schema.DecimalScale)
		value, _ := current.Value()
		v.Set(reflect.ValueOf(schema.NewDecimalScale(value + 1)))
		return
	case textWidthType:
		current := v.Interface().(schema.TextWidth)
		value, _ := current.Value()
		v.Set(reflect.ValueOf(schema.NewTextWidth(value + 1)))
		return
	case integerDisplayWidthType:
		current := v.Interface().(schema.IntegerDisplayWidth)
		value, _ := current.Value()
		v.Set(reflect.ValueOf(schema.NewIntegerDisplayWidth(value + 1)))
		return
	}
	switch v.Kind() {
	case reflect.Bool:
		v.SetBool(!v.Bool())
	case reflect.String:
		v.SetString(v.String() + "_completeness_leaf")
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(v.Int() + 1)
	case reflect.Slice:
		elem := reflect.Zero(v.Type().Elem())
		if v.Type().Elem().Kind() == reflect.String {
			elem = reflect.ValueOf("zz_completeness_leaf")
		}
		v.Set(reflect.Append(v, elem))
	case reflect.Map:
		if v.Len() == 0 {
			t.Fatalf("baseline map at a completeness leaf must not be empty")
			return
		}
		keys := v.MapKeys()
		key := keys[0]
		current := v.MapIndex(key)
		if current.Kind() != reflect.String {
			t.Fatalf("completeness walk has no generic mutation for a map value of kind %s", current.Kind())
			return
		}
		v.SetMapIndex(key, reflect.ValueOf(current.String()+"_completeness_leaf"))
	default:
		t.Fatalf("completeness walk has no generic mutation for kind %s (type %s)", v.Kind(), v.Type())
	}
}

// collectCompletenessLeaves enumerates every leaf field this test requires
// Drift to compare: every field collectStructLeaves reaches from
// schema.TableDef (skipping Schema and Name, which identify a table for
// matching rather than describe it, and skipping ColumnDef.Type, handled
// below), plus each of the nine schema.ColumnType implementations' own
// fields.
func collectCompletenessLeaves(t *testing.T, baseline schema.TableDef) []completenessLeaf {
	var leaves []completenessLeaf

	rootType := reflect.TypeOf(baseline)
	probeRoot := reflect.ValueOf(baseline)
	skipColumnType := func(structType reflect.Type, fieldName string) bool {
		return structType == reflect.TypeOf(schema.ColumnDef{}) && fieldName == "Type"
	}
	for i := 0; i < rootType.NumField(); i++ {
		field := rootType.Field(i)
		if field.Name == "Schema" || field.Name == "Name" {
			// Table identity: match pairs the table list by Schema+Name, so
			// changing either does not make a table Changed -- it makes one
			// table Removed and an unrelated one Added, a different
			// property than the one this test checks.
			continue
		}
		expectNoDrift := field.Name == "RowName"
		steps := []step{fieldStep(field.Name)}
		collectStructLeaves(t, probeRoot.Field(i), field.Name, steps, skipColumnType, func(leafPath string, leafSteps []step) {
			capturedSteps := append([]step{}, leafSteps...)
			leaves = append(leaves, completenessLeaf{
				path:          leafPath,
				expectNoDrift: expectNoDrift || strings.HasPrefix(leafPath, "Relationships"),
				mutate: func(t *testing.T, table *schema.TableDef) {
					v := reflect.ValueOf(table).Elem()
					mutateLeaf(t, navigate(v, capturedSteps))
				},
			})
		})
	}

	samples := columnTypeSamples()
	require.Len(t, baseline.Columns, len(samples), "the baseline must carry exactly one column per schema.ColumnType implementation")
	for columnIndex, sample := range samples {
		require.IsTypef(t, sample, baseline.Columns[columnIndex].Type, "baseline column %d must hold sample %d's own concrete type", columnIndex, columnIndex)
		typeName := reflect.TypeOf(sample).Name()
		collectStructLeaves(t, reflect.ValueOf(sample), fmt.Sprintf("Columns[%d].Type(%s)", columnIndex, typeName), nil, nil, func(leafPath string, leafSteps []step) {
			capturedSteps := append([]step{}, leafSteps...)
			capturedIndex := columnIndex
			leaves = append(leaves, completenessLeaf{
				path:          leafPath,
				expectNoDrift: false,
				mutate: func(t *testing.T, table *schema.TableDef) {
					concrete := table.Columns[capturedIndex].Type
					fresh := reflect.New(reflect.TypeOf(concrete)).Elem()
					fresh.Set(reflect.ValueOf(concrete))
					mutateLeaf(t, navigate(fresh, capturedSteps))
					table.Columns[capturedIndex].Type = fresh.Interface().(schema.ColumnType)
				},
			})
		})
	}

	return leaves
}

func TestEveryDescriptorFieldIsCompared(t *testing.T) {
	baseline := completenessBaseline()
	leaves := collectCompletenessLeaves(t, baseline)
	t.Logf("completeness walk enumerates %d leaf paths", len(leaves))
	require.NotEmpty(t, leaves)

	sawException := false
	for _, leaf := range leaves {
		leaf := leaf
		t.Run(leaf.path, func(t *testing.T) {
			described := baseline.Clone()
			live := baseline.Clone()
			leaf.mutate(t, &live)
			require.False(t, reflect.DeepEqual(described, live), "mutate must actually change something, or this leaf's assertion would be vacuous")

			report := catalog.Drift([]schema.TableDef{described}, []schema.TableDef{live})
			if leaf.expectNoDrift {
				sawException = true
				require.True(t, report.Empty(), "path %s is one of the two ignored facts (RowName, Relationships) and must report no drift", leaf.path)
				return
			}
			require.False(t, report.Empty(), "path %s changed but Drift reported no drift", leaf.path)
			require.Len(t, report.Changed(), 1)
		})
	}
	require.True(t, sawException, "the walk must reach at least one of the two ignored-fact exceptions (RowName, Relationships)")
}
