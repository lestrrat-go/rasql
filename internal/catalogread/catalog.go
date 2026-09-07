package catalogread

import (
	"context"
	"database/sql"
	"errors"
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

var ErrUnresolvedFact = engineprofile.ErrUnresolvedFact

func Read(ctx context.Context, db DB, p engineprofile.Profile, scope Scope) (Result, error) {
	if err := engineprofile.Validate(p); err != nil {
		return Result{}, err
	}
	if p.Engine == engineprofile.Custom {
		return Result{}, fmt.Errorf("%w: custom catalog inspection is unavailable", engineprofile.ErrUnsupportedFeature)
	}
	if isNil(db) {
		return Result{}, fmt.Errorf("catalog database must not be nil")
	}
	if err := validateScope(scope, p.Engine == engineprofile.SQLite); err != nil {
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
	names, err := catalogNames(ctx, ins, scope)
	if err != nil {
		_ = tx.Rollback()
		return Result{}, err
	}
	selected := selectNames(names, scope, p.Engine == engineprofile.SQLite)
	if len(scope.Include) > 0 {
		seen := make(map[string]bool, len(names))
		for _, n := range names {
			seen[n.Schema+"\x00"+n.Name] = true
		}
		for _, want := range scope.Include {
			key := objectKey(want)
			if want.Schema == "" && p.Engine == engineprofile.SQLite {
				key = "main\x00" + want.Name
			}
			if !seen[key] {
				_ = tx.Rollback()
				cause := &inspect.TableNotFoundError{Table: want.Name, Scope: "the requested catalog scope"}
				return Result{}, errors.Join(engineprofile.ErrUnresolvedFact, cause)
			}
		}
	}
	tables := make([]schema.TableDef, 0, len(selected))
	unresolved := make([]UnresolvedFact, 0)
	for _, n := range selected {
		var t schema.TableDef
		if n.Schema != "" {
			t, err = ins.ObjectIn(ctx, n.Schema, n.Name)
		} else {
			t, err = ins.Object(ctx, n.Name)
		}
		if err != nil {
			if fact, ok := unresolvedFact(n, err); ok {
				if len(scope.Include) > 0 {
					_ = tx.Rollback()
					return Result{}, errors.Join(engineprofile.ErrUnresolvedFact, err)
				}
				unresolved = append(unresolved, fact)
				continue
			}
			_ = tx.Rollback()
			return Result{}, err
		}
		tables = append(tables, t)
	}
	sort.Slice(tables, func(i, j int) bool { return objectKey(tables[i].ObjectName()) < objectKey(tables[j].ObjectName()) })
	if err := tx.Commit(); err != nil {
		return Result{}, err
	}
	sort.Slice(unresolved, func(i, j int) bool {
		ki := objectKey(unresolved[i].Object) + "\x00" + unresolved[i].Path + "\x00" + unresolved[i].Code
		kj := objectKey(unresolved[j].Object) + "\x00" + unresolved[j].Path + "\x00" + unresolved[j].Code
		return ki < kj
	})
	return Result{Tables: tables, Observed: p, Unresolved: unresolved}, nil
}

func catalogNames(ctx context.Context, ins inspect.Inspector, scope Scope) ([]inspect.ObjectName, error) {
	if len(scope.Include) == 0 {
		return ins.ObjectNames(ctx)
	}
	var names []inspect.ObjectName
	seen := make(map[string]bool)
	for _, want := range scope.Include {
		var current []inspect.ObjectName
		var err error
		if want.Schema != "" {
			current, err = ins.ObjectNamesIn(ctx, want.Schema)
		} else {
			current, err = ins.ObjectNames(ctx)
		}
		if err != nil {
			return nil, err
		}
		for _, n := range current {
			key := n.Schema + "\x00" + n.Name
			if !seen[key] {
				seen[key] = true
				names = append(names, n)
			}
		}
	}
	sort.Slice(names, func(i, j int) bool {
		if names[i].Schema != names[j].Schema {
			return names[i].Schema < names[j].Schema
		}
		return names[i].Name < names[j].Name
	})
	return names, nil
}

func unresolvedFact(object inspect.ObjectName, err error) (UnresolvedFact, bool) {
	if errors.Is(err, inspect.ErrTableNotFound) {
		return UnresolvedFact{Object: schema.ObjectName{Schema: object.Schema, Name: object.Name}, Path: "$", Code: "object_missing", Detail: err.Error()}, true
	}
	if errors.Is(err, inspect.ErrIncompleteMetadata) {
		return UnresolvedFact{Object: schema.ObjectName{Schema: object.Schema, Name: object.Name}, Path: "columns", Code: "columns_incomplete", Detail: err.Error()}, true
	}
	return UnresolvedFact{}, false
}
func validateScope(s Scope, normalizeSQLite bool) error {
	seen := map[string]bool{}
	for _, x := range append(append([]schema.ObjectName{}, s.Include...), s.Exclude...) {
		if x.Name == "" {
			return fmt.Errorf("object name must not be blank")
		}
		schemaName := x.Schema
		if normalizeSQLite && schemaName == "" {
			schemaName = "main"
		}
		key := schemaName + "\x00" + x.Name
		if seen[key] {
			return fmt.Errorf("duplicate object %s", objectKey(x))
		}
		seen[key] = true
	}
	return nil
}

func objectKey(x schema.ObjectName) string { return x.Schema + "\x00" + x.Name }
func selectNames(all []inspect.ObjectName, s Scope, normalizeSQLite bool) []inspect.ObjectName {
	wanted := map[string]bool{}
	for _, x := range s.Include {
		schemaName := x.Schema
		if normalizeSQLite && schemaName == "" {
			schemaName = "main"
		}
		wanted[schemaName+"\x00"+x.Name] = true
	}
	excluded := map[string]bool{}
	for _, x := range s.Exclude {
		schemaName := x.Schema
		if normalizeSQLite && schemaName == "" {
			schemaName = "main"
		}
		excluded[schemaName+"\x00"+x.Name] = true
	}
	out := make([]inspect.ObjectName, 0)
	for _, n := range all {
		key := n.Schema + "\x00" + n.Name
		if n.Kind == schema.ObjectView && !s.IncludeViews {
			continue
		}
		historySchema := s.HistoryTable.Schema
		if normalizeSQLite && historySchema == "" {
			historySchema = n.Schema
		}
		if s.HistoryTable.Name != "" && key == historySchema+"\x00"+s.HistoryTable.Name {
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
