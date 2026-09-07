package ddlcompile_test

import (
	"sync"
	"testing"

	"github.com/lestrrat-go/rasql/internal/ddlcompile"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
)

func ddlProfile(t *testing.T) engineprofile.Profile {
	t.Helper()
	p, err := engineprofile.Builtin("postgresql-17", engineprofile.Version{Known: true, Major: 17})
	require.NoError(t, err)
	return p
}

func ddlTable() schema.TableDef {
	return schema.TableDef{
		Schema: "audit",
		Name:   "events",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "name", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes:    []schema.IndexDef{{Name: "events_name_idx", Columns: []string{"name"}, Unique: true}},
	}
}

func TestAcceptanceDDLRetainsTableIndexAndDropFidelity(t *testing.T) {
	c, err := ddlcompile.New(ddlProfile(t))
	require.NoError(t, err)
	table := ddlTable()

	tableStatements, err := c.CreateTable(table)
	require.NoError(t, err)
	require.Len(t, tableStatements, 1)
	require.Equal(t, `CREATE TABLE "audit"."events" ("id" BIGINT NOT NULL, "name" TEXT NOT NULL, PRIMARY KEY ("id"))`, tableStatements[0].SQL())
	require.Empty(t, tableStatements[0].Args())

	indexStatements, err := c.CreateIndexes(table)
	require.NoError(t, err)
	require.Len(t, indexStatements, 1)
	require.Equal(t, `CREATE UNIQUE INDEX "events_name_idx" ON "audit"."events" ("name")`, indexStatements[0].SQL())

	drop, err := c.DropTable(schema.ObjectName{Schema: "audit", Name: "events"})
	require.NoError(t, err)
	require.Equal(t, `DROP TABLE "audit"."events"`, drop.SQL())
}

func TestAcceptanceDDLZeroCompilerRejectsEveryEntryPoint(t *testing.T) {
	var c ddlcompile.Compiler
	_, err := c.CreateTable(ddlTable())
	require.ErrorIs(t, err, engineprofile.ErrInvalidProfile)
	_, err = c.CreateIndexes(ddlTable())
	require.ErrorIs(t, err, engineprofile.ErrInvalidProfile)
	_, err = c.DropTable(schema.ObjectName{Name: "events"})
	require.ErrorIs(t, err, engineprofile.ErrInvalidProfile)
}

func TestAcceptanceDDLReturnsNoPartialOutputOnFailure(t *testing.T) {
	c, err := ddlcompile.New(ddlProfile(t))
	require.NoError(t, err)
	table := ddlTable()
	table.Indexes = []schema.IndexDef{
		{Name: "good_idx", Columns: []string{"name"}},
		{Name: "bad_idx", Expressions: []sqltext.Text{"lower(name)"}},
	}
	statements, err := c.CreateIndexes(table)
	require.Error(t, err)
	require.Nil(t, statements)
}

func TestAcceptanceDDLCompilerIsReusableConcurrently(t *testing.T) {
	c, err := ddlcompile.New(ddlProfile(t))
	require.NoError(t, err)
	table := ddlTable()
	const workers = 100
	results := make([]string, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statements, callErr := c.CreateTable(table)
			errs[i] = callErr
			if callErr == nil {
				results[i] = statements[0].SQL()
			}
		}(i)
	}
	wg.Wait()
	for i := range results {
		require.NoError(t, errs[i])
		require.Equal(t, results[0], results[i])
	}
}
