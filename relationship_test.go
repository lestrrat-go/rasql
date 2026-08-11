package rasql_test

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type relationshipUser struct {
	ID int64
}

type relationshipOrder struct {
	ID     int64
	UserID int64
}

type unsignedRelationshipUser struct {
	ID uint64
}

type unsignedRelationshipOrder struct {
	ID     uint64
	UserID uint64
}

func TestLoadHasManyGroupsRowsByParentKey(t *testing.T) {
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
	userID, err := orders.Column("user_id")
	require.NoError(t, err)

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

func TestLoadBelongsToGroupsRowsByForeignKey(t *testing.T) {
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
	userID, err := users.Column("id")
	require.NoError(t, err)

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

func TestLoadRelationshipsSupportsMySQLUnsignedKeys(t *testing.T) {
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
	userID, err := users.Column("id")
	require.NoError(t, err)
	orderUserID, err := orders.Column("user_id")
	require.NoError(t, err)

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

func TestLoadRelationshipsSkipsEmptyInput(t *testing.T) {
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
	userID, err := users.Column("id")
	require.NoError(t, err)
	orderUserID, err := orders.Column("user_id")
	require.NoError(t, err)

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
