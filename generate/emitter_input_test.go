package generate_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/compilerlock"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func plainEmitterFixture(t *testing.T) generate.EmitterInput {
	t.Helper()
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite", Version: "3"}, Objects: []compilerir.PhysicalObject{{ID: "users", Kind: "table", Name: "users", Columns: []compilerir.PhysicalColumn{{Name: "id", Ordinal: 0, LogicalKind: "integer"}}}}}
	semantic, diagnostics := compilerir.BuildSemantic(catalog, compilerir.MappingConfig{}, nil)
	if len(diagnostics) != 0 {
		t.Fatalf("semantic diagnostics: %#v", diagnostics)
	}
	config := compilerir.GoConfig{Package: "store", Output: "generated", Emitter: "legacy", Objects: []compilerir.ObjectGoName{{ID: "users", File: "users_gen.go"}}}
	model, diagnostics := compilerir.BuildGo(semantic, config)
	if len(diagnostics) != 0 {
		t.Fatalf("Go diagnostics: %#v", diagnostics)
	}
	in, err := generate.NewEmitterInput(catalog, semantic, model, config)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func emitterFixture(t *testing.T) (generate.EmitterInput, compilerir.GoConfig) {
	t.Helper()
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite", Version: "3"}, Objects: []compilerir.PhysicalObject{{
		ID: "users", Kind: "table", Name: "users", Columns: []compilerir.PhysicalColumn{
			{Name: "id", Ordinal: 0, LogicalKind: "integer"},
			{Name: "status", Ordinal: 1, LogicalKind: "text", Nullable: true},
		},
	}}}
	mapping := compilerir.ScalarMapping{Name: "status", Match: compilerir.NativeMatch{LogicalKind: "text"}, GoType: "Status", NullableGoType: "NullableStatus", Codec: "status"}
	semantic, diagnostics := compilerir.BuildSemantic(catalog, compilerir.MappingConfig{Scalars: []compilerir.ScalarMapping{mapping}}, nil)
	if len(diagnostics) != 0 {
		t.Fatalf("semantic diagnostics: %#v", diagnostics)
	}
	config := compilerir.GoConfig{Package: "store", Output: "generated", Emitter: "legacy", Scalars: []compilerir.ScalarMapping{mapping}, Objects: []compilerir.ObjectGoName{{ID: "users", File: "users_gen.go"}}}
	model, diagnostics := compilerir.BuildGo(semantic, config)
	if len(diagnostics) != 0 {
		t.Fatalf("Go diagnostics: %#v", diagnostics)
	}
	in, err := generate.NewEmitterInput(catalog, semantic, model, config)
	if err != nil {
		t.Fatal(err)
	}
	return in, config
}

func TestEmitterInputCloneOwnsNestedValues(t *testing.T) {
	in, _ := emitterFixture(t)
	clone := in.Clone()
	clone.Catalog.Objects[0].Columns[0].Name = "changed"
	clone.Semantic.Objects[0].Columns[0].Name = "changed"
	clone.Go.Objects[0].Columns[0].GoType = "Changed"
	clone.Generation.Scalars[0].Imports = append(clone.Generation.Scalars[0].Imports, compilerir.GoImport{Path: "example.com/changed"})
	if in.Catalog.Objects[0].Columns[0].Name != "id" || in.Semantic.Objects[0].Columns[0].Name != "id" || in.Go.Objects[0].Columns[0].GoType == "Changed" || len(in.Generation.Scalars[0].Imports) != 0 {
		t.Fatal("clone mutation changed emitter input")
	}
}

func TestNewEmitterInputOwnsConstructorInputs(t *testing.T) {
	in, _ := emitterFixture(t)
	constructed, err := generate.NewEmitterInput(in.Catalog, in.Semantic, in.Go, in.Generation)
	require.NoError(t, err)

	in.Catalog.Objects[0].Columns[0].Name = "changed"
	in.Semantic.Objects[0].Columns[0].Name = "changed"
	in.Go.Objects[0].Columns[0].GoType = "Changed"
	in.Generation.Objects[0].File = "changed.go"

	require.Equal(t, "id", constructed.Catalog.Objects[0].Columns[0].Name)
	require.Equal(t, "id", constructed.Semantic.Objects[0].Columns[0].Name)
	require.NotEqual(t, "Changed", constructed.Go.Objects[0].Columns[0].GoType)
	require.Equal(t, "users_gen.go", constructed.Generation.Objects[0].File)
}

func TestLegacyStoreRejectsCustomCodecBeforePlanning(t *testing.T) {
	in, _ := emitterFixture(t)
	if _, err := generate.LegacyStore(in); err == nil {
		t.Fatal("legacy store accepted an unrepresentable codec")
	}
}

func TestEmitterInputRejectsMissingOrConflictingPolicy(t *testing.T) {
	in, config := emitterFixture(t)
	cases := []struct {
		name string
		edit func(*generate.EmitterInput)
	}{
		{name: "missing object policy", edit: func(value *generate.EmitterInput) { value.Generation.Objects = nil }},
		{name: "semantic kind", edit: func(value *generate.EmitterInput) { value.Semantic.Objects[0].Kind = "view" }},
		{name: "codec", edit: func(value *generate.EmitterInput) { value.Go.Objects[0].Columns[1].Codec = "other" }},
		{name: "relation coverage", edit: func(value *generate.EmitterInput) {
			value.Go.Objects[0].Relations = []compilerir.GoRelation{{Name: "missing", Target: "users", Kind: "belongs_to"}}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			candidate := in.Clone()
			test.edit(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("inconsistent emitter input was accepted")
			}
		})
	}
	config.Objects = nil
	if _, err := generate.NewEmitterInput(in.Catalog, in.Semantic, in.Go, config); err == nil {
		t.Fatal("missing generation policy was accepted")
	}
}

func TestLegacyStorePlanMatchesCanonicalBaseline(t *testing.T) {
	in := plainEmitterFixture(t)
	legacy, err := generate.LegacyStore(in)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "generated"), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy.Root = root
	tables, diagnostics := compilerir.TableDefsFromPhysical(in.Catalog)
	if len(diagnostics) != 0 {
		t.Fatalf("baseline diagnostics: %#v", diagnostics)
	}
	direct := generate.Store{Package: "store", Root: root, Dir: "generated", Tables: tables, Names: legacy.Names, Prune: in.Generation.Prune, Dialect: dialect.SQLite()}
	legacyPlan, err := legacy.Plan()
	if err != nil {
		t.Fatal(err)
	}
	directPlan, err := direct.Plan()
	if err != nil {
		t.Fatal(err)
	}
	legacyFiles, directFiles := legacyPlan.Files(), directPlan.Files()
	sort.Slice(legacyFiles, func(i, j int) bool { return legacyFiles[i].Path < legacyFiles[j].Path })
	sort.Slice(directFiles, func(i, j int) bool { return directFiles[i].Path < directFiles[j].Path })
	if len(legacyFiles) != len(directFiles) {
		t.Fatalf("file count differs: %d != %d", len(legacyFiles), len(directFiles))
	}
	for i := range legacyFiles {
		if legacyFiles[i].Path != directFiles[i].Path || string(legacyFiles[i].Source) != string(directFiles[i].Source) {
			t.Fatalf("file %d differs", i)
		}
	}
}

func TestEmitterInputAcceptsCanonicalViewWithoutWriteShapes(t *testing.T) {
	in := plainEmitterFixture(t)
	in.Catalog.Objects[0].Kind = "view"
	in.Semantic.Objects[0].Kind = "view"
	in.Semantic.Objects[0].Columns[0].InsertState = "forbidden"
	in.Semantic.Objects[0].Columns[0].PatchState = "forbidden"
	in.Go.Objects[0].Columns[0].InsertState = "forbidden"
	in.Go.Objects[0].Columns[0].PatchState = "forbidden"
	in.Go.Objects[0].Create = nil
	in.Go.Objects[0].Patch = nil
	if err := in.Validate(); err != nil {
		t.Fatalf("view without write shapes rejected: %v", err)
	}
}

func TestLegacyStoreUsesConfiguredAccessorRowAndFile(t *testing.T) {
	in := plainEmitterFixture(t)
	in.Generation.Objects[0] = compilerir.ObjectGoName{ID: "users", Source: "Users", Row: "PersonRow", Create: "PersonCreate", Patch: "PersonPatch", File: "people_gen.go"}
	in.Go.Objects[0].SourceName = "Users"
	in.Go.Objects[0].Row.Name = "PersonRow"
	in.Go.Objects[0].Create.Name = "PersonCreate"
	in.Go.Objects[0].Patch.Name = "PersonPatch"
	in.Go.Files = append(in.Go.Files, compilerir.GoFile{Path: "people_gen.go"})
	store, err := generate.LegacyStore(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Names[schema.ObjectName{Name: "users"}]; got.Accessor != "Users" || got.RowType != "PersonRow" || got.FileBase != "people" {
		t.Fatalf("unexpected names: %#v", got)
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "generated"), 0o755); err != nil {
		t.Fatal(err)
	}
	store.Root = root
	plan, err := store.Plan()
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range plan.Files() {
		if filepath.Base(file.Path) == "people_gen.go" {
			return
		}
	}
	t.Fatal("configured generated file was not planned")
}

func TestLegacyStoreRejectsIndependentMutationNames(t *testing.T) {
	in := plainEmitterFixture(t)
	in.Generation.Objects[0].Create = "OtherCreate"
	in.Go.Objects[0].Create.Name = "OtherCreate"
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := generate.LegacyStore(in); err == nil {
		t.Fatal("legacy store accepted independent mutation naming")
	}
}

func TestLockRoundTripRebuildsIdenticalLegacyPlan(t *testing.T) {
	in := plainEmitterFixture(t)
	gen := in.Generation.Clone()
	for i := range gen.Objects {
		gen.Objects[i].Source = "Users"
		gen.Objects[i].Row = "UsersRow"
		gen.Objects[i].Create = "UsersCreate"
		gen.Objects[i].Patch = "UsersPatch"
	}
	lock := compilerlock.File{Format: compilerlock.FormatVersion, Compiler: "rasql", Engine: compilerlock.EngineRecord{Dialect: "sqlite", Version: "3", Profile: "sqlite-3.35"}, Catalog: compilerlock.FromPhysical(in.Catalog), Generation: compilerlock.GenerationRecord{Package: gen.Package, Output: gen.Output, Emitter: gen.Emitter, Prune: gen.Prune}}
	for _, object := range gen.Objects {
		lock.Generation.Objects = append(lock.Generation.Objects, compilerlock.ObjectNameRecord{ID: string(object.ID), Source: object.Source, Row: object.Row, Create: object.Create, Patch: object.Patch, File: object.File})
	}
	lock.Source = compilerlock.SourceRecord{Kind: "live", Identity: "fixture"}
	var err error
	lock.Digests, err = compilerlock.BuildDigests(compilerlock.DigestInputs{Source: compilerlock.SourceDigestInput{Record: lock.Source, Engine: lock.Engine}, Generation: gen})
	require.NoError(t, err)
	encoded, err := compilerlock.Encode(lock)
	require.NoError(t, err)
	decoded, err := compilerlock.Decode(encoded)
	require.NoError(t, err)
	catalog := compilerlock.PhysicalFromCatalog(decoded)
	semantic, diagnostics := compilerir.BuildSemantic(catalog, compilerir.MappingConfig{}, nil)
	require.Empty(t, diagnostics)
	rebuiltConfig := compilerir.GoConfig{Package: decoded.Generation.Package, Output: decoded.Generation.Output, Emitter: decoded.Generation.Emitter, Prune: decoded.Generation.Prune}
	for _, object := range decoded.Generation.Objects {
		rebuiltConfig.Objects = append(rebuiltConfig.Objects, compilerir.ObjectGoName{ID: compilerir.ObjectID(object.ID), Source: object.Source, Row: object.Row, Create: object.Create, Patch: object.Patch, File: object.File})
	}
	model, diagnostics := compilerir.BuildGo(semantic, rebuiltConfig)
	require.Empty(t, diagnostics)
	rebuilt, err := generate.NewEmitterInput(catalog, semantic, model, rebuiltConfig)
	require.NoError(t, err)
	originalStore, err := generate.LegacyStore(in)
	require.NoError(t, err)
	rebuiltStore, err := generate.LegacyStore(rebuilt)
	require.NoError(t, err)
	originalPlan, err := originalStore.Plan()
	require.NoError(t, err)
	rebuiltPlan, err := rebuiltStore.Plan()
	require.NoError(t, err)
	require.Equal(t, originalPlan.Files(), rebuiltPlan.Files())
}
