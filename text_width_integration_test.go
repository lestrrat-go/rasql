//go:build unix

package rasql_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestTextWidthAgainstLiveDatabases pins what a live server actually reports
// back for a text column whose width and fixed-ness rasql stated, because
// every other test of that path uses a mocked catalog. The claims under test
// are the ones the width work rests on: that a stated width reaches the
// server as VARCHAR(n) or CHAR(n), that the server's own catalog reports it
// back, and that inspecting the column reproduces the descriptor rasql
// started from rather than a wider or unbounded one.
//
// The unstated-width cases are here for the same reason as the stated ones.
// "MySQL reports TEXT with no length" is an assumption the inspector encodes,
// and a mocked row cannot confirm it.
func TestTextWidthAgainstLiveDatabases(t *testing.T) {
	for _, test := range []struct {
		name    string
		open    func(*testing.T) *sql.DB
		dialect dialect.Dialect
		// fixedWidth is what the dialect is expected to report back for a
		// column rasql declared as fixed-width. PostgreSQL and MySQL both
		// keep it; the field exists so a dialect that does not can say so
		// rather than be silently skipped.
		fixedRoundTrips bool
	}{
		{
			name:            "postgresql",
			open:            dbtest.PostgreSQLDB,
			dialect:         dialect.PostgreSQL(),
			fixedRoundTrips: true,
		},
		{
			name:            "mysql",
			open:            dbtest.MySQLDB,
			dialect:         dialect.MySQL(),
			fixedRoundTrips: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			database := test.open(t)
			db, err := rasql.New(database, test.dialect)
			require.NoError(t, err, "create rasql db")

			inspector, err := inspect.New(database, test.dialect)
			require.NoError(t, err, "create inspector")

			for _, column := range []struct {
				name    string
				table   string
				options []schema.ColumnOption
				// want is the column type the live catalog must report back.
				want schema.ColumnType
			}{
				{
					name:    "variable width states its length",
					table:   "text_width_varchar",
					options: []schema.ColumnOption{schema.Width(255)},
					want:    schema.TextType{Width: schema.NewTextWidth(255)},
				},
				{
					name:    "fixed width states its length and fixedness",
					table:   "text_width_char",
					options: []schema.ColumnOption{schema.Width(36), schema.Fixed()},
					want:    schema.TextType{Width: schema.NewTextWidth(36), Fixed: true},
				},
				{
					name:    "unstated width stays unstated",
					table:   "text_width_plain",
					options: nil,
					want:    schema.TextType{},
				},
			} {
				t.Run(column.name, func(t *testing.T) {
					definition := schema.MustTableDef(column.table,
						schema.Integer("id"),
						schema.Text("label", column.options...),
						schema.PrimaryKey("id"),
					)
					table := rasql.MustTableOf[struct{}](definition)
					require.NoError(t, rasql.CreateTable(ctx, db, table), "create table")

					inspected, err := inspector.Table(ctx, column.table)
					require.NoError(t, err, "inspect table")

					var label schema.ColumnDef
					for _, candidate := range inspected.Columns {
						if candidate.Name == "label" {
							label = candidate
						}
					}
					require.Equal(t, "label", label.Name, "inspected table has a label column")
					require.Equal(t, column.want, label.Type,
						"the live catalog reports back the width and fixedness rasql declared")
				})
			}
		})
	}
}

// TestIndexedTextRequiresWidthOnMySQL pins the reason width exists at all.
// MySQL refuses to build a key over an unbounded TEXT column with error 1170,
// so rasql refuses to render such a statement, and a stated width is what
// makes the column indexable. Both halves are asserted against the server:
// that the bounded column really is indexed, confirmed by reading the index
// back from MySQL's own information_schema.statistics rather than trusting
// CreateTable's nil error to mean an index was ever issued, and that MySQL
// really does reject the unbounded one with error 1170 when the SQL reaches
// it directly.
func TestIndexedTextRequiresWidthOnMySQL(t *testing.T) {
	ctx := context.Background()
	database := dbtest.MySQLDB(t)
	db, err := rasql.New(database, dialect.MySQL())
	require.NoError(t, err, "create rasql db")

	definition := schema.MustTableDef("indexed_text_width",
		schema.Integer("id"),
		schema.Text("label", schema.Width(191)),
		schema.PrimaryKey("id"),
		schema.Index("indexed_text_width_label", "label"),
	)
	table := rasql.MustTableOf[struct{}](definition)
	require.NoError(t, rasql.CreateTable(ctx, db, table),
		"a bounded text column is indexable on MySQL")

	// CreateTable returning nil only means MySQL accepted the statements it
	// sent; it says nothing about what those statements were. Reading the
	// index back from information_schema.statistics -- scoped to the
	// current database with DATABASE() so a same-named index elsewhere
	// cannot make this pass by accident -- is what actually proves the
	// column is indexable, which is the claim this test exists to pin.
	var indexedColumn string
	err = database.QueryRowContext(ctx,
		"SELECT column_name FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?",
		"indexed_text_width", "indexed_text_width_label",
	).Scan(&indexedColumn)
	require.NoError(t, err,
		"MySQL's catalog must show an index named indexed_text_width_label on indexed_text_width, or CreateTable succeeding proved nothing about indexability")
	require.Equal(t, "label", indexedColumn,
		"the index MySQL's catalog reports must cover the label column, since an index that exists but covers the wrong column would not make label indexable either")

	_, err = database.ExecContext(ctx,
		"CREATE TABLE unbounded_text_key (id BIGINT NOT NULL, label TEXT NOT NULL, PRIMARY KEY (id), INDEX (label))")
	require.Error(t, err,
		"MySQL itself refuses a key over an unbounded TEXT column, which is why rasql refuses to render one")
	require.Contains(t, err.Error(), "1170",
		"the refusal is MySQL error 1170, the one rasql's render-time check exists to pre-empt")
}
