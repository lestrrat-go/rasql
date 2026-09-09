package conformance

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

func validatePortableEvidence(signature SignatureWorkload, expected PortableExpected, evidence parityEvidence) error {
	if err := evidence.validate(); err != nil {
		return fmt.Errorf("portable evidence %s: %w", expected.Workload, err)
	}
	sum := sha256.Sum256(evidence.ResultJSON)
	resultDigest := hex.EncodeToString(sum[:])
	if evidence.Outcome != expected.Outcome || resultDigest != expected.ResultSHA256 {
		return fmt.Errorf("portable evidence %s: result contract differs", expected.Workload)
	}
	if evidence.RowsReturned != expected.RowsReturned ||
		evidence.MeasuredRowsConsumed != expected.MeasuredRowsConsumed ||
		evidence.VerificationRowsConsumed != expected.VerificationRowsConsumed ||
		evidence.RowsConsumed != expected.RowsConsumed {
		return fmt.Errorf("portable evidence %s: row counters differ", expected.Workload)
	}
	statements := portableStatementCount(evidence)
	if statements < signature.MinStatements || statements > signature.MaxStatements {
		return fmt.Errorf("portable evidence %s: statement count %d is outside %d..%d", expected.Workload, statements, signature.MinStatements, signature.MaxStatements)
	}
	return nil
}

func portableStatementCount(evidence parityEvidence) int {
	if len(evidence.Observations) == 0 {
		return evidence.StatementCount()
	}
	count := 0
	for _, observation := range evidence.Observations {
		if !observation.Verification {
			count++
		}
	}
	return count
}

func addMeasurementPair(recorder *Recorder, measurements ...Measurement) error {
	if recorder == nil {
		return fmt.Errorf("conformance recorder is nil")
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	for index := range measurements {
		measurements[index].FailureReason = SanitizeFailure(measurements[index].FailureReason, recorder.dsns...)
		if err := measurements[index].Validate(); err != nil {
			return err
		}
	}
	recorder.records = append(recorder.records, measurements...)
	return nil
}
