package engineprofile_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/stretchr/testify/require"
)

func TestObserveExternalRowsAndVersions(t *testing.T) {
	cases := []struct {
		name   string
		engine engineprofile.EngineID
		query  string
		raw    string
		want   engineprofile.Version
	}{
		{"postgres", engineprofile.PostgreSQL, `SHOW server_version_num`, "170006", engineprofile.Version{Known: true, Major: 17, Minor: 6}},
		{"mysql", engineprofile.MySQL, `SELECT VERSION\(\)`, "8.4.11-ubuntu", engineprofile.Version{Known: true, Major: 8, Minor: 4, Patch: 11}},
		{"sqlite", engineprofile.SQLite, `SELECT sqlite_version\(\)`, "3.35.0", engineprofile.Version{Known: true, Major: 3, Minor: 35}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			mock.ExpectQuery(tc.query).WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(tc.raw))
			mock.ExpectClose()
			got, err := engineprofile.Observe(context.Background(), db, tc.engine)
			require.NoError(t, err)
			require.Equal(t, tc.want, got.Version)
			require.NoError(t, db.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestObserveExternalCardinalityAndQueryErrors(t *testing.T) {
	cases := []struct {
		name string
		rows *sqlmock.Rows
		err  error
	}{
		{"query", nil, errors.New("query failed")},
		{"empty", sqlmock.NewRows([]string{"version"}), nil},
		{"two rows", sqlmock.NewRows([]string{"version"}).AddRow("170000").AddRow("170001"), nil},
		{"two columns", sqlmock.NewRows([]string{"version", "extra"}).AddRow("170000", "x"), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			if tc.err != nil {
				mock.ExpectQuery("SHOW server_version_num").WillReturnError(tc.err)
			} else {
				mock.ExpectQuery("SHOW server_version_num").WillReturnRows(tc.rows)
			}
			mock.ExpectClose()
			_, err = engineprofile.Observe(context.Background(), db, engineprofile.PostgreSQL)
			require.ErrorIs(t, err, engineprofile.ErrVersionObservation)
			require.NoError(t, db.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
