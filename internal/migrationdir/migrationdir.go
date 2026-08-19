// Package migrationdir reads a directory tree of SQL migrations into
// migrate.Migration values.
//
// It is the one place the on-disk layout is defined: a migration directory
// holds one subdirectory per migration; each subdirectory holds one or more
// .up.sql sources and at least one .down.sql source beside them; forward
// sources run in ascending name order and reverse sources in descending
// name order, so a migration is undone in the reverse of the order it was
// done; and a dot-prefixed entry is ignored at either level.
// cmd/rasqlmigrate reads its -dir with it, and
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
// holds a subdirectory or a source named anything other than .up.sql or
// .down.sql, and an empty directory at either level, so a misplaced file is
// reported instead of being skipped. Every returned migration has passed
// migrate.Migration.Validate.
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

// upSuffix and downSuffix name the two source kinds. Every source in a
// migration ends in one of them; a plain ".sql" is refused, which is what
// makes a misspelled reverse suffix a load failure rather than a silent
// extra forward source.
const (
	upSuffix   = ".up.sql"
	downSuffix = ".down.sql"
)

// loadMigration reads one migration directory, whose name is its ID.
func loadMigration(directory string, id string) (migrate.Migration, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return migrate.Migration{}, fmt.Errorf("read migration %q: %w", id, err)
	}
	upFiles := make([]string, 0)
	downFiles := make([]string, 0)
	stems := make(map[string]struct{})
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		switch {
		case entry.IsDir():
			return migrate.Migration{}, fmt.Errorf("migration %q contains subdirectory %q", id, name)
		case strings.HasSuffix(name, upSuffix):
			upFiles = append(upFiles, name)
			stems[strings.TrimSuffix(name, upSuffix)] = struct{}{}
		case strings.HasSuffix(name, downSuffix):
			downFiles = append(downFiles, name)
		default:
			return migrate.Migration{}, fmt.Errorf("migration %q contains %q, which is neither a %s nor a %s source", id, name, upSuffix, downSuffix)
		}
	}
	sort.Strings(upFiles)
	// Reverse sources run in descending name order, undoing the migration
	// in the reverse of the order it was done, so 002's reverse runs before
	// 001's.
	sort.Sort(sort.Reverse(sort.StringSlice(downFiles)))
	if len(upFiles) == 0 {
		return migrate.Migration{}, fmt.Errorf("migration %q has no %s source", id, upSuffix)
	}
	if len(downFiles) == 0 {
		return migrate.Migration{}, fmt.Errorf("migration %q has no %s source; every migration must be reversible", id, downSuffix)
	}
	// A reverse source names the forward source it undoes. Requiring the
	// stem to match one is the check that catches a misspelled forward
	// name, which would otherwise pair with nothing and revert nothing.
	// Fewer reverse sources than forward ones is allowed, since one DROP
	// TABLE can undo a create-table and a create-index together.
	for _, name := range downFiles {
		if _, exists := stems[strings.TrimSuffix(name, downSuffix)]; !exists {
			return migrate.Migration{}, fmt.Errorf("migration %q reverse source %q matches no %s source", id, name, upSuffix)
		}
	}

	statements, err := readStatements(directory, id, upFiles)
	if err != nil {
		return migrate.Migration{}, err
	}
	down, err := readStatements(directory, id, downFiles)
	if err != nil {
		return migrate.Migration{}, err
	}
	migration := migrate.Migration{ID: id, Statements: statements, Down: down}
	if err := migration.Validate(); err != nil {
		return migrate.Migration{}, err
	}
	return migration, nil
}

// readStatements reads one ordered set of sources, keeping the order given.
func readStatements(directory string, id string, filenames []string) ([]migrate.Statement, error) {
	statements := make([]migrate.Statement, len(filenames))
	for index, filename := range filenames {
		source := filepath.Join(directory, filename)
		data, err := os.ReadFile(source)
		if err != nil {
			return nil, fmt.Errorf("read migration %q SQL source %q: %w", id, filename, err)
		}
		statements[index] = migrate.Statement{Source: filename, SQL: string(data)}
	}
	return statements, nil
}
