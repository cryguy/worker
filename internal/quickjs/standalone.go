//go:build !v8

package quickjs

import (
	"fmt"

	"github.com/cryguy/worker/v2/internal/core"
	"github.com/cryguy/worker/v2/internal/eventloop"
	"modernc.org/quickjs"
)

// NewStandaloneRuntime creates a QuickJS VM with all Web APIs configured,
// suitable for use as an interactive REPL. No worker script is loaded.
func NewStandaloneRuntime(cfg core.EngineConfig) (core.JSRuntime, *eventloop.EventLoop, func(), error) {
	vm, err := quickjs.NewVM()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("creating QuickJS VM: %w", err)
	}

	if cfg.MemoryLimitMB > 0 {
		vm.SetMemoryLimit(uintptr(cfg.MemoryLimitMB) * 1024 * 1024)
	}

	rt := &qjsRuntime{vm: vm}

	if err := rt.initBinaryTransfer(); err != nil {
		vm.Close()
		return nil, nil, nil, fmt.Errorf("init binary transfer: %w", err)
	}

	el := eventloop.New()

	for _, setup := range buildSetupFuncs(cfg) {
		if err := setup(rt, el); err != nil {
			vm.Close()
			return nil, nil, nil, fmt.Errorf("setup: %w", err)
		}
	}

	return rt, el, func() { vm.Close() }, nil
}
