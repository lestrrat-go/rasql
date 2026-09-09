package rasql

import "github.com/lestrrat-go/rasql/dberror"

type FailureCertainty uint8

const (
	OutcomeUnknown FailureCertainty = iota
	OutcomeRejected
)

type BatchFailureClassifier interface {
	Certainty(error) FailureCertainty
}

type ConstraintFailureClassifier struct {
	Classifiers []dberror.Classifier
}

func (c ConstraintFailureClassifier) Certainty(err error) FailureCertainty {
	metadata, ok := dberror.Classify(err, c.Classifiers...)
	if !ok {
		return OutcomeUnknown
	}
	switch metadata.Category {
	case dberror.UniqueViolation, dberror.ForeignKeyViolation, dberror.NotNullViolation, dberror.CheckViolation:
		return OutcomeRejected
	default:
		return OutcomeUnknown
	}
}
