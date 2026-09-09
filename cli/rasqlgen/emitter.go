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
		return generate.Store{}, fmt.Errorf(
			"generate: emitter %q was removed; set generation.emitter to %q",
			input.Generation.Emitter, "compact")
	default:
		return generate.Store{}, fmt.Errorf("generate: unsupported emitter %q", input.Generation.Emitter)
	}
}
