package worker

import (
	"fmt"
	"sync"

	"github.com/evanw/esbuild/pkg/api"
	"modernc.org/quickjs"
)

// qjsWorker is a single QuickJS VM in the pool.
type qjsWorker struct {
	vm        *quickjs.VM
	eventLoop *eventLoop
}

// qjsPool manages a fixed-size pool of pre-warmed QuickJS workers.
type qjsPool struct {
	workers chan *qjsWorker
	size    int
	mu      sync.Mutex
}

// setupFunc configures a QuickJS VM with Web APIs, crypto, console, etc.
type setupFunc func(vm *quickjs.VM, el *eventLoop) error

// globalThisCleanupJS removes per-request state and user-set globals from
// globalThis before a worker is returned to the pool. Built-in APIs and
// Go-registered helper functions (prefixed with __) are preserved, except
// for known per-request __-prefixed globals.
const globalThisCleanupJS = `
(function() {
	// Delete known per-request globals.
	var perRequest = ['__requestID', '__ws_active_server',
		'__await_input', '__awaited_result', '__awaited_state',
		'__fn_result'];
	for (var i = 0; i < perRequest.length; i++) {
		try { delete globalThis[perRequest[i]]; } catch(e) {}
	}
	// Clear per-request promise/callback maps.
	if (globalThis.__fetchPromises) {
		globalThis.__fetchPromises = {};
	}
	if (globalThis.__timerCallbacks) {
		globalThis.__timerCallbacks = {};
	}
	// Delete dynamic per-request __tmp_ prefixed globals.
	// NOTE: Do NOT delete __kv_*, __do_*, __d1_exec* — those are permanent
	// Go-backed functions registered at pool creation time.
	var names = Object.getOwnPropertyNames(globalThis);
	for (var i = 0; i < names.length; i++) {
		var n = names[i];
		if (n.indexOf('__tmp_') === 0) {
			try { delete globalThis[n]; } catch(e) {}
		}
	}
})();
`

// newQJSPool creates a pool of QuickJS VMs, each configured with the given
// setup functions and loaded with the worker script.
func newQJSPool(size int, source string, setupFns []setupFunc, memoryLimitMB int) (*qjsPool, error) {
	pool := &qjsPool{
		workers: make(chan *qjsWorker, size),
		size:    size,
	}

	for i := 0; i < size; i++ {
		w, err := newQJSWorker(source, setupFns, memoryLimitMB)
		if err != nil {
			pool.dispose()
			return nil, fmt.Errorf("creating pool worker %d: %w", i, err)
		}
		pool.workers <- w
	}

	return pool, nil
}

// newQJSWorker creates a single QuickJS VM, runs all setup functions,
// and loads the worker script.
func newQJSWorker(source string, setupFns []setupFunc, memoryLimitMB int) (*qjsWorker, error) {
	vm, err := quickjs.NewVM()
	if err != nil {
		return nil, fmt.Errorf("creating QuickJS VM: %w", err)
	}

	if memoryLimitMB > 0 {
		vm.SetMemoryLimit(uintptr(memoryLimitMB) * 1024 * 1024)
	}

	el := newEventLoop()

	// Run all setup functions (Web APIs, crypto, console, fetch, timers, etc.).
	for _, setup := range setupFns {
		if err := setup(vm, el); err != nil {
			vm.Close()
			return nil, fmt.Errorf("setup: %w", err)
		}
	}

	// Compile and run the worker script.
	wrapped := wrapESModule(source)
	v, err := vm.EvalValue(wrapped, quickjs.EvalGlobal)
	if err != nil {
		vm.Close()
		return nil, fmt.Errorf("running worker script: %w", err)
	}
	v.Free()

	// Verify __worker_module__ was set.
	ok, err := evalBool(vm, "typeof globalThis.__worker_module__ !== 'undefined'")
	if err != nil || !ok {
		vm.Close()
		return nil, fmt.Errorf("worker script did not export a default module")
	}

	return &qjsWorker{vm: vm, eventLoop: el}, nil
}

// get acquires a worker from the pool. Blocks until one is available.
func (p *qjsPool) get() (*qjsWorker, error) {
	w, ok := <-p.workers
	if !ok {
		return nil, fmt.Errorf("worker pool is closed")
	}
	return w, nil
}

// put returns a worker to the pool after resetting its event loop.
func (p *qjsPool) put(w *qjsWorker) {
	// Clean per-request globals before reuse.
	_ = evalDiscard(w.vm, globalThisCleanupJS)
	w.eventLoop.reset()
	select {
	case p.workers <- w:
	default:
		// Pool full (shouldn't happen), dispose the worker.
		w.vm.Close()
	}
}

// dispose closes all workers in the pool.
func (p *qjsPool) dispose() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for {
		select {
		case w := <-p.workers:
			w.vm.Close()
		default:
			return
		}
	}
}

// wrapESModule transforms an ES module source into a script that assigns
// exports to globalThis.__worker_module__. It uses esbuild's Transform API
// to properly parse the JS AST and wrap the module as an IIFE assigned to
// globalThis.__worker_module__.
//
// If the source has no exports (already a plain script), the IIFE wrapping
// is harmless — the global name is set to the IIFE's return value.
// If esbuild reports errors, the source is returned unchanged so that
// callers handle V8 compile errors downstream.
func wrapESModule(source string) string {
	result := api.Transform(source, api.TransformOptions{
		Format:     api.FormatIIFE,
		GlobalName: "globalThis.__worker_module__",
		Target:     api.ESNext,
	})
	if len(result.Errors) > 0 {
		return source
	}
	code := string(result.Code)
	// esbuild places the default export under a .default property when
	// converting ESM to IIFE. Unwrap it so callers can access handlers
	// (fetch, scheduled, etc.) directly on globalThis.__worker_module__.
	code += "if(globalThis.__worker_module__&&globalThis.__worker_module__.default)globalThis.__worker_module__=globalThis.__worker_module__.default;\n"
	return code
}
