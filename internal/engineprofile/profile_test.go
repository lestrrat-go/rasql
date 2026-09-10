package engineprofile

import (
	"testing"
)

func TestResolveBuiltins(t *testing.T) {
	for _, tc := range []struct {
		id      string
		engine  EngineID
		version Version
		binds   int
	}{
		{"postgresql-17", PostgreSQL, Version{Known: true, Major: 17, Minor: 11}, 65535},
		{"mysql-8.4", MySQL, Version{Known: true, Major: 8, Minor: 4, Patch: 11}, 65535},
		{"sqlite-3.35", SQLite, Version{Known: true, Major: 3, Minor: 45, Patch: 1}, 999},
	} {
		p, err := Resolve(tc.id, ObservedIdentity{Engine: tc.engine, Version: tc.version})
		if err != nil {
			t.Fatal(err)
		}
		if p.Limits.MaxBindParameters != tc.binds {
			t.Fatalf("%s bind limit=%d", tc.id, p.Limits.MaxBindParameters)
		}
	}
}
func TestParseVersion(t *testing.T) {
	if v, err := parseVersion(PostgreSQL, "170006"); err != nil || v.Major != 17 || v.Minor != 6 {
		t.Fatalf("postgres version=%+v err=%v", v, err)
	}
	if v, err := parseVersion(MySQL, "8.4.11-ubuntu"); err != nil || v.Patch != 11 {
		t.Fatalf("mysql version=%+v err=%v", v, err)
	}
	if _, err := parseVersion(SQLite, "3.35"); err == nil {
		t.Fatal("short sqlite version accepted")
	}
	if _, err := parseVersion(PostgreSQL, "655530006"); err == nil {
		t.Fatal("overflowing PostgreSQL version accepted")
	}
}
