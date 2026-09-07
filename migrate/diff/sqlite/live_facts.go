package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/lestrrat-go/rasql/migrate/diff"
)

// LiveCatalogFacts records catalog objects that can make a rebuild unsafe.
type LiveCatalogFacts struct {
	ObjectNames  []string
	TriggerNames []string
	ViewNames    []string
}

// AttachLiveCatalog marks a parsed baseline with facts obtained from the
// selected SQLite database. Triggers and dependent views are refused because
// rebuilding would otherwise silently leave stale dependencies behind.
func (Analyzer) AttachLiveCatalog(snapshot diff.Snapshot, facts LiveCatalogFacts) (diff.Snapshot, error) {
	parsed, ok := snapshot.(*schemaSnapshot)
	if !ok || parsed == nil || parsed.Dialect() != "sqlite" {
		return nil, fmt.Errorf("sqlite schema diff requires a SQLite snapshot")
	}
	parsed.liveFactsKnown = true
	parsed.liveFacts = cloneLiveCatalogFacts(facts)
	if len(facts.TriggerNames) > 0 || len(facts.ViewNames) > 0 {
		objects := append([]string(nil), facts.TriggerNames...)
		objects = append(objects, facts.ViewNames...)
		sort.Strings(objects)
		return nil, fmt.Errorf("sqlite schema diff: rebuild has unsafe live dependencies: %v", objects)
	}
	return parsed, nil
}

func cloneLiveCatalogFacts(facts LiveCatalogFacts) LiveCatalogFacts {
	return LiveCatalogFacts{
		ObjectNames:  append([]string(nil), facts.ObjectNames...),
		TriggerNames: append([]string(nil), facts.TriggerNames...),
		ViewNames:    append([]string(nil), facts.ViewNames...),
	}
}

// InspectLiveCatalog reads SQLite schema names and dependencies for tableName.
func InspectLiveCatalog(ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, tableName string) (LiveCatalogFacts, error) {
	rows, err := db.QueryContext(ctx, "SELECT type, name, tbl_name, sql FROM sqlite_schema WHERE name IS NOT NULL")
	if err != nil {
		return LiveCatalogFacts{}, err
	}
	defer func() { _ = rows.Close() }()
	var facts LiveCatalogFacts
	for rows.Next() {
		var objectType, name, owner string
		var source sql.NullString
		if err := rows.Scan(&objectType, &name, &owner, &source); err != nil {
			return LiveCatalogFacts{}, err
		}
		facts.ObjectNames = append(facts.ObjectNames, name)
		switch objectType {
		case "trigger":
			if strings.EqualFold(sqliteIdentifierKey(strings.Trim(owner, "\"`[]")), sqliteIdentifierKey(sqliteNameLeaf(tableName))) {
				facts.TriggerNames = append(facts.TriggerNames, name)
			}
		case "view":
			// SQLite does not expose a complete dependency graph. Refuse every
			// view because a quoted or schema-qualified reference may be missed.
			facts.ViewNames = append(facts.ViewNames, name)
		}
	}
	sort.Strings(facts.ObjectNames)
	sort.Strings(facts.TriggerNames)
	sort.Strings(facts.ViewNames)
	return facts, rows.Err()
}

func sqliteNameLeaf(name string) string {
	var leaf strings.Builder
	var quote byte
	for i := 0; i < len(name); i++ {
		c := name[i]
		if quote != 0 {
			if c == quote {
				if i+1 < len(name) && name[i+1] == quote {
					leaf.WriteByte(c)
					i++
					continue
				}
				quote = 0
				continue
			}
			leaf.WriteByte(c)
			continue
		}
		switch c {
		case '"', '`':
			quote = c
		case '[':
			quote = ']'
		case '.':
			leaf.Reset()
		default:
			leaf.WriteByte(c)
		}
	}
	return strings.TrimSpace(leaf.String())
}
