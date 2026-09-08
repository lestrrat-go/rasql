package changeplan

import (
	"fmt"
	"strings"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/compilerlock"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/stmt"
)

// ResolvedChanges is the already-lowered migration result consumed by FromLock.
// It deliberately contains no compiler or database handles.
type ResolvedChanges struct {
	baseline       compilerir.PhysicalCatalog
	target         compilerir.PhysicalCatalog
	decisions      []Decision
	operations     []Operation
	futureObjects  []BaselineObject
	renamedObjects []BaselineRename
}

func NewResolvedChanges(baseline, target compilerir.PhysicalCatalog, decisions []Decision, operations []Operation, futureObjects []BaselineObject, renames []BaselineRename) (ResolvedChanges, error) {
	out := ResolvedChanges{
		baseline:       baseline.Clone(),
		target:         target.Clone(),
		decisions:      append([]Decision(nil), decisions...),
		operations:     append([]Operation(nil), operations...),
		futureObjects:  append([]BaselineObject(nil), futureObjects...),
		renamedObjects: append([]BaselineRename(nil), renames...),
	}
	if out.decisions == nil {
		out.decisions = make([]Decision, 0)
	}
	if out.operations == nil {
		out.operations = make([]Operation, 0)
	}
	if out.futureObjects == nil {
		out.futureObjects = make([]BaselineObject, 0)
	}
	if out.renamedObjects == nil {
		out.renamedObjects = make([]BaselineRename, 0)
	}
	for i := range out.operations {
		out.operations[i] = cloneOperation(out.operations[i])
	}
	for _, object := range out.futureObjects {
		if object.introducedBy == "" {
			return ResolvedChanges{}, fmt.Errorf("%w: future object %q has no create operation", ErrInvalidIdentity, object.id)
		}
	}
	return out, nil
}
func (r ResolvedChanges) BaselineCatalog() compilerir.PhysicalCatalog { return r.baseline.Clone() }
func (r ResolvedChanges) TargetCatalog() compilerir.PhysicalCatalog   { return r.target.Clone() }
func (r ResolvedChanges) Decisions() []Decision                       { return append([]Decision(nil), r.decisions...) }
func (r ResolvedChanges) Operations() []Operation {
	out := append([]Operation(nil), r.operations...)
	for i := range out {
		out[i] = cloneOperation(out[i])
	}
	return out
}
func (r ResolvedChanges) FutureObjects() []BaselineObject {
	return append([]BaselineObject(nil), r.futureObjects...)
}
func (r ResolvedChanges) Renames() []BaselineRename {
	return append([]BaselineRename(nil), r.renamedObjects...)
}

func FromLock(file compilerlock.File, profile engineprofile.Profile, history HistoryIdentity, resolved ResolvedChanges) (Plan, error) {
	if _, err := compilerlock.Encode(file); err != nil {
		return Plan{}, fmt.Errorf("%w: invalid compiler lock: %v", ErrInvalidPlan, err)
	}
	if err := engineprofile.Validate(profile); err != nil {
		return Plan{}, err
	}
	if file.Engine.Profile != profile.ID || !profileEngineMatches(profile.Engine, file.Engine.Dialect) {
		return Plan{}, fmt.Errorf("%w: lock engine/profile mismatch", ErrInvalidPlan)
	}
	sourceDigest, err := parseDigest(file.Digests.Source)
	if err != nil {
		return Plan{}, fmt.Errorf("%w: %v", ErrInvalidSourceDigest, err)
	}
	physical := compilerlock.PhysicalFromCatalog(file)
	catalogDigest, err := CatalogDigest(physical)
	if err != nil {
		return Plan{}, err
	}
	profileDigest, err := profileDigest(profile)
	if err != nil {
		return Plan{}, err
	}
	catalogIdentity, err := NewCatalogIdentity(profile.Engine, profileDigest, catalogDigest, sourceDigest)
	if err != nil {
		return Plan{}, err
	}
	objects := make([]BaselineObject, 0, len(physical.Objects)+len(resolved.futureObjects))
	for _, object := range physical.Objects {
		value, err := NewBaselineObject(ObjectID(object.ID), object.Kind, object.Schema, object.Name, "")
		if err != nil {
			return Plan{}, err
		}
		objects = append(objects, value)
	}
	objects = append(objects, resolved.futureObjects...)
	baseline, err := NewBaselineIdentity(catalogIdentity, file.Source.Identity, objects, resolved.renamedObjects)
	if err != nil {
		return Plan{}, err
	}
	if !resolved.baselineEquivalent(physical) && len(resolved.baseline.Objects) > 0 {
		return Plan{}, fmt.Errorf("%w: resolved baseline differs from lock catalog", ErrInvalidPlan)
	}
	return NewPlan(profile, baseline, history, resolved.decisions, resolved.operations)
}
func profileEngineMatches(engine engineprofile.EngineID, dialect string) bool {
	dialect = strings.ToLower(dialect)
	switch engine {
	case engineprofile.PostgreSQL:
		return dialect == "postgres" || dialect == "postgresql"
	case engineprofile.MySQL:
		return dialect == "mysql"
	case engineprofile.SQLite:
		return dialect == "sqlite"
	case engineprofile.Custom:
		return true
	default:
		return false
	}
}
func ProfileDigest(profile engineprofile.Profile) (Digest, error) {
	b, err := jsonMarshal(profileToWire(profile))
	if err != nil {
		return Digest{}, err
	}
	return sha256Digest(b), nil
}
func profileDigest(profile engineprofile.Profile) (Digest, error) { return ProfileDigest(profile) }
func jsonMarshal(value any) ([]byte, error)                       { return marshalNoHTML(value) }
func (r ResolvedChanges) baselineEquivalent(lock compilerir.PhysicalCatalog) bool {
	if len(r.baseline.Objects) != len(lock.Objects) {
		return false
	}
	for i := range lock.Objects {
		if r.baseline.Objects[i].ID != lock.Objects[i].ID || r.baseline.Objects[i].Kind != lock.Objects[i].Kind || r.baseline.Objects[i].Schema != lock.Objects[i].Schema || r.baseline.Objects[i].Name != lock.Objects[i].Name {
			return false
		}
	}
	return true
}

// NewOperationStatements is a small convenience for producers adapting lowered SQL.
func NewOperationStatements(sqls []stmt.Statement) []stmt.Statement { return cloneStatements(sqls) }
