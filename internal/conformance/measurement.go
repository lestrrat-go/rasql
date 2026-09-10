package conformance

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
)

const MeasurementSchema = "rasql.d4.measurement.v1"

// ConformanceCommitEnvVar is the environment variable a measurement
// artifact's commit is read from. CI's "check" job sets it from the commit
// GitHub already provides; a local run has to export it itself, since
// CommitFromEnvironment's build-info fallback below never resolves inside a
// `go test` binary (see TestConformanceEnvironmentRequiresObservedBuildData,
// which pins that on this repository's own test binaries).
const ConformanceCommitEnvVar = "RASQL_CONFORMANCE_COMMIT"

//go:embed testdata/measurement.schema.json
var measurementSchemaFile []byte

type Environment struct {
	ServerVersion string
	DriverVersion string
	GoVersion     string
	OS            string
	Architecture  string
	Commit        string
}

type ResultSummary struct {
	RowsReturned int64
	RowsConsumed int64
	Outcome      string
	Committed    bool
	RolledBack   bool
}

type Measurement struct {
	Format                  string `json:"format"`
	SemanticStatus          string `json:"semantic_status"`
	Comparable              bool   `json:"comparable"`
	Implementation          string `json:"implementation"`
	Engine                  string `json:"engine"`
	Profile                 string `json:"profile"`
	ServerVersion           string `json:"server_version"`
	DriverVersion           string `json:"driver_version"`
	GoVersion               string `json:"go_version"`
	GOOS                    string `json:"goos"`
	GOARCH                  string `json:"goarch"`
	Commit                  string `json:"commit"`
	PortableSignatureDigest string `json:"portable_signature_digest"`
	Workload                string `json:"workload"`
	Sample                  int    `json:"sample"`
	SQLDigest               string `json:"sql_digest"`
	RowsReturned            int64  `json:"rows_returned"`
	RowsConsumed            int64  `json:"rows_consumed"`
	Statements              int    `json:"statements"`
	DurationNS              int64  `json:"duration_ns"`
	AllocationsPerOp        uint64 `json:"allocations_per_op"`
	GeneratedFiles          int    `json:"generated_files"`
	GeneratedBytes          int64  `json:"generated_bytes"`
	BuildDurationNS         int64  `json:"build_duration_ns"`
	FailureCode             string `json:"failure_code"`
	FailureReason           string `json:"failure_reason"`
}

func (m Measurement) Validate() error {
	if m.Format != MeasurementSchema {
		return fmt.Errorf("measurement format must be %q", MeasurementSchema)
	}
	for name, value := range map[string]string{
		"implementation": m.Implementation, "engine": m.Engine, "profile": m.Profile,
		"server_version": m.ServerVersion, "driver_version": m.DriverVersion,
		"go_version": m.GoVersion, "goos": m.GOOS, "goarch": m.GOARCH,
		"commit": m.Commit, "portable_signature_digest": m.PortableSignatureDigest,
		"workload": m.Workload, "sql_digest": m.SQLDigest, "semantic_status": m.SemanticStatus,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("measurement: %s is required", name)
		}
	}
	if m.Sample < 0 || m.Statements < 0 || m.RowsReturned < 0 || m.RowsConsumed < 0 ||
		m.DurationNS < 0 || m.GeneratedFiles < 0 || m.GeneratedBytes < 0 || m.BuildDurationNS < 0 {
		return errors.New("measurement: counters must not be negative")
	}
	switch m.SemanticStatus {
	case "pass":
		if m.FailureCode != "" || m.FailureReason != "" {
			return errors.New("measurement: passing result cannot contain failure")
		}
	case "fail", "skip":
		if strings.TrimSpace(m.FailureReason) == "" {
			return fmt.Errorf("measurement: %s result requires failure_reason", m.SemanticStatus)
		}
		if m.Comparable {
			return fmt.Errorf("measurement: %s result cannot be comparable", m.SemanticStatus)
		}
	default:
		return fmt.Errorf("measurement: unknown semantic status %q", m.SemanticStatus)
	}
	return nil
}

type Recorder struct {
	mu      sync.Mutex
	records []Measurement
	dsns    []string
}

var processRecorder = &Recorder{}

// NewRecorder returns the process recorder when an artifact is requested. The
// conformance package has one test per engine, but CI gives every test the
// same output path. Sharing the recorder keeps a later engine from replacing
// earlier evidence when its test opens that path.
func NewRecorder(dsns ...string) *Recorder {
	allDSNs := append([]string(nil), dsns...)
	allDSNs = append(allDSNs, configuredDSNs()...)
	if os.Getenv("RASQL_CONFORMANCE_OUTPUT") == "" {
		return &Recorder{dsns: uniqueStrings(allDSNs)}
	}
	processRecorder.mu.Lock()
	for _, dsn := range allDSNs {
		if dsn != "" && !slicesContains(processRecorder.dsns, dsn) {
			processRecorder.dsns = append(processRecorder.dsns, dsn)
		}
	}
	processRecorder.mu.Unlock()
	return processRecorder
}

func (r *Recorder) Add(m Measurement) error {
	if r == nil {
		return errors.New("conformance recorder is nil")
	}
	r.mu.Lock()
	m.FailureReason = SanitizeFailure(m.FailureReason, r.dsns...)
	r.mu.Unlock()
	if err := m.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	r.records = append(r.records, m)
	r.mu.Unlock()
	return nil
}
func (r *Recorder) Records() []Measurement {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	result := append([]Measurement(nil), r.records...)
	r.mu.Unlock()
	sort.SliceStable(result, func(i, j int) bool {
		left, right := result[i], result[j]
		for _, pair := range [][2]string{{left.Engine, right.Engine}, {left.Profile, right.Profile}, {left.Workload, right.Workload}, {left.Implementation, right.Implementation}} {
			if pair[0] != pair[1] {
				return pair[0] < pair[1]
			}
		}
		return left.Sample < right.Sample
	})
	return result
}
func (r *Recorder) WriteJSON(w io.Writer) error {
	if r == nil {
		return errors.New("conformance recorder is nil")
	}
	records := r.Records()
	r.mu.Lock()
	dsns := append([]string(nil), r.dsns...)
	r.mu.Unlock()
	for index := range records {
		records[index].FailureReason = SanitizeFailure(records[index].FailureReason, dsns...)
	}
	for _, m := range records {
		if err := m.Validate(); err != nil {
			return err
		}
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(records)
}

// ValidateArtifact checks the JSON shape and values described by
// testdata/measurement.schema.json. If engine names are supplied, it also
// requires the exact live workload matrix for those engines.
func ValidateArtifact(data []byte, engines ...string) error {
	schema, err := measurementSchemaContract()
	if err != nil {
		return err
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("measurement artifact must be a JSON array")
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("measurement artifact is not a JSON array: %w", err)
	}
	records := make([]Measurement, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for index, item := range raw {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(item, &fields); err != nil {
			return fmt.Errorf("measurement artifact record %d is not an object: %w", index, err)
		}
		if fields == nil {
			return fmt.Errorf("measurement artifact record %d must be an object", index)
		}
		for key := range fields {
			if _, ok := schema.Items.Properties[key]; !ok {
				return fmt.Errorf("measurement artifact record %d has unknown field %q", index, key)
			}
		}
		for _, key := range schema.Items.Required {
			if _, ok := fields[key]; !ok {
				return fmt.Errorf("measurement artifact record %d is missing field %q", index, key)
			}
		}
		for key, property := range schema.Items.Properties {
			if value, ok := fields[key]; ok {
				if err := validateSchemaValue(index, key, value, property); err != nil {
					return err
				}
			}
		}
		var measurement Measurement
		decoder := json.NewDecoder(strings.NewReader(string(item)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&measurement); err != nil {
			return fmt.Errorf("measurement artifact record %d is invalid: %w", index, err)
		}
		if err := measurement.Validate(); err != nil {
			return fmt.Errorf("measurement artifact record %d: %w", index, err)
		}
		key := measurementKey(measurement)
		if seen[key] {
			return fmt.Errorf("measurement artifact contains duplicate record %q", key)
		}
		seen[key] = true
		records = append(records, measurement)
	}
	if len(engines) == 0 {
		return nil
	}
	return validateMatrix(records, engines, seen)
}

type measurementSchemaProperty struct {
	Type      string   `json:"type"`
	Const     *string  `json:"const"`
	Enum      []string `json:"enum"`
	MinLength *int     `json:"minLength"`
	Pattern   string   `json:"pattern"`
	Minimum   *float64 `json:"minimum"`
}

type measurementSchemaContractDocument struct {
	Type  string `json:"type"`
	Items struct {
		Type                 string                               `json:"type"`
		AdditionalProperties bool                                 `json:"additionalProperties"`
		Required             []string                             `json:"required"`
		Properties           map[string]measurementSchemaProperty `json:"properties"`
	} `json:"items"`
}

func measurementSchemaContract() (measurementSchemaContractDocument, error) {
	var document measurementSchemaContractDocument
	if err := json.Unmarshal(measurementSchemaFile, &document); err != nil {
		return measurementSchemaContractDocument{}, fmt.Errorf("measurement schema is invalid: %w", err)
	}
	if document.Type != "array" || document.Items.Type != "object" || document.Items.AdditionalProperties ||
		len(document.Items.Required) == 0 || len(document.Items.Properties) == 0 {
		return measurementSchemaContractDocument{}, errors.New("measurement schema has invalid root or item constraints")
	}
	for name, property := range document.Items.Properties {
		if property.Type == "" && (len(property.Enum) > 0 || property.Const != nil) {
			property.Type = "string"
			document.Items.Properties[name] = property
		}
	}
	return document, nil
}

func validateSchemaValue(index int, name string, raw json.RawMessage, property measurementSchemaProperty) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("measurement artifact record %d field %q must have type %q", index, name, property.Type)
	}
	switch property.Type {
	case "string":
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("measurement artifact record %d field %q must be a string", index, name)
		}
		if property.MinLength != nil && utf8.RuneCountInString(value) < *property.MinLength {
			return fmt.Errorf("measurement artifact record %d field %q is shorter than the schema minimum", index, name)
		}
		if property.Const != nil && value != *property.Const {
			return fmt.Errorf("measurement artifact record %d field %q does not match the schema constant", index, name)
		}
		if len(property.Enum) > 0 {
			matched := false
			for _, allowed := range property.Enum {
				if value == allowed {
					matched = true
					break
				}
			}
			if !matched {
				return fmt.Errorf("measurement artifact record %d field %q is outside the schema enum", index, name)
			}
		}
		if property.Pattern != "" {
			matched, err := regexp.MatchString(property.Pattern, value)
			if err != nil {
				return fmt.Errorf("measurement schema pattern for %q is invalid: %w", name, err)
			}
			if !matched {
				return fmt.Errorf("measurement artifact record %d field %q does not match the schema pattern", index, name)
			}
		}
	case "boolean":
		var value bool
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("measurement artifact record %d field %q must be a boolean", index, name)
		}
	case "integer":
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return fmt.Errorf("measurement artifact record %d field %q must be an integer", index, name)
		}
		number, ok := value.(json.Number)
		if !ok {
			return fmt.Errorf("measurement artifact record %d field %q must be an integer", index, name)
		}
		parsed, err := strconv.ParseFloat(number.String(), 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || math.Trunc(parsed) != parsed {
			return fmt.Errorf("measurement artifact record %d field %q must be an integer", index, name)
		}
		if property.Minimum != nil && parsed < *property.Minimum {
			return fmt.Errorf("measurement artifact record %d field %q is below the schema minimum", index, name)
		}
	default:
		return fmt.Errorf("measurement schema has unsupported type %q for %q", property.Type, name)
	}
	return nil
}

func measurementKey(m Measurement) string {
	return strings.Join([]string{m.Engine, m.Profile, m.Workload, m.Implementation, fmt.Sprint(m.Sample)}, "\x00")
}

var requiredWorkloads = map[string][]string{
	"single_row_read":        {"database/sql", "rasql"},
	"nullable_join_report":   {"database/sql", "rasql"},
	"typed_sql_report":       {"database/sql", "rasql"},
	"taskboard_graph_page":   {"database/sql", "rasql"},
	"create_patch":           {"database/sql", "rasql"},
	"bulk_write":             {"database/sql", "rasql"},
	"rollback":               {"database/sql", "rasql"},
	"bulk_rollback":          {"database/sql", "rasql"},
	"early_exit":             {"database/sql", "rasql"},
	"cancellation":           {"database/sql", "rasql"},
	"graph_500_parent_limit": {"database/sql", "rasql"},
	"unsupported_version":    {"rasql"},
}

var requiredProfiles = map[string]string{
	"sqlite":     "sqlite-3.35",
	"postgresql": "postgresql-17",
	"mysql":      "mysql-8.4",
}

func validateMatrix(records []Measurement, engines []string, seen map[string]bool) error {
	expected := make(map[string]bool)
	for _, engine := range engines {
		profile, ok := requiredProfiles[engine]
		if !ok {
			return fmt.Errorf("measurement matrix has unknown engine %q", engine)
		}
		for workload, implementations := range requiredWorkloads {
			for _, implementation := range implementations {
				expected[measurementKey(Measurement{Engine: engine, Profile: profile, Workload: workload, Implementation: implementation})] = true
			}
		}
	}
	if len(records) != len(expected) {
		return fmt.Errorf("measurement matrix has %d records, want %d", len(records), len(expected))
	}
	for key := range expected {
		if !seen[key] {
			return fmt.Errorf("measurement matrix is missing %q", key)
		}
	}
	for _, record := range records {
		if !expected[measurementKey(record)] {
			return fmt.Errorf("measurement matrix has unexpected record %q", measurementKey(record))
		}
		if record.SemanticStatus != "pass" || !record.Comparable {
			return fmt.Errorf("measurement matrix record %q is not a comparable pass", measurementKey(record))
		}
	}
	return nil
}

func EnvironmentSnapshot(commit, serverVersion, driverVersion, _ string) Environment {
	return Environment{ServerVersion: serverVersion, DriverVersion: driverVersion, GoVersion: runtime.Version(), OS: runtime.GOOS, Architecture: runtime.GOARCH, Commit: commit}
}
func MeasurementFromEnvironment(env Environment, implementation, engine, profile, workload string, sample int, digest string) Measurement {
	commit, driverVersion := env.Commit, env.DriverVersion
	// Local unit runs do not publish evidence and may be built without VCS or
	// module metadata. Archive-producing runs set RASQL_CONFORMANCE_OUTPUT and
	// therefore keep the required nonempty metadata validation strict.
	if os.Getenv("RASQL_CONFORMANCE_OUTPUT") == "" {
		if commit == "" {
			commit = "development"
		}
		if driverVersion == "" {
			driverVersion = "development"
		}
	}
	return Measurement{Format: MeasurementSchema, Implementation: implementation, Engine: engine, Profile: profile, ServerVersion: env.ServerVersion, DriverVersion: driverVersion, GoVersion: env.GoVersion, GOOS: env.OS, GOARCH: env.Architecture, Commit: commit, Workload: workload, Sample: sample, PortableSignatureDigest: digest, SQLDigest: DigestParts("pending")}
}
func RedactDSN(string) string { return "<redacted>" }

func SanitizeFailure(detail string, dsns ...string) string {
	result := detail
	for _, dsn := range dsns {
		if dsn == "" {
			continue
		}
		result = strings.ReplaceAll(result, dsn, "<redacted>")
		for _, secret := range dsnSecrets(dsn) {
			if secret != "" {
				result = strings.ReplaceAll(result, secret, "<redacted>")
			}
		}
	}
	return regexp.MustCompile(`(?i)(password|passwd|user|username|host)=[^&\s,]+`).ReplaceAllString(result, "$1=<redacted>")
}

func dsnSecrets(dsn string) []string {
	secrets := []string{}
	add := func(value string) {
		if value != "" && !slicesContains(secrets, value) {
			secrets = append(secrets, value)
		}
	}
	if parsed, err := url.Parse(dsn); err == nil {
		if parsed.User != nil {
			add(parsed.User.String())
			add(parsed.User.Username())
			if password, ok := parsed.User.Password(); ok {
				add(password)
			}
		}
		add(parsed.Host)
		add(parsed.Hostname())
		for _, values := range parsed.Query() {
			for _, value := range values {
				add(value)
			}
		}
	}
	if parsed, err := mysql.ParseDSN(dsn); err == nil {
		add(parsed.User)
		add(parsed.Passwd)
		add(parsed.Addr)
		if parsed.Net == "tcp" {
			if host, _, err := net.SplitHostPort(parsed.Addr); err == nil {
				add(host)
			}
		}
		for _, value := range parsed.Params {
			add(value)
		}
	}
	return secrets
}
func DigestSQL(statements ...string) string { return DigestParts(statements...) }
func DigestParts(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		var length [8]byte
		for i := range length {
			length[len(length)-1-i] = byte(len(part) >> (8 * i))
		}
		_, _ = hash.Write(length[:])
		_, _ = io.WriteString(hash, part)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
// CommitFromEnvironment returns the commit a measurement artifact should be
// attributed to, or "" when none is available. It prefers
// ConformanceCommitEnvVar, and falls back to the running binary's own
// build-info VCS stamp -- a fallback that only ever pays off for a binary
// built with `go build`/`go install`, since `go test` does not stamp vcs.*
// settings into the test binaries it produces. A caller that genuinely needs
// a commit, rather than merely wanting one when convenient, must still
// handle "" itself; it is not this function's place to decide whether a
// blank result is fatal for that caller.
func CommitFromEnvironment() string {
	if commit := strings.TrimSpace(os.Getenv(ConformanceCommitEnvVar)); commit != "" {
		return commit
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && strings.TrimSpace(setting.Value) != "" {
				return setting.Value
			}
		}
	}
	return ""
}

// ModuleVersion returns the dependency version embedded in the test binary.
// Archive wrappers provide the resolved module version when Go omits build
// dependency metadata from a test binary.
func ModuleVersion(modulePath string) string {
	environmentName := map[string]string{
		"modernc.org/sqlite":             "RASQL_CONFORMANCE_SQLITE_DRIVER_VERSION",
		"github.com/jackc/pgx/v5":        "RASQL_CONFORMANCE_POSTGRESQL_DRIVER_VERSION",
		"github.com/go-sql-driver/mysql": "RASQL_CONFORMANCE_MYSQL_DRIVER_VERSION",
	}[modulePath]
	info, ok := debug.ReadBuildInfo()
	if ok {
		for _, dependency := range info.Deps {
			if dependency.Path == modulePath && strings.TrimSpace(dependency.Version) != "" {
				return dependency.Version
			}
		}
	}
	if environmentName != "" {
		return strings.TrimSpace(os.Getenv(environmentName))
	}
	return ""
}

func configuredDSNs() []string {
	return []string{os.Getenv("RASQL_TEST_POSTGRES_DSN"), os.Getenv("RASQL_TEST_MYSQL_DSN")}
}

func slicesContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !slicesContains(result, value) {
			result = append(result, value)
		}
	}
	return result
}
