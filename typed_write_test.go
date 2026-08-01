package rasql_test

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestInsertExecutesTypedRow(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	client, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table := schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	}
	type user struct {
		ID    int64  `rasql:"id"`
		Email string `rasql:"email"`
	}
	users, err := rasql.NewTable[user](table)
	require.NoError(t, err)
	mock.ExpectExec("INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2)").
		WithArgs(int64(42), "ada@example.com").
		WillReturnResult(sqlmock.NewResult(1, 1))

	result, err := rasql.Insert(t.Context(), client, users, user{ID: 42, Email: "ada@example.com"})
	require.NoError(t, err)
	rows, err := result.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)
}

func TestUpdateExecutesTypedRow(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	client, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table := schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	}
	type user struct {
		ID    int64  `rasql:"id"`
		Email string `rasql:"email"`
	}
	users, err := rasql.NewTable[user](table)
	require.NoError(t, err)
	mock.ExpectExec("UPDATE \"users\" SET \"email\" = $1 WHERE (\"users\".\"id\" = $2)").
		WithArgs("grace@example.com", int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	result, err := rasql.Update(t.Context(), client, users, user{ID: 42, Email: "grace@example.com"})
	require.NoError(t, err)
	rows, err := result.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)
}

func TestUpdateRejectsTableWithoutPrimaryKey(t *testing.T) {
	type user struct {
		ID    int64  `rasql:"id"`
		Email string `rasql:"email"`
	}
	table := schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
	}
	users, err := rasql.NewTable[user](table)
	require.NoError(t, err)

	_, err = rasql.Update(t.Context(), rasql.Client{}, users, user{ID: 42, Email: "grace@example.com"})
	require.ErrorContains(t, err, "has no primary key")
}

func TestUpdateMatchesCompositePrimaryKey(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	client, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table := schema.Table{
		Name: "memberships",
		Columns: []schema.Column{
			{Name: "account_id", Type: schema.TypeInteger},
			{Name: "user_id", Type: schema.TypeInteger},
			{Name: "role", Type: schema.TypeText},
		},
		PrimaryKey: []string{"account_id", "user_id"},
	}
	type membership struct {
		AccountID int64  `rasql:"account_id"`
		UserID    int64  `rasql:"user_id"`
		Role      string `rasql:"role"`
	}
	memberships, err := rasql.NewTable[membership](table)
	require.NoError(t, err)
	mock.ExpectExec("UPDATE \"memberships\" SET \"role\" = $1 WHERE ((\"memberships\".\"account_id\" = $2) AND (\"memberships\".\"user_id\" = $3))").
		WithArgs("admin", int64(10), int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	_, err = rasql.Update(t.Context(), client, memberships, membership{AccountID: 10, UserID: 42, Role: "admin"})
	require.NoError(t, err)
}

// mappedUser supplies its own column values and carries a tag naming a
// different column, so a write through the tag is distinguishable from one
// through the method.
type mappedUser struct {
	ID    int64  `rasql:"email"`
	Email string `rasql:"id"`
}

func (u mappedUser) ColumnValue(name string) (any, bool) {
	switch name {
	case "id":
		return u.ID, true
	case "email":
		return u.Email, true
	}
	return nil, false
}

// wrappedUser embeds a row type that supplies its own column values and tags no
// field of its own, so only the promoted ColumnValue can map it.
type wrappedUser struct {
	mappedUser
}

// taggedWrapperUser embeds the same row type and tags fields of its own, so the
// promoted ColumnValue and the tags name different values for every column.
type taggedWrapperUser struct {
	mappedUser
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
}

// declaringWrapperUser embeds the same row type, tags fields of its own, and
// declares ColumnValue as well. Its tags are crossed and its embedded fields
// hold values of their own, so the embedded mapping, the tag mapping, and the
// declared method each name a different value for every column. Go dispatches to
// the declared method, so the writes must follow it.
type declaringWrapperUser struct {
	mappedUser
	ID    int64  `rasql:"email"`
	Email string `rasql:"id"`
}

func (u declaringWrapperUser) ColumnValue(name string) (any, bool) {
	switch name {
	case "id":
		return u.ID, true
	case "email":
		return u.Email, true
	}
	return nil, false
}

// pointerDeclaringWrapperUser is the same shape with a pointer receiver, which
// is the other place a row type can declare its mapping method.
type pointerDeclaringWrapperUser struct {
	mappedUser
	ID    int64  `rasql:"email"`
	Email string `rasql:"id"`
}

func (u *pointerDeclaringWrapperUser) ColumnValue(name string) (any, bool) {
	switch name {
	case "id":
		return u.ID, true
	case "email":
		return u.Email, true
	}
	return nil, false
}

// partiallyMappedUser omits one column of its table.
type partiallyMappedUser struct {
	ID int64
}

func (u partiallyMappedUser) ColumnValue(name string) (any, bool) {
	if name == "id" {
		return u.ID, true
	}
	return nil, false
}

func TestWritesUseColumnValuer(t *testing.T) {
	table := schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	}

	t.Run("insert", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		client, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		users, err := rasql.NewTable[mappedUser](table)
		require.NoError(t, err)
		// The tags are crossed, so these arguments prove ColumnValue supplied them.
		mock.ExpectExec("INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2)").
			WithArgs(int64(42), "ada@example.com").
			WillReturnResult(sqlmock.NewResult(1, 1))

		_, err = rasql.Insert(t.Context(), client, users, mappedUser{ID: 42, Email: "ada@example.com"})
		require.NoError(t, err)
	})

	t.Run("update", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		client, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		users, err := rasql.NewTable[mappedUser](table)
		require.NoError(t, err)
		mock.ExpectExec("UPDATE \"users\" SET \"email\" = $1 WHERE (\"users\".\"id\" = $2)").
			WithArgs("grace@example.com", int64(42)).
			WillReturnResult(sqlmock.NewResult(0, 1))

		_, err = rasql.Update(t.Context(), client, users, mappedUser{ID: 42, Email: "grace@example.com"})
		require.NoError(t, err)
	})

	t.Run("embedded without tags of its own", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		client, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		users, err := rasql.NewTable[wrappedUser](table)
		require.NoError(t, err)
		// The wrapper tags nothing, so these arguments prove the promoted
		// ColumnValue supplied them rather than the tag path failing.
		mock.ExpectExec("INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2)").
			WithArgs(int64(42), "ada@example.com").
			WillReturnResult(sqlmock.NewResult(1, 1))

		_, err = rasql.Insert(t.Context(), client, users, wrappedUser{mappedUser{ID: 42, Email: "ada@example.com"}})
		require.NoError(t, err)
	})

	t.Run("missing column", func(t *testing.T) {
		users, err := rasql.NewTable[partiallyMappedUser](table)
		require.NoError(t, err)

		_, err = rasql.Insert(t.Context(), rasql.Client{}, users, partiallyMappedUser{ID: 42})
		require.ErrorContains(t, err, "supplies no value for column \"email\"")
	})
}

func TestWritesIgnorePromotedColumnValuer(t *testing.T) {
	table := schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	}
	// The embedded values differ from the outer tagged ones, so the arguments
	// below name which mapping ran.
	value := taggedWrapperUser{
		mappedUser: mappedUser{ID: 7, Email: "embedded@example.com"},
		ID:         42,
		Email:      "ada@example.com",
	}

	t.Run("insert", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		client, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		users, err := rasql.NewTable[taggedWrapperUser](table)
		require.NoError(t, err)
		mock.ExpectExec("INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2)").
			WithArgs(int64(42), "ada@example.com").
			WillReturnResult(sqlmock.NewResult(1, 1))

		_, err = rasql.Insert(t.Context(), client, users, value)
		require.NoError(t, err)
	})

	t.Run("update", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		client, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		users, err := rasql.NewTable[taggedWrapperUser](table)
		require.NoError(t, err)
		mock.ExpectExec("UPDATE \"users\" SET \"email\" = $1 WHERE (\"users\".\"id\" = $2)").
			WithArgs("ada@example.com", int64(42)).
			WillReturnResult(sqlmock.NewResult(0, 1))

		_, err = rasql.Update(t.Context(), client, users, value)
		require.NoError(t, err)
	})
}

func TestWritesFollowDeclaredColumnValuer(t *testing.T) {
	table := schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	}
	// The embedded values, the crossed tags, and the declared method each name a
	// different value for both columns, so the arguments below name which mapping
	// ran: the declared method binds 42 and method@example.com, the tags bind
	// method@example.com and 42, and the promoted method binds 7 and
	// embedded@example.com.
	value := declaringWrapperUser{
		mappedUser: mappedUser{ID: 7, Email: "embedded@example.com"},
		ID:         42,
		Email:      "method@example.com",
	}
	pointerValue := pointerDeclaringWrapperUser{
		mappedUser: mappedUser{ID: 7, Email: "embedded@example.com"},
		ID:         42,
		Email:      "method@example.com",
	}

	t.Run("go dispatches to the declared method", func(t *testing.T) {
		columnValue, ok := value.ColumnValue("id")
		require.True(t, ok)
		require.Equal(t, int64(42), columnValue)
		columnValue, ok = pointerValue.ColumnValue("email")
		require.True(t, ok)
		require.Equal(t, "method@example.com", columnValue)
	})

	t.Run("insert", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		client, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		users, err := rasql.NewTable[declaringWrapperUser](table)
		require.NoError(t, err)
		mock.ExpectExec("INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2)").
			WithArgs(int64(42), "method@example.com").
			WillReturnResult(sqlmock.NewResult(1, 1))

		_, err = rasql.Insert(t.Context(), client, users, value)
		require.NoError(t, err)
	})

	t.Run("update", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		client, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		users, err := rasql.NewTable[declaringWrapperUser](table)
		require.NoError(t, err)
		mock.ExpectExec("UPDATE \"users\" SET \"email\" = $1 WHERE (\"users\".\"id\" = $2)").
			WithArgs("method@example.com", int64(42)).
			WillReturnResult(sqlmock.NewResult(0, 1))

		_, err = rasql.Update(t.Context(), client, users, value)
		require.NoError(t, err)
	})

	t.Run("insert with a pointer receiver", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		client, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		users, err := rasql.NewTable[pointerDeclaringWrapperUser](table)
		require.NoError(t, err)
		mock.ExpectExec("INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2)").
			WithArgs(int64(42), "method@example.com").
			WillReturnResult(sqlmock.NewResult(1, 1))

		_, err = rasql.Insert(t.Context(), client, users, pointerValue)
		require.NoError(t, err)
	})

	t.Run("update with a pointer receiver", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		client, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		users, err := rasql.NewTable[pointerDeclaringWrapperUser](table)
		require.NoError(t, err)
		mock.ExpectExec("UPDATE \"users\" SET \"email\" = $1 WHERE (\"users\".\"id\" = $2)").
			WithArgs("method@example.com", int64(42)).
			WillReturnResult(sqlmock.NewResult(0, 1))

		_, err = rasql.Update(t.Context(), client, users, pointerValue)
		require.NoError(t, err)
	})
}

func TestInsertRejectsMissingTaggedColumn(t *testing.T) {
	type user struct {
		ID int64 `rasql:"id"`
	}
	table := schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	}
	users, err := rasql.NewTable[user](table)
	require.NoError(t, err)

	_, err = rasql.Insert(t.Context(), rasql.Client{}, users, user{ID: 42})
	require.ErrorContains(t, err, "no field tagged for column \"email\"")
}
