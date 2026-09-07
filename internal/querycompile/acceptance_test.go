package querycompile_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/querycompile"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

func queryProfile(t *testing.T) engineprofile.Profile {
	t.Helper()
	p, err := engineprofile.Builtin("postgresql-17", engineprofile.Version{Known: true, Major: 17})
	require.NoError(t, err)
	return p
}

func queryTable(t *testing.T) query.TableRef {
	t.Helper()
	return query.MustTableRef(schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "name", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
}

func TestAcceptanceQueryCompilesSelectAndWritesWithExactSQLAndArgs(t *testing.T) {
	c, err := querycompile.New(queryProfile(t))
	require.NoError(t, err)
	users := queryTable(t)

	selectQuery, err := query.NewSelect(users, users.Column("id"), users.Column("name"))
	require.NoError(t, err)
	selectQuery, err = selectQuery.WithWhere(query.Equal(users.Column("id"), 7))
	require.NoError(t, err)
	result, err := query.ResultOf(selectQuery,
		query.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		query.ResultColumn{Name: "name", Type: schema.TextType{}},
	)
	require.NoError(t, err)
	selectStatement, err := c.Select(result)
	require.NoError(t, err)
	require.Equal(t, `SELECT "users"."id", "users"."name" FROM "users" WHERE ("users"."id" = $1)`, selectStatement.SQL())
	require.Equal(t, []any{7}, selectStatement.Args())

	insertQuery, err := query.NewInsert(users, query.Set(users.Column("id"), 7), query.Set(users.Column("name"), "Ada"))
	require.NoError(t, err)
	insertStatement, err := c.Write(insertQuery)
	require.NoError(t, err)
	require.Equal(t, `INSERT INTO "users" ("id", "name") VALUES ($1, $2)`, insertStatement.SQL())
	require.Equal(t, []any{7, "Ada"}, insertStatement.Args())

	updateQuery, err := query.NewUpdate(users, query.Set(users.Column("name"), "Grace"))
	require.NoError(t, err)
	updateQuery, err = updateQuery.WithWhere(query.Equal(users.Column("id"), 7))
	require.NoError(t, err)
	updateStatement, err := c.Write(updateQuery)
	require.NoError(t, err)
	require.Equal(t, `UPDATE "users" SET "name" = $1 WHERE ("users"."id" = $2)`, updateStatement.SQL())
	require.Equal(t, []any{"Grace", 7}, updateStatement.Args())

	deleteQuery, err := query.NewDelete(users)
	require.NoError(t, err)
	deleteQuery, err = deleteQuery.WithWhere(query.Equal(users.Column("id"), 7))
	require.NoError(t, err)
	deleteStatement, err := c.Write(deleteQuery)
	require.NoError(t, err)
	require.Equal(t, `DELETE FROM "users" WHERE ("users"."id" = $1)`, deleteStatement.SQL())
	require.Equal(t, []any{7}, deleteStatement.Args())
}

func TestAcceptanceQueryCompilesEveryWriteAndOrderedSelectForAllBuiltins(t *testing.T) {
	users := queryTable(t)
	id, name := users.Column("id"), users.Column("name")
	base, err := query.NewSelect(users, id, name)
	require.NoError(t, err)
	base, err = base.WithWhere(query.GreaterThan(id, 3))
	require.NoError(t, err)
	base, err = base.WithOrder(query.Desc(name), query.Asc(id))
	require.NoError(t, err)
	base, err = base.WithLimit(5)
	require.NoError(t, err)
	base, err = base.WithOffset(2)
	require.NoError(t, err)
	result, err := query.ResultOf(base,
		query.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		query.ResultColumn{Name: "name", Type: schema.TextType{}},
	)
	require.NoError(t, err)
	insert, err := query.NewInsert(users, query.Set(id, 7), query.Set(name, "Ada"))
	require.NoError(t, err)
	update, err := query.NewUpdate(users, query.Set(name, "Grace"))
	require.NoError(t, err)
	update, err = update.WithWhere(query.Equal(id, 7))
	require.NoError(t, err)
	deleteStatement, err := query.NewDelete(users)
	require.NoError(t, err)
	deleteStatement, err = deleteStatement.WithWhere(query.Equal(id, 7))
	require.NoError(t, err)
	upsert, err := query.NewUpsert(insert, []query.ColumnRef{id}, []query.Assignment{query.Set(name, query.Excluded(name))})
	require.NoError(t, err)

	tests := []struct {
		name       string
		profileID  string
		version    engineprofile.Version
		selectSQL  string
		writeSQL   []string
		selectArgs []any
	}{
		{
			name: "postgresql", profileID: "postgresql-17", version: engineprofile.Version{Known: true, Major: 17},
			selectSQL: `SELECT "users"."id", "users"."name" FROM "users" WHERE ("users"."id" > $1) ORDER BY "users"."name" DESC, "users"."id" LIMIT $2 OFFSET $3`,
			writeSQL: []string{
				`INSERT INTO "users" ("id", "name") VALUES ($1, $2)`,
				`UPDATE "users" SET "name" = $1 WHERE ("users"."id" = $2)`,
				`DELETE FROM "users" WHERE ("users"."id" = $1)`,
				`INSERT INTO "users" ("id", "name") VALUES ($1, $2) ON CONFLICT ("id") DO UPDATE SET "name" = EXCLUDED."name"`,
			},
			selectArgs: []any{3, 5, 2},
		},
		{
			name: "mysql", profileID: "mysql-8.4", version: engineprofile.Version{Known: true, Major: 8, Minor: 4},
			selectSQL: "SELECT `users`.`id`, `users`.`name` FROM `users` WHERE (`users`.`id` > ?) ORDER BY `users`.`name` DESC, `users`.`id` LIMIT ? OFFSET ?",
			writeSQL: []string{
				"INSERT INTO `users` (`id`, `name`) VALUES (?, ?)",
				"UPDATE `users` SET `name` = ? WHERE (`users`.`id` = ?)",
				"DELETE FROM `users` WHERE (`users`.`id` = ?)",
				"INSERT INTO `users` (`id`, `name`) VALUES (?, ?) ON DUPLICATE KEY UPDATE `name` = VALUES(`name`)",
			},
			selectArgs: []any{3, 5, 2},
		},
		{
			name: "sqlite", profileID: "sqlite-3.35", version: engineprofile.Version{Known: true, Major: 3, Minor: 35},
			selectSQL: `SELECT "users"."id", "users"."name" FROM "users" WHERE ("users"."id" > ?) ORDER BY "users"."name" DESC, "users"."id" LIMIT ? OFFSET ?`,
			writeSQL: []string{
				`INSERT INTO "users" ("id", "name") VALUES (?, ?)`,
				`UPDATE "users" SET "name" = ? WHERE ("users"."id" = ?)`,
				`DELETE FROM "users" WHERE ("users"."id" = ?)`,
				`INSERT INTO "users" ("id", "name") VALUES (?, ?) ON CONFLICT ("id") DO UPDATE SET "name" = EXCLUDED."name"`,
			},
			selectArgs: []any{3, 5, 2},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			profile, profileErr := engineprofile.Builtin(testCase.profileID, testCase.version)
			require.NoError(t, profileErr)
			compiler, compilerErr := querycompile.New(profile)
			require.NoError(t, compilerErr)
			statement, compileErr := compiler.Select(result)
			require.NoError(t, compileErr)
			require.Equal(t, testCase.selectSQL, statement.SQL())
			require.Equal(t, testCase.selectArgs, statement.Args())
			writes := []query.WriteStatement{insert, update, deleteStatement, upsert}
			if testCase.name == "mysql" {
				writes[3], err = query.NewUpsert(insert, nil, []query.Assignment{query.Set(name, query.Excluded(name))})
				require.NoError(t, err)
			}
			for index, write := range writes {
				compiled, writeErr := compiler.Write(write)
				require.NoError(t, writeErr)
				require.Equal(t, testCase.writeSQL[index], compiled.SQL())
			}
		})
	}
}

func TestAcceptanceQueryPreservesReturningPresentNilAndNativeArguments(t *testing.T) {
	c, err := querycompile.New(queryProfile(t))
	require.NoError(t, err)
	users := queryTable(t)
	insertQuery, err := query.NewInsert(users, query.Set(users.Column("id"), nil), query.Set(users.Column("name"), "Ada"))
	require.NoError(t, err)
	insertQuery, err = insertQuery.WithReturning(users.Column("id"))
	require.NoError(t, err)
	statement, err := c.Write(insertQuery)
	require.NoError(t, err)
	require.Equal(t, `INSERT INTO "users" ("id", "name") VALUES ($1, $2) RETURNING "id"`, statement.SQL())
	require.Equal(t, []any{nil, "Ada"}, statement.Args())

	input := []byte("payload")
	native, err := c.Native(stmt.New("SELECT $1", input))
	require.NoError(t, err)
	require.Equal(t, "SELECT $1", native.SQL())
	require.Equal(t, []any{input}, native.Args())
	input[0] = 'X'
	require.Equal(t, []byte("payload"), native.Args()[0])
	_, err = c.Native(stmt.New("   "))
	require.Error(t, err)
}

func TestAcceptanceQueryRejectsZeroAndBindLimitWithoutOutput(t *testing.T) {
	users := queryTable(t)
	q, err := query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	result, err := query.ResultOf(q, query.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	var zero querycompile.Compiler
	got, err := zero.Select(result)
	require.ErrorIs(t, err, engineprofile.ErrInvalidProfile)
	require.Empty(t, got.SQL())

	c, err := querycompile.New(queryProfile(t))
	require.NoError(t, err)
	args := make([]any, 65536)
	got, err = c.Native(stmt.New("SELECT", args...))
	require.ErrorIs(t, err, engineprofile.ErrBindLimit)
	require.Empty(t, got.SQL())
}

func TestAcceptanceQueryCompilerIsReusableConcurrently(t *testing.T) {
	c, err := querycompile.New(queryProfile(t))
	require.NoError(t, err)
	users := queryTable(t)
	q, err := query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	q, err = q.WithWhere(query.Equal(users.Column("id"), 42))
	require.NoError(t, err)
	result, err := query.ResultOf(q, query.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)

	const workers = 100
	statements := make([]stmt.Statement, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range statements {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statements[i], errs[i] = c.Select(result)
		}(i)
	}
	wg.Wait()
	for i := range statements {
		require.NoError(t, errs[i])
		require.Equal(t, statements[0].SQL(), statements[i].SQL())
		require.Equal(t, []any{42}, statements[i].Args())
	}
}

func TestAcceptanceQueryReturnedArgumentsAndNestedMetadataAreIndependent(t *testing.T) {
	c, err := querycompile.New(queryProfile(t))
	require.NoError(t, err)
	users := queryTable(t)
	payload := []byte("payload")
	body, err := query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	body, err = body.WithWhere(query.Equal(users.Column("name"), payload))
	require.NoError(t, err)
	result, err := query.ResultOf(body, query.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	first, err := c.Select(result)
	require.NoError(t, err)
	second, err := c.Select(result)
	require.NoError(t, err)
	firstArgs := first.BoundArgs()
	firstArgs[0].([]byte)[0] = 'X'
	require.Equal(t, []byte("payload"), second.Args()[0])

	columns := result.Columns()
	columns[0].Name = "changed"
	require.Equal(t, "id", result.Columns()[0].Name)
}

func TestAcceptanceQueryDialectProfileMismatchIsRejected(t *testing.T) {
	p := queryProfile(t)
	_, err := querycompile.NewWithDialect(p, dialect.SQLite())
	require.Error(t, err)
}

func TestAcceptanceQueryCapabilityChecksIgnoreQuotedIdentifiers(t *testing.T) {
	p, err := engineprofile.Builtin("mysql-8.4", engineprofile.Version{Known: true, Major: 8, Minor: 4})
	require.NoError(t, err)
	c, err := querycompile.New(p)
	require.NoError(t, err)
	table := query.MustTableRef(schema.TableDef{
		Name:    "a RETURNING b",
		Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
	})
	body, err := query.NewSelect(table, table.Column("id"))
	require.NoError(t, err)
	result, err := query.ResultOf(body, query.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	statement, err := c.Select(result)
	require.NoError(t, err)
	require.Equal(t, "SELECT `a RETURNING b`.`id` FROM `a RETURNING b`", statement.SQL())
}

type typedNilDialect struct{}

func (*typedNilDialect) Name() string                              { return "typed-nil" }
func (*typedNilDialect) QuoteIdentifier(string) (string, error)    { return "", nil }
func (*typedNilDialect) Placeholder(int) (string, error)           { return "", nil }
func (*typedNilDialect) TypeName(schema.ColumnDef) (string, error) { return "", nil }
func (*typedNilDialect) UpsertStyle() dialect.UpsertStyle          { return dialect.UpsertUnsupported }
func (*typedNilDialect) Supports(dialect.Capability) bool          { return false }

func TestAcceptanceQueryRejectsTypedNilDialect(t *testing.T) {
	p, err := engineprofile.New("custom:typed-nil", engineprofile.Custom, "typed-nil", engineprofile.Version{}, engineprofile.Capabilities{}, engineprofile.Limits{MaxBindParameters: 1})
	require.NoError(t, err)
	var adapter *typedNilDialect
	_, err = querycompile.NewWithDialect(p, adapter)
	require.Error(t, err)
}

type extensionExpression struct{ value int }

func (extensionExpression) ExpressionNode() {}

type extensionCompiler struct{}

func (extensionCompiler) CompileExpression(emitter dialect.Emitter, expression query.Expression) (bool, error) {
	node, ok := expression.(extensionExpression)
	if !ok {
		return false, nil
	}
	emitter.WriteSQL("CUSTOM(")
	if err := emitter.Argument(node.value); err != nil {
		return true, err
	}
	emitter.WriteSQL(")")
	return true, nil
}

func (extensionCompiler) CompilePagination(dialect.Emitter, dialect.Pagination) error { return nil }

type extensionDialect struct {
	dialect.Dialect
	compiler dialect.Compiler
	compare  func(string, string) bool
}

func (d extensionDialect) Name() string { return "custom-extension" }

func (d extensionDialect) Compiler() dialect.Compiler { return d.compiler }

func (d extensionDialect) IdentifiersEqual(left, right string) bool {
	if d.compare == nil {
		return left == right
	}
	return d.compare(left, right)
}

type bareExtensionDialect struct{ dialect.Dialect }

func (bareExtensionDialect) Name() string { return "custom-extension" }

func customExtensionProfile(t *testing.T) engineprofile.Profile {
	t.Helper()
	base, err := engineprofile.Builtin("postgresql-17", engineprofile.Version{Known: true, Major: 17})
	require.NoError(t, err)
	profile, err := engineprofile.New("custom:extension", engineprofile.Custom, "custom-extension", engineprofile.Version{}, base.Capabilities, engineprofile.Limits{MaxBindParameters: 100})
	require.NoError(t, err)
	return profile
}

func TestAcceptanceQueryPreservesCustomCompilerAndIdentifierExtensions(t *testing.T) {
	profile := customExtensionProfile(t)
	d := extensionDialect{Dialect: dialect.PostgreSQL(), compiler: extensionCompiler{}, compare: func(left, right string) bool {
		return strings.EqualFold(left, right)
	}}
	compiler, err := querycompile.NewWithDialect(profile, d)
	require.NoError(t, err)
	users := queryTable(t)
	body, err := query.NewSelect(users, query.Project(extensionExpression{value: 9}))
	require.NoError(t, err)
	result, err := query.ResultOf(body, query.ResultColumn{Name: "custom", Type: schema.IntegerType{}})
	require.NoError(t, err)
	statement, err := compiler.Select(result)
	require.NoError(t, err)
	require.Equal(t, `SELECT CUSTOM($1) FROM "users"`, statement.SQL())
	require.Equal(t, []any{9}, statement.Args())

	base, err := query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	baseResult, err := query.ResultOf(base, query.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	first, err := query.CommonTable("local", baseResult)
	require.NoError(t, err)
	second, err := query.CommonTable("LOCAL", baseResult)
	require.NoError(t, err)
	outer, err := query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	outer, err = outer.WithCTEs(first, second)
	require.NoError(t, err)
	outerResult, err := query.ResultOf(outer, query.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	_, err = compiler.Select(outerResult)
	require.Error(t, err)
}

func TestAcceptanceQueryRejectsCustomDialectWithoutCompilerProvider(t *testing.T) {
	profile := customExtensionProfile(t)
	compiler, err := querycompile.NewWithDialect(profile, bareExtensionDialect{Dialect: dialect.PostgreSQL()})
	require.NoError(t, err)
	users := queryTable(t)
	body, err := query.NewSelect(users, query.Project(extensionExpression{value: 9}))
	require.NoError(t, err)
	result, err := query.ResultOf(body, query.ResultColumn{Name: "custom", Type: schema.IntegerType{}})
	require.NoError(t, err)
	statement, err := compiler.Select(result)
	require.Error(t, err)
	require.Empty(t, statement.SQL())
}

func customPostgresProfile(t *testing.T, edit func(*engineprofile.Capabilities)) engineprofile.Profile {
	t.Helper()
	base := queryProfile(t)
	caps := base.Capabilities
	edit(&caps)
	p, err := engineprofile.New("custom:acceptance", engineprofile.Custom, "postgresql", engineprofile.Version{}, caps, engineprofile.Limits{MaxBindParameters: 100})
	require.NoError(t, err)
	return p
}

func TestAcceptanceQueryRejectsWindowWhenProfileDisablesIt(t *testing.T) {
	p := customPostgresProfile(t, func(c *engineprofile.Capabilities) {
		c.WindowFunctions = false
		c.PerParentLimit = engineprofile.PerParentLimitUnsupported
	})
	c, err := querycompile.NewWithDialect(p, dialect.PostgreSQL())
	require.NoError(t, err)
	users := queryTable(t)
	body, err := query.NewSelect(users, query.Project(query.OverWindow(query.Func("row_number"), query.Window(nil, query.Asc(users.Column("id"))))).As("row"))
	require.NoError(t, err)
	result, err := query.ResultOf(body, query.ResultColumn{Name: "row", Type: schema.IntegerType{}})
	require.NoError(t, err)
	statement, err := c.Select(result)
	require.ErrorIs(t, err, engineprofile.ErrUnsupportedFeature)
	require.Empty(t, statement.SQL())
}

func TestAcceptanceQueryTraversesCTEForNestedCapabilities(t *testing.T) {
	p := customPostgresProfile(t, func(c *engineprofile.Capabilities) {
		c.WindowFunctions = false
		c.PerParentLimit = engineprofile.PerParentLimitUnsupported
	})
	c, err := querycompile.NewWithDialect(p, dialect.PostgreSQL())
	require.NoError(t, err)
	users := queryTable(t)
	body, err := query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	result, err := query.ResultOf(body, query.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	cte, err := query.CommonTable("recent", result)
	require.NoError(t, err)
	cteBody, err := query.NewSelect(users, query.Project(query.OverWindow(query.Func("row_number"), query.Window(nil, query.Asc(users.Column("id"))))).As("row"))
	require.NoError(t, err)
	cteBody, err = cteBody.WithCTEs(cte)
	require.NoError(t, err)
	cteResult, err := query.ResultOf(cteBody, query.ResultColumn{Name: "row", Type: schema.IntegerType{}})
	require.NoError(t, err)
	statement, err := c.Select(cteResult)
	require.ErrorIs(t, err, engineprofile.ErrUnsupportedFeature)
	require.Empty(t, statement.SQL())
}

func TestAcceptanceQueryRejectsCapabilitiesOnlyInNestedCTE(t *testing.T) {
	p := customPostgresProfile(t, func(c *engineprofile.Capabilities) {
		c.WindowFunctions = false
		c.PerParentLimit = engineprofile.PerParentLimitUnsupported
	})
	c, err := querycompile.NewWithDialect(p, dialect.PostgreSQL())
	require.NoError(t, err)
	users := queryTable(t)
	windowBody, err := query.NewSelect(users, query.Project(query.OverWindow(query.Func("row_number"), query.Window(nil, query.Asc(users.Column("id"))))).As("row"))
	require.NoError(t, err)
	windowResult, err := query.ResultOf(windowBody, query.ResultColumn{Name: "row", Type: schema.IntegerType{}})
	require.NoError(t, err)
	inner, err := query.CommonTable("inner_rows", windowResult)
	require.NoError(t, err)
	innerRef, err := inner.Ref("")
	require.NoError(t, err)
	outerBody, err := query.NewSelect(innerRef, innerRef.Column("row"))
	require.NoError(t, err)
	outerResult, err := query.ResultOf(outerBody, query.ResultColumn{Name: "row", Type: schema.IntegerType{}})
	require.NoError(t, err)
	outer, err := query.CommonTable("outer_rows", outerResult)
	require.NoError(t, err)
	outerRef, err := outer.Ref("")
	require.NoError(t, err)
	rootBody, err := query.NewSelect(outerRef, outerRef.Column("row"))
	require.NoError(t, err)
	rootBody, err = rootBody.WithCTEs(outer, inner)
	require.NoError(t, err)
	rootResult, err := query.ResultOf(rootBody, query.ResultColumn{Name: "row", Type: schema.IntegerType{}})
	require.NoError(t, err)
	statement, err := c.Select(rootResult)
	require.ErrorIs(t, err, engineprofile.ErrUnsupportedFeature)
	require.Empty(t, statement.SQL())
}

func TestAcceptanceQueryReturningInsertOnlyRejectsUpdateAndDelete(t *testing.T) {
	p := customPostgresProfile(t, func(c *engineprofile.Capabilities) { c.Returning = engineprofile.ReturningInsert })
	c, err := querycompile.NewWithDialect(p, dialect.PostgreSQL())
	require.NoError(t, err)
	users := queryTable(t)
	update, err := query.NewUpdate(users, query.Set(users.Column("name"), "Ada"))
	require.NoError(t, err)
	update, err = update.AllowAll()
	require.NoError(t, err)
	update, err = update.WithReturning(users.Column("id"))
	require.NoError(t, err)
	statement, err := c.Write(update)
	require.ErrorIs(t, err, engineprofile.ErrUnsupportedFeature)
	require.Empty(t, statement.SQL())

	insert, err := query.NewInsert(users, query.Set(users.Column("id"), 1), query.Set(users.Column("name"), "Ada"))
	require.NoError(t, err)
	insert, err = insert.WithReturning(users.Column("id"))
	require.NoError(t, err)
	statement, err = c.Write(insert)
	require.NoError(t, err)
	require.Contains(t, statement.SQL(), "RETURNING")
}

func TestAcceptanceQueryRejectsNilWriteFormsWithoutPanic(t *testing.T) {
	c, err := querycompile.New(queryProfile(t))
	require.NoError(t, err)
	for _, statement := range []query.WriteStatement{nil, (*query.Insert)(nil), (*query.Update)(nil), (*query.Delete)(nil), (*query.Upsert)(nil)} {
		require.NotPanics(t, func() {
			compiled, compileErr := c.Write(statement)
			require.Error(t, compileErr)
			require.Empty(t, compiled.SQL())
		})
	}
}

func TestAcceptanceQueryChecksTrustedFragmentHolesAndPointerWrites(t *testing.T) {
	p := customPostgresProfile(t, func(c *engineprofile.Capabilities) {
		c.WindowFunctions = false
		c.PerParentLimit = engineprofile.PerParentLimitUnsupported
	})
	c, err := querycompile.NewWithDialect(p, dialect.PostgreSQL())
	require.NoError(t, err)
	users := queryTable(t)
	window := query.OverWindow(query.Func("row_number"), query.Window(nil, query.Asc(users.Column("id"))))
	inner, err := query.NewSelect(users, query.Project(window).As("n"))
	require.NoError(t, err)
	body, err := query.NewSelect(users, query.Project(query.TrustedSQL("{}", query.Hole(window))).As("n"))
	require.NoError(t, err)
	result, err := query.ResultOf(body, query.ResultColumn{Name: "n", Type: schema.IntegerType{}})
	require.NoError(t, err)
	statement, err := c.Select(result)
	require.ErrorIs(t, err, engineprofile.ErrUnsupportedFeature)
	require.Empty(t, statement.SQL())

	update, err := query.NewUpdate(users, query.Set(users.Column("id"), query.Scalar(inner)))
	require.NoError(t, err)
	update, err = update.AllowAll()
	require.NoError(t, err)
	statement, err = c.Write(&update)
	require.ErrorIs(t, err, engineprofile.ErrUnsupportedFeature)
	require.Empty(t, statement.SQL())

	deleteStatement, err := query.NewDelete(users)
	require.NoError(t, err)
	deleteStatement, err = deleteStatement.WithWhere(query.Equal(users.Column("id"), query.Scalar(inner)))
	require.NoError(t, err)
	statement, err = c.Write(&deleteStatement)
	require.ErrorIs(t, err, engineprofile.ErrUnsupportedFeature)
	require.Empty(t, statement.SQL())
}
