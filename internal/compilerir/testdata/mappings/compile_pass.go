package mappings

// AccountID and Status stand in for application domain types in compiler fixtures.
type AccountID string
type Status uint8

type NullableStatus struct {
	Value Status
	Valid bool
}

type AccountRow struct {
	ID     AccountID
	Status NullableStatus
}

type AccountCreate struct {
	ID     AccountID
	Status NullableStatus
}

type AccountPatch struct {
	Status NullableStatus
}

type FindResult struct {
	ID     AccountID
	Status NullableStatus
}

var _ = AccountRow{}
var _ = AccountCreate{}
var _ = AccountPatch{}
var _ = FindResult{}
