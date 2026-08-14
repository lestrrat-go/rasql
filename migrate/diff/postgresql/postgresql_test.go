package postgresql_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/lestrrat-go/rasql/migrate/diff/postgresql"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestDiffLiveMatchesInlinePrimaryKey(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY);")
	liveSources, err := analyzer.LiveSources(schema.TableDef{
		Name:       "members",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	live := parseSources(t, analyzer, liveSources)

	plan, err := analyzer.Diff(baseline, live)
	require.NoError(t, err)
	require.Empty(t, plan.Statements)
}

// TestLiveSourcesRejectsGeneratedColumn proves that an inspected
// PostgreSQL table carrying a generated column does not reach diff-live's
// generated desired-schema sources as a silently downgraded plain writable
// column: LiveSources renders through render.CreateTable, which refuses
// GeneratedExpression regardless of which engine produced the descriptor,
// so the error surfaces here rather than a Plan going on to emit DDL for a
// column that cannot be written to at all.
func TestLiveSourcesRejectsGeneratedColumn(t *testing.T) {
	analyzer := postgresql.New()
	_, err := analyzer.LiveSources(schema.TableDef{
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
	require.ErrorContains(t, err, `"fahrenheit"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsNonDefaultIndexMethod proves that an inspected table
// carrying a non-default index method, such as a GIN index, does not reach
// diff-live's generated desired-schema sources as a silently downgraded
// plain index: LiveSources renders through render.CreateIndexes, which
// refuses the method, so the error surfaces here rather than a Plan going on
// to emit the wrong DDL for it.
func TestLiveSourcesRejectsNonDefaultIndexMethod(t *testing.T) {
	analyzer := postgresql.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name:       "members",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "tags", Type: schema.TextType{}}},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:    "members_tags_gin_idx",
			Columns: []string{"tags"},
			Method:  "gin",
		}},
	})
	require.ErrorContains(t, err, `"members_tags_gin_idx"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsPartialIndex proves that an inspected table
// carrying a partial index's predicate does not reach diff-live's generated
// desired-schema sources as a silently downgraded unconditional index:
// LiveSources renders through render.CreateIndexes, which refuses the
// predicate, so the error surfaces here rather than a Plan going on to emit
// a stricter index than the database actually has.
func TestLiveSourcesRejectsPartialIndex(t *testing.T) {
	analyzer := postgresql.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name:       "members",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "status", Type: schema.TextType{}}},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:      "members_active_idx",
			Columns:   []string{"status"},
			Predicate: "status = 'active'",
		}},
	})
	require.ErrorContains(t, err, `"members_active_idx"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsExpressionIndex proves that an inspected table
// carrying an expression index does not reach diff-live's generated
// desired-schema sources as a silently downgraded plain-column index:
// LiveSources renders through render.CreateIndexes, which refuses
// Expressions, so the error surfaces here rather than a Plan going on to
// emit DDL over the wrong columns.
func TestLiveSourcesRejectsExpressionIndex(t *testing.T) {
	analyzer := postgresql.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name:       "members",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "name", Type: schema.TextType{}}},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:        "members_lower_name_idx",
			Expressions: []string{"lower(name)"},
		}},
	})
	require.ErrorContains(t, err, `"members_lower_name_idx"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsIndexIncludeColumns proves that an inspected table
// carrying an index's INCLUDE columns does not reach diff-live's generated
// desired-schema sources as a silently downgraded index without them:
// LiveSources renders through render.CreateIndexes, which refuses
// IncludeColumns, so the error surfaces here rather than a Plan going on to
// emit an index missing its covering columns.
func TestLiveSourcesRejectsIndexIncludeColumns(t *testing.T) {
	analyzer := postgresql.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name:       "members",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "status", Type: schema.TextType{}}, {Name: "name", Type: schema.TextType{}}},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:           "members_status_idx",
			Columns:        []string{"status"},
			IncludeColumns: []string{"name"},
		}},
	})
	require.ErrorContains(t, err, `"members_status_idx"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsIndexKeyDetails proves that an inspected table
// carrying an index's per-key facts — here, a descending key — does not
// reach diff-live's generated desired-schema sources as a silently
// downgraded plain ascending index: LiveSources renders through
// render.CreateIndexes, which refuses Keys, so the error surfaces here
// rather than a Plan going on to emit DDL with the wrong key order.
func TestLiveSourcesRejectsIndexKeyDetails(t *testing.T) {
	analyzer := postgresql.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name:       "members",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "created_at", Type: schema.TimeType{}}},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name: "members_created_at_idx",
			Keys: []schema.IndexKeyDef{{Expression: "created_at", Descending: true}},
		}},
	})
	require.ErrorContains(t, err, `"members_created_at_idx"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsIndexValidityStorageAndPlacement proves that an
// inspected table carrying an invalid index, one with storage parameters,
// one on a nondefault tablespace, or one marking the table's replica
// identity does not reach diff-live's generated desired-schema sources as a
// silently downgraded plain, default-placement index: LiveSources renders
// through render.CreateIndexes, which refuses each of these facts, so the
// error surfaces here rather than a Plan going on to emit the wrong DDL for
// it.
func TestLiveSourcesRejectsIndexValidityStorageAndPlacement(t *testing.T) {
	tests := []struct {
		name  string
		index schema.IndexDef
	}{
		{name: "not valid", index: schema.IndexDef{Name: "members_status_idx", Columns: []string{"status"}, NotValid: true}},
		{name: "storage parameters", index: schema.IndexDef{Name: "members_status_idx", Columns: []string{"status"}, StorageParameters: map[string]string{"fillfactor": "70"}}},
		{name: "tablespace", index: schema.IndexDef{Name: "members_status_idx", Columns: []string{"status"}, Tablespace: "pg_custom"}},
		{name: "replica identity", index: schema.IndexDef{Name: "members_status_idx", Columns: []string{"status"}, Unique: true, ReplicaIdentity: true}},
		{name: "nulls not distinct", index: schema.IndexDef{Name: "members_status_idx", Columns: []string{"status"}, Unique: true, NullsNotDistinct: true}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analyzer := postgresql.New()
			_, err := analyzer.LiveSources(schema.TableDef{
				Name:       "members",
				Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "status", Type: schema.TextType{}}},
				PrimaryKey: []string{"id"},
				Indexes:    []schema.IndexDef{test.index},
			})
			require.ErrorContains(t, err, `"members_status_idx"`)
			require.ErrorContains(t, err, "can describe but not yet render")
		})
	}
}

// TestLiveSourcesRejectsNonDefaultForeignKeyMatch proves that an inspected
// table carrying a foreign key with a non-default MATCH clause does not
// reach diff-live's generated desired-schema sources as a silently
// downgraded plain MATCH SIMPLE foreign key: LiveSources renders through
// render.CreateTable, which refuses the match type, so the error surfaces
// here rather than a Plan going on to emit the wrong DDL for it.
func TestLiveSourcesRejectsNonDefaultForeignKeyMatch(t *testing.T) {
	analyzer := postgresql.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name:       "orders",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "customer_id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKeyDef{{
			Name:              "orders_customer_fk",
			Columns:           []string{"customer_id"},
			ReferencedTable:   "customers",
			ReferencedColumns: []string{"id"},
			Match:             schema.MatchFull,
		}},
	})
	require.ErrorContains(t, err, `"orders_customer_fk"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsNonDefaultForeignKeyDeferrability is the
// deferrability counterpart to
// TestLiveSourcesRejectsNonDefaultForeignKeyMatch.
func TestLiveSourcesRejectsNonDefaultForeignKeyDeferrability(t *testing.T) {
	analyzer := postgresql.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name:       "orders",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "customer_id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKeyDef{{
			Name:              "orders_customer_fk",
			Columns:           []string{"customer_id"},
			ReferencedTable:   "customers",
			ReferencedColumns: []string{"id"},
			Deferrable:        schema.DeferrableInitiallyDeferred,
		}},
	})
	require.ErrorContains(t, err, `"orders_customer_fk"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsExclusionConstraint proves that an inspected table
// carrying an EXCLUDE constraint does not reach diff-live's generated
// desired-schema sources as a silently downgraded table missing the
// constraint entirely: LiveSources renders through render.CreateTable,
// which refuses an ExclusionDef, so the error surfaces here rather than a
// Plan going on to emit DDL for a table that no longer prevents the
// conflicting rows the live database actually rejects.
func TestLiveSourcesRejectsExclusionConstraint(t *testing.T) {
	analyzer := postgresql.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name:       "reservations",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "room", Type: schema.TextType{}}},
		PrimaryKey: []string{"id"},
		ExclusionConstraints: []schema.ExclusionDef{{
			Name:     "reservations_no_double_booking",
			Method:   "gist",
			Elements: []schema.ExclusionElementDef{{Expression: "room", Operator: "="}},
		}},
	})
	require.ErrorContains(t, err, `"reservations_no_double_booking"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsCheckNoInherit proves that an inspected table
// carrying a NO INHERIT check constraint does not reach diff-live's
// generated desired-schema sources as a silently downgraded plain inherited
// check constraint: LiveSources renders through render.CreateTable, which
// refuses NoInherit, so the error surfaces here rather than a Plan going on
// to emit the wrong DDL for it.
func TestLiveSourcesRejectsCheckNoInherit(t *testing.T) {
	analyzer := postgresql.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name:       "invoices",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "amount", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
		Checks: []schema.CheckDef{{
			Name:       "invoices_amount_check",
			Expression: "amount >= 0",
			NoInherit:  true,
		}},
	})
	require.ErrorContains(t, err, `"invoices_amount_check"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsCheckNotValid is the NOT VALID counterpart to
// TestLiveSourcesRejectsCheckNoInherit.
func TestLiveSourcesRejectsCheckNotValid(t *testing.T) {
	analyzer := postgresql.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name:       "invoices",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "amount", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
		Checks: []schema.CheckDef{{
			Name:       "invoices_amount_check",
			Expression: "amount >= 0",
			NotValid:   true,
		}},
	})
	require.ErrorContains(t, err, `"invoices_amount_check"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsCheckNotEnforced is the NOT ENFORCED counterpart to
// TestLiveSourcesRejectsCheckNoInherit.
func TestLiveSourcesRejectsCheckNotEnforced(t *testing.T) {
	analyzer := postgresql.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name:       "invoices",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "amount", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
		Checks: []schema.CheckDef{{
			Name:        "invoices_amount_check",
			Expression:  "amount >= 0",
			NotEnforced: true,
		}},
	})
	require.ErrorContains(t, err, `"invoices_amount_check"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsForeignKeyNotValid proves that an inspected table
// carrying a NOT VALID foreign key does not reach diff-live's generated
// desired-schema sources as a silently downgraded plain validated foreign
// key: LiveSources renders through render.CreateTable, which refuses
// NotValid, so the error surfaces here rather than a Plan going on to emit
// the wrong DDL for it.
func TestLiveSourcesRejectsForeignKeyNotValid(t *testing.T) {
	analyzer := postgresql.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name:       "orders",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "customer_id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKeyDef{{
			Name:              "orders_customer_fk",
			Columns:           []string{"customer_id"},
			ReferencedTable:   "customers",
			ReferencedColumns: []string{"id"},
			NotValid:          true,
		}},
	})
	require.ErrorContains(t, err, `"orders_customer_fk"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsForeignKeyNotEnforced is the NOT ENFORCED
// counterpart to TestLiveSourcesRejectsForeignKeyNotValid.
func TestLiveSourcesRejectsForeignKeyNotEnforced(t *testing.T) {
	analyzer := postgresql.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name:       "orders",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "customer_id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKeyDef{{
			Name:              "orders_customer_fk",
			Columns:           []string{"customer_id"},
			ReferencedTable:   "customers",
			ReferencedColumns: []string{"id"},
			NotEnforced:       true,
		}},
	})
	require.ErrorContains(t, err, `"orders_customer_fk"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsForeignKeyTemporal is the Temporal counterpart to
// TestLiveSourcesRejectsForeignKeyNotValid.
func TestLiveSourcesRejectsForeignKeyTemporal(t *testing.T) {
	analyzer := postgresql.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name:       "orders",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "customer_id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKeyDef{{
			Name:              "orders_customer_fk",
			Columns:           []string{"customer_id"},
			ReferencedTable:   "customers",
			ReferencedColumns: []string{"id"},
			Temporal:          true,
		}},
	})
	require.ErrorContains(t, err, `"orders_customer_fk"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsForeignKeyDeleteSetColumns is the DeleteSetColumns
// counterpart to TestLiveSourcesRejectsForeignKeyNotValid.
func TestLiveSourcesRejectsForeignKeyDeleteSetColumns(t *testing.T) {
	analyzer := postgresql.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name:       "orders",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "customer_id", Type: schema.IntegerType{}, Nullable: true}},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKeyDef{{
			Name:              "orders_customer_fk",
			Columns:           []string{"customer_id"},
			ReferencedTable:   "customers",
			ReferencedColumns: []string{"id"},
			OnDelete:          schema.SetNull,
			DeleteSetColumns:  []string{"customer_id"},
		}},
	})
	require.ErrorContains(t, err, `"orders_customer_fk"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsUniqueConstraintDeferrability proves that an
// inspected table carrying a deferrable unique constraint does not reach
// diff-live's generated desired-schema sources as a silently downgraded
// plain NOT DEFERRABLE unique constraint: LiveSources renders through
// render.CreateTable, which refuses the deferrability, so the error
// surfaces here rather than a Plan going on to emit the wrong DDL for it.
func TestLiveSourcesRejectsUniqueConstraintDeferrability(t *testing.T) {
	analyzer := postgresql.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name:       "members",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "email", Type: schema.TextType{}}},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueDef{{
			Name:       "members_email_key",
			Columns:    []string{"email"},
			Deferrable: schema.DeferrableInitiallyDeferred,
		}},
	})
	require.ErrorContains(t, err, `"members_email_key"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsUniqueConstraintNullsNotDistinct is the
// NullsNotDistinct counterpart to
// TestLiveSourcesRejectsUniqueConstraintDeferrability.
func TestLiveSourcesRejectsUniqueConstraintNullsNotDistinct(t *testing.T) {
	analyzer := postgresql.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name:       "members",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "email", Type: schema.TextType{}, Nullable: true}},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueDef{{
			Name:             "members_email_key",
			Columns:          []string{"email"},
			NullsNotDistinct: true,
		}},
	})
	require.ErrorContains(t, err, `"members_email_key"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsUniqueConstraintIncludeColumns is the
// IncludeColumns counterpart to
// TestLiveSourcesRejectsUniqueConstraintDeferrability.
func TestLiveSourcesRejectsUniqueConstraintIncludeColumns(t *testing.T) {
	analyzer := postgresql.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name:       "members",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "email", Type: schema.TextType{}}, {Name: "name", Type: schema.TextType{}}},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueDef{{
			Name:           "members_email_key",
			Columns:        []string{"email"},
			IncludeColumns: []string{"name"},
		}},
	})
	require.ErrorContains(t, err, `"members_email_key"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsUniqueConstraintConflictResolution is the
// OnConflict counterpart to
// TestLiveSourcesRejectsUniqueConstraintDeferrability.
func TestLiveSourcesRejectsUniqueConstraintConflictResolution(t *testing.T) {
	analyzer := postgresql.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name:       "members",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "email", Type: schema.TextType{}}},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueDef{{
			Name:       "members_email_key",
			Columns:    []string{"email"},
			OnConflict: schema.ConflictReplace,
		}},
	})
	require.ErrorContains(t, err, `"members_email_key"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsUniqueConstraintBackingIndexFacts proves that an
// inspected table carrying a temporal unique constraint, or one whose
// backing index carries storage parameters, a nondefault tablespace, a
// nondefault column collation, or the table's replica identity, does not
// reach diff-live's generated desired-schema sources as a silently
// downgraded plain unique constraint: LiveSources renders through
// render.CreateTable, which refuses each of these facts, so the error
// surfaces here rather than a Plan going on to emit the wrong DDL for it.
func TestLiveSourcesRejectsUniqueConstraintBackingIndexFacts(t *testing.T) {
	tests := []struct {
		name       string
		constraint schema.UniqueDef
	}{
		{name: "temporal", constraint: schema.UniqueDef{Name: "members_email_key", Columns: []string{"email"}, Temporal: true}},
		{name: "storage parameters", constraint: schema.UniqueDef{Name: "members_email_key", Columns: []string{"email"}, StorageParameters: map[string]string{"fillfactor": "70"}}},
		{name: "tablespace", constraint: schema.UniqueDef{Name: "members_email_key", Columns: []string{"email"}, Tablespace: "pg_custom"}},
		{name: "replica identity", constraint: schema.UniqueDef{Name: "members_email_key", Columns: []string{"email"}, ReplicaIdentity: true}},
		{name: "collations", constraint: schema.UniqueDef{Name: "members_email_key", Columns: []string{"email"}, Collations: map[string]string{"email": "C"}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analyzer := postgresql.New()
			_, err := analyzer.LiveSources(schema.TableDef{
				Name:              "members",
				Columns:           []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "email", Type: schema.TextType{}}},
				PrimaryKey:        []string{"id"},
				UniqueConstraints: []schema.UniqueDef{test.constraint},
			})
			require.ErrorContains(t, err, `"members_email_key"`)
			require.ErrorContains(t, err, "can describe but not yet render")
		})
	}
}

func TestDiffGeneratesAdditiveColumnsAndIndexes(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, `
		CREATE TABLE members (
			id bigint PRIMARY KEY,
			name text NOT NULL
		);
		CREATE INDEX members_name_idx ON members (name);
	`)
	target := parseSnapshot(t, analyzer, `
		CREATE TABLE members (
			id bigint PRIMARY KEY,
			name text NOT NULL,
			email text
		);
		CREATE INDEX members_name_idx ON members (name);
		CREATE INDEX members_email_idx ON members (email);
	`)

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Equal(t, diff.Plan{
		Dialect: "postgresql",
		Statements: []diff.PlannedStatement{
			{
				Source:  "001_add_column_members_email.sql",
				SQL:     "ALTER TABLE members ADD COLUMN email text;\n",
				Summary: "add column members.email",
			},
			{
				Source:  "002_create_index_members_email_idx.sql",
				SQL:     "CREATE INDEX members_email_idx ON members (email);\n",
				Summary: "create index members_email_idx",
			},
		},
	}, plan)
}

func TestDiffGeneratesNewTable(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, `
		CREATE TABLE members (id bigint PRIMARY KEY);
		CREATE TABLE projects (id bigint PRIMARY KEY, owner_id bigint NOT NULL);
	`)

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Equal(t, []diff.PlannedStatement{{
		Source:  "001_create_table_projects.sql",
		SQL:     "CREATE TABLE projects (id bigint PRIMARY KEY, owner_id bigint NOT NULL);\n",
		Summary: "create table projects",
	}}, plan.Statements)
}

func TestDiffRejectsCollidingGeneratedStatementNames(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, `CREATE TABLE members (id bigint PRIMARY KEY); CREATE TABLE "foo-bar" (id bigint PRIMARY KEY); CREATE TABLE foo_bar (id bigint PRIMARY KEY);`)

	_, err := analyzer.Diff(baseline, target)
	require.ErrorContains(t, err, `duplicate generated SQL source "create_table_foo_bar.sql"`)
	require.ErrorContains(t, err, "create table foo-bar")
	require.ErrorContains(t, err, "create table foo_bar")
	require.NotContains(t, err.Error(), "001_")
}

func TestDiffRejectsNewRequiredColumnWithoutBackfill(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY, email text NOT NULL);")

	_, err := analyzer.Diff(baseline, target)
	require.ErrorContains(t, err, "new required column members.email needs an application-specific backfill")
}

func TestDiffGeneratesNewRequiredColumnWithDefault(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY, active boolean NOT NULL DEFAULT true);")

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Equal(t, []diff.PlannedStatement{{
		Source:  "001_add_column_members_active.sql",
		SQL:     "ALTER TABLE members ADD COLUMN active boolean NOT NULL DEFAULT TRUE;\n",
		Summary: "add column members.active",
	}}, plan.Statements)
}

func TestDiffRejectsNewRequiredPrimaryKeyColumnWithDefaultWhenPrimaryKeyFollowsNotNull(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY, active integer NOT NULL DEFAULT 1 PRIMARY KEY);")

	_, err := analyzer.Diff(baseline, target)
	require.ErrorContains(t, err, "new required column members.active needs an application-specific backfill")
}

func TestDiffRejectsNewRequiredColumnWithNullDefault(t *testing.T) {
	for _, columnDefinition := range []string{
		"email text DEFAULT NULL NOT NULL",
		"email text NOT NULL DEFAULT NULL",
	} {
		t.Run(columnDefinition, func(t *testing.T) {
			analyzer := postgresql.New()
			baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY);")
			target := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY, "+columnDefinition+");")

			_, err := analyzer.Diff(baseline, target)
			require.ErrorContains(t, err, "new required column members.email needs an application-specific backfill")
		})
	}
}

func TestDiffRejectsRemovedColumns(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY, email text);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY);")

	_, err := analyzer.Diff(baseline, target)
	require.ErrorContains(t, err, "column members.email was removed")
}

func TestDiffTreatsQuotedLowercaseIdentifiersAsEquivalent(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, `CREATE TABLE members ("members" text);`)
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (members text);")

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Empty(t, plan.Statements)
}

func TestDiffDistinguishesMixedCaseIdentifiers(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, `CREATE TABLE members ("Members" text);`)
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (Members text);")

	_, err := analyzer.Diff(baseline, target)
	require.ErrorContains(t, err, "column members.Members was removed")
}

func TestParseRejectsUnsupportedDesiredSchemaStatement(t *testing.T) {
	analyzer := postgresql.New()
	_, err := analyzer.Parse([]diff.Source{{Path: "views.sql", SQL: "CREATE VIEW member_names AS SELECT name FROM members;"}})
	require.ErrorContains(t, err, "must be CREATE TABLE or named CREATE INDEX")
}

func TestParseRejectsIndexForMissingTable(t *testing.T) {
	analyzer := postgresql.New()
	_, err := analyzer.Parse([]diff.Source{{Path: "indexes.sql", SQL: `
		CREATE TABLE members (id bigint PRIMARY KEY);
		CREATE INDEX orphan_idx ON missing (id);
	`}})
	require.ErrorContains(t, err, `postgresql schema source "indexes.sql"`)
	require.ErrorContains(t, err, "missing table missing")
}

func TestParseRejectsIndexOnlySourceForMissingTable(t *testing.T) {
	analyzer := postgresql.New()
	_, err := analyzer.Parse([]diff.Source{{Path: "indexes.sql", SQL: "CREATE INDEX orphan_idx ON missing (id);"}})
	require.EqualError(t, err, `postgresql schema source "indexes.sql" defines index orphan_idx on missing table missing`)
}

func TestDiffRejectsConcurrentIndex(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, `
		CREATE TABLE members (id bigint PRIMARY KEY);
		CREATE INDEX CONCURRENTLY members_id_idx ON members (id);
	`)

	_, err := analyzer.Diff(baseline, target)
	require.ErrorContains(t, err, "uses CONCURRENTLY")
}

func parseSnapshot(t *testing.T, analyzer postgresql.Analyzer, source string) diff.Snapshot {
	t.Helper()
	snapshot, err := analyzer.Parse([]diff.Source{{Path: "schema.sql", SQL: source}})
	require.NoError(t, err)
	return snapshot
}

func parseSources(t *testing.T, analyzer postgresql.Analyzer, sources []diff.Source) diff.Snapshot {
	t.Helper()
	snapshot, err := analyzer.Parse(sources)
	require.NoError(t, err)
	return snapshot
}
