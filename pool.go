package worker

import (
	"fmt"
	"sync"

	"github.com/evanw/esbuild/pkg/api"
	v8 "github.com/tommie/v8go"
)

// v8Worker is a single V8 isolate+context pair in the pool.
type v8Worker struct {
	iso       *v8.Isolate
	ctx       *v8.Context
	eventLoop *eventLoop
}

// v8Pool manages a fixed-size pool of pre-warmed V8 workers.
type v8Pool struct {
	workers chan *v8Worker
	size    int
	mu      sync.Mutex
}

// setupFunc configures a V8 context with Web APIs, crypto, console, etc.
type setupFunc func(iso *v8.Isolate, ctx *v8.Context, el *eventLoop) error

// globalThisCleanupJS removes per-request state and user-set globals from
// globalThis before a worker is returned to the pool. Built-in APIs and
// Go-registered helper functions (prefixed with __) are preserved, except
// for known per-request __-prefixed globals.
const globalThisCleanupJS = `
(function() {
	// Delete known per-request globals.
	var perRequest = ['__requestID', '__ws_active_server'];
	for (var i = 0; i < perRequest.length; i++) {
		try { delete globalThis[perRequest[i]]; } catch(e) {}
	}
	// Delete all __d1_exec_, __tmp_, __kv_ prefixed globals.
	var names = Object.getOwnPropertyNames(globalThis);
	for (var i = 0; i < names.length; i++) {
		var n = names[i];
		if (n.indexOf('__d1_exec_') === 0 ||
			n.indexOf('__tmp_') === 0 ||
			n.indexOf('__kv_') === 0 ||
			n.indexOf('__do_') === 0) {
			try { delete globalThis[n]; } catch(e) {}
		}
	}
})();
`

// newV8Pool creates a pool of V8 isolates, each configured with the given
// setup functions and loaded with the worker script.
func newV8Pool(size int, source string, setupFns []setupFunc, memoryLimitMB int) (*v8Pool, error) {
	pool := &v8Pool{
		workers: make(chan *v8Worker, size),
		size:    size,
	}

	for i := 0; i < size; i++ {
		w, err := newV8Worker(source, setupFns, memoryLimitMB)
		if err != nil {
			pool.dispose()
			return nil, fmt.Errorf("creating pool worker %d: %w", i, err)
		}
		pool.workers <- w
	}

	return pool, nil
}

// newV8Worker creates a single V8 isolate+context, runs all setup functions,
// and loads the worker script.
func newV8Worker(source string, setupFns []setupFunc, memoryLimitMB int) (*v8Worker, error) {
	var iso *v8.Isolate
	if memoryLimitMB > 0 {
		heapSize := uint64(memoryLimitMB) * 1024 * 1024
		iso = v8.NewIsolate(v8.WithResourceConstraints(heapSize/2, heapSize))
	} else {
		iso = v8.NewIsolate()
	}
	ctx := v8.NewContext(iso)
	el := newEventLoop()

	// Run all setup functions (Web APIs, crypto, console, fetch, etc.).
	for _, setup := range setupFns {
		if err := setup(iso, ctx, el); err != nil {
			ctx.Close()
			iso.Dispose()
			return nil, fmt.Errorf("setup: %w", err)
		}
	}

	// Compile and run the worker script.
	wrapped := wrapESModule(source)
	script, err := iso.CompileUnboundScript(wrapped, "worker.js", v8.CompileOptions{})
	if err != nil {
		ctx.Close()
		iso.Dispose()
		return nil, fmt.Errorf("compiling worker script: %w", err)
	}

	if _, err := script.Run(ctx); err != nil {
		ctx.Close()
		iso.Dispose()
		return nil, fmt.Errorf("running worker script: %w", err)
	}

	// Verify __worker_module__ was set.
	check, err := ctx.RunScript("typeof globalThis.__worker_module__ !== 'undefined'", "check.js")
	if err != nil || !check.Boolean() {
		ctx.Close()
		iso.Dispose()
		return nil, fmt.Errorf("worker script did not export a default module")
	}

	return &v8Worker{iso: iso, ctx: ctx, eventLoop: el}, nil
}

// get acquires a worker from the pool. Blocks until one is available.
func (p *v8Pool) get() (*v8Worker, error) {
	w, ok := <-p.workers
	if !ok {
		return nil, fmt.Errorf("worker pool is closed")
	}
	return w, nil
}

// put returns a worker to the pool after resetting its event loop.
func (p *v8Pool) put(w *v8Worker) {
	// Clean per-request globals before reuse.
	_, _ = w.ctx.RunScript(globalThisCleanupJS, "cleanup.js")
	w.eventLoop.reset()
	select {
	case p.workers <- w:
	default:
		// Pool full (shouldn't happen), dispose the worker.
		w.ctx.Close()
		w.iso.Dispose()
	}
}

// dispose closes all workers in the pool.
func (p *v8Pool) dispose() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for {
		select {
		case w := <-p.workers:
			w.ctx.Close()
			w.iso.Dispose()
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
