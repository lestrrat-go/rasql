package schemasource

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestDefaultFactoryJoinsDerivedDSNAndCleanupErrors(t *testing.T) {
	base, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	createErr := errors.New("derived dsn")
	dropErr := errors.New("drop database")
	mock.ExpectExec(`CREATE DATABASE .*`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DROP DATABASE .*`).WillReturnError(dropErr)
	factory := defaultFactory{
		open:   func(string, string) (*sql.DB, error) { return base, nil },
		derive: func(string, string, string) (string, error) { return "", createErr },
	}
	_, err = factory.Create(context.Background(), FactoryRequest{Dialect: "postgresql", BootstrapDSN: "postgres://bootstrap"})
	if !errors.Is(err, createErr) || !errors.Is(err, dropErr) {
		t.Fatalf("error=%v, want derived and cleanup causes", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultFactoryJoinsOwnedOpenAndCleanupErrors(t *testing.T) {
	base, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	openErr := errors.New("owned open")
	dropErr := errors.New("drop database")
	mock.ExpectExec(`CREATE DATABASE .*`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DROP DATABASE .*`).WillReturnError(dropErr)
	calls := 0
	factory := defaultFactory{
		open: func(string, string) (*sql.DB, error) {
			calls++
			if calls == 1 {
				return base, nil
			}
			return nil, openErr
		},
	}
	_, err = factory.Create(context.Background(), FactoryRequest{Dialect: "postgresql", BootstrapDSN: "postgres://bootstrap"})
	if !errors.Is(err, openErr) || !errors.Is(err, dropErr) {
		t.Fatalf("error=%v, want owned-open and cleanup causes", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
