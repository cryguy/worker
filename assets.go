package worker

import (
	"encoding/json"
	"fmt"
	"mime"
	"path/filepath"
	"strings"
	"time"

	v8 "github.com/tommie/v8go"
)

// contentType guesses the MIME type from the file extension.
func contentType(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	if ext == "" {
		return "application/octet-stream"
	}
	ct := mime.TypeByExtension(ext)
	if ct == "" {
		return "application/octet-stream"
	}
	return ct
}

// buildAssetsBinding creates a JS object with a fetch(request) method
// that delegates to the given AssetsFetcher. This is synchronous because
// Fetch is local file I/O.
func buildAssetsBinding(iso *v8.Isolate, ctx *v8.Context, fetcher AssetsFetcher) (*v8.Value, error) {
	assets, err := newJSObject(iso, ctx)
	if err != nil {
		return nil, fmt.Errorf("creating assets object: %w", err)
	}

	_ = assets.Set("fetch", v8.NewFunctionTemplate(iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
		args := info.Args()
		if len(args) == 0 {
			return throwError(iso, "ASSETS.fetch requires a request argument")
		}

		// Extract request data via JS.
		_ = ctx.Global().Set("__tmp_assets_req", args[0])
		result, err := ctx.RunScript(`(function() {
			var r = globalThis.__tmp_assets_req;
			delete globalThis.__tmp_assets_req;
			var headers = {};
			if (r.headers && r.headers._map) {
				var m = r.headers._map;
				for (var k in m) { if (m.hasOwnProperty(k)) headers[k] = String(m[k]); }
			}
			var body = r._body != null ? String(r._body) : null;
			return JSON.stringify({url: r.url || '', method: r.method || 'GET', headers: headers, body: body});
		})()`, "assets_extract_req.js")
		if err != nil {
			return throwError(iso, fmt.Sprintf("ASSETS.fetch: extracting request: %s", err.Error()))
		}

		var reqData struct {
			URL     string            `json:"url"`
			Method  string            `json:"method"`
			Headers map[string]string `json:"headers"`
			Body    *string           `json:"body"`
		}
		if err := json.Unmarshal([]byte(result.String()), &reqData); err != nil {
			return throwError(iso, fmt.Sprintf("ASSETS.fetch: parsing request: %s", err.Error()))
		}

		goReq := &WorkerRequest{
			URL:     reqData.URL,
			Method:  reqData.Method,
			Headers: reqData.Headers,
		}
		if reqData.Body != nil {
			goReq.Body = []byte(*reqData.Body)
		}

		resp, err := fetcher.Fetch(goReq)
		if err != nil {
			return throwError(iso, err.Error())
		}

		// Build Response via JS constructor.
		headersJSON, _ := json.Marshal(resp.Headers)
		if resp.Body != nil {
			bodyVal, _ := v8.NewValue(iso, string(resp.Body))
			_ = ctx.Global().Set("__tmp_assets_body", bodyVal)
		} else {
			_ = ctx.Global().Set("__tmp_assets_body", v8.Null(iso))
		}
		statusVal, _ := v8.NewValue(iso, int32(resp.StatusCode))
		_ = ctx.Global().Set("__tmp_assets_status", statusVal)
		hdrsVal, _ := v8.NewValue(iso, string(headersJSON))
		_ = ctx.Global().Set("__tmp_assets_headers", hdrsVal)

		jsResp, err := ctx.RunScript(`(function() {
			var body = globalThis.__tmp_assets_body;
			var status = globalThis.__tmp_assets_status;
			var hdrs = JSON.parse(globalThis.__tmp_assets_headers);
			delete globalThis.__tmp_assets_body;
			delete globalThis.__tmp_assets_status;
			delete globalThis.__tmp_assets_headers;
			return new Response(body, {status: status, headers: hdrs});
		})()`, "assets_build_resp.js")
		if err != nil {
			return throwError(iso, fmt.Sprintf("ASSETS.fetch: creating Response: %s", err.Error()))
		}

		return jsResp
	}).GetFunction(ctx))

	return assets.Value, nil
}

// buildEnvObject creates the full env JS object passed to the worker's
// fetch handler as the second argument.
func buildEnvObject(iso *v8.Isolate, ctx *v8.Context, env *Env, reqID uint64) (*v8.Value, error) {
	envObj, err := newJSObject(iso, ctx)
	if err != nil {
		return nil, fmt.Errorf("creating env object: %w", err)
	}

	// Plain environment variables.
	if env.Vars != nil {
		for k, v := range env.Vars {
			val, _ := v8.NewValue(iso, v)
			_ = envObj.Set(k, val)
		}
	}

	// Secrets (same shape, just from a different source).
	if env.Secrets != nil {
		for k, v := range env.Secrets {
			val, _ := v8.NewValue(iso, v)
			_ = envObj.Set(k, val)
		}
	}

	// KV namespace bindings.
	if env.KV != nil {
		for name, store := range env.KV {
			kvVal, err := buildKVBinding(iso, ctx, store)
			if err != nil {
				return nil, fmt.Errorf("building KV binding %q: %w", name, err)
			}
			_ = envObj.Set(name, kvVal)
		}
	}

	// Storage bucket bindings (R2-compatible).
	if env.Storage != nil {
		for name, store := range env.Storage {
			storVal, err := buildStorageBinding(iso, ctx, store)
			if err != nil {
				return nil, fmt.Errorf("building storage binding %q: %w", name, err)
			}
			_ = envObj.Set(name, storVal)
		}
	}

	// Queue bindings.
	if env.Queues != nil {
		for name, sender := range env.Queues {
			qVal, err := buildQueueBinding(iso, ctx, sender)
			if err != nil {
				return nil, fmt.Errorf("building queue binding %q: %w", name, err)
			}
			_ = envObj.Set(name, qVal)
		}
	}

	// Service bindings (worker-to-worker RPC).
	if env.ServiceBindings != nil && env.Dispatcher != nil {
		bridge := &ServiceBindingBridge{Dispatcher: env.Dispatcher, Env: env}
		for name, config := range env.ServiceBindings {
			sbVal, err := buildServiceBinding(iso, ctx, bridge, config)
			if err != nil {
				return nil, fmt.Errorf("building service binding %q: %w", name, err)
			}
			_ = envObj.Set(name, sbVal)
		}
	}

	// D1 database bindings — each gets its own isolated SQLite database.
	var d1Bridges []*D1Bridge
	closeD1Bridges := func() {
		for _, b := range d1Bridges {
			_ = b.Close()
		}
	}
	if env.D1Bindings != nil {
		for name, dbID := range env.D1Bindings {
			var bridge *D1Bridge
			var err error
			if env.D1DataDir != "" {
				bridge, err = OpenD1Database(env.D1DataDir, dbID)
			} else {
				// Fallback to in-memory for tests or when no data dir is configured.
				bridge, err = NewD1BridgeMemory(dbID)
			}
			if err != nil {
				closeD1Bridges()
				return nil, fmt.Errorf("opening D1 database %q: %w", name, err)
			}
			d1Bridges = append(d1Bridges, bridge)
			d1Val, err := buildD1Binding(iso, ctx, bridge)
			if err != nil {
				closeD1Bridges()
				return nil, fmt.Errorf("building D1 binding %q: %w", name, err)
			}
			_ = envObj.Set(name, d1Val)
		}
		// Store bridges in request state for cleanup by clearRequestState.
		state := getRequestState(reqID)
		if state != nil {
			state.d1Bridges = append(state.d1Bridges, d1Bridges...)
		}
	}

	// Durable Object bindings.
	if env.DurableObjects != nil {
		for name, store := range env.DurableObjects {
			doVal, err := buildDurableObjectBinding(iso, ctx, store, name)
			if err != nil {
				closeD1Bridges()
				return nil, fmt.Errorf("building durable object binding %q: %w", name, err)
			}
			_ = envObj.Set(name, doVal)
		}
	}

	// ASSETS binding.
	if env.Assets != nil {
		assetsVal, err := buildAssetsBinding(iso, ctx, env.Assets)
		if err != nil {
			closeD1Bridges()
			return nil, fmt.Errorf("building assets binding: %w", err)
		}
		_ = envObj.Set("ASSETS", assetsVal)
	}

	return envObj.Value, nil
}

// buildExecContext creates the ctx JS object (third argument to fetch handler).
// waitUntil(promise) collects promises into globalThis.__waitUntilPromises
// which are drained after the response is returned.
func buildExecContext(iso *v8.Isolate, ctx *v8.Context) (*v8.Value, error) {
	execCtx, err := newJSObject(iso, ctx)
	if err != nil {
		return nil, fmt.Errorf("creating exec context: %w", err)
	}

	// Initialize the promises array for this request.
	if _, err := ctx.RunScript("globalThis.__waitUntilPromises = [];", "waituntil_init.js"); err != nil {
		return nil, fmt.Errorf("initializing waitUntil array: %w", err)
	}

	waitUntilFT := v8.NewFunctionTemplate(iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
		args := info.Args()
		if len(args) > 0 {
			_ = ctx.Global().Set("__tmp_wu_promise", args[0])
			_, _ = ctx.RunScript("globalThis.__waitUntilPromises.push(Promise.resolve(globalThis.__tmp_wu_promise)); delete globalThis.__tmp_wu_promise;", "waituntil_push.js")
		}
		return v8.Undefined(iso)
	})
	_ = execCtx.Set("waitUntil", waitUntilFT.GetFunction(ctx))

	passThroughFT := v8.NewFunctionTemplate(iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
		return v8.Undefined(iso)
	})
	_ = execCtx.Set("passThroughOnException", passThroughFT.GetFunction(ctx))

	return execCtx.Value, nil
}

// drainWaitUntil awaits all promises collected by ctx.waitUntil().
// It runs Promise.allSettled on the array so that rejections don't break
// the response. Must be called on the isolate's goroutine.
func drainWaitUntil(ctx *v8.Context, deadline time.Time) {
	drainScript := `(async function() {
		var promises = globalThis.__waitUntilPromises || [];
		globalThis.__waitUntilPromises = [];
		if (promises.length > 0) {
			await Promise.allSettled(promises);
		}
	})()`
	wuVal, err := ctx.RunScript(drainScript, "waituntil_drain.js")
	if err != nil {
		return
	}
	if wuVal != nil && wuVal.IsPromise() {
		_, _ = awaitValue(ctx, wuVal, deadline)
	}
}
