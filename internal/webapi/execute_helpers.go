package webapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cryguy/worker/v2/internal/core"
)

// GoRequestToJS converts a Go WorkerRequest into a JS Request object
// stored in globalThis.__req.
func GoRequestToJS(rt core.JSRuntime, req *core.WorkerRequest) error {
	lowerHeaders := make(map[string]string, len(req.Headers))
	for k, v := range req.Headers {
		lowerHeaders[strings.ToLower(k)] = v
	}
	headersJSON, _ := json.Marshal(lowerHeaders)

	_ = rt.SetGlobal("__tmp_url", req.URL)
	_ = rt.SetGlobal("__tmp_method", req.Method)
	_ = rt.SetGlobal("__tmp_headers_json", string(headersJSON))

	var bodyScript string
	if len(req.Body) > 0 {
		_ = rt.SetGlobal("__tmp_body", string(req.Body))
		bodyScript = "init.body = globalThis.__tmp_body;"
	}

	script := fmt.Sprintf(`(function() {
		var init = {
			method: globalThis.__tmp_method,
			headers: JSON.parse(globalThis.__tmp_headers_json),
		};
		%s
		globalThis.__req = new Request(globalThis.__tmp_url, init);
		delete globalThis.__tmp_url;
		delete globalThis.__tmp_method;
		delete globalThis.__tmp_headers_json;
		delete globalThis.__tmp_body;
	})()`, bodyScript)

	return rt.Eval(script)
}

// JsResponseToGo extracts a Go WorkerResponse from the JS Response
// in globalThis.__result.
func JsResponseToGo(rt core.JSRuntime) (*core.WorkerResponse, error) {
	// Set a temporary flag so JS knows the Go side supports binary transfer.
	// The mode tells JS which buffer type to create: "sab" or "ab".
	if bt, ok := rt.(core.BinaryTransferer); ok {
		_ = rt.SetGlobal("__tmp_binary_mode", bt.BinaryMode())
	}

	resultJSON, err := rt.EvalString(`(function() {
		var r = globalThis.__result;
		delete globalThis.__result;
		if (r === null || r === undefined) return JSON.stringify({error: "null response"});
		var headers = {};
		if (r.headers && r.headers._map) {
			var m = r.headers._map;
			for (var k in m) {
				if (m.hasOwnProperty(k)) headers[k] = Array.isArray(m[k]) ? m[k].join(', ') : m[k];
			}
		}
		var hasWebSocket = !!(r.webSocket);
		if (hasWebSocket) {
			globalThis.__ws_check_resp = r.webSocket;
		}
		var body = '';
		var bodyType = 'string';
		var _bm = globalThis.__tmp_binary_mode || '';
		if (_bm) delete globalThis.__tmp_binary_mode;
		if (r._body !== null && r._body !== undefined) {
			if (r._body instanceof ReadableStream) {
				var _q = r._body._queue;
				var _allBytes = [];
				for (var _i = 0; _i < _q.length; _i++) {
					var _chunk = _q[_i];
					if (typeof _chunk === 'string') {
						var _enc = new TextEncoder();
						var _bytes = _enc.encode(_chunk);
						for (var _k = 0; _k < _bytes.length; _k++) _allBytes.push(_bytes[_k]);
					} else if (_chunk instanceof Uint8Array || ArrayBuffer.isView(_chunk)) {
						var _arr = new Uint8Array(_chunk.buffer || _chunk, _chunk.byteOffset || 0, _chunk.byteLength || _chunk.length);
						for (var _j = 0; _j < _arr.length; _j++) _allBytes.push(_arr[_j]);
					} else if (_chunk instanceof ArrayBuffer) {
						var _arr2 = new Uint8Array(_chunk);
						for (var _j2 = 0; _j2 < _arr2.length; _j2++) _allBytes.push(_arr2[_j2]);
					} else {
						var _s = String(_chunk);
						for (var _j3 = 0; _j3 < _s.length; _j3++) _allBytes.push(_s.charCodeAt(_j3) & 0xFF);
					}
				}
				r._body._queue = [];
				if (_allBytes.length > 0) {
					var _src = new Uint8Array(_allBytes);
					if (_bm) {
						var _buf = (_bm === 'sab') ? new SharedArrayBuffer(_src.byteLength) : new ArrayBuffer(_src.byteLength);
						new Uint8Array(_buf).set(_src);
						globalThis.__tmp_resp_sab = _buf;
						bodyType = 'binary';
					} else {
						body = __bufferSourceToB64(_src);
						bodyType = 'base64';
					}
				}
			} else if (r._body instanceof ArrayBuffer || ArrayBuffer.isView(r._body)) {
				var _src2 = (r._body instanceof ArrayBuffer)
					? new Uint8Array(r._body)
					: new Uint8Array(r._body.buffer, r._body.byteOffset, r._body.byteLength);
				if (_bm) {
					var _buf2 = (_bm === 'sab') ? new SharedArrayBuffer(_src2.byteLength) : new ArrayBuffer(_src2.byteLength);
					new Uint8Array(_buf2).set(_src2);
					globalThis.__tmp_resp_sab = _buf2;
					bodyType = 'binary';
				} else {
					body = __bufferSourceToB64(_src2);
					bodyType = 'base64';
				}
			} else {
				body = String(r._body);
			}
		}
		return JSON.stringify({
			status: r.status || 200,
			headers: headers,
			body: body,
			bodyType: bodyType,
			hasWebSocket: hasWebSocket,
		});
	})()`)
	if err != nil {
		return nil, fmt.Errorf("extracting response: %w", err)
	}

	var resp struct {
		Status       int               `json:"status"`
		Headers      map[string]string `json:"headers"`
		Body         string            `json:"body"`
		BodyType     string            `json:"bodyType"`
		HasWebSocket bool              `json:"hasWebSocket"`
		Error        string            `json:"error"`
	}
	if err := json.Unmarshal([]byte(resultJSON), &resp); err != nil {
		return nil, fmt.Errorf("parsing response JSON: %w", err)
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("worker returned %s instead of Response", resp.Error)
	}

	var body []byte
	switch resp.BodyType {
	case "binary":
		if bt, ok := rt.(core.BinaryTransferer); ok {
			body, err = bt.ReadBinaryFromJS("__tmp_resp_sab")
			if err != nil {
				return nil, fmt.Errorf("reading binary response body: %w", err)
			}
		} else {
			return nil, fmt.Errorf("binary response body requires BinaryTransferer runtime")
		}
	case "base64":
		if resp.Body != "" {
			body, err = base64.StdEncoding.DecodeString(resp.Body)
			if err != nil {
				return nil, fmt.Errorf("decoding base64 body: %w", err)
			}
		}
	default:
		if resp.Body != "" {
			body = []byte(resp.Body)
		}
	}

	return &core.WorkerResponse{
		StatusCode:   resp.Status,
		Headers:      resp.Headers,
		Body:         body,
		HasWebSocket: resp.HasWebSocket,
	}, nil
}

// BuildEnvObject creates the globalThis.__env object with vars, secrets,
// and binding namespaces (KV, R2, D1, DO, Queues, Service Bindings, Assets).
func BuildEnvObject(rt core.JSRuntime, env *core.Env, reqID uint64) error {
	if err := rt.Eval("globalThis.__env = {};"); err != nil {
		return fmt.Errorf("creating env object: %w", err)
	}

	// Add plain vars.
	if env.Vars != nil {
		for k, v := range env.Vars {
			js := fmt.Sprintf("globalThis.__env[%s] = %s;", core.JsEscape(k), core.JsEscape(v))
			if err := rt.Eval(js); err != nil {
				return fmt.Errorf("setting var %q: %w", k, err)
			}
		}
	}

	// Add secrets.
	if env.Secrets != nil {
		for k, v := range env.Secrets {
			js := fmt.Sprintf("globalThis.__env[%s] = %s;", core.JsEscape(k), core.JsEscape(v))
			if err := rt.Eval(js); err != nil {
				return fmt.Errorf("setting secret %q: %w", k, err)
			}
		}
	}

	// Add KV namespace bindings.
	if env.KV != nil {
		for name := range env.KV {
			js := fmt.Sprintf("globalThis.__env[%s] = globalThis.__makeKV(%s);",
				core.JsEscape(name), core.JsEscape(name))
			if err := rt.Eval(js); err != nil {
				return fmt.Errorf("setting KV binding %q: %w", name, err)
			}
		}
	}

	// Add R2 bucket bindings.
	if env.Storage != nil {
		for name := range env.Storage {
			js := fmt.Sprintf("globalThis.__env[%s] = globalThis.__makeR2(%s);",
				core.JsEscape(name), core.JsEscape(name))
			if err := rt.Eval(js); err != nil {
				return fmt.Errorf("setting R2 binding %q: %w", name, err)
			}
		}
	}

	// Populate env.D1 from D1Bindings if not already set.
	if env.D1Bindings != nil && env.D1 == nil {
		env.D1 = make(map[string]core.D1Store, len(env.D1Bindings))
	}
	if env.D1Bindings != nil {
		for name, dbID := range env.D1Bindings {
			if _, ok := env.D1[name]; !ok {
				var bridge *D1Bridge
				var err error
				if env.D1DataDir != "" {
					bridge, err = OpenD1Database(env.D1DataDir, dbID)
				} else {
					bridge, err = NewD1BridgeMemory(dbID)
				}
				if err != nil {
					return fmt.Errorf("opening D1 database %q: %w", name, err)
				}
				env.D1[name] = bridge
			}
		}
	}

	// Add D1 database bindings.
	if env.D1 != nil {
		for name := range env.D1 {
			js := fmt.Sprintf("globalThis.__env[%s] = globalThis.__makeD1(%s);",
				core.JsEscape(name), core.JsEscape(name))
			if err := rt.Eval(js); err != nil {
				return fmt.Errorf("setting D1 binding %q: %w", name, err)
			}
		}
	}

	// Add Durable Object namespace bindings.
	if env.DurableObjects != nil {
		for name := range env.DurableObjects {
			js := fmt.Sprintf("globalThis.__env[%s] = globalThis.__makeDO(%s);",
				core.JsEscape(name), core.JsEscape(name))
			if err := rt.Eval(js); err != nil {
				return fmt.Errorf("setting DO binding %q: %w", name, err)
			}
		}
	}

	// Add Queue bindings.
	if env.Queues != nil {
		for name := range env.Queues {
			js := fmt.Sprintf("globalThis.__env[%s] = globalThis.__makeQueue(%s);",
				core.JsEscape(name), core.JsEscape(name))
			if err := rt.Eval(js); err != nil {
				return fmt.Errorf("setting queue binding %q: %w", name, err)
			}
		}
	}

	// Add Service Bindings.
	if env.ServiceBindings != nil {
		for name := range env.ServiceBindings {
			js := fmt.Sprintf("globalThis.__env[%s] = globalThis.__makeSB(%s);",
				core.JsEscape(name), core.JsEscape(name))
			if err := rt.Eval(js); err != nil {
				return fmt.Errorf("setting service binding %q: %w", name, err)
			}
		}
	}

	// Add Assets binding.
	if env.Assets != nil {
		if err := rt.Eval("globalThis.__env.ASSETS = globalThis.__makeAssets();"); err != nil {
			return fmt.Errorf("setting assets binding: %w", err)
		}
	}

	// Add custom bindings.
	if env.CustomBindings != nil {
		for name, bindingFn := range env.CustomBindings {
			val, err := bindingFn(rt)
			if err != nil {
				return fmt.Errorf("custom binding %q: %w", name, err)
			}
			// Binding funcs may set globalThis.__tmp_custom_val directly via Eval
			// (returning nil). Only call SetGlobal if they returned a non-nil value.
			if val != nil {
				if err := rt.SetGlobal("__tmp_custom_val", val); err != nil {
					return fmt.Errorf("setting custom binding %q: %w", name, err)
				}
			}
			js := fmt.Sprintf("globalThis.__env[%s] = globalThis.__tmp_custom_val; delete globalThis.__tmp_custom_val;", core.JsEscape(name))
			if err := rt.Eval(js); err != nil {
				return fmt.Errorf("assigning custom binding %q: %w", name, err)
			}
		}
	}

	return nil
}

// BuildExecContext creates the globalThis.__ctx execution context with
// waitUntil() and passThroughOnException().
func BuildExecContext(rt core.JSRuntime) error {
	return rt.Eval(`
		globalThis.__waitUntilPromises = [];
		globalThis.__ctx = {
			waitUntil: function(promise) {
				globalThis.__waitUntilPromises.push(Promise.resolve(promise));
			},
			passThroughOnException: function() {}
		};
	`)
}
