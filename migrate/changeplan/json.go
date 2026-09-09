package changeplan

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
)

type planWire struct {
	Format     string          `json:"format"`
	ID         string          `json:"id,omitempty"`
	Profile    profileWire     `json:"profile"`
	Baseline   baselineWire    `json:"baseline"`
	History    historyWire     `json:"history"`
	Decisions  []decisionWire  `json:"decisions"`
	Operations []operationWire `json:"operations"`
}
type profileWire struct {
	Engine            string           `json:"engine"`
	CustomName        string           `json:"custom_name"`
	VersionKnown      bool             `json:"version_known"`
	Version           versionWire      `json:"version"`
	MaxBindParameters int              `json:"max_bind_parameters"`
	Capabilities      capabilitiesWire `json:"capabilities"`
}
type versionWire struct {
	Major uint16 `json:"major"`
	Minor uint16 `json:"minor"`
	Patch uint16 `json:"patch"`
}
type capabilitiesWire struct {
	Returning            string `json:"returning"`
	Upsert               string `json:"upsert"`
	ConflictTarget       bool   `json:"conflict_target"`
	DefaultValues        bool   `json:"default_values"`
	EmptyInsert          bool   `json:"empty_insert"`
	DefaultValuesUpsert  bool   `json:"default_values_upsert"`
	SubqueryLimit        bool   `json:"subquery_limit"`
	WriteSubqueryTarget  bool   `json:"write_subquery_target"`
	PartialIndex         bool   `json:"partial_index"`
	AggregateFilter      bool   `json:"aggregate_filter"`
	QualifiedReference   bool   `json:"qualified_reference"`
	QualifiedIndexTarget bool   `json:"qualified_index_target"`
	QualifiedIndexName   bool   `json:"qualified_index_name"`
	MatchOperator        bool   `json:"match_operator"`
	SelectForUpdate      bool   `json:"select_for_update"`
	SelectForShare       bool   `json:"select_for_share"`
	SelectLockOf         bool   `json:"select_lock_of"`
	SelectLockNoWait     bool   `json:"select_lock_no_wait"`
	SelectLockSkipLocked bool   `json:"select_lock_skip_locked"`
	UpsertConflictWhere  bool   `json:"upsert_conflict_where"`
	UpsertUpdateWhere    bool   `json:"upsert_update_where"`
	WindowFunctions      bool   `json:"window_functions"`
	LateralJoins         bool   `json:"lateral_joins"`
	Savepoints           bool   `json:"savepoints"`
	TransactionalDDL     bool   `json:"transactional_ddl"`
	ExplicitNullOrdering bool   `json:"explicit_null_ordering"`
	TupleComparison      bool   `json:"tuple_comparison"`
	PerParentLimit       string `json:"per_parent_limit"`
	UpdateDefault        string `json:"update_default"`
}
type baselineWire struct {
	Engine         string       `json:"engine"`
	ProfileDigest  string       `json:"profile_digest"`
	CatalogDigest  string       `json:"catalog_digest"`
	SourceDigest   string       `json:"source_digest"`
	SourceIdentity string       `json:"source_identity"`
	Objects        []objectWire `json:"objects"`
	Renames        []renameWire `json:"renames"`
}
type objectWire struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Schema       string `json:"schema"`
	Name         string `json:"name"`
	IntroducedBy string `json:"introduced_by"`
}
type renameWire struct {
	Operation string `json:"operation"`
	Object    string `json:"object"`
	ToSchema  string `json:"to_schema"`
	ToName    string `json:"to_name"`
}
type historyWire struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
}
type decisionWire struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Object   string `json:"object"`
	From     string `json:"from"`
	To       string `json:"to"`
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason"`
}
type operationWire struct {
	ID                string          `json:"id"`
	Kind              string          `json:"kind"`
	DependsOn         []string        `json:"depends_on"`
	Objects           []string        `json:"objects"`
	Preconditions     []factWire      `json:"preconditions"`
	Postconditions    []factWire      `json:"postconditions"`
	ResultDigest      string          `json:"result_digest"`
	Statements        []statementWire `json:"statements"`
	Transaction       string          `json:"transaction"`
	Reversible        bool            `json:"reversible"`
	ReverseStatements []statementWire `json:"reverse_statements"`
}
type factWire struct {
	Object   string `json:"object"`
	Path     string `json:"path"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
}
type statementWire struct {
	SQL  string    `json:"sql"`
	Args []argWire `json:"args"`
}
type argWire struct {
	Kind  string          `json:"kind"`
	Value json.RawMessage `json:"value"`
}

func marshalNoHTML(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'}), nil
}

func profileToWire(p Profile) profileWire {
	value := p.profile
	c := value.Capabilities
	return profileWire{Engine: engineName(value.Engine), CustomName: value.CustomName, VersionKnown: value.Version.Known,
		Version: versionWire{value.Version.Major, value.Version.Minor, value.Version.Patch}, MaxBindParameters: value.Limits.MaxBindParameters,
		Capabilities: capabilitiesWire{Returning: returningName(c.Returning), Upsert: upsertName(c.Upsert), ConflictTarget: c.ConflictTarget,
			DefaultValues: c.DefaultValues, EmptyInsert: c.EmptyInsert, DefaultValuesUpsert: c.DefaultValuesUpsert,
			SubqueryLimit: c.SubqueryLimit, WriteSubqueryTarget: c.WriteSubqueryTarget, PartialIndex: c.PartialIndex,
			AggregateFilter: c.AggregateFilter, QualifiedReference: c.QualifiedReference, QualifiedIndexTarget: c.QualifiedIndexTarget,
			QualifiedIndexName: c.QualifiedIndexName, MatchOperator: c.MatchOperator, SelectForUpdate: c.SelectForUpdate,
			SelectForShare: c.SelectForShare, SelectLockOf: c.SelectLockOf, SelectLockNoWait: c.SelectLockNoWait,
			SelectLockSkipLocked: c.SelectLockSkipLocked, UpsertConflictWhere: c.UpsertConflictWhere, UpsertUpdateWhere: c.UpsertUpdateWhere,
			WindowFunctions: c.WindowFunctions, LateralJoins: c.LateralJoins, Savepoints: c.Savepoints, TransactionalDDL: c.TransactionalDDL,
			ExplicitNullOrdering: c.ExplicitNullOrdering, TupleComparison: c.TupleComparison, PerParentLimit: perParentName(c.PerParentLimit),
			UpdateDefault: updateDefaultName(c.UpdateDefault)}}
}
func profileFromWire(w profileWire) (Profile, error) {
	e, ok := parseEngine(w.Engine)
	if !ok {
		return Profile{}, fmt.Errorf("%w: engine", ErrInvalidWire)
	}
	c := engineprofile.Capabilities{}
	var err error
	if c.Returning, err = parseReturning(w.Capabilities.Returning); err != nil {
		return Profile{}, err
	}
	if c.Upsert, err = parseUpsert(w.Capabilities.Upsert); err != nil {
		return Profile{}, err
	}
	if c.PerParentLimit, err = parsePerParent(w.Capabilities.PerParentLimit); err != nil {
		return Profile{}, err
	}
	if c.UpdateDefault, err = parseUpdateDefault(w.Capabilities.UpdateDefault); err != nil {
		return Profile{}, err
	}
	capWire := w.Capabilities
	c.ConflictTarget, c.DefaultValues, c.EmptyInsert, c.DefaultValuesUpsert = capWire.ConflictTarget, capWire.DefaultValues, capWire.EmptyInsert, capWire.DefaultValuesUpsert
	c.SubqueryLimit, c.WriteSubqueryTarget, c.PartialIndex, c.AggregateFilter = capWire.SubqueryLimit, capWire.WriteSubqueryTarget, capWire.PartialIndex, capWire.AggregateFilter
	c.QualifiedReference, c.QualifiedIndexTarget, c.QualifiedIndexName, c.MatchOperator = capWire.QualifiedReference, capWire.QualifiedIndexTarget, capWire.QualifiedIndexName, capWire.MatchOperator
	c.SelectForUpdate, c.SelectForShare, c.SelectLockOf, c.SelectLockNoWait, c.SelectLockSkipLocked = capWire.SelectForUpdate, capWire.SelectForShare, capWire.SelectLockOf, capWire.SelectLockNoWait, capWire.SelectLockSkipLocked
	c.UpsertConflictWhere, c.UpsertUpdateWhere = capWire.UpsertConflictWhere, capWire.UpsertUpdateWhere
	c.WindowFunctions, c.LateralJoins, c.Savepoints, c.TransactionalDDL = capWire.WindowFunctions, capWire.LateralJoins, capWire.Savepoints, capWire.TransactionalDDL
	c.ExplicitNullOrdering, c.TupleComparison = capWire.ExplicitNullOrdering, capWire.TupleComparison
	profileID := w.Engine
	switch e {
	case engineprofile.Custom:
		profileID = "custom:" + w.CustomName
	case engineprofile.PostgreSQL:
		profileID = fmt.Sprintf("postgresql-%d", w.Version.Major)
	case engineprofile.MySQL:
		profileID = fmt.Sprintf("mysql-%d.%d", w.Version.Major, w.Version.Minor)
	case engineprofile.SQLite:
		profileID = "sqlite-3.35"
	}
	return newProfile(profileID, e, w.CustomName, engineprofile.Version{Known: w.VersionKnown, Major: w.Version.Major, Minor: w.Version.Minor, Patch: w.Version.Patch}, c, engineprofile.Limits{MaxBindParameters: w.MaxBindParameters})
}
func engineName(e engineprofile.EngineID) string {
	switch e {
	case engineprofile.PostgreSQL:
		return "postgresql"
	case engineprofile.MySQL:
		return "mysql"
	case engineprofile.SQLite:
		return "sqlite"
	case engineprofile.Custom:
		return "custom"
	default:
		return ""
	}
}
func parseEngine(s string) (engineprofile.EngineID, bool) {
	switch s {
	case "postgresql":
		return engineprofile.PostgreSQL, true
	case "mysql":
		return engineprofile.MySQL, true
	case "sqlite":
		return engineprofile.SQLite, true
	case "custom":
		return engineprofile.Custom, true
	default:
		return 0, false
	}
}
func returningName(v engineprofile.ReturningForms) string {
	switch v {
	case engineprofile.ReturningNone:
		return "none"
	case engineprofile.ReturningInsert:
		return "insert"
	case engineprofile.ReturningInsertUpdateDelete:
		return "all"
	default:
		return ""
	}
}
func parseReturning(s string) (engineprofile.ReturningForms, error) {
	switch s {
	case "none":
		return engineprofile.ReturningNone, nil
	case "insert":
		return engineprofile.ReturningInsert, nil
	case "all":
		return engineprofile.ReturningInsertUpdateDelete, nil
	default:
		return 0, fmt.Errorf("%w: returning enum", ErrInvalidWire)
	}
}
func upsertName(v engineprofile.UpsertForm) string {
	switch v {
	case engineprofile.UpsertNone:
		return "none"
	case engineprofile.UpsertOnConflict:
		return "on_conflict"
	case engineprofile.UpsertDuplicateKey:
		return "duplicate_key"
	default:
		return ""
	}
}
func parseUpsert(s string) (engineprofile.UpsertForm, error) {
	switch s {
	case "none":
		return engineprofile.UpsertNone, nil
	case "on_conflict":
		return engineprofile.UpsertOnConflict, nil
	case "duplicate_key":
		return engineprofile.UpsertDuplicateKey, nil
	default:
		return 0, fmt.Errorf("%w: upsert enum", ErrInvalidWire)
	}
}
func perParentName(v engineprofile.PerParentLimitStrategy) string {
	switch v {
	case engineprofile.PerParentLimitUnsupported:
		return "unsupported"
	case engineprofile.PerParentLimitWindow:
		return "window"
	case engineprofile.PerParentLimitLateral:
		return "lateral"
	default:
		return ""
	}
}
func parsePerParent(s string) (engineprofile.PerParentLimitStrategy, error) {
	switch s {
	case "unsupported":
		return engineprofile.PerParentLimitUnsupported, nil
	case "window":
		return engineprofile.PerParentLimitWindow, nil
	case "lateral":
		return engineprofile.PerParentLimitLateral, nil
	default:
		return 0, fmt.Errorf("%w: per-parent enum", ErrInvalidWire)
	}
}
func updateDefaultName(v engineprofile.UpdateDefaultSupport) string {
	if v == engineprofile.UpdateDefaultExpression {
		return "expression"
	}
	return "unsupported"
}
func parseUpdateDefault(s string) (engineprofile.UpdateDefaultSupport, error) {
	switch s {
	case "unsupported":
		return engineprofile.UpdateDefaultUnsupported, nil
	case "expression":
		return engineprofile.UpdateDefaultExpression, nil
	default:
		return 0, fmt.Errorf("%w: update-default enum", ErrInvalidWire)
	}
}

func planWireFromPlan(p Plan, withID bool) (planWire, error) {
	w := planWire{
		Format:  Format,
		Profile: profileToWire(p.profile),
		Baseline: baselineWire{
			Engine:         engineName(p.baseline.catalog.engine),
			ProfileDigest:  digestHex(p.baseline.catalog.profileDigest),
			CatalogDigest:  digestHex(p.baseline.catalog.catalogDigest),
			SourceDigest:   digestHex(p.baseline.catalog.sourceDigest),
			SourceIdentity: p.baseline.sourceIdentity,
		},
		History: historyWire{Schema: p.history.schema, Table: p.history.table},
	}
	w.Baseline.Objects = make([]objectWire, len(p.baseline.objects))
	for i, x := range p.baseline.objects {
		w.Baseline.Objects[i] = objectWire{string(x.id), x.kind, x.schema, x.name, string(x.introducedBy)}
	}
	w.Baseline.Renames = make([]renameWire, len(p.baseline.renames))
	for i, x := range p.baseline.renames {
		w.Baseline.Renames[i] = renameWire{string(x.operation), string(x.object), x.toSchema, x.toName}
	}
	w.Decisions = make([]decisionWire, len(p.decisions))
	for i, x := range p.decisions {
		w.Decisions[i] = decisionWire{string(x.id), string(x.kind), string(x.object), x.from, x.to, x.accepted, x.reason}
	}
	w.Operations = make([]operationWire, len(p.operations))
	for i, x := range p.operations {
		var err error
		w.Operations[i], err = operationToWire(x)
		if err != nil {
			return planWire{}, err
		}
	}
	if withID {
		w.ID = digestHex(Digest(p.id))
	}
	return w, nil
}
func operationToWire(x Operation) (operationWire, error) {
	w := operationWire{ID: string(x.id), Kind: string(x.kind), DependsOn: append([]string{}, stringIDs(x.dependsOn)...), Objects: stringObjects(x.objects), ResultDigest: digestHex(x.resultDigest), Transaction: string(x.transaction), Reversible: x.reversible}
	w.Preconditions = factsToWire(x.preconditions)
	w.Postconditions = factsToWire(x.postconditions)
	var err error
	w.Statements, err = statementsToWire(x.statements)
	if err != nil {
		return operationWire{}, err
	}
	w.ReverseStatements, err = statementsToWire(x.reverseStatements)
	return w, err
}
func stringIDs(in []OperationID) []string {
	out := make([]string, len(in))
	for i, x := range in {
		out[i] = string(x)
	}
	return out
}
func stringObjects(in []ObjectID) []string {
	out := make([]string, len(in))
	for i, x := range in {
		out[i] = string(x)
	}
	return out
}
func factsToWire(in []Fact) []factWire {
	out := make([]factWire, len(in))
	for i, x := range in {
		out[i] = factWire{string(x.object), x.path, x.operator, x.canonicalValue}
	}
	return out
}
func statementsToWire(in []stmt.Statement) ([]statementWire, error) {
	out := make([]statementWire, len(in))
	for i, x := range in {
		args := x.Args()
		out[i] = statementWire{SQL: x.SQL(), Args: make([]argWire, len(args))}
		for j, arg := range args {
			value, err := encodeArg(arg)
			if err != nil {
				return nil, err
			}
			out[i].Args[j] = value
		}
	}
	return out, nil
}

func planDigest(p Plan) (Digest, error) {
	w, err := planWireFromPlan(p, false)
	if err != nil {
		return Digest{}, err
	}
	b, err := marshalNoHTML(w)
	if err != nil {
		return Digest{}, err
	}
	return Digest(sha256Sum(b)), nil
}
func sha256Sum(b []byte) [32]byte { return sha256.Sum256(b) }

func Encode(p Plan) ([]byte, error) {
	if p.id == (PlanID{}) {
		return nil, fmt.Errorf("%w: missing plan ID", ErrInvalidPlan)
	}
	if err := validatePlanParts(p.baseline, p.decisions, p.operations); err != nil {
		return nil, err
	}
	if err := validateFutureObjectIDs(p.baseline, p.operations); err != nil {
		return nil, err
	}
	if err := validatePlanIdentity(p); err != nil {
		return nil, err
	}
	computed, err := planDigest(p)
	if err != nil {
		return nil, err
	}
	if computed != Digest(p.id) {
		return nil, fmt.Errorf("%w: plan ID does not match content", ErrInvalidPlan)
	}
	w, err := planWireFromPlan(p, true)
	if err != nil {
		return nil, err
	}
	b, err := marshalNoHTML(w)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func Decode(data []byte) (Plan, error) {
	if err := validateWireShape(data); err != nil {
		return Plan{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var w planWire
	if err := dec.Decode(&w); err != nil {
		return Plan{}, fmt.Errorf("%w: %v", ErrInvalidWire, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return Plan{}, fmt.Errorf("%w: trailing JSON", ErrInvalidWire)
	}
	if w.Format != Format {
		return Plan{}, fmt.Errorf("%w: format", ErrInvalidWire)
	}
	p, err := planFromWire(w)
	if err != nil {
		return Plan{}, err
	}
	if err := validatePlanIdentity(p); err != nil {
		return Plan{}, err
	}
	if err := validateFutureObjectIDs(p.baseline, p.operations); err != nil {
		return Plan{}, err
	}
	id, err := parseDigest(w.ID)
	if err != nil {
		return Plan{}, err
	}
	p.id = PlanID(id)
	computed, err := planDigest(p)
	if err != nil {
		return Plan{}, err
	}
	if computed != id {
		return Plan{}, fmt.Errorf("%w: plan ID mismatch", ErrInvalidWire)
	}
	return p, nil
}

func validatePlanIdentity(p Plan) error {
	if p.baseline.catalog.engine != p.profile.Engine() {
		return fmt.Errorf("%w: baseline engine does not match profile", ErrInvalidPlan)
	}
	digest, err := profileDigestValue(p.profile)
	if err != nil {
		return err
	}
	if digest != p.baseline.catalog.profileDigest {
		return fmt.Errorf("%w: baseline profile digest does not match profile", ErrInvalidPlan)
	}
	return nil
}

func validateWireShape(data []byte) error {
	var root map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&root); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidWire, err)
	}
	if err := requireKeys(root, "format", "id", "profile", "baseline", "history", "decisions", "operations"); err != nil {
		return err
	}
	for _, key := range []string{"format", "id"} {
		if err := requireString(root, key); err != nil {
			return err
		}
	}
	if err := requireArray(root, "decisions"); err != nil {
		return err
	}
	if err := requireArray(root, "operations"); err != nil {
		return err
	}
	profile, err := objectRaw(root, "profile")
	if err != nil {
		return err
	}
	if err := requireKeys(profile, "engine", "custom_name", "version_known", "version", "max_bind_parameters", "capabilities"); err != nil {
		return err
	}
	for _, key := range []string{"engine", "custom_name"} {
		if err := requireString(profile, key); err != nil {
			return err
		}
	}
	if err := requireBool(profile, "version_known"); err != nil {
		return err
	}
	if err := requireInt(profile, "max_bind_parameters"); err != nil {
		return err
	}
	version, err := objectRaw(profile, "version")
	if err != nil {
		return err
	}
	if err := requireKeys(version, "major", "minor", "patch"); err != nil {
		return err
	}
	for _, key := range []string{"major", "minor", "patch"} {
		if err := requireInt(version, key); err != nil {
			return err
		}
	}
	caps, err := objectRaw(profile, "capabilities")
	if err != nil {
		return err
	}
	capKeys := []string{
		"returning", "upsert", "conflict_target", "default_values", "empty_insert",
		"default_values_upsert", "subquery_limit", "write_subquery_target", "partial_index",
		"aggregate_filter", "qualified_reference", "qualified_index_target", "qualified_index_name",
		"match_operator", "select_for_update", "select_for_share", "select_lock_of",
		"select_lock_no_wait", "select_lock_skip_locked", "upsert_conflict_where",
		"upsert_update_where", "window_functions", "lateral_joins", "savepoints", "transactional_ddl",
		"explicit_null_ordering", "tuple_comparison", "per_parent_limit", "update_default",
	}
	if err := requireKeys(caps, capKeys...); err != nil {
		return err
	}
	for _, key := range []string{"returning", "upsert", "per_parent_limit", "update_default"} {
		if err := requireString(caps, key); err != nil {
			return err
		}
	}
	for _, key := range []string{"conflict_target", "default_values", "empty_insert", "default_values_upsert", "subquery_limit", "write_subquery_target", "partial_index", "aggregate_filter", "qualified_reference", "qualified_index_target", "qualified_index_name", "match_operator", "select_for_update", "select_for_share", "select_lock_of", "select_lock_no_wait", "select_lock_skip_locked", "upsert_conflict_where", "upsert_update_where", "window_functions", "lateral_joins", "savepoints", "transactional_ddl", "explicit_null_ordering", "tuple_comparison"} {
		if err := requireBool(caps, key); err != nil {
			return err
		}
	}
	baseline, err := objectRaw(root, "baseline")
	if err != nil {
		return err
	}
	if err := requireKeys(baseline, "engine", "profile_digest", "catalog_digest", "source_digest", "source_identity", "objects", "renames"); err != nil {
		return err
	}
	for _, key := range []string{"engine", "profile_digest", "catalog_digest", "source_digest", "source_identity"} {
		if err := requireString(baseline, key); err != nil {
			return err
		}
	}
	if err := requireArray(baseline, "objects"); err != nil {
		return err
	}
	if err := requireArray(baseline, "renames"); err != nil {
		return err
	}
	for _, raw := range rawArray(baseline, "objects") {
		object, err := objectRawBytes(raw)
		if err != nil {
			return err
		}
		if err := requireKeys(object, "id", "kind", "schema", "name", "introduced_by"); err != nil {
			return err
		}
		for _, key := range []string{"id", "kind", "schema", "name", "introduced_by"} {
			if err := requireString(object, key); err != nil {
				return err
			}
		}
	}
	for _, raw := range rawArray(baseline, "renames") {
		rename, err := objectRawBytes(raw)
		if err != nil {
			return err
		}
		if err := requireKeys(rename, "operation", "object", "to_schema", "to_name"); err != nil {
			return err
		}
		for _, key := range []string{"operation", "object", "to_schema", "to_name"} {
			if err := requireString(rename, key); err != nil {
				return err
			}
		}
	}
	history, err := objectRaw(root, "history")
	if err != nil {
		return err
	}
	if err := requireKeys(history, "schema", "table"); err != nil {
		return err
	}
	for _, key := range []string{"schema", "table"} {
		if err := requireString(history, key); err != nil {
			return err
		}
	}
	for _, raw := range rawArray(root, "decisions") {
		decision, err := objectRawBytes(raw)
		if err != nil {
			return err
		}
		if err := requireKeys(decision, "id", "kind", "object", "from", "to", "accepted", "reason"); err != nil {
			return err
		}
		for _, key := range []string{"id", "kind", "object", "from", "to", "reason"} {
			if err := requireString(decision, key); err != nil {
				return err
			}
		}
		if err := requireBool(decision, "accepted"); err != nil {
			return err
		}
	}
	for _, raw := range rawArray(root, "operations") {
		operation, err := objectRawBytes(raw)
		if err != nil {
			return err
		}
		if err := requireKeys(operation, "id", "kind", "depends_on", "objects", "preconditions", "postconditions", "result_digest", "statements", "transaction", "reversible", "reverse_statements"); err != nil {
			return err
		}
		for _, key := range []string{"id", "kind", "transaction"} {
			if err := requireString(operation, key); err != nil {
				return err
			}
		}
		if err := requireBool(operation, "reversible"); err != nil {
			return err
		}
		for _, field := range []string{"preconditions", "postconditions", "statements", "reverse_statements"} {
			if err := requireArray(operation, field); err != nil {
				return err
			}
		}
		for _, field := range []string{"depends_on", "objects"} {
			if err := requireStringArray(operation, field); err != nil {
				return err
			}
		}
		for _, field := range []string{"preconditions", "postconditions"} {
			for _, rawFact := range rawArray(operation, field) {
				fact, err := objectRawBytes(rawFact)
				if err != nil {
					return err
				}
				if err := requireKeys(fact, "object", "path", "operator", "value"); err != nil {
					return err
				}
				for _, key := range []string{"object", "path", "operator", "value"} {
					if err := requireString(fact, key); err != nil {
						return err
					}
				}
			}
		}
		for _, field := range []string{"statements", "reverse_statements"} {
			for _, rawStatement := range rawArray(operation, field) {
				statement, err := objectRawBytes(rawStatement)
				if err != nil {
					return err
				}
				if err := requireKeys(statement, "sql", "args"); err != nil {
					return err
				}
				if err := requireString(statement, "sql"); err != nil {
					return err
				}
				if err := requireArray(statement, "args"); err != nil {
					return err
				}
				for _, rawArg := range rawArray(statement, "args") {
					arg, err := objectRawBytes(rawArg)
					if err != nil {
						return err
					}
					if err := requireKeys(arg, "kind", "value"); err != nil {
						return err
					}
					if err := requireString(arg, "kind"); err != nil {
						return err
					}
					if err := requireArgValueType(arg); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}
func requireKeys(object map[string]json.RawMessage, keys ...string) error {
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return fmt.Errorf("%w: missing required wire field %q", ErrInvalidWire, key)
		}
	}
	return nil
}

func requireString(object map[string]json.RawMessage, key string) error {
	var value string
	raw, ok := object[key]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil {
		return fmt.Errorf("%w: %s must be a string", ErrInvalidWire, key)
	}
	return nil
}

func requireBool(object map[string]json.RawMessage, key string) error {
	var value bool
	raw, ok := object[key]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil {
		return fmt.Errorf("%w: %s must be a boolean", ErrInvalidWire, key)
	}
	return nil
}

func requireInt(object map[string]json.RawMessage, key string) error {
	var value int64
	raw, ok := object[key]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil {
		return fmt.Errorf("%w: %s must be an integer", ErrInvalidWire, key)
	}
	return nil
}

func requireArgValueType(arg map[string]json.RawMessage) error {
	var kind string
	if err := json.Unmarshal(arg["kind"], &kind); err != nil {
		return fmt.Errorf("%w: argument kind", ErrInvalidWire)
	}
	raw := bytes.TrimSpace(arg["value"])
	if kind == "null" {
		if !bytes.Equal(raw, []byte("null")) {
			return fmt.Errorf("%w: null argument value", ErrInvalidWire)
		}
		return nil
	}
	if bytes.Equal(raw, []byte("null")) {
		return fmt.Errorf("%w: argument %s cannot be null", ErrInvalidWire, kind)
	}
	switch kind {
	case "bool":
		var value bool
		if json.Unmarshal(raw, &value) != nil {
			return fmt.Errorf("%w: bool argument", ErrInvalidWire)
		}
	case "int64":
		var value int64
		if json.Unmarshal(raw, &value) != nil {
			return fmt.Errorf("%w: int64 argument", ErrInvalidWire)
		}
	case "uint64":
		var value uint64
		if json.Unmarshal(raw, &value) != nil {
			return fmt.Errorf("%w: uint64 argument", ErrInvalidWire)
		}
	case "float64":
		var value float64
		if json.Unmarshal(raw, &value) != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("%w: float64 argument", ErrInvalidWire)
		}
	case "string", "bytes_base64", "time_rfc3339nano":
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return fmt.Errorf("%w: string argument", ErrInvalidWire)
		}
	default:
		return nil
	}
	return nil
}
func requireArray(object map[string]json.RawMessage, key string) error {
	value, ok := object[key]
	if !ok {
		return fmt.Errorf("%w: missing %s", ErrInvalidWire, key)
	}
	if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return fmt.Errorf("%w: null array %s", ErrInvalidWire, key)
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(value, &raw); err != nil {
		return fmt.Errorf("%w: array %s", ErrInvalidWire, key)
	}
	return nil
}
func requireStringArray(object map[string]json.RawMessage, key string) error {
	if err := requireArray(object, key); err != nil {
		return err
	}
	var raw []json.RawMessage
	_ = json.Unmarshal(object[key], &raw)
	for _, element := range raw {
		var value string
		if bytes.Equal(bytes.TrimSpace(element), []byte("null")) || json.Unmarshal(element, &value) != nil {
			return fmt.Errorf("%w: %s element must be a string", ErrInvalidWire, key)
		}
	}
	return nil
}
func objectRaw(object map[string]json.RawMessage, key string) (map[string]json.RawMessage, error) {
	value, ok := object[key]
	if !ok {
		return nil, fmt.Errorf("%w: missing object %s", ErrInvalidWire, key)
	}
	return objectRawBytes(value)
}
func objectRawBytes(value json.RawMessage) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%w: object expected", ErrInvalidWire)
	}
	return object, nil
}
func rawArray(object map[string]json.RawMessage, key string) []json.RawMessage {
	var out []json.RawMessage
	_ = json.Unmarshal(object[key], &out)
	return out
}

func planFromWire(w planWire) (Plan, error) {
	profile, err := profileFromWire(w.Profile)
	if err != nil {
		return Plan{}, err
	}
	pd, err := parseDigest(w.Baseline.ProfileDigest)
	if err != nil {
		return Plan{}, err
	}
	cd, err := parseDigest(w.Baseline.CatalogDigest)
	if err != nil {
		return Plan{}, err
	}
	sd, err := parseDigest(w.Baseline.SourceDigest)
	if err != nil {
		return Plan{}, err
	}
	e, ok := parseEngine(w.Baseline.Engine)
	if !ok || e != profile.Engine() {
		return Plan{}, fmt.Errorf("%w: baseline engine", ErrInvalidWire)
	}
	catalog, _ := NewCatalogIdentity(e, pd, cd, sd)
	objects := make([]BaselineObject, len(w.Baseline.Objects))
	for i, x := range w.Baseline.Objects {
		objects[i], err = newBaselineObject(ObjectID(x.ID), x.Kind, x.Schema, x.Name, OperationID(x.IntroducedBy))
		if err != nil {
			return Plan{}, err
		}
	}
	renames := make([]BaselineRename, len(w.Baseline.Renames))
	for i, x := range w.Baseline.Renames {
		renames[i], err = NewBaselineRename(OperationID(x.Operation), ObjectID(x.Object), x.ToSchema, x.ToName)
		if err != nil {
			return Plan{}, err
		}
	}
	baseline, err := NewBaselineIdentity(catalog, w.Baseline.SourceIdentity, objects, renames)
	if err != nil {
		return Plan{}, err
	}
	history, err := NewHistoryIdentity(w.History.Schema, w.History.Table)
	if err != nil {
		return Plan{}, err
	}
	decisions := make([]Decision, len(w.Decisions))
	for i, x := range w.Decisions {
		decisions[i], err = NewDecision(DecisionID(x.ID), DecisionKind(x.Kind), ObjectID(x.Object), x.From, x.To, x.Accepted, x.Reason)
		if err != nil {
			return Plan{}, err
		}
	}
	operations := make([]Operation, len(w.Operations))
	for i, x := range w.Operations {
		operations[i], err = operationFromWire(x)
		if err != nil {
			return Plan{}, err
		}
	}
	if err := validatePlanParts(baseline, decisions, operations); err != nil {
		return Plan{}, err
	}
	return Plan{profile: profile, baseline: baseline, history: history, decisions: decisions, operations: operations}, nil
}
func operationFromWire(w operationWire) (Operation, error) {
	pre, err := factsFromWire(w.Preconditions)
	if err != nil {
		return Operation{}, err
	}
	post, err := factsFromWire(w.Postconditions)
	if err != nil {
		return Operation{}, err
	}
	resultDigest, err := parseDigest(w.ResultDigest)
	if err != nil {
		return Operation{}, err
	}
	statements, err := statementsFromWire(w.Statements)
	if err != nil {
		return Operation{}, err
	}
	reverse, err := statementsFromWire(w.ReverseStatements)
	if err != nil {
		return Operation{}, err
	}
	deps := make([]OperationID, len(w.DependsOn))
	for i, x := range w.DependsOn {
		deps[i] = OperationID(x)
	}
	objects := make([]ObjectID, len(w.Objects))
	for i, x := range w.Objects {
		objects[i] = ObjectID(x)
	}
	return NewOperation(OperationID(w.ID), OperationKind(w.Kind), deps, objects, pre, post, resultDigest, statements, TransactionMode(w.Transaction), w.Reversible, reverse)
}
func factsFromWire(in []factWire) ([]Fact, error) {
	out := make([]Fact, len(in))
	for i, x := range in {
		if x.Operator == string(FactOperatorEqual) {
			canonical, err := canonicalJSON([]byte(x.Value))
			if err != nil || !bytes.Equal(canonical, []byte(x.Value)) {
				return nil, fmt.Errorf("%w: non-canonical fact value at index %d", ErrInvalidWire, i)
			}
		}
		var err error
		out[i], err = NewFact(ObjectID(x.Object), x.Path, FactOperator(x.Operator), x.Value)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func statementsFromWire(in []statementWire) ([]stmt.Statement, error) {
	out := make([]stmt.Statement, len(in))
	for i, x := range in {
		args := make([]any, len(x.Args))
		for j, a := range x.Args {
			value, err := decodeArg(a)
			if err != nil {
				return nil, err
			}
			args[j] = value
		}
		out[i] = stmt.New(sqltext.Text(x.SQL), args...)
	}
	return out, nil
}
func decodeArg(a argWire) (any, error) {
	if len(a.Value) == 0 {
		return nil, fmt.Errorf("%w: missing argument value", ErrInvalidWire)
	}
	switch a.Kind {
	case "null":
		if string(a.Value) != "null" {
			return nil, fmt.Errorf("%w: null argument", ErrInvalidWire)
		}
		return nil, nil
	case "bool":
		var v bool
		if err := json.Unmarshal(a.Value, &v); err != nil {
			return nil, fmt.Errorf("%w: bool argument", ErrInvalidWire)
		}
		return v, nil
	case "int64":
		var v int64
		if err := json.Unmarshal(a.Value, &v); err != nil {
			return nil, fmt.Errorf("%w: int64 argument", ErrInvalidWire)
		}
		return v, nil
	case "uint64":
		var v uint64
		if err := json.Unmarshal(a.Value, &v); err != nil {
			return nil, fmt.Errorf("%w: uint64 argument", ErrInvalidWire)
		}
		return v, nil
	case "float64":
		var v float64
		if err := json.Unmarshal(a.Value, &v); err != nil || (v != v) || v > 1.7976931348623157e308 || v < -1.7976931348623157e308 {
			return nil, fmt.Errorf("%w: float64 argument", ErrInvalidWire)
		}
		return v, nil
	case "string":
		var v string
		if err := json.Unmarshal(a.Value, &v); err != nil {
			return nil, fmt.Errorf("%w: string argument", ErrInvalidWire)
		}
		return v, nil
	case "bytes_base64":
		var encoded string
		if err := json.Unmarshal(a.Value, &encoded); err != nil {
			return nil, fmt.Errorf("%w: bytes argument", ErrInvalidWire)
		}
		b, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("%w: bytes argument", ErrInvalidWire)
		}
		return b, nil
	case "time_rfc3339nano":
		var v string
		if err := json.Unmarshal(a.Value, &v); err != nil {
			return nil, fmt.Errorf("%w: time argument", ErrInvalidWire)
		}
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return nil, fmt.Errorf("%w: time argument", ErrInvalidWire)
		}
		if t.Format(time.RFC3339Nano) != v {
			return nil, fmt.Errorf("%w: non-canonical time argument", ErrInvalidWire)
		}
		return t, nil
	default:
		return nil, fmt.Errorf("%w: argument kind %q", ErrInvalidWire, a.Kind)
	}
}
