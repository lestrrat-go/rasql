package catalog

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/lestrrat-go/rasql/schema"
)

// normalize returns a copy of table suitable for the drift verdict: RowName
// and Relationships cleared, since a generator states both and no database
// ever will, and every zero-length slice or map folded to nil, at every
// depth, since a generated store can never render one but inspection
// sometimes returns one. See Drift's own doc comment for why exactly these
// three rules, and no others.
func normalize(table schema.TableDef) schema.TableDef {
	table = table.Clone()
	table.RowName = ""
	table.Relationships = nil
	foldZeroLengthContainers(reflect.ValueOf(&table).Elem())
	return table
}

// foldZeroLengthContainers walks v reflectively -- every struct field, every
// slice element, the target of a non-nil pointer or interface -- and sets
// every settable, non-nil, zero-length slice or map it finds to its zero
// value (nil), at every depth. It is unconditional and names no field, so a
// field added to any descriptor type tomorrow is folded exactly like every
// field that exists today, with no list to fall behind.
func foldZeroLengthContainers(v reflect.Value) {
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			foldZeroLengthContainers(v.Field(i))
		}
	case reflect.Slice:
		if v.Len() == 0 {
			if v.CanSet() && !v.IsNil() {
				v.Set(reflect.Zero(v.Type()))
			}
			return
		}
		for i := 0; i < v.Len(); i++ {
			foldZeroLengthContainers(v.Index(i))
		}
	case reflect.Map:
		if v.Len() == 0 && v.CanSet() && !v.IsNil() {
			v.Set(reflect.Zero(v.Type()))
		}
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			foldZeroLengthContainers(v.Elem())
		}
	}
}

// indexPair is one matched element, named by its position in each of the
// two slices match was given.
type indexPair struct {
	oldIndex int
	newIndex int
}

// matchResult is what match found: every paired element, by position, and
// the positions left over on each side.
type matchResult struct {
	pairs   []indexPair
	added   []int // positions in new that no old element paired with
	removed []int // positions in old that no new element paired with
}

// match pairs each element of old with an element of new and reports what is
// left over. It is the one matcher used at every level a comparison needs
// to pair elements: the table list, and (from PR 2 on) every list of struct
// elements inside a table.
//
//  1. By stated name. An element's identity is its Name field, when its type
//     has one and it is a non-empty string, prefixed with "Schema." when the
//     type also has a non-empty Schema string field -- which today only
//     schema.TableDef does, so this qualifies tables and nothing else. An
//     identity appearing exactly once on each side pairs those two elements.
//     An identity appearing more than once on either side pairs nothing at
//     this step and falls through to steps 2 and 3: nothing is ever
//     silently collapsed onto a repeated or empty identity.
//  2. By whole-value equality. Among what step 1 left unpaired, each
//     unpaired old element, walked in order, is paired with the first
//     unpaired new element that is reflect.DeepEqual to it.
//  3. Whatever remains is reported: an unpaired old position as removed, an
//     unpaired new position as added.
func match[T any](old, new []T) matchResult {
	oldIdentity := make([]string, len(old))
	oldHasIdentity := make([]bool, len(old))
	oldIdentityCount := make(map[string]int, len(old))
	for i := range old {
		oldIdentity[i], oldHasIdentity[i] = identity(reflect.ValueOf(old[i]))
		if oldHasIdentity[i] {
			oldIdentityCount[oldIdentity[i]]++
		}
	}
	newIdentity := make([]string, len(new))
	newHasIdentity := make([]bool, len(new))
	newIdentityCount := make(map[string]int, len(new))
	for i := range new {
		newIdentity[i], newHasIdentity[i] = identity(reflect.ValueOf(new[i]))
		if newHasIdentity[i] {
			newIdentityCount[newIdentity[i]]++
		}
	}

	oldPaired := make([]bool, len(old))
	newPaired := make([]bool, len(new))
	var result matchResult

	// Step 1: by stated name, exactly once on each side.
	for oi := range old {
		if !oldHasIdentity[oi] || oldIdentityCount[oldIdentity[oi]] != 1 {
			continue
		}
		if newIdentityCount[oldIdentity[oi]] != 1 {
			continue
		}
		for ni := range new {
			if !newPaired[ni] && newHasIdentity[ni] && newIdentity[ni] == oldIdentity[oi] {
				oldPaired[oi] = true
				newPaired[ni] = true
				result.pairs = append(result.pairs, indexPair{oldIndex: oi, newIndex: ni})
				break
			}
		}
	}

	// Step 2: by whole-value equality, among what step 1 left unpaired.
	for oi := range old {
		if oldPaired[oi] {
			continue
		}
		for ni := range new {
			if newPaired[ni] {
				continue
			}
			if reflect.DeepEqual(old[oi], new[ni]) {
				oldPaired[oi] = true
				newPaired[ni] = true
				result.pairs = append(result.pairs, indexPair{oldIndex: oi, newIndex: ni})
				break
			}
		}
	}

	// Step 3: whatever remains.
	for oi := range old {
		if !oldPaired[oi] {
			result.removed = append(result.removed, oi)
		}
	}
	for ni := range new {
		if !newPaired[ni] {
			result.added = append(result.added, ni)
		}
	}
	return result
}

// identity computes v's matching identity for match's step 1: v's Name
// field, when v's type has one and it is a non-empty string, prefixed with
// "Schema." when v's type also has a non-empty Schema string field. v must
// be a struct.
func identity(v reflect.Value) (string, bool) {
	nameField := v.FieldByName("Name")
	if !nameField.IsValid() || nameField.Kind() != reflect.String {
		return "", false
	}
	name := nameField.String()
	if name == "" {
		return "", false
	}
	if schemaField := v.FieldByName("Schema"); schemaField.IsValid() && schemaField.Kind() == reflect.String {
		if schemaValue := schemaField.String(); schemaValue != "" {
			return schemaValue + "." + name, true
		}
	}
	return name, true
}

// --- The explanation ---------------------------------------------------
//
// Everything below this line builds the change lines a TableDrift reports,
// from the same two normalized descriptors the verdict in drift.go already
// compared. None of it decides whether a table drifted; §4.2 already did.

// listFieldLabel names the SQL-prose kind of element each of
// schema.TableDef's own list-of-struct fields holds, keyed by the Go field
// name. A list field with no entry here falls back to its own Go field
// name (see labelFor) rather than being described less well silently --
// see §4.6 of the phase 4 design for why a general prose generator is
// deliberately not used instead.
var listFieldLabel = map[string]string{
	"Columns":              "column",
	"UniqueConstraints":    "unique constraint",
	"Checks":               "check",
	"ExclusionConstraints": "exclusion constraint",
	"Indexes":              "index",
	"ForeignKeys":          "foreign key",
	"Relationships":        "relationship",
}

// labelFor returns fieldName's SQL-prose kind, or fieldName itself when
// listFieldLabel has no entry for it.
func labelFor(fieldName string) string {
	if label, ok := listFieldLabel[fieldName]; ok {
		return label
	}
	return fieldName
}

// tableChanges builds every Change between describedNorm and liveNorm, two
// already-normalized descriptors §4.2 has already found unequal. It walks
// schema.TableDef's own fields in declaration order, skipping Schema and
// Name (the identity the two descriptors were matched on, guaranteed equal
// for any pair Drift hands here -- see §4.5). A field whose Go type is a
// slice of structs is a list field, compared elementwise by
// diffListField; every other field is one of the table's own facts,
// compared by diffValue and collected into one leading Change with an
// empty Subject.
//
// The result is never empty: when the per-field walk finds nothing to say
// about a pair the verdict already called different, one Change naming the
// two whole descriptors is appended instead of returning silence -- the
// safety net §2.1 of the phase 4 design rests the whole split on.
func tableChanges(describedNorm, liveNorm schema.TableDef) []Change {
	describedValue := reflect.ValueOf(describedNorm)
	liveValue := reflect.ValueOf(liveNorm)
	tableType := describedValue.Type()

	var facts []FieldChange
	var changes []Change
	for i := 0; i < tableType.NumField(); i++ {
		fieldName := tableType.Field(i).Name
		if fieldName == "Schema" || fieldName == "Name" {
			continue
		}
		describedField, liveField := describedValue.Field(i), liveValue.Field(i)
		if describedField.Kind() == reflect.Slice && describedField.Type().Elem().Kind() == reflect.Struct {
			changes = append(changes, diffListField(labelFor(fieldName), describedField, liveField)...)
			continue
		}
		diffValue(fieldName, describedField, liveField, &facts)
	}

	if len(facts) > 0 {
		changes = append([]Change{{Kind: ChangeModified, Fields: facts}}, changes...)
	}

	if len(changes) == 0 && !valuesEqual(describedValue, liveValue) {
		changes = []Change{{
			Kind: ChangeModified,
			Fields: []FieldChange{
				{Path: "descriptor", Was: render(describedValue), Now: render(liveValue)},
			},
		}}
	}

	return changes
}

// diffListField compares one list-of-struct field's old and new slice
// values, pairs their elements with matchValues (§4.3), and returns one
// Change per addition, removal, modification, or move, in the order §4.5
// requires: every addition, then every removal, then every modification,
// then every move, each group sorted by Subject.
func diffListField(label string, old, new reflect.Value) []Change {
	m := matchValues(old, new)

	var added, removed, modified, moved []Change
	for _, oi := range m.removed {
		removed = append(removed, Change{Kind: ChangeRemoved, Subject: elementSubject(label, old.Index(oi))})
	}
	for _, ni := range m.added {
		added = append(added, Change{Kind: ChangeAdded, Subject: elementSubject(label, new.Index(ni))})
	}
	for _, p := range m.pairs {
		was, now := old.Index(p.oldIndex), new.Index(p.newIndex)
		subject := elementSubject(label, was)
		if valuesEqual(was, now) {
			if p.oldIndex != p.newIndex {
				moved = append(moved, Change{
					Kind:    ChangeMoved,
					Subject: subject,
					Fields:  []FieldChange{{Path: "position", Was: strconv.Itoa(p.oldIndex), Now: strconv.Itoa(p.newIndex)}},
				})
			}
			continue
		}
		var fields []FieldChange
		diffValue("", was, now, &fields)
		modified = append(modified, Change{Kind: ChangeModified, Subject: subject, Fields: fields})
	}

	sortChangesBySubject(added)
	sortChangesBySubject(removed)
	sortChangesBySubject(modified)
	sortChangesBySubject(moved)

	var out []Change
	out = append(out, added...)
	out = append(out, removed...)
	out = append(out, modified...)
	out = append(out, moved...)
	return out
}

func sortChangesBySubject(changes []Change) {
	sort.Slice(changes, func(i, j int) bool { return changes[i].Subject < changes[j].Subject })
}

// elementSubject names one element of a list field for a Change's Subject:
// `label "name"` when the element states a non-empty Name, or label
// followed by the element's own complete rendering when it does not. An
// unnamed element is never given a synthesized name -- naming it after some
// of its fields is exactly the ambiguity §2.5 point 3 forbids -- so two
// different unnamed elements can never render the same Subject.
func elementSubject(label string, v reflect.Value) string {
	if name, ok := identity(v); ok {
		return fmt.Sprintf("%s %q", label, name)
	}
	return label + " " + render(v)
}

// matchValues is match's reflect-based twin, used where the element type is
// not known until runtime: one list field inside a table, such as
// UniqueConstraints or ForeignKeys. It follows the same three steps as
// match (see match's own doc): pair by stated name where an identity
// appears exactly once on each side, then pair what remains by whole-value
// equality, then report whatever is left over as added or removed.
func matchValues(old, new reflect.Value) matchResult {
	oldLen, newLen := old.Len(), new.Len()
	oldIdentity := make([]string, oldLen)
	oldHasIdentity := make([]bool, oldLen)
	oldIdentityCount := make(map[string]int, oldLen)
	for i := 0; i < oldLen; i++ {
		oldIdentity[i], oldHasIdentity[i] = identity(old.Index(i))
		if oldHasIdentity[i] {
			oldIdentityCount[oldIdentity[i]]++
		}
	}
	newIdentity := make([]string, newLen)
	newHasIdentity := make([]bool, newLen)
	newIdentityCount := make(map[string]int, newLen)
	for i := 0; i < newLen; i++ {
		newIdentity[i], newHasIdentity[i] = identity(new.Index(i))
		if newHasIdentity[i] {
			newIdentityCount[newIdentity[i]]++
		}
	}

	oldPaired := make([]bool, oldLen)
	newPaired := make([]bool, newLen)
	var result matchResult

	// Step 1: by stated name, exactly once on each side.
	for oi := 0; oi < oldLen; oi++ {
		if !oldHasIdentity[oi] || oldIdentityCount[oldIdentity[oi]] != 1 {
			continue
		}
		if newIdentityCount[oldIdentity[oi]] != 1 {
			continue
		}
		for ni := 0; ni < newLen; ni++ {
			if !newPaired[ni] && newHasIdentity[ni] && newIdentity[ni] == oldIdentity[oi] {
				oldPaired[oi] = true
				newPaired[ni] = true
				result.pairs = append(result.pairs, indexPair{oldIndex: oi, newIndex: ni})
				break
			}
		}
	}

	// Step 2: by whole-value equality, among what step 1 left unpaired.
	for oi := 0; oi < oldLen; oi++ {
		if oldPaired[oi] {
			continue
		}
		for ni := 0; ni < newLen; ni++ {
			if newPaired[ni] {
				continue
			}
			if valuesEqual(old.Index(oi), new.Index(ni)) {
				oldPaired[oi] = true
				newPaired[ni] = true
				result.pairs = append(result.pairs, indexPair{oldIndex: oi, newIndex: ni})
				break
			}
		}
	}

	// Step 3: whatever remains.
	for oi := 0; oi < oldLen; oi++ {
		if !oldPaired[oi] {
			result.removed = append(result.removed, oi)
		}
	}
	for ni := 0; ni < newLen; ni++ {
		if !newPaired[ni] {
			result.added = append(result.added, ni)
		}
	}
	return result
}

// valuesEqual reports whether was and now hold equal values, walked
// reflectively field by field, element by element, or entry by entry. It
// exists because was and now can be reflect.Values reached by drilling into
// an unexported struct field (schema.DecimalScale, schema.TextWidth, and
// schema.IntegerDisplayWidth all keep their state unexported), and calling
// Value.Interface() on such a value panics -- so, unlike match's own use of
// reflect.DeepEqual on whole, freshly-obtained Go values, this walk reads
// every leaf through a Kind-specific accessor and never calls Interface().
func valuesEqual(was, now reflect.Value) bool {
	if was.Type() != now.Type() {
		return false
	}
	switch was.Kind() {
	case reflect.Struct:
		for i := 0; i < was.NumField(); i++ {
			if !valuesEqual(was.Field(i), now.Field(i)) {
				return false
			}
		}
		return true
	case reflect.Slice:
		if was.IsNil() != now.IsNil() {
			return false
		}
		fallthrough
	case reflect.Array:
		if was.Len() != now.Len() {
			return false
		}
		for i := 0; i < was.Len(); i++ {
			if !valuesEqual(was.Index(i), now.Index(i)) {
				return false
			}
		}
		return true
	case reflect.Map:
		if was.IsNil() != now.IsNil() {
			return false
		}
		if was.Len() != now.Len() {
			return false
		}
		iter := was.MapRange()
		for iter.Next() {
			nowValue := now.MapIndex(iter.Key())
			if !nowValue.IsValid() || !valuesEqual(iter.Value(), nowValue) {
				return false
			}
		}
		return true
	case reflect.Pointer, reflect.Interface:
		if was.IsNil() || now.IsNil() {
			return was.IsNil() == now.IsNil()
		}
		if was.Kind() == reflect.Interface && was.Elem().Type() != now.Elem().Type() {
			return false
		}
		return valuesEqual(was.Elem(), now.Elem())
	case reflect.String:
		return was.String() == now.String()
	case reflect.Bool:
		return was.Bool() == now.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return was.Int() == now.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return was.Uint() == now.Uint()
	case reflect.Float32, reflect.Float64:
		return was.Float() == now.Float()
	default:
		return false
	}
}

// diffValue is the leaf walk (§4.4): it appends one FieldChange to out for
// every difference it finds between was and now, extending path as it
// recurses. It is total -- there is no branch that recurses into something
// and finds nothing to report, and no reflect.Kind it does not handle --
// because the last case in the switch names the whole node whenever none of
// the earlier, more specific cases apply.
func diffValue(path string, was, now reflect.Value, out *[]FieldChange) {
	if valuesEqual(was, now) {
		return
	}

	switch {
	case was.Kind() == reflect.Struct && now.Kind() == reflect.Struct &&
		was.Type() == now.Type() && hasOnlyExportedFields(was.Type()):
		for i := 0; i < was.NumField(); i++ {
			fieldName := was.Type().Field(i).Name
			diffValue(joinPath(path, fieldName), was.Field(i), now.Field(i), out)
		}
	case isNonNilPointerOrInterface(was) && isNonNilPointerOrInterface(now) &&
		was.Elem().Type() == now.Elem().Type():
		diffValue(path, was.Elem(), now.Elem(), out)
	case was.Kind() == reflect.Slice && now.Kind() == reflect.Slice &&
		was.Type() == now.Type() && was.Type().Elem().Kind() == reflect.Struct:
		common := was.Len()
		if now.Len() < common {
			common = now.Len()
		}
		for i := 0; i < common; i++ {
			diffValue(fmt.Sprintf("%s[%d]", path, i), was.Index(i), now.Index(i), out)
		}
		for i := common; i < was.Len(); i++ {
			*out = append(*out, FieldChange{Path: fmt.Sprintf("%s[%d]", path, i), Was: render(was.Index(i)), Now: "(none)"})
		}
		for i := common; i < now.Len(); i++ {
			*out = append(*out, FieldChange{Path: fmt.Sprintf("%s[%d]", path, i), Was: "(none)", Now: render(now.Index(i))})
		}
	default:
		*out = append(*out, FieldChange{Path: path, Was: render(was), Now: render(now)})
	}
}

// hasOnlyExportedFields reports whether every field of t is exported.
func hasOnlyExportedFields(t reflect.Type) bool {
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).PkgPath != "" {
			return false
		}
	}
	return true
}

// isNonNilPointerOrInterface reports whether v is a non-nil pointer or
// interface.
func isNonNilPointerOrInterface(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		return !v.IsNil()
	default:
		return false
	}
}

// joinPath extends path with name, the way diffValue names a nested field:
// "Nullable" at the top of a subject, "Type.Unsigned" one level down.
func joinPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// render produces the complete, deterministic text form used for a
// FieldChange's Was and Now and for an unnamed element's Subject (§4.4). A
// field is omitted from a struct's rendering only when it holds its zero
// value, so two renderings are equal only when the two values are equal --
// the invariant that makes two different unnamed constraints unable to ever
// print the same Subject.
func render(v reflect.Value) string {
	switch v.Kind() {
	case reflect.String:
		return strconv.Quote(v.String())
	case reflect.Bool:
		return strconv.FormatBool(v.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(v.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'g', -1, 64)
	case reflect.Pointer:
		if v.IsNil() {
			return "nil"
		}
		return render(v.Elem())
	case reflect.Interface:
		if v.IsNil() {
			return "nil"
		}
		elem := v.Elem()
		return elem.Type().Name() + render(elem)
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return "nil"
		}
		elements := make([]string, v.Len())
		for i := 0; i < v.Len(); i++ {
			elements[i] = render(v.Index(i))
		}
		return "[" + strings.Join(elements, " ") + "]"
	case reflect.Map:
		if v.IsNil() {
			return "nil"
		}
		keys := v.MapKeys()
		type entry struct {
			key, text string
		}
		entries := make([]entry, len(keys))
		for i, key := range keys {
			entries[i] = entry{key: render(key), text: render(key) + ":" + render(v.MapIndex(key))}
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
		texts := make([]string, len(entries))
		for i, e := range entries {
			texts[i] = e.text
		}
		return "{" + strings.Join(texts, " ") + "}"
	case reflect.Struct:
		structType := v.Type()
		var fields []string
		for i := 0; i < structType.NumField(); i++ {
			fieldValue := v.Field(i)
			if fieldValue.IsZero() {
				continue
			}
			fields = append(fields, structType.Field(i).Name+":"+render(fieldValue))
		}
		return "{" + strings.Join(fields, " ") + "}"
	default:
		return fmt.Sprintf("%v", v)
	}
}
