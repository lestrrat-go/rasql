package querycompile

import (
	"fmt"
	"strings"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/stmt"
)

type Compiler struct {
	profile engineprofile.Profile
	dialect dialect.Dialect
}

func (c Compiler) EngineProfile() engineprofile.Profile { return c.profile }

func New(p engineprofile.Profile) (Compiler, error) {
	if err := engineprofile.Validate(p); err != nil {
		return Compiler{}, err
	}
	d, err := dialectFor(p)
	if err != nil {
		return Compiler{}, err
	}
	return Compiler{profile: p, dialect: engineprofile.ConstrainDialect(p, d)}, nil
}

// NewWithDialect retains the caller's validated dialect, including optional
// compiler and identifier extensions. Profile validation remains independent
// of the dialect value and is performed before the compiler is returned.
func NewWithDialect(p engineprofile.Profile, d dialect.Dialect) (Compiler, error) {
	if err := engineprofile.Validate(p); err != nil {
		return Compiler{}, err
	}
	if err := engineprofile.ValidateDialect(d, p); err != nil {
		return Compiler{}, err
	}
	return Compiler{profile: p, dialect: engineprofile.ConstrainDialect(p, d)}, nil
}
func (c Compiler) Select(q query.ResultQuery) (stmt.Statement, error) {
	if err := engineprofile.Validate(c.profile); err != nil {
		return stmt.Statement{}, err
	}
	if err := validateResultCapabilities(c.profile, q); err != nil {
		return stmt.Statement{}, err
	}
	s, err := render.Result(c.dialect, q)
	if err != nil {
		return stmt.Statement{}, err
	}
	if len(s.Args()) > c.profile.Limits.MaxBindParameters {
		return stmt.Statement{}, &engineprofile.ProfileError{Code: engineprofile.ErrBindLimit, Engine: c.profile.Engine, Feature: "bind parameters", Detail: fmt.Sprintf("got %d, limit %d", len(s.Args()), c.profile.Limits.MaxBindParameters)}
	}
	return stmt.New(s.Text(), s.Args()...), nil
}
func (c Compiler) Write(q query.WriteStatement) (stmt.Statement, error) {
	if err := engineprofile.Validate(c.profile); err != nil {
		return stmt.Statement{}, err
	}
	if err := validateWriteCapabilities(c.profile, q); err != nil {
		return stmt.Statement{}, err
	}
	s, err := render.Write(c.dialect, q)
	if err != nil {
		return stmt.Statement{}, err
	}
	if len(s.Args()) > c.profile.Limits.MaxBindParameters {
		return stmt.Statement{}, &engineprofile.ProfileError{Code: engineprofile.ErrBindLimit, Engine: c.profile.Engine, Feature: "bind parameters", Detail: fmt.Sprintf("got %d, limit %d", len(s.Args()), c.profile.Limits.MaxBindParameters)}
	}
	return stmt.New(s.Text(), s.Args()...), nil
}
func (c Compiler) Native(s stmt.Statement) (stmt.Statement, error) {
	if err := engineprofile.Validate(c.profile); err != nil {
		return stmt.Statement{}, err
	}
	if strings.TrimSpace(s.SQL()) == "" {
		return stmt.Statement{}, fmt.Errorf("native statement SQL must not be blank")
	}
	if len(s.Args()) > c.profile.Limits.MaxBindParameters {
		return stmt.Statement{}, &engineprofile.ProfileError{Code: engineprofile.ErrBindLimit, Engine: c.profile.Engine, Feature: "bind parameters", Detail: fmt.Sprintf("got %d, limit %d", len(s.Args()), c.profile.Limits.MaxBindParameters)}
	}
	return stmt.New(s.Text(), s.Args()...), nil
}
func dialectFor(p engineprofile.Profile) (dialect.Dialect, error) {
	switch p.Engine {
	case engineprofile.PostgreSQL:
		return dialect.PostgreSQL(), nil
	case engineprofile.MySQL:
		return dialect.MySQL(), nil
	case engineprofile.SQLite:
		return dialect.SQLite(), nil
	default:
		return nil, fmt.Errorf("%w: custom profile %q requires an explicit dialect adapter", engineprofile.ErrUnsupportedFeature, p.CustomName)
	}
}
