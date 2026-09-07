package query_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestSelectLockValidationAndCloning(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)
	statement, err := query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	lock := query.RowLock(query.LockUpdate).Of(users).Wait(query.LockWaitSkipLocked)
	locked, err := statement.WithLock(lock)
	require.NoError(t, err)
	got, ok := locked.Lock()
	require.True(t, ok)
	require.Equal(t, query.LockUpdate, got.Strength())
	require.Equal(t, query.LockWaitSkipLocked, got.WaitMode())
	require.Equal(t, []query.TableRef{users}, got.Tables())
	tables := got.Tables()
	tables[0] = query.TableRef{}
	require.Equal(t, users, got.Tables()[0])

	_, err = statement.WithLock(query.RowLock(99))
	require.ErrorContains(t, err, "lock.strength")
	_, err = statement.WithLock(query.RowLock(query.LockUpdate).Of(users, users))
	require.ErrorContains(t, err, "duplicates")
	other, err := query.NewTableRef(schema.MustTableDef("other", schema.Integer("id")))
	require.NoError(t, err)
	_, err = statement.WithLock(query.RowLock(query.LockUpdate).Of(other))
	require.ErrorContains(t, err, "outside")
}

func TestUpsertConditionalPredicatesValidate(t *testing.T) {
	table, err := query.NewTableRef(schema.MustTableDef("items",
		schema.Integer("id"), schema.Integer("version"), schema.Text("payload")))
	require.NoError(t, err)
	id, version, payload := table.Column("id"), table.Column("version"), table.Column("payload")
	insert, err := query.NewInsert(table, query.Set(id, 1), query.Set(version, 2), query.Set(payload, "new"))
	require.NoError(t, err)
	upsert, err := query.NewUpsert(insert, []query.ColumnRef{id}, []query.Assignment{
		query.Set(version, query.Excluded(version)), query.Set(payload, query.Excluded(payload)),
	})
	require.NoError(t, err)
	upsert, err = upsert.WithConflictWhere(query.GreaterThan(version, 0))
	require.NoError(t, err)
	upsert, err = upsert.WithUpdateWhere(query.LessThan(version, query.Excluded(version)))
	require.NoError(t, err)
	require.NotNil(t, upsert.ConflictWhere())
	require.NotNil(t, upsert.UpdateWhere())

	_, err = upsert.WithConflictWhere(nil)
	require.ErrorContains(t, err, "conflict_where")
	_, err = upsert.WithUpdateWhere(nil)
	require.ErrorContains(t, err, "update_where")
	_, err = upsert.WithConflictWhere(query.Excluded(version))
	require.ErrorContains(t, err, "EXCLUDED")
	_, err = query.NewUpsert(insert, nil, []query.Assignment{query.Set(payload, "x")})
	require.NoError(t, err)
	noTarget, _ := query.NewUpsert(insert, nil, []query.Assignment{query.Set(payload, "x")})
	_, err = noTarget.WithConflictWhere(query.Equal(id, 1))
	require.ErrorContains(t, err, "requires conflict columns")
	noAssignments, _ := query.NewUpsert(insert, []query.ColumnRef{id}, nil)
	_, err = noAssignments.WithUpdateWhere(query.Equal(id, 1))
	require.ErrorContains(t, err, "requires assignments")
}

func TestExcludedRejectedOutsideUpsertAction(t *testing.T) {
	table, err := query.NewTableRef(schema.MustTableDef("items", schema.Integer("id"), schema.Integer("version")))
	require.NoError(t, err)
	id, version := table.Column("id"), table.Column("version")
	selectStatement, err := query.NewSelect(table, id)
	require.NoError(t, err)
	_, err = selectStatement.WithWhere(query.Equal(id, query.Excluded(id)))
	require.ErrorContains(t, err, "EXCLUDED")
	update, err := query.NewUpdate(table, query.Set(version, 1))
	require.NoError(t, err)
	_, err = update.WithWhere(query.Equal(id, query.Excluded(id)))
	require.ErrorContains(t, err, "EXCLUDED")
	_, err = query.NewInsert(table, query.Set(id, query.Excluded(id)), query.Set(version, 1))
	require.ErrorContains(t, err, "EXCLUDED")
}
