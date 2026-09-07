package rasqlgen

import (
	"fmt"

	"github.com/lestrrat-go/rasql/generate"
)

func renderEmitter(input generate.EmitterInput) (generate.Store, error) {
	switch input.Generation.Emitter {
	case "compact":
		return generate.RenderCompact(input)
	case "legacy":
		return generate.LegacyStore(input)
	default:
		return generate.Store{}, fmt.Errorf("generate: unsupported emitter %q", input.Generation.Emitter)
	}
}
