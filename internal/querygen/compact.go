package querygen

// CompactGoSource emits the compact native query surface. TypedGoSource is
// already lock-backed and uses NativeProjection plus direct positional scan;
// this named entry point keeps emitter dispatch explicit for G5 callers.
func CompactGoSource(input TypedInput) ([]byte, error) {
	return TypedGoSource(input)
}
