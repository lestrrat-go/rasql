package schemasource

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/internal/compilerlock"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/lestrrat-go/rasql/sqltext"
	_ "modernc.org/sqlite"
)

type sqlOpener struct{}

func (sqlOpener) Open(d, dsn string) (*sql.DB, error) { return sql.Open(driverName(d), dsn) }
func driverName(d string) string {
	switch strings.ToLower(d) {
	case "postgres", "postgresql":
		return "pgx"
	case "mysql":
		return "mysql"
	case "sqlite", "sqlite3":
		return "sqlite"
	default:
		return d
	}
}

type defaultFactory struct{}

func (defaultFactory) Create(ctx context.Context, r FactoryRequest) (DisposableDatabase, error) {
	if r.Dialect == "sqlite" {
		if r.TempRoot == "" {
			return DisposableDatabase{}, fmt.Errorf("schema source: SQLite temp root is required")
		}
		if err := os.MkdirAll(r.TempRoot, 0700); err != nil {
			return DisposableDatabase{}, err
		}
		f, err := os.CreateTemp(r.TempRoot, "rasql_schema_*.db")
		if err != nil {
			return DisposableDatabase{}, err
		}
		path := f.Name()
		_ = f.Close()
		db, err := sql.Open("sqlite", path)
		if err != nil {
			_ = os.Remove(path)
			return DisposableDatabase{}, err
		}
		return DisposableDatabase{DB: db, DSN: path, CloseAndDrop: func(context.Context) error {
			cerr := db.Close()
			rerr := os.Remove(path)
			return errorsJoin(cerr, rerr)
		}}, nil
	}
	name, err := randomName()
	if err != nil {
		return DisposableDatabase{}, err
	}
	base, err := sql.Open(driverName(r.Dialect), r.BootstrapDSN)
	if err != nil {
		return DisposableDatabase{}, err
	}
	defer base.Close()
	quoted := quoteDB(r.Dialect, name)
	if _, err = base.ExecContext(ctx, "CREATE DATABASE "+quoted); err != nil {
		return DisposableDatabase{}, err
	}
	dsn, err := derivedDSN(r.Dialect, r.BootstrapDSN, name)
	if err != nil {
		_ = dropDatabase(ctx, base, r.Dialect, name)
		return DisposableDatabase{}, err
	}
	db, err := sql.Open(driverName(r.Dialect), dsn)
	if err != nil {
		_ = dropDatabase(ctx, base, r.Dialect, name)
		return DisposableDatabase{}, err
	}
	return DisposableDatabase{DB: db, DSN: dsn, CloseAndDrop: func(c context.Context) error {
		cerr := db.Close()
		d, oe := sql.Open(driverName(r.Dialect), r.BootstrapDSN)
		if oe != nil {
			return errorsJoin(cerr, oe)
		}
		defer d.Close()
		return errorsJoin(cerr, dropDatabase(c, d, r.Dialect, name))
	}}, nil
}
func randomName() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "rasql_schema_" + hex.EncodeToString(b), nil
}
func quoteDB(d, n string) string {
	if d == "mysql" {
		return "`" + n + "`"
	}
	return `"` + n + `"`
}
func dropDatabase(ctx context.Context, db *sql.DB, d, n string) error {
	_, err := db.ExecContext(ctx, "DROP DATABASE "+quoteDB(d, n))
	return err
}
func derivedDSN(d, s, n string) (string, error) {
	if d == "mysql" {
		c, e := mysql.ParseDSN(s)
		if e != nil {
			return "", e
		}
		c.DBName = n
		return c.FormatDSN(), nil
	}
	c, e := pgx.ParseConfig(s)
	if e != nil {
		return "", e
	}
	c.Database = n
	return c.ConnString(), nil
}

type defaultProfiles struct{}

func (defaultProfiles) Resolve(ctx context.Context, db *sql.DB, e EngineConfig) (engineprofile.Profile, error) {
	id := engineID(e.Dialect)
	return engineprofile.Discover(ctx, db, id, e.Profile)
}
func engineID(s string) engineprofile.EngineID {
	switch strings.ToLower(s) {
	case "postgresql", "postgres":
		return engineprofile.PostgreSQL
	case "mysql":
		return engineprofile.MySQL
	default:
		return engineprofile.SQLite
	}
}

type defaultCatalogs struct{}

func (defaultCatalogs) Read(ctx context.Context, db catalogread.DB, p engineprofile.Profile, s catalogread.Scope) (catalogread.Result, error) {
	return catalogread.Read(ctx, db, p, s)
}

type defaultMigrations struct{}

func (defaultMigrations) Apply(ctx context.Context, db *sql.DB, p engineprofile.Profile, s []compilerlock.SourceFileSnapshot) error {
	d := map[engineprofile.EngineID]dialect.Dialect{engineprofile.PostgreSQL: dialect.PostgreSQL(), engineprofile.MySQL: dialect.MySQL(), engineprofile.SQLite: dialect.SQLite()}[p.Engine]
	r, e := migrate.New(db, d)
	if e != nil {
		return e
	}
	ms := make([]migrate.Migration, len(s))
	for i, x := range s {
		ms[i] = migrate.Migration{ID: fmt.Sprintf("%06d_%s", i, strings.ReplaceAll(filepath.Base(x.Path()), ".", "_")), Statements: []migrate.Statement{{Source: x.Path(), SQL: sqltext.Text(x.Bytes())}}}
	}
	_, e = r.Apply(ctx, migrate.AllPending(), ms...)
	return e
}
func errorsJoin(a, b error) error { return errors.Join(a, b) }
