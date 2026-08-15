// Package migrationdir reads a directory tree of SQL migrations into
// migrate.Migration values.
//
// It is the one place the on-disk layout is defined: a migration directory
// holds one subdirectory per migration, each subdirectory holds one or more
// .sql sources, both are ordered by name, and a dot-prefixed entry is
// ignored at either level. cmd/rasqlmigrate reads its -dir with it, and
// examples/taskboard_store_test.go reads sample/taskboard's migrations with
// it, so a test that rebuilds a store from migrations applies exactly what
// the command applies.
package migrationdir

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lestrrat-go/rasql/migrate"
)

// Load reads every migration directory directly inside directory, in name
// order, and returns them ready for migrate.Runner.Apply.
//
// It refuses a directory that holds a non-directory entry, a migration that
// holds a subdirectory or a non-.sql source, and an empty directory at
// either level, so a misplaced file is reported instead of being skipped.
// Every returned migration has passed migrate.Migration.Validate.
func Load(directory string) ([]migrate.Migration, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read migration directory: %w", err)
	}
	directories := make([]string, 0)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if !entry.IsDir() {
			return nil, fmt.Errorf("migration directory %q contains non-directory entry %q", directory, entry.Name())
		}
		directories = append(directories, entry.Name())
	}
	sort.Strings(directories)
	if len(directories) == 0 {
		return nil, fmt.Errorf("migration directory %q has no migration directories", directory)
	}
	migrations := make([]migrate.Migration, len(directories))
	for index, name := range directories {
		migration, err := loadMigration(filepath.Join(directory, name), name)
		if err != nil {
			return nil, err
		}
		migrations[index] = migration
	}
	return migrations, nil
}

// loadMigration reads one migration directory, whose name is its ID.
func loadMigration(directory string, id string) (migrate.Migration, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return migrate.Migration{}, fmt.Errorf("read migration %q: %w", id, err)
	}
	files := make([]string, 0)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			return migrate.Migration{}, fmt.Errorf("migration %q contains non-SQL source %q", id, entry.Name())
		}
		files = append(files, entry.Name())
	}
	sort.Strings(files)
	if len(files) == 0 {
		return migrate.Migration{}, fmt.Errorf("migration %q has no SQL sources", id)
	}
	statements := make([]migrate.Statement, len(files))
	for index, filename := range files {
		source := filepath.Join(directory, filename)
		data, err := os.ReadFile(source)
		if err != nil {
			return migrate.Migration{}, fmt.Errorf("read migration %q SQL source %q: %w", id, filename, err)
		}
		statements[index] = migrate.Statement{Source: filename, SQL: string(data)}
	}
	migration := migrate.Migration{ID: id, Statements: statements}
	if err := migration.Validate(); err != nil {
		return migrate.Migration{}, err
	}
	return migration, nil
}
