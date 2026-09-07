// Package dberror provides optional, portable database error categories.
package dberror

import "reflect"

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
func Classify(err error, classifiers ...Classifier) (Metadata, bool) {
	if err == nil {
		return Metadata{}, false
	}
	for _, classifier := range classifiers {
		if nilClassifier(classifier) {
			continue
		}
		metadata, ok := classifier.Classify(err)
		if ok && metadata.Category != Unknown {
			return metadata, true
		}
	}
	return Metadata{}, false
}

func nilClassifier(classifier Classifier) bool {
	if classifier == nil {
		return true
	}
	value := reflect.ValueOf(classifier)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
