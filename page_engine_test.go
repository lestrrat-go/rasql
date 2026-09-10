package rasql_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

type r5ComboRow struct {
	ID   int64
	Rank sql.NullInt64
}

type r5ComboDecoder struct{ schema rasql.ResultSchema }

func (d r5ComboDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (r5ComboDecoder) Presence() []rasql.Presence         { return nil }
func (d r5ComboDecoder) DecodeRow(source rasql.ScanSource, row *r5ComboRow) error {
	return source.Scan(&row.ID, &row.Rank)
}

func TestPageAfterEngines(t *testing.T) {
	t.Run("PostgreSQL pages match every explicit null order", func(t *testing.T) {
		database := dbtest.PostgreSQLDB(t)
		tableName := dbtest.UniqueName(t, "rasql_r5_pg_nulls")
		_, err := database.ExecContext(t.Context(), "CREATE TABLE "+tableName+" (id BIGINT NOT NULL, rank BIGINT NULL)")
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = database.ExecContext(t.Context(), "DROP TABLE "+tableName) })
		_, err = database.ExecContext(t.Context(), "INSERT INTO "+tableName+" (id, rank) VALUES (1, 2), (2, NULL), (3, 1), (4, NULL), (5, 2), (6, 3)")
		require.NoError(t, err)
		db, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		profile, err := rasql.EngineProfileFromVersion("postgresql-17", 17, 0, 0)
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		table, err := rasql.ReadTableOf[r5ComboRow](schema.TableDef{Name: tableName, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "rank", Type: schema.IntegerType{}, Nullable: true}}})
		require.NoError(t, err)
		relation, err := rasql.SourceOf(table, "p")
		require.NoError(t, err)
		id, err := rasql.BindColumn[r5ComboRow, int64](relation, "id", "")
		require.NoError(t, err)
		rank, err := rasql.BindNullColumn[r5ComboRow, int64](relation, "rank", "")
		require.NoError(t, err)
		resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}}, rasql.ResultColumn{Name: "rank", Type: schema.IntegerType{}, Nullable: true})
		require.NoError(t, err)
		projection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", id.Expr(), schema.IntegerType{}, ""), rasql.NullItem("rank", rank.NullExpr(), schema.IntegerType{}, "")}, r5ComboDecoder{schema: resultSchema})
		require.NoError(t, err)
		query := rasql.Select(relation.Source(), projection)
		for _, direction := range []rasql.PageDirection{rasql.PageAscending, rasql.PageDescending} {
			for _, nulls := range []rasql.NullOrder{rasql.NullsFirst, rasql.NullsLast} {
				t.Run(fmt.Sprintf("direction-%d-nulls-%d", direction, nulls), func(t *testing.T) {
					var order rasql.PageKey[r5ComboRow]
					if direction == rasql.PageAscending {
						order = rasql.AscNullKey(rank.NullExpr(), func(row r5ComboRow) rasql.Nullable[int64] {
							return rasql.Nullable[int64]{Value: row.Rank.Int64, Valid: row.Rank.Valid}
						}, nulls)
					} else {
						order = rasql.DescNullKey(rank.NullExpr(), func(row r5ComboRow) rasql.Nullable[int64] {
							return rasql.Nullable[int64]{Value: row.Rank.Int64, Valid: row.Rank.Valid}
						}, nulls)
					}
					idKey := rasql.AscKey(id.Expr(), func(row r5ComboRow) int64 { return row.ID })
					spec, specErr := rasql.NewPageSpec([]rasql.PageKey[r5ComboRow]{order, idKey}, idKey)
					require.NoError(t, specErr)
					want := orderedLiveIDs(t, database, tableName, direction, nulls)
					for _, limit := range []int{1, 2} {
						var got []int64
						request := rasql.PageRequest{Limit: limit}
						for {
							page, pageErr := rasql.PageAfter(t.Context(), executor, query, spec, rasql.PagePolicy{DefaultLimit: 2, MaxLimit: 10}, request)
							require.NoError(t, pageErr)
							for _, row := range page.Values {
								got = append(got, row.ID)
							}
							if !page.HasMore {
								break
							}
							request.After = page.Next
						}
						require.Equal(t, want, got)
					}
				})
			}
		}
	})

	t.Run("MySQL refuses an explicit null order before taking a handle", func(t *testing.T) {
		database := dbtest.MySQLDB(t)
		tableName := dbtest.UniqueName(t, "rasql_r5_mysql_nulls")
		_, err := database.ExecContext(t.Context(), "CREATE TABLE "+tableName+" (id BIGINT NOT NULL, sort_value BIGINT NULL)")
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = database.ExecContext(t.Context(), "DROP TABLE "+tableName) })
		db, err := rasql.New(database, dialect.MySQL())
		require.NoError(t, err)
		profile, err := rasql.EngineProfileFromVersion("mysql-8.4", 8, 4, 0)
		require.NoError(t, err)
		base, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		counted := &r5HandleCountingExecutor{Executor: base}
		executor, err := rasql.WithEngineProfile(counted, profile)
		require.NoError(t, err)
		table, err := rasql.ReadTableOf[r5ComboRow](schema.TableDef{Name: tableName, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "sort_value", Type: schema.IntegerType{}, Nullable: true}}})
		require.NoError(t, err)
		relation, err := rasql.SourceOf(table, "p")
		require.NoError(t, err)
		id, err := rasql.BindColumn[r5ComboRow, int64](relation, "id", "")
		require.NoError(t, err)
		rank, err := rasql.BindNullColumn[r5ComboRow, int64](relation, "sort_value", "")
		require.NoError(t, err)
		resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}}, rasql.ResultColumn{Name: "rank", Type: schema.IntegerType{}, Nullable: true})
		require.NoError(t, err)
		projection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", id.Expr(), schema.IntegerType{}, ""), rasql.NullItem("rank", rank.NullExpr(), schema.IntegerType{}, "")}, r5ComboDecoder{schema: resultSchema})
		require.NoError(t, err)
		query := rasql.Select(relation.Source(), projection)
		key := rasql.AscNullKey(rank.NullExpr(), func(row r5ComboRow) rasql.Nullable[int64] {
			return rasql.Nullable[int64]{Value: row.Rank.Int64, Valid: row.Rank.Valid}
		}, rasql.NullsFirst)
		idKey := rasql.AscKey(id.Expr(), func(row r5ComboRow) int64 { return row.ID })
		spec, err := rasql.NewPageSpec([]rasql.PageKey[r5ComboRow]{key, idKey}, idKey)
		require.NoError(t, err)
		_, err = rasql.PageAfter(t.Context(), executor, query, spec, rasql.PagePolicy{DefaultLimit: 2, MaxLimit: 4}, rasql.PageRequest{Limit: 2})
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "unsupported_feature", planErr.Code)
		require.ErrorIs(t, err, rasql.ErrUnsupportedEngineFeature)
		require.Zero(t, counted.queries)
	})

	t.Run("an autocommit mutation differs from an explicit transaction", func(t *testing.T) {
		database := dbtest.PostgreSQLDB(t)
		tableName := dbtest.UniqueName(t, "rasql_r5_pg_mutation")
		_, err := database.ExecContext(t.Context(), "CREATE TABLE "+tableName+" (id BIGINT NOT NULL)")
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = database.ExecContext(t.Context(), "DROP TABLE "+tableName) })
		_, err = database.ExecContext(t.Context(), "INSERT INTO "+tableName+" (id) VALUES (1), (2), (3)")
		require.NoError(t, err)
		makeQuery := func(handle rasql.DB) (rasql.Executor, rasql.Query[r5LiveRow], rasql.PageSpec[r5LiveRow]) {
			db, makeErr := rasql.New(handle.Handle(), dialect.PostgreSQL())
			require.NoError(t, makeErr)
			profile, profileErr := rasql.EngineProfileFromVersion("postgresql-17", 17, 0, 0)
			require.NoError(t, profileErr)
			executor, executorErr := rasql.AsExecutor(db, profile)
			require.NoError(t, executorErr)
			table, tableErr := rasql.ReadTableOf[r5LiveRow](schema.TableDef{Name: tableName, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
			require.NoError(t, tableErr)
			relation, relationErr := rasql.SourceOf(table, "p")
			require.NoError(t, relationErr)
			id, idErr := rasql.BindColumn[r5LiveRow, int64](relation, "id", "")
			require.NoError(t, idErr)
			resultSchema, schemaErr := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
			require.NoError(t, schemaErr)
			projection, projectionErr := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", id.Expr(), schema.IntegerType{}, "")}, r5LiveDecoder{schema: resultSchema})
			require.NoError(t, projectionErr)
			query := rasql.Select(relation.Source(), projection)
			key := rasql.AscKey[r5LiveRow](id.Expr(), func(row r5LiveRow) int64 { return row.ID })
			spec, specErr := rasql.NewPageSpec([]rasql.PageKey[r5LiveRow]{key}, key)
			require.NoError(t, specErr)
			return executor, query, spec
		}
		db, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		executor, query, spec := makeQuery(db)
		first, err := rasql.PageAfter(t.Context(), executor, query, spec, rasql.PagePolicy{DefaultLimit: 1, MaxLimit: 2}, rasql.PageRequest{Limit: 1})
		require.NoError(t, err)
		require.Equal(t, int64(1), first.Values[0].ID)
		_, err = database.ExecContext(t.Context(), "DELETE FROM "+tableName+" WHERE id = 2")
		require.NoError(t, err)
		second, err := rasql.PageAfter(t.Context(), executor, query, spec, rasql.PagePolicy{DefaultLimit: 2, MaxLimit: 2}, rasql.PageRequest{Limit: 2, After: first.Next})
		require.NoError(t, err)
		require.Equal(t, []int64{3}, rowIDs(second.Values))
		err = rasql.Within(t.Context(), executor, &sql.TxOptions{Isolation: sql.LevelRepeatableRead}, func(ctx context.Context, scoped rasql.Executor) error {
			txFirst, scopeErr := rasql.PageAfter(ctx, scoped, query, spec, rasql.PagePolicy{DefaultLimit: 1, MaxLimit: 2}, rasql.PageRequest{Limit: 1})
			if scopeErr != nil {
				return scopeErr
			}
			if _, scopeErr = database.ExecContext(ctx, "INSERT INTO "+tableName+" (id) VALUES (4)"); scopeErr != nil {
				return scopeErr
			}
			txSecond, scopeErr := rasql.PageAfter(ctx, scoped, query, spec, rasql.PagePolicy{DefaultLimit: 10, MaxLimit: 10}, rasql.PageRequest{Limit: 10, After: txFirst.Next})
			if scopeErr != nil {
				return scopeErr
			}
			require.Equal(t, []int64{3}, rowIDs(txSecond.Values))
			return nil
		})
		require.NoError(t, err)
	})

	t.Run("live PostgreSQL 17 and MySQL 8.4", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			open    func(*testing.T) *sql.DB
			dialect dialect.Dialect
			profile string
		}{
			{name: "postgresql-17", open: dbtest.PostgreSQLDB, dialect: dialect.PostgreSQL(), profile: "postgresql-17"},
			{name: "mysql-8.4", open: dbtest.MySQLDB, dialect: dialect.MySQL(), profile: "mysql-8.4"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				database := tc.open(t)
				tableName := dbtest.UniqueName(t, "rasql_r5_page")
				_, err := database.ExecContext(t.Context(), "CREATE TABLE "+tableName+" (id BIGINT NOT NULL)")
				require.NoError(t, err)
				t.Cleanup(func() { _, _ = database.ExecContext(t.Context(), "DROP TABLE "+tableName) })
				_, err = database.ExecContext(t.Context(), "INSERT INTO "+tableName+" (id) VALUES (1),(2),(3),(4),(5)")
				require.NoError(t, err)
				db, err := rasql.New(database, tc.dialect)
				require.NoError(t, err)
				profile, err := rasql.EngineProfileFromVersion(tc.profile, map[string]int{"postgresql-17": 17, "mysql-8.4": 8}[tc.profile], map[string]int{"postgresql-17": 0, "mysql-8.4": 4}[tc.profile], 0)
				require.NoError(t, err)
				executor, err := rasql.AsExecutor(db, profile)
				require.NoError(t, err)
				table, err := rasql.ReadTableOf[r5LiveRow](schema.TableDef{Name: tableName, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
				require.NoError(t, err)
				relation, err := rasql.SourceOf(table, "p")
				require.NoError(t, err)
				id, err := rasql.BindColumn[r5LiveRow, int64](relation, "id", "")
				require.NoError(t, err)
				resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
				require.NoError(t, err)
				projection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", id.Expr(), schema.IntegerType{}, "")}, r5LiveDecoder{schema: resultSchema})
				require.NoError(t, err)
				query := rasql.Select(relation.Source(), projection)
				key := rasql.AscKey[r5LiveRow](id.Expr(), func(row r5LiveRow) int64 { return row.ID })
				spec, err := rasql.NewPageSpec([]rasql.PageKey[r5LiveRow]{key}, key)
				require.NoError(t, err)
				request := rasql.PageRequest{Limit: 2}
				var got []int64
				for {
					page, pageErr := rasql.PageAfter(t.Context(), executor, query, spec, rasql.PagePolicy{DefaultLimit: 2, MaxLimit: 10}, request)
					require.NoError(t, pageErr)
					for _, row := range page.Values {
						got = append(got, row.ID)
					}
					if !page.HasMore {
						break
					}
					request.After = page.Next
				}
				require.Equal(t, []int64{1, 2, 3, 4, 5}, got)
			})
		}
	})
}

func orderedLiveIDs(t *testing.T, database *sql.DB, tableName string, direction rasql.PageDirection, nulls rasql.NullOrder) []int64 {
	t.Helper()
	directionSQL := "ASC"
	if direction == rasql.PageDescending {
		directionSQL = "DESC"
	}
	nullsSQL := "FIRST"
	if nulls == rasql.NullsLast {
		nullsSQL = "LAST"
	}
	rows, err := database.QueryContext(t.Context(), "SELECT id FROM "+tableName+" ORDER BY rank "+directionSQL+" NULLS "+nullsSQL+", id ASC")
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var result []int64
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		result = append(result, id)
	}
	require.NoError(t, rows.Err())
	return result
}

type r5HandleCountingExecutor struct {
	rasql.Executor
	queries int
}

func (e *r5HandleCountingExecutor) Query(ctx context.Context, statement stmt.Statement) (rasql.ResultRows, error) {
	e.queries++
	return e.Executor.Query(ctx, statement)
}

func rowIDs(rows []r5LiveRow) []int64 {
	result := make([]int64, len(rows))
	for i, row := range rows {
		result[i] = row.ID
	}
	return result
}

type r5LiveRow struct{ ID int64 }
type r5LiveDecoder struct{ schema rasql.ResultSchema }

func (d r5LiveDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (r5LiveDecoder) Presence() []rasql.Presence         { return nil }
func (d r5LiveDecoder) DecodeRow(source rasql.ScanSource, row *r5LiveRow) error {
	return source.Scan(&row.ID)
}
