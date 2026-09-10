package catalogread_test

import (
	"context"
	"database/sql"
	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	"testing"
)

type noBeginDB struct{ called bool }

func (d *noBeginDB) BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error) {
	d.called = true
	return nil, nil
}

func TestReadRejectsForgedProfileBeforeBegin(t *testing.T) {
	db := &noBeginDB{}
	_, err := catalogread.Read(context.Background(), db, engineprofile.Profile{ID: "forged", Engine: engineprofile.SQLite, Limits: engineprofile.Limits{MaxBindParameters: 1}}, catalogread.Scope{})
	require.ErrorIs(t, err, engineprofile.ErrInvalidProfile)
	require.False(t, db.called)
}

// TestScopeValidationRefusesConflictingNamespaces exercises validateScope's
// new refusals through Read, the same way TestReadRejectsForgedProfileBeforeBegin
// proves a profile is rejected before BeginTx: a stub DB records whether it
// was ever asked to begin a transaction, so a refusal that reaches the
// database instead of stopping in validation would show up as db.called.
func TestScopeValidationRefusesConflictingNamespaces(t *testing.T) {
	t.Run("blank namespace is refused", func(t *testing.T) {
		db := &noBeginDB{}
		_, err := catalogread.Read(context.Background(), db, sqliteProfile(t), catalogread.Scope{Namespaces: []string{""}})
		require.Error(t, err)
		require.False(t, db.called)
	})
	t.Run("duplicate namespace is refused", func(t *testing.T) {
		db := &noBeginDB{}
		_, err := catalogread.Read(context.Background(), db, sqliteProfile(t), catalogread.Scope{Namespaces: []string{"aux", "aux"}})
		require.Error(t, err)
		require.False(t, db.called)
	})
	t.Run("namespaces with a schema-qualified include is refused", func(t *testing.T) {
		db := &noBeginDB{}
		_, err := catalogread.Read(context.Background(), db, sqliteProfile(t), catalogread.Scope{
			Namespaces: []string{"aux"},
			Include:    []schema.ObjectName{{Schema: "aux", Name: "widgets"}},
		})
		require.Error(t, err)
		require.False(t, db.called)
	})
}
