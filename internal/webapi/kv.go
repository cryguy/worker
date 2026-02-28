package webapi

import (
	"encoding/json"
	"fmt"

	"github.com/cryguy/worker/v2/internal/core"
	"github.com/cryguy/worker/v2/internal/eventloop"
)

// SetupKV registers global Go functions for KV namespace operations.
// The actual KV binding objects are built in JS via buildEnvObject.
func SetupKV(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	// __kv_get(reqIDStr, bindingName, key, valType) -> JSON string or "null"
	if err := rt.RegisterFunc("__kv_get", func(reqIDStr, bindingName, key, valType string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil || state.Env == nil || state.Env.KV == nil {
			return "null", nil
		}
		store, ok := state.Env.KV[bindingName]
		if !ok {
			return "null", nil
		}

		val, err := store.Get(key)
		if err != nil {
			return "", err
		}
		if val == nil {
			return "null", nil
		}

		result := map[string]interface{}{"value": *val}
		data, _ := json.Marshal(result)
		return string(data), nil
	}); err != nil {
		return fmt.Errorf("registering __kv_get: %w", err)
	}

	// __kv_get_with_metadata(reqIDStr, bindingName, key, valType) -> JSON string
	if err := rt.RegisterFunc("__kv_get_with_metadata", func(reqIDStr, bindingName, key, valType string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil || state.Env == nil || state.Env.KV == nil {
			return `{"value":null,"metadata":null}`, nil
		}
		store, ok := state.Env.KV[bindingName]
		if !ok {
			return `{"value":null,"metadata":null}`, nil
		}

		result, err := store.GetWithMetadata(key)
		if err != nil {
			return "", err
		}
		if result == nil {
			return `{"value":null,"metadata":null}`, nil
		}

		response := map[string]interface{}{
			"value":    result.Value,
			"metadata": result.Metadata,
		}
		data, _ := json.Marshal(response)
		return string(data), nil
	}); err != nil {
		return fmt.Errorf("registering __kv_get_with_metadata: %w", err)
	}

	// __kv_put(reqIDStr, bindingName, key, value, optsJSON) -> "" or error
	if err := rt.RegisterFunc("__kv_put", func(reqIDStr, bindingName, key, value, optsJSON string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil || state.Env == nil || state.Env.KV == nil {
			return "", fmt.Errorf("KV not available")
		}
		store, ok := state.Env.KV[bindingName]
		if !ok {
			return "", fmt.Errorf("KV binding %q not found", bindingName)
		}

		var metadata *string
		var ttl *int
		if optsJSON != "" && optsJSON != "{}" {
			var opts struct {
				Metadata      *string `json:"metadata"`
				ExpirationTtl *int    `json:"expirationTtl"`
			}
			if err := json.Unmarshal([]byte(optsJSON), &opts); err == nil {
				metadata = opts.Metadata
				ttl = opts.ExpirationTtl
			}
		}

		if err := store.Put(key, value, metadata, ttl); err != nil {
			return "", err
		}
		return "", nil
	}); err != nil {
		return fmt.Errorf("registering __kv_put: %w", err)
	}

	// __kv_delete(reqIDStr, bindingName, key) -> "" or error
	if err := rt.RegisterFunc("__kv_delete", func(reqIDStr, bindingName, key string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil || state.Env == nil || state.Env.KV == nil {
			return "", fmt.Errorf("KV not available")
		}
		store, ok := state.Env.KV[bindingName]
		if !ok {
			return "", fmt.Errorf("KV binding %q not found", bindingName)
		}

		if err := store.Delete(key); err != nil {
			return "", err
		}
		return "", nil
	}); err != nil {
		return fmt.Errorf("registering __kv_delete: %w", err)
	}

	// __kv_list(reqIDStr, bindingName, optsJSON) -> JSON result
	if err := rt.RegisterFunc("__kv_list", func(reqIDStr, bindingName, optsJSON string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil || state.Env == nil || state.Env.KV == nil {
			return "", fmt.Errorf("KV not available")
		}
		store, ok := state.Env.KV[bindingName]
		if !ok {
			return "", fmt.Errorf("KV binding %q not found", bindingName)
		}

		var prefix string
		var cursor string
		limit := 1000
		if optsJSON != "" && optsJSON != "{}" {
			var opts struct {
				Prefix string `json:"prefix"`
				Limit  int    `json:"limit"`
				Cursor string `json:"cursor"`
			}
			if err := json.Unmarshal([]byte(optsJSON), &opts); err == nil {
				prefix = opts.Prefix
				limit = opts.Limit
				cursor = opts.Cursor
			}
		}

		listResult, err := store.List(prefix, limit, cursor)
		if err != nil {
			return "", err
		}

		result := map[string]interface{}{
			"keys":          listResult.Keys,
			"list_complete": listResult.ListComplete,
		}
		if listResult.Cursor != "" {
			result["cursor"] = listResult.Cursor
		}

		data, _ := json.Marshal(result)
		return string(data), nil
	}); err != nil {
		return fmt.Errorf("registering __kv_list: %w", err)
	}

	// Define the __makeKV factory function.
	kvFactoryJS := `
globalThis.__makeKV = function(bindingName) {
	return {
		get: function(key, opts) {
			if (arguments.length === 0) {
				return Promise.reject(new Error("get requires at least 1 argument"));
			}
			var type = (opts && opts.type) || "text";
			var reqID = String(globalThis.__requestID);
			var resultStr = __kv_get(reqID, bindingName, String(key), type);
			return new Promise(function(resolve, reject) {
				try {
					if (resultStr === "null") {
						resolve(null);
						return;
					}
					var result = JSON.parse(resultStr);
					var val = result.value;
					if (type === "json") {
						resolve(JSON.parse(val));
					} else if (type === "arrayBuffer") {
						var enc = new TextEncoder();
						resolve(enc.encode(val).buffer);
					} else if (type === "stream") {
						var enc = new TextEncoder();
						var bytes = enc.encode(val);
						resolve(new ReadableStream({
							start: function(controller) {
								controller.enqueue(bytes);
								controller.close();
							}
						}));
					} else {
						resolve(val);
					}
				} catch(e) {
					reject(e);
				}
			});
		},
		getWithMetadata: function(key, opts) {
			var type = (opts && opts.type) || "text";
			var reqID = String(globalThis.__requestID);
			var resultStr = __kv_get_with_metadata(reqID, bindingName, String(key), type);
			return new Promise(function(resolve, reject) {
				try {
					var result = JSON.parse(resultStr);
					if (result.value === null) {
						resolve({value: null, metadata: null});
						return;
					}
					var val = result.value;
					var processedVal = val;
					if (type === "json") {
						processedVal = JSON.parse(val);
					} else if (type === "arrayBuffer") {
						var enc = new TextEncoder();
						processedVal = enc.encode(val).buffer;
					} else if (type === "stream") {
						var enc = new TextEncoder();
						var bytes = enc.encode(val);
						processedVal = new ReadableStream({
							start: function(controller) {
								controller.enqueue(bytes);
								controller.close();
							}
						});
					}
					var metadata = result.metadata;
					if (typeof metadata === "string") {
						try { metadata = JSON.parse(metadata); } catch(e) {}
					}
					resolve({value: processedVal, metadata: metadata});
				} catch(e) {
					reject(e);
				}
			});
		},
		put: function(key, value, opts) {
			if (arguments.length < 2) {
				return Promise.reject(new Error("put requires at least 2 arguments"));
			}
			var reqID = String(globalThis.__requestID);
			var valueStr = typeof value === "string" ? value : JSON.stringify(value);
			var optsJSON = opts ? JSON.stringify({
				metadata: opts.metadata ? JSON.stringify(opts.metadata) : null,
				expirationTtl: opts.expirationTtl || null
			}) : "{}";
			return new Promise(function(resolve, reject) {
				try {
					var err = __kv_put(reqID, bindingName, String(key), valueStr, optsJSON);
					if (err) {
						reject(new Error(err));
					} else {
						resolve();
					}
				} catch(e) {
					reject(e);
				}
			});
		},
		delete: function(key) {
			if (arguments.length === 0) {
				return Promise.reject(new Error("delete requires at least 1 argument"));
			}
			var reqID = String(globalThis.__requestID);
			return new Promise(function(resolve, reject) {
				try {
					var err = __kv_delete(reqID, bindingName, String(key));
					if (err) {
						reject(new Error(err));
					} else {
						resolve();
					}
				} catch(e) {
					reject(e);
				}
			});
		},
		list: function(opts) {
			var reqID = String(globalThis.__requestID);
			var optsJSON = opts ? JSON.stringify({
				prefix: opts.prefix || "",
				limit: opts.limit || 1000,
				cursor: opts.cursor || ""
			}) : "{}";
			return new Promise(function(resolve, reject) {
				try {
					var resultStr = __kv_list(reqID, bindingName, optsJSON);
					resolve(JSON.parse(resultStr));
				} catch(e) {
					reject(e);
				}
			});
		}
	};
};
`
	if err := rt.Eval(kvFactoryJS); err != nil {
		return fmt.Errorf("evaluating KV factory JS: %w", err)
	}

	return nil
}
