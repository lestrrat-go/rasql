package schemasource

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/sourcefile"
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

type defaultFactory struct {
	open   func(string, string) (*sql.DB, error)
	derive func(string, string, string) (string, error)
}

func (f defaultFactory) openDB(driver, dsn string) (*sql.DB, error) {
	if f.open != nil {
		return f.open(driver, dsn)
	}
	return sql.Open(driver, dsn)
}

func (f defaultFactory) deriveDSN(dialect, dsn, name string) (string, error) {
	if f.derive != nil {
		return f.derive(dialect, dsn, name)
	}
	return derivedDSN(dialect, dsn, name)
}

func (f defaultFactory) Create(ctx context.Context, r FactoryRequest) (DisposableDatabase, error) {
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
	base, err := f.openDB(driverName(r.Dialect), r.BootstrapDSN)
	if err != nil {
		return DisposableDatabase{}, err
	}
	defer func() { _ = base.Close() }()
	quoted := quoteDB(r.Dialect, name)
	if _, err = base.ExecContext(ctx, "CREATE DATABASE "+quoted); err != nil {
		return DisposableDatabase{}, err
	}
	dsn, err := f.deriveDSN(r.Dialect, r.BootstrapDSN, name)
	if err != nil {
		return DisposableDatabase{}, cleanupCreatedDatabase(ctx, err, base, r.Dialect, name)
	}
	db, err := f.openDB(driverName(r.Dialect), dsn)
	if err != nil {
		return DisposableDatabase{}, cleanupCreatedDatabase(ctx, err, base, r.Dialect, name)
	}
	return DisposableDatabase{DB: db, DSN: dsn, CloseAndDrop: func(c context.Context) error {
		cerr := db.Close()
		d, oe := sql.Open(driverName(r.Dialect), r.BootstrapDSN)
		if oe != nil {
			return errorsJoin(cerr, oe)
		}
		defer func() { _ = d.Close() }()
		return errorsJoin(cerr, dropDatabase(c, d, r.Dialect, name))
	}}, nil
}

func cleanupCreatedDatabase(ctx context.Context, primary error, base *sql.DB, dialect, name string) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	return errors.Join(primary, dropDatabase(cleanupCtx, base, dialect, name))
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
	if _, e := pgx.ParseConfig(s); e != nil {
		return "", e
	}
	if strings.HasPrefix(s, "postgres://") || strings.HasPrefix(s, "postgresql://") {
		u, e := url.Parse(s)
		if e != nil {
			return "", e
		}
		u.Path = "/" + url.PathEscape(n)
		q := u.Query()
		q.Set("dbname", n)
		u.RawQuery = q.Encode()
		return u.String(), nil
	}
	return replaceKeywordDatabase(s, n), nil
}

func replaceKeywordDatabase(s, name string) string {
	quoted := "'" + strings.ReplaceAll(strings.ReplaceAll(name, `\`, `\\`), "'", `\'`) + "'"
	if strings.TrimSpace(s) == "" {
		return "dbname=" + quoted
	}
	return s + " dbname=" + quoted
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

func dialectFor(e engineprofile.EngineID) dialect.Dialect {
	return map[engineprofile.EngineID]dialect.Dialect{engineprofile.PostgreSQL: dialect.PostgreSQL(), engineprofile.MySQL: dialect.MySQL(), engineprofile.SQLite: dialect.SQLite()}[e]
}

type defaultMigrations struct{}

// Apply applies migrations directly when it is non-empty, which is how Read always calls this
// (migrations loaded through internal/migrationdir carry their own IDs, modes, and reverse
// sources). Materialize's flat-file "migrations" kind has no such structure, so it still
// synthesizes one migration per snapshot from s.
func (defaultMigrations) Apply(ctx context.Context, db *sql.DB, p engineprofile.Profile, s []sourcefile.SourceFileSnapshot, migrations []migrate.Migration) error {
	r, e := migrate.New(db, dialectFor(p.Engine))
	if e != nil {
		return e
	}
	ms := migrations
	if len(ms) == 0 {
		ms = make([]migrate.Migration, len(s))
		for i, x := range s {
			ms[i] = migrate.Migration{ID: fmt.Sprintf("%06d_%s", i, strings.ReplaceAll(filepath.Base(x.Path()), ".", "_")), Statements: []migrate.Statement{{Source: x.Path(), SQL: sqltext.Text(x.Bytes())}}}
		}
	}
	_, e = r.Apply(ctx, migrate.AllPending(), ms...)
	return e
}

// Status reports migrations' state without applying or creating anything beyond what
// migrate.Runner.Status itself creates; see Read's doc comment for the one residual case.
func (defaultMigrations) Status(ctx context.Context, db *sql.DB, p engineprofile.Profile, migrations []migrate.Migration) ([]migrate.StatusEntry, error) {
	r, e := migrate.New(db, dialectFor(p.Engine))
	if e != nil {
		return nil, e
	}
	return r.Status(ctx, migrations...)
}

type defaultProfileDiscoverer struct{}

func (defaultProfileDiscoverer) Discover(ctx context.Context, db *sql.DB, d string) (engineprofile.Profile, error) {
	return engineprofile.DiscoverBuiltin(ctx, db, engineID(d))
}
func errorsJoin(a, b error) error { return errors.Join(a, b) }
