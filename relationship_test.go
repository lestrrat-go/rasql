package rasql_test

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/exec"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestLoadHasManyPlanUsesPinnedSQLiteLimitAndScopedQueryCount(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	conn, err := database.Conn(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	_, err = sqlite.Limit(conn, sqlite3.SQLITE_LIMIT_VARIABLE_NUMBER, 2)
	require.NoError(t, err)
	_, err = conn.ExecContext(context.Background(), "CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER, active INTEGER, created_at INTEGER)")
	require.NoError(t, err)
	_, err = conn.ExecContext(context.Background(), "INSERT INTO orders VALUES (1,1,1,1),(2,1,1,2),(3,2,1,3),(4,3,1,4)")
	require.NoError(t, err)
	queries := 0
	db, err := rasql.New(conn, dialect.SQLite(), exec.HookFunc{BeforeFunc: func(_ context.Context, operation exec.Operation) error {
		if operation.Kind() == exec.QueryOperation {
			queries++
		}
		return nil
	}}, rasql.WithRelationshipBindLimit(2))
	require.NoError(t, err)
	orders, err := rasql.TableOf[planOrder](schema.TableDef{Name: "orders", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "user_id", Type: schema.IntegerType{}}, {Name: "active", Type: schema.IntegerType{}}, {Name: "created_at", Type: schema.IntegerType{}}}, PrimaryKey: []string{"id"}})
	require.NoError(t, err)
	other, err := rasql.TableOf[planOrder](schema.TableDef{Name: "other", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}, PrimaryKey: []string{"id"}})
	require.NoError(t, err)
	for name, columns := range map[string][]query.ColumnRef{"empty columns": nil, "wrong table": {other.Column("id")}} {
		t.Run(name, func(t *testing.T) {
			_, err := rasql.LoadHasManyPlan(t.Context(), db, orders, columns, []relationshipUser{{1}}, func(user relationshipUser) int64 { return user.ID }, func(order planOrder) int64 { return order.UserID }, func(key int64) ([]any, bool) { return []any{key}, true }, rasql.RelationshipLoadOptions{})
			require.Error(t, err)
		})
	}
	for name, options := range map[string]rasql.RelationshipLoadOptions{
		"invalid filter": {Where: query.Equal(other.Column("id"), 1)},
		"invalid order":  {OrderBy: []query.Order{query.Asc(other.Column("id"))}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := rasql.LoadHasManyPlan(t.Context(), db, orders, []query.ColumnRef{orders.Column("user_id")}, []relationshipUser{{1}}, func(user relationshipUser) int64 { return user.ID }, func(order planOrder) int64 { return order.UserID }, func(key int64) ([]any, bool) { return []any{key}, true }, options)
			require.Error(t, err)
		})
	}
	_, err = rasql.LoadHasManyPlan(t.Context(), db, orders, []query.ColumnRef{orders.Column("user_id")}, []relationshipUser{{1}}, func(user relationshipUser) int64 { return user.ID }, func(order planOrder) int64 { return order.UserID }, func(key int64) ([]any, bool) { return nil, true }, rasql.RelationshipLoadOptions{})
	require.Error(t, err)
	loaded, err := rasql.LoadHasManyPlan(context.Background(), db, orders, []query.ColumnRef{orders.Column("user_id")}, []relationshipUser{{ID: 1}, {ID: 1}, {ID: 2}, {ID: 3}}, func(user relationshipUser) int64 { return user.ID }, func(order planOrder) int64 { return order.UserID }, func(key int64) ([]any, bool) { return []any{key}, true }, rasql.RelationshipLoadOptions{BindLimit: 2})
	require.NoError(t, err)
	require.Len(t, loaded[1], 2)
	require.Len(t, loaded[2], 1)
	require.Len(t, loaded[3], 1)
	require.Equal(t, 2, queries)
}

type compositeRelationshipUser struct {
	TenantID int64
	ID       int64
}

type compositeRelationshipOrder struct {
	ID       int64
	TenantID int64
	UserID   int64
}

func TestLoadHasManyPlanCompositeIsolationAndDescriptorOrder(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.Exec("CREATE TABLE orders (id INTEGER PRIMARY KEY, tenant_id INTEGER, user_id INTEGER)")
	require.NoError(t, err)
	_, err = database.Exec("INSERT INTO orders VALUES (1,7,1),(2,8,1),(3,7,2)")
	require.NoError(t, err)
	var bound [][]any
	db, err := rasql.New(database, dialect.SQLite(), exec.HookFunc{BeforeFunc: func(_ context.Context, operation exec.Operation) error {
		if operation.Kind() == exec.QueryOperation {
			bound = append(bound, operation.Args())
		}
		return nil
	}})
	require.NoError(t, err)
	orders, err := rasql.TableOf[compositeRelationshipOrder](schema.TableDef{Name: "orders", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "tenant_id", Type: schema.IntegerType{}}, {Name: "user_id", Type: schema.IntegerType{}}}, PrimaryKey: []string{"id"}})
	require.NoError(t, err)
	loaded, err := rasql.LoadHasManyPlan(context.Background(), db, orders, []query.ColumnRef{orders.Column("tenant_id"), orders.Column("user_id")}, []compositeRelationshipUser{{7, 1}, {8, 1}, {7, 2}}, func(user compositeRelationshipUser) string { return fmt.Sprintf("%d:%d", user.TenantID, user.ID) }, func(order compositeRelationshipOrder) string {
		return fmt.Sprintf("%d:%d", order.TenantID, order.UserID)
	}, func(key string) ([]any, bool) {
		var tenant, id int64
		_, err := fmt.Sscanf(key, "%d:%d", &tenant, &id)
		return []any{tenant, id}, err == nil
	}, rasql.RelationshipLoadOptions{BindLimit: 4})
	require.NoError(t, err)
	require.Len(t, loaded["7:1"], 1)
	require.Len(t, loaded["8:1"], 1)
	require.Len(t, loaded["7:2"], 1)
	require.Equal(t, [][]any{{int64(7), int64(1), int64(8), int64(1)}, {int64(7), int64(2)}}, bound)
}

func TestLoadHasManyPlanCompositeRenderingByDialect(t *testing.T) {
	for _, test := range []struct {
		name    string
		dialect dialect.Dialect
	}{
		{name: "postgresql", dialect: dialect.PostgreSQL()},
		{name: "mysql", dialect: dialect.MySQL()},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() {
				mock.ExpectClose()
				require.NoError(t, database.Close())
				require.NoError(t, mock.ExpectationsWereMet())
			})
			var sqlText string
			db, err := rasql.New(database, test.dialect, exec.HookFunc{BeforeFunc: func(_ context.Context, operation exec.Operation) error { sqlText = operation.SQL(); return nil }})
			require.NoError(t, err)
			orders, err := rasql.TableOf[compositeRelationshipOrder](schema.TableDef{Name: "orders", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "tenant_id", Type: schema.IntegerType{}}, {Name: "user_id", Type: schema.IntegerType{}}}})
			require.NoError(t, err)
			mock.ExpectQuery("SELECT").WithArgs(int64(7), int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "user_id"}).AddRow(1, 7, 1))
			_, err = rasql.LoadHasManyPlan(t.Context(), db, orders, []query.ColumnRef{orders.Column("tenant_id"), orders.Column("user_id")}, []compositeRelationshipUser{{7, 1}}, func(user compositeRelationshipUser) string { return fmt.Sprintf("%d:%d", user.TenantID, user.ID) }, func(order compositeRelationshipOrder) string {
				return fmt.Sprintf("%d:%d", order.TenantID, order.UserID)
			}, func(key string) ([]any, bool) { return []any{int64(7), int64(1)}, true }, rasql.RelationshipLoadOptions{BindLimit: 2})
			require.NoError(t, err)
			require.Contains(t, sqlText, "tenant_id")
			require.Less(t, strings.Index(sqlText, "tenant_id"), strings.Index(sqlText, "user_id"))
		})
	}
}

func TestLoadHasManyPlanCompositeHighBitUnsignedComponent(t *testing.T) {
	type key struct{ Tenant, Parent uint64 }
	type row struct {
		ID     int64
		Tenant string
		Parent uint64
	}
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.Exec("CREATE TABLE rows (id INTEGER, tenant TEXT, parent INTEGER); INSERT INTO rows VALUES (1, '9223372036854775815', 1)")
	require.NoError(t, err)
	var bound [][]any
	db, err := rasql.New(database, dialect.SQLite(), exec.HookFunc{BeforeFunc: func(_ context.Context, operation exec.Operation) error {
		if operation.Kind() == exec.QueryOperation {
			bound = append(bound, operation.Args())
		}
		return nil
	}})
	require.NoError(t, err)
	table, err := rasql.TableOf[row](schema.TableDef{Name: "rows", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "tenant", Type: schema.TextType{}}, {Name: "parent", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	keyValue := uint64(1<<63) + 7
	loaded, err := rasql.LoadHasManyPlan(t.Context(), db, table, []query.ColumnRef{table.Column("tenant"), table.Column("parent")}, []key{{Tenant: keyValue, Parent: 1}}, func(value key) key { return value }, func(value row) key {
		tenant, _ := strconv.ParseUint(value.Tenant, 10, 64)
		return key{Tenant: tenant, Parent: value.Parent}
	}, func(value key) ([]any, bool) { return []any{value.Tenant, value.Parent}, true }, rasql.RelationshipLoadOptions{BindLimit: 2})
	require.NoError(t, err)
	require.Len(t, loaded[key{Tenant: keyValue, Parent: 1}], 1)
	require.Equal(t, [][]any{{"9223372036854775815", uint64(1)}}, bound)
}

func TestLoadHasManyPlanBatchesAndCapsSQLite(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	database.SetMaxOpenConns(1)
	_, err = database.Exec("CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER, active INTEGER, created_at INTEGER); INSERT INTO orders VALUES (1,1,1,1),(2,1,1,2),(3,1,1,3),(4,1,1,4),(5,1,1,5),(6,1,1,6),(7,2,1,1)")
	require.NoError(t, err)
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	orders, err := rasql.TableOf[planOrder](schema.TableDef{Name: "orders", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "user_id", Type: schema.IntegerType{}}, {Name: "active", Type: schema.IntegerType{}}, {Name: "created_at", Type: schema.IntegerType{}}}, PrimaryKey: []string{"id"}})
	require.NoError(t, err)
	loaded, err := rasql.LoadHasManyPlan(t.Context(), db, orders, []query.ColumnRef{orders.Column("user_id")}, []relationshipUser{{ID: 1}, {ID: 1}, {ID: 2}, {ID: 3}}, func(user relationshipUser) int64 { return user.ID }, func(order planOrder) int64 { return order.UserID }, func(key int64) ([]any, bool) { return []any{key}, true }, rasql.RelationshipLoadOptions{Where: query.Equal(orders.Column("active"), 1), OrderBy: []query.Order{query.Desc(orders.Column("created_at"))}, PerParentLimit: 5, BindLimit: 2})
	require.NoError(t, err)
	require.Len(t, loaded[1], 5)
	require.Equal(t, []int64{6, 5, 4, 3, 2}, []int64{loaded[1][0].ID, loaded[1][1].ID, loaded[1][2].ID, loaded[1][3].ID, loaded[1][4].ID})
	require.Len(t, loaded[2], 1)
	require.Empty(t, loaded[3])
}

type relationshipUser struct {
	ID int64
}

type relationshipOrder struct {
	ID     int64
	UserID int64
}

type planOrder struct{ ID, UserID, Active, CreatedAt int64 }

type unsignedRelationshipUser struct {
	ID uint64
}

type unsignedRelationshipOrder struct {
	ID     uint64
	UserID uint64
}

type belongsToParent struct{ ID int64 }
type belongsToChild struct{ ParentID int64 }

func TestLoadBelongsToPlanMissingAndDuplicateKeys(t *testing.T) {
	newDB := func(t *testing.T, rows string) (rasql.DB, rasql.Table[belongsToParent]) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		_, err = database.Exec("CREATE TABLE parents (id INTEGER); INSERT INTO parents VALUES " + rows)
		require.NoError(t, err)
		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		parents, err := rasql.TableOf[belongsToParent](schema.TableDef{Name: "parents", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
		require.NoError(t, err)
		return db, parents
	}
	t.Run("missing and nonexistent are omitted", func(t *testing.T) {
		db, parents := newDB(t, "(1),(2)")
		loaded, err := rasql.LoadBelongsToPlan(t.Context(), db, parents, []query.ColumnRef{parents.Column("id")}, []belongsToChild{{1}, {0}, {99}}, func(child belongsToChild) int64 { return child.ParentID }, func(parent belongsToParent) int64 { return parent.ID }, func(key int64) ([]any, bool) { return []any{key}, key != 0 }, rasql.RelationshipLoadOptions{})
		require.NoError(t, err)
		require.Contains(t, loaded, int64(1))
		require.NotContains(t, loaded, int64(0))
		require.NotContains(t, loaded, int64(99))
	})
	t.Run("duplicate scalar parent errors", func(t *testing.T) {
		db, parents := newDB(t, "(1),(1)")
		_, err := rasql.LoadBelongsToPlan(t.Context(), db, parents, []query.ColumnRef{parents.Column("id")}, []belongsToChild{{1}}, func(child belongsToChild) int64 { return child.ParentID }, func(parent belongsToParent) int64 { return parent.ID }, func(key int64) ([]any, bool) { return []any{key}, true }, rasql.RelationshipLoadOptions{})
		require.ErrorContains(t, err, "duplicate parent")
	})
	t.Run("all missing performs zero queries", func(t *testing.T) {
		db, parents := newDB(t, "(1)")
		queries := 0
		db, err := db.WithHooks(exec.HookFunc{BeforeFunc: func(_ context.Context, operation exec.Operation) error {
			if operation.Kind() == exec.QueryOperation {
				queries++
			}
			return nil
		}})
		require.NoError(t, err)
		loaded, err := rasql.LoadBelongsToPlan(t.Context(), db, parents, []query.ColumnRef{parents.Column("id")}, []belongsToChild{{0}}, func(child belongsToChild) int64 { return child.ParentID }, func(parent belongsToParent) int64 { return parent.ID }, func(key int64) ([]any, bool) { return nil, false }, rasql.RelationshipLoadOptions{})
		require.NoError(t, err)
		require.Empty(t, loaded)
		require.Zero(t, queries)
	})
}

func TestRelationshipPlanValidationAndBindPrecedence(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db, err := rasql.New(database, dialect.PostgreSQL(), rasql.WithRelationshipBindLimit(10))
	require.NoError(t, err)
	orders, err := rasql.TableOf[planOrder](schema.TableDef{Name: "orders", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "user_id", Type: schema.IntegerType{}}, {Name: "active", Type: schema.IntegerType{}}, {Name: "created_at", Type: schema.IntegerType{}}}, PrimaryKey: []string{"id"}})
	require.NoError(t, err)
	for name, options := range map[string]rasql.RelationshipLoadOptions{
		"negative limit":     {BindLimit: -1},
		"negative cap":       {PerParentLimit: -1},
		"one key cannot fit": {BindLimit: 1, Where: query.Equal(orders.Column("active"), 1)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := rasql.LoadHasManyPlan(t.Context(), db, orders, []query.ColumnRef{orders.Column("user_id")}, []relationshipUser{{1}}, func(user relationshipUser) int64 { return user.ID }, func(order planOrder) int64 { return order.UserID }, func(key int64) ([]any, bool) { return []any{key}, true }, options)
			require.Error(t, err)
		})
	}
	for _, key := range []int64{1, 2, 3} {
		mock.ExpectQuery("SELECT").WithArgs(key).WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "active", "created_at"}))
	}
	_, err = rasql.LoadHasManyPlan(t.Context(), db, orders, []query.ColumnRef{orders.Column("user_id")}, []relationshipUser{{1}, {2}, {3}}, func(user relationshipUser) int64 { return user.ID }, func(order planOrder) int64 { return order.UserID }, func(key int64) ([]any, bool) { return []any{key}, true }, rasql.RelationshipLoadOptions{BindLimit: 1})
	require.NoError(t, err)
}

func TestLoadRelationships(t *testing.T) {
	t.Run("has many groups rows by parent key", testLoadHasManyGroupsRowsByParentKey)
	t.Run("belongs to groups rows by foreign key", testLoadBelongsToGroupsRowsByForeignKey)
	t.Run("supports MySQL unsigned keys", testLoadRelationshipsSupportsMySQLUnsignedKeys)
	t.Run("skips empty input", testLoadRelationshipsSkipsEmptyInput)
}

func testLoadHasManyGroupsRowsByParentKey(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	_, err = rasql.TableOf[relationshipUser](schema.TableDef{
		Name:       "users",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	orders, err := rasql.TableOf[relationshipOrder](schema.TableDef{
		Name:       "orders",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "user_id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	userID := orders.Column("user_id")

	mock.ExpectQuery(`SELECT "orders"."id", "orders"."user_id" FROM "orders" WHERE ("orders"."user_id" IN ($1, $2))`).
		WithArgs(int64(1), int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id"}).AddRow(int64(10), int64(1)).AddRow(int64(11), int64(1)).AddRow(int64(20), int64(2)))

	loaded, err := rasql.LoadHasMany(t.Context(), db, orders, userID,
		[]relationshipUser{{ID: 1}, {ID: 2}, {ID: 1}},
		func(user relationshipUser) int64 { return user.ID },
		func(order relationshipOrder) int64 { return order.UserID },
	)
	require.NoError(t, err)
	require.Equal(t, map[int64][]relationshipOrder{
		1: {{ID: 10, UserID: 1}, {ID: 11, UserID: 1}},
		2: {{ID: 20, UserID: 2}},
	}, loaded)
}

func testLoadBelongsToGroupsRowsByForeignKey(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	users, err := rasql.TableOf[relationshipUser](schema.TableDef{
		Name:       "users",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	userID := users.Column("id")

	mock.ExpectQuery(`SELECT "users"."id" FROM "users" WHERE ("users"."id" IN ($1, $2))`).
		WithArgs(int64(1), int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(1)).AddRow(int64(2)))

	loaded, err := rasql.LoadBelongsTo(t.Context(), db, users, userID,
		[]relationshipOrder{{ID: 10, UserID: 1}, {ID: 11, UserID: 1}, {ID: 20, UserID: 2}},
		func(order relationshipOrder) int64 { return order.UserID },
		func(user relationshipUser) int64 { return user.ID },
	)
	require.NoError(t, err)
	require.Equal(t, map[int64]relationshipUser{1: {ID: 1}, 2: {ID: 2}}, loaded)
}

func testLoadRelationshipsSupportsMySQLUnsignedKeys(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.MySQL())
	require.NoError(t, err)
	users, err := rasql.TableOf[unsignedRelationshipUser](schema.TableDef{
		Name:       "users",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{Unsigned: true}}},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	orders, err := rasql.TableOf[unsignedRelationshipOrder](schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{Unsigned: true}},
			{Name: "user_id", Type: schema.IntegerType{Unsigned: true}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	userID := users.Column("id")
	orderUserID := orders.Column("user_id")

	const key = uint64(1 << 63)
	const keyText = "9223372036854775808"
	mock.ExpectQuery("SELECT `orders`.`id`, `orders`.`user_id` FROM `orders` WHERE (`orders`.`user_id` IN (?))").
		WithArgs(keyText).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id"}))

	hasMany, err := rasql.LoadHasMany(t.Context(), db, orders, orderUserID,
		[]unsignedRelationshipUser{{ID: key}},
		func(user unsignedRelationshipUser) uint64 { return user.ID },
		func(order unsignedRelationshipOrder) uint64 { return order.UserID },
	)
	require.NoError(t, err)
	require.Equal(t, map[uint64][]unsignedRelationshipOrder{
		key: nil,
	}, hasMany)

	mock.ExpectQuery("SELECT `users`.`id` FROM `users` WHERE (`users`.`id` IN (?))").
		WithArgs(keyText).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	belongsTo, err := rasql.LoadBelongsTo(t.Context(), db, users, userID,
		[]unsignedRelationshipOrder{{ID: 10, UserID: key}},
		func(order unsignedRelationshipOrder) uint64 { return order.UserID },
		func(user unsignedRelationshipUser) uint64 { return user.ID },
	)
	require.NoError(t, err)
	require.Empty(t, belongsTo)
}

func testLoadRelationshipsSkipsEmptyInput(t *testing.T) {
	users, err := rasql.TableOf[relationshipUser](schema.TableDef{
		Name:       "users",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	orders, err := rasql.TableOf[relationshipOrder](schema.TableDef{
		Name:       "orders",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "user_id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	userID := users.Column("id")
	orderUserID := orders.Column("user_id")

	// A zero DB proves the empty input short-circuits before either loader
	// looks at the database at all.
	hasMany, err := rasql.LoadHasMany[relationshipUser, relationshipOrder, int64](
		t.Context(), rasql.DB{}, orders, orderUserID, nil,
		func(user relationshipUser) int64 { return user.ID },
		func(order relationshipOrder) int64 { return order.UserID },
	)
	require.NoError(t, err)
	require.Empty(t, hasMany)

	belongsTo, err := rasql.LoadBelongsTo[relationshipOrder, relationshipUser, int64](
		t.Context(), rasql.DB{}, users, userID, nil,
		func(order relationshipOrder) int64 { return order.UserID },
		func(user relationshipUser) int64 { return user.ID },
	)
	require.NoError(t, err)
	require.Empty(t, belongsTo)
}
