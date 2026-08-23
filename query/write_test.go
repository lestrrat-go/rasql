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
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	id, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)

	insert, err := query.NewInsert(users, []query.ColumnRef{id, email}, []query.Expression{query.Bind(1), query.Bind("ada@example.com")})
	require.NoError(t, err)
	update, err := query.NewUpdate(users, query.Set(email, query.Bind("grace@example.com")))
	require.NoError(t, err)
	deleteStatement, err := query.NewDelete(users)
	require.NoError(t, err)
	upsert, err := query.NewUpsert(insert, []query.ColumnRef{id}, []query.Assignment{query.Set(email, query.Excluded(email))})
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

	returningInsert, err := insert.WithReturning(id, email)
	require.NoError(t, err)
	require.Equal(t, []query.Projection{id, email}, returningInsert.Returning())

	returningUpdate, err := update.WithReturning(id)
	require.NoError(t, err)
	require.Equal(t, []query.Projection{id}, returningUpdate.Returning())

	returningDelete, err := deleteStatement.WithReturning(id)
	require.NoError(t, err)
	require.Equal(t, []query.Projection{id}, returningDelete.Returning())

	returningUpsert, err := upsert.WithReturning(id)
	require.NoError(t, err)
	require.Equal(t, []query.Projection{id}, returningUpsert.Returning())
}

// TestInsertRejectsNilReturningProjection pins that a nil Projection element
// in WithReturning reports a validation error naming its position, rather
// than panicking when validation dereferences it.
func TestInsertRejectsNilReturningProjection(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	id, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)

	insert, err := query.NewInsert(users, []query.ColumnRef{id, email}, []query.Expression{query.Bind(1), query.Bind("ada@example.com")})
	require.NoError(t, err)

	_, err = insert.WithReturning(nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "returning[0]")
	require.ErrorContains(t, err, "must not be nil")
}

func TestWriteStatementsValidate(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	id, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)

	insert, err := query.NewInsert(users, []query.ColumnRef{id, email}, []query.Expression{query.Bind(1), query.Bind("ada@example.com")})
	require.NoError(t, err)
	insert, err = insert.WithReturning(id)
	require.NoError(t, err)
	require.NoError(t, insert.Validate())

	defaultInsert, err := query.NewDefaultInsert(users)
	require.NoError(t, err)
	require.True(t, defaultInsert.UsesDefaultValues())
	require.Empty(t, defaultInsert.Columns())
	require.Empty(t, defaultInsert.Rows())
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

	// Validation is shared by writes, not select-only: a DELETE accepts NotIn
	// with values and rejects In with none.
	membershipDelete, err := query.NewDelete(users)
	require.NoError(t, err)
	membershipDelete, err = membershipDelete.WithWhere(query.NotIn(id, query.Bind(1)))
	require.NoError(t, err)
	require.NoError(t, membershipDelete.Validate())

	emptyDelete, err := query.NewDelete(users)
	require.NoError(t, err)
	_, err = emptyDelete.WithWhere(query.In(id))
	require.Error(t, err)
}

func TestWriteStatementsRequireExplicitAllowAll(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	id, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)

	update, err := query.NewUpdate(users, query.Set(email, query.Bind("grace@example.com")))
	require.NoError(t, err)
	require.False(t, update.AllowsAll())
	allowedUpdate, err := update.AllowAll()
	require.NoError(t, err)
	require.True(t, allowedUpdate.AllowsAll())
	require.False(t, update.AllowsAll())

	whereUpdate, err := update.WithWhere(query.Equal(id, query.Bind(1)))
	require.NoError(t, err)
	_, err = whereUpdate.AllowAll()
	require.ErrorContains(t, err, "UPDATE AllowAll must not be combined")

	deleteStatement, err := query.NewDelete(users)
	require.NoError(t, err)
	require.False(t, deleteStatement.AllowsAll())
	allowedDelete, err := deleteStatement.AllowAll()
	require.NoError(t, err)
	require.True(t, allowedDelete.AllowsAll())
	require.False(t, deleteStatement.AllowsAll())

	whereDelete, err := deleteStatement.WithWhere(query.Equal(id, query.Bind(1)))
	require.NoError(t, err)
	_, err = whereDelete.AllowAll()
	require.ErrorContains(t, err, "DELETE AllowAll must not be combined")
}

func TestWriteStatementsRejectInvalidInput(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	id, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)

	// NewInsert with nil values becomes one row of length zero against a
	// non-empty column list, and must keep failing.
	_, err = query.NewInsert(users, []query.ColumnRef{id}, nil)
	require.Error(t, err)
	_, err = query.NewInsert(users, []query.ColumnRef{id, id}, []query.Expression{query.Bind(1), query.Bind(2)})
	require.Error(t, err)
	_, err = query.NewUpdate(users)
	require.Error(t, err)

	_, err = query.NewInsertRows(users, []query.ColumnRef{id, email}, nil)
	require.Error(t, err)
	_, err = query.NewInsertRows(users, []query.ColumnRef{id, email}, [][]query.Expression{})
	require.Error(t, err)
	_, err = query.NewInsertRows(users, []query.ColumnRef{id, email}, [][]query.Expression{
		{query.Bind(1), query.Bind("ada@example.com")},
		{query.Bind(2)},
	})
	require.Error(t, err)

	defaultInsert, err := query.NewDefaultInsert(users)
	require.NoError(t, err)
	_, err = defaultInsert.WithRows([]query.Expression{query.Bind(1), query.Bind("ada@example.com")})
	require.Error(t, err)

	aliased, err := users.As("u")
	require.NoError(t, err)
	_, err = query.NewDelete(aliased)
	require.Error(t, err)
	_, err = query.NewUpdate(users, query.Set(email, query.And(id)))
	require.Error(t, err)
}

// TestWriteTargetRejectsColumnFromAnotherSchema pins the sharpening of
// query/write.go's validateTargetColumn once the schema joined key(): a
// column built from an unqualified "users" descriptor is refused against a
// "tenant.users" target, where it was wrongly accepted before the schema was
// part of the key comparison.
func TestWriteTargetRejectsColumnFromAnotherSchema(t *testing.T) {
	unqualified, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	email, err := unqualified.Column("email")
	require.NoError(t, err)

	tenantDescriptor := usersTable()
	tenantDescriptor.Schema = "tenant"
	tenantUsers, err := query.NewTableRef(tenantDescriptor)
	require.NoError(t, err)

	_, err = query.NewUpdate(tenantUsers, query.Set(email, query.Bind("grace@example.com")))
	require.ErrorContains(t, err, `belongs to table "users" instead of target "tenant.users"`)
}

func TestInsertHoldsMultipleRows(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	id, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)

	rows := [][]query.Expression{
		{query.Bind(1), query.Bind("ada@example.com")},
		{query.Bind(2), query.Bind("grace@example.com")},
		{query.Bind(3), query.Bind("edsger@example.com")},
	}
	insert, err := query.NewInsertRows(users, []query.ColumnRef{id, email}, rows)
	require.NoError(t, err)
	require.Equal(t, rows, insert.Rows())

	// Rows returns an independent copy: appending to the returned outer slice
	// and mutating one of its inner slices must not reach the statement.
	returned := insert.Rows()
	returned = append(returned, []query.Expression{query.Bind(4), query.Bind("grete@example.com")})
	returned[0][0] = query.Bind(999)
	require.Equal(t, rows, insert.Rows())
}

func TestInsertAppendsRowsImmutably(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	id, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)

	first := [][]query.Expression{{query.Bind(1), query.Bind("ada@example.com")}}
	insert, err := query.NewInsertRows(users, []query.ColumnRef{id, email}, first)
	require.NoError(t, err)

	appended, err := insert.WithRows([]query.Expression{query.Bind(2), query.Bind("grace@example.com")})
	require.NoError(t, err)

	require.Len(t, insert.Rows(), 1)
	require.Len(t, appended.Rows(), 2)
}

func TestInsertValidatesEveryRowExpression(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	id, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)

	orders, err := query.NewTableRef(ordersTable())
	require.NoError(t, err)
	orderID, err := orders.Column("id")
	require.NoError(t, err)

	_, err = query.NewInsertRows(users, []query.ColumnRef{id, email}, [][]query.Expression{
		{query.Bind(1), query.Bind("ada@example.com")},
		{query.Bind(2), query.Bind("grace@example.com")},
		{orderID, query.Bind("edsger@example.com")},
	})
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, "references table")
	require.ErrorContains(t, err, "outside the statement")
	require.ErrorContains(t, err, "rows[2].values[0]")
}

// TestInsertRowValueRejectsTargetTableColumn pins the fix for a VALUES row
// that reads a column of its own INSERT's target table. No row exists yet for
// such a read to resolve against: PostgreSQL and SQLite refuse it outright,
// and MySQL silently accepts it and writes the wrong data, so validation must
// refuse it on every dialect rather than let it through to the renderer.
func TestInsertRowValueRejectsTargetTableColumn(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	id, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)

	t.Run("bare column", func(t *testing.T) {
		_, err := query.NewInsert(users, []query.ColumnRef{id, email}, []query.Expression{query.Bind(1), email})
		requireQueryValidationError(t, err)
		require.ErrorContains(t, err, `references column "email" of the target table`)
		require.ErrorContains(t, err, "an INSERT VALUES row cannot read the target table's columns")
		require.ErrorContains(t, err, "rows[0].values[1]")
	})

	t.Run("nested in function call", func(t *testing.T) {
		_, err := query.NewInsert(users, []query.ColumnRef{id, email}, []query.Expression{query.Bind(1), query.Lower(email)})
		requireQueryValidationError(t, err)
		require.ErrorContains(t, err, `references column "email" of the target table`)
		require.ErrorContains(t, err, "an INSERT VALUES row cannot read the target table's columns")
		require.ErrorContains(t, err, "rows[0].values[1].arguments[0]")
	})

	t.Run("NewInsertRows form", func(t *testing.T) {
		_, err := query.NewInsertRows(users, []query.ColumnRef{id, email}, [][]query.Expression{
			{query.Bind(1), query.Bind("ada@example.com")},
			{query.Bind(2), email},
		})
		requireQueryValidationError(t, err)
		require.ErrorContains(t, err, `references column "email" of the target table`)
		require.ErrorContains(t, err, "an INSERT VALUES row cannot read the target table's columns")
		require.ErrorContains(t, err, "rows[1].values[1]")
	})
}

// TestWriteStatementsRejectAggregates covers the placement rule for write
// statements: no clause of a write statement may call an aggregate, because
// only a SELECT projection may.
func TestWriteStatementsRejectAggregates(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	id, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)
	insert, err := query.NewInsert(users, []query.ColumnRef{id, email}, []query.Expression{query.Bind(1), query.Bind("ada@example.com")})
	require.NoError(t, err)
	update, err := query.NewUpdate(users, query.Set(email, query.Bind("grace@example.com")))
	require.NoError(t, err)
	deleteStatement, err := query.NewDelete(users)
	require.NoError(t, err)

	tests := map[string]struct {
		build   func() error
		message string
	}{
		"insert value": {
			build: func() error {
				_, err := query.NewInsert(users, []query.ColumnRef{id}, []query.Expression{query.Count(id)})
				return err
			},
			message: `calls aggregate function "COUNT" in an INSERT value`,
		},
		"insert returning": {
			build: func() error {
				_, err := insert.WithReturning(query.CountAll())
				return err
			},
			message: `calls aggregate function "COUNT" in a RETURNING projection`,
		},
		"update assignment": {
			build: func() error {
				_, err := query.NewUpdate(users, query.Set(id, query.Max(id)))
				return err
			},
			message: `calls aggregate function "MAX" in a SET assignment`,
		},
		"update where": {
			build: func() error {
				_, err := update.WithWhere(query.GreaterThan(query.Count(id), query.Bind(1)))
				return err
			},
			message: `calls aggregate function "COUNT" in a WHERE clause`,
		},
		"delete where": {
			build: func() error {
				_, err := deleteStatement.WithWhere(query.GreaterThan(query.Count(id), query.Bind(1)))
				return err
			},
			message: `calls aggregate function "COUNT" in a WHERE clause`,
		},
		"upsert assignment": {
			build: func() error {
				_, err := query.NewUpsert(insert, []query.ColumnRef{id}, []query.Assignment{query.Set(email, query.Max(email))})
				return err
			},
			message: `calls aggregate function "MAX" in a conflict-update assignment`,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := test.build()
			requireQueryValidationError(t, err)
			require.ErrorContains(t, err, test.message)
		})
	}
}

// TestWriteStatementsAcceptScalarFunctions covers the reach of a scalar
// function call into every write clause: an INSERT row value, a SET
// assignment, an upsert conflict-update assignment, and RETURNING all reach
// validateClauseExpression, which carries ctx unchanged, so a scalar call is
// legal there while an aggregate is refused, exactly as in
// TestWriteStatementsRejectAggregates.
func TestWriteStatementsAcceptScalarFunctions(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	id, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)

	insert, err := query.NewInsert(users, []query.ColumnRef{id, email}, []query.Expression{query.Bind(1), query.Coalesce(query.Bind("ada@example.com"), query.Bind(""))})
	require.NoError(t, err)
	require.NoError(t, insert.Validate())

	returning, err := insert.WithReturning(query.Lower(email).As("lower_email"))
	require.NoError(t, err)
	require.NoError(t, returning.Validate())

	update, err := query.NewUpdate(users, query.Set(email, query.Lower(email)))
	require.NoError(t, err)
	require.NoError(t, update.Validate())

	upsert, err := query.NewUpsert(insert, []query.ColumnRef{id}, []query.Assignment{query.Set(email, query.Coalesce(query.Excluded(email), email))})
	require.NoError(t, err)
	require.NoError(t, upsert.Validate())
}

// TestUpsertAcceptsNestedExcludedColumn pins that validation accepts an
// ExcludedColumn nested inside another expression, such as inside a scalar
// function call, and not only at the top level of a conflict-update
// assignment's value. render/write_test.go's
// TestSQLiteUpsertRendersNestedExcludedColumn proves the renderer now
// matches what validation already accepted here.
func TestUpsertAcceptsNestedExcludedColumn(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	id, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)

	insert, err := query.NewInsert(users, []query.ColumnRef{id, email}, []query.Expression{query.Bind(1), query.Bind("ada@example.com")})
	require.NoError(t, err)

	upsert, err := query.NewUpsert(insert, []query.ColumnRef{id}, []query.Assignment{
		query.Set(email, query.Coalesce(query.Excluded(email), query.Bind("unknown@example.com"))),
	})
	require.NoError(t, err)
	require.NoError(t, upsert.Validate())

	// The exact shape the design doc reproduced: a comparison nests
	// ExcludedColumn as its left operand rather than a scalar function, and
	// validation accepts it the same way.
	comparison, err := query.NewUpsert(insert, []query.ColumnRef{id}, []query.Assignment{
		query.Set(id, query.Equal(query.Excluded(id), query.Bind(5))),
	})
	require.NoError(t, err)
	require.NoError(t, comparison.Validate())
}

// TestWriteStatementsRejectSubqueries covers the placement rule that keeps a
// subquery out of every write clause: only a SELECT statement's own clauses set
// allowsSubquery, so a write clause refuses one with the same message a write
// clause uses for a misplaced aggregate.
func TestWriteStatementsRejectSubqueries(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	id, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)
	insert, err := query.NewInsert(users, []query.ColumnRef{id, email}, []query.Expression{query.Bind(1), query.Bind("ada@example.com")})
	require.NoError(t, err)
	update, err := query.NewUpdate(users, query.Set(email, query.Bind("grace@example.com")))
	require.NoError(t, err)
	deleteStatement, err := query.NewDelete(users)
	require.NoError(t, err)

	ids, err := query.NewSelect(users, id)
	require.NoError(t, err)

	tests := map[string]struct {
		build func() error
	}{
		"insert value": {
			build: func() error {
				_, err := query.NewInsert(users, []query.ColumnRef{id}, []query.Expression{query.Scalar(ids)})
				return err
			},
		},
		"insert returning": {
			build: func() error {
				_, err := insert.WithReturning(query.Project(query.Scalar(ids)))
				return err
			},
		},
		"update assignment": {
			build: func() error {
				_, err := query.NewUpdate(users, query.Set(id, query.Scalar(ids)))
				return err
			},
		},
		"update where": {
			build: func() error {
				_, err := update.WithWhere(query.InSelect(id, ids))
				return err
			},
		},
		"delete where": {
			build: func() error {
				_, err := deleteStatement.WithWhere(query.InSelect(id, ids))
				return err
			},
		},
		"upsert assignment": {
			build: func() error {
				_, err := query.NewUpsert(insert, []query.ColumnRef{id}, []query.Assignment{query.Set(email, query.Scalar(ids))})
				return err
			},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := test.build()
			requireQueryValidationError(t, err)
			require.ErrorContains(t, err, "only valid in the projections, JOIN ON conditions, WHERE clause, GROUP BY clause, HAVING clause, and ORDER BY clause of a SELECT statement")
		})
	}
}

func TestUpsertValidatesConflictAssignments(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	id, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)
	insert, err := query.NewInsert(users, []query.ColumnRef{id, email}, []query.Expression{query.Bind(1), query.Bind("ada@example.com")})
	require.NoError(t, err)

	upsert, err := query.NewUpsert(insert, []query.ColumnRef{id}, []query.Assignment{query.Set(email, query.Excluded(email))})
	require.NoError(t, err)
	require.NoError(t, upsert.Validate())

	_, err = query.NewUpsert(insert, nil, nil)
	require.Error(t, err)
	_, err = query.NewUpsert(insert, []query.ColumnRef{id, id}, nil)
	require.Error(t, err)
}
