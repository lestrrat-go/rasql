package conformance

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func testMeasurement(t testing.TB, engine, profile, workload, implementation string) Measurement {
	t.Helper()
	digest, err := PortableSignatureDigestChecked()
	require.NoError(t, err)
	m := MeasurementFromEnvironment(EnvironmentSnapshot("commit-123", "server-1", "driver-1", "postgres://user:secret@example/unsafe"), implementation, engine, profile, workload, 0, digest)
	m.SemanticStatus = "pass"
	m.Comparable = true
	m.SQLDigest = DigestSQL(workload)
	return m
}

func TestRecorderRedactsSecretsAndSortsMeasurements(t *testing.T) {
	recorder := NewRecorder()
	first := testMeasurement(t, "sqlite", "sqlite-3.35", "z-workload", "rasql")
	second := testMeasurement(t, "postgresql", "postgresql-17", "a-workload", "database/sql")
	require.NoError(t, recorder.Add(first))
	require.NoError(t, recorder.Add(second))
	var output bytes.Buffer
	require.NoError(t, recorder.WriteJSON(&output))
	for _, secret := range []string{"secret", "user", "example", "SELECT unsafe", "file::memory:"} {
		require.NotContains(t, output.String(), secret)
	}
	var records []map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &records))
	require.Len(t, records, 2)
	require.Equal(t, "postgresql", records[0]["engine"])
	require.Equal(t, "sqlite", records[1]["engine"])
	require.NotContains(t, output.String(), "dsn")

	dsn := "postgres://gold-user:gold-password@db.example.test:5432/app?sslmode=disable&token=gold-query"
	failure := testMeasurement(t, "sqlite", "sqlite-3.35", "failure", "rasql")
	failure.SemanticStatus = "fail"
	failure.Comparable = false
	failure.FailureCode = "connection"
	failure.FailureReason = "connect " + dsn + " user=gold-user password=gold-password host=db.example.test query=gold-query"
	redacted := NewRecorder(dsn)
	require.NoError(t, redacted.Add(failure))
	redactedRecords := redacted.Records()
	require.Len(t, redactedRecords, 1)
	for _, secret := range []string{"gold-user", "gold-password", "db.example.test", "gold-query"} {
		require.NotContains(t, redactedRecords[0].FailureReason, secret)
	}
	output.Reset()
	require.NoError(t, redacted.WriteJSON(&output))
	for _, secret := range []string{"gold-user", "gold-password", "db.example.test", "gold-query"} {
		require.NotContains(t, output.String(), secret)
	}
}

func TestMeasurementRejectsMissingContractFields(t *testing.T) {
	base := testMeasurement(t, "sqlite", "sqlite-3.35", "read", "rasql")
	for name, measurement := range map[string]Measurement{
		"missing environment":     func() Measurement { value := base; value.GoVersion = ""; return value }(),
		"missing digest":          func() Measurement { value := base; value.PortableSignatureDigest = ""; return value }(),
		"missing semantic status": func() Measurement { value := base; value.SemanticStatus = ""; return value }(),
		"failed without reason":   func() Measurement { value := base; value.SemanticStatus = "fail"; return value }(),
		"comparable failure": func() Measurement {
			value := base
			value.SemanticStatus = "fail"
			value.Comparable = true
			value.FailureReason = "failed"
			return value
		}(),
	} {
		t.Run(name, func(t *testing.T) { require.Error(t, measurement.Validate()) })
	}
}

func TestValidateConformanceArtifact(t *testing.T) {
	path := strings.TrimSpace(os.Getenv("RASQL_CONFORMANCE_OUTPUT"))
	if path == "" {
		t.Skip("RASQL_CONFORMANCE_OUTPUT is not set")
	}
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var engines []string
	for _, value := range strings.Split(os.Getenv("RASQL_CONFORMANCE_EXPECTED_ENGINES"), ",") {
		if value = strings.TrimSpace(value); value != "" {
			engines = append(engines, value)
		}
	}
	require.NotEmpty(t, engines)
	require.NoError(t, ValidateArtifact(data, engines...))
}

func TestValidateArtifactUsesMeasurementSchema(t *testing.T) {
	require.ErrorContains(t, ValidateArtifact([]byte("null")), "JSON array")
	data, err := json.Marshal([]Measurement{testMeasurement(t, "sqlite", "sqlite-3.35", "read", "rasql")})
	require.NoError(t, err)
	require.NoError(t, ValidateArtifact(data))

	var fields map[string]any
	require.NoError(t, json.Unmarshal(data[1:len(data)-1], &fields))
	fields["unexpected"] = true
	data, err = json.Marshal([]map[string]any{fields})
	require.NoError(t, err)
	require.ErrorContains(t, ValidateArtifact(data), "unknown field")

	delete(fields, "unexpected")
	fields["duration_ns"] = nil
	data, err = json.Marshal([]map[string]any{fields})
	require.NoError(t, err)
	require.ErrorContains(t, ValidateArtifact(data), "duration_ns")

	fields["duration_ns"] = 0
	fields["semantic_status"] = "unknown"
	data, err = json.Marshal([]map[string]any{fields})
	require.NoError(t, err)
	require.ErrorContains(t, ValidateArtifact(data), "enum")

	fields["semantic_status"] = "pass"
	fields["sql_digest"] = "invalid"
	data, err = json.Marshal([]map[string]any{fields})
	require.NoError(t, err)
	require.ErrorContains(t, ValidateArtifact(data), "pattern")
}

func TestPortableSignatureIsCanonicalAndStrict(t *testing.T) {
	document, err := LoadPortableSignature()
	require.NoError(t, err)
	first, err := document.CanonicalJSON()
	require.NoError(t, err)
	second, err := document.CanonicalJSON()
	require.NoError(t, err)
	require.Equal(t, first, second)
	digest, err := document.Digest()
	require.NoError(t, err)
	require.Len(t, digest, 64)
	require.NotEmpty(t, SchemaSQLDigest())
	require.NotEmpty(t, SeedDigestValue())
}

func TestSanitizeFailureRemovesDSNFormsAndQueryValues(t *testing.T) {
	tests := []struct {
		name   string
		dsn    string
		detail string
		secret []string
	}{
		{
			name:   "postgresql components",
			dsn:    "postgres://alice:secret@example.test:5432/app?sslmode=disable&token=pg-query",
			detail: `user "alice" password "secret" lookup example.test query "pg-query"`,
			secret: []string{"alice", "secret", "example.test", "example.test:5432", "pg-query"},
		},
		{
			name:   "mysql components",
			dsn:    "gold-user:gold-password@tcp(mysql.example.test:3306)/app?parseTime=true&token=mysql-query",
			detail: `user "gold-user" password "gold-password" host "mysql.example.test:3306" lookup mysql.example.test query "mysql-query"`,
			secret: []string{"gold-user", "gold-password", "mysql.example.test", "mysql.example.test:3306", "mysql-query"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			redacted := SanitizeFailure(test.detail, test.dsn)
			for _, value := range test.secret {
				require.NotContains(t, redacted, value)
			}
		})
	}
}

func TestRecorderRedactsDSNComponentsInMemoryAndJSON(t *testing.T) {
	tests := []struct {
		name   string
		dsn    string
		detail string
		secret []string
	}{
		{
			name:   "postgresql",
			dsn:    "postgres://alice:secret@example.test:5432/app?token=pg-query",
			detail: `user "alice" password "secret" host "example.test:5432" query "pg-query"`,
			secret: []string{"alice", "secret", "example.test", "example.test:5432", "pg-query"},
		},
		{
			name:   "mysql",
			dsn:    "gold-user:gold-password@tcp(mysql.example.test:3306)/app?token=mysql-query",
			detail: `user "gold-user" password "gold-password" lookup mysql.example.test query "mysql-query"`,
			secret: []string{"gold-user", "gold-password", "mysql.example.test", "mysql.example.test:3306", "mysql-query"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			measurement := testMeasurement(t, "sqlite", "sqlite-3.35", test.name, "rasql")
			measurement.SemanticStatus = "fail"
			measurement.Comparable = false
			measurement.FailureReason = test.detail
			recorder := NewRecorder(test.dsn)
			require.NoError(t, recorder.Add(measurement))
			for _, secret := range test.secret {
				require.NotContains(t, recorder.Records()[0].FailureReason, secret)
			}
			var output bytes.Buffer
			require.NoError(t, recorder.WriteJSON(&output))
			for _, secret := range test.secret {
				require.NotContains(t, output.String(), secret)
			}
		})
	}
}
