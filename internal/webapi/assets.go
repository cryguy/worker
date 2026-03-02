package webapi

import (
	"encoding/json"
	"fmt"

	"github.com/cryguy/worker/v2/internal/core"
	"github.com/cryguy/worker/v2/internal/eventloop"
)

// SetupAssets registers global Go functions for Assets operations.
func SetupAssets(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	// __assets_fetch(reqIDStr, reqJSON) -> JSON response or error
	if err := rt.RegisterFunc("__assets_fetch", func(reqIDStr, reqJSON string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil || state.Env == nil || state.Env.Assets == nil {
			return "", fmt.Errorf("ASSETS not available")
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

		goReq := &core.WorkerRequest{
			URL:     reqData.URL,
			Method:  reqData.Method,
			Headers: reqData.Headers,
		}
		if reqData.Body != nil {
			goReq.Body = []byte(*reqData.Body)
		}

		resp, err := state.Env.Assets.Fetch(goReq)
		if err != nil {
			return "", err
		}

		respJSON := map[string]interface{}{
			"status":  resp.StatusCode,
			"headers": resp.Headers,
			"body":    string(resp.Body),
		}
		data, _ := json.Marshal(respJSON)
		return string(data), nil
	}); err != nil {
		return fmt.Errorf("registering __assets_fetch: %w", err)
	}

	// Define the __makeAssets factory function.
	assetsFactoryJS := `
globalThis.__makeAssets = function() {
	return {
		fetch: function(input) {
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
							var m = input.headers._map;
							for (var hk in m) { if (m.hasOwnProperty(hk)) headers[hk] = Array.isArray(m[hk]) ? m[hk].join(', ') : m[hk]; }
						}
						body = input._bodyStr || null;
					}
					var reqJSON = JSON.stringify({
						url: url,
						method: method,
						headers: headers,
						body: body
					});
					var respStr = __assets_fetch(reqID, reqJSON);
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
	if err := rt.Eval(assetsFactoryJS); err != nil {
		return fmt.Errorf("evaluating Assets factory JS: %w", err)
	}

	return nil
}
