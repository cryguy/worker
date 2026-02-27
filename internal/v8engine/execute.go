//go:build v8

package v8engine

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cryguy/worker/internal/core"
	"github.com/cryguy/worker/internal/webapi"
	v8 "github.com/tommie/v8go"
)

// wsConnectionTimeout is the maximum duration for a WebSocket connection.
const wsConnectionTimeout = 5 * time.Minute

// poolKey uniquely identifies a compiled worker deployment for a site.
type poolKey struct {
	SiteID    string
	DeployKey string
}

// sitePool wraps a v8Pool with an invalidation flag.
type sitePool struct {
	pool    *v8Pool
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

// Engine manages per-site worker pools and executes JS worker scripts
// using the V8 engine.
type Engine struct {
	pools        sync.Map // poolKey -> *sitePool
	sources      sync.Map // poolKey -> string (JS source)
	config       core.EngineConfig
	sourceLoader core.SourceLoader
	poolMu       sync.Mutex
}

// NewEngine creates an Engine with the given configuration and source loader.
func NewEngine(cfg core.EngineConfig, sourceLoader core.SourceLoader) *Engine {
	return &Engine{
		config:       cfg,
		sourceLoader: sourceLoader,
	}
}

// SetDispatcher satisfies the EngineBackend interface.
func (e *Engine) SetDispatcher(d core.WorkerDispatcher) {}

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

// CompileAndCache validates that a worker script compiles and stores the source.
func (e *Engine) CompileAndCache(siteID string, deployKey string, source string) ([]byte, error) {
	key := poolKey{SiteID: siteID, DeployKey: deployKey}

	iso := v8.NewIsolate()
	defer iso.Dispose()

	wrapped := webapi.WrapESModule(source)
	if _, err := iso.CompileUnboundScript(wrapped, "worker.js", v8.CompileOptions{}); err != nil {
		return nil, fmt.Errorf("compiling worker script: %w", err)
	}

	e.sources.Store(key, source)
	return []byte(source), nil
}

// getOrCreatePool returns the worker pool for the given site/deploy.
func (e *Engine) getOrCreatePool(siteID string, deployKey string) (*v8Pool, error) {
	key := poolKey{SiteID: siteID, DeployKey: deployKey}

	if val, ok := e.pools.Load(key); ok {
		sp := val.(*sitePool)
		if sp.isValid() {
			return sp.pool, nil
		}
	}

	e.poolMu.Lock()
	defer e.poolMu.Unlock()

	if val, ok := e.pools.Load(key); ok {
		sp := val.(*sitePool)
		if sp.isValid() {
			return sp.pool, nil
		}
		e.pools.Delete(key)
		sp.pool.dispose()
	}

	srcVal, ok := e.sources.Load(key)
	if !ok {
		return nil, fmt.Errorf("no source for site %s deploy %s", siteID, deployKey)
	}
	source := srcVal.(string)

	setupFns := buildSetupFuncs(e.config)

	pool, err := newV8Pool(e.config.PoolSize, source, setupFns, e.config.MemoryLimitMB)
	if err != nil {
		return nil, fmt.Errorf("creating v8 pool: %w", err)
	}

	sp := &sitePool{pool: pool}
	e.pools.Store(key, sp)
	return pool, nil
}

// Execute runs the worker's fetch handler for the given request.
func (e *Engine) Execute(siteID string, deployKey string, env *core.Env, req *core.WorkerRequest) (result *core.WorkerResult) {
	start := time.Now()
	result = &core.WorkerResult{}

	if env == nil {
		result.Error = fmt.Errorf("env must not be nil for site %s", siteID)
		result.Duration = time.Since(start)
		return result
	}

	env.InitRuntime(e, siteID)

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
		w.iso.TerminateExecution()
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
			w.ctx.Close()
			w.iso.Dispose()
			key := poolKey{SiteID: siteID, DeployKey: deployKey}
			if val, ok := e.pools.Load(key); ok {
				sp := val.(*sitePool)
				sp.markInvalid()
			}
		}
	}()

	rt := w.rt

	// Set up per-request state.
	reqID := core.NewRequestState(e.config.MaxFetchRequests, env)
	if err := rt.SetGlobal("__requestID", strconv.FormatUint(reqID, 10)); err != nil {
		core.ClearRequestState(reqID)
		result.Error = fmt.Errorf("setting request ID: %w", err)
		return result
	}

	if err := webapi.GoRequestToJS(rt, req); err != nil {
		core.ClearRequestState(reqID)
		result.Error = fmt.Errorf("building JS request: %w", err)
		return result
	}

	if err := webapi.BuildEnvObject(rt, env, reqID); err != nil {
		state := core.ClearRequestState(reqID)
		if state != nil {
			result.Logs = state.Logs
		}
		result.Error = fmt.Errorf("building JS env: %w", err)
		return result
	}

	if err := webapi.BuildExecContext(rt); err != nil {
		state := core.ClearRequestState(reqID)
		if state != nil {
			result.Logs = state.Logs
		}
		result.Error = fmt.Errorf("building JS context: %w", err)
		return result
	}

	// Call __worker_module__.fetch(request, env, ctx) via JS.
	_, err = w.ctx.RunScript(`
		(function() {
			var mod = globalThis.__worker_module__;
			if (!mod || typeof mod.fetch !== 'function') {
				throw new Error('worker module has no fetch handler');
			}
			globalThis.__call_result = mod.fetch(globalThis.__req, globalThis.__env, globalThis.__ctx);
		})()
	`, "call_fetch.js")
	if err != nil {
		state := core.ClearRequestState(reqID)
		if state != nil {
			result.Logs = state.Logs
		}
		if timedOut.Load() {
			result.Error = fmt.Errorf("worker execution timed out (limit: %v)", timeout)
		} else {
			result.Error = fmt.Errorf("invoking worker fetch: %w", err)
		}
		return result
	}

	rt.RunMicrotasks()

	deadline := start.Add(timeout)
	if w.eventLoop.HasPending() {
		w.eventLoop.Drain(rt, deadline)
	}

	if err := webapi.AwaitValue(rt, "__call_result", deadline, w.eventLoop); err != nil {
		state := core.ClearRequestState(reqID)
		if state != nil {
			result.Logs = state.Logs
		}
		result.Error = fmt.Errorf("awaiting worker response: %w", err)
		return result
	}

	_ = rt.Eval("globalThis.__result = globalThis.__call_result; delete globalThis.__call_result;")

	resp, err := webapi.JsResponseToGo(rt)
	if err != nil {
		state := core.ClearRequestState(reqID)
		if state != nil {
			result.Logs = state.Logs
		}
		result.Error = fmt.Errorf("converting worker response: %w", err)
		return result
	}

	webapi.DrainWaitUntil(rt, deadline)

	if resp.HasWebSocket && resp.StatusCode == 101 {
		_ = rt.Eval(`
			if (globalThis.__ws_check_resp && globalThis.__ws_check_resp._peer) {
				globalThis.__ws_active_server = globalThis.__ws_check_resp._peer;
				globalThis.__ws_active_server._isHTTPBridged = true;
			}
			delete globalThis.__ws_check_resp;
		`)

		state := core.GetRequestState(reqID)
		if state != nil {
			result.Logs = state.Logs
		}

		keepWorker = true
		result.Response = resp
		result.WebSocket = &webapi.WebSocketHandler{
			Runtime: rt,
			Loop:    w.eventLoop,
			ReqID:   reqID,
			Timeout: wsConnectionTimeout,
			OnComplete: func() {
				pool.put(w)
			},
		}
		return result
	}

	state := core.ClearRequestState(reqID)
	if state != nil {
		result.Logs = state.Logs
	}
	result.Response = resp
	return result
}

// ExecuteScheduled runs the worker's scheduled handler.
func (e *Engine) ExecuteScheduled(siteID string, deployKey string, env *core.Env, cron string) (result *core.WorkerResult) {
	start := time.Now()
	result = &core.WorkerResult{}

	if env == nil {
		result.Error = fmt.Errorf("env must not be nil for site %s", siteID)
		result.Duration = time.Since(start)
		return result
	}

	env.InitRuntime(e, siteID)

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
		w.iso.TerminateExecution()
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
			w.ctx.Close()
			w.iso.Dispose()
			key := poolKey{SiteID: siteID, DeployKey: deployKey}
			if val, ok := e.pools.Load(key); ok {
				sp := val.(*sitePool)
				sp.markInvalid()
			}
		}
	}()

	rt := w.rt

	reqID := core.NewRequestState(e.config.MaxFetchRequests, env)
	_ = rt.SetGlobal("__requestID", strconv.FormatUint(reqID, 10))

	scheduledTimeMs := float64(time.Now().UnixMilli())
	eventScript := fmt.Sprintf(`globalThis.__sched_event = new ScheduledEvent(%f, %q)`, scheduledTimeMs, cron)
	if err := rt.Eval(eventScript); err != nil {
		core.ClearRequestState(reqID)
		result.Error = fmt.Errorf("creating ScheduledEvent: %w", err)
		return result
	}

	if err := webapi.BuildEnvObject(rt, env, reqID); err != nil {
		state := core.ClearRequestState(reqID)
		if state != nil {
			result.Logs = state.Logs
		}
		result.Error = fmt.Errorf("building JS env: %w", err)
		return result
	}

	if err := webapi.BuildExecContext(rt); err != nil {
		state := core.ClearRequestState(reqID)
		if state != nil {
			result.Logs = state.Logs
		}
		result.Error = fmt.Errorf("building JS context: %w", err)
		return result
	}

	_, err = w.ctx.RunScript(`
		(function() {
			var mod = globalThis.__worker_module__;
			if (!mod || typeof mod.scheduled !== 'function') {
				throw new Error('worker module has no scheduled handler');
			}
			globalThis.__call_result = mod.scheduled(globalThis.__sched_event, globalThis.__env, globalThis.__ctx);
		})()
	`, "call_scheduled.js")
	if err != nil {
		state := core.ClearRequestState(reqID)
		if state != nil {
			result.Logs = state.Logs
		}
		result.Error = fmt.Errorf("invoking worker scheduled: %w", err)
		return result
	}

	rt.RunMicrotasks()
	deadline := start.Add(timeout)
	if w.eventLoop.HasPending() {
		w.eventLoop.Drain(rt, deadline)
	}

	isPromise, _ := rt.EvalBool("globalThis.__call_result instanceof Promise")
	if isPromise {
		if err := webapi.AwaitValue(rt, "__call_result", deadline, w.eventLoop); err != nil {
			state := core.ClearRequestState(reqID)
			if state != nil {
				result.Logs = state.Logs
			}
			result.Error = fmt.Errorf("awaiting scheduled handler: %w", err)
			return result
		}
	}

	_ = rt.Eval("delete globalThis.__call_result; delete globalThis.__sched_event;")

	webapi.DrainWaitUntil(rt, deadline)

	state := core.ClearRequestState(reqID)
	if state != nil {
		result.Logs = state.Logs
	}
	return result
}

// ExecuteTail runs the worker's tail handler.
func (e *Engine) ExecuteTail(siteID string, deployKey string, env *core.Env, events []core.TailEvent) (result *core.WorkerResult) {
	start := time.Now()
	result = &core.WorkerResult{}

	if env == nil {
		result.Error = fmt.Errorf("env must not be nil for site %s", siteID)
		result.Duration = time.Since(start)
		return result
	}

	env.InitRuntime(e, siteID)

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
		w.iso.TerminateExecution()
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
			w.ctx.Close()
			w.iso.Dispose()
			key := poolKey{SiteID: siteID, DeployKey: deployKey}
			if val, ok := e.pools.Load(key); ok {
				sp := val.(*sitePool)
				sp.markInvalid()
			}
		}
	}()

	rt := w.rt

	reqID := core.NewRequestState(e.config.MaxFetchRequests, env)
	_ = rt.SetGlobal("__requestID", strconv.FormatUint(reqID, 10))

	eventsJSON, err := json.Marshal(events)
	if err != nil {
		core.ClearRequestState(reqID)
		result.Error = fmt.Errorf("marshaling tail events: %w", err)
		return result
	}
	eventsScript := fmt.Sprintf(`globalThis.__tail_events = JSON.parse(%q)`, string(eventsJSON))
	if err := rt.Eval(eventsScript); err != nil {
		core.ClearRequestState(reqID)
		result.Error = fmt.Errorf("creating tail events array: %w", err)
		return result
	}

	if err := webapi.BuildEnvObject(rt, env, reqID); err != nil {
		state := core.ClearRequestState(reqID)
		if state != nil {
			result.Logs = state.Logs
		}
		result.Error = fmt.Errorf("building JS env: %w", err)
		return result
	}

	if err := webapi.BuildExecContext(rt); err != nil {
		state := core.ClearRequestState(reqID)
		if state != nil {
			result.Logs = state.Logs
		}
		result.Error = fmt.Errorf("building JS context: %w", err)
		return result
	}

	_, err = w.ctx.RunScript(`
		(function() {
			var mod = globalThis.__worker_module__;
			if (!mod || typeof mod.tail !== 'function') {
				throw new Error('worker module has no tail handler');
			}
			globalThis.__call_result = mod.tail(globalThis.__tail_events, globalThis.__env, globalThis.__ctx);
		})()
	`, "call_tail.js")
	if err != nil {
		state := core.ClearRequestState(reqID)
		if state != nil {
			result.Logs = state.Logs
		}
		if timedOut.Load() {
			result.Error = fmt.Errorf("worker execution timed out (limit: %v)", timeout)
		} else {
			result.Error = fmt.Errorf("invoking worker tail: %w", err)
		}
		return result
	}

	rt.RunMicrotasks()
	deadline := start.Add(timeout)
	if w.eventLoop.HasPending() {
		w.eventLoop.Drain(rt, deadline)
	}

	isPromise, _ := rt.EvalBool("globalThis.__call_result instanceof Promise")
	if isPromise {
		if err := webapi.AwaitValue(rt, "__call_result", deadline, w.eventLoop); err != nil {
			state := core.ClearRequestState(reqID)
			if state != nil {
				result.Logs = state.Logs
			}
			result.Error = fmt.Errorf("awaiting tail handler: %w", err)
			return result
		}
	}

	_ = rt.Eval("delete globalThis.__call_result; delete globalThis.__tail_events;")

	webapi.DrainWaitUntil(rt, deadline)

	state := core.ClearRequestState(reqID)
	if state != nil {
		result.Logs = state.Logs
	}
	return result
}

// ExecuteFunction calls an arbitrary named function on the worker module.
func (e *Engine) ExecuteFunction(siteID string, deployKey string, env *core.Env, fnName string, args ...any) (result *core.WorkerResult) {
	start := time.Now()
	result = &core.WorkerResult{}

	if env == nil {
		result.Error = fmt.Errorf("env must not be nil for site %s", siteID)
		result.Duration = time.Since(start)
		return result
	}

	env.InitRuntime(e, siteID)

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
		w.iso.TerminateExecution()
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
			w.ctx.Close()
			w.iso.Dispose()
			key := poolKey{SiteID: siteID, DeployKey: deployKey}
			if val, ok := e.pools.Load(key); ok {
				sp := val.(*sitePool)
				sp.markInvalid()
			}
		}
	}()

	rt := w.rt

	reqID := core.NewRequestState(e.config.MaxFetchRequests, env)
	if err := rt.SetGlobal("__requestID", strconv.FormatUint(reqID, 10)); err != nil {
		core.ClearRequestState(reqID)
		result.Error = fmt.Errorf("setting request ID: %w", err)
		return result
	}

	if err := webapi.BuildEnvObject(rt, env, reqID); err != nil {
		state := core.ClearRequestState(reqID)
		if state != nil {
			result.Logs = state.Logs
		}
		result.Error = fmt.Errorf("building JS env: %w", err)
		return result
	}

	// Build JS arguments: inject each arg via JSON.parse, stored in globals.
	argsJS := "globalThis.__env"
	for i, arg := range args {
		argJSON, err := json.Marshal(arg)
		if err != nil {
			state := core.ClearRequestState(reqID)
			if state != nil {
				result.Logs = state.Logs
			}
			result.Error = fmt.Errorf("marshaling argument %d: %w", i, err)
			return result
		}
		varName := fmt.Sprintf("__fn_arg_%d", i)
		argScript := fmt.Sprintf(`globalThis.%s = JSON.parse(%q)`, varName, string(argJSON))
		if err := rt.Eval(argScript); err != nil {
			state := core.ClearRequestState(reqID)
			if state != nil {
				result.Logs = state.Logs
			}
			result.Error = fmt.Errorf("creating JS argument %d: %w", i, err)
			return result
		}
		argsJS += fmt.Sprintf(", globalThis.%s", varName)
	}

	callScript := fmt.Sprintf(`
		(function() {
			var mod = globalThis.__worker_module__;
			if (!mod || typeof mod[%q] !== 'function') {
				throw new Error('worker module has no "' + %q + '" function');
			}
			globalThis.__call_result = mod[%q](%s);
		})()
	`, fnName, fnName, fnName, argsJS)

	if _, err := w.ctx.RunScript(callScript, "call_fn.js"); err != nil {
		state := core.ClearRequestState(reqID)
		if state != nil {
			result.Logs = state.Logs
		}
		if timedOut.Load() {
			result.Error = fmt.Errorf("worker execution timed out (limit: %v)", timeout)
		} else {
			result.Error = fmt.Errorf("invoking worker %q: %w", fnName, err)
		}
		return result
	}

	rt.RunMicrotasks()
	deadline := start.Add(timeout)
	if w.eventLoop.HasPending() {
		w.eventLoop.Drain(rt, deadline)
	}

	if err := webapi.AwaitValue(rt, "__call_result", deadline, w.eventLoop); err != nil {
		state := core.ClearRequestState(reqID)
		if state != nil {
			result.Logs = state.Logs
		}
		result.Error = fmt.Errorf("awaiting worker %q: %w", fnName, err)
		return result
	}

	webapi.DrainWaitUntil(rt, deadline)

	jsonStr, err := rt.EvalString(`
		(function() {
			var r = globalThis.__call_result;
			delete globalThis.__call_result;
			if (r === undefined || r === null) return "null";
			return JSON.stringify(r);
		})()
	`)
	if err != nil {
		state := core.ClearRequestState(reqID)
		if state != nil {
			result.Logs = state.Logs
		}
		result.Error = fmt.Errorf("serializing return value: %w", err)
		return result
	}
	result.Data = jsonStr

	for i := range args {
		_ = rt.Eval(fmt.Sprintf("delete globalThis.__fn_arg_%d", i))
	}

	state := core.ClearRequestState(reqID)
	if state != nil {
		result.Logs = state.Logs
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
