package compilerlock_test

import (
	"reflect"
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/compilerlock"
)

func TestManyThroughMappingRoundTripPreservesOrderedPaths(t *testing.T) {
	want := compilerir.MappingConfig{Relations: []compilerir.RelationMapping{{
		Name: "roles", Source: "users", From: []string{"tenant_id", "id"}, Target: "roles", To: []string{"tenant_id", "id"},
		Through: compilerir.ThroughMapping{Object: "user_roles", SourceFrom: []string{"user_tenant", "user_id"}, SourceTo: []string{"tenant_id", "id"}, TargetFrom: []string{"role_tenant", "role_id"}, TargetTo: []string{"tenant_id", "id"}},
	}}}
	record := compilerlock.FromMappings(want)
	got, err := record.MappingConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mapping changed during round trip: %#v", got)
	}
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	encoded, err := compilerlock.Encode(compilerlock.File{Format: 1, Compiler: "test", Source: compilerlock.SourceRecord{Kind: "external", Identity: "x"}, Engine: compilerlock.EngineRecord{Dialect: "sqlite", Profile: "sqlite-3"}, Catalog: compilerlock.CatalogRecord{Objects: []compilerlock.ObjectRecord{}}, Mappings: record, Queries: []compilerlock.QueryRecord{}, Generation: compilerlock.GenerationRecord{Package: "p", Output: "out", Emitter: "compact", Objects: []compilerlock.ObjectNameRecord{}}, Digests: compilerlock.Digests{Source: hash, Mappings: hash, Queries: hash, Generation: hash}})
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) == 0 {
		t.Fatal("mapping lock encoding was empty")
	}
}

func TestManyThroughMappingDigestIgnoresOrderAndDetectsPathChanges(t *testing.T) {
	base := compilerlock.DigestInputs{Source: compilerlock.SourceDigestInput{Record: compilerlock.SourceRecord{Kind: "external", Identity: "x"}, Engine: compilerlock.EngineRecord{Dialect: "sqlite", Profile: "sqlite-3"}}, Mappings: compilerir.MappingConfig{Relations: []compilerir.RelationMapping{{Name: "roles", Source: "users", Target: "roles", From: []string{"id"}, To: []string{"id"}, Through: compilerir.ThroughMapping{Object: "user_roles", SourceFrom: []string{"user_id"}, SourceTo: []string{"id"}, TargetFrom: []string{"role_id"}, TargetTo: []string{"id"}}}}}, Generation: compilerir.GoConfig{Package: "store", Output: "out", Emitter: "compact"}}
	first, err := compilerlock.BuildDigests(base)
	if err != nil {
		t.Fatal(err)
	}
	base.Mappings.Relations = append(base.Mappings.Relations, compilerir.RelationMapping{Name: "groups", Source: "users", Target: "groups", From: []string{"id"}, To: []string{"id"}, Through: compilerir.ThroughMapping{Object: "user_groups", SourceFrom: []string{"id"}, SourceTo: []string{"user_id"}, TargetFrom: []string{"id"}, TargetTo: []string{"group_id"}}})
	second, err := compilerlock.BuildDigests(base)
	if err != nil {
		t.Fatal(err)
	}
	if first.Mappings == second.Mappings {
		t.Fatal("mapping digest did not change when a relation was added")
	}
	base.Mappings.Relations[0].Through.SourceTo[0] = "changed"
	third, err := compilerlock.BuildDigests(base)
	if err != nil {
		t.Fatal(err)
	}
	if second.Mappings == third.Mappings {
		t.Fatal("mapping digest did not change when a path changed")
	}
}
