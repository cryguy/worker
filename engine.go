package worker

import (
	"encoding/json"
	"fmt"
	"log"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"modernc.org/quickjs"
)

// wsConnectionTimeout is the maximum duration for a WebSocket connection.
const wsConnectionTimeout = 5 * time.Minute

// maxWSMessageBytes is the maximum size of a single WebSocket message (64 KB).
const maxWSMessageBytes = 64 * 1024

// poolKey uniquely identifies a compiled worker deployment for a site.
type poolKey struct {
	SiteID    string
	DeployKey string
}

// sitePool wraps a qjsPool with an invalidation flag so that stale pools
// are replaced transparently on the next Execute call.
type sitePool struct {
	pool    *qjsPool
	invalid bool
	mu      sync.RWMutex
}

func (sp *sitePool) isValid() bool {
	sp.mu.RLock()
	defer sp.mu.RUnlock()
	return !sp.invalid
}

func (sp *sitePool) markInvalid() {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	sp.invalid = true
}

// Engine manages per-site worker pools and executes JS worker scripts.
type Engine struct {
	pools        sync.Map // poolKey -> *sitePool
	sources      sync.Map // poolKey -> string (JS source)
	config       EngineConfig
	sourceLoader SourceLoader
	poolMu       sync.Mutex // serializes pool creation/replacement
}

// NewEngine creates an Engine with the given configuration and source loader.
func NewEngine(cfg EngineConfig, sourceLoader SourceLoader) *Engine {
	return &Engine{
		config:       cfg,
		sourceLoader: sourceLoader,
	}
}

// EnsureSource loads the worker JS source into memory if not already cached.
func (e *Engine) EnsureSource(siteID string, deployKey string) error {
	key := poolKey{SiteID: siteID, DeployKey: deployKey}
	if _, ok := e.sources.Load(key); ok {
		return nil
	}

	if e.sourceLoader == nil {
		return fmt.Errorf("source loader not set")
	}

	source, err := e.sourceLoader.GetWorkerScript(siteID, deployKey)
	if err != nil {
		return fmt.Errorf("no source for site %s deploy %s: %w", siteID, deployKey, err)
	}

	e.sources.Store(key, source)
	return nil
}

// CompileAndCache validates that a worker script compiles and stores the
// source for later pool creation. Returns the source bytes for disk storage.
func (e *Engine) CompileAndCache(siteID string, deployKey string, source string) ([]byte, error) {
	key := poolKey{SiteID: siteID, DeployKey: deployKey}

	// Validate the source compiles in a temporary VM.
	vm, err := quickjs.NewVM()
	if err != nil {
		return nil, fmt.Errorf("creating validation VM: %w", err)
	}
	defer vm.Close()

	wrapped := wrapESModule(source)
	v, err := vm.EvalValue(wrapped, quickjs.EvalGlobal)
	if err != nil {
		return nil, fmt.Errorf("compiling worker script: %w", err)
	}
	v.Free()

	e.sources.Store(key, source)
	return []byte(source), nil
}

// getOrCreatePool returns the worker pool for the given site/deploy,
// creating it if necessary.
func (e *Engine) getOrCreatePool(siteID string, deployKey string) (*qjsPool, error) {
	key := poolKey{SiteID: siteID, DeployKey: deployKey}

	// Fast path: valid pool exists (lock-free).
	if val, ok := e.pools.Load(key); ok {
		sp := val.(*sitePool)
		if sp.isValid() {
			return sp.pool, nil
		}
	}

	// Slow path: serialize creation/replacement.
	e.poolMu.Lock()
	defer e.poolMu.Unlock()

	// Double-check after acquiring lock.
	if val, ok := e.pools.Load(key); ok {
		sp := val.(*sitePool)
		if sp.isValid() {
			return sp.pool, nil
		}
		e.pools.Delete(key)
		sp.pool.dispose()
	}

	// Load source.
	srcVal, ok := e.sources.Load(key)
	if !ok {
		return nil, fmt.Errorf("no source for site %s deploy %s", siteID, deployKey)
	}
	source := srcVal.(string)

	cfg := e.config

	setupFns := []setupFunc{
		setupWebAPIs,
		setupURLSearchParamsExt,
		setupGlobals,
		setupEncoding,
		setupTimers,
		setupAbort,
		setupReportError,
		setupCrypto,
		setupCryptoExt,
		setupCryptoDerive,
		setupCryptoRSA,
		setupCryptoEd25519,
		setupCryptoAesCtrKw,
		setupCryptoECDH,
		setupURLPattern,
		setupStreams,
		setupTextStreams,
		setupFormData,
		setupBlobExt,
		setupCompression,
		setupBodyTypes,
		setupWebSocket,
		setupHTMLRewriter,
		setupConsole,
		setupConsoleExt,
		// Fetch needs engine config.
		func(vm *quickjs.VM, el *eventLoop) error {
			return setupFetchWithConfig(vm, cfg, el)
		},
		setupBYOBReader,
		setupMessageChannel,
		setupUnhandledRejection,
		setupScheduler,
		setupDigestStream,
		setupEventSource,
		setupTCPSocket,
		// Phase 6: Env bindings
		setupKV,
		setupStorage,
		setupQueues,
		setupD1,
		setupDurableObjects,
		setupServiceBindings,
		setupAssets,
		setupCache,
	}

	pool, err := newQJSPool(cfg.PoolSize, source, setupFns, e.config.MemoryLimitMB)
	if err != nil {
		return nil, fmt.Errorf("creating worker pool: %w", err)
	}

	sp := &sitePool{pool: pool}
	e.pools.Store(key, sp)
	return pool, nil
}

// Execute runs the worker's fetch handler for the given request and returns
// the result including the response, captured logs, and any error.
func (e *Engine) Execute(siteID string, deployKey string, env *Env, req *WorkerRequest) (result *WorkerResult) {
	start := time.Now()
	result = &WorkerResult{}

	if env == nil {
		result.Error = fmt.Errorf("env must not be nil for site %s", siteID)
		result.Duration = time.Since(start)
		return result
	}

	if env.Dispatcher == nil {
		env.Dispatcher = e
	}
	if env.SiteID == "" {
		env.SiteID = siteID
	}

	if err := e.EnsureSource(siteID, deployKey); err != nil {
		result.Error = err
		result.Duration = time.Since(start)
		return result
	}

	pool, err := e.getOrCreatePool(siteID, deployKey)
	if err != nil {
		result.Error = err
		result.Duration = time.Since(start)
		return result
	}

	w, err := pool.get()
	if err != nil {
		result.Error = fmt.Errorf("acquiring worker from pool: %w", err)
		result.Duration = time.Since(start)
		return result
	}

	var keepWorker bool
	var timedOut atomic.Bool
	timeout := time.Duration(e.config.ExecutionTimeout) * time.Millisecond
	watchdog := time.AfterFunc(timeout, func() {
		timedOut.Store(true)
		w.vm.Interrupt()
	})

	var panicked bool
	defer func() {
		stopped := watchdog.Stop()
		if r := recover(); r != nil {
			panicked = true
			if timedOut.Load() {
				result.Error = fmt.Errorf("worker execution timed out (limit: %v)", timeout)
			} else {
				result.Error = fmt.Errorf("worker panic: %v", r)
			}
		}
		result.Duration = time.Since(start)
		if keepWorker {
			return
		}
		if stopped && !timedOut.Load() && !panicked {
			pool.put(w)
		} else {
			log.Printf("worker: discarding worker for site %s deploy %s (timed out or panicked)", siteID, deployKey)
			w.vm.Close()
			key := poolKey{SiteID: siteID, DeployKey: deployKey}
			if val, ok := e.pools.Load(key); ok {
				sp := val.(*sitePool)
				sp.markInvalid()
			}
		}
	}()

	vm := w.vm

	// Set up per-request state.
	reqID := newRequestState(e.config.MaxFetchRequests, env)
	if err := setGlobal(vm, "__requestID", strconv.FormatUint(reqID, 10)); err != nil {
		clearRequestState(reqID)
		result.Error = fmt.Errorf("setting request ID: %w", err)
		return result
	}

	// Build the JS arguments: request, env, ctx.
	if err := goRequestToJS(vm, req); err != nil {
		clearRequestState(reqID)
		result.Error = fmt.Errorf("building JS request: %w", err)
		return result
	}

	if err := buildEnvObject(vm, env, reqID); err != nil {
		state := clearRequestState(reqID)
		if state != nil {
			result.Logs = state.logs
		}
		result.Error = fmt.Errorf("building JS env: %w", err)
		return result
	}

	if err := buildExecContext(vm); err != nil {
		state := clearRequestState(reqID)
		if state != nil {
			result.Logs = state.logs
		}
		result.Error = fmt.Errorf("building JS context: %w", err)
		return result
	}

	// Call __worker_module__.fetch(request, env, ctx) via EvalValue.
	callResult, err := vm.EvalValue(`
		(function() {
			var mod = globalThis.__worker_module__;
			if (!mod || typeof mod.fetch !== 'function') {
				throw new Error('worker module has no fetch handler');
			}
			return mod.fetch(globalThis.__req, globalThis.__env, globalThis.__ctx);
		})()
	`, quickjs.EvalGlobal)
	if err != nil {
		state := clearRequestState(reqID)
		if state != nil {
			result.Logs = state.logs
		}
		if timedOut.Load() {
			result.Error = fmt.Errorf("worker execution timed out (limit: %v)", timeout)
		} else {
			result.Error = fmt.Errorf("invoking worker fetch: %w", err)
		}
		return result
	}

	// Store the result for further processing and free the EvalValue result.
	if err := setGlobal(vm, "__call_result", callResult); err != nil {
		callResult.Free()
		state := clearRequestState(reqID)
		if state != nil {
			result.Logs = state.logs
		}
		result.Error = fmt.Errorf("storing call result: %w", err)
		return result
	}
	callResult.Free()

	// Pump microtasks.
	executePendingJobs(vm)

	// Drain event loop.
	deadline := start.Add(timeout)
	if w.eventLoop.hasPending() {
		w.eventLoop.drain(vm, deadline)
	}

	// Await the result if it's a Promise.
	if err := awaitValueWithLoop(vm, "__call_result", deadline, w.eventLoop); err != nil {
		state := clearRequestState(reqID)
		if state != nil {
			result.Logs = state.logs
		}
		result.Error = fmt.Errorf("awaiting worker response: %w", err)
		return result
	}

	// Move result to __result for jsResponseToGo.
	_ = evalDiscard(vm, "globalThis.__result = globalThis.__call_result; delete globalThis.__call_result;")

	// Convert JS Response to Go.
	resp, err := jsResponseToGo(vm)
	if err != nil {
		state := clearRequestState(reqID)
		if state != nil {
			result.Logs = state.logs
		}
		result.Error = fmt.Errorf("converting worker response: %w", err)
		return result
	}

	// Drain waitUntil promises.
	drainWaitUntil(vm, deadline)

	// WebSocket upgrade handling.
	if resp.HasWebSocket && resp.StatusCode == 101 {
		_ = evalDiscard(vm, `
			if (globalThis.__ws_check_resp && globalThis.__ws_check_resp._peer) {
				globalThis.__ws_active_server = globalThis.__ws_check_resp._peer;
				globalThis.__ws_active_server._isHTTPBridged = true;
			}
			delete globalThis.__ws_check_resp;
		`)

		state := getRequestState(reqID)
		if state != nil {
			result.Logs = state.logs
		}

		keepWorker = true
		result.Response = resp
		result.WebSocket = &WebSocketHandler{
			worker:  w,
			pool:    pool,
			reqID:   reqID,
			timeout: wsConnectionTimeout,
		}
		return result
	}

	state := clearRequestState(reqID)
	if state != nil {
		result.Logs = state.logs
	}
	result.Response = resp
	return result
}

// ExecuteScheduled runs the worker's scheduled handler for cron triggers.
func (e *Engine) ExecuteScheduled(siteID string, deployKey string, env *Env, cron string) (result *WorkerResult) {
	start := time.Now()
	result = &WorkerResult{}

	if env == nil {
		result.Error = fmt.Errorf("env must not be nil for site %s", siteID)
		result.Duration = time.Since(start)
		return result
	}

	if env.Dispatcher == nil {
		env.Dispatcher = e
	}
	if env.SiteID == "" {
		env.SiteID = siteID
	}

	if err := e.EnsureSource(siteID, deployKey); err != nil {
		result.Error = err
		result.Duration = time.Since(start)
		return result
	}

	pool, err := e.getOrCreatePool(siteID, deployKey)
	if err != nil {
		result.Error = err
		result.Duration = time.Since(start)
		return result
	}

	w, err := pool.get()
	if err != nil {
		result.Error = fmt.Errorf("acquiring worker from pool: %w", err)
		result.Duration = time.Since(start)
		return result
	}

	var timedOut atomic.Bool
	timeout := time.Duration(e.config.ExecutionTimeout) * time.Millisecond
	watchdog := time.AfterFunc(timeout, func() {
		timedOut.Store(true)
		w.vm.Interrupt()
	})

	var panicked bool
	defer func() {
		stopped := watchdog.Stop()
		if r := recover(); r != nil {
			panicked = true
			if timedOut.Load() {
				result.Error = fmt.Errorf("worker execution timed out (limit: %v)", timeout)
			} else {
				result.Error = fmt.Errorf("worker panic: %v", r)
			}
		}
		result.Duration = time.Since(start)
		if stopped && !timedOut.Load() && !panicked {
			pool.put(w)
		} else {
			log.Printf("worker: discarding scheduled worker for site %s deploy %s (timed out or panicked)", siteID, deployKey)
			w.vm.Close()
			key := poolKey{SiteID: siteID, DeployKey: deployKey}
			if val, ok := e.pools.Load(key); ok {
				sp := val.(*sitePool)
				sp.markInvalid()
			}
		}
	}()

	vm := w.vm

	reqID := newRequestState(e.config.MaxFetchRequests, env)
	_ = setGlobal(vm, "__requestID", strconv.FormatUint(reqID, 10))

	// Build the scheduled event.
	scheduledTimeMs := float64(time.Now().UnixMilli())
	eventScript := fmt.Sprintf(`new ScheduledEvent(%f, %q)`, scheduledTimeMs, cron)
	v, err := vm.EvalValue(eventScript, quickjs.EvalGlobal)
	if err != nil {
		clearRequestState(reqID)
		result.Error = fmt.Errorf("creating ScheduledEvent: %w", err)
		return result
	}
	if err := setGlobal(vm, "__sched_event", v); err != nil {
		v.Free()
		clearRequestState(reqID)
		result.Error = fmt.Errorf("storing ScheduledEvent: %w", err)
		return result
	}
	v.Free()

	if err := buildEnvObject(vm, env, reqID); err != nil {
		state := clearRequestState(reqID)
		if state != nil {
			result.Logs = state.logs
		}
		result.Error = fmt.Errorf("building JS env: %w", err)
		return result
	}

	if err := buildExecContext(vm); err != nil {
		state := clearRequestState(reqID)
		if state != nil {
			result.Logs = state.logs
		}
		result.Error = fmt.Errorf("building JS context: %w", err)
		return result
	}

	// Call __worker_module__.scheduled(event, env, ctx).
	callResult, err := vm.EvalValue(`
		(function() {
			var mod = globalThis.__worker_module__;
			if (!mod || typeof mod.scheduled !== 'function') {
				throw new Error('worker module has no scheduled handler');
			}
			return mod.scheduled(globalThis.__sched_event, globalThis.__env, globalThis.__ctx);
		})()
	`, quickjs.EvalGlobal)
	if err != nil {
		state := clearRequestState(reqID)
		if state != nil {
			result.Logs = state.logs
		}
		result.Error = fmt.Errorf("invoking worker scheduled: %w", err)
		return result
	}
	if err := setGlobal(vm, "__call_result", callResult); err == nil {
		callResult.Free()
	}

	// Pump microtasks and drain event loop.
	executePendingJobs(vm)
	deadline := start.Add(timeout)
	if w.eventLoop.hasPending() {
		w.eventLoop.drain(vm, deadline)
	}

	// Await if the handler returns a promise.
	isPromise, _ := evalBool(vm, "globalThis.__call_result instanceof Promise")
	if isPromise {
		if err := awaitValueWithLoop(vm, "__call_result", deadline, w.eventLoop); err != nil {
			state := clearRequestState(reqID)
			if state != nil {
				result.Logs = state.logs
			}
			result.Error = fmt.Errorf("awaiting scheduled handler: %w", err)
			return result
		}
	}

	_ = evalDiscard(vm, "delete globalThis.__call_result; delete globalThis.__sched_event;")

	drainWaitUntil(vm, deadline)

	state := clearRequestState(reqID)
	if state != nil {
		result.Logs = state.logs
	}
	return result
}

// ExecuteTail runs the worker's tail handler for log forwarding.
func (e *Engine) ExecuteTail(siteID string, deployKey string, env *Env, events []TailEvent) (result *WorkerResult) {
	start := time.Now()
	result = &WorkerResult{}

	if env == nil {
		result.Error = fmt.Errorf("env must not be nil for site %s", siteID)
		result.Duration = time.Since(start)
		return result
	}

	if env.Dispatcher == nil {
		env.Dispatcher = e
	}
	if env.SiteID == "" {
		env.SiteID = siteID
	}

	if err := e.EnsureSource(siteID, deployKey); err != nil {
		result.Error = err
		result.Duration = time.Since(start)
		return result
	}

	pool, err := e.getOrCreatePool(siteID, deployKey)
	if err != nil {
		result.Error = err
		result.Duration = time.Since(start)
		return result
	}

	w, err := pool.get()
	if err != nil {
		result.Error = fmt.Errorf("acquiring worker from pool: %w", err)
		result.Duration = time.Since(start)
		return result
	}

	var timedOut atomic.Bool
	timeout := time.Duration(e.config.ExecutionTimeout) * time.Millisecond
	watchdog := time.AfterFunc(timeout, func() {
		timedOut.Store(true)
		w.vm.Interrupt()
	})

	var panicked bool
	defer func() {
		stopped := watchdog.Stop()
		if r := recover(); r != nil {
			panicked = true
			if timedOut.Load() {
				result.Error = fmt.Errorf("worker execution timed out (limit: %v)", timeout)
			} else {
				result.Error = fmt.Errorf("worker panic: %v", r)
			}
		}
		result.Duration = time.Since(start)
		if stopped && !timedOut.Load() && !panicked {
			pool.put(w)
		} else {
			log.Printf("worker: discarding tail worker for site %s deploy %s (timed out or panicked)", siteID, deployKey)
			w.vm.Close()
			key := poolKey{SiteID: siteID, DeployKey: deployKey}
			if val, ok := e.pools.Load(key); ok {
				sp := val.(*sitePool)
				sp.markInvalid()
			}
		}
	}()

	vm := w.vm

	reqID := newRequestState(e.config.MaxFetchRequests, env)
	_ = setGlobal(vm, "__requestID", strconv.FormatUint(reqID, 10))

	// Serialize tail events to JSON and inject into JS context.
	eventsJSON, err := json.Marshal(events)
	if err != nil {
		clearRequestState(reqID)
		result.Error = fmt.Errorf("marshaling tail events: %w", err)
		return result
	}
	eventsScript := fmt.Sprintf(`JSON.parse(%q)`, string(eventsJSON))
	evVal, err := vm.EvalValue(eventsScript, quickjs.EvalGlobal)
	if err != nil {
		clearRequestState(reqID)
		result.Error = fmt.Errorf("creating tail events array: %w", err)
		return result
	}
	if err := setGlobal(vm, "__tail_events", evVal); err != nil {
		evVal.Free()
		clearRequestState(reqID)
		result.Error = fmt.Errorf("storing tail events: %w", err)
		return result
	}
	evVal.Free()

	if err := buildEnvObject(vm, env, reqID); err != nil {
		state := clearRequestState(reqID)
		if state != nil {
			result.Logs = state.logs
		}
		result.Error = fmt.Errorf("building JS env: %w", err)
		return result
	}

	if err := buildExecContext(vm); err != nil {
		state := clearRequestState(reqID)
		if state != nil {
			result.Logs = state.logs
		}
		result.Error = fmt.Errorf("building JS context: %w", err)
		return result
	}

	// Call __worker_module__.tail(events, env, ctx).
	callResult, err := vm.EvalValue(`
		(function() {
			var mod = globalThis.__worker_module__;
			if (!mod || typeof mod.tail !== 'function') {
				throw new Error('worker module has no tail handler');
			}
			return mod.tail(globalThis.__tail_events, globalThis.__env, globalThis.__ctx);
		})()
	`, quickjs.EvalGlobal)
	if err != nil {
		state := clearRequestState(reqID)
		if state != nil {
			result.Logs = state.logs
		}
		if timedOut.Load() {
			result.Error = fmt.Errorf("worker execution timed out (limit: %v)", timeout)
		} else {
			result.Error = fmt.Errorf("invoking worker tail: %w", err)
		}
		return result
	}
	if err := setGlobal(vm, "__call_result", callResult); err == nil {
		callResult.Free()
	}

	// Pump microtasks and drain event loop.
	executePendingJobs(vm)
	deadline := start.Add(timeout)
	if w.eventLoop.hasPending() {
		w.eventLoop.drain(vm, deadline)
	}

	// Await if the handler returns a promise.
	isPromise, _ := evalBool(vm, "globalThis.__call_result instanceof Promise")
	if isPromise {
		if err := awaitValueWithLoop(vm, "__call_result", deadline, w.eventLoop); err != nil {
			state := clearRequestState(reqID)
			if state != nil {
				result.Logs = state.logs
			}
			result.Error = fmt.Errorf("awaiting tail handler: %w", err)
			return result
		}
	}

	_ = evalDiscard(vm, "delete globalThis.__call_result; delete globalThis.__tail_events;")

	drainWaitUntil(vm, deadline)

	state := clearRequestState(reqID)
	if state != nil {
		result.Logs = state.logs
	}
	return result
}

// ExecuteFunction calls an arbitrary named function on the worker module
// with the given env and optional JSON-serializable arguments.
func (e *Engine) ExecuteFunction(siteID string, deployKey string, env *Env, fnName string, args ...any) (result *WorkerResult) {
	start := time.Now()
	result = &WorkerResult{}

	if env == nil {
		result.Error = fmt.Errorf("env must not be nil for site %s", siteID)
		result.Duration = time.Since(start)
		return result
	}

	if env.Dispatcher == nil {
		env.Dispatcher = e
	}
	if env.SiteID == "" {
		env.SiteID = siteID
	}

	if err := e.EnsureSource(siteID, deployKey); err != nil {
		result.Error = err
		result.Duration = time.Since(start)
		return result
	}

	pool, err := e.getOrCreatePool(siteID, deployKey)
	if err != nil {
		result.Error = err
		result.Duration = time.Since(start)
		return result
	}

	w, err := pool.get()
	if err != nil {
		result.Error = fmt.Errorf("acquiring worker from pool: %w", err)
		result.Duration = time.Since(start)
		return result
	}

	var timedOut atomic.Bool
	timeout := time.Duration(e.config.ExecutionTimeout) * time.Millisecond
	watchdog := time.AfterFunc(timeout, func() {
		timedOut.Store(true)
		w.vm.Interrupt()
	})

	var panicked bool
	defer func() {
		stopped := watchdog.Stop()
		if r := recover(); r != nil {
			panicked = true
			if timedOut.Load() {
				result.Error = fmt.Errorf("worker execution timed out (limit: %v)", timeout)
			} else {
				result.Error = fmt.Errorf("worker panic: %v", r)
			}
		}
		result.Duration = time.Since(start)
		if stopped && !timedOut.Load() && !panicked {
			pool.put(w)
		} else {
			log.Printf("worker: discarding worker for site %s deploy %s (timed out or panicked)", siteID, deployKey)
			w.vm.Close()
			key := poolKey{SiteID: siteID, DeployKey: deployKey}
			if val, ok := e.pools.Load(key); ok {
				sp := val.(*sitePool)
				sp.markInvalid()
			}
		}
	}()

	vm := w.vm

	reqID := newRequestState(e.config.MaxFetchRequests, env)
	if err := setGlobal(vm, "__requestID", strconv.FormatUint(reqID, 10)); err != nil {
		clearRequestState(reqID)
		result.Error = fmt.Errorf("setting request ID: %w", err)
		return result
	}

	if err := buildEnvObject(vm, env, reqID); err != nil {
		state := clearRequestState(reqID)
		if state != nil {
			result.Logs = state.logs
		}
		result.Error = fmt.Errorf("building JS env: %w", err)
		return result
	}

	// Build JS arguments array: inject each arg via JSON.parse.
	argsJS := "globalThis.__env"
	for i, arg := range args {
		argJSON, err := json.Marshal(arg)
		if err != nil {
			state := clearRequestState(reqID)
			if state != nil {
				result.Logs = state.logs
			}
			result.Error = fmt.Errorf("marshaling argument %d: %w", i, err)
			return result
		}
		varName := fmt.Sprintf("__fn_arg_%d", i)
		argScript := fmt.Sprintf(`JSON.parse(%q)`, string(argJSON))
		argVal, err := vm.EvalValue(argScript, quickjs.EvalGlobal)
		if err != nil {
			state := clearRequestState(reqID)
			if state != nil {
				result.Logs = state.logs
			}
			result.Error = fmt.Errorf("creating JS argument %d: %w", i, err)
			return result
		}
		if err := setGlobal(vm, varName, argVal); err != nil {
			argVal.Free()
			state := clearRequestState(reqID)
			if state != nil {
				result.Logs = state.logs
			}
			result.Error = fmt.Errorf("storing JS argument %d: %w", i, err)
			return result
		}
		argVal.Free()
		argsJS += fmt.Sprintf(", globalThis.%s", varName)
	}

	// Call the named function.
	callScript := fmt.Sprintf(`
		(function() {
			var mod = globalThis.__worker_module__;
			if (!mod || typeof mod[%q] !== 'function') {
				throw new Error('worker module has no "' + %q + '" function');
			}
			return mod[%q](%s);
		})()
	`, fnName, fnName, fnName, argsJS)

	callResult, err := vm.EvalValue(callScript, quickjs.EvalGlobal)
	if err != nil {
		state := clearRequestState(reqID)
		if state != nil {
			result.Logs = state.logs
		}
		if timedOut.Load() {
			result.Error = fmt.Errorf("worker execution timed out (limit: %v)", timeout)
		} else {
			result.Error = fmt.Errorf("invoking worker %q: %w", fnName, err)
		}
		return result
	}
	if err := setGlobal(vm, "__call_result", callResult); err == nil {
		callResult.Free()
	}

	// Pump microtasks and drain event loop.
	executePendingJobs(vm)
	deadline := start.Add(timeout)
	if w.eventLoop.hasPending() {
		w.eventLoop.drain(vm, deadline)
	}

	// Await the result if it's a Promise.
	if err := awaitValueWithLoop(vm, "__call_result", deadline, w.eventLoop); err != nil {
		state := clearRequestState(reqID)
		if state != nil {
			result.Logs = state.logs
		}
		result.Error = fmt.Errorf("awaiting worker %q: %w", fnName, err)
		return result
	}

	drainWaitUntil(vm, deadline)

	// Serialize the return value to JSON.
	jsonStr, err := evalString(vm, `
		(function() {
			var r = globalThis.__call_result;
			delete globalThis.__call_result;
			if (r === undefined || r === null) return "null";
			return JSON.stringify(r);
		})()
	`)
	if err != nil {
		state := clearRequestState(reqID)
		if state != nil {
			result.Logs = state.logs
		}
		result.Error = fmt.Errorf("serializing return value: %w", err)
		return result
	}
	result.Data = jsonStr

	// Clean up fn arg globals.
	for i := range args {
		_ = evalDiscard(vm, fmt.Sprintf("delete globalThis.__fn_arg_%d", i))
	}

	state := clearRequestState(reqID)
	if state != nil {
		result.Logs = state.logs
	}
	return result
}

// InvalidatePool marks the pool for the given site/deploy as invalid.
func (e *Engine) InvalidatePool(siteID string, deployKey string) {
	key := poolKey{SiteID: siteID, DeployKey: deployKey}
	if val, ok := e.pools.LoadAndDelete(key); ok {
		sp := val.(*sitePool)
		sp.markInvalid()
		sp.pool.dispose()
	}
	e.sources.Delete(key)
}

// Shutdown invalidates all pools and clears all cached sources.
func (e *Engine) Shutdown() {
	e.pools.Range(func(key, val any) bool {
		sp := val.(*sitePool)
		sp.markInvalid()
		sp.pool.dispose()
		e.pools.Delete(key)
		return true
	})
	e.sources.Range(func(key, _ any) bool {
		e.sources.Delete(key)
		return true
	})
}

// MaxResponseBytes returns the configured maximum response body size.
func (e *Engine) MaxResponseBytes() int {
	return e.config.MaxResponseBytes
}

// awaitValue resolves a potentially-promise value stored in a global variable
// by pumping QuickJS's microtask queue via Promise.resolve().then().
// The global variable is updated in-place with the resolved value.
func awaitValue(vm *quickjs.VM, globalVar string, deadline time.Time) error {
	return awaitValueWithLoop(vm, globalVar, deadline, nil)
}

// awaitValueWithLoop awaits a Promise stored in a global variable, optionally
// draining the event loop between microtask pumps. This is needed for streaming
// operations (CompressionStream, etc.) that create async work requiring timers.
func awaitValueWithLoop(vm *quickjs.VM, globalVar string, deadline time.Time, el *eventLoop) error {
	// Check if the value is a Promise.
	isPromise, err := evalBool(vm, fmt.Sprintf("globalThis.%s instanceof Promise", globalVar))
	if err != nil || !isPromise {
		return nil // Not a promise, nothing to await.
	}

	// Set up Promise.then() to capture the resolved/rejected value.
	setupJS := fmt.Sprintf(`
		delete globalThis.__awaited_result;
		delete globalThis.__awaited_state;
		Promise.resolve(globalThis.%s).then(
			function(r) { globalThis.__awaited_result = r; globalThis.__awaited_state = 'fulfilled'; },
			function(e) { globalThis.__awaited_result = e; globalThis.__awaited_state = 'rejected'; }
		);
	`, globalVar)
	if err := evalDiscard(vm, setupJS); err != nil {
		return fmt.Errorf("setting up promise await: %w", err)
	}

	// Pump microtasks (and optionally the event loop) until the promise settles.
	for {
		executePendingJobs(vm)

		// Also drain event loop if provided — streaming operations need timers/fetches.
		if el != nil && el.hasPending() {
			shortDeadline := time.Now().Add(10 * time.Millisecond)
			if shortDeadline.After(deadline) {
				shortDeadline = deadline
			}
			el.drain(vm, shortDeadline)
			executePendingJobs(vm)
		}

		stateStr, err := evalString(vm, "String(globalThis.__awaited_state)")
		if err != nil {
			return fmt.Errorf("checking promise state: %w", err)
		}
		if stateStr != "undefined" {
			break
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("promise resolution timed out")
		}
		runtime.Gosched()
	}

	stateStr, _ := evalString(vm, "String(globalThis.__awaited_state)")

	if stateStr == "rejected" {
		errMsg, _ := evalString(vm, "String(globalThis.__awaited_result)")
		_ = evalDiscard(vm, "delete globalThis.__awaited_result; delete globalThis.__awaited_state;")
		return fmt.Errorf("promise rejected: %s", errMsg)
	}

	// Move the resolved value back to the original global.
	_ = evalDiscard(vm, fmt.Sprintf(
		"globalThis.%s = globalThis.__awaited_result; delete globalThis.__awaited_result; delete globalThis.__awaited_state;",
		globalVar))

	return nil
}
