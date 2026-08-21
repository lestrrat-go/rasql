package rasql_test

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestInsertOmitsGeneratedColumn proves that a generated column never
// reaches the default INSERT column list, because a database rejects a
// statement that targets one explicitly. Before this fix, typedInsertMany
// built its column list from every descriptor column except the ones an
// explicit rasql.DefaultColumns names, so a generated column reached the
// statement unless a caller remembered to exclude it by hand -- exactly the
// gap a rasqlgen-generated store for a table with a generated column would
// hit on its very first insert.
func TestInsertOmitsGeneratedColumn(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	type measurement struct {
		ID         int64 `rasql:"id"`
		Celsius    int64 `rasql:"celsius"`
		Fahrenheit int64 `rasql:"fahrenheit"`
	}
	measurements, err := rasql.TableOf[measurement](schema.TableDef{
		Name: "measurements",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "celsius", Type: schema.IntegerType{}},
			{
				Name:                "fahrenheit",
				Type:                schema.IntegerType{},
				GeneratedExpression: "celsius * 9 / 5 + 32",
				GeneratedStorage:    schema.GeneratedStored,
			},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)

	// The expectation itself is the pin: it never mentions "fahrenheit". If
	// the generated column reached the statement, sqlmock would report an
	// unmatched query rather than this expectation firing.
	mock.ExpectExec(`INSERT INTO "measurements" ("id", "celsius") VALUES ($1, $2)`).
		WithArgs(int64(1), int64(20)).
		WillReturnResult(sqlmock.NewResult(1, 1))

	_, err = rasql.Insert(t.Context(), db, measurements, measurement{ID: 1, Celsius: 20, Fahrenheit: 68})
	require.NoError(t, err)
}

// TestUpdateOmitsGeneratedColumn is the UPDATE counterpart to
// TestInsertOmitsGeneratedColumn: typedUpdateWithOptions built its default
// assignment list from every non-primary-key descriptor column, so a
// generated column reached a plain rasql.Update the same way it reached a
// plain rasql.Insert.
func TestUpdateOmitsGeneratedColumn(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	type measurement struct {
		ID         int64 `rasql:"id"`
		Celsius    int64 `rasql:"celsius"`
		Fahrenheit int64 `rasql:"fahrenheit"`
	}
	measurements, err := rasql.TableOf[measurement](schema.TableDef{
		Name: "measurements",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "celsius", Type: schema.IntegerType{}},
			{
				Name:                "fahrenheit",
				Type:                schema.IntegerType{},
				GeneratedExpression: "celsius * 9 / 5 + 32",
				GeneratedStorage:    schema.GeneratedStored,
			},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)

	mock.ExpectExec(`UPDATE "measurements" SET "celsius" = $1 WHERE ("measurements"."id" = $2)`).
		WithArgs(int64(21), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	_, err = rasql.Update(t.Context(), db, measurements, measurement{ID: 1, Celsius: 21, Fahrenheit: 70})
	require.NoError(t, err)
}

// TestUpdateColumnsRejectsGeneratedColumn proves that a caller who explicitly
// names a generated column via rasql.UpdateColumns is refused up front, the
// same way naming a primary key is, rather than left to fail against the
// database once the statement reaches it.
func TestUpdateColumnsRejectsGeneratedColumn(t *testing.T) {
	type measurement struct {
		ID         int64 `rasql:"id"`
		Celsius    int64 `rasql:"celsius"`
		Fahrenheit int64 `rasql:"fahrenheit"`
	}
	measurements, err := rasql.TableOf[measurement](schema.TableDef{
		Name: "measurements",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "celsius", Type: schema.IntegerType{}},
			{
				Name:                "fahrenheit",
				Type:                schema.IntegerType{},
				GeneratedExpression: "celsius * 9 / 5 + 32",
				GeneratedStorage:    schema.GeneratedStored,
			},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)

	_, err = rasql.UpdateWithOptions(t.Context(), buildOnlyDB(t), measurements, measurement{}, rasql.UpdateColumns("fahrenheit"))
	require.ErrorContains(t, err, `column "fahrenheit" is generated and cannot be updated`)
}

// identityMember is the row type the identity-column tests below share: id
// is an ALWAYS identity primary key, external_ref is an ALWAYS identity
// column that is not the primary key (so its exclusion is pinned to
// Identity rather than to already being a primary key), legacy_id is a BY
// DEFAULT identity column, and name is an ordinary column.
type identityMember struct {
	ID          int64  `rasql:"id"`
	ExternalRef int64  `rasql:"external_ref"`
	LegacyID    int64  `rasql:"legacy_id"`
	Name        string `rasql:"name"`
}

func identityMembersTable(t *testing.T) rasql.Table[identityMember] {
	t.Helper()
	table, err := rasql.TableOf[identityMember](schema.TableDef{
		Name: "members",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}, Identity: schema.IdentityAlways},
			{Name: "external_ref", Type: schema.IntegerType{}, Identity: schema.IdentityAlways},
			{Name: "legacy_id", Type: schema.IntegerType{}, Identity: schema.IdentityByDefault},
			{Name: "name", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	return table
}

// TestInsertOmitsAlwaysIdentityColumn proves that an ALWAYS identity
// column never reaches the default INSERT column list, because PostgreSQL
// rejects an explicit value for one ("cannot insert a non-DEFAULT value").
// A BY DEFAULT identity column, legacy_id, stays in the list: it accepts
// an explicit value, so its absence from the expectation below would be
// the wrong assertion to make.
func TestInsertOmitsAlwaysIdentityColumn(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	members := identityMembersTable(t)

	// Neither "id" nor "external_ref" appears: both are ALWAYS identity
	// columns. "legacy_id" does appear: it is BY DEFAULT, which accepts an
	// explicit value.
	mock.ExpectExec(`INSERT INTO "members" ("legacy_id", "name") VALUES ($1, $2)`).
		WithArgs(int64(7), "Ada").
		WillReturnResult(sqlmock.NewResult(1, 1))

	_, err = rasql.Insert(t.Context(), db, members, identityMember{ID: 1, ExternalRef: 2, LegacyID: 7, Name: "Ada"})
	require.NoError(t, err)
}

// TestUpdateOmitsAlwaysIdentityColumn proves that an ALWAYS identity
// column that is not the primary key is still skipped from the default
// UPDATE assignment list, the same way a generated column is: PostgreSQL
// rejects an UPDATE naming one ("column can only be updated to DEFAULT").
// legacy_id, the BY DEFAULT identity column, is left in the assignment
// list, since it is updatable like any other column.
func TestUpdateOmitsAlwaysIdentityColumn(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	members := identityMembersTable(t)

	mock.ExpectExec(`UPDATE "members" SET "legacy_id" = $1, "name" = $2 WHERE ("members"."id" = $3)`).
		WithArgs(int64(9), "Grace", int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	_, err = rasql.Update(t.Context(), db, members, identityMember{ID: 1, ExternalRef: 2, LegacyID: 9, Name: "Grace"})
	require.NoError(t, err)
}

// TestUpdateColumnsRejectsAlwaysIdentityColumn proves that a caller who
// explicitly names an ALWAYS identity column via rasql.UpdateColumns is
// refused up front, the same way naming a generated column is.
func TestUpdateColumnsRejectsAlwaysIdentityColumn(t *testing.T) {
	members := identityMembersTable(t)

	_, err := rasql.UpdateWithOptions(t.Context(), buildOnlyDB(t), members, identityMember{}, rasql.UpdateColumns("external_ref"))
	require.ErrorContains(t, err, `column "external_ref" is an ALWAYS identity column and cannot be updated`)
}

// TestUpdateColumnsAcceptsByDefaultIdentityColumn proves that a BY DEFAULT
// identity column named through rasql.UpdateColumns is accepted and
// updated like any other column, unlike its ALWAYS counterpart.
func TestUpdateColumnsAcceptsByDefaultIdentityColumn(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	members := identityMembersTable(t)

	mock.ExpectExec(`UPDATE "members" SET "legacy_id" = $1 WHERE ("members"."id" = $2)`).
		WithArgs(int64(42), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	_, err = rasql.UpdateWithOptions(t.Context(), db, members, identityMember{ID: 1, LegacyID: 42}, rasql.UpdateColumns("legacy_id"))
	require.NoError(t, err)
}
