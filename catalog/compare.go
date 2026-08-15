package catalog

import (
	"reflect"

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
