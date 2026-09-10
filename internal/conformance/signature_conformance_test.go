package conformance

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSignatureValidationRejectsBrokenReferences(t *testing.T) {
	document, err := LoadPortableSignature()
	require.NoError(t, err)
	document.Schema.Tables[2].PrimaryKey = []string{"missing"}
	require.Error(t, document.Validate())
}

func TestSignatureSelectionFailsClosed(t *testing.T) {
	_, err := PortableSignatureForChecked("does-not-exist")
	require.Error(t, err)
}

func TestSignatureValidationRejectsInvertedBounds(t *testing.T) {
	document, err := LoadPortableSignature()
	require.NoError(t, err)
	document.Workloads[0].MaxStatements = document.Workloads[0].MinStatements - 1
	require.Error(t, document.Validate())
}
