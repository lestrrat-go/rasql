package generate_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

const maxQueryInputBytes = 64 << 20

// TestStorePlanRejectsOversizedQueryInput keeps Store.Queries bounded by the
// same 64 MiB input limit as generated query functions. The plan must reject the source
// before template parsing or any generated file can be committed.
func TestStorePlanRejectsOversizedQueryInput(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "oversized.sql")
	data := bytes.Repeat([]byte("x"), maxQueryInputBytes+1)
	require.NoError(t, os.WriteFile(input, data, 0o600))

	store := generate.Store{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Tables:  []schema.TableDef{usersTableDef()},
		Dialect: dialect.PostgreSQL(),
		Queries: []generate.Query{{
			Input:    input,
			Function: "Oversized",
			Output:   "oversized_gen.go",
		}},
	}

	_, err := store.Plan()
	require.ErrorContains(t, err, input)
	require.ErrorContains(t, err, "exceeds maximum size of 67108864 bytes")
}

// TestStoreCompilesInlineQuery requires a query carrying its template in SQL
// to generate the same function a query naming a file generates, with no file
// on disk for it.
func TestStoreCompilesInlineQuery(t *testing.T) {
	root := t.TempDir()
	store := generate.Store{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Tables:  []schema.TableDef{usersTableDef()},
		Dialect: dialect.PostgreSQL(),
		Queries: []generate.Query{{
			SQL:      `SELECT id FROM users WHERE email = {{bind "email"}}`,
			Function: "UserByEmail",
			Output:   "user_by_email_gen.go",
		}},
	}

	require.NoError(t, store.Write())
	generated, err := os.ReadFile(filepath.Join(root, "store", "user_by_email_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(generated), "func UserByEmail(email any)")
	require.Contains(t, string(generated), "SELECT id FROM users WHERE email = $1")
	require.NoError(t, store.Check())
}

// TestStoreCompilesTypedQuery requires a bind that names a column to
// generate that column's Go type instead of any.
func TestStoreCompilesTypedQuery(t *testing.T) {
	root := t.TempDir()
	store := generate.Store{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Tables:  []schema.TableDef{usersTableDef()},
		Dialect: dialect.PostgreSQL(),
		Queries: []generate.Query{{
			SQL:      `SELECT id FROM users WHERE email = {{bind "email" users.email}}`,
			Function: "UserByEmail",
			Output:   "user_by_email_gen.go",
		}},
	}

	require.NoError(t, store.Write())
	generated, err := os.ReadFile(filepath.Join(root, "store", "user_by_email_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(generated), "func UserByEmail(email string)")
	require.NoError(t, store.Check())
}

// TestStoreRejectsTypedBindNamingAnAbsentTable requires Plan to fail, naming
// the query and the reference, when a bind names a table this store's
// Tables does not include.
func TestStoreRejectsTypedBindNamingAnAbsentTable(t *testing.T) {
	root := t.TempDir()
	store := generate.Store{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Tables:  []schema.TableDef{usersTableDef()},
		Dialect: dialect.PostgreSQL(),
		Queries: []generate.Query{{
			SQL:      `SELECT id FROM orders WHERE id = {{bind "id" orders.id}}`,
			Function: "OrderByID",
			Output:   "order_by_id_gen.go",
		}},
	}

	_, err := store.Plan()
	require.ErrorContains(t, err, "query[0]")
	require.ErrorContains(t, err, "orders.id")
}

// TestStoreTypedBindIgnoresTableOrder requires the lookup to run against
// Plan's hint-applied, sorted table set rather than the caller's Tables
// order, so the same descriptors given in a different order plan identical
// bytes.
func TestStoreTypedBindIgnoresTableOrder(t *testing.T) {
	root := t.TempDir()
	base := generate.Store{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Dialect: dialect.PostgreSQL(),
		Queries: []generate.Query{{
			SQL:      `SELECT id FROM users WHERE email = {{bind "email" users.email}}`,
			Function: "UserByEmail",
			Output:   "user_by_email_gen.go",
		}},
	}

	forward := base
	forward.Tables = []schema.TableDef{usersTableDef(), ordersTableDef()}
	plannedForward, err := forward.Plan()
	require.NoError(t, err)

	reversed := base
	reversed.Tables = []schema.TableDef{ordersTableDef(), usersTableDef()}
	plannedReversed, err := reversed.Plan()
	require.NoError(t, err)

	require.Equal(t, plannedForward.Files(), plannedReversed.Files())
}

// TestStoreRejectsQueryTemplateCount requires a query to carry its template in
// exactly one of Input and SQL.
func TestStoreRejectsQueryTemplateCount(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "query.sql")
	require.NoError(t, os.WriteFile(input, []byte("SELECT 1"), 0o600))
	base := generate.Store{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Tables:  []schema.TableDef{usersTableDef()},
		Dialect: dialect.PostgreSQL(),
	}

	testCases := []struct {
		name    string
		query   generate.Query
		message string
	}{
		{
			name:    "neither",
			query:   generate.Query{Function: "Query", Output: "query_gen.go"},
			message: "input or sql is required",
		},
		{
			name:    "both",
			query:   generate.Query{Input: input, SQL: "SELECT 1", Function: "Query", Output: "query_gen.go"},
			message: "input and sql cannot both be set",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			store := base
			store.Queries = []generate.Query{testCase.query}
			_, err := store.Plan()
			require.ErrorContains(t, err, testCase.message)
		})
	}
}
