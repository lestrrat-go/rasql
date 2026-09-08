// Package changeplan defines immutable, serializable migration plans.
package changeplan

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"
	"time"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
)

const Format = "rasql.migration-plan/v1"

type Digest [32]byte
type PlanID Digest
type ObjectID string
type OperationID string
type DecisionID string

type EngineID = engineprofile.EngineID
type EngineVersion = engineprofile.Version
type EngineCapabilities = engineprofile.Capabilities
type EngineLimits = engineprofile.Limits

const (
	PostgreSQLEngine EngineID = engineprofile.PostgreSQL
	MySQLEngine      EngineID = engineprofile.MySQL
	SQLiteEngine     EngineID = engineprofile.SQLite
	CustomEngine     EngineID = engineprofile.Custom
)

type ProfileSource interface {
	ID() string
	Engine() EngineID
	Version() EngineVersion
	Capabilities() EngineCapabilities
	Limits() EngineLimits
}

type Profile struct {
	profile engineprofile.Profile
}

func NewProfile(source ProfileSource) (Profile, error) {
	if nilProfileSource(source) {
		return Profile{}, fmt.Errorf("%w: profile source is nil", ErrInvalidPlan)
	}
	id := source.ID()
	engine := source.Engine()
	version := source.Version()
	caps := source.Capabilities()
	limits := source.Limits()
	customName := ""
	if engine == engineprofile.Custom {
		const prefix = "custom:"
		if !strings.HasPrefix(id, prefix) || len(id) == len(prefix) {
			return Profile{}, fmt.Errorf("%w: custom profile ID must have a name", ErrInvalidPlan)
		}
		customName = id[len(prefix):]
	}
	return newProfile(id, engine, customName, version, caps, limits)
}

func newProfile(id string, engine engineprofile.EngineID, customName string, version engineprofile.Version, caps engineprofile.Capabilities, limits engineprofile.Limits) (Profile, error) {
	value, err := engineprofile.New(id, engine, customName, version, caps, limits)
	if err != nil {
		return Profile{}, err
	}
	return Profile{profile: value}, nil
}

func nilProfileSource(source ProfileSource) bool {
	if source == nil {
		return true
	}
	value := reflect.ValueOf(source)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (p Profile) ID() string                       { return p.profile.ID }
func (p Profile) Engine() EngineID                 { return p.profile.Engine }
func (p Profile) CustomName() string               { return p.profile.CustomName }
func (p Profile) Version() EngineVersion           { return p.profile.Version }
func (p Profile) Capabilities() EngineCapabilities { return p.profile.Capabilities }
func (p Profile) Limits() EngineLimits             { return p.profile.Limits }

func (d Digest) String() string { return hex.EncodeToString(d[:]) }
func (d PlanID) String() string { return Digest(d).String() }

var (
	ErrInvalidPlan         = errors.New("migration plan: invalid plan")
	ErrInvalidDecision     = errors.New("migration plan: invalid decision")
	ErrInvalidOperation    = errors.New("migration plan: invalid operation")
	ErrInvalidFact         = errors.New("migration plan: invalid fact")
	ErrInvalidIdentity     = errors.New("migration plan: invalid identity")
	ErrInvalidWire         = errors.New("migration plan: invalid wire data")
	ErrDependencyCycle     = errors.New("migration plan: dependency cycle")
	ErrFactMismatch        = errors.New("migration plan: fact mismatch")
	ErrUnsupportedArg      = errors.New("migration plan: unsupported statement argument")
	ErrInvalidSourceDigest = errors.New("migration plan: invalid source digest")
)

type DecisionKind string

const (
	DecisionRenameObject          DecisionKind = "rename_object"
	DecisionAcceptDestructive     DecisionKind = "accept_destructive"
	DecisionSupplyBackfill        DecisionKind = "supply_backfill"
	DecisionAcceptNativeSQL       DecisionKind = "accept_native_sql"
	DecisionRename                             = DecisionRenameObject
	DecisionDestructive                        = DecisionAcceptDestructive
	DecisionBackfill                           = DecisionSupplyBackfill
	DecisionNativeSQL                          = DecisionAcceptNativeSQL
	DecisionKindRename                         = DecisionRenameObject
	DecisionKindAcceptDestructive              = DecisionAcceptDestructive
	DecisionKindSupplyBackfill                 = DecisionSupplyBackfill
	DecisionKindAcceptNativeSQL                = DecisionAcceptNativeSQL
)

type OperationKind string

const (
	OperationCreateTable        OperationKind = "create_table"
	OperationDropTable          OperationKind = "drop_table"
	OperationRenameTable        OperationKind = "rename_table"
	OperationAddColumn          OperationKind = "add_column"
	OperationDropColumn         OperationKind = "drop_column"
	OperationRenameColumn       OperationKind = "rename_column"
	OperationAlterColumn        OperationKind = "alter_column"
	OperationCreateIndex        OperationKind = "create_index"
	OperationDropIndex          OperationKind = "drop_index"
	OperationAddConstraint      OperationKind = "add_constraint"
	OperationDropConstraint     OperationKind = "drop_constraint"
	OperationBackfill           OperationKind = "backfill"
	OperationNativeSQL          OperationKind = "native_sql"
	OperationKindCreateTable                  = OperationCreateTable
	OperationKindDropTable                    = OperationDropTable
	OperationKindRenameTable                  = OperationRenameTable
	OperationKindAddColumn                    = OperationAddColumn
	OperationKindDropColumn                   = OperationDropColumn
	OperationKindRenameColumn                 = OperationRenameColumn
	OperationKindAlterColumn                  = OperationAlterColumn
	OperationKindCreateIndex                  = OperationCreateIndex
	OperationKindDropIndex                    = OperationDropIndex
	OperationKindAddConstraint                = OperationAddConstraint
	OperationKindDropConstraint               = OperationDropConstraint
	OperationKindBackfill                     = OperationBackfill
	OperationKindNativeSQL                    = OperationNativeSQL
)

type TransactionMode string

const (
	TransactionRequired          TransactionMode = "required"
	TransactionForbidden         TransactionMode = "forbidden"
	TransactionEngineDefault     TransactionMode = "engine_default"
	TransactionModeRequired                      = TransactionRequired
	TransactionModeForbidden                     = TransactionForbidden
	TransactionModeEngineDefault                 = TransactionEngineDefault
)

type FactOperator string

const (
	FactOperatorEqual   FactOperator = "equal"
	FactOperatorAbsent  FactOperator = "absent"
	FactOperatorPresent FactOperator = "present"
	FactEqual                        = FactOperatorEqual
	FactAbsent                       = FactOperatorAbsent
	FactPresent                      = FactOperatorPresent
	OperatorEqual                    = FactOperatorEqual
	OperatorAbsent                   = FactOperatorAbsent
	OperatorPresent                  = FactOperatorPresent
)

type CatalogIdentity struct {
	engine        engineprofile.EngineID
	profileDigest Digest
	catalogDigest Digest
	sourceDigest  Digest
}

func NewCatalogIdentity(engine EngineID, profileDigest, catalogDigest, sourceDigest Digest) (CatalogIdentity, error) {
	if engine < engineprofile.PostgreSQL || engine > engineprofile.Custom {
		return CatalogIdentity{}, fmt.Errorf("%w: engine", ErrInvalidIdentity)
	}
	return CatalogIdentity{engine: engine, profileDigest: profileDigest, catalogDigest: catalogDigest, sourceDigest: sourceDigest}, nil
}
func (c CatalogIdentity) Engine() EngineID      { return c.engine }
func (c CatalogIdentity) ProfileDigest() Digest { return c.profileDigest }
func (c CatalogIdentity) CatalogDigest() Digest { return c.catalogDigest }
func (c CatalogIdentity) SourceDigest() Digest  { return c.sourceDigest }

type BaselineObject struct {
	id           ObjectID
	kind, schema string
	name         string
	introducedBy OperationID
}

func NewBaselineObject(id ObjectID, kind, schema, name string) (BaselineObject, error) {
	return newBaselineObject(id, kind, schema, name, "")
}

func NewIntroducedBaselineObject(sourceIdentity string, introducedBy OperationID, definition schema.TableDef) (BaselineObject, error) {
	if strings.TrimSpace(sourceIdentity) == "" || introducedBy == "" {
		return BaselineObject{}, fmt.Errorf("%w: introduced object identity is required", ErrInvalidIdentity)
	}
	definition = definition.Clone()
	if err := definition.Validate(); err != nil {
		return BaselineObject{}, fmt.Errorf("%w: introduced object: %v", ErrInvalidIdentity, err)
	}
	id, err := introducedObjectID(sourceIdentity, introducedBy, string(definition.EffectiveKind()), definition.Schema, definition.Name)
	if err != nil {
		return BaselineObject{}, err
	}
	return newBaselineObject(id, string(definition.EffectiveKind()), definition.Schema, definition.Name, introducedBy)
}

func newBaselineObject(id ObjectID, kind, schema, name string, introducedBy OperationID) (BaselineObject, error) {
	if id == "" || strings.TrimSpace(kind) == "" || strings.TrimSpace(name) == "" {
		return BaselineObject{}, fmt.Errorf("%w: baseline object fields are required", ErrInvalidIdentity)
	}
	return BaselineObject{id: id, kind: kind, schema: schema, name: name, introducedBy: introducedBy}, nil
}

func introducedObjectID(sourceIdentity string, introducedBy OperationID, kind, schemaName, name string) (ObjectID, error) {
	if strings.TrimSpace(sourceIdentity) == "" || introducedBy == "" {
		return "", fmt.Errorf("%w: introduced object identity is required", ErrInvalidIdentity)
	}
	var namespace bytes.Buffer
	namespace.WriteString("rasql.object-id/introduced/v1\x00")
	for _, value := range []string{sourceIdentity, string(introducedBy)} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		namespace.Write(length[:])
		namespace.WriteString(value)
	}
	physical := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite", Version: "3.0", Profile: "sqlite-3.35"}, Objects: []compilerir.PhysicalObject{{Kind: kind, Schema: schemaName, Name: name, Columns: []compilerir.PhysicalColumn{{Name: "id", Ordinal: 0, LogicalKind: "integer"}}}}}
	assigned, diagnostics := compilerir.AssignObjectIDs(physical, compilerir.IdentityInput{SourceIdentity: namespace.String()})
	for _, diagnostic := range diagnostics {
		if diagnostic.Level == compilerir.DiagnosticError {
			return "", fmt.Errorf("%w: introduced object ID: %s", ErrInvalidIdentity, diagnostic.Message)
		}
	}
	if len(assigned.Objects) != 1 || assigned.Objects[0].ID == "" {
		return "", fmt.Errorf("%w: introduced object ID was not assigned", ErrInvalidIdentity)
	}
	return ObjectID(assigned.Objects[0].ID), nil
}
func (o BaselineObject) ID() ObjectID              { return o.id }
func (o BaselineObject) Kind() string              { return o.kind }
func (o BaselineObject) Schema() string            { return o.schema }
func (o BaselineObject) Name() string              { return o.name }
func (o BaselineObject) IntroducedBy() OperationID { return o.introducedBy }

type BaselineRename struct {
	operation OperationID
	object    ObjectID
	toSchema  string
	toName    string
}

func NewBaselineRename(operation OperationID, object ObjectID, toSchema, toName string) (BaselineRename, error) {
	if operation == "" || object == "" || strings.TrimSpace(toName) == "" {
		return BaselineRename{}, fmt.Errorf("%w: baseline rename fields are required", ErrInvalidIdentity)
	}
	return BaselineRename{operation: operation, object: object, toSchema: toSchema, toName: toName}, nil
}
func (r BaselineRename) Operation() OperationID { return r.operation }
func (r BaselineRename) Object() ObjectID       { return r.object }
func (r BaselineRename) ToSchema() string       { return r.toSchema }
func (r BaselineRename) ToName() string         { return r.toName }

type BaselineIdentity struct {
	catalog        CatalogIdentity
	sourceIdentity string
	objects        []BaselineObject
	renames        []BaselineRename
}

func NewBaselineIdentity(catalog CatalogIdentity, sourceIdentity string, objects []BaselineObject, renames []BaselineRename) (BaselineIdentity, error) {
	if strings.TrimSpace(sourceIdentity) == "" {
		return BaselineIdentity{}, fmt.Errorf("%w: source identity is required", ErrInvalidIdentity)
	}
	out := BaselineIdentity{catalog: catalog, sourceIdentity: sourceIdentity}
	out.objects = append([]BaselineObject(nil), objects...)
	out.renames = append([]BaselineRename(nil), renames...)
	if out.objects == nil {
		out.objects = make([]BaselineObject, 0)
	}
	if out.renames == nil {
		out.renames = make([]BaselineRename, 0)
	}
	if err := validateBaseline(out); err != nil {
		return BaselineIdentity{}, err
	}
	return out, nil
}
func (b BaselineIdentity) Catalog() CatalogIdentity { return b.catalog }
func (b BaselineIdentity) SourceIdentity() string   { return b.sourceIdentity }
func (b BaselineIdentity) Objects() []BaselineObject {
	return append([]BaselineObject(nil), b.objects...)
}
func (b BaselineIdentity) Renames() []BaselineRename {
	return append([]BaselineRename(nil), b.renames...)
}

type HistoryIdentity struct{ schema, table string }

func NewHistoryIdentity(schema, table string) (HistoryIdentity, error) {
	if strings.TrimSpace(table) == "" {
		return HistoryIdentity{}, fmt.Errorf("%w: history table is required", ErrInvalidIdentity)
	}
	return HistoryIdentity{schema: schema, table: table}, nil
}
func (h HistoryIdentity) Schema() string { return h.schema }
func (h HistoryIdentity) Table() string  { return h.table }

type Fact struct {
	object                         ObjectID
	path, operator, canonicalValue string
}

func NewFact(object ObjectID, path string, operator any, canonicalValue string) (Fact, error) {
	if object == "" || (path != "$" && !strings.HasPrefix(path, "/")) {
		return Fact{}, fmt.Errorf("%w: object and RFC 6901 path are required", ErrInvalidFact)
	}
	operatorValue, ok := operator.(FactOperator)
	if !ok {
		if value, stringOK := operator.(string); stringOK {
			operatorValue = FactOperator(value)
			ok = true
		}
	}
	if !ok || operatorValue != FactOperatorEqual && operatorValue != FactOperatorAbsent && operatorValue != FactOperatorPresent {
		return Fact{}, fmt.Errorf("%w: unknown operator %q", ErrInvalidFact, operator)
	}
	if operatorValue == FactOperatorEqual {
		canonical, err := canonicalJSON([]byte(canonicalValue))
		if err != nil {
			return Fact{}, fmt.Errorf("%w: canonical value: %v", ErrInvalidFact, err)
		}
		canonicalValue = string(canonical)
	} else if canonicalValue != "" {
		return Fact{}, fmt.Errorf("%w: non-equal facts have no value", ErrInvalidFact)
	} else if path != "$" {
		return Fact{}, fmt.Errorf("%w: present/absent facts require the object path", ErrInvalidFact)
	}
	if operatorValue != FactOperatorEqual && path == "" {
		return Fact{}, fmt.Errorf("%w: path is required", ErrInvalidFact)
	}
	return Fact{object: object, path: path, operator: string(operatorValue), canonicalValue: canonicalValue}, nil
}
func (f Fact) Object() ObjectID       { return f.object }
func (f Fact) Path() string           { return f.path }
func (f Fact) Operator() string       { return f.operator }
func (f Fact) CanonicalValue() string { return f.canonicalValue }

type Decision struct {
	id       DecisionID
	kind     DecisionKind
	object   ObjectID
	from, to string
	accepted bool
	reason   string
}

func NewDecision(id DecisionID, kind DecisionKind, object ObjectID, from, to string, accepted bool, reason string) (Decision, error) {
	if id == "" || object == "" {
		return Decision{}, fmt.Errorf("%w: id and object are required", ErrInvalidDecision)
	}
	switch kind {
	case DecisionRenameObject:
		if !accepted || from == "" || to == "" || from == to || reason != "" {
			return Decision{}, fmt.Errorf("%w: invalid rename decision", ErrInvalidDecision)
		}
	case DecisionAcceptDestructive, DecisionAcceptNativeSQL:
		if !accepted || from != "" || to != "" || strings.TrimSpace(reason) == "" {
			return Decision{}, fmt.Errorf("%w: invalid approval decision", ErrInvalidDecision)
		}
	case DecisionSupplyBackfill:
		if !accepted || from != "" || to != "" || strings.TrimSpace(reason) == "" {
			return Decision{}, fmt.Errorf("%w: invalid backfill decision", ErrInvalidDecision)
		}
	default:
		return Decision{}, fmt.Errorf("%w: unknown kind %q", ErrInvalidDecision, kind)
	}
	return Decision{id: id, kind: kind, object: object, from: from, to: to, accepted: accepted, reason: reason}, nil
}
func (d Decision) ID() DecisionID     { return d.id }
func (d Decision) Kind() DecisionKind { return d.kind }
func (d Decision) Object() ObjectID   { return d.object }
func (d Decision) From() string       { return d.from }
func (d Decision) To() string         { return d.to }
func (d Decision) Accepted() bool     { return d.accepted }
func (d Decision) Reason() string     { return d.reason }

type Operation struct {
	id                            OperationID
	kind                          OperationKind
	dependsOn                     []OperationID
	objects                       []ObjectID
	preconditions, postconditions []Fact
	resultDigest                  Digest
	statements                    []stmt.Statement
	transaction                   TransactionMode
	reversible                    bool
	reverseStatements             []stmt.Statement
}

func NewOperation(
	id OperationID,
	kind OperationKind,
	dependsOn []OperationID,
	objects []ObjectID,
	preconditions, postconditions []Fact,
	resultDigest Digest,
	statements []stmt.Statement,
	transaction TransactionMode,
	reversible bool,
	reverseStatements []stmt.Statement,
) (Operation, error) {
	if id == "" || len(strings.TrimSpace(string(kind))) == 0 {
		return Operation{}, fmt.Errorf("%w: id and kind are required", ErrInvalidOperation)
	}
	if resultDigest == (Digest{}) {
		return Operation{}, fmt.Errorf("%w: result digest is required", ErrInvalidOperation)
	}
	if !validOperationKind(kind) || !validTransaction(transaction) {
		return Operation{}, fmt.Errorf("%w: unknown kind or transaction mode", ErrInvalidOperation)
	}
	for _, dependency := range dependsOn {
		if dependency == id {
			return Operation{}, fmt.Errorf("%w: operation depends on itself", ErrInvalidOperation)
		}
	}
	if len(objects) == 0 {
		return Operation{}, fmt.Errorf("%w: operation needs an object", ErrInvalidOperation)
	}
	if kind == OperationCreateTable && len(objects) != 1 {
		return Operation{}, fmt.Errorf("%w: create_table needs one object", ErrInvalidOperation)
	}
	if len(statements) == 0 {
		return Operation{}, fmt.Errorf("%w: operation needs forward SQL", ErrInvalidOperation)
	}
	if kind != OperationBackfill && kind != OperationNativeSQL {
		if err := validateStatements(statements, false); err != nil {
			return Operation{}, err
		}
	}
	if kind == OperationBackfill || kind == OperationNativeSQL {
		if err := validateStatements(statements, true); err != nil {
			return Operation{}, err
		}
	}
	if err := validateStatements(reverseStatements, kind == OperationBackfill || kind == OperationNativeSQL); err != nil {
		return Operation{}, err
	}
	if reversible && len(reverseStatements) == 0 {
		return Operation{}, fmt.Errorf("%w: reversible operation has no reverse SQL", ErrInvalidOperation)
	}
	if !reversible && len(reverseStatements) != 0 {
		return Operation{}, fmt.Errorf("%w: irreversible operation has reverse SQL", ErrInvalidOperation)
	}
	out := Operation{id: id, kind: kind, resultDigest: resultDigest, transaction: transaction, reversible: reversible}
	out.dependsOn = append([]OperationID(nil), dependsOn...)
	out.objects = append([]ObjectID(nil), objects...)
	out.preconditions = append([]Fact(nil), preconditions...)
	out.postconditions = append([]Fact(nil), postconditions...)
	out.statements = cloneStatements(statements)
	out.reverseStatements = cloneStatements(reverseStatements)
	for _, fact := range append(append([]Fact(nil), preconditions...), postconditions...) {
		if err := validateFact(fact); err != nil {
			return Operation{}, err
		}
	}
	if out.dependsOn == nil {
		out.dependsOn = make([]OperationID, 0)
	}
	if out.objects == nil {
		out.objects = make([]ObjectID, 0)
	}
	if out.preconditions == nil {
		out.preconditions = make([]Fact, 0)
	}
	if out.postconditions == nil {
		out.postconditions = make([]Fact, 0)
	}
	if out.statements == nil {
		out.statements = make([]stmt.Statement, 0)
	}
	if out.reverseStatements == nil {
		out.reverseStatements = make([]stmt.Statement, 0)
	}
	return out, nil
}
func validateStatements(statements []stmt.Statement, argumentsAllowed bool) error {
	for _, statement := range statements {
		if strings.TrimSpace(statement.SQL()) == "" {
			return fmt.Errorf("%w: statement SQL is empty", ErrInvalidOperation)
		}
		for _, argument := range statement.Args() {
			if !argumentsAllowed {
				return fmt.Errorf("%w: DDL statement has arguments", ErrInvalidOperation)
			}
			if _, err := cloneArg(argument); err != nil {
				return fmt.Errorf("%w: %w", ErrInvalidOperation, err)
			}
		}
	}
	return nil
}
func (o Operation) ID() OperationID                     { return o.id }
func (o Operation) Kind() OperationKind                 { return o.kind }
func (o Operation) DependsOn() []OperationID            { return append([]OperationID(nil), o.dependsOn...) }
func (o Operation) Objects() []ObjectID                 { return append([]ObjectID(nil), o.objects...) }
func (o Operation) Preconditions() []Fact               { return append([]Fact(nil), o.preconditions...) }
func (o Operation) Postconditions() []Fact              { return append([]Fact(nil), o.postconditions...) }
func (o Operation) ResultDigest() Digest                { return o.resultDigest }
func (o Operation) Statements() []stmt.Statement        { return cloneStatements(o.statements) }
func (o Operation) Transaction() TransactionMode        { return o.transaction }
func (o Operation) Reversible() bool                    { return o.reversible }
func (o Operation) ReverseStatements() []stmt.Statement { return cloneStatements(o.reverseStatements) }

type Plan struct {
	id         PlanID
	profile    Profile
	baseline   BaselineIdentity
	history    HistoryIdentity
	decisions  []Decision
	operations []Operation
}

func NewPlan(source ProfileSource, baseline BaselineIdentity, history HistoryIdentity, decisions []Decision, operations []Operation) (Plan, error) {
	profile, err := NewProfile(source)
	if err != nil {
		return Plan{}, err
	}
	return newPlan(profile, baseline, history, decisions, operations)
}

func newPlan(profile Profile, baseline BaselineIdentity, history HistoryIdentity, decisions []Decision, operations []Operation) (Plan, error) {
	if err := validateBaseline(baseline); err != nil {
		return Plan{}, err
	}
	if history.table == "" {
		return Plan{}, fmt.Errorf("%w: history table is required", ErrInvalidPlan)
	}
	if err := validatePlanParts(baseline, decisions, operations); err != nil {
		return Plan{}, err
	}
	if err := validateFutureObjectIDs(baseline, operations); err != nil {
		return Plan{}, err
	}
	p := Plan{profile: profile, baseline: cloneBaseline(baseline), history: history}
	if err := validatePlanIdentity(p); err != nil {
		return Plan{}, err
	}
	p.decisions = append([]Decision(nil), decisions...)
	p.operations = append([]Operation(nil), operations...)
	for i := range p.operations {
		p.operations[i] = cloneOperation(p.operations[i])
	}
	if p.decisions == nil {
		p.decisions = make([]Decision, 0)
	}
	if p.operations == nil {
		p.operations = make([]Operation, 0)
	}
	digest, err := planDigest(p)
	if err != nil {
		return Plan{}, err
	}
	p.id = PlanID(digest)
	return p, nil
}
func (p Plan) ID() PlanID                 { return p.id }
func (p Plan) Profile() Profile           { return p.profile }
func (p Plan) Baseline() BaselineIdentity { return cloneBaseline(p.baseline) }
func (p Plan) History() HistoryIdentity   { return p.history }
func (p Plan) Decisions() []Decision      { return append([]Decision(nil), p.decisions...) }
func (p Plan) Operations() []Operation {
	out := append([]Operation(nil), p.operations...)
	for i := range out {
		out[i] = cloneOperation(out[i])
	}
	return out
}

func cloneBaseline(in BaselineIdentity) BaselineIdentity {
	out := in
	out.objects = append([]BaselineObject(nil), in.objects...)
	out.renames = append([]BaselineRename(nil), in.renames...)
	return out
}
func cloneOperation(in Operation) Operation {
	in.dependsOn = append([]OperationID(nil), in.dependsOn...)
	in.objects = append([]ObjectID(nil), in.objects...)
	in.preconditions = append([]Fact(nil), in.preconditions...)
	in.postconditions = append([]Fact(nil), in.postconditions...)
	in.statements = cloneStatements(in.statements)
	in.reverseStatements = cloneStatements(in.reverseStatements)
	return in
}
func cloneArg(value any) (any, error) {
	switch v := value.(type) {
	case nil, bool, int64, uint64, string:
		return v, nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, ErrUnsupportedArg
		}
		return v, nil
	case []byte:
		return bytes.Clone(v), nil
	case time.Time:
		return v, nil
	case sql.NamedArg:
		return nil, ErrUnsupportedArg
	default:
		return nil, ErrUnsupportedArg
	}
}
func cloneStatements(in []stmt.Statement) []stmt.Statement {
	if in == nil {
		return nil
	}
	out := make([]stmt.Statement, len(in))
	for i, source := range in {
		args := source.Args()
		copied := make([]any, len(args))
		for j, value := range args {
			var err error
			copied[j], err = cloneArg(value)
			if err != nil {
				copied[j] = nil
			}
		}
		out[i] = stmt.New(sqltext.Text(source.SQL()), copied...)
	}
	return out
}

func digestHex(d Digest) string { return hex.EncodeToString(d[:]) }
func DigestHex(d Digest) string { return digestHex(d) }
func parseDigest(s string) (Digest, error) {
	var d Digest
	if len(s) != sha256.Size*2 || strings.ToLower(s) != s {
		return d, fmt.Errorf("%w: digest must be lowercase hex", ErrInvalidWire)
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return d, fmt.Errorf("%w: digest: %v", ErrInvalidWire, err)
	}
	copy(d[:], b)
	return d, nil
}
func ParseDigest(s string) (Digest, error) { return parseDigest(s) }
func canonicalJSON(data []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, errors.New("trailing JSON")
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

func encodeArg(value any) (argWire, error) {
	switch v := value.(type) {
	case nil:
		return argWire{Kind: "null", Value: json.RawMessage("null")}, nil
	case bool:
		b, _ := marshalNoHTML(v)
		return argWire{Kind: "bool", Value: b}, nil
	case int64:
		return argWire{Kind: "int64", Value: json.RawMessage(fmt.Sprintf("%d", v))}, nil
	case uint64:
		return argWire{Kind: "uint64", Value: json.RawMessage(fmt.Sprintf("%d", v))}, nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return argWire{}, ErrUnsupportedArg
		}
		b, _ := marshalNoHTML(v)
		return argWire{Kind: "float64", Value: b}, nil
	case string:
		b, _ := marshalNoHTML(v)
		return argWire{Kind: "string", Value: b}, nil
	case []byte:
		b, _ := marshalNoHTML(base64.StdEncoding.EncodeToString(v))
		return argWire{Kind: "bytes_base64", Value: b}, nil
	case time.Time:
		b, _ := marshalNoHTML(v.Format(time.RFC3339Nano))
		return argWire{Kind: "time_rfc3339nano", Value: b}, nil
	default:
		return argWire{}, ErrUnsupportedArg
	}
}
