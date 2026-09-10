package conformance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
)

type statementRole string

const (
	roleRead              statementRole = "read"
	roleReport            statementRole = "report"
	roleRoot              statementRole = "root"
	roleTasks             statementRole = "tasks"
	roleAssignees         statementRole = "assignees"
	roleMutation          statementRole = "mutation"
	roleSavepointBegin    statementRole = "savepoint_begin"
	roleSavepointRollback statementRole = "savepoint_rollback"
	roleSavepointRelease  statementRole = "savepoint_release"
	roleSentinel          statementRole = "sentinel"
	roleVerification      statementRole = "verification"
)

type statementMetadata struct {
	Role           statementRole
	LogicalParent  string
	StatementIndex int
	Verification   bool
	Scope          bool
}

type statementObservation struct {
	Role              statementRole
	LogicalParent     string
	StatementIndex    int
	Verification      bool
	SQL               string
	Descriptor        sqlDescriptor
	Args              []any
	Kind              string
	Phase             string
	Started           bool
	Completed         bool
	CompletionCount   int
	RowsConsumed      int64
	RowsAffected      int64
	RowsAffectedValid bool
	RowValues         [][]any
	EarlyClose        bool
	Err               error
	RowsAffectedErr   error
	ScanErr           error
	RowsErr           error
	CloseErr          error
}

type handwrittenExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type handwrittenQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type handwrittenObserver struct {
	mu           sync.Mutex
	records      []statementObservation
	nextByParent map[string]int
}

func newHandwrittenObserver() *handwrittenObserver {
	return &handwrittenObserver{nextByParent: make(map[string]int)}
}

func measuredStatement(role statementRole, parent string, index int) (statementMetadata, error) {
	if !validMeasuredRole(role) {
		return statementMetadata{}, fmt.Errorf("handwritten statement: invalid role %q", role)
	}
	if strings.TrimSpace(parent) == "" {
		return statementMetadata{}, errors.New("handwritten statement: logical parent is required")
	}
	if index < 0 {
		return statementMetadata{}, errors.New("handwritten statement: statement index must not be negative")
	}
	return statementMetadata{Role: role, LogicalParent: parent, StatementIndex: index}, nil
}

func verificationStatement(parent string, index int) (statementMetadata, error) {
	if strings.TrimSpace(parent) == "" {
		return statementMetadata{}, errors.New("handwritten statement: logical parent is required")
	}
	if index < 0 {
		return statementMetadata{}, errors.New("handwritten statement: statement index must not be negative")
	}
	return statementMetadata{Role: roleVerification, LogicalParent: parent, StatementIndex: index, Verification: true}, nil
}

func scopeStatement(role statementRole, parent string) (statementMetadata, error) {
	if !isScopeRole(role) {
		return statementMetadata{}, fmt.Errorf("handwritten scope statement: invalid role %q", role)
	}
	if strings.TrimSpace(parent) == "" {
		return statementMetadata{}, errors.New("handwritten scope statement: logical parent is required")
	}
	return statementMetadata{Role: role, LogicalParent: parent, StatementIndex: 0, Scope: true}, nil
}

func validMeasuredRole(role statementRole) bool {
	switch role {
	case roleRead, roleReport, roleRoot, roleTasks, roleAssignees, roleMutation,
		roleSavepointBegin, roleSavepointRollback, roleSavepointRelease, roleSentinel:
		return true
	default:
		return false
	}
}

func isScopeRole(role statementRole) bool {
	return role == roleSavepointBegin || role == roleSavepointRollback || role == roleSavepointRelease
}

func cloneHandwrittenValue(value any) (any, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case []byte:
		return append([]byte(nil), typed...), nil
	case sql.RawBytes:
		return sql.RawBytes(append([]byte(nil), typed...)), nil
	case sql.NamedArg:
		cloned, err := cloneHandwrittenValue(typed.Value)
		if err != nil {
			return nil, err
		}
		typed.Value = cloned
		return typed, nil
	default:
		return value, nil
	}
}

func cloneHandwrittenValues(values []any) ([]any, error) {
	result := make([]any, len(values))
	for index, value := range values {
		cloned, err := cloneHandwrittenValue(value)
		if err != nil {
			return nil, err
		}
		result[index] = cloned
	}
	return result, nil
}

func (o *handwrittenObserver) begin(metadata statementMetadata, kind, sqlStatement string, args []any) (int, error) {
	if o == nil {
		return 0, errors.New("handwritten observer is nil")
	}
	if strings.TrimSpace(metadata.LogicalParent) == "" || metadata.StatementIndex < 0 {
		return 0, errors.New("handwritten statement metadata is invalid")
	}
	if metadata.Scope && !isScopeRole(metadata.Role) {
		return 0, errors.New("handwritten scope statement metadata has a non-scope role")
	}
	if metadata.Verification {
		if metadata.Role != roleVerification {
			return 0, errors.New("handwritten verification metadata has a non-verification role")
		}
	} else if !validMeasuredRole(metadata.Role) {
		return 0, fmt.Errorf("handwritten statement: invalid role %q", metadata.Role)
	}
	ownedArgs, err := cloneHandwrittenValues(args)
	if err != nil {
		return 0, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !metadata.Scope {
		next := o.nextByParent[metadata.LogicalParent]
		if metadata.StatementIndex != next {
			return 0, fmt.Errorf("handwritten statement %s index %d, want %d", metadata.LogicalParent, metadata.StatementIndex, next)
		}
		o.nextByParent[metadata.LogicalParent] = next + 1
	}
	o.records = append(o.records, statementObservation{
		Role: metadata.Role, LogicalParent: metadata.LogicalParent, StatementIndex: metadata.StatementIndex,
		Verification: metadata.Verification, SQL: sqlStatement, Args: ownedArgs, Kind: kind, Started: true,
	})
	return len(o.records) - 1, nil
}

func (o *handwrittenObserver) complete(index int, update func(*statementObservation)) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if index < 0 || index >= len(o.records) {
		return errors.New("handwritten observer record index is invalid")
	}
	record := &o.records[index]
	if record.Completed {
		return nil
	}
	update(record)
	record.Completed = true
	record.CompletionCount++
	return nil
}

func (o *handwrittenObserver) ExecContext(ctx context.Context, execer handwrittenExecer, metadata statementMetadata, sqlStatement string, args ...any) (sql.Result, error) {
	ownedArgs, err := cloneHandwrittenValues(args)
	if err != nil {
		return nil, err
	}
	index, err := o.begin(metadata, "exec", sqlStatement, ownedArgs)
	if err != nil {
		return nil, err
	}
	result, callErr := execer.ExecContext(ctx, sqlStatement, ownedArgs...)
	var affected int64
	var affectedErr error
	if callErr == nil && !metadata.Scope {
		if result == nil {
			affectedErr = errHandwrittenMissingResult
		} else {
			affected, affectedErr = result.RowsAffected()
		}
	}
	completeErr := o.complete(index, func(record *statementObservation) {
		record.Phase = "execution"
		record.Err = callErr
		record.RowsAffectedErr = affectedErr
		if callErr == nil && affectedErr == nil && !metadata.Scope {
			record.RowsAffected = affected
			record.RowsAffectedValid = true
		}
	})
	if completeErr != nil {
		return result, completeErr
	}
	return result, callErr
}

func (o *handwrittenObserver) QueryContext(ctx context.Context, querier handwrittenQuerier, metadata statementMetadata, sqlStatement string, args ...any) (*handwrittenRows, error) {
	ownedArgs, err := cloneHandwrittenValues(args)
	if err != nil {
		return nil, err
	}
	index, err := o.begin(metadata, "query", sqlStatement, ownedArgs)
	if err != nil {
		return nil, err
	}
	rows, queryErr := querier.QueryContext(ctx, sqlStatement, ownedArgs...)
	if queryErr != nil {
		_ = o.complete(index, func(record *statementObservation) { record.Phase, record.Err = "execution", queryErr })
		return nil, queryErr
	}
	if rows == nil {
		nilErr := errors.New("handwritten query returned nil rows")
		_ = o.complete(index, func(record *statementObservation) { record.Phase, record.Err = "execution", nilErr })
		return nil, nilErr
	}
	return &handwrittenRows{observer: o, index: index, rows: rows}, nil
}

func (o *handwrittenObserver) QueryOne(ctx context.Context, querier handwrittenQuerier, metadata statementMetadata, sqlStatement string, destination []any, args ...any) error {
	rows, err := o.QueryContext(ctx, querier, metadata, sqlStatement, args...)
	if err != nil {
		return err
	}
	if !rows.next(sql.ErrNoRows) {
		return rows.Err()
	}
	if err := rows.Scan(destination...); err != nil {
		return err
	}
	if rows.Next() {
		return rows.closeWithError(errHandwrittenCardinality)
	}
	return rows.Err()
}

var errHandwrittenCardinality = errors.New("handwritten query returned multiple rows")

var errHandwrittenMissingResult = errors.New("handwritten exec returned nil result")

var errHandwrittenUnsupportedDestination = errors.New("handwritten scan destination is unsupported")

type handwrittenRows struct {
	observer       *handwrittenObserver
	index          int
	rows           *sql.Rows
	exhausted      bool
	closed         bool
	current        bool
	currentScanned bool
	terminalErr    error
}

func (r *handwrittenRows) Next() bool {
	return r.next(nil)
}

func (r *handwrittenRows) next(noRowsError error) bool {
	if r == nil || r.closed || r.terminalErr != nil {
		return false
	}
	if r.rows.Next() {
		r.current = true
		r.currentScanned = false
		return true
	}
	r.exhausted = true
	rowsErr := r.rows.Err()
	closeErr := r.rows.Close()
	r.closed = true
	if rowsErr != nil || closeErr != nil {
		noRowsError = nil
	}
	r.finish(false, nil, rowsErr, closeErr, noRowsError)
	return false
}

func (r *handwrittenRows) Scan(destination ...any) error {
	if r == nil || r.closed || !r.current {
		return errors.New("handwritten rows has no current row")
	}
	if err := r.rows.Scan(destination...); err != nil {
		return r.failScan(err)
	}
	if r.currentScanned {
		return nil
	}
	values, err := snapshotHandwrittenDestinations(destination)
	if err != nil {
		return r.failScan(err)
	}
	r.currentScanned = true
	r.finishScan(values)
	return nil
}

func (r *handwrittenRows) finishScan(values []any) {
	r.observer.mu.Lock()
	record := &r.observer.records[r.index]
	record.RowsConsumed++
	record.RowValues = append(record.RowValues, values)
	r.observer.mu.Unlock()
}

func (r *handwrittenRows) failScan(scanErr error) error {
	rowsErr := r.rows.Err()
	closeErr := r.rows.Close()
	r.closed = true
	r.current = false
	r.finish(true, scanErr, rowsErr, closeErr, nil)
	return r.terminalErr
}

func (r *handwrittenRows) closeWithError(extra error) error {
	if r == nil {
		return extra
	}
	if r.terminalErr != nil {
		return r.terminalErr
	}
	closeErr := r.rows.Close()
	rowsErr := r.rows.Err()
	r.closed = true
	r.current = false
	r.finish(true, nil, rowsErr, closeErr, extra)
	return r.terminalErr
}

func (r *handwrittenRows) Close() error {
	if r == nil {
		return nil
	}
	if r.closed {
		return r.terminalErr
	}
	closeErr := r.rows.Close()
	rowsErr := r.rows.Err()
	early := !r.exhausted
	r.closed = true
	r.current = false
	r.finish(early, nil, rowsErr, closeErr, nil)
	return r.terminalErr
}

func (r *handwrittenRows) Err() error {
	if r == nil {
		return nil
	}
	if r.terminalErr != nil {
		return r.terminalErr
	}
	return r.rows.Err()
}

func (r *handwrittenRows) finish(early bool, scanErr, rowsErr, closeErr, extra error) {
	if r.terminalErr != nil {
		return
	}
	r.terminalErr = errors.Join(scanErr, rowsErr, closeErr, extra)
	_ = r.observer.complete(r.index, func(record *statementObservation) {
		record.Phase = "consumption"
		record.EarlyClose = early
		record.ScanErr = scanErr
		record.RowsErr = rowsErr
		record.CloseErr = closeErr
		record.Err = r.terminalErr
		if r.terminalErr == nil && record.RowValues == nil {
			record.RowValues = make([][]any, 0)
		}
	})
}

func snapshotHandwrittenDestinations(destinations []any) ([]any, error) {
	values := make([]any, len(destinations))
	for index, destination := range destinations {
		value, err := snapshotHandwrittenDestination(destination)
		if err != nil {
			return nil, err
		}
		values[index] = value
	}
	return values, nil
}

func snapshotHandwrittenDestination(destination any) (any, error) {
	switch typed := destination.(type) {
	case *sql.NullBool:
		if !typed.Valid {
			return nil, nil
		}
		return typed.Bool, nil
	case *sql.NullInt64:
		if !typed.Valid {
			return nil, nil
		}
		return typed.Int64, nil
	case *sql.NullString:
		if !typed.Valid {
			return nil, nil
		}
		return typed.String, nil
	case *sql.NullTime:
		if !typed.Valid {
			return nil, nil
		}
		return typed.Time, nil
	case *time.Time:
		return *typed, nil
	case *[]byte:
		return append([]byte(nil), (*typed)...), nil
	case *sql.RawBytes:
		return sql.RawBytes(append([]byte(nil), (*typed)...)), nil
	}
	value := reflect.ValueOf(destination)
	if !value.IsValid() || value.Kind() != reflect.Pointer || value.IsNil() {
		return nil, errors.New("handwritten scan destination must be a non-nil pointer")
	}
	element := value.Elem()
	if element.Kind() == reflect.Slice && element.Type().Elem().Kind() == reflect.Uint8 {
		return append([]byte(nil), element.Bytes()...), nil
	}
	switch element.Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64,
		reflect.String:
		return element.Interface(), nil
	case reflect.Struct:
		if element.Type() == reflect.TypeOf(time.Time{}) {
			return element.Interface(), nil
		}
	}
	return nil, fmt.Errorf("%w: %T", errHandwrittenUnsupportedDestination, destination)
}

func (o *handwrittenObserver) Snapshot() ([]statementObservation, error) {
	if o == nil {
		return nil, errors.New("handwritten observer is nil")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	result := make([]statementObservation, len(o.records))
	for index, record := range o.records {
		if !record.Started || !record.Completed || record.CompletionCount != 1 {
			return nil, fmt.Errorf("handwritten statement %d is incomplete", index)
		}
		result[index] = record
		args, err := cloneHandwrittenValues(record.Args)
		if err != nil {
			return nil, err
		}
		result[index].Args = args
		if record.RowValues == nil {
			result[index].RowValues = nil
			continue
		}
		result[index].RowValues = make([][]any, len(record.RowValues))
		for rowIndex, row := range record.RowValues {
			values, err := cloneHandwrittenValues(row)
			if err != nil {
				return nil, err
			}
			result[index].RowValues[rowIndex] = values
		}
	}
	return result, nil
}
