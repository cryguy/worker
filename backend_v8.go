//go:build v8

package worker

import (
	"github.com/cryguy/worker/v2/internal/core"
	"github.com/cryguy/worker/v2/internal/v8engine"
)

func newBackend(cfg core.EngineConfig, loader core.SourceLoader) core.EngineBackend {
	return v8engine.NewEngine(cfg, loader)
}
