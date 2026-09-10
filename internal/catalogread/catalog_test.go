package catalogread_test

import (
	"context"
	"database/sql"
	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
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
