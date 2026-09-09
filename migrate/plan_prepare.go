package migrate

import (
	"fmt"

	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/lestrrat-go/rasql/schema"
)

type resolvedChangePlanMode uint8

const (
	planModeRequired resolvedChangePlanMode = iota + 1
	planModeForbidden
)

type preparedPlanOperation struct {
	index        int
	operation    changeplan.Operation
	mode         resolvedChangePlanMode
	beforeDigest changeplan.Digest
	afterDigest  changeplan.Digest
}

type preparedPlanProgressName struct {
	bareName          string
	quotedBareName    string
	quotedPlanID      string
	quotedNextIndex   string
	quotedCount       string
	quotedCatalogHash string
}

type preparedChangePlan struct {
	id           changeplan.PlanID
	profile      engineprofile.Profile
	baseline     changeplan.BaselineIdentity
	history      changeplan.HistoryIdentity
	operations   []preparedPlanOperation
	progressName preparedPlanProgressName
	hasForbidden bool
}

func prepareChangePlan(r Runner, plan changeplan.Plan) (preparedChangePlan, error) {
	if err := r.validate(); err != nil {
		return preparedChangePlan{}, err
	}
	if _, err := changeplan.Encode(plan); err != nil {
		return preparedChangePlan{}, fmt.Errorf("migrate: encode change plan: %w", err)
	}
	operations, err := plan.TopologicalOperations()
	if err != nil {
		return preparedChangePlan{}, fmt.Errorf("migrate: resolve change plan order: %w", err)
	}
	planProfile := plan.Profile()
	profile, err := engineprofile.New(planProfile.ID(), planProfile.Engine(), planProfile.CustomName(), planProfile.Version(), planProfile.Capabilities(), planProfile.Limits())
	if err != nil {
		return preparedChangePlan{}, fmt.Errorf("migrate: rebuild change plan profile: %w", err)
	}
	if expected, ok := engineForDialect(r.dialect.Name()); !ok || profile.Engine != expected {
		return preparedChangePlan{}, fmt.Errorf("migrate: change plan engine %d does not match dialect %q", profile.Engine, r.dialect.Name())
	}
	baseline := plan.Baseline()
	history := plan.History()
	if history.Table() != r.historyTable {
		return preparedChangePlan{}, fmt.Errorf("migrate: change plan history table %q does not match runner history table %q", history.Table(), r.historyTable)
	}
	progressName, err := prepareProgressName(r.dialect, history.Table())
	if err != nil {
		return preparedChangePlan{}, err
	}
	prepared := preparedChangePlan{id: plan.ID(), profile: profile, baseline: baseline, history: history, progressName: progressName}
	prepared.operations = make([]preparedPlanOperation, len(operations))
	for index, operation := range operations {
		mode, modeErr := resolvePlanMode(operation.Transaction(), profile.Capabilities.TransactionalDDL)
		if modeErr != nil {
			return preparedChangePlan{}, fmt.Errorf("migrate: operation %q: %w", operation.ID(), modeErr)
		}
		if mode == planModeRequired && !profile.Capabilities.TransactionalDDL {
			return preparedChangePlan{}, fmt.Errorf("migrate: operation %q requires transactional DDL", operation.ID())
		}
		beforeDigest := baseline.Catalog().CatalogDigest()
		if index > 0 {
			beforeDigest = operations[index-1].ResultDigest()
		}
		afterDigest := operation.ResultDigest()
		if afterDigest == (changeplan.Digest{}) {
			return preparedChangePlan{}, fmt.Errorf("migrate: operation %q has zero result digest", operation.ID())
		}
		prepared.operations[index] = preparedPlanOperation{index: index, operation: operation, mode: mode, beforeDigest: beforeDigest, afterDigest: afterDigest}
		prepared.hasForbidden = prepared.hasForbidden || mode == planModeForbidden
	}
	return prepared, nil
}

func (p preparedChangePlan) expectedPrefixDigest(index int) (changeplan.Digest, error) {
	if index < 0 || index > len(p.operations) {
		return changeplan.Digest{}, fmt.Errorf("migrate: prefix index %d is outside operation range", index)
	}
	if index == 0 {
		return p.baseline.Catalog().CatalogDigest(), nil
	}
	return p.operations[index-1].afterDigest, nil
}

func engineForDialect(name string) (engineprofile.EngineID, bool) {
	switch name {
	case "postgresql":
		return engineprofile.PostgreSQL, true
	case "mysql":
		return engineprofile.MySQL, true
	case "sqlite":
		return engineprofile.SQLite, true
	default:
		return 0, false
	}
}

func prepareProgressName(dialectValue interface {
	Name() string
	QuoteIdentifier(string) (string, error)
}, historyTable string) (preparedPlanProgressName, error) {
	bareName := historyTable + "_plan_progress"
	if err := schema.ValidateSimpleIdentifier(bareName); err != nil {
		return preparedPlanProgressName{}, fmt.Errorf("migrate: plan progress table: %w", err)
	}
	maxLength := 0
	switch dialectValue.Name() {
	case "postgresql":
		maxLength = 63
	case "mysql":
		maxLength = 64
	}
	if maxLength > 0 && len(bareName) > maxLength {
		return preparedPlanProgressName{}, fmt.Errorf("migrate: plan progress table %q exceeds %d-byte identifier limit", bareName, maxLength)
	}
	quoted := make([]string, 5)
	for index, name := range []string{bareName, "plan_id", "next_index", "operation_count", "catalog_digest"} {
		value, err := dialectValue.QuoteIdentifier(name)
		if err != nil {
			return preparedPlanProgressName{}, fmt.Errorf("migrate: quote plan progress identifier %q: %w", name, err)
		}
		quoted[index] = value
	}
	return preparedPlanProgressName{bareName: bareName, quotedBareName: quoted[0], quotedPlanID: quoted[1], quotedNextIndex: quoted[2], quotedCount: quoted[3], quotedCatalogHash: quoted[4]}, nil
}

func resolvePlanMode(transaction changeplan.TransactionMode, transactionalDDL bool) (resolvedChangePlanMode, error) {
	switch transaction {
	case changeplan.TransactionRequired:
		return planModeRequired, nil
	case changeplan.TransactionForbidden:
		return planModeForbidden, nil
	case changeplan.TransactionEngineDefault:
		if transactionalDDL {
			return planModeRequired, nil
		}
		return planModeForbidden, nil
	default:
		return 0, fmt.Errorf("unknown transaction mode %q", transaction)
	}
}
