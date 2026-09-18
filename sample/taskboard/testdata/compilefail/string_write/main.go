package compilefail

import "example.com/taskboard/internal/store"

// This package is compiled by compilefail_test.go and must remain invalid.
var _ = store.Tasks().Create().Column("title", "wrong API")
