//go:build !v8

package quickjs

import (
	"fmt"
	"sync"

	"github.com/cryguy/worker/internal/core"
	"github.com/cryguy/worker/internal/eventloop"
	"github.com/cryguy/worker/internal/webapi"
	"modernc.org/quickjs"
)

// qjsWorker is a single QuickJS VM in the pool.
type qjsWorker struct {
	vm        *quickjs.VM
	rt        *qjsRuntime
	eventLoop *eventloop.EventLoop
}

// qjsPool manages a fixed-size pool of pre-warmed QuickJS workers.
type qjsPool struct {
	workers chan *qjsWorker
	size    int
	mu      sync.Mutex
}

// setupFunc configures a QuickJS VM with Web APIs, crypto, console, etc.
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

	rt := &qjsRuntime{vm: vm}
	el := eventloop.New()

	for _, setup := range setupFns {
		if err := setup(rt, el); err != nil {
			vm.Close()
			return nil, fmt.Errorf("setup: %w", err)
		}
	}

	// Compile and run the worker script.
	wrapped := webapi.WrapESModule(source)
	v, err := vm.EvalValue(wrapped, quickjs.EvalGlobal)
	if err != nil {
		vm.Close()
		return nil, fmt.Errorf("running worker script: %w", err)
	}
	v.Free()

	// Verify __worker_module__ was set.
	ok, err := rt.EvalBool("typeof globalThis.__worker_module__ !== 'undefined'")
	if err != nil || !ok {
		vm.Close()
		return nil, fmt.Errorf("worker script did not export a default module")
	}

	return &qjsWorker{vm: vm, rt: rt, eventLoop: el}, nil
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
	_ = w.rt.Eval(globalThisCleanupJS)
	w.eventLoop.Reset()
	select {
	case p.workers <- w:
	default:
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
