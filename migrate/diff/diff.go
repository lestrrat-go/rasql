// Package diff generates reviewed SQL migrations from desired schema sources.
package diff

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lestrrat-go/rasql/migrate"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
)

// Source is one SQL file in a desired schema tree.
type Source struct {
	Path string
	SQL  sqltext.Text
}

// Snapshot is a parsed desired schema owned by one Analyzer.
type Snapshot interface {
	Dialect() string
}

// Analyzer parses and compares desired schemas for one database dialect.
type Analyzer interface {
	Dialect() string
	Parse([]Source) (Snapshot, error)
	Diff(Snapshot, Snapshot) (Plan, error)
}

// LiveAnalyzer supplies the dialect-specific boundary used by diff-live.
// LiveSources converts one inspected table into desired-schema sources, and
// ValidateLivePlan checks that generated statements stay within that table.
type LiveAnalyzer interface {
	Analyzer
	LiveSources(schema.TableDef) ([]Source, error)
	ValidateLivePlan(Plan, string) error
}

// Schema groups the tables and indexes in one parsed schema. The map keys are
// canonicalized by the dialect package that parsed the schema.
type Schema[Table, Index any] struct {
	Tables  map[string]Table
	Indexes map[string]Index
}

// SchemaEntry identifies one schema object by its dialect-canonical key.
type SchemaEntry[T any] struct {
	Key   string
	Value T
}

// SchemaPair contains the baseline and target versions of one schema object.
type SchemaPair[T any] struct {
	Key      string
	Baseline T
	Target   T
	Equal    bool
}

// SchemaChanges describes added, removed, and matched objects in one category.
type SchemaChanges[T any] struct {
	Added   []SchemaEntry[T]
	Removed []SchemaEntry[T]
	Matched []SchemaPair[T]
}

// SchemaComparison is the canonical comparison of two parsed schemas.
type SchemaComparison[Table, Index any] struct {
	Tables  SchemaChanges[Table]
	Indexes SchemaChanges[Index]
}

// CompareSchemas compares schema objects by their dialect-canonical keys.
// Dialects supply equality functions because identifier folding and metadata
// normalization are database rules, not shared migration rules.
func CompareSchemas[Table, Index any](baseline, target Schema[Table, Index], tableEqual func(Table, Table) bool, indexEqual func(Index, Index) bool) SchemaComparison[Table, Index] {
	return SchemaComparison[Table, Index]{
		Tables:  compareSchemaObjects(baseline.Tables, target.Tables, tableEqual),
		Indexes: compareSchemaObjects(baseline.Indexes, target.Indexes, indexEqual),
	}
}

func compareSchemaObjects[T any](baseline, target map[string]T, equal func(T, T) bool) SchemaChanges[T] {
	changes := SchemaChanges[T]{
		Added:   make([]SchemaEntry[T], 0),
		Removed: make([]SchemaEntry[T], 0),
		Matched: make([]SchemaPair[T], 0),
	}
	keys := make([]string, 0, len(target))
	for key := range target {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		targetValue := target[key]
		baselineValue, exists := baseline[key]
		if !exists {
			changes.Added = append(changes.Added, SchemaEntry[T]{Key: key, Value: targetValue})
			continue
		}
		changes.Matched = append(changes.Matched, SchemaPair[T]{
			Key:      key,
			Baseline: baselineValue,
			Target:   targetValue,
			Equal:    equal == nil || equal(baselineValue, targetValue),
		})
	}
	keys = keys[:0]
	for key := range baseline {
		if _, exists := target[key]; !exists {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		changes.Removed = append(changes.Removed, SchemaEntry[T]{Key: key, Value: baseline[key]})
	}
	return changes
}

// Plan is a reviewed set of SQL sources generated for one migration.
type Plan struct {
	Dialect            string
	Operations         []ProposedOperation
	Decisions          []RequiredDecision
	Mode               migrate.ExecutionMode
	Statements         []PlannedStatement
	IrreversibleReason string
	lowerer            Lowerer
}

// OperationKind identifies the kind of schema change proposed by an analyzer.
type OperationKind string

const (
	OperationCreateTable       OperationKind = "create_table"
	OperationAddColumn         OperationKind = "add_column"
	OperationAlterNullability  OperationKind = "alter_nullability"
	OperationReplaceConstraint OperationKind = "replace_constraint"
	OperationRebuildTable      OperationKind = "rebuild_table"
)

// ProposedOperation is a reviewable schema change. Forward and Reverse are
// populated only when the operation can be lowered without a decision.
type ProposedOperation struct {
	ID, Table, Column, Constraint, Summary string
	Kind                                   OperationKind
	Forward, Reverse                       []PlannedStatement
}

// DecisionKind identifies caller-owned information needed before execution.
type DecisionKind string

const (
	DecisionBackfill DecisionKind = "backfill"
	DecisionRename   DecisionKind = "rename"
)

// RequiredDecision records information which cannot safely be inferred.
type RequiredDecision struct {
	ID, Table, Column, Baseline, Target, Reason string
	Kind                                        DecisionKind
}

// Resolution supplies one caller-owned answer to a RequiredDecision.
type Resolution struct {
	DecisionID  string
	BackfillSQL string
	RenameFrom  string
}

// OperationID returns the stable identifier format used by analyzers.
func OperationID(kind OperationKind, dialect, table, column, constraint string) string {
	parts := []string{string(kind), strings.ToLower(dialect), strings.ToLower(table)}
	if column != "" {
		parts = append(parts, strings.ToLower(column))
	}
	if constraint != "" {
		parts = append(parts, strings.ToLower(constraint))
	}
	return strings.Join(parts, "_")
}

// DecisionID returns the stable identifier format used by analyzers.
func DecisionID(kind DecisionKind, dialect, table, column string) string {
	return strings.Join([]string{string(kind), strings.ToLower(dialect), strings.ToLower(table), strings.ToLower(column)}, "_")
}

// LoweringResult is the immutable result of lowering a plan after all
// required decisions have been supplied.
type LoweringResult struct {
	Operations         []ProposedOperation
	Mode               migrate.ExecutionMode
	Statements         []PlannedStatement
	IrreversibleReason string
}

// Lowerer supplies dialect-native lowering for a plan. The map is keyed by
// decision ID and is owned by the call; implementations must not retain it.
type Lowerer func(map[string]Resolution) (LoweringResult, error)

// NewPlan constructs a plan from analyzer-owned operation and decision
// metadata. The lowerer is retained privately so callers cannot mutate the
// plan's lowering state.
func NewPlan(dialect string, operations []ProposedOperation, decisions []RequiredDecision, lowerer Lowerer) (Plan, error) {
	plan := Plan{
		Dialect:    dialect,
		Operations: cloneOperations(operations),
		Decisions:  append([]RequiredDecision(nil), decisions...),
		lowerer:    lowerer,
	}
	if len(plan.Decisions) == 0 && lowerer != nil {
		lowered, err := lowerer(nil)
		if err != nil {
			return Plan{}, fmt.Errorf("migrate diff: lower plan: %w", err)
		}
		plan.Operations = cloneOperations(lowered.Operations)
		plan.Mode = lowered.Mode
		plan.Statements = cloneStatements(lowered.Statements)
		plan.IrreversibleReason = lowered.IrreversibleReason
		if err := plan.Validate(); err != nil {
			return Plan{}, fmt.Errorf("migrate diff: lowered plan: %w", err)
		}
	} else if err := plan.Validate(); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

// Executable reports whether all required decisions have been supplied.
func (p Plan) Executable() bool { return len(p.Decisions) == 0 && len(p.Statements) > 0 }

// Resolve returns an independent executable copy after applying every answer.
func (p Plan) Resolve(resolutions ...Resolution) (Plan, error) {
	if len(resolutions) != len(p.Decisions) {
		return Plan{}, fmt.Errorf("migrate diff: expected one resolution for each decision, got %d for %d", len(resolutions), len(p.Decisions))
	}
	byID := make(map[string]RequiredDecision, len(p.Decisions))
	for _, decision := range p.Decisions {
		if decision.ID == "" || decision.Kind == "" {
			return Plan{}, fmt.Errorf("migrate diff: invalid decision metadata")
		}
		byID[decision.ID] = decision
	}
	copyPlan := p.clone()
	copyPlan.Decisions = nil
	copyPlan.Statements = nil
	seen := make(map[string]struct{}, len(resolutions))
	for _, resolution := range resolutions {
		decision, ok := byID[resolution.DecisionID]
		if !ok {
			return Plan{}, fmt.Errorf("migrate diff: unknown decision %q", resolution.DecisionID)
		}
		if _, ok := seen[resolution.DecisionID]; ok {
			return Plan{}, fmt.Errorf("migrate diff: duplicate resolution for %q", resolution.DecisionID)
		}
		seen[resolution.DecisionID] = struct{}{}
		switch decision.Kind {
		case DecisionBackfill:
			if strings.TrimSpace(resolution.BackfillSQL) == "" || strings.TrimSpace(resolution.RenameFrom) != "" {
				return Plan{}, fmt.Errorf("migrate diff: backfill resolution %q is empty", decision.ID)
			}
			if err := validateNativeSQL(p.Dialect, resolution.BackfillSQL); err != nil {
				return Plan{}, fmt.Errorf("migrate diff: backfill resolution %q: %w", decision.ID, err)
			}
		case DecisionRename:
			if strings.TrimSpace(resolution.RenameFrom) == "" || strings.TrimSpace(resolution.BackfillSQL) != "" {
				return Plan{}, fmt.Errorf("migrate diff: rename resolution %q is empty", decision.ID)
			}
			if resolution.RenameFrom != decision.Baseline {
				return Plan{}, fmt.Errorf("migrate diff: rename resolution %q names non-candidate column %q", decision.ID, resolution.RenameFrom)
			}
		default:
			return Plan{}, fmt.Errorf("migrate diff: unsupported decision kind %q", decision.Kind)
		}
		seen[resolution.DecisionID] = struct{}{}
	}
	if len(seen) != len(byID) {
		return Plan{}, fmt.Errorf("migrate diff: unresolved decision IDs remain")
	}
	if p.lowerer != nil {
		answers := make(map[string]Resolution, len(resolutions))
		for _, resolution := range resolutions {
			answers[resolution.DecisionID] = resolution
		}
		lowered, err := p.lowerer(answers)
		if err != nil {
			return Plan{}, fmt.Errorf("migrate diff: lower resolved plan: %w", err)
		}
		copyPlan.Operations = cloneOperations(lowered.Operations)
		copyPlan.Mode = lowered.Mode
		copyPlan.Statements = cloneStatements(lowered.Statements)
		copyPlan.IrreversibleReason = lowered.IrreversibleReason
	} else {
		for _, decision := range p.Decisions {
			if decision.Kind == DecisionRename {
				forward := PlannedStatement{Source: "000_rename_" + decision.ID + ".sql", SQL: fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s;\n", decision.Table, decision.Baseline, decision.Target), ReverseSQL: fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s;\n", decision.Table, decision.Target, decision.Baseline), Summary: "rename " + decision.Table + "." + decision.Baseline}
				copyPlan.Statements = append(copyPlan.Statements, forward)
				continue
			}
			if decision.Kind != DecisionBackfill {
				continue
			}
			resolution := answersFor(resolutions, decision.ID)
			statement := PlannedStatement{
				Source:     "000_backfill_" + decision.ID + ".sql",
				SQL:        strings.TrimSpace(resolution.BackfillSQL) + "\n",
				ReverseSQL: "-- caller-supplied backfill has no inferred reverse\n",
				Summary:    "backfill " + decision.Table + "." + decision.Column,
			}
			if len(copyPlan.Operations) > 0 && len(copyPlan.Operations[0].Forward) > 0 {
				copyPlan.Operations[0].Forward[0].SQL = statement.SQL + copyPlan.Operations[0].Forward[0].SQL
				copyPlan.Statements = nil
			} else {
				copyPlan.Statements = append([]PlannedStatement{statement}, copyPlan.Statements...)
			}
			copyPlan.IrreversibleReason = "caller-supplied backfill has no inferred reverse"
		}
		for _, operation := range copyPlan.Operations {
			copyPlan.Statements = append(copyPlan.Statements, cloneStatements(operation.Forward)...)
		}
		if len(copyPlan.Statements) == 0 {
			copyPlan.Statements = cloneStatements(p.Statements)
		}
	}
	if err := copyPlan.Validate(); err != nil {
		return Plan{}, err
	}
	return copyPlan, nil
}

func answersFor(resolutions []Resolution, id string) Resolution {
	for _, resolution := range resolutions {
		if resolution.DecisionID == id {
			return resolution
		}
	}
	return Resolution{}
}

func validateNativeSQL(dialect, source string) error {
	return validateNativeSQLSource(dialect, source)
}

func (p Plan) clone() Plan {
	copyPlan := p
	copyPlan.Operations = append([]ProposedOperation(nil), p.Operations...)
	copyPlan.Decisions = append([]RequiredDecision(nil), p.Decisions...)
	copyPlan.Statements = cloneStatements(p.Statements)
	copyPlan.lowerer = p.lowerer
	for index := range copyPlan.Operations {
		copyPlan.Operations[index].Forward = cloneStatements(p.Operations[index].Forward)
		copyPlan.Operations[index].Reverse = cloneStatements(p.Operations[index].Reverse)
	}
	return copyPlan
}

func cloneOperations(in []ProposedOperation) []ProposedOperation {
	if in == nil {
		return nil
	}
	out := append([]ProposedOperation(nil), in...)
	for index := range out {
		out[index].Forward = cloneStatements(in[index].Forward)
		out[index].Reverse = cloneStatements(in[index].Reverse)
	}
	return out
}

func cloneStatements(in []PlannedStatement) []PlannedStatement {
	if in == nil {
		return nil
	}
	return append([]PlannedStatement(nil), in...)
}

// PlannedStatement is one generated native SQL source file.
type PlannedStatement struct {
	Source     string
	SQL        string
	ReverseSQL string
	Summary    string
}

// NumberSources prefixes each unnumbered SQL source with its plan ordinal.
// The shared width keeps lexical source order equal to numeric plan order,
// including plans that cross the 999-source boundary.
func NumberSources(statements []PlannedStatement) {
	if len(statements) == 0 {
		return
	}
	width := max(3, len(strconv.Itoa(len(statements))))
	for index := range statements {
		statements[index].Source = fmt.Sprintf("%0*d_%s", width, index+1, statements[index].Source)
	}
}

// Empty reports whether a plan contains no generated SQL sources.
func (p Plan) Empty() bool {
	return len(p.Statements) == 0 && len(p.Operations) == 0 && len(p.Decisions) == 0
}

// Validate reports whether p can be written as one migration directory.
func (p Plan) Validate() error {
	if p.Dialect == "" {
		return fmt.Errorf("migrate diff: plan dialect must not be empty")
	}
	if len(p.Statements) == 0 {
		if len(p.Operations) == 0 && len(p.Decisions) == 0 {
			return fmt.Errorf("migrate diff: plan has no SQL sources")
		}
	}
	decisionIDs := make(map[string]struct{}, len(p.Decisions))
	for _, decision := range p.Decisions {
		if decision.ID == "" || decision.Table == "" || decision.Column == "" || decision.Kind == "" {
			return fmt.Errorf("migrate diff: decision metadata is incomplete")
		}
		if _, exists := decisionIDs[decision.ID]; exists {
			return fmt.Errorf("migrate diff: duplicate decision ID %q", decision.ID)
		}
		decisionIDs[decision.ID] = struct{}{}
	}
	operationIDs := make(map[string]struct{}, len(p.Operations))
	for _, operation := range p.Operations {
		if operation.ID == "" || operation.Kind == "" || operation.Table == "" {
			return fmt.Errorf("migrate diff: operation metadata is incomplete")
		}
		if _, exists := operationIDs[operation.ID]; exists {
			return fmt.Errorf("migrate diff: duplicate operation ID %q", operation.ID)
		}
		operationIDs[operation.ID] = struct{}{}
		for _, statement := range append(cloneStatements(operation.Forward), operation.Reverse...) {
			if statement.Source == "" || filepath.Base(statement.Source) != statement.Source || strings.HasPrefix(statement.Source, ".") || filepath.Ext(statement.Source) != ".sql" {
				return fmt.Errorf("migrate diff: operation %q contains invalid SQL source", operation.ID)
			}
		}
	}
	if p.Mode != migrate.ExecutionModeAtomic && p.Mode != migrate.ExecutionModeNonTransactional {
		return fmt.Errorf("migrate diff: invalid execution mode %q", p.Mode)
	}
	sources := make(map[string]int, len(p.Statements))
	for index, statement := range p.Statements {
		if statement.Source == "" || filepath.Base(statement.Source) != statement.Source || strings.HasPrefix(statement.Source, ".") || filepath.Ext(statement.Source) != ".sql" {
			return fmt.Errorf("migrate diff: generated SQL source %d is invalid", index+1)
		}
		if strings.TrimSpace(statement.SQL) == "" {
			return fmt.Errorf("migrate diff: generated SQL source %q is empty", statement.Source)
		}
		if previous, exists := sources[statement.Source]; exists {
			return fmt.Errorf("migrate diff: duplicate generated SQL source %q for %q and %q", statement.Source, p.Statements[previous].Summary, statement.Summary)
		}
		sources[statement.Source] = index
	}
	if strings.TrimSpace(p.IrreversibleReason) != "" {
		if strings.TrimSpace(p.IrreversibleReason) != p.IrreversibleReason {
			return fmt.Errorf("migrate diff: irreversible reason must be trimmed")
		}
	} else {
		for _, statement := range p.Statements {
			if strings.TrimSpace(statement.ReverseSQL) == "" {
				return fmt.Errorf("migrate diff: generated SQL source %q has no reverse SQL", statement.Source)
			}
			if !utf8.ValidString(statement.ReverseSQL) {
				return fmt.Errorf("migrate diff: generated SQL source %q has invalid reverse SQL", statement.Source)
			}
		}
	}
	return nil
}

// WriteMigration writes p into a new migration directory at directory.
func WriteMigration(directory string, p Plan) error {
	if directory == "" {
		return fmt.Errorf("migrate diff: output directory must not be empty")
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if len(p.Decisions) > 0 {
		ids := make([]string, len(p.Decisions))
		for index, decision := range p.Decisions {
			ids[index] = decision.ID
		}
		sort.Strings(ids)
		return fmt.Errorf("migrate diff: unresolved decisions: %s", strings.Join(ids, ", "))
	}
	if !p.Executable() {
		return fmt.Errorf("migrate diff: plan is not executable")
	}
	parent := filepath.Dir(directory)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("migrate diff: create migration parent directory: %w", err)
	}
	temporary, err := os.MkdirTemp(parent, "."+filepath.Base(directory)+".tmp-")
	if err != nil {
		return fmt.Errorf("migrate diff: create temporary migration directory: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(temporary)
		}
	}()
	for _, statement := range p.Statements {
		path := filepath.Join(temporary, strings.TrimSuffix(statement.Source, ".sql")+".up.sql")
		if err := os.WriteFile(path, []byte(statement.SQL), 0o600); err != nil {
			return fmt.Errorf("migrate diff: write generated SQL source %q: %w", statement.Source, err)
		}
	}
	if p.Mode == migrate.ExecutionModeNonTransactional {
		if err := os.WriteFile(filepath.Join(temporary, ".rasql-mode"), []byte("nontransactional\n"), 0o600); err != nil {
			return fmt.Errorf("migrate diff: write execution mode: %w", err)
		}
	}
	if strings.TrimSpace(p.IrreversibleReason) != "" {
		if err := os.WriteFile(filepath.Join(temporary, ".rasql-irreversible"), []byte(p.IrreversibleReason+"\n"), 0o600); err != nil {
			return fmt.Errorf("migrate diff: write irreversibility marker: %w", err)
		}
	} else {
		for _, statement := range p.Statements {
			path := filepath.Join(temporary, strings.TrimSuffix(statement.Source, ".sql")+".down.sql")
			if err := os.WriteFile(path, []byte(statement.ReverseSQL), 0o600); err != nil {
				return fmt.Errorf("migrate diff: write reverse SQL source %q: %w", statement.Source, err)
			}
		}
	}
	if err := os.Rename(temporary, directory); err != nil {
		return fmt.Errorf("migrate diff: create migration directory %q: %w", directory, err)
	}
	committed = true
	return nil
}
