package domain

type AccountID string
type Status uint8

type NullableStatus struct {
	Value Status
	Valid bool
}
