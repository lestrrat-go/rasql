package rasql

import "errors"

type Durability uint8

const (
	DurabilityUnknown Durability = iota
	DurabilityPending
	DurabilityCommitted
)

type MutationOutcome struct {
	Affected   int64
	Durability Durability
}

type InputOutcome uint8

const (
	InputUnattempted InputOutcome = iota
	InputApplied
	InputRolledBack
	InputRejected
	InputUnknown
)

type MutationBatchOutcome struct {
	Inputs      []InputOutcome
	Durability  Durability
	FailedBatch []int
}

type MutationBatchOptions struct {
	MaxRows           int
	MaxBindParameters int
	Atomic            bool
	Classifier        BatchFailureClassifier
}

var ErrPrecondition = errors.New("rasql: mutation precondition failed")
