package rasql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type dynamicProjectionEmbedded struct {
	Name string `rasql:"name"`
}

type dynamicProjectionRow struct {
	dynamicProjectionEmbedded
	ID       int64                 `rasql:"id"`
	Nickname *string               `rasql:"nickname"`
	Total    Nullable[int64]       `rasql:"total"`
	Code     dynamicProjectionCode `rasql:"code"`
}

type dynamicProjectionCode struct {
	Value string
	Valid bool
}

type dynamicProjectionAggregateRow struct {
	UserID int64
	Name   string
	Total  Nullable[int64]
}

type dynamicProjectionAggregateDecoder struct {
	schema ResultSchema
}

func (d dynamicProjectionAggregateDecoder) ResultSchema() ResultSchema { return d.schema }
func (dynamicProjectionAggregateDecoder) Presence() []Presence         { return nil }
func (d dynamicProjectionAggregateDecoder) DecodeRow(source ScanSource, result *dynamicProjectionAggregateRow) error {
	var total any
	if err := source.Scan(&result.UserID, &result.Name, &total); err != nil {
		return err
	}
	if total == nil {
		result.Total = Nullable[int64]{}
		return nil
	}
	if err := ScanValue(&result.Total.Value, total); err != nil {
		return err
	}
	result.Total.Valid = true
	return nil
}

func (d *dynamicProjectionCode) Scan(value any) error {
	if value == nil {
		d.Value, d.Valid = "", false
		return nil
	}
	s, ok := value.(string)
	if !ok {
		return fmt.Errorf("code requires string, got %T", value)
	}
	d.Value, d.Valid = s, true
	return nil
}

var _ sql.Scanner = (*dynamicProjectionCode)(nil)

func dynamicProjectionSchema(t *testing.T) ResultSchema {
	t.Helper()
	s, err := NewResultSchema(
		ResultColumn{Name: "id", Type: schema.IntegerType{}},
		ResultColumn{Name: "name", Type: schema.TextType{}},
		ResultColumn{Name: "nickname", Type: schema.TextType{}, Nullable: true},
		ResultColumn{Name: "total", Type: schema.IntegerType{}, Nullable: true},
		ResultColumn{Name: "code", Type: schema.TextType{}, Nullable: true},
	)
	require.NoError(t, err)
	return s
}

func dynamicProjectionNativeQuery[R any](t *testing.T, projection Projection[R], sqlText string) Query[R] {
	t.Helper()
	q, err := Native(NativeStatement{Engine: "sqlite", SQL: sqlText}, projection, Many)
	require.NoError(t, err)
	return q
}

func TestDynamicProjectionConstructionAndReturnedColumnBinding(t *testing.T) {
	schemaValue := dynamicProjectionSchema(t)
	projection, err := DynamicProjection[dynamicProjectionRow](schemaValue)
	require.NoError(t, err)
	require.NoError(t, projection.Validate())

	for _, tc := range []struct {
		name    string
		columns []string
		wantErr string
	}{
		{name: "missing", columns: []string{"id", "name", "nickname", "total"}, wantErr: "uncertain_contract"},
		{name: "duplicate", columns: []string{"id", "name", "nickname", "total", "total"}, wantErr: "uncertain_contract"},
		{name: "extra", columns: []string{"id", "name", "nickname", "total", "unknown"}, wantErr: "uncertain_contract"},
		{name: "unknown", columns: []string{"id", "name", "nickname", "code", "other"}, wantErr: "uncertain_contract"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := &dynamicProjectionRows{columns: tc.columns, values: [][]any{{int64(1), "Ada", nil, nil, "A"}}}
			executor := dynamicProjectionExecutorFor(t, rows)
			q := dynamicProjectionNativeQuery(t, projection, "SELECT 1")
			seq, err := Rows(t.Context(), executor, q)
			require.NoError(t, err)
			var got []error
			for _, err := range seq {
				got = append(got, err)
			}
			require.Len(t, got, 1)
			var planErr *PlanError
			require.ErrorAs(t, got[0], &planErr)
			require.Equal(t, tc.wantErr, planErr.Code)
			require.Zero(t, rows.nextCalls)
			require.Equal(t, 1, rows.closeCalls)
			require.Equal(t, 1, rows.finishCalls)
		})
	}

	t.Run("reordered names preserve positional metadata", func(t *testing.T) {
		rows := &dynamicProjectionRows{
			columns: []string{"total", "id", "name", "code", "nickname"},
			values:  [][]any{{int64(7), int64(42), "Ada", "A-1", "ada"}},
		}
		executor := dynamicProjectionExecutorFor(t, rows)
		q := dynamicProjectionNativeQuery(t, projection, "SELECT 1")
		values, err := All(t.Context(), executor, q)
		require.NoError(t, err)
		require.Len(t, values, 1)
		require.Equal(t, "Ada", values[0].Name)
		require.Equal(t, int64(42), values[0].ID)
		require.Equal(t, int64(7), values[0].Total.Value)
		require.True(t, values[0].Total.Valid)
		require.Equal(t, "A-1", values[0].Code.Value)
		require.True(t, values[0].Code.Valid)
	})
}

func TestDynamicProjectionSQLiteMatchesHandwrittenDecoder(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	database.SetMaxOpenConns(1)
	db, err := New(database, dialect.SQLite())
	require.NoError(t, err)
	for _, statement := range []string{
		"CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL)",
		"CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL, amount INTEGER NOT NULL)",
		"INSERT INTO users (id, name) VALUES (1, 'Ada'), (2, 'Bob')",
		"INSERT INTO orders (id, user_id, amount) VALUES (1, 1, 7), (2, 1, 5)",
	} {
		_, err = database.ExecContext(t.Context(), statement)
		require.NoError(t, err)
	}
	resultSchema, err := NewResultSchema(
		ResultColumn{Name: "user_id", Type: schema.IntegerType{}},
		ResultColumn{Name: "name", Type: schema.TextType{}},
		ResultColumn{Name: "total", Type: schema.IntegerType{}, Nullable: true},
	)
	require.NoError(t, err)
	decoder := dynamicProjectionAggregateDecoder{schema: resultSchema}
	items := []ProjectionItem{
		{expression: query.Bind(nil), column: ResultColumn{Name: "user_id", Type: schema.IntegerType{}}},
		{expression: query.Bind(nil), column: ResultColumn{Name: "name", Type: schema.TextType{}}},
		{expression: query.Bind(nil), column: ResultColumn{Name: "total", Type: schema.IntegerType{}, Nullable: true}},
	}
	staticProjection, err := NewProjection(items, decoder)
	require.NoError(t, err)
	dynamicProjection, err := DynamicProjection[dynamicProjectionAggregateRow](resultSchema)
	require.NoError(t, err)
	sqlText := "SELECT u.id AS user_id, u.name AS name, SUM(o.amount) AS total FROM users u LEFT JOIN orders o ON o.user_id = u.id GROUP BY u.id ORDER BY u.id"
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := AsExecutor(db, profile)
	require.NoError(t, err)
	staticQuery := dynamicProjectionNativeQuery(t, staticProjection, sqlText)
	dynamicQuery := dynamicProjectionNativeQuery(t, dynamicProjection, sqlText)
	staticValues, err := All(t.Context(), executor, staticQuery)
	require.NoError(t, err)
	dynamicValues, err := All(t.Context(), executor, dynamicQuery)
	require.NoError(t, err)
	require.Equal(t, staticValues, dynamicValues)
	require.Equal(t, []dynamicProjectionAggregateRow{{UserID: 1, Name: "Ada", Total: Nullable[int64]{Value: 12, Valid: true}}, {UserID: 2, Name: "Bob"}}, dynamicValues)
}

func TestDynamicProjectionNullScannerDomainAndIncompatibleValues(t *testing.T) {
	schemaValue, err := NewResultSchema(
		ResultColumn{Name: "id", Type: schema.IntegerType{}},
		ResultColumn{Name: "code", Type: schema.TextType{}, Nullable: true},
	)
	require.NoError(t, err)
	type row struct {
		ID   int64                 `rasql:"id"`
		Code dynamicProjectionCode `rasql:"code"`
	}
	projection, err := DynamicProjection[row](schemaValue)
	require.NoError(t, err)
	t.Run("null reaches scanner", func(t *testing.T) {
		rows := &dynamicProjectionRows{columns: []string{"id", "code"}, values: [][]any{{int64(1), nil}}}
		values, err := All(t.Context(), dynamicProjectionExecutorFor(t, rows), dynamicProjectionNativeQuery(t, projection, "SELECT 1"))
		require.NoError(t, err)
		require.Equal(t, row{ID: 1}, values[0])
	})
	t.Run("scanner failure preserves cause and closes once", func(t *testing.T) {
		cause := errors.New("scanner source failure")
		rows := &dynamicProjectionRows{columns: []string{"id", "code"}, values: [][]any{{int64(1), cause}}}
		_, err := All(t.Context(), dynamicProjectionExecutorFor(t, rows), dynamicProjectionNativeQuery(t, projection, "SELECT 1"))
		require.ErrorIs(t, err, cause)
		require.Equal(t, 1, rows.closeCalls)
		require.Equal(t, 1, rows.finishCalls)
	})
	t.Run("incompatible schema fails at construction", func(t *testing.T) {
		badSchema, err := NewResultSchema(ResultColumn{Name: "id", Type: schema.TextType{}})
		require.NoError(t, err)
		_, err = DynamicProjection[struct {
			ID int64 `rasql:"id"`
		}](badSchema)
		var planErr *PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "invalid_projection", planErr.Code)
	})
	t.Run("NULL into non-null field is a decode error", func(t *testing.T) {
		nonNullable, err := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}})
		require.NoError(t, err)
		idProjection, err := DynamicProjection[struct {
			ID int64 `rasql:"id"`
		}](nonNullable)
		require.NoError(t, err)
		rows := &dynamicProjectionRows{columns: []string{"id"}, values: [][]any{{nil}}}
		_, err = All(t.Context(), dynamicProjectionExecutorFor(t, rows), dynamicProjectionNativeQuery(t, idProjection, "SELECT 1"))
		require.ErrorIs(t, err, ErrUnexpectedNull)
		require.Equal(t, 1, rows.closeCalls)
	})
	t.Run("incompatible returned value preserves decode failure", func(t *testing.T) {
		textSchema, err := NewResultSchema(ResultColumn{Name: "code", Type: schema.TextType{}})
		require.NoError(t, err)
		textProjection, err := DynamicProjection[struct {
			Code string `rasql:"code"`
		}](textSchema)
		require.NoError(t, err)
		rows := &dynamicProjectionRows{columns: []string{"code"}, values: [][]any{{int64(9)}}}
		_, err = All(t.Context(), dynamicProjectionExecutorFor(t, rows), dynamicProjectionNativeQuery(t, textProjection, "SELECT 1"))
		require.Error(t, err)
		require.Equal(t, 1, rows.closeCalls)
	})
}

func TestDynamicProjectionReorderedColumnsUseBoundCodecPositions(t *testing.T) {
	schemaValue, err := NewResultSchema(
		ResultColumn{Name: "id", Type: schema.IntegerType{}, Codec: "dynamic_int"},
		ResultColumn{Name: "name", Type: schema.TextType{}, Codec: "dynamic_text"},
	)
	require.NoError(t, err)
	projection, err := DynamicProjection[struct {
		ID   int64  `rasql:"id"`
		Name string `rasql:"name"`
	}](schemaValue)
	require.NoError(t, err)
	rows := &dynamicProjectionRows{columns: []string{"name", "id"}, values: [][]any{{"Ada", int64(42)}}}
	executor := dynamicProjectionExecutorFor(t, rows)
	codecs, err := NewCodecRegistry(map[CodecID]ValueCodec{
		"dynamic_int":  dynamicProjectionCodec{},
		"dynamic_text": dynamicProjectionCodec{},
	})
	require.NoError(t, err)
	executor, err = WithCodecs(executor, codecs)
	require.NoError(t, err)
	values, err := All(t.Context(), executor, dynamicProjectionNativeQuery(t, projection, "SELECT 1"))
	require.NoError(t, err)
	require.Len(t, values, 1)
	require.Equal(t, int64(42), values[0].ID)
	require.Equal(t, "Ada", values[0].Name)
}

type dynamicProjectionCodec struct{}

func (dynamicProjectionCodec) Encode(value any) (driver.Value, error) { return value, nil }
func (dynamicProjectionCodec) Decode(value any, destination any) error {
	return dynamicProjectionAssign(destination, value)
}

func TestDynamicProjectionCancellationAndReuse(t *testing.T) {
	schemaValue, err := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	projection, err := DynamicProjection[struct {
		ID int64 `rasql:"id"`
	}](schemaValue)
	require.NoError(t, err)
	executor := dynamicProjectionExecutorFor(t, &dynamicProjectionRows{columns: []string{"id"}, values: [][]any{{int64(1)}}})
	q := dynamicProjectionNativeQuery(t, projection, "SELECT 1")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = All(ctx, executor, q)
	require.ErrorIs(t, err, context.Canceled)

	var calls atomic.Int64
	reusable := &dynamicProjectionExecutor{dialect: dialect.SQLite(), factory: func() *dynamicProjectionRows {
		calls.Add(1)
		return &dynamicProjectionRows{columns: []string{"id"}, values: [][]any{{int64(9)}}}
	}}
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	profiled, err := WithEngineProfile(reusable, profile)
	require.NoError(t, err)
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			values, err := All(t.Context(), profiled, q)
			if err != nil {
				errs <- err
				return
			}
			if len(values) != 1 || values[0].ID != 9 {
				errs <- fmt.Errorf("unexpected concurrent value: %#v", values)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, int64(100), calls.Load())
}

type dynamicProjectionExecutor struct {
	dialect dialect.Dialect
	factory func() *dynamicProjectionRows
	mu      sync.Mutex
	rows    []*dynamicProjectionRows
}

func dynamicProjectionExecutorFor(t *testing.T, rows *dynamicProjectionRows) Executor {
	t.Helper()
	executor := &dynamicProjectionExecutor{dialect: dialect.SQLite(), factory: func() *dynamicProjectionRows { return rows }}
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	profiled, err := WithEngineProfile(executor, profile)
	require.NoError(t, err)
	return profiled
}

func (e *dynamicProjectionExecutor) Dialect() dialect.Dialect { return e.dialect }
func (e *dynamicProjectionExecutor) Query(ctx context.Context, _ stmt.Statement) (ResultRows, error) {
	rows := e.factory()
	rows.ctx = ctx
	e.mu.Lock()
	e.rows = append(e.rows, rows)
	e.mu.Unlock()
	return rows, nil
}
func (*dynamicProjectionExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) {
	return driver.RowsAffected(0), nil
}

type dynamicProjectionRows struct {
	columns     []string
	values      [][]any
	index       int
	nextCalls   int
	closeCalls  int
	finishCalls int
	ctxErr      error
	finishErr   error
	iterErr     error
	ctx         context.Context
	closed      bool
}

func (r *dynamicProjectionRows) Columns() ([]string, error) {
	return append([]string(nil), r.columns...), nil
}
func (r *dynamicProjectionRows) Next() bool {
	r.nextCalls++
	if r.ctx != nil && r.ctx.Err() != nil {
		return false
	}
	return r.index < len(r.values)
}
func (r *dynamicProjectionRows) Scan(destinations ...any) error {
	if r.index >= len(r.values) {
		return sql.ErrNoRows
	}
	values := r.values[r.index]
	if len(values) != len(destinations) {
		return fmt.Errorf("scan destinations: got %d, want %d", len(destinations), len(values))
	}
	for i, destination := range destinations {
		if err := dynamicProjectionAssign(destination, values[i]); err != nil {
			return err
		}
	}
	r.index++
	return nil
}
func (r *dynamicProjectionRows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	r.closeCalls++
	return nil
}
func (r *dynamicProjectionRows) Err() error {
	if r.ctx != nil && r.ctx.Err() != nil {
		return r.ctx.Err()
	}
	return r.iterErr
}
func (r *dynamicProjectionRows) RecordRow() {}
func (r *dynamicProjectionRows) Finish(err error, _ bool) error {
	r.finishCalls++
	_ = r.Close()
	if err == nil {
		err = r.ctxErr
	}
	return errors.Join(err, r.finishErr)
}

func dynamicProjectionAssign(destination, value any) error {
	if err, ok := value.(error); ok {
		return err
	}
	if scanner, ok := destination.(sql.Scanner); ok {
		return scanner.Scan(value)
	}
	destinationValue := reflect.ValueOf(destination)
	if destinationValue.Kind() != reflect.Pointer || destinationValue.IsNil() {
		return fmt.Errorf("destination %T is not a pointer", destination)
	}
	if value == nil {
		destinationValue.Elem().SetZero()
		return nil
	}
	sourceValue := reflect.ValueOf(value)
	if sourceValue.Type().AssignableTo(destinationValue.Elem().Type()) {
		destinationValue.Elem().Set(sourceValue)
		return nil
	}
	if sourceValue.Type().ConvertibleTo(destinationValue.Elem().Type()) {
		destinationValue.Elem().Set(sourceValue.Convert(destinationValue.Elem().Type()))
		return nil
	}
	return fmt.Errorf("cannot assign %T to %T", value, destination)
}

var _ Executor = (*dynamicProjectionExecutor)(nil)
var _ ResultRows = (*dynamicProjectionRows)(nil)
