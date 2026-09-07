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
	if p.ID == "" || p.Limits.MaxBindParameters <= 0 {
		return Compiler{}, fmt.Errorf("%w: invalid profile", engineprofile.ErrInvalidProfile)
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
		return Compiler{}, fmt.Errorf("custom dialect unavailable")
	}
	return Compiler{profile: p, dialect: d}, nil
}
func NewWithDialect(p engineprofile.Profile, d dialect.Dialect) (Compiler, error) {
	if p.ID == "" || p.Limits.MaxBindParameters <= 0 {
		return Compiler{}, fmt.Errorf("%w: invalid profile", engineprofile.ErrInvalidProfile)
	}
	if d == nil {
		return Compiler{}, fmt.Errorf("%w: dialect must not be nil", engineprofile.ErrInvalidProfile)
	}
	if p.Engine == engineprofile.Custom && p.CustomName != d.Name() {
		return Compiler{}, fmt.Errorf("dialect and custom profile disagree")
	}
	return Compiler{profile: p, dialect: d}, nil
}
func (c Compiler) CreateTable(t schema.TableDef) ([]stmt.Statement, error) {
	s, err := render.CreateTable(c.dialect, t)
	if err != nil {
		return nil, err
	}
	return []stmt.Statement{s}, nil
}
func (c Compiler) CreateIndexes(t schema.TableDef) ([]stmt.Statement, error) {
	return render.CreateIndexes(c.dialect, t)
}
func (c Compiler) DropTable(n schema.ObjectName) (stmt.Statement, error) {
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
