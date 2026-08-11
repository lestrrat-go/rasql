package render_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestCreateTableRendersDialectTypesAndConstraints(t *testing.T) {
	table := schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "customer_id", Type: schema.IntegerType{}},
			{Name: "metadata", Type: schema.JSONType{}, Nullable: true},
		},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueConstraint{{
			Name:    "orders_customer_key",
			Columns: []string{"customer_id"},
		}},
		Checks: []schema.CheckConstraint{{
			Name:       "orders_id_check",
			Expression: "id > 0",
		}},
		ForeignKeys: []schema.ForeignKeyDef{{
			Name:              "orders_customer_fk",
			Columns:           []string{"customer_id"},
			ReferencedTable:   "customers",
			ReferencedColumns: []string{"id"},
			OnDelete:          schema.Cascade,
		}},
		Indexes: []schema.IndexDef{{
			Name:    "orders_customer_idx",
			Columns: []string{"customer_id"},
		}},
	}

	rendered, err := render.CreateTable(dialect.PostgreSQL(), table)
	require.NoError(t, err)
	require.Equal(t, "CREATE TABLE \"orders\" (\"id\" BIGINT NOT NULL, \"customer_id\" BIGINT NOT NULL, \"metadata\" JSONB, PRIMARY KEY (\"id\"), CONSTRAINT \"orders_customer_key\" UNIQUE (\"customer_id\"), CONSTRAINT \"orders_id_check\" CHECK (id > 0), CONSTRAINT \"orders_customer_fk\" FOREIGN KEY (\"customer_id\") REFERENCES \"customers\" (\"id\") ON DELETE CASCADE)", rendered.SQL())
	require.Empty(t, rendered.Args())

	indexes, err := render.CreateIndexes(dialect.MySQL(), table)
	require.NoError(t, err)
	require.Equal(t, []string{"CREATE INDEX `orders_customer_idx` ON `orders` (`customer_id`)"}, sqls(indexes))
}

func TestCreateTableRendersDecimalColumns(t *testing.T) {
	table := schema.Table{
		Name: "invoices",
		Columns: []schema.Column{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.DecimalType{Precision: 19, Scale: schema.NewDecimalScale(4)}},
			{Name: "tax_rate", Type: schema.DecimalType{Precision: 5, Scale: schema.NewDecimalScale(4)}, Nullable: true},
		},
		PrimaryKey: []string{"id"},
	}

	tests := map[string]struct {
		dialect dialect.Dialect
		sql     string
	}{
		"postgresql": {
			dialect: dialect.PostgreSQL(),
			sql:     `CREATE TABLE "invoices" ("id" BIGINT NOT NULL, "amount" NUMERIC(19,4) NOT NULL, "tax_rate" NUMERIC(5,4), PRIMARY KEY ("id"))`,
		},
		"mysql": {
			dialect: dialect.MySQL(),
			sql:     "CREATE TABLE `invoices` (`id` BIGINT NOT NULL, `amount` DECIMAL(19,4) NOT NULL, `tax_rate` DECIMAL(5,4), PRIMARY KEY (`id`))",
		},
		"sqlite": {
			dialect: dialect.SQLite(),
			sql:     `CREATE TABLE "invoices" ("id" INTEGER NOT NULL, "amount" TEXT NOT NULL, "tax_rate" TEXT, PRIMARY KEY ("id"))`,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rendered, err := render.CreateTable(test.dialect, table)
			require.NoError(t, err)
			require.Equal(t, test.sql, rendered.SQL())
			require.Empty(t, rendered.Args())
		})
	}
}

// TestCreateTableRendersUnsignedIntegerColumns is the DDL half of the fix for
// an unsigned integer column: MySQL keeps the UNSIGNED the descriptor states,
// where it used to render a signed BIGINT that stops at 9223372036854775807,
// and the two dialects with no unsigned integer type refuse the table instead
// of narrowing it silently.
func TestCreateTableRendersUnsignedIntegerColumns(t *testing.T) {
	table := schema.Table{
		Name: "events",
		Columns: []schema.Column{
			{Name: "id", Type: schema.IntegerType{Unsigned: true}},
			{Name: "sequence", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
	}
	require.NoError(t, table.Validate())

	rendered, err := render.CreateTable(dialect.MySQL(), table)
	require.NoError(t, err)
	require.Equal(t, "CREATE TABLE `events` (`id` BIGINT UNSIGNED NOT NULL, `sequence` BIGINT NOT NULL, PRIMARY KEY (`id`))", rendered.SQL())

	for _, d := range []dialect.Dialect{dialect.PostgreSQL(), dialect.SQLite()} {
		t.Run(d.Name(), func(t *testing.T) {
			_, err := render.CreateTable(d, table)
			require.ErrorContains(t, err, `column "id"`)
			require.ErrorContains(t, err, "has no unsigned integer type")
		})
	}
}

func TestCreateTableReportsDecimalTypeErrorWithColumn(t *testing.T) {
	table := schema.Table{
		Name: "invoices",
		Columns: []schema.Column{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.DecimalType{Precision: 100, Scale: schema.NewDecimalScale(4)}},
		},
		PrimaryKey: []string{"id"},
	}

	_, err := render.CreateTable(dialect.MySQL(), table)
	require.ErrorContains(t, err, `column "amount"`)
	require.ErrorContains(t, err, "decimal precision 100 exceeds the maximum of 65")
}

// TestCreateTableRendersQualifiedName pins CREATE TABLE for a schema-qualified
// table across all three built-in dialects. No dialect.Capability is
// consulted for CREATE TABLE itself: quoteQualified renders two identifiers
// joined by a dot on every dialect, and section 3.2 of the design measured
// that all three accept that form.
func TestCreateTableRendersQualifiedName(t *testing.T) {
	table := schema.Table{
		Schema: "audit",
		Name:   "events",
		Columns: []schema.Column{
			{Name: "id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
	}

	tests := map[string]struct {
		dialect dialect.Dialect
		sql     string
	}{
		"postgresql": {
			dialect: dialect.PostgreSQL(),
			sql:     `CREATE TABLE "audit"."events" ("id" BIGINT NOT NULL, PRIMARY KEY ("id"))`,
		},
		"mysql": {
			dialect: dialect.MySQL(),
			sql:     "CREATE TABLE `audit`.`events` (`id` BIGINT NOT NULL, PRIMARY KEY (`id`))",
		},
		"sqlite": {
			dialect: dialect.SQLite(),
			sql:     `CREATE TABLE "audit"."events" ("id" INTEGER NOT NULL, PRIMARY KEY ("id"))`,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rendered, err := render.CreateTable(test.dialect, table)
			require.NoError(t, err)
			require.Equal(t, test.sql, rendered.SQL())
		})
	}
}

// TestCreateTableRendersQualifiedForeignKey covers ForeignKey.ReferencedSchema.
// PostgreSQL and MySQL both hold dialect.CapabilityQualifiedReference and
// render a cross-schema REFERENCES clause qualified. SQLite holds neither: a
// same-schema reference still renders unqualified, because dropping a
// same-schema qualifier changes nothing about what the reference means and
// is the only form SQLite's grammar accepts at all (section 3.2 of the
// design), while a genuinely cross-schema reference is refused rather than
// silently narrowed to the wrong table.
func TestCreateTableRendersQualifiedForeignKey(t *testing.T) {
	crossSchema := schema.Table{
		Schema: "audit",
		Name:   "events",
		Columns: []schema.Column{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKeyDef{{
			Name:              "events_user_id_fkey",
			Columns:           []string{"user_id"},
			ReferencedSchema:  "tenant",
			ReferencedTable:   "users",
			ReferencedColumns: []string{"id"},
			OnDelete:          schema.Cascade,
		}},
	}

	rendered, err := render.CreateTable(dialect.PostgreSQL(), crossSchema)
	require.NoError(t, err)
	require.Contains(t, rendered.SQL(), `REFERENCES "tenant"."users" ("id") ON DELETE CASCADE`)

	rendered, err = render.CreateTable(dialect.MySQL(), crossSchema)
	require.NoError(t, err)
	require.Contains(t, rendered.SQL(), "REFERENCES `tenant`.`users` (`id`) ON DELETE CASCADE")

	_, err = render.CreateTable(dialect.SQLite(), crossSchema)
	require.ErrorContains(t, err, "dialect.CapabilityQualifiedReference")
	require.ErrorContains(t, err, `"tenant"`)
	require.ErrorContains(t, err, `"audit"`)

	sameSchema := crossSchema
	sameSchema.ForeignKeys = []schema.ForeignKeyDef{{
		Name:              "events_user_id_fkey",
		Columns:           []string{"user_id"},
		ReferencedSchema:  "audit",
		ReferencedTable:   "users",
		ReferencedColumns: []string{"id"},
		OnDelete:          schema.Cascade,
	}}
	rendered, err = render.CreateTable(dialect.SQLite(), sameSchema)
	require.NoError(t, err)
	require.Contains(t, rendered.SQL(), `REFERENCES "users" ("id") ON DELETE CASCADE`)
}

// TestCreateIndexRendersDialectQualifierPosition covers writeCreateIndex's
// two mutually exclusive capabilities. PostgreSQL and MySQL hold
// dialect.CapabilityQualifiedIndexTarget and qualify the indexed table,
// leaving the index name bare. SQLite holds dialect.CapabilityQualifiedIndexName
// instead and qualifies the index name, leaving the indexed table bare,
// because it cannot qualify the table in "ON table" at all (section 3.2 of
// the design). Both the plain and the UNIQUE form are covered, since the
// qualifier position is decided before the UNIQUE keyword is even written.
func TestCreateIndexRendersDialectQualifierPosition(t *testing.T) {
	table := schema.Table{
		Schema: "audit",
		Name:   "events",
		Columns: []schema.Column{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{
			{Name: "events_user_id_idx", Columns: []string{"user_id"}},
			{Name: "events_user_id_uidx", Columns: []string{"user_id"}, Unique: true},
		},
	}

	tests := map[string]struct {
		dialect dialect.Dialect
		sql     []string
	}{
		"postgresql": {
			dialect: dialect.PostgreSQL(),
			sql: []string{
				`CREATE INDEX "events_user_id_idx" ON "audit"."events" ("user_id")`,
				`CREATE UNIQUE INDEX "events_user_id_uidx" ON "audit"."events" ("user_id")`,
			},
		},
		"mysql": {
			dialect: dialect.MySQL(),
			sql: []string{
				"CREATE INDEX `events_user_id_idx` ON `audit`.`events` (`user_id`)",
				"CREATE UNIQUE INDEX `events_user_id_uidx` ON `audit`.`events` (`user_id`)",
			},
		},
		"sqlite": {
			dialect: dialect.SQLite(),
			sql: []string{
				`CREATE INDEX "audit"."events_user_id_idx" ON "events" ("user_id")`,
				`CREATE UNIQUE INDEX "audit"."events_user_id_uidx" ON "events" ("user_id")`,
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			statements, err := render.CreateIndexes(test.dialect, table)
			require.NoError(t, err)
			require.Equal(t, test.sql, sqls(statements))
		})
	}
}

// noQualifiedDDLDialect wraps a dialect.Dialect and reports false for every
// capability this change adds, regardless of what the wrapped dialect
// actually supports. It stands in for any third-party dialect.Dialect
// implementation written before these three constants existed: Capability is
// a growing bitmask, and an implementation outside this module returns false
// for a bit it has never heard of rather than panicking or guessing.
type noQualifiedDDLDialect struct {
	dialect.Dialect
}

func (d noQualifiedDDLDialect) Supports(capability dialect.Capability) bool {
	switch capability {
	case dialect.CapabilityQualifiedReference, dialect.CapabilityQualifiedIndexTarget, dialect.CapabilityQualifiedIndexName:
		return false
	default:
		return d.Dialect.Supports(capability)
	}
}

// TestUnqualifiedDDLIsUnchanged is the regression guard for every existing
// user of render.CreateTable and render.CreateIndexes: a descriptor that
// names no schema anywhere renders exactly what it rendered before this
// change, on all three built-in dialects and on a third-party dialect that
// has never heard of the three capabilities this change adds. That holds
// because the empty-schema paths in writeCreateTable, qualifiedIndexNames and
// qualifiedReferencedTable never call dialect.Dialect.Supports at all.
func TestUnqualifiedDDLIsUnchanged(t *testing.T) {
	table := schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "customer_id", Type: schema.IntegerType{}},
			{Name: "metadata", Type: schema.JSONType{}, Nullable: true},
		},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueConstraint{{
			Name:    "orders_customer_key",
			Columns: []string{"customer_id"},
		}},
		Checks: []schema.CheckConstraint{{
			Name:       "orders_id_check",
			Expression: "id > 0",
		}},
		ForeignKeys: []schema.ForeignKeyDef{{
			Name:              "orders_customer_fk",
			Columns:           []string{"customer_id"},
			ReferencedTable:   "customers",
			ReferencedColumns: []string{"id"},
			OnDelete:          schema.Cascade,
		}},
		Indexes: []schema.IndexDef{{
			Name:    "orders_customer_idx",
			Columns: []string{"customer_id"},
		}},
	}

	tests := map[string]struct {
		dialect     dialect.Dialect
		createTable string
		createIndex string
	}{
		"postgresql": {
			dialect:     dialect.PostgreSQL(),
			createTable: `CREATE TABLE "orders" ("id" BIGINT NOT NULL, "customer_id" BIGINT NOT NULL, "metadata" JSONB, PRIMARY KEY ("id"), CONSTRAINT "orders_customer_key" UNIQUE ("customer_id"), CONSTRAINT "orders_id_check" CHECK (id > 0), CONSTRAINT "orders_customer_fk" FOREIGN KEY ("customer_id") REFERENCES "customers" ("id") ON DELETE CASCADE)`,
			createIndex: `CREATE INDEX "orders_customer_idx" ON "orders" ("customer_id")`,
		},
		"mysql": {
			dialect:     dialect.MySQL(),
			createTable: "CREATE TABLE `orders` (`id` BIGINT NOT NULL, `customer_id` BIGINT NOT NULL, `metadata` JSON, PRIMARY KEY (`id`), CONSTRAINT `orders_customer_key` UNIQUE (`customer_id`), CONSTRAINT `orders_id_check` CHECK (id > 0), CONSTRAINT `orders_customer_fk` FOREIGN KEY (`customer_id`) REFERENCES `customers` (`id`) ON DELETE CASCADE)",
			createIndex: "CREATE INDEX `orders_customer_idx` ON `orders` (`customer_id`)",
		},
		"sqlite": {
			dialect:     dialect.SQLite(),
			createTable: `CREATE TABLE "orders" ("id" INTEGER NOT NULL, "customer_id" INTEGER NOT NULL, "metadata" TEXT, PRIMARY KEY ("id"), CONSTRAINT "orders_customer_key" UNIQUE ("customer_id"), CONSTRAINT "orders_id_check" CHECK (id > 0), CONSTRAINT "orders_customer_fk" FOREIGN KEY ("customer_id") REFERENCES "customers" ("id") ON DELETE CASCADE)`,
			createIndex: `CREATE INDEX "orders_customer_idx" ON "orders" ("customer_id")`,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rendered, err := render.CreateTable(test.dialect, table)
			require.NoError(t, err)
			require.Equal(t, test.createTable, rendered.SQL())

			indexes, err := render.CreateIndexes(test.dialect, table)
			require.NoError(t, err)
			require.Equal(t, []string{test.createIndex}, sqls(indexes))

			stub := noQualifiedDDLDialect{Dialect: test.dialect}
			rendered, err = render.CreateTable(stub, table)
			require.NoError(t, err)
			require.Equal(t, test.createTable, rendered.SQL())

			indexes, err = render.CreateIndexes(stub, table)
			require.NoError(t, err)
			require.Equal(t, []string{test.createIndex}, sqls(indexes))
		})
	}
}

// TestCreateTableRendersQualifiedDecimalColumn pins that qualification and
// the decimal-column work (schema.DecimalType's Precision/Scale rendering)
// do not interact: a decimal column inside a qualified table renders its
// NUMERIC(p,s) type exactly as it would unqualified, next to a qualified
// table name.
func TestCreateTableRendersQualifiedDecimalColumn(t *testing.T) {
	table := schema.Table{
		Schema: "audit",
		Name:   "invoices",
		Columns: []schema.Column{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.DecimalType{Precision: 19, Scale: schema.NewDecimalScale(4)}},
		},
		PrimaryKey: []string{"id"},
	}

	rendered, err := render.CreateTable(dialect.PostgreSQL(), table)
	require.NoError(t, err)
	require.Equal(t, `CREATE TABLE "audit"."invoices" ("id" BIGINT NOT NULL, "amount" NUMERIC(19,4) NOT NULL, PRIMARY KEY ("id"))`, rendered.SQL())
}

// TestSQLiteExecutesQualifiedDDL proves the SQLite branch against a real
// parser rather than a golden string: the rendered CREATE TABLE, CREATE
// INDEX and CREATE UNIQUE INDEX all execute, and every object lands in the
// attached "audit" database rather than "main".
func TestSQLiteExecutesQualifiedDDL(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)

	_, err = database.ExecContext(t.Context(), `ATTACH DATABASE ':memory:' AS audit`)
	require.NoError(t, err)

	table := schema.Table{
		Schema: "audit",
		Name:   "events",
		Columns: []schema.Column{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{
			{Name: "events_user_id_idx", Columns: []string{"user_id"}},
			{Name: "events_user_id_uidx", Columns: []string{"user_id"}, Unique: true},
		},
	}

	created, err := render.CreateTable(dialect.SQLite(), table)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), created.SQL())
	require.NoError(t, err)

	indexes, err := render.CreateIndexes(dialect.SQLite(), table)
	require.NoError(t, err)
	for _, index := range indexes {
		_, err = database.ExecContext(t.Context(), index.SQL())
		require.NoError(t, err)
	}

	rows, err := database.QueryContext(t.Context(), `SELECT name FROM audit.sqlite_schema WHERE type IN ('table', 'index') ORDER BY name`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		names = append(names, name)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{"events", "events_user_id_idx", "events_user_id_uidx"}, names)

	var mainCount int
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM main.sqlite_schema WHERE type IN ('table', 'index')`).Scan(&mainCount))
	require.Zero(t, mainCount)
}

func sqls(statements []render.Statement) []string {
	sql := make([]string, len(statements))
	for i, statement := range statements {
		sql[i] = statement.SQL()
	}
	return sql
}
