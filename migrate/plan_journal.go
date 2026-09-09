package migrate

import (
	"fmt"

	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/lestrrat-go/rasql/sqltext"
)

type planJournalIdentity struct {
	id       string
	checksum string
	source   string
	index    int
}

func journalIdentity(prepared preparedChangePlan, operation preparedPlanOperation, statementIndex int) planJournalIdentity {
	return planJournalIdentity{
		id: "d2:" + framedPlanHash("rasql/changeplan/journal-id/v1", []byte(prepared.id.String()),
			[]byte(operation.operation.ID())),
		checksum: prepared.id.String(),
		source:   fmt.Sprintf("d2 operation %s statement %d", operation.operation.ID(), statementIndex),
		index:    statementIndex,
	}
}

func journalMigration(prepared preparedChangePlan, operation preparedPlanOperation) preparedMigration {
	statements := operation.operation.Statements()
	legacy := make([]Statement, len(statements))
	for index, statement := range statements {
		legacy[index] = Statement{Source: journalIdentity(prepared, operation, index).source, SQL: sqltext.Text(statement.SQL())}
	}
	return preparedMigration{
		id:         journalIdentity(prepared, operation, 0).id,
		mode:       ExecutionModeNonTransactional,
		statements: legacy,
		checksum:   prepared.id.String(),
	}
}

func matchingPlanJournal(prepared preparedChangePlan, entry *progressEntry) (int, preparedMigration, error) {
	if entry == nil {
		return 0, preparedMigration{}, fmt.Errorf("migrate: no plan journal exists")
	}
	for index, operation := range prepared.operations {
		migration := journalMigration(prepared, operation)
		if migration.id != entry.id {
			continue
		}
		if entry.checksum != prepared.id.String() || entry.direction != DirectionUp || entry.sourceIndex < 0 ||
			entry.sourceIndex >= len(migration.statements) || entry.nextIndex < entry.sourceIndex ||
			entry.nextIndex > entry.sourceIndex+1 || entry.source != migration.statements[entry.sourceIndex].Source {
			return 0, preparedMigration{}, fmt.Errorf("migrate: plan journal identity is invalid")
		}
		return index, migration, nil
	}
	return 0, preparedMigration{}, fmt.Errorf("migrate: progress does not belong to this change plan")
}

func operationFactsMatch(catalog changeplan.Catalog, facts []changeplan.Fact, digest, want changeplan.Digest) error {
	if err := changeplan.EvaluateFacts(catalog, facts); err != nil {
		return err
	}
	if digest != want {
		return fmt.Errorf("migrate: catalog digest does not match change plan")
	}
	return nil
}
