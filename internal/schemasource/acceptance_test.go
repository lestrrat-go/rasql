package schemasource_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/lestrrat-go/rasql/internal/schemasource"
)

func TestMigrationOverlappingGlobsSnapshotEachPathOnce(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "migrations"), 0700); err != nil {
		t.Fatal(err)
	}
	for name := range map[string]struct{}{"001.sql": {}, "002.sql": {}} {
		if err := os.WriteFile(filepath.Join(root, "migrations", name), []byte("-- "+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r := schemasource.Request{ModuleRoot: root, Engine: schemasource.EngineConfig{Dialect: "sqlite", Profile: "sqlite-3.35"}, Source: schemasource.SchemaSourceConfig{Kind: "migrations", Identity: "overlap-v1", Paths: []string{"migrations/*.sql", "migrations/00?.sql"}}, TempRoot: root}
	f := &fakeFactory{db: db, dsn: "owned"}
	m := &fakeMigration{}
	got, err := schemasource.Materialize(context.Background(), r, schemasource.Dependencies{Factory: f, Migrations: m, Profiles: &fakeProfile{p: liveProfile(t)}, Catalogs: &fakeCatalog{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Snapshots) != 2 || len(got.Source.Record.Files) != 2 {
		t.Fatalf("snapshots=%d source files=%d", len(got.Snapshots), len(got.Source.Record.Files))
	}
	if got.Snapshots[0].Path() != "migrations/001.sql" || got.Snapshots[1].Path() != "migrations/002.sql" {
		t.Fatalf("snapshots=%#v", got.Snapshots)
	}
}

func TestExternalCommandIdentityContributesToSourceEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "schema.sql"), []byte("schema"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	base := liveRequest(root, "external")
	base.Source.Command = []string{"tool", "--one"}
	deps := func() schemasource.Dependencies {
		return schemasource.Dependencies{Factory: &fakeFactory{db: db, dsn: "owned"}, Profiles: &fakeProfile{p: liveProfile(t)}, Catalogs: &fakeCatalog{}, Processes: &fakeProcess{}}
	}
	a, err := schemasource.Materialize(context.Background(), base, deps())
	if err != nil {
		t.Fatal(err)
	}
	base.Source.Command = []string{"tool", "--two"}
	b, err := schemasource.Materialize(context.Background(), base, deps())
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(a.Source, b.Source) {
		t.Fatal("external command change did not change source evidence")
	}
}
