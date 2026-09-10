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

func TestObserveExternalRowAndCloseFailures(t *testing.T) {
	rowErr := errors.New("row iteration failed")
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	mock.ExpectQuery("SHOW server_version_num").WillReturnRows(
		sqlmock.NewRows([]string{"version"}).AddRow("170000").RowError(0, rowErr),
	).RowsWillBeClosed()
	mock.ExpectClose()
	_, err = engineprofile.Observe(t.Context(), db, engineprofile.PostgreSQL)
	require.ErrorIs(t, err, engineprofile.ErrVersionObservation)
	require.NoError(t, db.Close())
	require.NoError(t, mock.ExpectationsWereMet())

	db, mock, err = sqlmock.New()
	require.NoError(t, err)
	closeErr := errors.New("row close failed")
	mock.ExpectQuery("SHOW server_version_num").WillReturnRows(
		sqlmock.NewRows([]string{"version"}).AddRow("170000").CloseError(closeErr),
	).RowsWillBeClosed()
	mock.ExpectClose()
	_, err = engineprofile.Observe(t.Context(), db, engineprofile.PostgreSQL)
	require.ErrorIs(t, err, engineprofile.ErrVersionObservation)
	var discovery *engineprofile.DiscoveryError
	require.ErrorAs(t, err, &discovery)
	// DiscoveryError.Unwrap exposes the observation sentinel; the driver cause is stored in Detail by contract.
	require.Equal(t, closeErr.Error(), discovery.Detail)
	require.NoError(t, db.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestObserveExternalContextFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	mock.ExpectQuery("SHOW server_version_num").WillReturnError(context.Canceled)
	mock.ExpectClose()
	_, err = engineprofile.Observe(t.Context(), db, engineprofile.PostgreSQL)
	require.ErrorIs(t, err, engineprofile.ErrVersionObservation)
	require.NoError(t, db.Close())
}

func TestObserveExternalMalformedResponses(t *testing.T) {
	cases := []struct {
		name   string
		engine engineprofile.EngineID
		query  string
		raw    string
	}{
		{name: "postgres below minimum", engine: engineprofile.PostgreSQL, query: "SHOW server_version_num", raw: "99999"},
		{name: "postgres non decimal", engine: engineprofile.PostgreSQL, query: "SHOW server_version_num", raw: "17.6"},
		{name: "mysql missing patch", engine: engineprofile.MySQL, query: `SELECT VERSION\(\)`, raw: "8.4"},
		{name: "mysql extra component", engine: engineprofile.MySQL, query: `SELECT VERSION\(\)`, raw: "8.4.1.2"},
		{name: "mysql trailing text", engine: engineprofile.MySQL, query: `SELECT VERSION\(\)`, raw: "8.4.1vendor"},
		{name: "sqlite missing component", engine: engineprofile.SQLite, query: `SELECT sqlite_version\(\)`, raw: "3.35"},
		{name: "sqlite suffix", engine: engineprofile.SQLite, query: `SELECT sqlite_version\(\)`, raw: "3.35.0-ubuntu"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			mock.ExpectQuery(tc.query).WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(tc.raw))
			mock.ExpectClose()
			got, err := engineprofile.Observe(t.Context(), db, tc.engine)
			require.ErrorIs(t, err, engineprofile.ErrVersionParse)
			require.Equal(t, engineprofile.ObservedIdentity{}, got)
			require.NoError(t, db.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
