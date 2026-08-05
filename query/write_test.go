package query_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/query"
	"github.com/stretchr/testify/require"
)

// Every write statement type must implement WriteStatement, including its
// Returning method, so a statement built through query.New… can always be
// checked for a RETURNING clause without a type switch.
var (
	_ query.WriteStatement = query.Insert{}
	_ query.WriteStatement = query.Update{}
	_ query.WriteStatement = query.Delete{}
	_ query.WriteStatement = query.Upsert{}
)

func TestWriteStatementsReportReturning(t *testing.T) {
	users, err := query.NewTable(usersTable())
	require.NoError(t, err)
	id, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)

	insert, err := query.NewInsert(users, []query.Column{id, email}, []query.Expression{query.Bind(1), query.Bind("ada@example.com")})
	require.NoError(t, err)
	update, err := query.NewUpdate(users, query.Set(email, query.Bind("grace@example.com")))
	require.NoError(t, err)
	deleteStatement, err := query.NewDelete(users)
	require.NoError(t, err)
	upsert, err := query.NewUpsert(insert, []query.Column{id}, []query.Assignment{query.Set(email, query.Excluded(email))})
	require.NoError(t, err)

	for _, testCase := range []struct {
		name      string
		statement query.WriteStatement
	}{
		{name: "insert", statement: insert},
		{name: "update", statement: update},
		{name: "delete", statement: deleteStatement},
		{name: "upsert", statement: upsert},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			require.Empty(t, testCase.statement.Returning())
		})
	}

	returningInsert, err := insert.WithReturning(query.Project(id), query.Project(email))
	require.NoError(t, err)
	require.Equal(t, []query.Projection{query.Project(id), query.Project(email)}, returningInsert.Returning())

	returningUpdate, err := update.WithReturning(query.Project(id))
	require.NoError(t, err)
	require.Equal(t, []query.Projection{query.Project(id)}, returningUpdate.Returning())

	returningDelete, err := deleteStatement.WithReturning(query.Project(id))
	require.NoError(t, err)
	require.Equal(t, []query.Projection{query.Project(id)}, returningDelete.Returning())

	returningUpsert, err := upsert.WithReturning(query.Project(id))
	require.NoError(t, err)
	require.Equal(t, []query.Projection{query.Project(id)}, returningUpsert.Returning())
}

func TestWriteStatementsValidate(t *testing.T) {
	users, err := query.NewTable(usersTable())
	require.NoError(t, err)
	id, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)

	insert, err := query.NewInsert(users, []query.Column{id, email}, []query.Expression{query.Bind(1), query.Bind("ada@example.com")})
	require.NoError(t, err)
	insert, err = insert.WithReturning(query.Project(id))
	require.NoError(t, err)
	require.NoError(t, insert.Validate())

	defaultInsert, err := query.NewDefaultInsert(users)
	require.NoError(t, err)
	require.True(t, defaultInsert.UsesDefaultValues())
	require.Empty(t, defaultInsert.Columns())
	require.Empty(t, defaultInsert.Values())
	require.NoError(t, defaultInsert.Validate())

	update, err := query.NewUpdate(users, query.Set(email, query.Bind("grace@example.com")))
	require.NoError(t, err)
	update, err = update.WithWhere(query.Equal(id, query.Bind(1)))
	require.NoError(t, err)
	require.NoError(t, update.Validate())

	deleteStatement, err := query.NewDelete(users)
	require.NoError(t, err)
	deleteStatement, err = deleteStatement.WithWhere(query.Equal(id, query.Bind(1)))
	require.NoError(t, err)
	require.NoError(t, deleteStatement.Validate())
}

func TestWriteStatementsRejectInvalidInput(t *testing.T) {
	users, err := query.NewTable(usersTable())
	require.NoError(t, err)
	id, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)

	_, err = query.NewInsert(users, []query.Column{id}, nil)
	require.Error(t, err)
	_, err = query.NewInsert(users, []query.Column{id, id}, []query.Expression{query.Bind(1), query.Bind(2)})
	require.Error(t, err)
	_, err = query.NewUpdate(users)
	require.Error(t, err)

	aliased, err := users.As("u")
	require.NoError(t, err)
	_, err = query.NewDelete(aliased)
	require.Error(t, err)
	_, err = query.NewUpdate(users, query.Set(email, query.And(id)))
	require.Error(t, err)
}

func TestUpsertValidatesConflictAssignments(t *testing.T) {
	users, err := query.NewTable(usersTable())
	require.NoError(t, err)
	id, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)
	insert, err := query.NewInsert(users, []query.Column{id, email}, []query.Expression{query.Bind(1), query.Bind("ada@example.com")})
	require.NoError(t, err)

	upsert, err := query.NewUpsert(insert, []query.Column{id}, []query.Assignment{query.Set(email, query.Excluded(email))})
	require.NoError(t, err)
	require.NoError(t, upsert.Validate())

	_, err = query.NewUpsert(insert, nil, nil)
	require.Error(t, err)
	_, err = query.NewUpsert(insert, []query.Column{id, id}, nil)
	require.Error(t, err)
}
