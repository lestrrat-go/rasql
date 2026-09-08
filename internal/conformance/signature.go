package conformance

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"regexp"
	"strings"
	"time"
)

// PortableSignature is the selected workload identity retained for compatibility
// with the small workload helpers. Its digest always comes from the complete
// engine-neutral document, never from rendered SQL.
type PortableSignature struct {
	Name            string
	SchemaDigest    string
	SeedDigest      string
	Operation       string
	ResultContract  string
	Order           string
	TransactionMode string
	Warmup          int
}

type SignatureDocument struct {
	Format    string              `json:"format"`
	Schema    SignatureSchema     `json:"schema"`
	Seed      SignatureSeed       `json:"seed"`
	Workloads []SignatureWorkload `json:"workloads"`
}
type SignatureSchema struct {
	Tables []SignatureTable `json:"tables"`
}
type SignatureTable struct {
	Ordinal     int                   `json:"ordinal"`
	Name        string                `json:"name"`
	Columns     []SignatureColumn     `json:"columns"`
	PrimaryKey  []string              `json:"primary_key"`
	ForeignKeys []SignatureForeignKey `json:"foreign_keys"`
	Indexes     []SignatureIndex      `json:"indexes"`
}
type SignatureColumn struct {
	Ordinal    int              `json:"ordinal"`
	Name       string           `json:"name"`
	Type       string           `json:"type"`
	NullPolicy string           `json:"null_policy"`
	Default    SignatureDefault `json:"default"`
}
type SignatureDefault struct {
	Kind  string `json:"kind"`
	Value any    `json:"value,omitempty"`
}
type SignatureForeignKey struct {
	Columns    []string `json:"columns"`
	References string   `json:"references"`
}
type SignatureIndex struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique"`
}
type SignatureSeed struct {
	Cardinalities []SignatureCardinality `json:"cardinalities"`
	Rows          []SignatureRow         `json:"rows"`
}
type SignatureCardinality struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}
type SignatureRow struct {
	Table  string `json:"table"`
	Values []any  `json:"values"`
}
type SignatureWorkload struct {
	Name            string   `json:"name"`
	Operation       string   `json:"operation"`
	ResultFields    []string `json:"result_fields"`
	StableOrder     []string `json:"stable_order"`
	ExpectedOutcome string   `json:"expected_outcome"`
	TransactionMode string   `json:"transaction_mode"`
	Warmup          int      `json:"warmup"`
	MinStatements   int      `json:"min_statements"`
	MaxStatements   int      `json:"max_statements"`
	MinReturned     int      `json:"min_returned"`
	MaxReturned     int      `json:"max_returned"`
}

//go:embed testdata/portable-signature.json
var signatureFS embed.FS

//go:embed testdata/portable-expected.json
var expectedFS embed.FS

const portableExpectedFormat = "rasql.d4.portable-expected.v1"

const (
	portableSchemaSHA256 = "ceec21e9f0d7aee1331ef91f738d455eda4a298eac0c770c888f92c502197766"
	portableSeedSHA256   = "2ee8f369935907dc5c463a5721cd6d7625eb93686ff9e5243426409ea62dada8"
)

type PortableExpectedDocument struct {
	Format                  string             `json:"format"`
	PortableSignatureDigest string             `json:"portable_signature_digest"`
	Workloads               []PortableExpected `json:"workloads"`
}
type PortableExpected struct {
	Workload                 string          `json:"workload"`
	Outcome                  string          `json:"outcome"`
	ResultSHA256             string          `json:"result_sha256"`
	RowsReturned             int64           `json:"rows_returned"`
	MeasuredRowsConsumed     int64           `json:"measured_rows_consumed"`
	VerificationRowsConsumed int64           `json:"verification_rows_consumed"`
	RowsConsumed             int64           `json:"rows_consumed"`
	Verification             json.RawMessage `json:"verification"`
}

func (p *PortableExpected) UnmarshalJSON(data []byte) error {
	type expectedAlias PortableExpected
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	required := []string{
		"workload", "outcome", "result_sha256", "rows_returned", "measured_rows_consumed",
		"verification_rows_consumed", "rows_consumed", "verification",
	}
	if len(fields) != len(required) {
		return fmt.Errorf("portable expected entry must contain exactly %d fields", len(required))
	}
	for _, name := range required {
		value, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("portable expected entry requires non-null field %q", name)
		}
	}
	var decoded expectedAlias
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*p = PortableExpected(decoded)
	return nil
}

func LoadPortableExpected() (PortableExpectedDocument, error) {
	data, err := expectedFS.ReadFile("testdata/portable-expected.json")
	if err != nil {
		return PortableExpectedDocument{}, err
	}
	var expected PortableExpectedDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&expected); err != nil {
		return PortableExpectedDocument{}, fmt.Errorf("portable expected: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return PortableExpectedDocument{}, fmt.Errorf("portable expected: trailing data")
	}
	if err := expected.Validate(); err != nil {
		return PortableExpectedDocument{}, err
	}
	return expected, nil
}

func (d PortableExpectedDocument) Validate() error {
	if d.Format != portableExpectedFormat {
		return fmt.Errorf("portable expected: format must be %q", portableExpectedFormat)
	}
	if !lowerSHA256.MatchString(d.PortableSignatureDigest) {
		return fmt.Errorf("portable expected: portable signature digest must be lowercase SHA-256")
	}
	contracts := requiredPortableExpected()
	if len(d.Workloads) != len(contracts) {
		return fmt.Errorf("portable expected: workload count is %d, want %d", len(d.Workloads), len(contracts))
	}
	for index, value := range d.Workloads {
		contract := contracts[index]
		if value.Workload != contract.Workload || value.Outcome != contract.Outcome ||
			value.ResultSHA256 != contract.ResultSHA256 || value.RowsReturned != contract.RowsReturned ||
			value.MeasuredRowsConsumed != contract.MeasuredRowsConsumed ||
			value.VerificationRowsConsumed != contract.VerificationRowsConsumed || value.RowsConsumed != contract.RowsConsumed {
			return fmt.Errorf("portable expected: workload %d does not match required contract %q", index, contract.Workload)
		}
		if !lowerSHA256.MatchString(value.ResultSHA256) {
			return fmt.Errorf("portable expected: result digest for %q must be lowercase SHA-256", value.Workload)
		}
		if value.RowsReturned < 0 || value.MeasuredRowsConsumed < 0 || value.VerificationRowsConsumed < 0 || value.RowsConsumed < 0 || value.RowsConsumed != value.MeasuredRowsConsumed+value.VerificationRowsConsumed {
			return fmt.Errorf("portable expected: invalid row counters for %q", value.Workload)
		}
		if len(bytes.TrimSpace(value.Verification)) == 0 || bytes.Equal(bytes.TrimSpace(value.Verification), []byte("null")) {
			return fmt.Errorf("portable expected: verification is required for %q", value.Workload)
		}
		var verification any
		if err := json.Unmarshal(value.Verification, &verification); err != nil {
			return fmt.Errorf("portable expected: invalid verification for %q: %w", value.Workload, err)
		}
	}
	return nil
}

var lowerSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func requiredPortableExpected() []PortableExpected {
	return []PortableExpected{
		{Workload: "single_row_read", Outcome: "one", ResultSHA256: "6ac079119095fd4fcc6a2bfc76df98b36fdfe2b7c821ccb7d79a274f192435e2", RowsReturned: 1, MeasuredRowsConsumed: 1, RowsConsumed: 1},
		{Workload: "nullable_join_report", Outcome: "ordered", ResultSHA256: "7cac9550d47a8678ffc71f15d6c3eeb1c76246796982b866a11e5e76b5ce879c", RowsReturned: 7, MeasuredRowsConsumed: 7, RowsConsumed: 7},
		{Workload: "typed_sql_report", Outcome: "ordered", ResultSHA256: "db611dd796d9899f93b800329ca6acdd174ae9aa29191b0f21274b2831ca88fd", RowsReturned: 2, MeasuredRowsConsumed: 2, RowsConsumed: 2},
		{Workload: "create_patch", Outcome: "committed", ResultSHA256: "471bbd88525dbaf38e29e08e140383e6fa17cb5b079076587ae2fb50045c04ac", RowsReturned: 1, VerificationRowsConsumed: 1, RowsConsumed: 1},
		{Workload: "bulk_write", Outcome: "committed", ResultSHA256: "0604cd3138feed202ef293e062da2f4720f77a05d25ee036a7a01c9cfcdd1f0a", VerificationRowsConsumed: 500, RowsConsumed: 500},
		{Workload: "rollback", Outcome: "rolled_back", ResultSHA256: "5feceb66ffc86f38d952786c6d696c79c2dbc239dd4e91b46729d73a27fb57e9"},
		{Workload: "bulk_rollback", Outcome: "rolled_back", ResultSHA256: "623f2b7b00a64c7f4d805bb97b0488ff6182a8c927b7af5bdaead2cf772e2305", VerificationRowsConsumed: 1, RowsConsumed: 1},
		{Workload: "early_exit", Outcome: "early_close", ResultSHA256: "6b86b273ff34fce19d6b804eff5a3f5747ada4eaa22f1d49c01e52ddb7875b4b", RowsReturned: 1, MeasuredRowsConsumed: 1, VerificationRowsConsumed: 1, RowsConsumed: 2},
		{Workload: "cancellation", Outcome: "canceled", ResultSHA256: "09c709078558111623ed9b85a94173debb3b4c08a49b7b9d728aec7602a6d095", MeasuredRowsConsumed: 3500, VerificationRowsConsumed: 3, RowsConsumed: 3503},
		{Workload: "taskboard_graph_page", Outcome: "50 parents", ResultSHA256: "95313c20eb0ea1290e926aac80bee0fee2a93413c54a868af57d2a7abc65d693", RowsReturned: 50, MeasuredRowsConsumed: 531, RowsConsumed: 531},
		{Workload: "graph_500_parent_limit", Outcome: "500 parents", ResultSHA256: "e4a724ea644396275d85f7e31143ff331be3c75f43c3c34ad938dc81f14a64d0", RowsReturned: 500, MeasuredRowsConsumed: 5272, RowsConsumed: 5272},
		{Workload: "unsupported_version", Outcome: "version_error", ResultSHA256: "0a58cc52fea4bc76f951dabdbf512f95bf014c5f7495fb2c994dc34a24d4c41c"},
	}
}

var supportedSignatureTypes = map[string]struct{}{
	"integer": {}, "varchar(32)": {}, "varchar(64)": {}, "varchar(128)": {},
	"boolean": {}, "date": {}, "timestamp": {},
}

func LoadPortableSignature() (SignatureDocument, error) {
	data, err := signatureFS.ReadFile("testdata/portable-signature.json")
	if err != nil {
		return SignatureDocument{}, err
	}
	if err := validateSignatureJSONShape(data); err != nil {
		return SignatureDocument{}, err
	}
	var document SignatureDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return SignatureDocument{}, fmt.Errorf("portable signature: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return SignatureDocument{}, fmt.Errorf("portable signature: trailing data")
	}
	if err := document.Validate(); err != nil {
		return SignatureDocument{}, err
	}
	return document, nil
}

func validateSignatureJSONShape(data []byte) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("portable signature: %w", err)
	}
	if err := requireJSONFields("signature", root, "format", "schema", "seed", "workloads"); err != nil {
		return err
	}
	var schema map[string]json.RawMessage
	if err := json.Unmarshal(root["schema"], &schema); err != nil {
		return fmt.Errorf("portable signature: schema must be an object")
	}
	if err := requireJSONFields("schema", schema, "tables"); err != nil {
		return err
	}
	var tables []map[string]json.RawMessage
	if err := json.Unmarshal(schema["tables"], &tables); err != nil || tables == nil {
		return fmt.Errorf("portable signature: tables must be an array")
	}
	for tableIndex, table := range tables {
		if err := requireJSONFields(fmt.Sprintf("table %d", tableIndex), table, "ordinal", "name", "columns", "primary_key", "foreign_keys", "indexes"); err != nil {
			return err
		}
		var columns []map[string]json.RawMessage
		if err := json.Unmarshal(table["columns"], &columns); err != nil || columns == nil {
			return fmt.Errorf("portable signature: columns must be an array")
		}
		for columnIndex, column := range columns {
			if err := requireJSONFields(fmt.Sprintf("column %d.%d", tableIndex, columnIndex), column, "ordinal", "name", "type", "null_policy", "default"); err != nil {
				return err
			}
			var policy map[string]json.RawMessage
			if err := json.Unmarshal(column["default"], &policy); err != nil || policy == nil {
				return fmt.Errorf("portable signature: default must be an object")
			}
			var kind string
			if err := json.Unmarshal(policy["kind"], &kind); err != nil {
				return fmt.Errorf("portable signature: default kind is required")
			}
			fields := []string{"kind"}
			if kind == "literal" {
				fields = append(fields, "value")
			}
			if err := requireJSONFields("default", policy, fields...); err != nil {
				return err
			}
		}
		for field, names := range map[string][]string{"foreign_keys": {"columns", "references"}, "indexes": {"name", "columns", "unique"}} {
			var entries []map[string]json.RawMessage
			if err := json.Unmarshal(table[field], &entries); err != nil || entries == nil {
				return fmt.Errorf("portable signature: %s must be an array", field)
			}
			for index, entry := range entries {
				if err := requireJSONFields(fmt.Sprintf("%s %d", field, index), entry, names...); err != nil {
					return err
				}
			}
		}
	}
	var seed map[string]json.RawMessage
	if err := json.Unmarshal(root["seed"], &seed); err != nil || seed == nil {
		return fmt.Errorf("portable signature: seed must be an object")
	}
	if err := requireJSONFields("seed", seed, "cardinalities", "rows"); err != nil {
		return err
	}
	for field, names := range map[string][]string{"cardinalities": {"name", "count"}, "rows": {"table", "values"}} {
		var entries []map[string]json.RawMessage
		if err := json.Unmarshal(seed[field], &entries); err != nil || entries == nil {
			return fmt.Errorf("portable signature: seed %s must be an array", field)
		}
		for index, entry := range entries {
			if err := requireJSONFields(fmt.Sprintf("seed %s %d", field, index), entry, names...); err != nil {
				return err
			}
		}
	}
	var workloads []map[string]json.RawMessage
	if err := json.Unmarshal(root["workloads"], &workloads); err != nil || workloads == nil {
		return fmt.Errorf("portable signature: workloads must be an array")
	}
	for index, workload := range workloads {
		if err := requireJSONFields(fmt.Sprintf("workload %d", index), workload, "name", "operation", "result_fields", "stable_order", "expected_outcome", "transaction_mode", "warmup", "min_statements", "max_statements", "min_returned", "max_returned"); err != nil {
			return err
		}
	}
	return nil
}

func requireJSONFields(owner string, fields map[string]json.RawMessage, names ...string) error {
	if len(fields) != len(names) {
		return fmt.Errorf("portable signature: %s must contain exactly %d fields", owner, len(names))
	}
	for _, name := range names {
		value, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("portable signature: %s requires non-null field %q", owner, name)
		}
	}
	return nil
}
func (s SignatureDocument) Validate() error {
	if strings.TrimSpace(s.Format) == "" {
		return fmt.Errorf("portable signature: format is required")
	}
	if err := validateOrdinals("table", "schema", len(s.Schema.Tables), func(i int) int { return s.Schema.Tables[i].Ordinal }); err != nil {
		return err
	}
	if err := validateNames("table", len(s.Schema.Tables), func(i int) string { return s.Schema.Tables[i].Name }); err != nil {
		return err
	}
	for _, table := range s.Schema.Tables {
		if err := validateOrdinals("column", table.Name, len(table.Columns), func(i int) int { return table.Columns[i].Ordinal }); err != nil {
			return err
		}
		if err := validateNames("column", len(table.Columns), func(i int) string { return table.Columns[i].Name }); err != nil {
			return err
		}
		columns := make(map[string]SignatureColumn, len(table.Columns))
		for _, column := range table.Columns {
			if _, ok := supportedSignatureTypes[column.Type]; !ok {
				return fmt.Errorf("portable signature: unsupported column type %q in %s", column.Type, table.Name)
			}
			if column.NullPolicy != "required" && column.NullPolicy != "nullable" {
				return fmt.Errorf("portable signature: invalid null policy %q in %s.%s", column.NullPolicy, table.Name, column.Name)
			}
			if err := validateSignatureDefault(table.Name, column); err != nil {
				return err
			}
			columns[column.Name] = column
		}
		if len(table.PrimaryKey) == 0 {
			return fmt.Errorf("portable signature: primary key is required in %s", table.Name)
		}
		primaryKeyNames := make(map[string]struct{}, len(table.PrimaryKey))
		for _, key := range table.PrimaryKey {
			if _, ok := columns[key]; !ok {
				return fmt.Errorf("portable signature: primary key %q references missing column in %s", key, table.Name)
			}
			if _, ok := primaryKeyNames[key]; ok {
				return fmt.Errorf("portable signature: duplicate primary key column %q in %s", key, table.Name)
			}
			primaryKeyNames[key] = struct{}{}
		}
		for _, foreignKey := range table.ForeignKeys {
			if len(foreignKey.Columns) == 0 || !strings.Contains(foreignKey.References, "(") {
				return fmt.Errorf("portable signature: malformed foreign key in %s", table.Name)
			}
			for _, column := range foreignKey.Columns {
				if _, ok := columns[column]; !ok {
					return fmt.Errorf("portable signature: foreign key references missing column %q in %s", column, table.Name)
				}
			}
		}
		indexNames := make(map[string]struct{}, len(table.Indexes))
		for _, index := range table.Indexes {
			if strings.TrimSpace(index.Name) == "" {
				return fmt.Errorf("portable signature: index name is required in %s", table.Name)
			}
			if _, ok := indexNames[index.Name]; ok {
				return fmt.Errorf("portable signature: duplicate index %q in %s", index.Name, table.Name)
			}
			indexNames[index.Name] = struct{}{}
			if len(index.Columns) == 0 {
				return fmt.Errorf("portable signature: index %q has no columns", index.Name)
			}
			for _, column := range index.Columns {
				if _, ok := columns[column]; !ok {
					return fmt.Errorf("portable signature: index references missing column %q in %s", column, table.Name)
				}
			}
		}
	}
	tables := make(map[string]SignatureTable, len(s.Schema.Tables))
	for _, table := range s.Schema.Tables {
		tables[table.Name] = table
	}
	for _, table := range s.Schema.Tables {
		for _, key := range table.ForeignKeys {
			reference := strings.TrimSuffix(strings.TrimPrefix(key.References, ""), "")
			open := strings.IndexByte(reference, '(')
			close := strings.LastIndexByte(reference, ')')
			if open <= 0 || close != len(reference)-1 {
				return fmt.Errorf("portable signature: foreign key reference %q is not table(column)", key.References)
			}
			parts := []string{strings.TrimSpace(reference[:open]), strings.TrimSpace(reference[open+1 : close])}
			ref, ok := tables[parts[0]]
			if !ok {
				return fmt.Errorf("portable signature: foreign key references missing table %q", parts[0])
			}
			referencedColumns := strings.Split(parts[1], ",")
			if len(referencedColumns) != len(key.Columns) {
				return fmt.Errorf("portable signature: foreign key %q has mismatched column widths", key.References)
			}
			for index, referenced := range referencedColumns {
				found := false
				for _, column := range ref.Columns {
					if column.Name == strings.TrimSpace(referenced) {
						local := table.Columns[signatureColumnIndex(table, key.Columns[index])]
						if local.Type != column.Type {
							return fmt.Errorf("portable signature: foreign key %q has incompatible column types", key.References)
						}
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("portable signature: foreign key references missing column %q", key.References)
				}
			}
		}
	}
	cardinalityNames := make(map[string]struct{}, len(s.Seed.Cardinalities))
	for _, cardinality := range s.Seed.Cardinalities {
		if cardinality.Count < 0 {
			return fmt.Errorf("portable signature: negative cardinality for %q", cardinality.Name)
		}
		if _, ok := tables[cardinality.Name]; !ok {
			return fmt.Errorf("portable signature: cardinality references missing table %q", cardinality.Name)
		}
		if _, ok := cardinalityNames[cardinality.Name]; ok {
			return fmt.Errorf("portable signature: duplicate cardinality %q", cardinality.Name)
		}
		cardinalityNames[cardinality.Name] = struct{}{}
	}
	if len(cardinalityNames) != len(tables) {
		return fmt.Errorf("portable signature: cardinality coverage is incomplete")
	}
	seedCounts := make(map[string]int, len(tables))
	seedKeys := make(map[string]map[string]struct{}, len(tables))
	for _, row := range s.Seed.Rows {
		table, ok := tables[row.Table]
		if !ok {
			return fmt.Errorf("portable signature: seed row references missing table %q", row.Table)
		}
		if len(row.Values) != len(table.Columns) {
			return fmt.Errorf("portable signature: seed row %q has %d values, want %d", row.Table, len(row.Values), len(table.Columns))
		}
		for index, value := range row.Values {
			column := table.Columns[index]
			if value == nil {
				if column.NullPolicy != "nullable" {
					return fmt.Errorf("portable signature: seed row %q has null required column %q", row.Table, column.Name)
				}
				continue
			}
			if err := validateSignatureValue(column.Type, value); err != nil {
				return fmt.Errorf("portable signature: seed row %q column %q: %w", row.Table, column.Name, err)
			}
		}
		seedCounts[row.Table]++
		keyParts := make([]any, 0, len(table.PrimaryKey))
		for _, key := range table.PrimaryKey {
			columnIndex := 0
			for index, column := range table.Columns {
				if column.Name == key {
					columnIndex = index
					break
				}
			}
			keyParts = append(keyParts, row.Values[columnIndex])
		}
		keyData, err := json.Marshal(keyParts)
		if err != nil {
			return fmt.Errorf("portable signature: seed key %s: %w", row.Table, err)
		}
		if seedKeys[row.Table] == nil {
			seedKeys[row.Table] = make(map[string]struct{})
		}
		if _, ok := seedKeys[row.Table][string(keyData)]; ok {
			return fmt.Errorf("portable signature: duplicate seed primary key in %s", row.Table)
		}
		seedKeys[row.Table][string(keyData)] = struct{}{}
	}
	if err := validateSeedForeignKeys(s.Schema.Tables, s.Seed.Rows); err != nil {
		return err
	}
	for _, cardinality := range s.Seed.Cardinalities {
		if seedCounts[cardinality.Name] != cardinality.Count {
			return fmt.Errorf("portable signature: cardinality for %q is %d, seed has %d rows", cardinality.Name, cardinality.Count, seedCounts[cardinality.Name])
		}
	}
	if err := validateNames("workload", len(s.Workloads), func(i int) string { return s.Workloads[i].Name }); err != nil {
		return err
	}
	contracts := requiredWorkloadContracts()
	if len(s.Workloads) != len(contracts) {
		return fmt.Errorf("portable signature: workload count is %d, want %d", len(s.Workloads), len(contracts))
	}
	for index, workload := range s.Workloads {
		if strings.TrimSpace(workload.Operation) == "" || strings.TrimSpace(workload.ExpectedOutcome) == "" || strings.TrimSpace(workload.TransactionMode) == "" || (workload.Name != "unsupported_version" && (len(workload.ResultFields) == 0 || len(workload.StableOrder) == 0)) {
			return fmt.Errorf("portable signature: incomplete workload %q", workload.Name)
		}
		if workload.TransactionMode != "none" && workload.TransactionMode != "explicit" && workload.TransactionMode != "nested" && workload.TransactionMode != "within" {
			return fmt.Errorf("portable signature: invalid transaction mode %q", workload.TransactionMode)
		}
		if workload.Warmup < 0 || workload.MinStatements < 0 || workload.MaxStatements < workload.MinStatements || workload.MinReturned < 0 || workload.MaxReturned < workload.MinReturned {
			return fmt.Errorf("portable signature: invalid bounds for %q", workload.Name)
		}
		if !reflect.DeepEqual(workload, contracts[index]) {
			return fmt.Errorf("portable signature: workload %d does not match required contract %q", index, contracts[index].Name)
		}
	}
	if digestJSON(s.Schema) != portableSchemaSHA256 {
		return fmt.Errorf("portable signature: schema digest differs")
	}
	if digestJSON(s.Seed) != portableSeedSHA256 {
		return fmt.Errorf("portable signature: seed digest differs")
	}
	return nil
}

func digestJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validateSignatureDefault(table string, column SignatureColumn) error {
	switch column.Default.Kind {
	case "none":
		if column.Default.Value != nil {
			return fmt.Errorf("portable signature: no-default column %s.%s has a value", table, column.Name)
		}
	case "literal":
		if column.Default.Value == nil {
			return fmt.Errorf("portable signature: literal default is missing for %s.%s", table, column.Name)
		}
		if err := validateSignatureValue(column.Type, column.Default.Value); err != nil {
			return fmt.Errorf("portable signature: default for %s.%s: %w", table, column.Name, err)
		}
	default:
		return fmt.Errorf("portable signature: invalid default kind %q in %s.%s", column.Default.Kind, table, column.Name)
	}
	return nil
}

var canonicalDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func validateSignatureValue(columnType string, value any) error {
	switch columnType {
	case "integer":
		number, ok := value.(float64)
		if !ok || math.IsInf(number, 0) || math.IsNaN(number) || number != math.Trunc(number) {
			return fmt.Errorf("value must be an integer")
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("value must be a boolean")
		}
	case "varchar(32)", "varchar(64)", "varchar(128)":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("value must be a string")
		}
		limit := 32
		switch columnType {
		case "varchar(64)":
			limit = 64
		case "varchar(128)":
			limit = 128
		}
		if len([]rune(text)) > limit {
			return fmt.Errorf("value exceeds %s", columnType)
		}
	case "date":
		text, ok := value.(string)
		if !ok || !canonicalDate.MatchString(text) {
			return fmt.Errorf("value must be a canonical date")
		}
		if _, err := time.Parse("2006-01-02", text); err != nil {
			return fmt.Errorf("value must be a valid date")
		}
	case "timestamp":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("value must be a canonical UTC timestamp")
		}
		parsed, err := time.Parse(time.RFC3339, text)
		if err != nil || parsed.Location() != time.UTC || parsed.Format(time.RFC3339) != text {
			return fmt.Errorf("value must be a canonical UTC timestamp")
		}
	default:
		return fmt.Errorf("unsupported type %q", columnType)
	}
	return nil
}

func validateSeedForeignKeys(tables []SignatureTable, rows []SignatureRow) error {
	byTable := make(map[string]SignatureTable, len(tables))
	seedRows := make(map[string][]SignatureRow, len(tables))
	targets := make(map[string]map[string]struct{})
	for _, table := range tables {
		byTable[table.Name] = table
	}
	for _, row := range rows {
		seedRows[row.Table] = append(seedRows[row.Table], row)
	}
	for _, row := range rows {
		table := byTable[row.Table]
		for _, foreignKey := range table.ForeignKeys {
			localValues := make([]any, len(foreignKey.Columns))
			null := false
			for index, name := range foreignKey.Columns {
				localValues[index] = row.Values[signatureColumnIndex(table, name)]
				null = null || localValues[index] == nil
			}
			if null {
				continue
			}
			open := strings.IndexByte(foreignKey.References, '(')
			refTable := byTable[strings.TrimSpace(foreignKey.References[:open])]
			refColumns := strings.Split(strings.TrimSuffix(foreignKey.References[open+1:], ")"), ",")
			targetSet := targets[foreignKey.References]
			if targetSet == nil {
				targetSet = make(map[string]struct{}, len(seedRows[refTable.Name]))
				for _, candidate := range seedRows[refTable.Name] {
					values := make([]any, len(refColumns))
					for index, name := range refColumns {
						values[index] = candidate.Values[signatureColumnIndex(refTable, strings.TrimSpace(name))]
					}
					key, err := json.Marshal(values)
					if err != nil {
						return fmt.Errorf("portable signature: foreign key target %q: %w", foreignKey.References, err)
					}
					targetSet[string(key)] = struct{}{}
				}
				targets[foreignKey.References] = targetSet
			}
			key, err := json.Marshal(localValues)
			if err != nil {
				return fmt.Errorf("portable signature: foreign key value %q: %w", foreignKey.References, err)
			}
			if _, found := targetSet[string(key)]; !found {
				return fmt.Errorf("portable signature: seed row %q has missing foreign key target %q", row.Table, foreignKey.References)
			}
		}
	}
	return nil
}

func signatureColumnIndex(table SignatureTable, name string) int {
	for index, column := range table.Columns {
		if column.Name == name {
			return index
		}
	}
	return -1
}

func requiredWorkloadContracts() []SignatureWorkload {
	return []SignatureWorkload{
		{Name: "single_row_read", Operation: "read", ResultFields: []string{"id", "name"}, StableOrder: []string{"id"}, ExpectedOutcome: "one", TransactionMode: "none", Warmup: 1, MinStatements: 1, MaxStatements: 1, MinReturned: 1, MaxReturned: 1},
		{Name: "nullable_join_report", Operation: "read", ResultFields: []string{"project_id", "project_name", "task_id", "task_title", "assignee_id", "member_name"}, StableOrder: []string{"project_id", "task_id"}, ExpectedOutcome: "ordered", TransactionMode: "none", Warmup: 1, MinStatements: 1, MaxStatements: 1, MinReturned: 7, MaxReturned: 7},
		{Name: "typed_sql_report", Operation: "typed_sql", ResultFields: []string{"id", "project_id", "assignee_id", "title", "is_open", "due_on", "created_at"}, StableOrder: []string{"due_on", "id"}, ExpectedOutcome: "ordered", TransactionMode: "none", Warmup: 1, MinStatements: 1, MaxStatements: 1, MinReturned: 2, MaxReturned: 2},
		{Name: "create_patch", Operation: "write", ResultFields: []string{"id", "project_id", "assignee_id", "title", "is_open", "due_on", "created_at"}, StableOrder: []string{"id"}, ExpectedOutcome: "committed", TransactionMode: "none", Warmup: 1, MinStatements: 2, MaxStatements: 2, MinReturned: 1, MaxReturned: 1},
		{Name: "bulk_write", Operation: "batch", ResultFields: []string{"verified_count"}, StableOrder: []string{"member_id"}, ExpectedOutcome: "committed", TransactionMode: "explicit", Warmup: 1, MinStatements: 1, MaxStatements: 2, MinReturned: 0, MaxReturned: 0},
		{Name: "rollback", Operation: "scope", ResultFields: []string{"survivor_count"}, StableOrder: []string{"member_id"}, ExpectedOutcome: "rolled_back", TransactionMode: "within", Warmup: 1, MinStatements: 1, MaxStatements: 1, MinReturned: 0, MaxReturned: 0},
		{Name: "bulk_rollback", Operation: "batch", ResultFields: []string{"sentinel", "survivors"}, StableOrder: []string{"member_id"}, ExpectedOutcome: "rolled_back", TransactionMode: "nested", Warmup: 1, MinStatements: 6, MaxStatements: 6, MinReturned: 0, MaxReturned: 0},
		{Name: "early_exit", Operation: "read", ResultFields: []string{"task_id"}, StableOrder: []string{"task_id"}, ExpectedOutcome: "early_close", TransactionMode: "none", Warmup: 1, MinStatements: 1, MaxStatements: 1, MinReturned: 1, MaxReturned: 1},
		{Name: "cancellation", Operation: "graph", ResultFields: []string{"stage", "started", "error", "rows_closed", "partial", "recovered_project_id"}, StableOrder: []string{"stage"}, ExpectedOutcome: "canceled", TransactionMode: "none", Warmup: 1, MinStatements: 6, MaxStatements: 6, MinReturned: 0, MaxReturned: 0},
		{Name: "taskboard_graph_page", Operation: "graph", ResultFields: []string{"project.id", "project.name", "task.id", "task.title", "assignee.state", "assignee.id", "assignee.name"}, StableOrder: []string{"project_id", "task_id"}, ExpectedOutcome: "50 parents", TransactionMode: "none", Warmup: 1, MinStatements: 15, MaxStatements: 15, MinReturned: 50, MaxReturned: 50},
		{Name: "graph_500_parent_limit", Operation: "graph", ResultFields: []string{"project.id", "project.name", "task.id", "task.title", "assignee.state", "assignee.id", "assignee.name"}, StableOrder: []string{"project_id", "task_id"}, ExpectedOutcome: "500 parents", TransactionMode: "none", Warmup: 1, MinStatements: 3, MaxStatements: 5, MinReturned: 500, MaxReturned: 500},
		{Name: "unsupported_version", Operation: "profile", ResultFields: []string{}, StableOrder: []string{}, ExpectedOutcome: "version_error", TransactionMode: "none"},
	}
}

func validateNames(kind string, count int, name func(int) string) error {
	seen := make(map[string]struct{}, count)
	for i := 0; i < count; i++ {
		value := strings.TrimSpace(name(i))
		if value == "" {
			return fmt.Errorf("portable signature: %s name is required", kind)
		}
		if _, ok := seen[value]; ok {
			return fmt.Errorf("portable signature: duplicate %s %q", kind, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}
func validateOrdinals(kind, owner string, count int, ordinal func(int) int) error {
	for i := 0; i < count; i++ {
		if ordinal(i) != i {
			return fmt.Errorf("portable signature: %s ordinal %d in %s is not contiguous", kind, ordinal(i), owner)
		}
	}
	return nil
}
func (s SignatureDocument) CanonicalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(s)
}
func (s SignatureDocument) Digest() (string, error) {
	data, err := s.CanonicalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
func (s SignatureDocument) SchemaSHA256() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	return digestJSON(s.Schema), nil
}
func (s SignatureDocument) SeedSHA256() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	return digestJSON(s.Seed), nil
}
func (s SignatureDocument) Workload(name string) (SignatureWorkload, bool) {
	for _, w := range s.Workloads {
		if w.Name == name {
			return w, true
		}
	}
	return SignatureWorkload{}, false
}
func (s PortableSignature) Validate() error {
	for name, value := range map[string]string{"name": s.Name, "schema_digest": s.SchemaDigest, "seed_digest": s.SeedDigest, "operation": s.Operation, "result_contract": s.ResultContract, "order": s.Order, "transaction_mode": s.TransactionMode} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("conformance signature: %s is required", name)
		}
	}
	if s.Warmup < 0 {
		return fmt.Errorf("conformance signature: warmup must not be negative")
	}
	return nil
}
func PortableSignatureForChecked(workload string) (PortableSignature, error) {
	document, err := LoadPortableSignature()
	if err != nil {
		return PortableSignature{}, err
	}
	selected, ok := document.Workload(workload)
	if !ok {
		return PortableSignature{}, fmt.Errorf("portable signature: unknown workload %q", workload)
	}
	schemaDigest, err := document.SchemaSHA256()
	if err != nil {
		return PortableSignature{}, err
	}
	seedDigest, err := document.SeedSHA256()
	if err != nil {
		return PortableSignature{}, err
	}
	return PortableSignature{Name: selected.Name, SchemaDigest: schemaDigest, SeedDigest: seedDigest, Operation: selected.Operation, ResultContract: strings.Join(selected.ResultFields, ","), Order: strings.Join(selected.StableOrder, ","), TransactionMode: selected.TransactionMode, Warmup: selected.Warmup}, nil
}

func PortableSignatureDigestChecked() (string, error) {
	document, err := LoadPortableSignature()
	if err != nil {
		return "", err
	}
	return document.Digest()
}
