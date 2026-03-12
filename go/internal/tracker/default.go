package tracker

import (
	"github.com/openai/symphony/go/internal/config"
	"github.com/openai/symphony/go/internal/linear"
	"github.com/openai/symphony/go/internal/tracker/memory"
)

// Default returns the tracker adapter selected by the current runtime configuration.
func Default() Client {
	if config.Current().TrackerKind == "memory" {
		return &memory.Client{}
	}

	return linear.NewClient()
}
