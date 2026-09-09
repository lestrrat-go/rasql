package querydescribe_test

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/internal/compilerquery"
	"github.com/lestrrat-go/rasql/querydescribe"
)

func TestDeclaredUsesRequestDBPrepareAndCloseOnly(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectPrepare("SELECT 1").WillBeClosed()
	d := querydescribe.NewDeclared(nil)
	if _, err := d.Describe(t.Context(), compilerquery.DescribeRequest{DB: db, SQL: "SELECT 1"}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
