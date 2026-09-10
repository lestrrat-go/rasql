// Package gensum owns the format of rasql.sum: the small text file rasql codegen generate writes
// beside the store it emits, and the one rasql codegen check reads to say whether the inputs it
// controls and the outputs it wrote are unchanged since the last generation.
//
// This package only encodes, parses, and compares. It opens no database, reads no other file, and
// knows nothing about how a Value is computed; a caller supplies already-hashed strings.
package gensum

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
)

// version is the only header rasql.sum v1 accepts. A file whose header names any other version is
// refused by Parse rather than guessed at.
const version = "v1"

// Entry is one named value within a group: a migration ID and the checksum migrate recorded for
// it, or a file path and the sha256 digest of its contents.
type Entry struct {
	Name  string
	Value string
}

// File is the decoded form of rasql.sum. Migrations is empty when no migration directory is
// configured; Queries and Outputs are empty only when a store declares no queries or writes no
// files, which does not happen in practice but is not refused here.
type File struct {
	Dialect  string
	Profile  string
	Settings string

	Migrations []Entry
	Queries    []Entry
	Outputs    []Entry
}

// Difference names one group that differs between a recorded rasql.sum and a freshly computed
// File, and, for a group of entries, the first differing name or path within it. Path is empty for
// the dialect, profile, and settings groups, which each carry a single value rather than a list.
type Difference struct {
	Group string
	Path  string
}

// Encode renders f as rasql.sum: one record per line, fields separated by single spaces, entries
// sorted by name within each group, groups in the fixed order dialect, profile, settings,
// migration, query, output. Encode does not validate f; a caller that supplies an empty Dialect,
// Profile, or Settings gets a header field with no value written back, which Parse then refuses to
// read.
func Encode(f File) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "rasql.sum %s\n", version)
	fmt.Fprintf(&b, "dialect %s\n", f.Dialect)
	fmt.Fprintf(&b, "profile %s\n", f.Profile)
	fmt.Fprintf(&b, "settings %s\n", f.Settings)
	for _, e := range sortedEntries(f.Migrations) {
		fmt.Fprintf(&b, "migration %s %s\n", e.Name, e.Value)
	}
	for _, e := range sortedEntries(f.Queries) {
		fmt.Fprintf(&b, "query %s %s\n", e.Name, e.Value)
	}
	for _, e := range sortedEntries(f.Outputs) {
		fmt.Fprintf(&b, "output %s %s\n", e.Name, e.Value)
	}
	return b.Bytes()
}

func sortedEntries(entries []Entry) []Entry {
	out := append([]Entry(nil), entries...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// group orders the sections of rasql.sum. header exists only to anchor the version line; every
// other line belongs to exactly one of the remaining groups, and a valid file visits them in this
// order, each at most once except migration, query, and output, which repeat.
type group int

const (
	groupHeader group = iota
	groupDialect
	groupProfile
	groupSettings
	groupMigration
	groupQuery
	groupOutput
)

// Parse decodes rasql.sum, refusing an unrecognized version, a malformed record (a line with the
// wrong number of single-space-separated fields, a tab, or a trailing space), and a file whose
// groups or whose entries within a group are out of order.
func Parse(b []byte) (File, error) {
	text := string(b)
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return File{}, fmt.Errorf("gensum: empty rasql.sum")
	}
	lines := strings.Split(text, "\n")
	header, err := splitFields(lines[0], 2)
	if err != nil {
		return File{}, fmt.Errorf("gensum: header: %w", err)
	}
	if header[0] != "rasql.sum" {
		return File{}, fmt.Errorf("gensum: not a rasql.sum file")
	}
	if header[1] != version {
		return File{}, fmt.Errorf("gensum: unsupported rasql.sum version %q", header[1])
	}

	var f File
	last := groupHeader
	lastName := map[group]string{}
	for _, line := range lines[1:] {
		keyword, _, ok := strings.Cut(line, " ")
		if !ok {
			return File{}, fmt.Errorf("gensum: malformed record %q", line)
		}
		g, width, err := groupFor(keyword)
		if err != nil {
			return File{}, err
		}
		if g < last || (g == last && g != groupMigration && g != groupQuery && g != groupOutput) {
			return File{}, fmt.Errorf("gensum: record out of order: %q", line)
		}
		values, err := splitFields(line, width)
		if err != nil {
			return File{}, err
		}
		switch g {
		case groupDialect:
			f.Dialect = values[1]
		case groupProfile:
			f.Profile = values[1]
		case groupSettings:
			f.Settings = values[1]
		case groupMigration, groupQuery, groupOutput:
			name := values[1]
			if prior, seen := lastName[g]; seen && name <= prior {
				return File{}, fmt.Errorf("gensum: record out of order: %q", line)
			}
			lastName[g] = name
			entry := Entry{Name: name, Value: values[2]}
			switch g {
			case groupMigration:
				f.Migrations = append(f.Migrations, entry)
			case groupQuery:
				f.Queries = append(f.Queries, entry)
			case groupOutput:
				f.Outputs = append(f.Outputs, entry)
			}
		}
		last = g
	}
	return f, nil
}

func groupFor(keyword string) (group, int, error) {
	switch keyword {
	case "dialect":
		return groupDialect, 2, nil
	case "profile":
		return groupProfile, 2, nil
	case "settings":
		return groupSettings, 2, nil
	case "migration":
		return groupMigration, 3, nil
	case "query":
		return groupQuery, 3, nil
	case "output":
		return groupOutput, 3, nil
	default:
		return groupHeader, 0, fmt.Errorf("gensum: unknown record kind %q", keyword)
	}
}

// splitFields splits line on single spaces and refuses anything that is not exactly want fields
// with no empty field, no tab, and no leading or trailing space.
func splitFields(line string, want int) ([]string, error) {
	if line == "" {
		return nil, fmt.Errorf("gensum: empty record")
	}
	if strings.Contains(line, "\t") {
		return nil, fmt.Errorf("gensum: record contains a tab: %q", line)
	}
	if strings.HasPrefix(line, " ") || strings.HasSuffix(line, " ") {
		return nil, fmt.Errorf("gensum: record has leading or trailing space: %q", line)
	}
	fields := strings.Split(line, " ")
	if len(fields) != want {
		return nil, fmt.Errorf("gensum: record has %d fields, want %d: %q", len(fields), want, line)
	}
	for _, field := range fields {
		if field == "" {
			return nil, fmt.Errorf("gensum: record has an empty field: %q", line)
		}
	}
	return fields, nil
}

// Compare reports how current differs from recorded, one Difference per group that differs, in
// the fixed group order dialect, profile, settings, migrations, queries, outputs. For a group of
// entries, Path names the first name or path, in sorted order, that was added, removed, or whose
// value changed.
func Compare(recorded, current File) []Difference {
	var diffs []Difference
	if recorded.Dialect != current.Dialect {
		diffs = append(diffs, Difference{Group: "dialect"})
	}
	if recorded.Profile != current.Profile {
		diffs = append(diffs, Difference{Group: "profile"})
	}
	if recorded.Settings != current.Settings {
		diffs = append(diffs, Difference{Group: "settings"})
	}
	if path, differs := firstDifference(recorded.Migrations, current.Migrations); differs {
		diffs = append(diffs, Difference{Group: "migrations", Path: path})
	}
	if path, differs := firstDifference(recorded.Queries, current.Queries); differs {
		diffs = append(diffs, Difference{Group: "queries", Path: path})
	}
	if path, differs := firstDifference(recorded.Outputs, current.Outputs); differs {
		diffs = append(diffs, Difference{Group: "outputs", Path: path})
	}
	return diffs
}

// firstDifference walks two entry lists in sorted-by-name order and reports the first name present
// in only one of them, or present in both with a different value.
func firstDifference(recorded, current []Entry) (string, bool) {
	a := sortedEntries(recorded)
	b := sortedEntries(current)
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i].Name < b[j].Name:
			return a[i].Name, true
		case b[j].Name < a[i].Name:
			return b[j].Name, true
		case a[i].Value != b[j].Value:
			return a[i].Name, true
		default:
			i++
			j++
		}
	}
	if i < len(a) {
		return a[i].Name, true
	}
	if j < len(b) {
		return b[j].Name, true
	}
	return "", false
}
