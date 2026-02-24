//go:build v8

package v8engine

import (
	"fmt"
	"sync"

	"github.com/cryguy/worker/internal/core"
	"github.com/cryguy/worker/internal/eventloop"
	"github.com/cryguy/worker/internal/webapi"
	v8 "github.com/tommie/v8go"
)

// v8Worker is a single V8 isolate+context pair in the pool.
type v8Worker struct {
	iso       *v8.Isolate
	ctx       *v8.Context
	rt        *v8Runtime
	eventLoop *eventloop.EventLoop
}

// v8Pool manages a fixed-size pool of pre-warmed V8 workers.
type v8Pool struct {
	workers chan *v8Worker
	size    int
	mu      sync.Mutex
}

// setupFunc configures a V8 context with Web APIs, crypto, console, etc.
type setupFunc func(rt core.JSRuntime, el *eventloop.EventLoop) error

// globalThisCleanupJS removes per-request state and user-set globals from
// globalThis before a worker is returned to the pool.
const globalThisCleanupJS = `
(function() {
	var perRequest = ['__requestID', '__ws_active_server',
		'__await_input', '__awaited_result', '__awaited_state',
		'__fn_result', '__req', '__env', '__ctx', '__result',
		'__call_result', '__sched_event', '__tail_events'];
	for (var i = 0; i < perRequest.length; i++) {
		try { delete globalThis[perRequest[i]]; } catch(e) {}
	}
	if (globalThis.__fetchPromises) {
		globalThis.__fetchPromises = {};
	}
	if (globalThis.__timerCallbacks) {
		globalThis.__timerCallbacks = {};
	}
	var names = Object.getOwnPropertyNames(globalThis);
	for (var i = 0; i < names.length; i++) {
		var n = names[i];
		if (n.indexOf('__tmp_') === 0 || n.indexOf('__fn_arg_') === 0) {
			try { delete globalThis[n]; } catch(e) {}
		}
	}
})();
`

// buildSetupFuncs returns the list of Web API setup functions for pool workers.
func buildSetupFuncs(cfg core.EngineConfig) []setupFunc {
	return []setupFunc{
		webapi.SetupWebAPIs,
		webapi.SetupURLSearchParamsExt,
		webapi.SetupGlobals,
		webapi.SetupEncoding,
		webapi.SetupTimers,
		webapi.SetupAbort,
		webapi.SetupReportError,
		webapi.SetupCrypto,
		webapi.SetupCryptoExt,
		webapi.SetupCryptoDerive,
		webapi.SetupCryptoRSA,
		webapi.SetupCryptoEd25519,
		webapi.SetupCryptoAesCtrKw,
		webapi.SetupCryptoECDH,
		webapi.SetupURLPattern,
		webapi.SetupStreams,
		webapi.SetupTextStreams,
		webapi.SetupFormData,
		webapi.SetupBlobExt,
		webapi.SetupCompression,
		webapi.SetupBodyTypes,
		webapi.SetupWebSocket,
		webapi.SetupHTMLRewriter,
		webapi.SetupConsole,
		webapi.SetupConsoleExt,
		func(rt core.JSRuntime, el *eventloop.EventLoop) error {
			return webapi.SetupFetch(rt, cfg, el)
		},
		webapi.SetupBYOBReader,
		webapi.SetupMessageChannel,
		webapi.SetupUnhandledRejection,
		webapi.SetupScheduler,
		webapi.SetupDigestStream,
		webapi.SetupEventSource,
		webapi.SetupTCPSocket,
		webapi.SetupKV,
		webapi.SetupStorage,
		webapi.SetupQueues,
		webapi.SetupD1,
		webapi.SetupDurableObjects,
		webapi.SetupServiceBindings,
		webapi.SetupAssets,
		webapi.SetupCache,
	}
}

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
	rt := &v8Runtime{iso: iso, ctx: ctx}
	el := eventloop.New()

	for _, setup := range setupFns {
		if err := setup(rt, el); err != nil {
			ctx.Close()
			iso.Dispose()
			return nil, fmt.Errorf("setup: %w", err)
		}
	}

	// Compile and run the worker script.
	wrapped := webapi.WrapESModule(source)
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

	return &v8Worker{iso: iso, ctx: ctx, rt: rt, eventLoop: el}, nil
}

// get acquires a worker from the pool.
func (p *v8Pool) get() (*v8Worker, error) {
	w, ok := <-p.workers
	if !ok {
		return nil, fmt.Errorf("worker pool is closed")
	}
	return w, nil
}

// put returns a worker to the pool after resetting its event loop.
func (p *v8Pool) put(w *v8Worker) {
	_, _ = w.ctx.RunScript(globalThisCleanupJS, "cleanup.js")
	w.eventLoop.Reset()
	select {
	case p.workers <- w:
	default:
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
