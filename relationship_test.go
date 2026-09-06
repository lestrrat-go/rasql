package rasql_test

import (
	"context"
	"database/sql"
	"fmt"
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
	db, err := rasql.New(database, dialect.SQLite())
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
	require.Equal(t, int64(6), loaded[1][0].ID)
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
