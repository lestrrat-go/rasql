package rasqlgen

import (
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestConfigNamesUsesExactObjectIdentity(t *testing.T) {
	settings := config{Tables: configTables{Names: map[string]generate.ObjectNames{
		"billing.events": {Accessor: "BillingEvents", TableType: "BillingEventsTable", RowType: "BillingEventsRow", FileBase: "billing_events"},
		"audit.events":   {Accessor: "AuditEvents", TableType: "AuditEventsTable", RowType: "AuditEventsRow", FileBase: "audit_events"},
	}}}
	names, err := settings.names()
	require.NoError(t, err)
	require.Equal(t, generate.ObjectNames{Accessor: "BillingEvents", TableType: "BillingEventsTable", RowType: "BillingEventsRow", FileBase: "billing_events"}, names[schema.ObjectName{Schema: "billing", Name: "events"}])
	require.Equal(t, "AuditEvents", names[schema.ObjectName{Schema: "audit", Name: "events"}].Accessor)
}
