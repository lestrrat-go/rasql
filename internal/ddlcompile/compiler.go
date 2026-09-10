package ddlcompile

import (
	"fmt"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
)

type Compiler struct {
	profile engineprofile.Profile
	dialect dialect.Dialect
}

func New(p engineprofile.Profile) (Compiler, error) {
	if err := engineprofile.Validate(p); err != nil {
		return Compiler{}, err
	}
	var d dialect.Dialect
	switch p.Engine {
	case engineprofile.PostgreSQL:
		d = dialect.PostgreSQL()
	case engineprofile.MySQL:
		d = dialect.MySQL()
	case engineprofile.SQLite:
		d = dialect.SQLite()
	default:
		return Compiler{}, fmt.Errorf("%w: custom profile requires an explicit dialect adapter", engineprofile.ErrUnsupportedFeature)
	}
	return Compiler{profile: p, dialect: engineprofile.ConstrainDialect(p, d)}, nil
}
func NewWithDialect(p engineprofile.Profile, d dialect.Dialect) (Compiler, error) {
	if err := engineprofile.Validate(p); err != nil {
		return Compiler{}, err
	}
	if err := engineprofile.ValidateDialect(d, p); err != nil {
		return Compiler{}, err
	}
	return Compiler{profile: p, dialect: engineprofile.ConstrainDialect(p, d)}, nil
}
func (c Compiler) CreateTable(t schema.TableDef) ([]stmt.Statement, error) {
	if err := engineprofile.Validate(c.profile); err != nil {
		return nil, err
	}
	s, err := render.CreateTable(c.dialect, t)
	if err != nil {
		return nil, err
	}
	return []stmt.Statement{s}, nil
}
func (c Compiler) CreateIndexes(t schema.TableDef) ([]stmt.Statement, error) {
	if err := engineprofile.Validate(c.profile); err != nil {
		return nil, err
	}
	return render.CreateIndexes(c.dialect, t)
}
func (c Compiler) DropTable(n schema.ObjectName) (stmt.Statement, error) {
	if err := engineprofile.Validate(c.profile); err != nil {
		return stmt.Statement{}, err
	}
	if n.Name == "" {
		return stmt.Statement{}, fmt.Errorf("table name must not be blank")
	}
	name, err := c.dialect.QuoteIdentifier(n.Name)
	if err != nil {
		return stmt.Statement{}, err
	}
	if n.Schema != "" {
		schemaName, e := c.dialect.QuoteIdentifier(n.Schema)
		if e != nil {
			return stmt.Statement{}, e
		}
		name = schemaName + "." + name
	}
	return stmt.New(sqltext.Text("DROP TABLE " + name)), nil
}
