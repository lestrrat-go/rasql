package mysqlerr_test

import (
	"fmt"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/lestrrat-go/rasql/dberror"
	"github.com/lestrrat-go/rasql/dberror/mysqlerr"
	"github.com/stretchr/testify/require"
)

func TestClassifyMappingsAndWrappers(t *testing.T) {
	tests := []struct {
		number   uint16
		category dberror.Category
	}{
		{1062, dberror.UniqueViolation}, {1451, dberror.ForeignKeyViolation}, {1452, dberror.ForeignKeyViolation},
		{1048, dberror.NotNullViolation}, {3819, dberror.CheckViolation},
		{1205, dberror.TransactionConflict}, {1213, dberror.TransactionConflict},
	}
	for _, testCase := range tests {
		native := &mysql.MySQLError{Number: testCase.number, SQLState: [5]byte{'4', '2', '0', '0', '0'}}
		metadata, ok := dberror.Classify(fmt.Errorf("exec: %w", native), mysqlerr.New())
		require.True(t, ok)
		require.Equal(t, testCase.category, metadata.Category)
		require.Equal(t, fmt.Sprint(testCase.number), metadata.NativeCode)
		require.Equal(t, "42000", metadata.SQLState)
	}
	metadata, ok := dberror.Classify(&mysql.MySQLError{Number: 9999}, mysqlerr.New())
	require.False(t, ok)
	require.Equal(t, dberror.Metadata{}, metadata)
}
