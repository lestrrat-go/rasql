package engineprofile

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

type Queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}
type ObservedIdentity struct {
	Engine  EngineID
	Raw     string
	Version Version
}

var (
	ErrVersionObservation = errors.New("engine profile: version observation failed")
	ErrVersionParse       = errors.New("engine profile: version parse failed")
	ErrUnknownProfile     = errors.New("engine profile: unknown profile")
	ErrProfileMismatch    = errors.New("engine profile: profile mismatch")
)

type DiscoveryError struct {
	Code            error
	RequestedEngine EngineID
	ProfileID, Raw  string
	Observed        ObservedIdentity
	Detail          string
}

func (e *DiscoveryError) Error() string {
	return fmt.Sprintf("engine profile: %s: %s", e.Code, e.Detail)
}
func (e *DiscoveryError) Unwrap() error { return e.Code }

func Observe(ctx context.Context, q Queryer, engine EngineID) (identity ObservedIdentity, retErr error) {
	if isNil(q) || (engine != PostgreSQL && engine != MySQL && engine != SQLite) {
		return ObservedIdentity{}, &DiscoveryError{Code: ErrVersionObservation, RequestedEngine: engine, Detail: "invalid queryer or engine"}
	}
	sqlText := map[EngineID]string{PostgreSQL: "SHOW server_version_num", MySQL: "SELECT VERSION()", SQLite: "SELECT sqlite_version()"}[engine]
	rows, err := q.QueryContext(ctx, sqlText)
	if err != nil {
		return ObservedIdentity{}, &DiscoveryError{Code: ErrVersionObservation, RequestedEngine: engine, Detail: err.Error()}
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && retErr == nil {
			identity = ObservedIdentity{}
			retErr = &DiscoveryError{Code: ErrVersionObservation, RequestedEngine: engine, Detail: closeErr.Error()}
		}
	}()
	columns, err := rows.Columns()
	if err != nil {
		return ObservedIdentity{}, &DiscoveryError{Code: ErrVersionObservation, RequestedEngine: engine, Detail: err.Error()}
	}
	if len(columns) != 1 || !rows.Next() {
		if err := rows.Err(); err != nil {
			return ObservedIdentity{}, &DiscoveryError{Code: ErrVersionObservation, RequestedEngine: engine, Detail: err.Error()}
		}
		return ObservedIdentity{}, &DiscoveryError{Code: ErrVersionObservation, RequestedEngine: engine, Detail: "expected one row and one column"}
	}
	var value any
	if err := rows.Scan(&value); err != nil {
		return ObservedIdentity{}, &DiscoveryError{Code: ErrVersionObservation, RequestedEngine: engine, Detail: err.Error()}
	}
	if rows.Next() {
		return ObservedIdentity{}, &DiscoveryError{Code: ErrVersionObservation, RequestedEngine: engine, Detail: "expected one row"}
	}
	if err := rows.Err(); err != nil {
		return ObservedIdentity{}, &DiscoveryError{Code: ErrVersionObservation, RequestedEngine: engine, Detail: err.Error()}
	}
	raw := stringValue(value)
	v, err := parseVersion(engine, raw)
	if err != nil {
		return ObservedIdentity{}, &DiscoveryError{Code: ErrVersionParse, RequestedEngine: engine, Raw: raw, Detail: err.Error()}
	}
	return ObservedIdentity{Engine: engine, Raw: raw, Version: v}, nil
}
func Discover(ctx context.Context, q Queryer, engine EngineID, id string) (Profile, error) {
	o, err := Observe(ctx, q, engine)
	if err != nil {
		return Profile{}, err
	}
	p, err := Resolve(id, o)
	if err != nil {
		return Profile{}, err
	}
	return p, nil
}
func parseVersion(engine EngineID, raw string) (Version, error) {
	if engine == PostgreSQL {
		n, e := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
		major, minor := n/10000, n%10000
		if e != nil || n < 100000 || major > 65535 || minor > 65535 {
			return Version{}, fmt.Errorf("invalid server_version_num")
		}
		return Version{Known: true, Major: uint16(major), Minor: uint16(minor)}, nil
	}
	parts := strings.Split(raw, ".")
	if engine == SQLite && len(parts) != 3 {
		return Version{}, fmt.Errorf("SQLite version must have three components")
	}
	if engine == MySQL && len(parts) < 3 {
		return Version{}, fmt.Errorf("MySQL version requires major.minor.patch")
	}
	if engine == MySQL {
		if len(parts) > 3 && !strings.Contains(parts[2], "-") && !strings.Contains(parts[2], "+") {
			return Version{}, fmt.Errorf("MySQL version has extra components")
		}
		parts = parts[:3]
		for i := range parts {
			p := parts[i]
			if i == 2 {
				for j, c := range p {
					if c == '-' || c == '+' {
						p = p[:j]
						break
					}
				}
			}
			if p == "" {
				return Version{}, fmt.Errorf("empty version component")
			}
			n, e := strconv.ParseUint(p, 10, 16)
			if e != nil {
				return Version{}, fmt.Errorf("invalid version component")
			}
			parts[i] = strconv.FormatUint(n, 10)
		}
	}
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("invalid version")
	}
	v := Version{Known: true}
	for i, p := range parts {
		n, e := strconv.ParseUint(p, 10, 16)
		if e != nil {
			return Version{}, fmt.Errorf("invalid version component")
		}
		switch i {
		case 0:
			v.Major = uint16(n)
		case 1:
			v.Minor = uint16(n)
		case 2:
			v.Patch = uint16(n)
		}
	}
	if v.Major == 0 {
		return Version{}, fmt.Errorf("zero major")
	}
	return v, nil
}
func stringValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	}
	return fmt.Sprint(v)
}
func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	}
	return false
}
