//go:build !v8

package worker

import (
	"github.com/cryguy/worker/internal/core"
	"github.com/cryguy/worker/internal/quickjs"
)

func newBackend(cfg core.EngineConfig, loader core.SourceLoader) core.EngineBackend {
	return quickjs.NewEngine(cfg, loader)
}
