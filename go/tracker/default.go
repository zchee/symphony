package tracker

import (
	"github.com/openai/symphony/go/config"
	"github.com/openai/symphony/go/linear"
	"github.com/openai/symphony/go/tracker/memory"
)

// Default returns the tracker adapter selected by the current runtime configuration.
func Default() Client {
	if config.Current().TrackerKind == "memory" {
		return &memory.Client{}
	}

	return linear.NewClient()
}
