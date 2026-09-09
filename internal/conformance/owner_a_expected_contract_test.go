package conformance

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOwnerASignatureValidationMatrix(t *testing.T) {
	tests := map[string]func(*SignatureDocument){
		"unknown column type":        func(value *SignatureDocument) { value.Schema.Tables[0].Columns[0].Type = "bigint" },
		"unknown null policy":        func(value *SignatureDocument) { value.Schema.Tables[0].Columns[0].NullPolicy = "" },
		"invalid default":            func(value *SignatureDocument) { value.Schema.Tables[2].Columns[4].Default.Value = "true" },
		"required seed null":         func(value *SignatureDocument) { value.Seed.Rows[0].Values[0] = nil },
		"fractional integer":         func(value *SignatureDocument) { value.Seed.Rows[0].Values[0] = 1.5 },
		"missing foreign key target": func(value *SignatureDocument) { value.Seed.Rows[4000].Values[1] = float64(501) },
		"reordered workloads": func(value *SignatureDocument) {
			value.Workloads[0], value.Workloads[1] = value.Workloads[1], value.Workloads[0]
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value, err := LoadPortableSignature()
			require.NoError(t, err)
			mutate(&value)
			require.Error(t, value.Validate())
		})
	}
}

func TestOwnerAPortableExpectedValidationMatrix(t *testing.T) {
	tests := map[string]func(*PortableExpectedDocument){
		"wrong format": func(value *PortableExpectedDocument) { value.Format = "" },
		"uppercase digest": func(value *PortableExpectedDocument) {
			value.PortableSignatureDigest = "A" + value.PortableSignatureDigest[1:]
		},
		"reordered workloads": func(value *PortableExpectedDocument) {
			value.Workloads[0], value.Workloads[1] = value.Workloads[1], value.Workloads[0]
		},
		"wrong result digest":  func(value *PortableExpectedDocument) { value.Workloads[0].ResultSHA256 = value.PortableSignatureDigest },
		"counter mismatch":     func(value *PortableExpectedDocument) { value.Workloads[0].RowsConsumed++ },
		"missing verification": func(value *PortableExpectedDocument) { value.Workloads[0].Verification = nil },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value, err := LoadPortableExpected()
			require.NoError(t, err)
			mutate(&value)
			require.Error(t, value.Validate())
		})
	}
	var entry PortableExpected
	require.Error(t, json.Unmarshal([]byte(`{"workload":"single_row_read","unknown":true}`), &entry))
}

func TestOwnerAEngineExpectedValidationMatrix(t *testing.T) {
	tests := map[string]func(*EngineExpectedDocument){
		"wrong engine":  func(value *EngineExpectedDocument) { value.Engine = "mysql" },
		"wrong profile": func(value *EngineExpectedDocument) { value.Profile = "sqlite-3.34" },
		"uppercase digest": func(value *EngineExpectedDocument) {
			value.PortableSignatureDigest = "A" + value.PortableSignatureDigest[1:]
		},
		"reordered workloads": func(value *EngineExpectedDocument) {
			value.Workloads[0], value.Workloads[1] = value.Workloads[1], value.Workloads[0]
		},
		"wrong statement count": func(value *EngineExpectedDocument) { value.Workloads[0].RASQL.Statements++ },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value, err := LoadEngineExpected("sqlite")
			require.NoError(t, err)
			mutate(&value)
			require.Error(t, value.Validate("sqlite", "sqlite-3.35"))
		})
	}
}

func TestOwnerASignatureValidationImmutability(t *testing.T) {
	value, err := LoadPortableSignature()
	require.NoError(t, err)
	value.Seed.Rows[0].Values[0] = 1.5
	before, err := json.Marshal(value)
	require.NoError(t, err)
	require.Error(t, value.Validate())
	after, err := json.Marshal(value)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestOwnerAExpectedSelectionImmutability(t *testing.T) {
	value, err := LoadPortableExpected()
	require.NoError(t, err)
	before, err := json.Marshal(value)
	require.NoError(t, err)
	_, ok := expectedWorkload(value, "missing")
	require.False(t, ok)
	after, err := json.Marshal(value)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func expectedWorkload(document PortableExpectedDocument, name string) (PortableExpected, bool) {
	for _, workload := range document.Workloads {
		if workload.Workload == name {
			return workload, true
		}
	}
	return PortableExpected{}, false
}

func TestOwnerASeedIdentityGate(t *testing.T) {
	document, err := LoadPortableSignature()
	require.NoError(t, err)
	schemaDigest, err := document.SchemaSHA256()
	require.NoError(t, err)
	seedDigest, err := document.SeedSHA256()
	require.NoError(t, err)
	require.Equal(t, portableSchemaSHA256, schemaDigest)
	require.Equal(t, portableSeedSHA256, seedDigest)
	require.NotEqual(t, schemaDigest, seedDigest)
	signature, err := PortableSignatureForChecked("single_row_read")
	require.NoError(t, err)
	require.Equal(t, schemaDigest, signature.SchemaDigest)
	require.Equal(t, seedDigest, signature.SeedDigest)
}

func TestOwnerAMeasurementPublicationGate(t *testing.T) {
	t.Run("invalid measurement keeps pair atomic", func(t *testing.T) {
		recorder := NewRecorder()
		valid := testMeasurement(t, "sqlite", "sqlite-3.35", "single_row_read", "rasql")
		invalid := valid
		invalid.Implementation = ""
		require.Error(t, addMeasurementPair(recorder, valid, invalid))
		require.Empty(t, recorder.Records())
	})
	t.Run("matching wrong implementations fail independent result contract", func(t *testing.T) {
		recorder := NewRecorder()
		wrong := parityEvidence{
			ResultJSON: []byte(`{"ID":2,"Name":"project-002"}`), Outcome: "wrong", RowsReturned: 1,
			MeasuredRowsConsumed: 1, RowsConsumed: 1,
			Observations: []statementObservation{{
				Role: roleRead, LogicalParent: "single_row_read", StatementIndex: 0, Kind: "query",
				Phase: "consumption", Started: true, Completed: true, CompletionCount: 1, RowsConsumed: 1,
			}},
		}
		signature := requiredWorkloadContracts()[0]
		expected := requiredPortableExpected()[0]
		require.Error(t, validatePortableEvidence(signature, expected, wrong))
		require.Error(t, validatePortableEvidence(signature, expected, wrong))
		require.Empty(t, recorder.Records())
	})
}
