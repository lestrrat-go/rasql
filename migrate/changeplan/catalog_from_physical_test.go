package changeplan_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerlock"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

// TestNewCatalogFromPhysicalMatchesCatalogFromLock proves NewCatalogFromPhysical
// is not a subtly different catalog from the lock-shaped constructor it is
// meant to replace: built from the same lock fixture, both ways must agree on
// every object's identity and definition, which the catalog digest pins in
// one comparison.
func TestNewCatalogFromPhysicalMatchesCatalogFromLock(t *testing.T) {
	lockBytes, err := os.ReadFile(filepath.Join("testdata", "external", "lock.json"))
	require.NoError(t, err)

	viaLock, err := changeplan.CatalogFromLock(lockBytes)
	require.NoError(t, err)

	file, err := compilerlock.Decode(lockBytes)
	require.NoError(t, err)
	physical := compilerlock.PhysicalFromCatalog(file)

	viaPhysical, err := changeplan.NewCatalogFromPhysical(physical, file.Source.Identity)
	require.NoError(t, err)

	require.Equal(t, viaLock.SourceIdentity(), viaPhysical.SourceIdentity())

	lockDigest, err := changeplan.CatalogDigest(viaLock)
	require.NoError(t, err)
	physicalDigest, err := changeplan.CatalogDigest(viaPhysical)
	require.NoError(t, err)
	require.Equal(t, lockDigest, physicalDigest)

	tasksFromLock, ok := viaLock.ObjectID(schema.ObjectTable, "main", "tasks")
	require.True(t, ok)
	tasksFromPhysical, ok := viaPhysical.ObjectID(schema.ObjectTable, "main", "tasks")
	require.True(t, ok)
	require.Equal(t, tasksFromLock, tasksFromPhysical)
}

// TestFromBaselineMatchesFromLock proves the same plan comes out whether the
// baseline catalog is decoded from a lock or handed in directly as a Catalog,
// which is what licenses migrate plan create to stop reading a lock at all.
//
// FromBaseline has no lock-recorded source digest to carry forward, so it
// sets the plan's sourceDigest equal to the catalog digest (see
// fromBaselineWithSourceDigest). The fixture lock's own recorded digest is an
// unrelated placeholder, so the lock is patched here to record that same
// catalog digest -- otherwise the two paths would legitimately produce
// different plans, which would not be the equivalence this test is for.
func TestFromBaselineMatchesFromLock(t *testing.T) {
	lockBytes, err := os.ReadFile(filepath.Join("testdata", "external", "lock.json"))
	require.NoError(t, err)

	unpatchedCatalog, err := changeplan.CatalogFromLock(lockBytes)
	require.NoError(t, err)
	catalogDigest, err := changeplan.CatalogDigest(unpatchedCatalog)
	require.NoError(t, err)

	const placeholderSourceDigest = `"source": "1111111111111111111111111111111111111111111111111111111111111111"`
	patchedLock := bytes.Replace(lockBytes, []byte(placeholderSourceDigest),
		[]byte(`"source": "`+changeplan.DigestHex(catalogDigest)+`"`), 1)
	require.NotEqual(t, string(lockBytes), string(patchedLock), "fixture no longer contains the expected placeholder digest")

	baseProfile, err := engineprofile.Builtin("sqlite-3.35", engineprofile.Version{Known: true, Major: 3, Minor: 45})
	require.NoError(t, err)
	source := catalogFromPhysicalProfileSource{value: baseProfile}

	lockCatalog, err := changeplan.CatalogFromLock(patchedLock)
	require.NoError(t, err)

	tasks, ok := lockCatalog.ObjectID(schema.ObjectTable, "main", "tasks")
	require.True(t, ok)

	after := schema.TableDef{Schema: "main", Name: "tasks", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "payload", Type: schema.TextType{}},
	}}
	afterObject, err := changeplan.NewCatalogObject(tasks, after)
	require.NoError(t, err)
	afterCatalog, err := changeplan.NewCatalogLike(lockCatalog, []changeplan.CatalogObject{afterObject})
	require.NoError(t, err)
	resultDigest, err := changeplan.CatalogDigest(afterCatalog)
	require.NoError(t, err)

	operation, err := changeplan.NewOperation("alter", changeplan.OperationAlterColumn, nil, []changeplan.ObjectID{tasks},
		nil, nil, resultDigest, []stmt.Statement{stmt.New(sqltext.Text("ALTER TABLE tasks ALTER COLUMN payload TYPE TEXT"))},
		changeplan.TransactionEngineDefault, false, nil)
	require.NoError(t, err)
	step, err := changeplan.NewResolvedCatalogStep(operation.ID(), afterCatalog)
	require.NoError(t, err)
	history, err := changeplan.NewHistoryIdentity("main", "schema_migrations")
	require.NoError(t, err)

	resolved, err := changeplan.NewResolvedChanges(lockCatalog, []changeplan.ResolvedCatalogStep{step}, nil, []changeplan.Operation{operation}, nil, nil)
	require.NoError(t, err)
	planFromLock, err := changeplan.FromLock(patchedLock, source, history, resolved)
	require.NoError(t, err)

	file, err := compilerlock.Decode(patchedLock)
	require.NoError(t, err)
	physical := compilerlock.PhysicalFromCatalog(file)
	baselineCatalog, err := changeplan.NewCatalogFromPhysical(physical, file.Source.Identity)
	require.NoError(t, err)

	planFromBaseline, err := changeplan.FromBaseline(baselineCatalog, source, history, resolved)
	require.NoError(t, err)

	lockJSON, err := changeplan.Encode(planFromLock)
	require.NoError(t, err)
	baselineJSON, err := changeplan.Encode(planFromBaseline)
	require.NoError(t, err)
	require.Equal(t, string(lockJSON), string(baselineJSON))
}

type catalogFromPhysicalProfileSource struct{ value engineprofile.Profile }

func (s catalogFromPhysicalProfileSource) ID() string                                  { return s.value.ID }
func (s catalogFromPhysicalProfileSource) Engine() changeplan.EngineID                 { return s.value.Engine }
func (s catalogFromPhysicalProfileSource) Version() changeplan.EngineVersion           { return s.value.Version }
func (s catalogFromPhysicalProfileSource) Capabilities() changeplan.EngineCapabilities { return s.value.Capabilities }
func (s catalogFromPhysicalProfileSource) Limits() changeplan.EngineLimits             { return s.value.Limits }
