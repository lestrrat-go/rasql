package changeplan

import (
	"fmt"
	"strings"

	"github.com/lestrrat-go/rasql/internal/compilerlock"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
)

// ResolvedChanges is the already-lowered migration result consumed by FromLock.
// It deliberately contains no compiler or database handles.
type ResolvedChanges struct {
	baseline       Catalog
	steps          []ResolvedCatalogStep
	decisions      []Decision
	operations     []Operation
	futureObjects  []BaselineObject
	renamedObjects []BaselineRename
}

type ResolvedCatalogStep struct {
	operation OperationID
	after     Catalog
}

func NewResolvedCatalogStep(operation OperationID, after Catalog) (ResolvedCatalogStep, error) {
	if operation == "" {
		return ResolvedCatalogStep{}, fmt.Errorf("%w: resolved step operation is required", ErrInvalidPlan)
	}
	if err := after.validate(); err != nil {
		return ResolvedCatalogStep{}, err
	}
	return ResolvedCatalogStep{operation: operation, after: cloneCatalog(after)}, nil
}
func (s ResolvedCatalogStep) Operation() OperationID { return s.operation }
func (s ResolvedCatalogStep) Catalog() Catalog       { return cloneCatalog(s.after) }

func NewResolvedChanges(baseline Catalog, steps []ResolvedCatalogStep, decisions []Decision, operations []Operation, futureObjects []BaselineObject, renames []BaselineRename) (ResolvedChanges, error) {
	if err := baseline.validate(); err != nil {
		return ResolvedChanges{}, err
	}
	if steps == nil {
		return ResolvedChanges{}, fmt.Errorf("%w: resolved catalog steps must be nonnil", ErrInvalidPlan)
	}
	out := ResolvedChanges{
		baseline:       cloneCatalog(baseline),
		steps:          append([]ResolvedCatalogStep(nil), steps...),
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
	if out.steps == nil {
		out.steps = make([]ResolvedCatalogStep, 0)
	}
	if out.futureObjects == nil {
		out.futureObjects = make([]BaselineObject, 0)
	}
	if out.renamedObjects == nil {
		out.renamedObjects = make([]BaselineRename, 0)
	}
	for i := range out.steps {
		out.steps[i] = ResolvedCatalogStep{
			operation: out.steps[i].operation,
			after:     cloneCatalog(out.steps[i].after),
		}
	}
	for i := range out.operations {
		out.operations[i] = cloneOperation(out.operations[i])
	}
	order, err := stableOrder(out.operations)
	if err != nil {
		return ResolvedChanges{}, err
	}
	if len(out.steps) != len(out.operations) {
		return ResolvedChanges{}, fmt.Errorf("%w: resolved catalog step count does not match operations", ErrInvalidPlan)
	}
	for i := range out.steps {
		if out.steps[i].operation != out.operations[order[i]].id {
			return ResolvedChanges{}, fmt.Errorf("%w: resolved catalog steps are not in stable order", ErrInvalidPlan)
		}
		if err := out.steps[i].after.validate(); err != nil {
			return ResolvedChanges{}, err
		}
		if !sameCatalogIdentity(out.baseline, out.steps[i].after) {
			return ResolvedChanges{}, fmt.Errorf("%w: resolved catalog identity differs", ErrInvalidPlan)
		}
		digest, err := CatalogDigest(out.steps[i].after)
		if err != nil {
			return ResolvedChanges{}, err
		}
		if digest != out.operations[order[i]].resultDigest {
			return ResolvedChanges{}, fmt.Errorf("%w: operation %q result digest does not match resolved catalog", ErrInvalidPlan, out.operations[order[i]].id)
		}
		before := out.baseline
		if i > 0 {
			before = out.steps[i-1].after
		}
		if err := EvaluateFacts(before, out.operations[order[i]].preconditions); err != nil {
			return ResolvedChanges{}, err
		}
		if err := EvaluateFacts(out.steps[i].after, out.operations[order[i]].postconditions); err != nil {
			return ResolvedChanges{}, err
		}
	}
	identity, err := NewCatalogIdentity(catalogEngineID(out.baseline.physical.Engine.Dialect), Digest{}, Digest{}, Digest{})
	if err != nil {
		return ResolvedChanges{}, err
	}
	objects := make([]BaselineObject, 0, len(out.baseline.physical.Objects)+len(out.futureObjects))
	for _, object := range out.baseline.physical.Objects {
		value, err := NewBaselineObject(ObjectID(object.ID), object.Kind, object.Schema, object.Name)
		if err != nil {
			return ResolvedChanges{}, err
		}
		objects = append(objects, value)
	}
	objects = append(objects, out.futureObjects...)
	baselineIdentity, err := NewBaselineIdentity(identity, out.baseline.sourceIdentity, objects, out.renamedObjects)
	if err != nil {
		return ResolvedChanges{}, err
	}
	if err := validateResolvedState(out, baselineIdentity); err != nil {
		return ResolvedChanges{}, err
	}
	for _, object := range out.futureObjects {
		if object.introducedBy == "" {
			return ResolvedChanges{}, fmt.Errorf("%w: future object %q has no create operation", ErrInvalidIdentity, object.id)
		}
	}
	return out, nil
}
func (r ResolvedChanges) BaselineCatalog() Catalog { return cloneCatalog(r.baseline) }
func (r ResolvedChanges) TargetCatalog() Catalog {
	if len(r.steps) == 0 {
		return cloneCatalog(r.baseline)
	}
	return cloneCatalog(r.steps[len(r.steps)-1].after)
}
func (r ResolvedChanges) CatalogSteps() []ResolvedCatalogStep {
	out := append([]ResolvedCatalogStep(nil), r.steps...)
	for i := range out {
		out[i] = ResolvedCatalogStep{operation: out[i].operation, after: cloneCatalog(out[i].after)}
	}
	if out == nil {
		out = make([]ResolvedCatalogStep, 0)
	}
	return out
}
func (r ResolvedChanges) Decisions() []Decision { return append([]Decision(nil), r.decisions...) }
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

func catalogEngineID(dialect string) EngineID {
	switch strings.ToLower(dialect) {
	case "postgres", "postgresql":
		return PostgreSQLEngine
	case "mysql":
		return MySQLEngine
	case "sqlite":
		return SQLiteEngine
	default:
		return CustomEngine
	}
}

func FromLock(lockJSON []byte, source ProfileSource, history HistoryIdentity, resolved ResolvedChanges) (Plan, error) {
	profile, err := NewProfile(source)
	if err != nil {
		return Plan{}, err
	}
	lockCopy := append([]byte(nil), lockJSON...)
	file, err := compilerlock.Decode(lockCopy)
	if err != nil {
		return Plan{}, fmt.Errorf("%w: invalid compiler lock: %v", ErrInvalidPlan, err)
	}
	if _, err := compilerlock.Encode(file); err != nil {
		return Plan{}, fmt.Errorf("%w: invalid compiler lock: %v", ErrInvalidPlan, err)
	}
	if file.Engine.Profile != profile.ID() || !profileEngineMatches(profile.Engine(), file.Engine.Dialect) || !profileVersionMatches(profile, file.Engine.Version) {
		return Plan{}, fmt.Errorf("%w: lock engine/profile mismatch", ErrInvalidPlan)
	}
	sourceDigest, err := parseDigest(file.Digests.Source)
	if err != nil {
		return Plan{}, fmt.Errorf("%w: %v", ErrInvalidSourceDigest, err)
	}
	lockCatalog, err := CatalogFromLock(lockCopy)
	if err != nil {
		return Plan{}, err
	}
	return fromBaselineWithSourceDigest(lockCatalog, &sourceDigest, profile, history, resolved)
}

// FromBaseline builds a Plan from a Catalog read directly from a live
// database, rather than decoded from a compiler lock. The plan's
// sourceDigest -- read back nowhere in migrate/, only inside
// migrate/changeplan -- is set equal to the catalog digest, since there is
// no separate lock-source digest to carry forward.
func FromBaseline(baseline Catalog, source ProfileSource, history HistoryIdentity, resolved ResolvedChanges) (Plan, error) {
	profile, err := NewProfile(source)
	if err != nil {
		return Plan{}, err
	}
	if err := baseline.validate(); err != nil {
		return Plan{}, err
	}
	return fromBaselineWithSourceDigest(baseline, nil, profile, history, resolved)
}

// fromBaselineWithSourceDigest is the shared body of FromLock and
// FromBaseline. sourceDigest is the lock's own recorded source digest for
// FromLock; passing nil, as FromBaseline does, means "use the catalog digest
// itself", which is the design's answer for a baseline with no lock to carry
// a separate source digest from.
func fromBaselineWithSourceDigest(baseline Catalog, sourceDigest *Digest, profile Profile, history HistoryIdentity, resolved ResolvedChanges) (Plan, error) {
	physical := baseline.physical
	catalogDigest, err := CatalogDigest(baseline)
	if err != nil {
		return Plan{}, err
	}
	if sourceDigest == nil {
		sourceDigest = &catalogDigest
	}
	profileDigest, err := profileDigestValue(profile)
	if err != nil {
		return Plan{}, err
	}
	catalogIdentity, err := NewCatalogIdentity(profile.Engine(), profileDigest, catalogDigest, *sourceDigest)
	if err != nil {
		return Plan{}, err
	}
	if !sameCatalogIdentity(baseline, resolved.baseline) || !samePhysicalObjects(baseline.physical, resolved.baseline.physical) {
		return Plan{}, fmt.Errorf("%w: resolved baseline differs from baseline catalog", ErrInvalidPlan)
	}
	objects := make([]BaselineObject, 0, len(physical.Objects)+len(resolved.futureObjects))
	for _, object := range physical.Objects {
		value, err := NewBaselineObject(ObjectID(object.ID), object.Kind, object.Schema, object.Name)
		if err != nil {
			return Plan{}, err
		}
		objects = append(objects, value)
	}
	objects = append(objects, resolved.futureObjects...)
	baselineIdentity, err := NewBaselineIdentity(catalogIdentity, baseline.sourceIdentity, objects, resolved.renamedObjects)
	if err != nil {
		return Plan{}, err
	}
	if err := validateResolvedState(resolved, baselineIdentity); err != nil {
		return Plan{}, err
	}
	return newPlan(profile, baselineIdentity, history, resolved.decisions, resolved.operations)
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
func profileVersionMatches(profile Profile, observed string) bool {
	version := profile.Version()
	if !version.Known {
		return observed == ""
	}
	want := fmt.Sprintf("%d.%d.%d", version.Major, version.Minor, version.Patch)
	return observed == want || (version.Patch == 0 && observed == fmt.Sprintf("%d.%d", version.Major, version.Minor))
}
func ProfileDigest(source ProfileSource) (Digest, error) {
	profile, err := NewProfile(source)
	if err != nil {
		return Digest{}, err
	}
	return profileDigestValue(profile)
}
func profileDigestValue(profile Profile) (Digest, error) {
	b, err := jsonMarshal(profileToWire(profile))
	if err != nil {
		return Digest{}, err
	}
	return sha256Digest(b), nil
}
func jsonMarshal(value any) ([]byte, error) { return marshalNoHTML(value) }
func cloneCatalog(c Catalog) Catalog {
	return Catalog{physical: c.physical.Clone(), sourceIdentity: c.sourceIdentity}
}

// NewOperationStatements is a small convenience for producers adapting lowered SQL.
func NewOperationStatements(sqls []stmt.Statement) ([]stmt.Statement, error) {
	out := make([]stmt.Statement, len(sqls))
	for i, statement := range sqls {
		args := statement.Args()
		copied := make([]any, len(args))
		for j, value := range args {
			cloned, err := cloneArg(value)
			if err != nil {
				return nil, fmt.Errorf("%w: statement %d argument %d: %v", ErrInvalidOperation, i, j, err)
			}
			copied[j] = cloned
		}
		out[i] = stmt.New(sqltext.Text(statement.SQL()), copied...)
	}
	return out, nil
}
