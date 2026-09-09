package conformance

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
)

const engineExpectedFormat = "rasql.d4.engine-expected.v1"

//go:embed testdata/sqlite/expected.json testdata/postgresql/expected.json testdata/mysql/expected.json
var engineExpectedFS embed.FS

type EngineExpectedDocument struct {
	Format                  string                   `json:"format"`
	Engine                  string                   `json:"engine"`
	Profile                 string                   `json:"profile"`
	PortableSignatureDigest string                   `json:"portable_signature_digest"`
	Workloads               []EngineWorkloadExpected `json:"workloads"`
}

type EngineWorkloadExpected struct {
	Workload    string                       `json:"workload"`
	RASQL       EngineImplementationExpected `json:"rasql"`
	DatabaseSQL EngineImplementationExpected `json:"database_sql"`
}

type EngineImplementationExpected struct {
	Statements int `json:"statements"`
}

func (w *EngineWorkloadExpected) UnmarshalJSON(data []byte) error {
	type workloadAlias EngineWorkloadExpected
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, name := range []string{"workload", "rasql", "database_sql"} {
		value, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("engine expected workload requires non-null field %q", name)
		}
	}
	if len(fields) != 3 {
		return fmt.Errorf("engine expected workload must contain exactly 3 fields")
	}
	var decoded workloadAlias
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*w = EngineWorkloadExpected(decoded)
	return nil
}

func (e *EngineImplementationExpected) UnmarshalJSON(data []byte) error {
	type implementationAlias EngineImplementationExpected
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	value, ok := fields["statements"]
	if !ok || len(fields) != 1 || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return fmt.Errorf("engine expected implementation requires statements")
	}
	var decoded implementationAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*e = EngineImplementationExpected(decoded)
	return nil
}

func LoadEngineExpected(engine string) (EngineExpectedDocument, error) {
	profile, ok := map[string]string{"sqlite": "sqlite-3.35", "postgresql": "postgresql-17", "mysql": "mysql-8.4"}[engine]
	if !ok {
		return EngineExpectedDocument{}, fmt.Errorf("engine expected: unsupported engine %q", engine)
	}
	data, err := engineExpectedFS.ReadFile("testdata/" + engine + "/expected.json")
	if err != nil {
		return EngineExpectedDocument{}, err
	}
	var document EngineExpectedDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return EngineExpectedDocument{}, fmt.Errorf("engine expected: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return EngineExpectedDocument{}, fmt.Errorf("engine expected: trailing data")
	}
	if err := document.Validate(engine, profile); err != nil {
		return EngineExpectedDocument{}, err
	}
	return document, nil
}

func (d EngineExpectedDocument) Validate(engine, profile string) error {
	if d.Format != engineExpectedFormat || d.Engine != engine || d.Profile != profile {
		return fmt.Errorf("engine expected: identity differs for %s/%s", engine, profile)
	}
	if !lowerSHA256.MatchString(d.PortableSignatureDigest) {
		return fmt.Errorf("engine expected: portable signature digest must be lowercase SHA-256")
	}
	contracts := requiredWorkloadContracts()
	if len(d.Workloads) != len(contracts) {
		return fmt.Errorf("engine expected: workload count is %d, want %d", len(d.Workloads), len(contracts))
	}
	for index, workload := range d.Workloads {
		if workload.Workload != contracts[index].Name {
			return fmt.Errorf("engine expected: workload %d must be %q", index, contracts[index].Name)
		}
		for implementation, statements := range map[string]int{
			"rasql": workload.RASQL.Statements, "database/sql": workload.DatabaseSQL.Statements,
		} {
			if statements < contracts[index].MinStatements || statements > contracts[index].MaxStatements {
				return fmt.Errorf("engine expected: %s/%s statement count %d is invalid", workload.Workload, implementation, statements)
			}
		}
	}
	return nil
}

func (d EngineExpectedDocument) Workload(name string) (EngineWorkloadExpected, bool) {
	for _, workload := range d.Workloads {
		if workload.Workload == name {
			return workload, true
		}
	}
	return EngineWorkloadExpected{}, false
}

func validateEngineEvidence(expected EngineWorkloadExpected, implementation string, evidence parityEvidence) error {
	want := expected.RASQL.Statements
	if implementation == "database/sql" {
		want = expected.DatabaseSQL.Statements
	} else if implementation != "rasql" {
		return fmt.Errorf("engine expected: unknown implementation %q", implementation)
	}
	if got := portableStatementCount(evidence); got != want {
		return fmt.Errorf("engine expected: %s/%s has %d statements, want %d", expected.Workload, implementation, got, want)
	}
	return nil
}
