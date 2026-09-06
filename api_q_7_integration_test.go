//go:build unix

package rasql_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type apiQ7User struct {
	ID int64 `rasql:"id"`
}

type apiQ7Order struct {
	ID     int64 `rasql:"id"`
	UserID int64 `rasql:"user_id"`
	Amount int64 `rasql:"amount"`
}

type apiQ7Result struct {
	ID    int64 `rasql:"id"`
	Value int64 `rasql:"value"`
}

func TestSQLiteCorrelatedProjectionConstructorsDecode(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	database.SetMaxOpenConns(1)
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	users, err := rasql.TableOf[apiQ7User](schema.TableDef{
		Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}, PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	orders, err := rasql.TableOf[apiQ7Order](schema.TableDef{
		Name: "orders", Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}}, {Name: "user_id", Type: schema.IntegerType{}}, {Name: "amount", Type: schema.IntegerType{}},
		}, PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	require.NoError(t, rasql.CreateTable(t.Context(), db, users))
	require.NoError(t, rasql.CreateTable(t.Context(), db, orders))
	for _, user := range []apiQ7User{{ID: 1}, {ID: 2}} {
		_, err = rasql.Insert(t.Context(), db, users, user)
		require.NoError(t, err)
	}
	for _, order := range []apiQ7Order{{ID: 1, UserID: 1, Amount: 1}, {ID: 2, UserID: 2, Amount: 2}} {
		_, err = rasql.Insert(t.Context(), db, orders, order)
		require.NoError(t, err)
	}

	inner, err := query.NewCorrelatedSelect(
		orders.Ref(), []query.TableRef{users.Ref()},
		query.Project(query.Coalesce(orders.Ref().Column("amount"), users.Ref().Column("id"))).As("value"),
	)
	require.NoError(t, err)
	inner, err = inner.WithWhere(query.Equal(orders.Ref().Column("user_id"), users.Ref().Column("id")))
	require.NoError(t, err)
	rows, err := rasql.DecodeFrom[apiQ7Result](users).
		Project(users.Ref().Column("id"), query.Project(query.Scalar(inner)).As("value")).
		Order(query.Asc(users.Ref().Column("id"))).
		All(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, []apiQ7Result{{ID: 1, Value: 1}, {ID: 2, Value: 2}}, rows)

	leaf, err := query.NewCorrelatedSelect(
		orders.Ref(), []query.TableRef{users.Ref()}, query.Project(orders.Ref().Column("amount")),
	)
	require.NoError(t, err)
	leaf, err = leaf.WithWhere(query.Equal(orders.Ref().Column("user_id"), users.Ref().Column("id")))
	require.NoError(t, err)
	middle, err := query.NewCorrelatedSelect(
		orders.Ref(), []query.TableRef{users.Ref()}, query.Project(query.Scalar(leaf)).As("value"),
	)
	require.NoError(t, err)
	middle, err = middle.WithWhere(query.Equal(orders.Ref().Column("user_id"), users.Ref().Column("id")))
	require.NoError(t, err)
	rows, err = rasql.DecodeFrom[apiQ7Result](users).
		Project(users.Ref().Column("id"), query.Project(query.Scalar(middle)).As("value")).
		Order(query.Asc(users.Ref().Column("id"))).
		All(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, []apiQ7Result{{ID: 1, Value: 1}, {ID: 2, Value: 2}}, rows)
}
