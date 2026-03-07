//go:build v8

package v8engine

import (
	"fmt"

	"github.com/cryguy/worker/v2/internal/core"
	"github.com/cryguy/worker/v2/internal/eventloop"
	v8 "github.com/tommie/v8go"
)

// NewStandaloneRuntime creates a V8 isolate+context with all Web APIs configured,
// suitable for use as an interactive REPL. No worker script is loaded.
func NewStandaloneRuntime(cfg core.EngineConfig) (core.JSRuntime, *eventloop.EventLoop, func(), error) {
	var iso *v8.Isolate
	if cfg.MemoryLimitMB > 0 {
		heapSize := uint64(cfg.MemoryLimitMB) * 1024 * 1024
		iso = v8.NewIsolate(v8.WithResourceConstraints(heapSize/2, heapSize))
	} else {
		iso = v8.NewIsolate()
	}

	ctx := v8.NewContext(iso)
	rt := &v8Runtime{iso: iso, ctx: ctx}
	el := eventloop.New()

	for _, setup := range buildSetupFuncs(cfg) {
		if err := setup(rt, el); err != nil {
			ctx.Close()
			iso.Dispose()
			return nil, nil, nil, fmt.Errorf("setup: %w", err)
		}
	}

	cleanup := func() {
		ctx.Close()
		iso.Dispose()
	}

	return rt, el, cleanup, nil
}
