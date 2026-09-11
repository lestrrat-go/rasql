// Package dberror provides optional, portable database error categories.
package dberror

// Category identifies a database error's portable meaning.
type Category uint8

const (
	Unknown Category = iota
	UniqueViolation
	ForeignKeyViolation
	NotNullViolation
	CheckViolation
	TransactionConflict
)

// String returns the stable name of c.
func (c Category) String() string {
	switch c {
	case UniqueViolation:
		return "unique_violation"
	case ForeignKeyViolation:
		return "foreign_key_violation"
	case NotNullViolation:
		return "not_null_violation"
	case CheckViolation:
		return "check_violation"
	case TransactionConflict:
		return "transaction_conflict"
	default:
		return "unknown"
	}
}

// Metadata contains a portable category and native details supplied by a driver.
type Metadata struct {
	Category   Category
	SQLState   string
	NativeCode string
	Constraint string
	Table      string
	Column     string
}

// Classifier recognizes errors from one database driver.
type Classifier interface {
	Classify(error) (Metadata, bool)
}

// Classify asks classifiers in order and returns the first recognized category.
// It preserves err unchanged; callers can still inspect its original chain.
//
// A nil classifier in classifiers is skipped and the next one is asked. A non-nil
// interface value holding a nil pointer is not skipped, and Classify calls its
// Classify method.
func Classify(err error, classifiers ...Classifier) (Metadata, bool) {
	if err == nil {
		return Metadata{}, false
	}
	for _, classifier := range classifiers {
		if classifier == nil {
			continue
		}
		metadata, ok := classifier.Classify(err)
		if ok && metadata.Category != Unknown {
			return metadata, true
		}
	}
	return Metadata{}, false
}
