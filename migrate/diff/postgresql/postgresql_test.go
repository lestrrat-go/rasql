package postgresql_test

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/internal/migrationdir"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/lestrrat-go/rasql/migrate/diff"
	mysqldiff "github.com/lestrrat-go/rasql/migrate/diff/mysql"
	"github.com/lestrrat-go/rasql/migrate/diff/postgresql"
	sqliteDiff "github.com/lestrrat-go/rasql/migrate/diff/sqlite"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
)

func TestLiveSourcesNativeCrossDialectRefusalMatrix(t *testing.T) {
	native := &schema.NativeTypeDef{Dialect: "postgresql", Schema: "app", Name: "mood", Kind: schema.NativeEnum, Arguments: []string{"sad", "happy"}}
	desired := schema.TableDef{Name: "events", Columns: []schema.ColumnDef{{Name: "mood", Type: schema.OpaqueType{}, NativeType: native}}}
	for _, analyzer := range []diff.LiveAnalyzer{mysqldiff.New(), sqliteDiff.New()} {
		sources, err := analyzer.LiveSources(desired)
		var unsupported *render.ErrUnsupportedNativeType
		require.ErrorAs(t, err, &unsupported)
		require.Nil(t, sources)
		require.Equal(t, analyzer.Dialect(), unsupported.Dialect)
		require.Equal(t, "events", unsupported.Table)
		require.Equal(t, "mood", unsupported.Column)
		require.Equal(t, *native, unsupported.Native)
	}
}

func TestDiffNumbersLargePlan(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE existing (id bigint PRIMARY KEY);")
	var source strings.Builder
	for index := 0; index < 1000; index++ {
		source.WriteString("CREATE TABLE table_")
		source.WriteString(strconv.Itoa(index))
		source.WriteString(" (id bigint PRIMARY KEY); ")
	}
	target := parseSnapshot(t, analyzer, source.String()+"CREATE TABLE existing (id bigint PRIMARY KEY);")
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Len(t, plan.Statements, 1000)
	sources := make([]string, len(plan.Statements))
	for index, statement := range plan.Statements {
		sources[index] = statement.Source
	}
	sorted := append([]string(nil), sources...)
	sort.Strings(sorted)
	require.Equal(t, sources, sorted)
	require.Equal(t, "0001_", sources[0][:5])
	require.Equal(t, "1000_", sources[len(sources)-1][:5])
}

func TestDiffWriteMigrationLoadsExactArtifact(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY, email text); CREATE INDEX members_email_idx ON members (email);")
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	root := t.TempDir()
	require.NoError(t, diff.WriteMigration(filepath.Join(root, "001_add_email"), plan))
	loaded, err := migrationdir.Load(root)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Len(t, loaded[0].Statements, len(plan.Statements))
	for index, statement := range plan.Statements {
		require.Equal(t, statement.Source[:len(statement.Source)-4]+".up.sql", loaded[0].Statements[index].Source)
		require.Equal(t, statement.SQL, string(loaded[0].Statements[index].SQL))
	}
	require.Len(t, loaded[0].Down, len(plan.Statements))
	for index, statement := range plan.Statements {
		require.Equal(t, statement.Source[:len(statement.Source)-4]+".down.sql", loaded[0].Down[len(plan.Statements)-index-1].Source)
		require.Equal(t, statement.ReverseSQL, string(loaded[0].Down[len(plan.Statements)-index-1].SQL))
	}
}

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

func TestLiveSourcesPreservesNativeTypeAndRejectsCrossDialect(t *testing.T) {
	native := &schema.NativeTypeDef{Dialect: "postgresql", Schema: "app", Name: "mood", Kind: schema.NativeEnum}
	desired := schema.TableDef{Name: "events", Columns: []schema.ColumnDef{{Name: "mood", Type: schema.OpaqueType{}, NativeType: native}}}
	sources, err := postgresql.New().LiveSources(desired)
	require.NoError(t, err)
	require.Contains(t, string(sources[0].SQL), `"app"."mood"`)
	_, err = mysqldiff.New().LiveSources(desired)
	var unsupported *render.ErrUnsupportedNativeType
	require.ErrorAs(t, err, &unsupported)
	require.Equal(t, *native, unsupported.Native)
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

// TestDiffLiveMatchesPartialIndex proves that an inspected table carrying
// a partial index's predicate now reaches diff-live's generated
// desired-schema sources as the same partial index, rather than a
// downgraded unconditional one: LiveSources renders through
// render.CreateIndexes, which now renders a Predicate verbatim on
// PostgreSQL, so a baseline stating the identical WHERE clause diffs to no
// statements at all.
func TestDiffLiveMatchesPartialIndex(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY, status text NOT NULL); CREATE INDEX members_active_idx ON members (status) WHERE status = 'active';")
	liveSources, err := analyzer.LiveSources(schema.TableDef{
		Name:       "members",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "status", Type: schema.TextType{}}},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:      "members_active_idx",
			Columns:   []string{"status"},
			Predicate: "status = 'active'",
		}},
	})
	require.NoError(t, err)
	live := parseSources(t, analyzer, liveSources)

	plan, err := analyzer.Diff(baseline, live)
	require.NoError(t, err)
	require.Empty(t, plan.Statements)
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
			Expressions: []sqltext.Text{"lower(name)"},
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
	require.Equal(t, []diff.PlannedStatement{
		{
			Source:     "001_add_column_members_email.sql",
			SQL:        "ALTER TABLE members ADD COLUMN email text;\n",
			ReverseSQL: "ALTER TABLE members DROP COLUMN email;\n",
			Summary:    "add column members.email",
		},
		{
			Source:     "002_create_index_members_email_idx.sql",
			SQL:        "CREATE INDEX members_email_idx ON members (email);\n",
			ReverseSQL: "DROP INDEX members_email_idx;\n",
			Summary:    "create index members_email_idx",
		},
	}, plan.Statements)
	require.Len(t, plan.Operations, 2)
	require.Equal(t, []diff.ProposedOperation{
		{ID: "add_column_postgresql_members_email", Table: "members", Column: "email", Kind: diff.OperationAddColumn},
		{ID: "replace_constraint_postgresql_members_members_email_idx", Table: "members", Constraint: "members_email_idx", Kind: diff.OperationReplaceConstraint},
	}, []diff.ProposedOperation{{ID: plan.Operations[0].ID, Table: plan.Operations[0].Table, Column: plan.Operations[0].Column, Kind: plan.Operations[0].Kind}, {ID: plan.Operations[1].ID, Table: plan.Operations[1].Table, Constraint: plan.Operations[1].Constraint, Kind: plan.Operations[1].Kind}})
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
		Source:     "001_create_table_projects.sql",
		SQL:        "CREATE TABLE projects (id bigint PRIMARY KEY, owner_id bigint NOT NULL);\n",
		ReverseSQL: "DROP TABLE projects;\n",
		Summary:    "create table projects",
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

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Equal(t, "backfill_postgresql_members_email", plan.Decisions[0].ID)
}

func TestDiffGeneratesNewRequiredColumnWithDefault(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY, active boolean NOT NULL DEFAULT true);")

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Equal(t, []diff.PlannedStatement{{
		Source:     "001_add_column_members_active.sql",
		SQL:        "ALTER TABLE members ADD COLUMN active boolean NOT NULL DEFAULT TRUE;\n",
		ReverseSQL: "ALTER TABLE members DROP COLUMN active;\n",
		Summary:    "add column members.active",
	}}, plan.Statements)
}

func TestDiffRejectsNewRequiredPrimaryKeyColumnWithDefaultWhenPrimaryKeyFollowsNotNull(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY, active integer NOT NULL DEFAULT 1 PRIMARY KEY);")

	_, err := analyzer.Diff(baseline, target)
	require.ErrorContains(t, err, "table members constraints changed")
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

			plan, err := analyzer.Diff(baseline, target)
			require.NoError(t, err)
			require.Equal(t, "backfill_postgresql_members_email", plan.Decisions[0].ID)
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

func TestDiffProposesConfirmedCompatibleRename(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (name text);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (display_name text);")
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Len(t, plan.Decisions, 1)
	require.Equal(t, diff.DecisionRename, plan.Decisions[0].Kind)
	require.Equal(t, "name", plan.Decisions[0].Baseline)
	resolved, err := plan.Resolve(diff.Resolution{DecisionID: plan.Decisions[0].ID, RenameFrom: "name"})
	require.NoError(t, err)
	require.Contains(t, resolved.Statements[0].SQL, "name TO display_name")
	require.Contains(t, resolved.Statements[0].ReverseSQL, "display_name TO name")
}

func TestDiffLowersQuotedRenameFromIndependentASTNames(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, `CREATE TABLE "members" ("Old Name" text);`)
	target := parseSnapshot(t, analyzer, `CREATE TABLE "members" ("New Name" text);`)
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	resolved, err := plan.Resolve(diff.Resolution{DecisionID: plan.Decisions[0].ID, RenameFrom: "Old Name"})
	require.NoError(t, err)
	expected := diff.ProposedOperation{
		ID: "add_column_postgresql_members_new name", Table: "members", Column: "New Name",
		Summary: "rename column members.New Name", Kind: diff.OperationAddColumn,
		Forward: []diff.PlannedStatement{{
			Source:     "001_rename_column_members.sql",
			SQL:        "ALTER TABLE \"members\" RENAME COLUMN \"Old Name\" TO \"New Name\";\n",
			ReverseSQL: "ALTER TABLE \"members\" RENAME COLUMN \"New Name\" TO \"Old Name\";\n",
			Summary:    "rename column members.New Name",
		}},
		Reverse: []diff.PlannedStatement{{
			Source:     "001_rename_column_members.sql",
			SQL:        "ALTER TABLE \"members\" RENAME COLUMN \"New Name\" TO \"Old Name\";\n",
			ReverseSQL: "ALTER TABLE \"members\" RENAME COLUMN \"Old Name\" TO \"New Name\";\n",
			Summary:    "rename column members.New Name",
		}},
	}
	require.Equal(t, expected, resolved.Operations[0])
	require.Equal(t, expected.Forward[0], resolved.Statements[0])
}

func TestDiffLowersPostgreSQLBackfillInNativeOrder(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, `CREATE TABLE tasks (id bigint PRIMARY KEY, owner_id bigint);`)
	target := parseSnapshot(t, analyzer, `CREATE TABLE tasks (id bigint PRIMARY KEY, owner_id bigint, owner_label text NOT NULL);`)
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	first, err := plan.Resolve(diff.Resolution{DecisionID: plan.Decisions[0].ID, BackfillSQL: `UPDATE "tasks" SET "owner_label" = 'owner-' || "owner_id";`})
	require.NoError(t, err)
	require.Equal(t, diff.OperationAddColumn, first.Operations[0].Kind)
	require.Equal(t, "add_column_postgresql_tasks_owner_label", first.Operations[0].ID)
	require.Equal(t, "tasks", first.Operations[0].Table)
	require.Equal(t, "owner_label", first.Operations[0].Column)
	require.Equal(t, "add column tasks.owner_label", first.Operations[0].Summary)
	require.Equal(t, []diff.PlannedStatement{{
		Source:     "001_add_column_tasks_owner_label.sql",
		SQL:        "ALTER TABLE tasks ADD COLUMN owner_label text;\nUPDATE \"tasks\" SET \"owner_label\" = 'owner-' || \"owner_id\";\nALTER TABLE tasks ALTER COLUMN owner_label SET NOT NULL;\n",
		ReverseSQL: "ALTER TABLE tasks DROP COLUMN owner_label;\n",
		Summary:    "add column tasks.owner_label",
	}}, first.Operations[0].Forward)
	require.Equal(t, []diff.PlannedStatement{{
		Source:     "001_add_column_tasks_owner_label.sql",
		SQL:        "ALTER TABLE tasks DROP COLUMN owner_label;\n",
		ReverseSQL: "ALTER TABLE tasks ADD COLUMN owner_label text;\nUPDATE \"tasks\" SET \"owner_label\" = 'owner-' || \"owner_id\";\nALTER TABLE tasks ALTER COLUMN owner_label SET NOT NULL;\n",
		Summary:    "add column tasks.owner_label",
	}}, first.Operations[0].Reverse)
	require.Equal(t, first.Operations[0].Forward, first.Operations[0].Forward)
	require.Equal(t, first.Operations[0].Forward, first.Operations[0].Forward)
	require.Equal(t, first.Operations[0].Forward[0], first.Statements[0])
	require.Equal(t, "caller-supplied backfill has no inferred reverse", first.IrreversibleReason)
}

func TestDiffLowersExistingColumnNullabilityBothDirections(t *testing.T) {
	analyzer := postgresql.New()
	base := parseSnapshot(t, analyzer, "CREATE TABLE users (name text NOT NULL);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE users (name text);")
	plan, err := analyzer.Diff(base, target)
	require.NoError(t, err)
	require.Equal(t, []diff.ProposedOperation{{ID: "alter_nullability_postgresql_users_name", Table: "users", Column: "name", Summary: "alter nullability users.name", Kind: diff.OperationAlterNullability, Forward: []diff.PlannedStatement{{Source: "001_alter_nullability_users_name.sql", SQL: "ALTER TABLE users ALTER COLUMN name DROP NOT NULL;\n", ReverseSQL: "ALTER TABLE users ALTER COLUMN name SET NOT NULL;\n", Summary: "alter nullability users.name"}}, Reverse: []diff.PlannedStatement{{Source: "001_alter_nullability_users_name.sql", SQL: "ALTER TABLE users ALTER COLUMN name SET NOT NULL;\n", ReverseSQL: "ALTER TABLE users ALTER COLUMN name DROP NOT NULL;\n", Summary: "alter nullability users.name"}}}}, plan.Operations)
	base = parseSnapshot(t, analyzer, "CREATE TABLE users (name text);")
	target = parseSnapshot(t, analyzer, "CREATE TABLE users (name text NOT NULL);")
	plan, err = analyzer.Diff(base, target)
	require.NoError(t, err)
	require.Equal(t, diff.OperationAlterNullability, plan.Operations[0].Kind)
	resolved := plan.Operations[0]
	require.Equal(t, "ALTER TABLE users ALTER COLUMN name SET NOT NULL;\n", resolved.Forward[0].SQL)
	require.Equal(t, "ALTER TABLE users ALTER COLUMN name DROP NOT NULL;\n", resolved.Reverse[0].SQL)
}

func TestDiffRefusesIdentityModeAlterationsPrecisely(t *testing.T) {
	tests := []struct {
		name, baseline, target, diagnostic string
	}{
		{"always to default", "GENERATED ALWAYS AS IDENTITY", "GENERATED BY DEFAULT AS IDENTITY", "column users.id identity mode changed from GENERATED ALWAYS AS IDENTITY to GENERATED BY DEFAULT AS IDENTITY; manual migration required"},
		{"default to absent", "GENERATED BY DEFAULT AS IDENTITY", "", "column users.id identity mode changed from GENERATED BY DEFAULT AS IDENTITY to absent; manual migration required"},
		{"absent to always", "", "GENERATED ALWAYS AS IDENTITY", "column users.id identity mode changed from absent to GENERATED ALWAYS AS IDENTITY; manual migration required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analyzer := postgresql.New()
			base := parseSnapshot(t, analyzer, "CREATE TABLE users (id bigint "+test.baseline+");")
			target := parseSnapshot(t, analyzer, "CREATE TABLE users (id bigint "+test.target+");")
			plan, err := analyzer.Diff(base, target)
			require.Equal(t, diff.Plan{}, plan)
			require.EqualError(t, err, "postgresql schema diff requires manual migration:\n- "+test.diagnostic)
		})
	}
}

func TestTaskboardNullabilityAndForeignKeyReplacement(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, `CREATE TABLE "tasks" (
  "id" BIGINT NOT NULL GENERATED ALWAYS AS IDENTITY,
  "project_id" BIGINT NOT NULL,
  "assignee_id" BIGINT NOT NULL,
  "title" TEXT NOT NULL,
  "is_open" BOOLEAN NOT NULL DEFAULT true,
  "created_at" TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY ("id"),
  CONSTRAINT "tasks_assignee_id_fkey" FOREIGN KEY ("assignee_id") REFERENCES "members" ("id") ON DELETE NO ACTION ON UPDATE NO ACTION,
  CONSTRAINT "tasks_project_id_fkey" FOREIGN KEY ("project_id") REFERENCES "projects" ("id") ON DELETE CASCADE ON UPDATE NO ACTION
);`)
	target := parseSnapshot(t, analyzer, `CREATE TABLE "tasks" (
  "id" BIGINT NOT NULL GENERATED ALWAYS AS IDENTITY,
  "project_id" BIGINT NOT NULL,
  "assignee_id" BIGINT,
  "title" TEXT NOT NULL,
  "is_open" BOOLEAN NOT NULL DEFAULT true,
  "created_at" TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY ("id"),
  CONSTRAINT "tasks_assignee_id_fkey" FOREIGN KEY ("assignee_id") REFERENCES "members" ("id") ON DELETE SET NULL ON UPDATE NO ACTION,
  CONSTRAINT "tasks_project_id_fkey" FOREIGN KEY ("project_id") REFERENCES "projects" ("id") ON DELETE CASCADE ON UPDATE NO ACTION
);`)
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	resolved, err := plan.Resolve()
	require.NoError(t, err)
	name := "replace constraint tasks.tasks_assignee_id_fkey"
	dropForward := diff.PlannedStatement{Source: "001_drop_constraint_tasks_tasks_assignee_id_fkey.sql", SQL: "ALTER TABLE \"tasks\" DROP CONSTRAINT \"tasks_assignee_id_fkey\";\n", ReverseSQL: "ALTER TABLE \"tasks\" ADD CONSTRAINT \"tasks_assignee_id_fkey\" FOREIGN KEY (\"assignee_id\") REFERENCES \"members\" (\"id\") ON DELETE NO ACTION ON UPDATE NO ACTION;\n", Summary: name}
	alterForward := diff.PlannedStatement{Source: "002_alter_nullability_tasks_assignee_id.sql", SQL: "ALTER TABLE \"tasks\" ALTER COLUMN \"assignee_id\" DROP NOT NULL;\n", ReverseSQL: "ALTER TABLE \"tasks\" ALTER COLUMN \"assignee_id\" SET NOT NULL;\n", Summary: "alter nullability tasks.assignee_id"}
	addForward := diff.PlannedStatement{Source: "003_add_constraint_tasks_tasks_assignee_id_fkey.sql", SQL: "ALTER TABLE \"tasks\" ADD CONSTRAINT \"tasks_assignee_id_fkey\" FOREIGN KEY (\"assignee_id\") REFERENCES \"members\" (\"id\") ON DELETE SET NULL ON UPDATE NO ACTION;\n", ReverseSQL: "ALTER TABLE \"tasks\" DROP CONSTRAINT \"tasks_assignee_id_fkey\";\n", Summary: name}
	expectedOperations := []diff.ProposedOperation{
		{ID: "alter_nullability_postgresql_tasks_assignee_id", Table: "tasks", Column: "assignee_id", Summary: "alter nullability tasks.assignee_id", Kind: diff.OperationAlterNullability, Forward: []diff.PlannedStatement{alterForward}, Reverse: []diff.PlannedStatement{{Source: alterForward.Source, SQL: alterForward.ReverseSQL, ReverseSQL: alterForward.SQL, Summary: alterForward.Summary}}},
		{ID: "replace_constraint_postgresql_tasks_tasks_assignee_id_fkey", Table: "tasks", Constraint: "tasks_assignee_id_fkey", Summary: name, Kind: diff.OperationReplaceConstraint, Forward: []diff.PlannedStatement{dropForward, addForward}, Reverse: []diff.PlannedStatement{{Source: addForward.Source, SQL: addForward.ReverseSQL, ReverseSQL: addForward.SQL, Summary: name}, {Source: dropForward.Source, SQL: dropForward.ReverseSQL, ReverseSQL: dropForward.SQL, Summary: name}}},
	}
	require.Equal(t, expectedOperations, resolved.Operations)
	operation := resolved.Operations[1]
	require.Equal(t, addForward.Source, operation.Reverse[0].Source)
	require.Equal(t, addForward.ReverseSQL, operation.Reverse[0].SQL)
	require.Equal(t, addForward.SQL, operation.Reverse[0].ReverseSQL)
	require.Equal(t, dropForward.Source, operation.Reverse[1].Source)
	require.Equal(t, dropForward.ReverseSQL, operation.Reverse[1].SQL)
	require.Equal(t, dropForward.SQL, operation.Reverse[1].ReverseSQL)
	require.Equal(t, []diff.PlannedStatement{dropForward, alterForward, addForward}, resolved.Statements)
	root := t.TempDir()
	require.NoError(t, diff.WriteMigration(filepath.Join(root, "001_taskboard"), resolved))
	loaded, err := migrationdir.Load(root)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Equal(t, []string{"001_drop_constraint_tasks_tasks_assignee_id_fkey.up.sql", "002_alter_nullability_tasks_assignee_id.up.sql", "003_add_constraint_tasks_tasks_assignee_id_fkey.up.sql"}, []string{loaded[0].Statements[0].Source, loaded[0].Statements[1].Source, loaded[0].Statements[2].Source})
	require.Equal(t, []string{"003_add_constraint_tasks_tasks_assignee_id_fkey.down.sql", "002_alter_nullability_tasks_assignee_id.down.sql", "001_drop_constraint_tasks_tasks_assignee_id_fkey.down.sql"}, []string{loaded[0].Down[0].Source, loaded[0].Down[1].Source, loaded[0].Down[2].Source})
	for index, statement := range resolved.Statements {
		require.Equal(t, statement.SQL, string(loaded[0].Statements[index].SQL))
	}
	require.Equal(t, resolved.Operations[1].Reverse[0].SQL, string(loaded[0].Down[0].SQL))
	require.Equal(t, resolved.Operations[0].Reverse[0].SQL, string(loaded[0].Down[1].SQL))
	require.Equal(t, resolved.Operations[1].Reverse[1].SQL, string(loaded[0].Down[2].SQL))
}

func TestDiffRefusesAnonymousAndInlineForeignKeyChanges(t *testing.T) {
	analyzer := postgresql.New()
	base := parseSnapshot(t, analyzer, "CREATE TABLE child (parent_id bigint REFERENCES parent (id) ON DELETE NO ACTION);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE child (parent_id bigint REFERENCES parent (id) ON DELETE CASCADE);")
	_, err := analyzer.Diff(base, target)
	require.EqualError(t, err, "postgresql schema diff requires manual migration:\n- table child constraints changed")

	base = parseSnapshot(t, analyzer, "CREATE TABLE things (id bigint, UNIQUE (id));")
	target = parseSnapshot(t, analyzer, "CREATE TABLE things (id bigint, PRIMARY KEY (id));")
	_, err = analyzer.Diff(base, target)
	require.EqualError(t, err, "postgresql schema diff requires manual migration:\n- table things constraints changed")
}

func TestDiffLowersNamedConstraintReplacements(t *testing.T) {
	tests := []struct {
		name, baseline, target, constraint, baselineFragment, targetFragment string
	}{
		{
			name:             "unique",
			baseline:         `CREATE TABLE things (id bigint, code text, label text, CONSTRAINT things_code_key UNIQUE (code));`,
			target:           `CREATE TABLE things (id bigint, code text, label text, CONSTRAINT things_code_key UNIQUE (label));`,
			constraint:       "things_code_key",
			baselineFragment: `CONSTRAINT things_code_key UNIQUE (code)`,
			targetFragment:   `CONSTRAINT things_code_key UNIQUE (label)`,
		},
		{
			name:             "check",
			baseline:         `CREATE TABLE things (id bigint, quantity integer, CONSTRAINT things_quantity_check CHECK (quantity > 0));`,
			target:           `CREATE TABLE things (id bigint, quantity integer, CONSTRAINT things_quantity_check CHECK (quantity >= 0));`,
			constraint:       "things_quantity_check",
			baselineFragment: `CONSTRAINT things_quantity_check CHECK (quantity > 0)`,
			targetFragment:   `CONSTRAINT things_quantity_check CHECK (quantity >= 0)`,
		},
		{
			name:             "primary key",
			baseline:         `CREATE TABLE things (id bigint, code text, CONSTRAINT things_pkey PRIMARY KEY (id, code));`,
			target:           `CREATE TABLE things (id bigint, code text, CONSTRAINT things_pkey PRIMARY KEY (code, id));`,
			constraint:       "things_pkey",
			baselineFragment: `CONSTRAINT things_pkey PRIMARY KEY (id, code)`,
			targetFragment:   `CONSTRAINT things_pkey PRIMARY KEY (code, id)`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analyzer := postgresql.New()
			baseline := parseSnapshot(t, analyzer, test.baseline)
			target := parseSnapshot(t, analyzer, test.target)
			plan, err := analyzer.Diff(baseline, target)
			require.NoError(t, err)
			resolved, err := plan.Resolve()
			require.NoError(t, err)
			require.Equal(t, []diff.ProposedOperation{{
				ID:    "replace_constraint_postgresql_things_" + test.constraint,
				Table: "things", Constraint: test.constraint,
				Summary: "replace constraint things." + test.constraint, Kind: diff.OperationReplaceConstraint,
				Forward: []diff.PlannedStatement{
					{Source: "001_drop_constraint_things_" + test.constraint + ".sql", SQL: "ALTER TABLE things DROP CONSTRAINT " + test.constraint + ";\n", ReverseSQL: "ALTER TABLE things ADD " + test.baselineFragment + ";\n", Summary: "replace constraint things." + test.constraint},
					{Source: "002_add_constraint_things_" + test.constraint + ".sql", SQL: "ALTER TABLE things ADD " + test.targetFragment + ";\n", ReverseSQL: "ALTER TABLE things DROP CONSTRAINT " + test.constraint + ";\n", Summary: "replace constraint things." + test.constraint},
				},
				Reverse: []diff.PlannedStatement{
					{Source: "002_add_constraint_things_" + test.constraint + ".sql", SQL: "ALTER TABLE things DROP CONSTRAINT " + test.constraint + ";\n", ReverseSQL: "ALTER TABLE things ADD " + test.targetFragment + ";\n", Summary: "replace constraint things." + test.constraint},
					{Source: "001_drop_constraint_things_" + test.constraint + ".sql", SQL: "ALTER TABLE things ADD " + test.baselineFragment + ";\n", ReverseSQL: "ALTER TABLE things DROP CONSTRAINT " + test.constraint + ";\n", Summary: "replace constraint things." + test.constraint},
				},
			}}, resolved.Operations)
			require.Equal(t, resolved.Operations[0].Forward, resolved.Statements)
			root := t.TempDir()
			require.NoError(t, diff.WriteMigration(filepath.Join(root, "001_constraint"), resolved))
			loaded, err := migrationdir.Load(root)
			require.NoError(t, err)
			require.Len(t, loaded, 1)
			require.Len(t, loaded[0].Statements, 2)
			require.Len(t, loaded[0].Down, 2)
			expectedDown := resolved.Operations[0].Reverse
			for index, statement := range resolved.Statements {
				require.Equal(t, statement.Source[:len(statement.Source)-len(".sql")]+".up.sql", loaded[0].Statements[index].Source)
				require.Equal(t, statement.SQL, string(loaded[0].Statements[index].SQL))
			}
			for index, statement := range expectedDown {
				require.Equal(t, statement.Source[:len(statement.Source)-len(".sql")]+".down.sql", loaded[0].Down[index].Source)
				require.Equal(t, statement.SQL, string(loaded[0].Down[index].SQL))
			}
		})
	}
}

func TestDiffDoesNotPublishPartialPlanForAnonymousConstraintChange(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, `CREATE TABLE things (id bigint, parent_id bigint, CONSTRAINT things_parent_fkey FOREIGN KEY (parent_id) REFERENCES parents (id), UNIQUE (id));`)
	target := parseSnapshot(t, analyzer, `CREATE TABLE things (id bigint, parent_id bigint, CONSTRAINT things_parent_fkey FOREIGN KEY (parent_id) REFERENCES parents (id) ON DELETE CASCADE, CHECK (id > 0));`)
	plan, err := analyzer.Diff(baseline, target)
	require.Equal(t, diff.Plan{}, plan)
	require.EqualError(t, err, "postgresql schema diff requires manual migration:\n- table things constraints changed")
}

func TestCreateIndexSchedulesWithoutConstraintReplacementPanic(t *testing.T) {
	analyzer := postgresql.New()
	base := parseSnapshot(t, analyzer, "CREATE TABLE users (id bigint);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE users (id bigint); CREATE INDEX users_id_idx ON users (id);")
	plan, err := analyzer.Diff(base, target)
	require.NoError(t, err)
	resolved, err := plan.Resolve()
	require.NoError(t, err)
	require.Len(t, resolved.Statements, 1)
	require.Equal(t, "001_create_index_users_id_idx.sql", resolved.Statements[0].Source)
	require.Equal(t, "CREATE INDEX users_id_idx ON users (id);\n", resolved.Statements[0].SQL)
	require.Equal(t, "DROP INDEX users_id_idx;\n", resolved.Statements[0].ReverseSQL)
}

func TestDiffRefusesIncompatibleRenameCandidate(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (name text);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (display_name integer);")
	_, err := analyzer.Diff(baseline, target)
	require.Error(t, err)
}

func TestDiffRefusesAmbiguousRenameCandidates(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (first text, second text);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (given text, family text);")
	_, err := analyzer.Diff(baseline, target)
	require.Error(t, err)
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

func TestDiffPlansConcurrentIndexAsNonTransactional(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, `
		CREATE TABLE members (id bigint PRIMARY KEY);
		CREATE INDEX CONCURRENTLY members_id_idx ON members (id);
	`)

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Equal(t, migrate.ExecutionModeNonTransactional, plan.Mode)
	require.Len(t, plan.Statements, 1)
	require.Contains(t, plan.Statements[0].SQL, "CREATE INDEX CONCURRENTLY")
	require.Contains(t, plan.Statements[0].ReverseSQL, "DROP INDEX CONCURRENTLY")
}

func TestResolvedPlanPreservesNonTransactionalMode(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE tasks (id bigint PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, `
		CREATE TABLE tasks (id bigint PRIMARY KEY, owner_label text NOT NULL);
		CREATE INDEX CONCURRENTLY tasks_owner_label_idx ON tasks (owner_label);
	`)

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Len(t, plan.Decisions, 1)
	resolved, err := plan.Resolve(diff.Resolution{
		DecisionID:  plan.Decisions[0].ID,
		BackfillSQL: "UPDATE tasks SET owner_label = 'owner';",
	})
	require.NoError(t, err)
	require.Equal(t, migrate.ExecutionModeNonTransactional, resolved.Mode)
	require.Contains(t, resolved.Statements[len(resolved.Statements)-1].SQL, "CREATE INDEX CONCURRENTLY")
	require.Contains(t, resolved.Statements[len(resolved.Statements)-1].ReverseSQL, "DROP INDEX CONCURRENTLY")
}

func parseSnapshot(t *testing.T, analyzer postgresql.Analyzer, source string) diff.Snapshot {
	t.Helper()
	snapshot, err := analyzer.Parse([]diff.Source{{Path: "schema.sql", SQL: sqltext.Text(source)}})
	require.NoError(t, err)
	return snapshot
}

func parseSources(t *testing.T, analyzer postgresql.Analyzer, sources []diff.Source) diff.Snapshot {
	t.Helper()
	snapshot, err := analyzer.Parse(sources)
	require.NoError(t, err)
	return snapshot
}
