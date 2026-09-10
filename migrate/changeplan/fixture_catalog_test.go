package changeplan

import (
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/stretchr/testify/require"
)

// fixtureTasksSourceIdentity is the source identity a decoded
// testdata/external/lock.json (removed when internal/compilerlock was
// deleted) always carried, and every golden digest below was computed
// against it.
const fixtureTasksSourceIdentity = "fixtures/sqlite"

// fixtureTasksPhysicalCatalog reproduces, as a Go literal, the exact
// compilerir.PhysicalCatalog that compilerlock.PhysicalFromCatalog used to
// build by decoding testdata/external/lock.json's "catalog" object: one
// "tasks" table with an integer primary key "id" and a nullable native
// "payload" column. name overrides the table's own Name field (its object ID
// stays "tasks"), the way a hand-edited copy of the lock JSON's "name" field
// used to for TestTableRenameBindingFromLock.
func fixtureTasksPhysicalCatalog(name string) compilerir.PhysicalCatalog {
	return compilerir.PhysicalCatalog{
		Engine: compilerir.EngineIdentity{Dialect: "sqlite", Version: "3.45", Profile: "sqlite-3.35"},
		Objects: []compilerir.PhysicalObject{
			{
				ID:     "tasks",
				Kind:   "table",
				Schema: "main",
				Name:   name,
				Columns: []compilerir.PhysicalColumn{
					{
						Name: "id", Ordinal: 0, LogicalKind: "integer",
						Native:  &compilerir.NativeType{Dialect: "sqlite", Name: "INTEGER", Kind: "builtin"},
						Integer: &compilerir.IntegerTypeFacts{DisplayWidth: compilerir.OptionalInt{Value: 0, Set: false}},
						Nullable: false,
					},
					{
						Name: "payload", Ordinal: 1, LogicalKind: "native",
						Native:   &compilerir.NativeType{Dialect: "sqlite", Name: "BLOBISH", Kind: "other"},
						Nullable: true,
					},
				},
				Constraints: []compilerir.PhysicalConstraint{
					{Name: "", Kind: "primary_key", Columns: []string{"id"}},
				},
				Strict:       true,
				WithoutRowID: true,
			},
		},
	}
}

// fixtureTasksCatalog is fixtureTasksPhysicalCatalog, carried through
// NewCatalogFromPhysical the way every caller of the old lock fixture did.
func fixtureTasksCatalog(t *testing.T, name string) Catalog {
	t.Helper()
	catalog, err := NewCatalogFromPhysical(fixtureTasksPhysicalCatalog(name), fixtureTasksSourceIdentity)
	require.NoError(t, err)
	return catalog
}
