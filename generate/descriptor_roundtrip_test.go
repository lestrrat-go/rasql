package generate_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestSchemaDescriptorRoundTripsThroughGeneratedSource is the gate a planned
// redesign depends on: it deletes a separate checked-in schema package and
// makes the generated store the single record of the schema, which is only
// safe if the descriptor rasqlgen renders into schema_gen.go is a complete,
// lossless serialization of the schema.TableDef it came from. If any field
// of TableDef is recorded in memory but never emitted as Go source, this
// test is what makes that impossible to regress.
//
// What each check compares is the whole point. roundTripDescriptors carries
// the fixtures' own values into the compiled scratch module and compares the
// descriptors read back there against them, so a field the renderer never
// emitted fails the test. A comparison with rendered output on both sides
// cannot do that: a field missing from the rendering is missing from both
// sides at once, and they agree.
//
// The two fixtures together, not either alone, cover every field: SQLite's
// virtual-table facts (VirtualTableModule, its arguments, and a Hidden
// column) can only appear on a table that TableDef.Validate forbids from
// also carrying a primary key, indexes, unique/check/exclusion constraints,
// or foreign keys, so those live on newTableDefFixture and the virtual-table
// facts live on newVirtualTableDefFixture. requireFixturesCoverEveryField
// proves, by reflection over the actual schema package rather than a
// hand-kept list, that between the two nothing was left at its zero value --
// so a field added to TableDef (or to a type it reaches) without being
// covered here fails this test instead of silently going unrendered.
func TestSchemaDescriptorRoundTripsThroughGeneratedSource(t *testing.T) {
	mainFixture := newTableDefFixture()
	virtualFixture := newVirtualTableDefFixture()

	requireFixturesCoverEveryField(t, mainFixture, virtualFixture)

	require.NoError(t, mainFixture.Validate(), "main fixture must itself be a valid descriptor")
	require.NoError(t, virtualFixture.Validate(), "virtual-table fixture must itself be a valid descriptor")

	roundTripDescriptors(t, mainFixture, virtualFixture)
}

// newTableDefFixture returns a schema.TableDef covering every TableDef field
// that can coexist with a primary key, indexes, and constraints: every
// column type with its own type-specific options, both generated-column
// storage kinds, a table-level primary key with AUTOINCREMENT and an ON
// CONFLICT clause, a STRICT WITHOUT ROWID table, a column-based and a
// Keys-based unique constraint, a check, an exclusion constraint, a
// column-based, an expression-based, and a Keys-based index, a foreign key
// naming every one of its own options, and a relationship matching it.
func newTableDefFixture() schema.TableDef {
	return schema.TableDef{
		Schema:  "public",
		Name:    "widgets",
		RowName: "WidgetRow",
		Columns: []schema.ColumnDef{
			{
				Name: "id",
				Type: schema.IntegerType{
					Unsigned:     true,
					DisplayWidth: schema.NewIntegerDisplayWidth(11),
					ZeroFill:     true,
				},
			},
			{
				Name:     "name",
				Type:     schema.TextType{Width: schema.NewTextWidth(255), Fixed: true},
				Nullable: true,
				Default:  "'unknown'",
			},
			{
				Name: "amount",
				Type: schema.DecimalType{
					Precision: 10,
					Scale:     schema.NewDecimalScale(2),
					Unsigned:  true,
					ZeroFill:  true,
				},
			},
			{Name: "score", Type: schema.FloatType{}},
			{Name: "active", Type: schema.BooleanType{}},
			{Name: "payload", Type: schema.BytesType{}},
			{Name: "created_at", Type: schema.TimeType{}},
			{Name: "meta", Type: schema.JSONType{}},
			{Name: "uid", Type: schema.UUIDType{}},
			{
				Name:                "full_name",
				Type:                schema.TextType{},
				GeneratedExpression: "name",
				GeneratedStorage:    schema.GeneratedStored,
			},
			{
				Name:                "display_name",
				Type:                schema.TextType{},
				GeneratedExpression: "upper(name)",
				GeneratedStorage:    schema.GeneratedVirtual,
			},
		},
		PrimaryKey:              []string{"id"},
		Strict:                  true,
		WithoutRowID:            true,
		PrimaryKeyAutoincrement: true,
		PrimaryKeyOnConflict:    schema.ConflictRollback,
		UniqueConstraints: []schema.UniqueDef{
			{
				Name:              "uq_name_amount",
				Columns:           []string{"name", "amount"},
				Deferrable:        schema.DeferrableInitiallyDeferred,
				NullsNotDistinct:  true,
				IncludeColumns:    []string{"score"},
				OnConflict:        schema.ConflictReplace,
				Temporal:          true,
				StorageParameters: map[string]string{"fillfactor": "70"},
				Tablespace:        "pg_default",
				ReplicaIdentity:   true,
				Collations:        map[string]string{"name": "en_US"},
			},
			{
				Name: "uq_via_keys",
				Keys: []schema.IndexKeyDef{
					{
						Expression:    "lower(name)",
						Descending:    true,
						Collation:     "C",
						OperatorClass: "text_ops",
						PrefixLength:  5,
						NullsOrder:    schema.NullsFirst,
					},
				},
			},
		},
		Checks: []schema.CheckDef{
			{
				Name:        "chk_amount_positive",
				Expression:  "amount > 0",
				NoInherit:   true,
				NotValid:    true,
				NotEnforced: true,
			},
		},
		ExclusionConstraints: []schema.ExclusionDef{
			{
				Name:   "excl_overlap",
				Method: schema.IndexMethod("gist"),
				Elements: []schema.ExclusionElementDef{
					{Expression: "amount", Operator: "="},
				},
				Predicate:  "amount IS NOT NULL",
				Deferrable: schema.DeferrableInitiallyImmediate,
			},
		},
		Indexes: []schema.IndexDef{
			{
				Name:              "idx_name_amount",
				Columns:           []string{"name", "amount"},
				Unique:            true,
				Method:            schema.IndexMethod("btree"),
				Predicate:         "name IS NOT NULL",
				IncludeColumns:    []string{"score"},
				Invisible:         true,
				NotValid:          true,
				StorageParameters: map[string]string{"fillfactor": "80"},
				Tablespace:        "pg_default",
				ReplicaIdentity:   true,
				NullsNotDistinct:  true,
			},
			{
				Name:        "idx_expr",
				Expressions: []string{"lower(name)", "amount * 2"},
			},
			{
				Name: "idx_keys",
				Keys: []schema.IndexKeyDef{
					{
						Expression:    "name",
						Descending:    true,
						Collation:     "C",
						OperatorClass: "text_ops",
						PrefixLength:  10,
						NullsOrder:    schema.NullsLast,
					},
				},
			},
		},
		ForeignKeys: []schema.ForeignKeyDef{
			{
				Name:              "fk_owner",
				Columns:           []string{"uid"},
				ReferencedSchema:  "public",
				ReferencedTable:   "owners",
				ReferencedColumns: []string{"id"},
				Match:             schema.MatchFull,
				OnDelete:          schema.SetNull,
				OnUpdate:          schema.Cascade,
				Deferrable:        schema.DeferrableInitiallyDeferred,
				NotValid:          true,
				NotEnforced:       true,
				Temporal:          true,
				DeleteSetColumns:  []string{"uid"},
			},
		},
		Relationships: []schema.RelationshipDef{
			{
				Name:              "owner",
				Kind:              schema.RelationshipBelongsTo,
				Columns:           []string{"uid"},
				ReferencedSchema:  "public",
				ReferencedTable:   "owners",
				ReferencedColumns: []string{"id"},
			},
		},
	}
}

// newVirtualTableDefFixture returns the SQLite virtual-table facts
// newTableDefFixture cannot carry: TableDef.Validate rejects
// VirtualTableModule stated alongside a primary key, an index, or a
// unique/check/exclusion/foreign-key constraint, since those are the
// module's own business rather than SQLite's table catalog.
func newVirtualTableDefFixture() schema.TableDef {
	return schema.TableDef{
		Schema: "main",
		Name:   "search_docs",
		Columns: []schema.ColumnDef{
			{Name: "content", Type: schema.TextType{}},
			{Name: "rank", Type: schema.TextType{}, Hidden: true},
		},
		VirtualTableModule:          "fts5",
		VirtualTableModuleArguments: []string{"content", "tokenize = 'porter'"},
	}
}

// schemaPackagePath scopes the reflection walks below (walkFieldType over
// the types, withEachCoveredFieldDropped over the values) to types declared
// in package schema, so a field of a foreign type (string, bool, a slice or
// map of those) is treated as a leaf rather than walked into.
var schemaPackagePath = reflect.TypeOf(schema.TableDef{}).PkgPath()

// requireFixturesCoverEveryField fails the test with the list of any
// exported field, reachable from schema.TableDef by walking the type
// itself, that no value in tables ever set to something other than its zero
// value. This is the self-check the task exists to have: a field added to
// TableDef, or to a type it reaches, without being covered by a fixture here
// is caught here instead of silently going unrendered by
// generate.WritePackage.
//
// It collects the names through withEachCoveredFieldDropped, which drops each
// field it names for the duration of the call and restores it again. Only the
// names matter here; the shared walk is what keeps this notion of a covered
// field the same one TestDescriptorFingerprintChangesWhenAnyFieldIsDropped
// works from.
func requireFixturesCoverEveryField(t *testing.T, tables ...schema.TableDef) {
	t.Helper()

	covered := make(map[string]bool)
	for _, table := range tables {
		withEachCoveredFieldDropped(reflect.ValueOf(&table).Elem(), func(name string) { covered[name] = true })
	}

	missing := missingRequiredFields(covered)
	require.Emptyf(t, missing,
		"these schema.TableDef fields never held a non-zero value across the fixtures: %s",
		strings.Join(missing, ", "))
}

// requiredFieldNames returns "<Type>.<Field>" for every exported field a
// fixture has to set to a non-zero value, found by walking schema.TableDef's
// own type. reflect.Type cannot discover the concrete implementations of the
// schema.ColumnType interface, so they are walked as extra roots alongside
// schema.TableDef itself.
func requiredFieldNames() map[string]bool {
	required := make(map[string]bool)
	walkFieldType(reflect.TypeOf(schema.TableDef{}), required, make(map[reflect.Type]bool))
	for _, columnType := range []schema.ColumnType{
		schema.BooleanType{}, schema.IntegerType{}, schema.FloatType{},
		schema.TextType{}, schema.BytesType{}, schema.TimeType{},
		schema.JSONType{}, schema.UUIDType{}, schema.DecimalType{},
	} {
		walkFieldType(reflect.TypeOf(columnType), required, make(map[reflect.Type]bool))
	}
	return required
}

// missingRequiredFields returns, sorted, the names in requiredFieldNames that
// covered does not hold.
func missingRequiredFields(covered map[string]bool) []string {
	missing := make([]string, 0)
	for field := range requiredFieldNames() {
		if !covered[field] {
			missing = append(missing, field)
		}
	}
	sort.Strings(missing)
	return missing
}

// walkFieldType records, into required, "<Type>.<Field>" for every exported
// field of t and of every struct type t reaches through a field, a slice or
// array element, or a map value, stopping at a type outside package schema
// and at an interface (schema.ColumnType's own concrete implementations are
// registered separately; see requiredFieldNames). visited
// prevents revisiting a struct type reached by more than one path, such as
// schema.IndexKeyDef, which both schema.IndexDef.Keys and
// schema.UniqueDef.Keys reach.
func walkFieldType(t reflect.Type, required map[string]bool, visited map[reflect.Type]bool) {
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		walkFieldType(t.Elem(), required, visited)
	case reflect.Map:
		walkFieldType(t.Elem(), required, visited)
	case reflect.Struct:
		if visited[t] {
			return
		}
		visited[t] = true
		if t.PkgPath() != schemaPackagePath {
			return
		}
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.PkgPath != "" {
				// Unexported: DecimalScale, TextWidth, and
				// IntegerDisplayWidth each carry an unexported "set" flag
				// this walk has no business reaching into. The field on
				// TableDef (or whichever struct) that holds one of these is
				// itself the thing checked for a non-zero value.
				continue
			}
			required[t.String()+"."+field.Name] = true
			walkFieldType(field.Type, required, visited)
		}
	}
}

// withEachCoveredFieldDropped calls visit once for every exported field of a
// package-schema struct anywhere in value that holds a non-zero value, naming
// it "<Type>.<Field>" the way walkFieldType does, with that one field zeroed
// for the duration of the call and restored before the walk moves on. It
// recurses through pointers, interfaces, slices, arrays, and map values to
// reach nested structs -- including a schema.ColumnType interface field's own
// concrete value, which reflect.Value (unlike reflect.Type) can follow.
//
// requireFixturesCoverEveryField wants only the names and
// TestDescriptorFingerprintChangesWhenAnyFieldIsDropped only the dropped
// value, but they share this one walk on purpose: "a field the fixtures
// cover" and "a field descriptorFingerprint is sensitive to" then name the
// same set by construction rather than by two traversals agreeing.
//
// value must be addressable, so callers start from reflect.ValueOf(&table).Elem().
// The concrete value inside an interface is not addressable even then, so a
// field inside one is dropped in an addressable copy that is stored back into
// the interface field before visit runs. A field that still cannot be set --
// anything reached through a map value, which schema has no struct behind
// today -- is skipped, and shows up as a missing name in whichever caller
// checks coverage.
func withEachCoveredFieldDropped(value reflect.Value, visit func(name string)) {
	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return
		}
		withEachCoveredFieldDropped(value.Elem(), visit)
	case reflect.Interface:
		if value.IsNil() || !value.CanSet() {
			return
		}
		concrete := reflect.New(value.Elem().Type()).Elem()
		concrete.Set(value.Elem())
		withEachCoveredFieldDropped(concrete, func(name string) {
			value.Set(concrete)
			visit(name)
		})
		value.Set(concrete)
	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			withEachCoveredFieldDropped(value.Index(i), visit)
		}
	case reflect.Map:
		for _, key := range value.MapKeys() {
			withEachCoveredFieldDropped(value.MapIndex(key), visit)
		}
	case reflect.Struct:
		t := value.Type()
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			fieldValue := value.Field(i)
			if field.PkgPath == "" && t.PkgPath() == schemaPackagePath && !fieldValue.IsZero() && fieldValue.CanSet() {
				original := reflect.New(fieldValue.Type()).Elem()
				original.Set(fieldValue)
				fieldValue.SetZero()
				visit(t.String() + "." + field.Name)
				fieldValue.Set(original)
			}
			withEachCoveredFieldDropped(fieldValue, visit)
		}
	}
}

// descriptorFingerprint returns fmt's %#v rendering of table: the complete
// textual form of the value, produced by reflection over every field of every
// struct it reaches -- unexported ones included, so schema.DecimalScale's own
// "set" flag is in it -- with map keys sorted and a nil slice printed
// differently from an empty one.
//
// It is how the descriptor a caller passed to roundTripDescriptors reaches
// the comparison inside the scratch module: writeRoundTripCheckFile embeds
// each table's fingerprint as a string constant, and the child process
// compares the fingerprint of what it read back through the generated
// accessor against it. Nothing in the fingerprint comes from the renderer
// under test, which is the point -- a field the renderer never emitted comes
// back missing and the two strings differ, where comparing rendered output
// against rendered output cannot tell the difference.
//
// TestDescriptorFingerprintChangesWhenAnyFieldIsDropped pins the property
// this relies on: no covered field can go missing without the fingerprint
// changing.
//
// The child computes its side with the same one-line expression, emitted as
// source by writeRoundTripCheckFile. The two cannot drift apart silently:
// any difference in how either side formats a value makes every table's
// comparison fail.
func descriptorFingerprint(table schema.TableDef) string {
	return fmt.Sprintf("%#v", table)
}

// TestDescriptorFingerprintChangesWhenAnyFieldIsDropped is the evidence that
// descriptorFingerprint can stand in for the descriptor itself in the
// comparison the scratch module makes: for every field the fixtures cover,
// dropping that one field changes the fingerprint. A field the fingerprint
// rendered the same way whether or not it was set would let the round-trip
// check pass on a renderer that never emitted it, which is the exact defect
// the check exists to catch.
func TestDescriptorFingerprintChangesWhenAnyFieldIsDropped(t *testing.T) {
	dropped := make(map[string]bool)
	for _, table := range []schema.TableDef{newTableDefFixture(), newVirtualTableDefFixture()} {
		baseline := descriptorFingerprint(table)
		withEachCoveredFieldDropped(reflect.ValueOf(&table).Elem(), func(name string) {
			require.NotEqualf(t, baseline, descriptorFingerprint(table),
				"dropping %s leaves the fingerprint unchanged, so the scratch module's comparison could not tell a renderer that emitted this field from one that never did", name)
			dropped[name] = true
		})
		require.Equal(t, baseline, descriptorFingerprint(table),
			"the walk must restore every field it drops")
	}

	missing := missingRequiredFields(dropped)
	require.Emptyf(t, missing,
		"dropping these schema.TableDef fields was never exercised, so nothing pins the fingerprint to them: %s",
		strings.Join(missing, ", "))
}

// descriptorAccessor names, for one table schema_gen.go declares, both the
// exported function that hands back a clone of its descriptor and the
// unexported package-level variable holding the literal that function
// clones from. writeRoundTripCheckFile needs both names to compare them
// against each other from inside the very package that declares both,
// without exporting the variable itself.
type descriptorAccessor struct {
	tableName string
	funcName  string
	varName   string
}

// descriptorAccessorPattern matches the comment and declaration
// writeTableDescriptor (internal/schemagen/schema.go) emits once per table
// in schema_gen.go: "// <Func> returns a copy of the descriptor for the
// "<table>" table.\nfunc <Func>() schema.TableDef { return <Var>.Clone()
// }". Capturing all three names from the generated source, rather than
// recomputing rasqlgen's own name-derivation rules a second time here, is
// what keeps this test honest about what rasqlgen actually named them.
var descriptorAccessorPattern = regexp.MustCompile(`(?m)^// (\w+) returns a copy of the descriptor for the "([^"]*)" table\.\n^func (\w+)\(\) schema\.TableDef \{ return (\w+)\.Clone\(\) \}$`)

// extractDescriptorAccessors returns, for each table schema_gen.go declares,
// its descriptorAccessor, sorted by table name so the generated files this
// test writes come out the same on every run.
func extractDescriptorAccessors(t *testing.T, source []byte) []descriptorAccessor {
	t.Helper()
	matches := descriptorAccessorPattern.FindAllSubmatch(source, -1)
	require.NotEmptyf(t, matches, "schema_gen.go declares no descriptor accessor:\n%s", source)
	accessors := make([]descriptorAccessor, 0, len(matches))
	for _, match := range matches {
		accessors = append(accessors, descriptorAccessor{
			tableName: string(match[2]),
			funcName:  string(match[3]),
			varName:   string(match[4]),
		})
	}
	sort.Slice(accessors, func(i, j int) bool { return accessors[i].tableName < accessors[j].tableName })
	return accessors
}

// writeRoundTripCheckFile writes a hand-written companion file into
// outputDir, the same directory generate.WritePackage just wrote the "store"
// package into. It is not part of what rasqlgen generates: it exists so the
// descriptors read back are compared inside the process that holds them as
// Go values, and it sits in that same package so it can reach each table's
// unexported literal variable (accessor.varName) directly.
//
// The expected side does cross the process boundary, as the fingerprints
// argument, and that is the one place an encoding could hide a field -- the
// way JSON's inconsistent ExclusionConstraints json:",omitempty" tag hid one
// from an earlier version of this test, which decoded JSON the child wrote
// and silently repaired the round-trip defect it was looking for (see the
// commit message this test's history carries for that finding). fmt's %#v is
// not that kind of encoding: it renders every field of every struct it
// reaches, and TestDescriptorFingerprintChangesWhenAnyFieldIsDropped holds it
// to that.
//
// The file checks each table twice, and the two failures mean different
// things:
//
//   - The accessor result's descriptorFingerprint against fingerprints[table],
//     the fingerprint of the descriptor the caller handed
//     roundTripDescriptors. This is the check that the descriptor survived
//     being written out as Go source and read back, and the only one whose
//     expected side did not itself come out of the renderer under test.
//   - The accessor result against this package's own literal variable, with
//     reflect.DeepEqual. Both sides came from the renderer, so this says
//     nothing about what was rendered. What it isolates is
//     schema.TableDef.Clone, which the accessor calls and the literal did
//     not: a clone that does not reproduce its input -- turning a nil slice
//     into an empty one, say -- fails this one on top of the first, and
//     names Clone rather than the renderer as the half that changed the
//     value.
func writeRoundTripCheckFile(t *testing.T, outputDir string, accessors []descriptorAccessor, fingerprints map[string]string) {
	t.Helper()

	var rows strings.Builder
	for _, accessor := range accessors {
		want, stated := fingerprints[accessor.tableName]
		require.Truef(t, stated,
			"schema_gen.go declares an accessor for table %q, which is not one of the tables the test rendered it from", accessor.tableName)
		rows.WriteString("\t\t{" + strconv.Quote(accessor.tableName) + ", " + strconv.Quote(want) + ", " + accessor.varName + ", " + accessor.funcName + "()},\n")
	}

	require.Equalf(t, 1, strings.Count(roundTripCheckTemplate, roundTripCheckRowsPlaceholder),
		"the companion file template must hold exactly one %q line for the per-table rows", roundTripCheckRowsPlaceholder)
	source := strings.Replace(roundTripCheckTemplate, roundTripCheckRowsPlaceholder, rows.String(), 1)

	require.NoError(t, os.WriteFile(filepath.Join(outputDir, "descriptor_round_trip_check.go"), []byte(source), 0o600))
}

// roundTripCheckRowsPlaceholder is the line in roundTripCheckTemplate
// writeRoundTripCheckFile replaces with one struct literal row per table.
const roundTripCheckRowsPlaceholder = "\t\t// ROWS\n"

// roundTripCheckTemplate is everything in the companion file that does not
// depend on which tables were rendered. It is source this test writes, not
// source rasqlgen generates, and it never reaches the repository: it is
// written into the scratch module beside the generated package.
const roundTripCheckTemplate = `package store

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/lestrrat-go/rasql/schema"
)

// RunDescriptorRoundTripCheck reports "PASS" when every generated descriptor
// accessor in this package hands back the descriptor the test rendered this
// package from, or a "FAIL" report naming which table did not survive
// otherwise.
func RunDescriptorRoundTripCheck() string {
	var failures []string
	for _, check := range []struct {
		name    string
		want    string
		literal schema.TableDef
		got     schema.TableDef
	}{
		// ROWS
	} {
		if got := descriptorFingerprint(check.got); got != check.want {
			failures = append(failures, fmt.Sprintf("%s: the descriptor read back through the accessor is not the descriptor the test rendered this package from\n%s", check.name, fingerprintDifference(check.want, got)))
		}
		if !reflect.DeepEqual(check.literal, check.got) {
			failures = append(failures, fmt.Sprintf("%s: the accessor result is not reflect.DeepEqual to the literal it clones\n%s", check.name, fingerprintDifference(descriptorFingerprint(check.literal), descriptorFingerprint(check.got))))
		}
	}
	if len(failures) == 0 {
		return "PASS"
	}
	return "FAIL\n" + strings.Join(failures, "\n")
}

// descriptorFingerprint must render a descriptor exactly the way the test's
// own descriptorFingerprint (generate/descriptor_roundtrip_test.go) does,
// since the want strings above came from there.
func descriptorFingerprint(definition schema.TableDef) string {
	return fmt.Sprintf("%#v", definition)
}

// fingerprintDifference reports where want and got first differ, with the
// same window of each around that point. A descriptor fingerprint runs to
// thousands of characters, and printing two of them in full buries the one
// field that did not survive.
func fingerprintDifference(want, got string) string {
	shared := 0
	for shared < len(want) && shared < len(got) && want[shared] == got[shared] {
		shared++
	}
	start := max(shared-60, 0)
	return fmt.Sprintf("  first difference at byte %d\n  want: %s\n  got:  %s",
		shared, fingerprintWindow(want, start), fingerprintWindow(got, start))
}

// fingerprintWindow returns up to 180 bytes of value from start, marking
// either end it had to cut with an ellipsis.
func fingerprintWindow(value string, start int) string {
	end := min(start+180, len(value))
	window := value[start:end]
	if start > 0 {
		window = "..." + window
	}
	if end < len(value) {
		window += "..."
	}
	return window
}
`

// writeReadbackProgram writes cmd/readback/main.go into moduleDir: a small
// package main that calls every accessor in accessors, renders the results
// through generate.DescriptorSource -- the same entry point
// roundTripDescriptors itself used for the first rendering -- and writes
// that Go source to its own stdout, so the parent process can compare it,
// byte-for-byte, as text: the artifact actually under test, which no struct
// tag or encoding step can mask a field in. It writes
// store.RunDescriptorRoundTripCheck()'s result to its own stderr on a line
// of its own, keeping the two checks on separate streams so neither can be
// mistaken for the other.
func writeReadbackProgram(t *testing.T, moduleDir string, accessors []descriptorAccessor) {
	t.Helper()
	programDir := filepath.Join(moduleDir, "cmd", "readback")
	require.NoError(t, os.MkdirAll(programDir, 0o700))

	var source strings.Builder
	source.WriteString("package main\n\n")
	source.WriteString("import (\n\t\"fmt\"\n\t\"os\"\n\n\t\"github.com/lestrrat-go/rasql/generate\"\n\t\"github.com/lestrrat-go/rasql/schema\"\n\n\t\"example.com/consumer/internal/store\"\n)\n\n")
	source.WriteString("func main() {\n")
	source.WriteString("\ttables := []schema.TableDef{\n")
	for _, accessor := range accessors {
		source.WriteString("\t\tstore." + accessor.funcName + "(),\n")
	}
	source.WriteString("\t}\n")
	source.WriteString("\trendered, err := generate.DescriptorSource(\"store\", tables...)\n")
	source.WriteString("\tif err != nil {\n\t\tfmt.Fprintln(os.Stderr, \"render error:\", err)\n\t\tos.Exit(1)\n\t}\n")
	source.WriteString("\tif _, err := os.Stdout.Write(rendered); err != nil {\n\t\tfmt.Fprintln(os.Stderr, \"write error:\", err)\n\t\tos.Exit(1)\n\t}\n")
	source.WriteString("\tfmt.Fprintln(os.Stderr, store.RunDescriptorRoundTripCheck())\n")
	source.WriteString("}\n")

	require.NoError(t, os.WriteFile(filepath.Join(programDir, "main.go"), []byte(source.String()), 0o600))
}

// runGo runs "go" args... in dir with env, requiring it to succeed, for a
// caller that only cares whether the command succeeded (such as "go
// build").
func runGo(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), "go", args...)
	command.Dir = dir
	command.Env = env
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "go %s output:\n%s", strings.Join(args, " "), output)
}

// runGoOutput is runGo for a caller that needs the command's stdout and
// stderr kept apart, such as "go run" here: stdout carries the rendered Go
// source the child writes (see writeReadbackProgram), stderr carries the
// plain-text structural check result.
func runGoOutput(t *testing.T, dir string, env []string, args ...string) (stdout, stderr []byte) {
	t.Helper()
	command := exec.CommandContext(t.Context(), "go", args...)
	command.Dir = dir
	command.Env = env
	var stdoutBuf, stderrBuf bytes.Buffer
	command.Stdout = &stdoutBuf
	command.Stderr = &stderrBuf
	err := command.Run()
	require.NoErrorf(t, err, "go %s stderr:\n%s", strings.Join(args, " "), stderrBuf.String())
	return stdoutBuf.Bytes(), stderrBuf.Bytes()
}

// requireGeneratorDerivesNoRelationship fails unless every foreign key in
// tables is already claimed by a relationship the caller stated. rasqlgen
// does not render the descriptor it was handed: prepareSchema
// (internal/schemagen/schema.go) replaces Relationships with
// mergeRelationships's result first, which walks the stated relationships,
// lets each claim at most one foreign key it matches, and then appends a
// derived belongs-to relationship for every foreign key left unclaimed. That
// derivation is the one way the descriptor read back out may legitimately
// differ from the descriptor passed in, so roundTripDescriptors can only
// compare against its own input where nothing is derived, and this is what
// holds that. The rest of the merge cannot survive rendering and needs no
// accounting: it makes a nil Relationships slice non-nil, and
// writeTableDefLiteral emits a slice field only when it is non-empty, so an
// empty one comes back nil either way.
//
// A caller whose tables do need a derived relationship has to state the
// expected descriptors itself instead of reusing its input, and gets a
// failure here rather than a comparison that quietly proves less.
func requireGeneratorDerivesNoRelationship(t *testing.T, tables ...schema.TableDef) {
	t.Helper()

	for _, table := range tables {
		claimed := make([]bool, len(table.ForeignKeys))
		for _, relationship := range table.Relationships {
			for index, key := range table.ForeignKeys {
				if claimed[index] || !relationshipClaimsForeignKey(relationship, key) {
					continue
				}
				claimed[index] = true
				break
			}
		}
		for index, key := range table.ForeignKeys {
			require.Truef(t, claimed[index],
				"table %q: no stated relationship matches foreign key %q, so rasqlgen derives one and the descriptor read back is not this input",
				table.Name, key.Name)
		}
	}
}

// relationshipClaimsForeignKey mirrors relationshipMatchesForeignKey
// (internal/schemagen/schema.go), the test of a match mergeRelationships
// itself applies.
func relationshipClaimsForeignKey(relationship schema.RelationshipDef, key schema.ForeignKeyDef) bool {
	return relationship.ReferencedSchema == key.ReferencedSchema &&
		relationship.ReferencedTable == key.ReferencedTable &&
		slices.Equal(relationship.Columns, key.Columns) &&
		slices.Equal(relationship.ReferencedColumns, key.ReferencedColumns)
}

// roundTripDescriptors renders tables into a scratch consumer module the
// same way generate.WritePackage always has (see
// TestGeneratedStorePackageCompilesAndRuns in acceptance_test.go for the
// same scratch-module mechanics: copy this repository's own go.mod and
// go.sum, append a replace directive back to this checkout, and run the
// child "go" command with an explicit environment -- GOPROXY=off so it
// never reaches the network, and the rest of os.Environ() inherited so it
// keeps GOCACHE along with PATH and HOME), compiles the module, and runs a
// tiny program that reads every table's descriptor back out through its
// generated accessor.
//
// Two checks run inside that child process rather than by shipping a
// descriptor back to this one. What does cross into the child is each
// table's descriptorFingerprint, as a string constant;
// writeRoundTripCheckFile says why that is not the kind of hop that can hide
// a field, as JSON's ExclusionConstraints json:",omitempty" tag once did.
//
//   - The child calls store.RunDescriptorRoundTripCheck(), a hand-written
//     companion file writeRoundTripCheckFile placed beside the generated
//     files, and writes "PASS" or a "FAIL" report to its own stderr. This
//     function requires that to read "PASS". That check is what compares
//     what came back against tables, the descriptors this function was
//     handed: each one's descriptorFingerprint travels into the scratch
//     module as a string constant, and the child fingerprints its accessor
//     result the same way. Both sides of that comparison are values, not
//     rendered source, so a field the renderer never emits fails it -- which
//     comparing one rendering against another cannot do.
//   - The child also renders the descriptors it read back through
//     generate.DescriptorSource -- the same entry point this function used
//     for the first rendering -- and writes that Go source to its own
//     stdout. This function requires it byte-for-byte identical to the first
//     rendering. Both sides come from the renderer, so this is the
//     idempotence check alone: rendering a descriptor rasqlgen already
//     rendered reproduces it exactly.
//
// Comparing what came back against tables verbatim holds only because
// requireGeneratorDerivesNoRelationship refuses the one input rasqlgen
// normalizes; see it for what that is.
func roundTripDescriptors(t *testing.T, tables ...schema.TableDef) {
	t.Helper()

	requireGeneratorDerivesNoRelationship(t, tables...)

	fingerprints := make(map[string]string, len(tables))
	for _, table := range tables {
		_, duplicate := fingerprints[table.Name]
		require.Falsef(t, duplicate,
			"two tables are named %q, so their descriptors cannot be told apart by name", table.Name)
		fingerprints[table.Name] = descriptorFingerprint(table)
	}

	moduleDir := t.TempDir()

	repoGoMod, err := os.ReadFile(filepath.Join("..", "go.mod"))
	require.NoError(t, err)
	repository, err := filepath.Abs("..")
	require.NoError(t, err)
	module := strings.Replace(string(repoGoMod), "module github.com/lestrrat-go/rasql\n", "module example.com/consumer\n", 1)
	module += "\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(repository) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte(module), 0o600))

	repoGoSum, err := os.ReadFile(filepath.Join("..", "go.sum"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "go.sum"), repoGoSum, 0o600))

	outputDir := filepath.Join(moduleDir, "internal", "store")
	require.NoError(t, os.MkdirAll(outputDir, 0o700))
	require.NoError(t, generate.WritePackage("store", outputDir, tables...))

	firstRender, err := os.ReadFile(filepath.Join(outputDir, "schema_gen.go"))
	require.NoError(t, err)

	accessors := extractDescriptorAccessors(t, firstRender)
	require.Lenf(t, accessors, len(tables),
		"schema_gen.go must declare one descriptor accessor per table rendered, so every table passed in is checked")
	writeRoundTripCheckFile(t, outputDir, accessors, fingerprints)
	writeReadbackProgram(t, moduleDir, accessors)

	// -buildvcs=false: the scratch module lives in t.TempDir(), which is
	// never itself a VCS checkout, but Go's VCS auto-detection has been
	// observed here to still try (and fail) to run "git status" -- see the
	// commit message for the exact symptom. The scratch module needs no VCS
	// stamp regardless, so this sidesteps the detection entirely rather than
	// depending on it correctly deciding there is nothing to stamp.
	env := append(os.Environ(), "GOPROXY=off")
	runGo(t, moduleDir, env, "build", "-buildvcs=false", "./...")
	secondRender, checkOutput := runGoOutput(t, moduleDir, env, "run", "-buildvcs=false", "./cmd/readback")

	checkResult := strings.TrimRight(string(checkOutput), "\n")
	require.Equal(t, "PASS", checkResult,
		"the scratch module's own child process compared each descriptor it read back against the one this test rendered the module from, and reported a difference")

	require.Equal(t, string(firstRender), string(secondRender),
		"rendering the descriptor read back from the compiled scratch module must byte-for-byte match the first rendering")
}
