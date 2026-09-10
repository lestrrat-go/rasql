package rasql_test

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

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
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
	Total    rasql.Nullable[int64] `rasql:"total"`
	Code     dynamicProjectionCode `rasql:"code"`
}

type dynamicProjectionCode struct {
	Value string
	Valid bool
}

type dynamicProjectionAggregateRow struct {
	UserID int64
	Name   string
	Total  rasql.Nullable[int64]
}

type dynamicProjectionAggregateDecoder struct {
	schema rasql.ResultSchema
}

func (d dynamicProjectionAggregateDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (dynamicProjectionAggregateDecoder) Presence() []rasql.Presence         { return nil }
func (d dynamicProjectionAggregateDecoder) DecodeRow(source rasql.ScanSource, result *dynamicProjectionAggregateRow) error {
	var total any
	if err := source.Scan(&result.UserID, &result.Name, &total); err != nil {
		return err
	}
	if total == nil {
		result.Total = rasql.Nullable[int64]{}
		return nil
	}
	if err := rasql.ScanValue(&result.Total.Value, total); err != nil {
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

func dynamicProjectionSchema(t *testing.T) rasql.ResultSchema {
	t.Helper()
	s, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "name", Type: schema.TextType{}},
		rasql.ResultColumn{Name: "nickname", Type: schema.TextType{}, Nullable: true},
		rasql.ResultColumn{Name: "total", Type: schema.IntegerType{}, Nullable: true},
		rasql.ResultColumn{Name: "code", Type: schema.TextType{}, Nullable: true},
	)
	require.NoError(t, err)
	return s
}

func dynamicProjectionNativeQuery[R any](t *testing.T, projection rasql.Projection[R], sqlText string) rasql.Query[R] {
	t.Helper()
	q, err := rasql.Native(rasql.NativeStatement{Engine: "sqlite", SQL: sqlText}, projection, rasql.Many)
	require.NoError(t, err)
	return q
}

func TestDynamicProjection(t *testing.T) {
	t.Run("construction and returned column binding", func(t *testing.T) {
		schemaValue := dynamicProjectionSchema(t)
		projection, err := rasql.DynamicProjection[dynamicProjectionRow](schemaValue)
		require.NoError(t, err)
		require.NoError(t, projection.Validate())
		columns := projection.Schema().Columns()
		require.Equal(t, schemaValue.Columns(), columns)
		columns[0].Name = "changed"
		require.Equal(t, "id", projection.Schema().Columns()[0].Name)

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
				seq, err := rasql.Rows(t.Context(), executor, q)
				require.NoError(t, err)
				var got []error
				for _, err := range seq {
					got = append(got, err)
				}
				require.Len(t, got, 1)
				var planErr *rasql.PlanError
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
			values, err := rasql.All(t.Context(), executor, q)
			require.NoError(t, err)
			require.Len(t, values, 1)
			require.Equal(t, "Ada", values[0].Name)
			require.Equal(t, int64(42), values[0].ID)
			require.Equal(t, int64(7), values[0].Total.Value)
			require.True(t, values[0].Total.Valid)
			require.Equal(t, "A-1", values[0].Code.Value)
			require.True(t, values[0].Code.Valid)
		})
	})

	t.Run("empty rows retain the schema and finish", func(t *testing.T) {
		schemaValue := dynamicProjectionSchema(t)
		projection, err := rasql.DynamicProjection[dynamicProjectionRow](schemaValue)
		require.NoError(t, err)
		rows := &dynamicProjectionRows{columns: []string{"id", "name", "nickname", "total", "code"}}

		query := dynamicProjectionNativeQuery(t, projection, "SELECT 1")
		values, err := rasql.All(t.Context(), dynamicProjectionExecutorFor(t, rows), query)
		require.NoError(t, err)
		require.Empty(t, values)
		require.Equal(t, schemaValue.Columns(), projection.Schema().Columns())
		require.Equal(t, 1, rows.closeCalls)
		require.Equal(t, 1, rows.finishCalls)
	})

	t.Run("a columns error finishes the rows", func(t *testing.T) {
		schemaValue := dynamicProjectionSchema(t)
		projection, err := rasql.DynamicProjection[dynamicProjectionRow](schemaValue)
		require.NoError(t, err)
		columnErr := errors.New("columns unavailable")
		rows := &dynamicProjectionRows{
			columns:    []string{"id", "name", "nickname", "total", "code"},
			columnsErr: columnErr,
		}

		query := dynamicProjectionNativeQuery(t, projection, "SELECT 1")
		sequence, err := rasql.Rows(t.Context(), dynamicProjectionExecutorFor(t, rows), query)
		require.NoError(t, err)
		var yielded []error
		for _, err := range sequence {
			yielded = append(yielded, err)
		}
		require.Len(t, yielded, 1)
		require.ErrorIs(t, yielded[0], columnErr)
		require.Equal(t, 1, rows.closeCalls)
		require.Equal(t, 1, rows.finishCalls)
	})

	t.Run("SQLite matches a hand-written decoder", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		database.SetMaxOpenConns(1)
		db, err := rasql.New(database, dialect.SQLite())
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
		resultSchema, err := rasql.NewResultSchema(
			rasql.ResultColumn{Name: "user_id", Type: schema.IntegerType{}},
			rasql.ResultColumn{Name: "name", Type: schema.TextType{}},
			rasql.ResultColumn{Name: "total", Type: schema.IntegerType{}, Nullable: true},
		)
		require.NoError(t, err)
		decoder := dynamicProjectionAggregateDecoder{schema: resultSchema}
		items := []rasql.ProjectionItem{
			rasql.Item("user_id", rasql.Value(int64(0)), schema.IntegerType{}, ""),
			rasql.Item("name", rasql.Value(""), schema.TextType{}, ""),
			rasql.NullItem("total", rasql.SumExpr(rasql.Value(int64(0))), schema.IntegerType{}, ""),
		}
		staticProjection, err := rasql.NewProjection(items, decoder)
		require.NoError(t, err)
		dynamicProjection, err := rasql.DynamicProjection[dynamicProjectionAggregateRow](resultSchema)
		require.NoError(t, err)
		sqlText := "SELECT u.id AS user_id, u.name AS name, SUM(o.amount) AS total FROM users u LEFT JOIN orders o ON o.user_id = u.id GROUP BY u.id ORDER BY u.id"
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		staticQuery := dynamicProjectionNativeQuery(t, staticProjection, sqlText)
		dynamicQuery := dynamicProjectionNativeQuery(t, dynamicProjection, sqlText)
		staticValues, err := rasql.All(t.Context(), executor, staticQuery)
		require.NoError(t, err)
		dynamicValues, err := rasql.All(t.Context(), executor, dynamicQuery)
		require.NoError(t, err)
		require.Equal(t, staticValues, dynamicValues)
		require.Equal(t, []dynamicProjectionAggregateRow{{UserID: 1, Name: "Ada", Total: rasql.Nullable[int64]{Value: 12, Valid: true}}, {UserID: 2, Name: "Bob"}}, dynamicValues)
	})

	t.Run("null scanner domain and incompatible values", func(t *testing.T) {
		schemaValue, err := rasql.NewResultSchema(
			rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
			rasql.ResultColumn{Name: "code", Type: schema.TextType{}, Nullable: true},
		)
		require.NoError(t, err)
		type row struct {
			ID   int64                 `rasql:"id"`
			Code dynamicProjectionCode `rasql:"code"`
		}
		projection, err := rasql.DynamicProjection[row](schemaValue)
		require.NoError(t, err)
		t.Run("null reaches scanner", func(t *testing.T) {
			rows := &dynamicProjectionRows{columns: []string{"id", "code"}, values: [][]any{{int64(1), nil}}}
			values, err := rasql.All(t.Context(), dynamicProjectionExecutorFor(t, rows), dynamicProjectionNativeQuery(t, projection, "SELECT 1"))
			require.NoError(t, err)
			require.Equal(t, row{ID: 1}, values[0])
		})
		t.Run("scanner failure preserves cause and closes once", func(t *testing.T) {
			cause := errors.New("scanner source failure")
			rows := &dynamicProjectionRows{columns: []string{"id", "code"}, values: [][]any{{int64(1), cause}}}
			_, err := rasql.All(t.Context(), dynamicProjectionExecutorFor(t, rows), dynamicProjectionNativeQuery(t, projection, "SELECT 1"))
			require.ErrorIs(t, err, cause)
			require.Equal(t, 1, rows.closeCalls)
			require.Equal(t, 1, rows.finishCalls)
		})
		t.Run("incompatible schema fails at construction", func(t *testing.T) {
			badSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.TextType{}})
			require.NoError(t, err)
			_, err = rasql.DynamicProjection[struct {
				ID int64 `rasql:"id"`
			}](badSchema)
			var planErr *rasql.PlanError
			require.ErrorAs(t, err, &planErr)
			require.Equal(t, "invalid_projection", planErr.Code)
		})
		t.Run("NULL into non-null field is a decode error", func(t *testing.T) {
			nonNullable, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
			require.NoError(t, err)
			idProjection, err := rasql.DynamicProjection[struct {
				ID int64 `rasql:"id"`
			}](nonNullable)
			require.NoError(t, err)
			rows := &dynamicProjectionRows{columns: []string{"id"}, values: [][]any{{nil}}}
			_, err = rasql.All(t.Context(), dynamicProjectionExecutorFor(t, rows), dynamicProjectionNativeQuery(t, idProjection, "SELECT 1"))
			require.ErrorIs(t, err, rasql.ErrUnexpectedNull)
			require.Equal(t, 1, rows.closeCalls)
		})
		t.Run("incompatible returned value preserves decode failure", func(t *testing.T) {
			textSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "code", Type: schema.TextType{}})
			require.NoError(t, err)
			textProjection, err := rasql.DynamicProjection[struct {
				Code string `rasql:"code"`
			}](textSchema)
			require.NoError(t, err)
			rows := &dynamicProjectionRows{columns: []string{"code"}, values: [][]any{{int64(9)}}}
			_, err = rasql.All(t.Context(), dynamicProjectionExecutorFor(t, rows), dynamicProjectionNativeQuery(t, textProjection, "SELECT 1"))
			var decodeErr *rasql.DecodeError
			require.ErrorAs(t, err, &decodeErr)
			require.Equal(t, "code", decodeErr.Column)
			require.Equal(t, rasql.CodecID(""), decodeErr.Codec)
			require.Equal(t, 1, rows.closeCalls)
		})
	})

	t.Run("rejects tagged unexported fields", func(t *testing.T) {
		schemaValue, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "code", Type: schema.TextType{}})
		require.NoError(t, err)
		_, err = rasql.DynamicProjection[struct {
			code string `rasql:"code"`
		}](schemaValue)
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "invalid_projection", planErr.Code)
		require.Equal(t, "decoder", planErr.Path)
	})

	t.Run("reordered columns use bound codec positions", func(t *testing.T) {
		schemaValue, err := rasql.NewResultSchema(
			rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}, Codec: "dynamic_int"},
			rasql.ResultColumn{Name: "name", Type: schema.TextType{}, Codec: "dynamic_text"},
		)
		require.NoError(t, err)
		projection, err := rasql.DynamicProjection[struct {
			ID   int64  `rasql:"id"`
			Name string `rasql:"name"`
		}](schemaValue)
		require.NoError(t, err)
		rows := &dynamicProjectionRows{columns: []string{"name", "id"}, values: [][]any{{"Ada", int64(42)}}}
		executor := dynamicProjectionExecutorFor(t, rows)
		codecs, err := rasql.NewCodecRegistry(map[rasql.CodecID]rasql.ValueCodec{
			"dynamic_int":  dynamicProjectionCodec{},
			"dynamic_text": dynamicProjectionCodec{},
		})
		require.NoError(t, err)
		executor, err = rasql.WithCodecs(executor, codecs)
		require.NoError(t, err)
		values, err := rasql.All(t.Context(), executor, dynamicProjectionNativeQuery(t, projection, "SELECT 1"))
		require.NoError(t, err)
		require.Len(t, values, 1)
		require.Equal(t, int64(42), values[0].ID)
		require.Equal(t, "Ada", values[0].Name)
	})

	t.Run("cancellation and reuse", func(t *testing.T) {
		schemaValue, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
		require.NoError(t, err)
		projection, err := rasql.DynamicProjection[struct {
			ID int64 `rasql:"id"`
		}](schemaValue)
		require.NoError(t, err)
		executor := dynamicProjectionExecutorFor(t, &dynamicProjectionRows{columns: []string{"id"}, values: [][]any{{int64(1)}}})
		q := dynamicProjectionNativeQuery(t, projection, "SELECT 1")
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err = rasql.All(ctx, executor, q)
		require.ErrorIs(t, err, context.Canceled)

		var calls atomic.Int64
		reusable := &dynamicProjectionExecutor{dialect: dialect.SQLite(), factory: func() *dynamicProjectionRows {
			calls.Add(1)
			return &dynamicProjectionRows{columns: []string{"id"}, values: [][]any{{int64(9)}}}
		}}
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		profiled, err := rasql.WithEngineProfile(reusable, profile)
		require.NoError(t, err)
		var wg sync.WaitGroup
		errs := make(chan error, 100)
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				values, err := rasql.All(t.Context(), profiled, q)
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
	})

	// TestDynamicProjection/"rejects a non-struct result type" covers a gap the
	// returning_preflight_test.go audit found: internal/rowvalue.Decoder used to
	// refuse a non-struct decode destination with "row: decode destination %T
	// must be a struct", but that decoder is dead code today (NewDecoder has no
	// caller left in this module now that QueryWriteAll and QueryWriteOne are
	// gone). DynamicProjection is the canonical replacement, its dynamicFields
	// check carries the same restriction under a different message, and it is
	// reachable through any caller building a Projection[R] for a non-struct R
	// with DynamicProjection instead of a hand-written RowDecoder[R] -- but
	// nothing in dynamic_projection_internal_test.go called it with one, so this check
	// had no test at all until this one.
	t.Run("rejects a non-struct result type", func(t *testing.T) {
		result, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
		require.NoError(t, err)
		_, err = rasql.DynamicProjection[int64](result)
		require.ErrorContains(t, err, "must be a struct")
	})
}

type dynamicProjectionCodec struct{}

func (dynamicProjectionCodec) Encode(value any) (driver.Value, error) { return value, nil }
func (dynamicProjectionCodec) Decode(value any, destination any) error {
	return dynamicProjectionAssign(destination, value)
}

type dynamicProjectionExecutor struct {
	dialect dialect.Dialect
	factory func() *dynamicProjectionRows
	mu      sync.Mutex
	rows    []*dynamicProjectionRows
}

func dynamicProjectionExecutorFor(t *testing.T, rows *dynamicProjectionRows) rasql.Executor {
	t.Helper()
	executor := &dynamicProjectionExecutor{dialect: dialect.SQLite(), factory: func() *dynamicProjectionRows { return rows }}
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	profiled, err := rasql.WithEngineProfile(executor, profile)
	require.NoError(t, err)
	return profiled
}

func (e *dynamicProjectionExecutor) Dialect() dialect.Dialect { return e.dialect }
func (e *dynamicProjectionExecutor) Query(ctx context.Context, _ stmt.Statement) (rasql.ResultRows, error) {
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
	columnsErr  error
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
	if r.columnsErr != nil {
		return nil, r.columnsErr
	}
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

var _ rasql.Executor = (*dynamicProjectionExecutor)(nil)
var _ rasql.ResultRows = (*dynamicProjectionRows)(nil)
