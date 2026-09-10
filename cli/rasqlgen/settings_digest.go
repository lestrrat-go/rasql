package rasqlgen

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

// settingsSnapshot is every configuration field rasql.sum's settings line covers: what a
// generated store's shape, or a query's declaration, depends on. It never touches a file --
// Mappings is the already-decoded JSON value, carried as any rather than as the raw
// json.RawMessage config.Mappings holds, so that re-marshaling it here goes through
// encoding/json's own map-key sort and drops whatever whitespace the source file happened to use.
// Struct field order below is fixed by the struct itself, so two settingsSnapshot values that
// differ in nothing observable encode identically regardless of how their rasql.json was
// formatted or in what order its JSON object keys were written.
type settingsSnapshot struct {
	Dialect    string        `json:"dialect"`
	Package    string        `json:"package"`
	Output     string        `json:"output"`
	Emitter    string        `json:"emitter"`
	Prune      bool          `json:"prune"`
	Migrations string        `json:"migrations"`
	Tables     configTables  `json:"tables"`
	Queries    []configQuery `json:"queries"`
	Mappings   any           `json:"mappings"`
}

// settingsDigest hashes the effective settings loadConfig produced: every default runGenerate
// itself would apply -- the compact emitter, prune true -- is filled in first, so a config that
// omits a field with a default hashes the same as one that states the default explicitly. A
// changed tables.exclude, a renamed query function, or an edited mapping all change this digest;
// the DSN, the observed server version, and every other fact only a database can state do not,
// because settingsDigest opens nothing and reads no file.
func settingsDigest(cfg config) (string, error) {
	emitter := cfg.Emitter
	if emitter == "" {
		emitter = "compact"
	}
	prune := true
	if cfg.Prune != nil {
		prune = *cfg.Prune
	}
	var mappings any
	if len(cfg.Mappings) != 0 {
		if err := json.Unmarshal(cfg.Mappings, &mappings); err != nil {
			return "", fmt.Errorf("generate: decode mappings: %w", err)
		}
	}
	snapshot := settingsSnapshot{
		Dialect:    canonicalDialectName(cfg.Dialect),
		Package:    cfg.Package,
		Output:     cfg.Output,
		Emitter:    emitter,
		Prune:      prune,
		Migrations: cfg.Migrations,
		Tables:     cfg.Tables,
		Queries:    cfg.Queries,
		Mappings:   mappings,
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return "", fmt.Errorf("generate: encode settings: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", digest[:]), nil
}

// canonicalDialectName normalizes a configured dialect to the spelling rasql.sum records:
// "postgres" and "postgresql" both become "postgresql", so a project that renames one to the
// other in rasql.json is not reported as a settings change.
func canonicalDialectName(d string) string {
	switch strings.ToLower(d) {
	case "postgres", "postgresql":
		return "postgresql"
	case "mysql":
		return "mysql"
	case "sqlite", "sqlite3":
		return "sqlite"
	default:
		return strings.ToLower(d)
	}
}
