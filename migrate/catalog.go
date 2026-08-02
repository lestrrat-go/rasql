// Package migrate plans and applies forward-only schema migrations.
package migrate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lestrrat-go/rasql/schema"
)

// Catalog is a complete set of related table descriptors.
// It owns copies of its descriptors and is safe to reuse concurrently.
type Catalog struct {
	tables []schema.Table
}

// NewCatalog validates tables as one schema catalog.
func NewCatalog(tables ...schema.Table) (Catalog, error) {
	clones := make([]schema.Table, len(tables))
	byName := make(map[string]schema.Table, len(tables))
	indexes := make(map[string]string)
	for index, table := range tables {
		if err := table.Validate(); err != nil {
			return Catalog{}, fmt.Errorf("migrate: table at index %d: %w", index, err)
		}
		if _, exists := byName[table.Name]; exists {
			return Catalog{}, fmt.Errorf("migrate: duplicate table %q", table.Name)
		}
		clone := table.Clone()
		for _, index := range clone.Indexes {
			if owner, exists := indexes[index.Name]; exists {
				return Catalog{}, fmt.Errorf("migrate: index %q is declared by both tables %q and %q", index.Name, owner, clone.Name)
			}
			indexes[index.Name] = clone.Name
		}
		clones[index] = clone
		byName[clone.Name] = clone
	}
	for _, table := range clones {
		for _, key := range table.ForeignKeys {
			referenced, exists := byName[key.ReferencedTable]
			if !exists {
				return Catalog{}, fmt.Errorf("migrate: table %q foreign key %q references table %q outside the catalog", table.Name, foreignKeyName(key), key.ReferencedTable)
			}
			for _, column := range key.ReferencedColumns {
				if _, exists := referenced.Column(column); !exists {
					return Catalog{}, fmt.Errorf("migrate: table %q foreign key %q references missing column %q on table %q", table.Name, foreignKeyName(key), column, referenced.Name)
				}
			}
		}
	}
	return Catalog{tables: clones}, nil
}

// Tables returns copies of the catalog's table descriptors in declaration order.
func (c Catalog) Tables() []schema.Table {
	tables := make([]schema.Table, len(c.tables))
	for index, table := range c.tables {
		tables[index] = table.Clone()
	}
	return tables
}

// InitialMigration returns a CREATE TABLE migration ordered by foreign-key dependencies.
// It rejects multi-table foreign-key cycles because those require separate ADD CONSTRAINT
// operations, which this initial migration intentionally does not synthesize.
func (c Catalog) InitialMigration(id string) (Migration, error) {
	if err := validateMigrationID(id); err != nil {
		return Migration{}, err
	}
	if len(c.tables) == 0 {
		return Migration{}, fmt.Errorf("migrate: catalog must contain at least one table")
	}

	byName := make(map[string]schema.Table, len(c.tables))
	dependencies := make(map[string]map[string]struct{}, len(c.tables))
	reverse := make(map[string][]string, len(c.tables))
	for _, table := range c.tables {
		byName[table.Name] = table
		dependencies[table.Name] = make(map[string]struct{})
	}
	for _, table := range c.tables {
		for _, key := range table.ForeignKeys {
			if key.ReferencedTable == table.Name {
				continue
			}
			if _, exists := dependencies[table.Name][key.ReferencedTable]; exists {
				continue
			}
			dependencies[table.Name][key.ReferencedTable] = struct{}{}
			reverse[key.ReferencedTable] = append(reverse[key.ReferencedTable], table.Name)
		}
	}

	ready := make([]string, 0, len(c.tables))
	for name, tableDependencies := range dependencies {
		if len(tableDependencies) == 0 {
			ready = append(ready, name)
		}
	}
	sort.Strings(ready)
	operations := make([]Operation, 0, len(c.tables))
	for len(ready) > 0 {
		name := ready[0]
		ready = ready[1:]
		operations = append(operations, CreateTable{Table: byName[name].Clone()})
		for _, dependent := range reverse[name] {
			delete(dependencies[dependent], name)
			if len(dependencies[dependent]) == 0 {
				ready = append(ready, dependent)
			}
		}
		sort.Strings(ready)
	}
	if len(operations) == len(c.tables) {
		return Migration{ID: id, Operations: operations}, nil
	}
	remaining := make([]string, 0)
	for name, tableDependencies := range dependencies {
		if len(tableDependencies) > 0 {
			remaining = append(remaining, name)
		}
	}
	sort.Strings(remaining)
	return Migration{}, fmt.Errorf("migrate: catalog has a foreign-key cycle among tables %s", strings.Join(remaining, ", "))
}

func foreignKeyName(key schema.ForeignKey) string {
	if key.Name != "" {
		return key.Name
	}
	return "<unnamed>"
}
