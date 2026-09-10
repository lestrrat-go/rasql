package nullable

import "mappingfixture/domain"

type Status struct {
	Value domain.Status
	Valid bool
}

type Time struct {
	Value domain.Time
	Valid bool
}
