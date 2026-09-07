package sqlite

import (
	"database/sql"
	"strconv"
	"strings"
	"testing"

	sqlitequery "github.com/lestrrat-go/rasql-sqlite/query"
	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestChooseRebuildNameChecksQualifiedLeafAndSeparateDirections(t *testing.T) {
	table := sqlitequery.QualifiedName{{Name: "main"}, {Name: "Tasks"}}
	occupied := map[string]struct{}{
		"tasks__rasql_rebuild": {},
		qualifiedNameKey(sqlitequery.QualifiedName{{Name: "main"}, {Name: "tasks__rasql_rebuild_2"}}): {},
	}
	forward, err := chooseRebuildName(table, occupied)
	require.NoError(t, err)
	require.Equal(t, "Tasks__rasql_rebuild_3", forward[len(forward)-1].Name)
	occupied[qualifiedNameKey(forward)] = struct{}{}
	reverse, err := chooseRebuildName(table, occupied)
	require.NoError(t, err)
	require.Equal(t, "Tasks__rasql_rebuild_4", reverse[len(reverse)-1].Name)
}

func TestChooseRebuildNameExhaustionIsBounded(t *testing.T) {
	occupied := make(map[string]struct{}, 1000)
	for i := 1; i <= 1000; i++ {
		name := "tasks__rasql_rebuild"
		if i > 1 {
			name = "tasks__rasql_rebuild_" + strconv.Itoa(i)
		}
		occupied[sqliteIdentifierKey(name)] = struct{}{}
		occupied[qualifiedNameKey(sqlitequery.QualifiedName{{Name: name}})] = struct{}{}
	}
	_, err := chooseRebuildName(sqlitequery.QualifiedName{{Name: "tasks"}}, occupied)
	require.EqualError(t, err, "sqlite schema diff: table tasks has no available rebuild temporary name")
}

func TestCloneCreateTableReportsSerializationFailure(t *testing.T) {
	_, err := cloneCreateTable(nil)
	require.Error(t, err)
	_, err = buildRebuildCarrier(tableDefinition{}, tableDefinition{}, nil, nil, nil, LiveCatalogFacts{}, true)
	require.EqualError(t, err, "sqlite schema diff: clone rebuild carrier: table AST is nil")
}

func TestCloneCarrierPreservesNestedSQLiteFacts(t *testing.T) {
	analyzer := New()
	snapshot, err := analyzer.Parse([]diff.Source{{Path: "schema.sql", SQL: `CREATE TABLE "main"."a.b" (
  "id" INTEGER PRIMARY KEY,
  "amount" DECIMAL(10,2) COLLATE "NOCASE" DEFAULT 1 CHECK (amount > 0),
  CONSTRAINT "parent.fk" FOREIGN KEY ("id") REFERENCES "main"."parents" ("id")
);
CREATE INDEX "main"."a.b_idx" ON "main"."a.b" ("amount" COLLATE "NOCASE" DESC) WHERE amount > 0;`}})
	require.NoError(t, err)
	parsed := snapshot.(*schemaSnapshot)
	original := parsed.tables[qualifiedNameKey(sqlitequery.QualifiedName{{Name: "main"}, {Name: "a.b"}})]
	original.foreignKeys = []foreignKeyActions{{onDelete: "CASCADE", onUpdate: "RESTRICT"}}
	clone, err := cloneTable(original)
	require.NoError(t, err)
	require.Equal(t, original.statement.Columns, clone.statement.Columns)
	require.Equal(t, original.statement.Constraints, clone.statement.Constraints)
	require.Equal(t, original.foreignKeys, clone.foreignKeys)
	original.statement.Columns[1].Type.Words[0] = "BROKEN"
	original.statement.Constraints[0].References.Columns[0].Name = "broken"
	require.NotEqual(t, "BROKEN", clone.statement.Columns[1].Type.Words[0])
	require.Equal(t, "id", clone.statement.Constraints[0].References.Columns[0].Name)
	index := parsed.indexes[qualifiedNameKey(sqlitequery.QualifiedName{{Name: "main"}, {Name: "a.b_idx"}})]
	indexes, err := cloneIndexes([]indexDefinition{index})
	require.NoError(t, err)
	require.Equal(t, index.statement, indexes[0].statement)
	index.statement.Name[0].Name = "broken_schema"
	index.statement.Table[1].Name = "broken.table"
	index.statement.Elements[0].Expression = nil
	index.statement.Elements[0].Collation = nil
	index.statement.Elements[0].Direction = sqlitequery.SortAscending
	index.statement.Where = nil
	require.NotEqual(t, "broken_schema", indexes[0].statement.Name[0].Name)
	require.NotEqual(t, "broken.table", indexes[0].statement.Table[1].Name)
	require.NotNil(t, indexes[0].statement.Elements[0].Expression)
	require.NotNil(t, indexes[0].statement.Elements[0].Collation)
	require.NotNil(t, indexes[0].statement.Where)
}

func TestMalformedNonNilCarrierCloneFailsBeforePlanMetadata(t *testing.T) {
	malformed := &sqlitequery.CreateTableStatement{
		Name:        sqlitequery.QualifiedName{{Name: "tasks"}},
		Columns:     []sqlitequery.ColumnDefinition{{Name: sqlitequery.Identifier{Name: "id"}}},
		Constraints: []sqlitequery.TableConstraint{{Kind: "UNSUPPORTED"}},
	}
	definition := tableDefinition{statement: malformed, normalized: malformed}
	carrier, err := buildRebuildCarrier(definition, definition, nil, nil, nil, LiveCatalogFacts{}, true)
	require.Error(t, err)
	require.ErrorContains(t, err, "clone")
	require.Empty(t, carrier.forward)
	from := &schemaSnapshot{tables: map[string]tableDefinition{"1:tasks": definition}, indexes: map[string]indexDefinition{}}
	target := &schemaSnapshot{tables: map[string]tableDefinition{"1:tasks": {statement: &sqlitequery.CreateTableStatement{Name: sqlitequery.QualifiedName{{Name: "tasks"}}, Columns: []sqlitequery.ColumnDefinition{{Name: sqlitequery.Identifier{Name: "id"}}, {Name: sqlitequery.Identifier{Name: "extra"}}}}, normalized: &sqlitequery.CreateTableStatement{Name: sqlitequery.QualifiedName{{Name: "tasks"}}, Columns: []sqlitequery.ColumnDefinition{{Name: sqlitequery.Identifier{Name: "id"}}, {Name: sqlitequery.Identifier{Name: "extra"}}}}}}, indexes: map[string]indexDefinition{}}
	plan, err := New().Diff(from, target)
	require.ErrorContains(t, err, "clone")
	require.Empty(t, plan.Operations)
	require.Empty(t, plan.Statements)
}

func TestCarrierRetainsCatalogNamesAcrossPlanAndResolve(t *testing.T) {
	analyzer := New()
	base := analyzer.Parse
	baseline, err := base([]diff.Source{{Path: "base.sql", SQL: "CREATE TABLE tasks (id INTEGER PRIMARY KEY, name TEXT);"}})
	require.NoError(t, err)
	target, err := base([]diff.Source{{Path: "target.sql", SQL: "CREATE TABLE tasks (id INTEGER PRIMARY KEY, name TEXT, owner_label TEXT NOT NULL);"}})
	require.NoError(t, err)
	baseline, err = analyzer.AttachLiveCatalog(baseline, LiveCatalogFacts{ObjectNames: []string{"tasks__rasql_rebuild"}})
	require.NoError(t, err)
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Len(t, plan.Decisions, 1)
	first, err := plan.Resolve(diff.Resolution{DecisionID: plan.Decisions[0].ID, BackfillSQL: "UPDATE tasks SET owner_label = 'filled';"})
	require.NoError(t, err)
	foundTemporary := false
	for _, statement := range first.Statements {
		if strings.Contains(statement.SQL, "tasks__rasql_rebuild_2") {
			foundTemporary = true
		}
	}
	require.True(t, foundTemporary)
	first.Statements[0].SQL = "mutated"
	second, err := plan.Resolve(diff.Resolution{DecisionID: plan.Decisions[0].ID, BackfillSQL: "UPDATE tasks SET owner_label = 'filled';"})
	require.NoError(t, err)
	require.NotEqual(t, "mutated", second.Statements[0].SQL)
}

func TestResolveIsolationPreservesSQLiteCarrierAndPreview(t *testing.T) {
	analyzer := New()
	baseline, err := analyzer.Parse([]diff.Source{{Path: "base.sql", SQL: "CREATE TABLE tasks (id INTEGER PRIMARY KEY, name TEXT); CREATE INDEX tasks_name_idx ON tasks(name);"}})
	require.NoError(t, err)
	target, err := analyzer.Parse([]diff.Source{{Path: "target.sql", SQL: "CREATE TABLE tasks (id INTEGER PRIMARY KEY, name TEXT, owner_label TEXT NOT NULL); CREATE INDEX tasks_name_idx ON tasks(name);"}})
	require.NoError(t, err)
	baseline, err = analyzer.AttachLiveCatalog(baseline, LiveCatalogFacts{ObjectNames: []string{"tasks__rasql_rebuild"}})
	require.NoError(t, err)
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	previewDecisions := append([]diff.RequiredDecision(nil), plan.Decisions...)
	previewOperations := append([]diff.ProposedOperation(nil), plan.Operations...)
	require.Empty(t, plan.Statements)

	parsed := baseline.(*schemaSnapshot)
	for _, table := range parsed.tables {
		table.statement.Name[0].Name = "mutated"
		if len(table.statement.Columns) > 0 {
			table.statement.Columns[0].Name.Name = "mutated"
		}
		if len(table.normalized.Columns) > 0 {
			table.normalized.Columns[0].Type.Words = []string{"BROKEN"}
		}
	}
	for _, index := range parsed.indexes {
		index.statement.Name[0].Name = "mutated"
		if len(index.statement.Elements) > 0 {
			index.statement.Elements[0].Direction = "BROKEN"
		}
	}
	parsed.liveFacts.ObjectNames[0] = "mutated"

	first, err := plan.Resolve(diff.Resolution{DecisionID: plan.Decisions[0].ID, BackfillSQL: "UPDATE tasks SET owner_label = 'owner-one';"})
	require.NoError(t, err)
	second, err := plan.Resolve(diff.Resolution{DecisionID: plan.Decisions[0].ID, BackfillSQL: "UPDATE tasks SET owner_label = 'owner-two';"})
	require.NoError(t, err)
	require.NotEqual(t, first.Statements, second.Statements)

	mutated := first
	mutated.Dialect = "broken"
	mutated.Operations[0].Summary = "broken"
	mutated.Operations[0].Forward[0].SQL = "broken"
	mutated.Operations[0].Reverse[0].SQL = "broken"
	mutated.Statements[0].SQL = "broken"
	untouched, err := plan.Resolve(diff.Resolution{DecisionID: plan.Decisions[0].ID, BackfillSQL: "UPDATE tasks SET owner_label = 'owner-one';"})
	require.NoError(t, err)
	require.Equal(t, second.Statements, func() []diff.PlannedStatement {
		result, resolveErr := plan.Resolve(diff.Resolution{DecisionID: plan.Decisions[0].ID, BackfillSQL: "UPDATE tasks SET owner_label = 'owner-two';"})
		require.NoError(t, resolveErr)
		return result.Statements
	}())
	require.Equal(t, previewDecisions, plan.Decisions)
	require.Equal(t, previewOperations, plan.Operations)
	require.Empty(t, plan.Statements)
	require.NotEqual(t, "broken", untouched.Statements[0].SQL)
}

func TestInspectLiveCatalogCanonicalizesOwnersAndSortsUnsafeNames(t *testing.T) {
	db, err := sql.Open("sqlite", "file:metadata-facts?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE "Tasks" (id INTEGER PRIMARY KEY);
CREATE TABLE other (id INTEGER);
CREATE TABLE "a.b" (id INTEGER PRIMARY KEY);
CREATE TRIGGER z_trigger AFTER INSERT ON "Tasks" BEGIN SELECT 1; END;
CREATE TRIGGER unrelated AFTER INSERT ON other BEGIN SELECT 1; END;
CREATE TRIGGER dot_trigger AFTER INSERT ON "a.b" BEGIN SELECT 1; END;
CREATE VIEW z_view AS SELECT id FROM "Tasks";
CREATE VIEW a_view AS SELECT id FROM other;`)
	require.NoError(t, err)
	facts, err := InspectLiveCatalog(t.Context(), db, `main."tasks"`)
	require.NoError(t, err)
	require.Equal(t, []string{"z_trigger"}, facts.TriggerNames)
	require.Equal(t, []string{"a_view", "z_view"}, facts.ViewNames)
	_, err = New().AttachLiveCatalog((&schemaSnapshot{}), facts)
	require.ErrorContains(t, err, "[a_view z_trigger z_view]")
	dotFacts, err := InspectLiveCatalog(t.Context(), db, `main."a.b"`)
	require.NoError(t, err)
	require.Equal(t, []string{"dot_trigger"}, dotFacts.TriggerNames)
}

func TestSQLiteNameLeafDecodesQuotedEscapes(t *testing.T) {
	for _, test := range []struct {
		name string
		want string
	}{
		{name: `main."a.b"`, want: "a.b"},
		{name: "main.\"a\"\"b\"", want: `a"b`},
		{name: "main.`a``b`", want: "a`b"},
		{name: "main.[a]]b]", want: "a]b"},
		{name: `MAIN."Tasks"`, want: "Tasks"},
	} {
		t.Run(test.name, func(t *testing.T) { require.Equal(t, test.want, sqliteNameLeaf(test.name)) })
	}
}
