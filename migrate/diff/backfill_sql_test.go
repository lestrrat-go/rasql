package diff

import "testing"

func TestValidateNativeSQLUsesDialectParsers(t *testing.T) {
	for _, test := range []struct {
		dialect string
		source  string
	}{
		{"postgresql", "/* note; */ SELECT 'value;';"},
		{"mysql", "-- note\nSELECT 'value;';"},
		{"sqlite", "SELECT 'value;'; -- note"},
	} {
		if err := validateNativeSQL(test.dialect, test.source); err != nil {
			t.Errorf("%s: valid source rejected: %v", test.dialect, err)
		}
	}
	for _, test := range []struct {
		dialect string
		source  string
	}{
		{"postgresql", "SELECT 1; SELECT 2;"},
		{"mysql", "SELECT 1; SELECT 2;"},
		{"sqlite", "SELECT 1; SELECT 2;"},
		{"postgresql", "/* comment only */"},
		{"mysql", "-- comment only\n"},
		{"sqlite", "/* comment only */"},
		{"postgresql", "this is not SQL"},
		{"mysql", "this is not SQL"},
		{"sqlite", "this is not SQL"},
	} {
		if err := validateNativeSQL(test.dialect, test.source); err == nil {
			t.Errorf("%s: invalid source accepted: %q", test.dialect, test.source)
		}
	}
}
