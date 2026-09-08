package generate

// HistoricalStoreForTest renders the retired store shape for source-size
// comparisons. It is compiled only into generate's test binary.
func HistoricalStoreForTest(in EmitterInput) (Store, error) {
	return legacyStore(in)
}
