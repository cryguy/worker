package webapi

import (
	"encoding/json"
	"fmt"

	"github.com/cryguy/worker/internal/core"
	"github.com/cryguy/worker/internal/eventloop"
)

// SetupServiceBindings registers global Go functions for service binding operations.
func SetupServiceBindings(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	// __sb_fetch(reqIDStr, bindingName, reqJSON) -> JSON response or error
	if err := rt.RegisterFunc("__sb_fetch", func(reqIDStr, bindingName, reqJSON string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil || state.Env == nil || state.Env.ServiceBindings == nil || state.Env.Dispatcher == nil {
			return "", fmt.Errorf("ServiceBindings not available")
		}
		config, ok := state.Env.ServiceBindings[bindingName]
		if !ok {
			return "", fmt.Errorf("ServiceBinding %q not found", bindingName)
		}

		var reqData struct {
			URL     string            `json:"url"`
			Method  string            `json:"method"`
			Headers map[string]string `json:"headers"`
			Body    *string           `json:"body"`
		}
		if err := json.Unmarshal([]byte(reqJSON), &reqData); err != nil {
			return "", fmt.Errorf("invalid request JSON: %w", err)
		}

		workerReq := &core.WorkerRequest{
			Method:  reqData.Method,
			URL:     reqData.URL,
			Headers: reqData.Headers,
		}
		if reqData.Body != nil {
			workerReq.Body = []byte(*reqData.Body)
		}

		// Provide a minimal env for the target worker. The target must never
		// receive the caller's env (secret isolation), but Execute requires
		// a non-nil Env.
		targetEnv := &core.Env{
			Vars:    make(map[string]string),
			Secrets: make(map[string]string),
		}

		result := state.Env.Dispatcher.Execute(config.TargetSiteID, config.TargetDeployKey, targetEnv, workerReq)
		if result.Error != nil {
			return "", result.Error
		}
		if result.Response == nil {
			return "", fmt.Errorf("target worker returned no response")
		}

		// Build response JSON.
		respJSON := map[string]interface{}{
			"status":  result.Response.StatusCode,
			"headers": result.Response.Headers,
			"body":    string(result.Response.Body),
		}
		data, _ := json.Marshal(respJSON)
		return string(data), nil
	}); err != nil {
		return fmt.Errorf("registering __sb_fetch: %w", err)
	}

	// Define the __makeSB factory function.
	sbFactoryJS := `
globalThis.__makeSB = function(bindingName) {
	return {
		fetch: function(input, init) {
			if (arguments.length === 0) {
				return Promise.reject(new Error('fetch() requires at least one argument'));
			}
			var reqID = String(globalThis.__requestID);
			return new Promise(function(resolve, reject) {
				try {
					var url = '', method = 'GET', headers = {}, body = null;
					if (typeof input === 'string') {
						url = input;
					} else if (input && typeof input === 'object') {
						url = input.url || '';
						method = input.method || 'GET';
						if (input.headers && input.headers._map) {
							for (var k in input.headers._map) headers[k] = input.headers._map[k];
						}
						body = input._body || null;
					}
					if (init) {
						if (init.method) method = init.method;
						if (init.headers) {
							if (init.headers._map) {
								for (var k in init.headers._map) headers[k] = init.headers._map[k];
							} else if (typeof init.headers === 'object') {
								for (var k in init.headers) headers[k] = init.headers[k];
							}
						}
						if (init.body !== undefined) body = String(init.body);
					}
					var reqJSON = JSON.stringify({
						url: url || 'https://fake-host/',
						method: method,
						headers: headers,
						body: body
					});
					var respStr = __sb_fetch(reqID, bindingName, reqJSON);
					var respData = JSON.parse(respStr);
					var h = new Headers();
					if (respData.headers) {
						for (var k in respData.headers) h.set(k, respData.headers[k]);
					}
					resolve(new Response(respData.body, { status: respData.status, headers: h }));
				} catch(e) {
					reject(e);
				}
			});
		}
	};
};
`
	if err := rt.Eval(sbFactoryJS); err != nil {
		return fmt.Errorf("evaluating ServiceBinding factory JS: %w", err)
	}

	return nil
}
