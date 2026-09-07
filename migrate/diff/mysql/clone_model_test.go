package mysql

import (
	"reflect"
	"strings"
	"testing"

	mysqlquery "github.com/lestrrat-go/rasql-mysql/query"
	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/lestrrat-go/rasql/sqltext"
)

const carrierFixture = "CREATE TABLE `t` (" +
	"`id` bigint NOT NULL AUTO_INCREMENT, " +
	"`amount` decimal(10,2) DEFAULT (1 + 2) CHECK (`amount` > 0), " +
	"`computed` int GENERATED ALWAYS AS (`amount` + 1) STORED, " +
	"`parent_id` bigint NOT NULL, " +
	"CONSTRAINT `pk_t` PRIMARY KEY (`id`), " +
	"CONSTRAINT `uq_amount` UNIQUE (`amount`), " +
	"CONSTRAINT `fk_parent` FOREIGN KEY (`parent_id`) REFERENCES `parent` (`id`), " +
	"CONSTRAINT `ck_amount` CHECK (`amount` > 0)); " +
	"CREATE INDEX `amount_idx` ON `t` (`amount`);"

func TestCloneLoweringModelCopiesAllOwnedValues(t *testing.T) {
	model, sourceTable, sourceIndex := carrierModel(t)
	want, err := cloneLoweringModel(model)
	if err != nil {
		t.Fatal(err)
	}
	copy, err := cloneLoweringModel(model)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(model, copy) {
		t.Fatal("clone changed carrier metadata before mutation")
	}

	entry := &copy.entries[0]
	entry.table[0].Name = "changed table"
	entry.targetTable.statement.Name[0].Name = "changed target table"
	entry.targetTable.statement.Columns[1].Name.Name = "changed target column"
	entry.targetTable.statement.Columns[1].Type.Modifiers[0].(*mysqlquery.Literal).Value = "99"
	entry.baselineColumn.AST.Name.Name = "changed baseline column"
	entry.baselineColumn.AST.Type.Modifiers[0].(*mysqlquery.Literal).Value = "98"
	entry.baselineColumn.AST.Constraints[0].Expression.(*mysqlquery.BinaryExpression).Left.(*mysqlquery.Literal).Value = "7"
	entry.targetColumn.AST.Constraints[1].Expression.(*mysqlquery.BinaryExpression).Right.(*mysqlquery.Literal).Value = "8"
	(*entry.targetColumn.Facts.Collation)[0].Name = "changed collation"
	entry.targetTable.columns["computed"].Generated.Expression = "changed generated expression"
	entry.targetTable.columns["computed"].Generated.Storage = "VIRTUAL"
	idFacts := entry.targetTable.columns["id"]
	idFacts.AutoIncrement = false
	entry.targetTable.columns["id"] = idFacts
	entry.targetConstraint.Name.Name = "changed constraint"
	entry.targetConstraint.Columns[0].Name = "changed owned column"
	entry.baselineConstraint.Expression.(*mysqlquery.BinaryExpression).Right.(*mysqlquery.Literal).Value = "9"
	entry.targetConstraint.References.Table[0].Name = "changed reference table"
	entry.targetConstraint.References.Columns[0].Name = "changed reference column"
	entry.targetIndex.Name[0].Name = "changed index"
	entry.targetIndex.Table[0].Name = "changed index table"
	entry.targetIndex.Elements[0].Expression.(*mysqlquery.IdentifierExpression).Name[0].Name = "changed index element"
	entry.operation.Forward[0].SQL = "changed forward"
	entry.operation.Reverse[0].SQL = "changed reverse"
	copy.decisions[0].Target = "changed decision"

	if !reflect.DeepEqual(model, want) {
		t.Fatal("source carrier changed after mutating every nested clone value")
	}
	if sourceTable.statement.Name[0].Name != "t" || sourceIndex.statement.Name[0].Name != "amount_idx" || !sourceTable.columns["id"].AutoIncrement {
		t.Fatal("source snapshot carrier changed after clone mutation")
	}
}

func TestLoweringUsesFreshImmutableModelPerInvocation(t *testing.T) {
	a := New()
	base, err := a.Parse([]diff.Source{{SQL: sqltext.Text("CREATE TABLE `t` (`id` bigint NOT NULL AUTO_INCREMENT, CONSTRAINT `pk_t` PRIMARY KEY (`id`));")}})
	if err != nil {
		t.Fatal(err)
	}
	target, err := a.Parse([]diff.Source{{SQL: sqltext.Text(carrierFixture)}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := a.Diff(base, target)
	if err != nil {
		t.Fatal(err)
	}
	resolutions := testResolutions(plan.Decisions)
	expected, err := plan.Resolve(resolutions...)
	if err != nil {
		t.Fatal(err)
	}
	expectedOperations := cloneTestOperations(expected.Operations)
	expectedStatements := append([]diff.PlannedStatement(nil), expected.Statements...)
	base.(*schemaSnapshot).tables["1:t"].statement.Columns[0].Name.Name = "MUTATED_BASELINE"
	target.(*schemaSnapshot).tables["1:t"].statement.Columns[1].Name.Name = "MUTATED"
	plan.Operations[0].Summary = "mutated plan operation"
	plan.Decisions[0].Target = "mutated plan decision"
	expected.Operations[0].Forward[0].SQL = "mutated result operation"
	if len(expected.Operations[0].Reverse) > 0 {
		expected.Operations[0].Reverse[0].SQL = "mutated result reverse"
	}
	expected.Statements[0].SQL = "mutated result statement"

	second, err := plan.Resolve(resolutions...)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(second.Operations, expectedOperations) || !reflect.DeepEqual(second.Statements, expectedStatements) {
		t.Fatal("repeated lowering changed operations or statements")
	}
	if len(second.Statements) == 0 || strings.Contains(second.Statements[0].SQL, "MUTATED") {
		t.Fatal("lowering read caller-owned snapshot state")
	}
}

func TestCloneCarriesErrorsToCaller(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*schemaSnapshot)
	}{
		{name: "column", mutate: func(snapshot *schemaSnapshot) {
			table := snapshot.tables["1:t"]
			table.statement.Columns = append(table.statement.Columns, mysqlquery.ColumnDefinition{
				Name: mysqlquery.Identifier{Name: "broken"}, Type: mysqlquery.DataType{Words: []string{"decimal"}, Modifiers: []mysqlquery.Expression{nil}},
			})
			snapshot.tables["1:t"] = table
		}},
		{name: "constraint", mutate: func(snapshot *schemaSnapshot) {
			table := snapshot.tables["1:t"]
			table.statement.Constraints = append(table.statement.Constraints, mysqlquery.TableConstraint{Name: identifier("broken"), Kind: mysqlquery.ConstraintCheck})
			snapshot.tables["1:t"] = table
		}},
		{name: "index", mutate: func(snapshot *schemaSnapshot) {
			snapshot.indexes[indexKey{owner: "1:t", name: "7:broken"}] = indexDefinition{statement: &mysqlquery.CreateIndexStatement{
				Name: mysqlquery.QualifiedName{{Name: "broken"}}, Table: mysqlquery.QualifiedName{{Name: "t"}}, Elements: []mysqlquery.IndexElement{{}},
			}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			a := New()
			base, err := a.Parse([]diff.Source{{SQL: sqltext.Text("CREATE TABLE `t` (`id` bigint PRIMARY KEY);")}})
			if err != nil {
				t.Fatal(err)
			}
			target, err := a.Parse([]diff.Source{{SQL: sqltext.Text("CREATE TABLE `t` (`id` bigint PRIMARY KEY);")}})
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(target.(*schemaSnapshot))
			plan, err := a.Diff(base, target)
			if err == nil {
				t.Fatal("Diff accepted malformed carrier")
			}
			if !strings.Contains(err.Error(), "clone") {
				t.Fatalf("Diff error did not identify clone boundary: %v", err)
			}
			if !reflect.DeepEqual(plan, diff.Plan{}) {
				t.Fatalf("Diff returned partial plan after clone error: %#v", plan)
			}
		})
	}
}

func TestCloneCarrierPreservesCompleteNestedMetadata(t *testing.T) {
	model, sourceTable, sourceIndex := carrierModel(t)
	copy, err := cloneLoweringModel(model)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(model, copy) {
		t.Fatal("clone did not preserve complete carrier metadata")
	}
	if !reflect.DeepEqual(sourceTable.statement, copy.entries[0].targetTable.statement) || !reflect.DeepEqual(sourceIndex.statement, copy.entries[0].targetIndex) {
		t.Fatal("clone did not preserve table and index metadata")
	}
}

func TestRenamePlanOwnsSourceIdentifiers(t *testing.T) {
	a := New()
	baseline, err := a.Parse([]diff.Source{{SQL: sqltext.Text("CREATE TABLE `Accounts` (`Old``Name` varchar(20));")}})
	if err != nil {
		t.Fatal(err)
	}
	target, err := a.Parse([]diff.Source{{SQL: sqltext.Text("CREATE TABLE `Accounts` (`New``Name` varchar(20));")}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := a.Diff(baseline, target)
	if err != nil {
		t.Fatal(err)
	}
	resolution := diff.Resolution{DecisionID: plan.Decisions[0].ID, RenameFrom: "Old`Name"}
	first, err := plan.Resolve(resolution)
	if err != nil {
		t.Fatal(err)
	}
	want := first.Statements[0]
	for key, table := range baseline.(*schemaSnapshot).tables {
		table.statement.Columns[0].Name.Name = "mutated baseline"
		baseline.(*schemaSnapshot).tables[key] = table
		break
	}
	for key, table := range target.(*schemaSnapshot).tables {
		table.statement.Columns[0].Name.Name = "mutated target"
		target.(*schemaSnapshot).tables[key] = table
		break
	}
	first.Statements[0].SQL = "mutated result"
	again, err := plan.Resolve(resolution)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again.Statements[0], want) {
		t.Fatalf("rename lowering changed after source/result mutation: %#v", again.Statements[0])
	}
}

func TestUnsupportedForeignKeyClauseRequiresLexedForeignKeyContext(t *testing.T) {
	for _, source := range []string{
		"CREATE TABLE `t` (`value` varchar(20) DEFAULT 'MATCH FULL);",
		"CREATE TABLE `t` (`ON DELETE` varchar(20));",
		"CREATE TABLE `t` (`value` varchar(20) /* ON UPDATE CASCADE */);",
		"CREATE TABLE `t` (`value` varchar(20) MATCH FULL);",
	} {
		if got := unsupportedForeignKeyClause(source); got != "" {
			t.Fatalf("malformed non-clause source %q was classified as %q", source, got)
		}
	}
}

func carrierModel(t *testing.T) (loweringModel, *tableDefinition, *indexDefinition) {
	t.Helper()
	a := New()
	snapshot, err := a.Parse([]diff.Source{{SQL: sqltext.Text(carrierFixture)}})
	if err != nil {
		t.Fatal(err)
	}
	s := snapshot.(*schemaSnapshot)
	table := s.tables["1:t"]
	index := s.indexes[indexKey{owner: "1:t", name: "10:amount_idx"}]
	table.columns["amount"] = columnFacts{Collation: &mysqlquery.QualifiedName{{Name: "utf8mb4", Quoted: true}, {Name: "bin", Quoted: true}}}
	table.columns["computed"] = columnFacts{Generated: &generatedFact{Expression: "amount + 1", Storage: "STORED"}}
	s.tables["1:t"] = table
	amount := table.statement.Columns[1]
	model := loweringModel{entries: []loweringEntry{{
		operation: diff.ProposedOperation{ID: "operation", Forward: []diff.PlannedStatement{{SQL: "forward"}}, Reverse: []diff.PlannedStatement{{SQL: "reverse"}}},
		table:     table.statement.Name, baselineColumn: &columnDefinition{AST: amount, Facts: table.columns["amount"]},
		targetColumn: &columnDefinition{AST: amount, Facts: table.columns["amount"]}, targetTable: &table,
		baselineConstraint: &table.statement.Constraints[3], targetConstraint: &table.statement.Constraints[2], targetIndex: index.statement}},
		decisions: []diff.RequiredDecision{{ID: "decision", Table: "t", Column: "amount", Target: "amount"}}}
	return model, &table, &index
}

func identifier(name string) *mysqlquery.Identifier {
	return &mysqlquery.Identifier{Name: name, Quoted: true}
}

func testResolutions(decisions []diff.RequiredDecision) []diff.Resolution {
	resolutions := make([]diff.Resolution, len(decisions))
	for i, decision := range decisions {
		resolutions[i] = diff.Resolution{DecisionID: decision.ID, RenameFrom: decision.Baseline, BackfillSQL: "UPDATE `t` SET `amount` = 1;"}
		if decision.Kind == diff.DecisionRename {
			resolutions[i].BackfillSQL = ""
		}
	}
	return resolutions
}

func cloneTestOperations(operations []diff.ProposedOperation) []diff.ProposedOperation {
	copy := append([]diff.ProposedOperation(nil), operations...)
	for i := range copy {
		copy[i].Forward = append([]diff.PlannedStatement(nil), operations[i].Forward...)
		copy[i].Reverse = append([]diff.PlannedStatement(nil), operations[i].Reverse...)
	}
	return copy
}
