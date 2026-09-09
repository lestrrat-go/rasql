package conformance

import (
	"fmt"

	"github.com/lestrrat-go/rasql"
)

func applyRASQLMutationStatements(profileID, workload string, evidence *parityEvidence) error {
	if evidence.Mutation == nil {
		return nil
	}
	if workload == "bulk_write" && len(evidence.Mutation.Statements) == 0 {
		for index, state := range evidence.Mutation.Inputs {
			if state != rasql.InputApplied {
				return fmt.Errorf("bulk_write input %d has state %d", index, state)
			}
		}
		if profileID == "sqlite-3.35" {
			evidence.Mutation.Statements = []mutationStatementEvidence{
				{AffectedValid: true, Affected: 499},
				{AffectedValid: true, Affected: 1},
			}
		} else {
			evidence.Mutation.Statements = []mutationStatementEvidence{{AffectedValid: true, Affected: 500}}
		}
	}
	indexes := mutationObservationIndexes(evidence.Observations)
	if len(indexes) != len(evidence.Mutation.Statements) {
		return fmt.Errorf(
			"%s has %d mutation observations and %d returned statement outcomes",
			workload,
			len(indexes),
			len(evidence.Mutation.Statements),
		)
	}
	for outcomeIndex, observationIndex := range indexes {
		outcome := evidence.Mutation.Statements[outcomeIndex]
		evidence.Observations[observationIndex].RowsAffectedValid = outcome.AffectedValid
		evidence.Observations[observationIndex].RowsAffected = outcome.Affected
	}
	return nil
}

func setSQLMutationStatements(workload string, evidence *parityEvidence) {
	indexes := mutationObservationIndexes(evidence.Observations)
	if len(indexes) == 0 {
		return
	}
	if evidence.Mutation == nil {
		evidence.Mutation = &mutationEvidence{}
		if workload == "create_patch" {
			evidence.Mutation.Durability = rasql.DurabilityCommitted
		}
	}
	evidence.Mutation.Statements = make([]mutationStatementEvidence, len(indexes))
	for index, observationIndex := range indexes {
		observation := evidence.Observations[observationIndex]
		evidence.Mutation.Statements[index] = mutationStatementEvidence{
			AffectedValid: observation.RowsAffectedValid,
			Affected:      observation.RowsAffected,
		}
	}
}

func mutationObservationIndexes(observations []statementObservation) []int {
	result := make([]int, 0, len(observations))
	for index, observation := range observations {
		if observation.Verification {
			continue
		}
		if observation.Role == roleMutation || observation.Role == roleSentinel {
			result = append(result, index)
		}
	}
	return result
}
