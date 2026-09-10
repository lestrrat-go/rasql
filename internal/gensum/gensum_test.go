package gensum_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/internal/gensum"
	"github.com/stretchr/testify/require"
)

func exampleFile() gensum.File {
	return gensum.File{
		Dialect:  "postgresql",
		Profile:  "postgresql-17",
		Settings: "sha256:" + repeatHex("a"),
		Migrations: []gensum.Entry{
			{Name: "001_initial", Value: repeatHex("1")},
			{Name: "002_due_dates", Value: repeatHex("2")},
		},
		Queries: []gensum.Entry{
			{Name: "queries/overdue_count.sql", Value: "sha256:" + repeatHex("3")},
		},
		Outputs: []gensum.Entry{
			{Name: "members_gen.go", Value: "sha256:" + repeatHex("4")},
			{Name: "schema_gen.go", Value: "sha256:" + repeatHex("5")},
		},
	}
}

func repeatHex(s string) string {
	out := ""
	for len(out) < 64 {
		out += s
	}
	return out[:64]
}

func TestGensumRoundTrip(t *testing.T) {
	f := exampleFile()
	got, err := gensum.Parse(gensum.Encode(f))
	require.NoError(t, err)
	require.Equal(t, f, got)
}

func TestGensumRoundTripEmptyMigrations(t *testing.T) {
	f := exampleFile()
	f.Migrations = nil
	got, err := gensum.Parse(gensum.Encode(f))
	require.NoError(t, err)
	require.Equal(t, f, got)
}

func TestGensumEncodeSortsWithinGroups(t *testing.T) {
	f := gensum.File{
		Dialect:  "postgresql",
		Profile:  "postgresql-17",
		Settings: "sha256:" + repeatHex("a"),
		Migrations: []gensum.Entry{
			{Name: "002_due_dates", Value: repeatHex("2")},
			{Name: "001_initial", Value: repeatHex("1")},
		},
		Queries: []gensum.Entry{
			{Name: "queries/overdue_count.sql", Value: "sha256:" + repeatHex("3")},
			{Name: "queries/active_count.sql", Value: "sha256:" + repeatHex("6")},
		},
		Outputs: []gensum.Entry{
			{Name: "schema_gen.go", Value: "sha256:" + repeatHex("5")},
			{Name: "members_gen.go", Value: "sha256:" + repeatHex("4")},
		},
	}
	encoded := gensum.Encode(f)
	sorted := exampleFile()
	sorted.Queries = []gensum.Entry{
		{Name: "queries/active_count.sql", Value: "sha256:" + repeatHex("6")},
		{Name: "queries/overdue_count.sql", Value: "sha256:" + repeatHex("3")},
	}
	require.Equal(t, string(gensum.Encode(sorted)), string(encoded))

	decoded, err := gensum.Parse(encoded)
	require.NoError(t, err)
	require.Equal(t, []gensum.Entry{
		{Name: "001_initial", Value: repeatHex("1")},
		{Name: "002_due_dates", Value: repeatHex("2")},
	}, decoded.Migrations)
	require.Equal(t, []gensum.Entry{
		{Name: "members_gen.go", Value: "sha256:" + repeatHex("4")},
		{Name: "schema_gen.go", Value: "sha256:" + repeatHex("5")},
	}, decoded.Outputs)
}

func TestGensumParseRefusesUnknownVersion(t *testing.T) {
	_, err := gensum.Parse([]byte("rasql.sum v2\ndialect postgresql\nprofile postgresql-17\nsettings sha256:" + repeatHex("a") + "\n"))
	require.ErrorContains(t, err, "v2")
}

func TestGensumCompareReportsOneDifferencePerGroup(t *testing.T) {
	recorded := exampleFile()
	current := exampleFile()
	current.Settings = "sha256:" + repeatHex("f")
	current.Migrations = append([]gensum.Entry(nil), recorded.Migrations...)
	current.Migrations[0].Value = repeatHex("9")
	current.Queries = append([]gensum.Entry(nil), recorded.Queries...)
	current.Queries[0].Value = "sha256:" + repeatHex("8")
	current.Outputs = append([]gensum.Entry(nil), recorded.Outputs...)
	current.Outputs[1].Value = "sha256:" + repeatHex("7")

	diffs := gensum.Compare(recorded, current)
	require.Equal(t, []gensum.Difference{
		{Group: "settings"},
		{Group: "migrations", Path: "001_initial"},
		{Group: "queries", Path: "queries/overdue_count.sql"},
		{Group: "outputs", Path: "schema_gen.go"},
	}, diffs)
}

func TestGensumCompareReportsDialectAndProfile(t *testing.T) {
	recorded := exampleFile()
	current := exampleFile()
	current.Dialect = "mysql"
	current.Profile = "mysql-8.4"
	require.Equal(t, []gensum.Difference{{Group: "dialect"}, {Group: "profile"}}, gensum.Compare(recorded, current))
}

func TestGensumCompareReportsNothingWhenEqual(t *testing.T) {
	require.Empty(t, gensum.Compare(exampleFile(), exampleFile()))
}

func TestGensumParseRefusesMalformedRecord(t *testing.T) {
	header := "rasql.sum v1\ndialect postgresql\nprofile postgresql-17\nsettings sha256:" + repeatHex("a") + "\n"
	tests := []struct {
		name string
		body string
	}{
		{"missing field", "migration 001_initial\n"},
		{"extra field", "migration 001_initial " + repeatHex("1") + " extra\n"},
		{"tab", "migration 001_initial\t" + repeatHex("1") + "\n"},
		{"trailing space", "migration 001_initial " + repeatHex("1") + " \n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := gensum.Parse([]byte(header + test.body))
			require.Error(t, err)
		})
	}
}

func TestGensumParseRefusesOutOfOrderGroups(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{
			name: "settings before profile",
			text: "rasql.sum v1\ndialect postgresql\nsettings sha256:" + repeatHex("a") + "\nprofile postgresql-17\n",
		},
		{
			name: "query before migration",
			text: "rasql.sum v1\ndialect postgresql\nprofile postgresql-17\nsettings sha256:" + repeatHex("a") +
				"\nquery queries/overdue_count.sql sha256:" + repeatHex("3") +
				"\nmigration 001_initial " + repeatHex("1") + "\n",
		},
		{
			name: "unsorted entries within group",
			text: "rasql.sum v1\ndialect postgresql\nprofile postgresql-17\nsettings sha256:" + repeatHex("a") +
				"\nmigration 002_due_dates " + repeatHex("2") +
				"\nmigration 001_initial " + repeatHex("1") + "\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := gensum.Parse([]byte(test.text))
			require.Error(t, err)
		})
	}
}

func TestGensumParseRefusesUnrecognizedHeader(t *testing.T) {
	_, err := gensum.Parse([]byte("not-rasql.sum v1\n"))
	require.Error(t, err)
}

func TestGensumParseRefusesEmptyInput(t *testing.T) {
	_, err := gensum.Parse([]byte(""))
	require.Error(t, err)
}
