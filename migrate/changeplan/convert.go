package changeplan

import (
	"fmt"
	"strings"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/compilerlock"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/sqltext"
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
	if err := baseline.Validate(); err != nil {
		return ResolvedChanges{}, fmt.Errorf("%w: baseline catalog: %v", ErrInvalidIdentity, err)
	}
	if err := target.Validate(); err != nil {
		return ResolvedChanges{}, fmt.Errorf("%w: target catalog: %v", ErrInvalidIdentity, err)
	}
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
	if file.Engine.Profile != profile.ID() || !profileEngineMatches(profile.Engine(), file.Engine.Dialect) {
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
	profileDigest, err := profileDigestValue(profile)
	if err != nil {
		return Plan{}, err
	}
	catalogIdentity, err := NewCatalogIdentity(profile.Engine(), profileDigest, catalogDigest, sourceDigest)
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
	if !resolved.baselineEquivalent(physical) {
		return Plan{}, fmt.Errorf("%w: resolved baseline differs from lock catalog", ErrInvalidPlan)
	}
	if err := resolved.target.Validate(); err != nil {
		return Plan{}, fmt.Errorf("%w: resolved target: %v", ErrInvalidPlan, err)
	}
	for _, operation := range resolved.operations {
		if err := EvaluateFacts(resolved.baseline, operation.preconditions); err != nil {
			return Plan{}, fmt.Errorf("%w: operation %q precondition: %v", ErrInvalidPlan, operation.id, err)
		}
		if err := EvaluateFacts(resolved.target, operation.postconditions); err != nil {
			return Plan{}, fmt.Errorf("%w: operation %q postcondition: %v", ErrInvalidPlan, operation.id, err)
		}
	}
	prior := make([]compilerir.PriorObject, 0, len(physical.Objects))
	for _, object := range physical.Objects {
		prior = append(prior, compilerir.PriorObject{ID: object.ID, Kind: object.Kind, Name: compilerir.QualifiedName{Schema: object.Schema, Name: object.Name}})
	}
	renames := make([]compilerir.ObjectRename, 0, len(resolved.renamedObjects))
	for _, rename := range resolved.renamedObjects {
		renames = append(renames, compilerir.ObjectRename{ID: compilerir.ObjectID(rename.object), To: compilerir.QualifiedName{Schema: rename.toSchema, Name: rename.toName}})
	}
	assigned, diagnostics := compilerir.AssignObjectIDs(resolved.target, compilerir.IdentityInput{SourceIdentity: file.Source.Identity, Prior: prior, Renames: renames})
	for _, diagnostic := range diagnostics {
		if diagnostic.Level == compilerir.DiagnosticError {
			return Plan{}, fmt.Errorf("%w: target identity: %s", ErrInvalidPlan, diagnostic.Message)
		}
	}
	assignedByName := make(map[string]compilerir.ObjectID, len(assigned.Objects))
	for _, object := range assigned.Objects {
		assignedByName[object.Kind+"\x00"+object.Schema+"\x00"+object.Name] = object.ID
	}
	for _, object := range resolved.futureObjects {
		want := assignedByName[string(object.kind)+"\x00"+object.schema+"\x00"+object.name]
		if want == "" || ObjectID(want) != object.id {
			return Plan{}, fmt.Errorf("%w: future object %q has an invalid deterministic ID", ErrInvalidIdentity, object.id)
		}
	}
	return newPlan(profile, baseline, history, resolved.decisions, resolved.operations)
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
func (r ResolvedChanges) baselineEquivalent(lock compilerir.PhysicalCatalog) bool {
	left, err := CatalogDigest(r.baseline)
	if err != nil {
		return false
	}
	right, err := CatalogDigest(lock)
	return err == nil && left == right
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
