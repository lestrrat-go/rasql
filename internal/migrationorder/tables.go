// Package migrationorder orders newly added tables by foreign-key dependency.
package migrationorder

import (
	"fmt"
	"sort"
	"strings"
)

// TableDependency describes one table and the tables it references.
type TableDependency struct {
	Key       string
	Display   string
	DependsOn []string
}

// OrderTables returns stable creation order for tables and their dependencies.
// References to absent tables are ignored because those tables already exist
// outside the added set. Self-references are also ignored.
func OrderTables(tables []TableDependency) ([]string, error) {
	byKey := make(map[string]TableDependency, len(tables))
	for _, table := range tables {
		if table.Key == "" {
			return nil, fmt.Errorf("migration order: table key must not be empty")
		}
		if _, exists := byKey[table.Key]; exists {
			return nil, fmt.Errorf("migration order: duplicate table key %q", table.Key)
		}
		byKey[table.Key] = table
	}
	indegree := make(map[string]int, len(tables))
	dependents := make(map[string]map[string]struct{}, len(tables))
	for _, table := range tables {
		indegree[table.Key] = 0
		dependents[table.Key] = make(map[string]struct{})
	}
	for _, table := range tables {
		seen := make(map[string]struct{}, len(table.DependsOn))
		for _, dependency := range table.DependsOn {
			if dependency == table.Key {
				continue
			}
			if _, exists := byKey[dependency]; !exists {
				continue
			}
			if _, exists := seen[dependency]; exists {
				continue
			}
			seen[dependency] = struct{}{}
			indegree[table.Key]++
			dependents[dependency][table.Key] = struct{}{}
		}
	}
	ready := make([]string, 0, len(tables))
	for key, degree := range indegree {
		if degree == 0 {
			ready = append(ready, key)
		}
	}
	sort.Strings(ready)
	ordered := make([]string, 0, len(tables))
	for len(ready) > 0 {
		key := ready[0]
		ready = ready[1:]
		ordered = append(ordered, key)
		for dependent := range dependents[key] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				ready = append(ready, dependent)
			}
		}
		sort.Strings(ready)
	}
	if len(ordered) == len(tables) {
		return ordered, nil
	}
	remaining := make([]string, 0, len(tables)-len(ordered))
	for key, degree := range indegree {
		if degree > 0 {
			remaining = append(remaining, byKey[key].Display)
		}
	}
	sort.Strings(remaining)
	return nil, fmt.Errorf("migration order: tables %s form a foreign-key cycle; no CREATE TABLE order can satisfy the foreign keys", strings.Join(remaining, ", "))
}
