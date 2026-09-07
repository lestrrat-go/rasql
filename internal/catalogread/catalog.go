package catalogread

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"sort"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/schema"
)

type DB interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}
type Scope struct {
	Include, Exclude []schema.ObjectName
	IncludeViews     bool
	HistoryTable     schema.ObjectName
}
type UnresolvedFact struct {
	Object             schema.ObjectName
	Path, Code, Detail string
}
type Result struct {
	Tables     []schema.TableDef
	Observed   engineprofile.Profile
	Unresolved []UnresolvedFact
}

var ErrUnresolvedFact = fmt.Errorf("catalog read: unresolved fact")

func Read(ctx context.Context, db DB, p engineprofile.Profile, scope Scope) (Result, error) {
	if p.ID == "" || p.Limits.MaxBindParameters <= 0 {
		return Result{}, fmt.Errorf("invalid engine profile")
	}
	if isNil(db) {
		return Result{}, fmt.Errorf("catalog database must not be nil")
	}
	if err := validateScope(scope); err != nil {
		return Result{}, err
	}
	opts := &sql.TxOptions{ReadOnly: true}
	if p.Engine != engineprofile.SQLite {
		opts.Isolation = sql.LevelRepeatableRead
	}
	tx, err := db.BeginTx(ctx, opts)
	if err != nil {
		return Result{}, err
	}
	d := profileDialect(p)
	if d == nil {
		_ = tx.Rollback()
		return Result{}, fmt.Errorf("unsupported engine")
	}
	ins, err := inspect.New(tx, d)
	if err != nil {
		_ = tx.Rollback()
		return Result{}, err
	}
	names, err := ins.ObjectNames(ctx)
	if err != nil {
		_ = tx.Rollback()
		return Result{}, err
	}
	selected := selectNames(names, scope)
	if len(scope.Include) > 0 {
		seen := make(map[string]bool, len(selected))
		for _, n := range selected {
			seen[n.Schema+"\x00"+n.Name] = true
		}
		for _, want := range scope.Include {
			if !seen[objectKey(want)] {
				_ = tx.Rollback()
				return Result{}, fmt.Errorf("%w: requested object %s was not found", ErrUnresolvedFact, objectKey(want))
			}
		}
	}
	tables := make([]schema.TableDef, 0, len(selected))
	for _, n := range selected {
		var t schema.TableDef
		if n.Schema != "" {
			t, err = ins.ObjectIn(ctx, n.Schema, n.Name)
		} else {
			t, err = ins.Object(ctx, n.Name)
		}
		if err != nil {
			_ = tx.Rollback()
			return Result{}, err
		}
		tables = append(tables, t)
	}
	sort.Slice(tables, func(i, j int) bool { return objectKey(tables[i].ObjectName()) < objectKey(tables[j].ObjectName()) })
	if err := tx.Commit(); err != nil {
		return Result{}, err
	}
	return Result{Tables: tables, Observed: p}, nil
}
func validateScope(s Scope) error {
	seen := map[schema.ObjectName]bool{}
	for _, x := range append(append([]schema.ObjectName{}, s.Include...), s.Exclude...) {
		if x.Name == "" {
			return fmt.Errorf("object name must not be blank")
		}
		if seen[x] {
			return fmt.Errorf("duplicate object %s", objectKey(x))
		}
		seen[x] = true
	}
	if len(s.Include) > 0 && len(s.Exclude) > 0 {
		return fmt.Errorf("include and exclude cannot both be set")
	}
	return nil
}

func objectKey(x schema.ObjectName) string { return x.Schema + "\x00" + x.Name }
func selectNames(all []inspect.ObjectName, s Scope) []inspect.ObjectName {
	wanted := map[string]bool{}
	for _, x := range s.Include {
		wanted[x.Schema+"\x00"+x.Name] = true
	}
	excluded := map[string]bool{}
	for _, x := range s.Exclude {
		excluded[x.Schema+"\x00"+x.Name] = true
	}
	out := make([]inspect.ObjectName, 0)
	for _, n := range all {
		key := n.Schema + "\x00" + n.Name
		if n.Kind == schema.ObjectView && !s.IncludeViews {
			continue
		}
		if s.HistoryTable.Name != "" && key == s.HistoryTable.Schema+"\x00"+s.HistoryTable.Name {
			continue
		}
		if len(wanted) > 0 && !wanted[key] || excluded[key] {
			continue
		}
		out = append(out, n)
	}
	return out
}
func profileDialect(p engineprofile.Profile) dialect.Dialect {
	switch p.Engine {
	case engineprofile.PostgreSQL:
		return dialect.PostgreSQL()
	case engineprofile.MySQL:
		return dialect.MySQL()
	case engineprofile.SQLite:
		return dialect.SQLite()
	}
	return nil
}
func isNil(v any) bool {
	if v == nil {
		return true
	}
	x := reflect.ValueOf(v)
	switch x.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func:
		return x.IsNil()
	}
	return false
}
