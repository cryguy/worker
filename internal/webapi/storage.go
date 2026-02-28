package webapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cryguy/worker/v2/internal/core"
	"github.com/cryguy/worker/v2/internal/eventloop"
)

const maxObjectSize = 128 * 1024 * 1024 // 128 MB

// SetupStorage registers global Go functions for R2 storage operations.
func SetupStorage(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	// __r2_get(reqIDStr, bindingName, key) -> JSON or "null"
	if err := rt.RegisterFunc("__r2_get", func(reqIDStr, bindingName, key string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil || state.Env == nil || state.Env.Storage == nil {
			return "null", nil
		}
		store, ok := state.Env.Storage[bindingName]
		if !ok {
			return "null", nil
		}

		data, r2obj, err := store.Get(key)
		if err != nil || r2obj == nil {
			return "null", nil
		}

		if r2obj.Size > int64(maxObjectSize) {
			return "", fmt.Errorf("object too large: %d bytes (max %d)", r2obj.Size, maxObjectSize)
		}

		bodyB64 := base64.StdEncoding.EncodeToString(data)
		result := map[string]interface{}{
			"key":            r2obj.Key,
			"size":           r2obj.Size,
			"etag":           r2obj.ETag,
			"contentType":    r2obj.ContentType,
			"customMetadata": r2obj.CustomMetadata,
			"bodyB64":        bodyB64,
			"uploaded":       r2obj.LastModified.UnixMilli(),
		}
		resultJSON, _ := json.Marshal(result)
		return string(resultJSON), nil
	}); err != nil {
		return fmt.Errorf("registering __r2_get: %w", err)
	}

	// __r2_put(reqIDStr, bindingName, key, bodyB64, optsJSON) -> JSON R2Object or error
	if err := rt.RegisterFunc("__r2_put", func(reqIDStr, bindingName, key, bodyB64, optsJSON string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil || state.Env == nil || state.Env.Storage == nil {
			return "", fmt.Errorf("Storage not available")
		}
		store, ok := state.Env.Storage[bindingName]
		if !ok {
			return "", fmt.Errorf("Storage binding %q not found", bindingName)
		}

		valueBytes, err := base64.StdEncoding.DecodeString(bodyB64)
		if err != nil {
			return "", fmt.Errorf("invalid base64 body: %w", err)
		}

		putOpts := core.R2PutOptions{}
		if optsJSON != "" && optsJSON != "{}" {
			var parsed struct {
				HTTPMetadata struct {
					ContentType string `json:"contentType"`
				} `json:"httpMetadata"`
				CustomMetadata map[string]string `json:"customMetadata"`
			}
			if err := json.Unmarshal([]byte(optsJSON), &parsed); err == nil {
				if parsed.HTTPMetadata.ContentType != "" {
					putOpts.ContentType = parsed.HTTPMetadata.ContentType
				}
				putOpts.CustomMetadata = parsed.CustomMetadata
			}
		}

		r2obj, err := store.Put(key, valueBytes, putOpts)
		if err != nil {
			return "", err
		}

		result := map[string]interface{}{
			"key":            key,
			"size":           r2obj.Size,
			"etag":           r2obj.ETag,
			"contentType":    r2obj.ContentType,
			"customMetadata": r2obj.CustomMetadata,
		}
		data, _ := json.Marshal(result)
		return string(data), nil
	}); err != nil {
		return fmt.Errorf("registering __r2_put: %w", err)
	}

	// __r2_delete(reqIDStr, bindingName, keysJSON) -> "" or error
	if err := rt.RegisterFunc("__r2_delete", func(reqIDStr, bindingName, keysJSON string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil || state.Env == nil || state.Env.Storage == nil {
			return "", fmt.Errorf("Storage not available")
		}
		store, ok := state.Env.Storage[bindingName]
		if !ok {
			return "", fmt.Errorf("Storage binding %q not found", bindingName)
		}

		var keys []string
		if err := json.Unmarshal([]byte(keysJSON), &keys); err != nil {
			return "", fmt.Errorf("invalid keys JSON: %w", err)
		}

		// R2 delete is best-effort: ignore error.
		_ = store.Delete(keys)
		return "", nil
	}); err != nil {
		return fmt.Errorf("registering __r2_delete: %w", err)
	}

	// __r2_head(reqIDStr, bindingName, key) -> JSON R2Object or "null"
	if err := rt.RegisterFunc("__r2_head", func(reqIDStr, bindingName, key string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil || state.Env == nil || state.Env.Storage == nil {
			return "null", nil
		}
		store, ok := state.Env.Storage[bindingName]
		if !ok {
			return "null", nil
		}

		r2obj, err := store.Head(key)
		if err != nil || r2obj == nil {
			return "null", nil
		}

		result := map[string]interface{}{
			"key":            key,
			"size":           r2obj.Size,
			"etag":           r2obj.ETag,
			"contentType":    r2obj.ContentType,
			"customMetadata": r2obj.CustomMetadata,
			"uploaded":       r2obj.LastModified.UnixMilli(),
		}
		data, _ := json.Marshal(result)
		return string(data), nil
	}); err != nil {
		return fmt.Errorf("registering __r2_head: %w", err)
	}

	// __r2_list(reqIDStr, bindingName, optsJSON) -> JSON result
	if err := rt.RegisterFunc("__r2_list", func(reqIDStr, bindingName, optsJSON string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil || state.Env == nil || state.Env.Storage == nil {
			return "", fmt.Errorf("Storage not available")
		}
		store, ok := state.Env.Storage[bindingName]
		if !ok {
			return "", fmt.Errorf("Storage binding %q not found", bindingName)
		}

		listOpts := core.R2ListOptions{Limit: 1000}
		if optsJSON != "" && optsJSON != "{}" {
			var opts struct {
				Prefix    string `json:"prefix"`
				Cursor    string `json:"cursor"`
				Delimiter string `json:"delimiter"`
				Limit     int    `json:"limit"`
			}
			if err := json.Unmarshal([]byte(optsJSON), &opts); err == nil {
				listOpts.Prefix = opts.Prefix
				listOpts.Cursor = opts.Cursor
				listOpts.Delimiter = opts.Delimiter
				listOpts.Limit = opts.Limit
			}
		}

		listResult, err := store.List(listOpts)
		if err != nil {
			// Return empty result on error (matches R2 behavior).
			listResult = &core.R2ListResult{}
		}

		var objects []map[string]interface{}
		for _, obj := range listResult.Objects {
			objects = append(objects, map[string]interface{}{
				"key":      obj.Key,
				"size":     obj.Size,
				"etag":     obj.ETag,
				"uploaded": obj.LastModified.UnixMilli(),
			})
		}

		result := map[string]interface{}{
			"objects":           objects,
			"truncated":         listResult.Truncated,
			"cursor":            listResult.Cursor,
			"delimitedPrefixes": listResult.DelimitedPrefixes,
		}
		data, _ := json.Marshal(result)
		return string(data), nil
	}); err != nil {
		return fmt.Errorf("registering __r2_list: %w", err)
	}

	// __r2_presigned_url(reqIDStr, bindingName, key, expiresIn) -> URL string
	if err := rt.RegisterFunc("__r2_presigned_url", func(reqIDStr, bindingName, key string, expiresIn int) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil || state.Env == nil || state.Env.Storage == nil {
			return "", fmt.Errorf("Storage not available")
		}
		store, ok := state.Env.Storage[bindingName]
		if !ok {
			return "", fmt.Errorf("Storage binding %q not found", bindingName)
		}

		if expiresIn < 1 {
			expiresIn = 1
		}
		if expiresIn > 604800 {
			expiresIn = 604800 // cap at 7 days
		}

		urlStr, err := store.PresignedGetURL(key, time.Duration(expiresIn)*time.Second)
		if err != nil {
			return "", err
		}
		return urlStr, nil
	}); err != nil {
		return fmt.Errorf("registering __r2_presigned_url: %w", err)
	}

	// __r2_public_url(reqIDStr, bindingName, key) -> URL string
	if err := rt.RegisterFunc("__r2_public_url", func(reqIDStr, bindingName, key string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil || state.Env == nil || state.Env.Storage == nil {
			return "", fmt.Errorf("Storage not available")
		}
		store, ok := state.Env.Storage[bindingName]
		if !ok {
			return "", fmt.Errorf("Storage binding %q not found", bindingName)
		}

		urlStr, err := store.PublicURL(key)
		if err != nil {
			return "", err
		}
		return urlStr, nil
	}); err != nil {
		return fmt.Errorf("registering __r2_public_url: %w", err)
	}

	// Register binary transfer functions when available and set mode flag.
	if bt, ok := rt.(core.BinaryTransferer); ok {
		_ = rt.SetGlobal("__binary_mode", bt.BinaryMode())
		// __r2_get_sab: like __r2_get but writes body via SharedArrayBuffer instead of base64.
		if err := rt.RegisterFunc("__r2_get_sab", func(reqIDStr, bindingName, key string) (string, error) {
			reqID := core.ParseReqID(reqIDStr)
			state := core.GetRequestState(reqID)
			if state == nil || state.Env == nil || state.Env.Storage == nil {
				return "null", nil
			}
			store, ok := state.Env.Storage[bindingName]
			if !ok {
				return "null", nil
			}

			data, r2obj, err := store.Get(key)
			if err != nil || r2obj == nil {
				return "null", nil
			}

			if r2obj.Size > int64(maxObjectSize) {
				return "", fmt.Errorf("object too large: %d bytes (max %d)", r2obj.Size, maxObjectSize)
			}

			if err := bt.WriteBinaryToJS("__tmp_r2_body", data); err != nil {
				return "", fmt.Errorf("writing binary to JS: %w", err)
			}

			result := map[string]interface{}{
				"key":            r2obj.Key,
				"size":           r2obj.Size,
				"etag":           r2obj.ETag,
				"contentType":    r2obj.ContentType,
				"customMetadata": r2obj.CustomMetadata,
				"uploaded":       r2obj.LastModified.UnixMilli(),
			}
			resultJSON, _ := json.Marshal(result)
			return string(resultJSON), nil
		}); err != nil {
			return fmt.Errorf("registering __r2_get_sab: %w", err)
		}

		// __r2_put_sab: like __r2_put but reads body via SharedArrayBuffer instead of base64.
		if err := rt.RegisterFunc("__r2_put_sab", func(reqIDStr, bindingName, key, optsJSON string) (string, error) {
			reqID := core.ParseReqID(reqIDStr)
			state := core.GetRequestState(reqID)
			if state == nil || state.Env == nil || state.Env.Storage == nil {
				return "", fmt.Errorf("Storage not available")
			}
			store, ok := state.Env.Storage[bindingName]
			if !ok {
				return "", fmt.Errorf("Storage binding %q not found", bindingName)
			}

			valueBytes, err := bt.ReadBinaryFromJS("__tmp_r2_put_sab")
			if err != nil {
				return "", fmt.Errorf("reading binary from JS: %w", err)
			}

			putOpts := core.R2PutOptions{}
			if optsJSON != "" && optsJSON != "{}" {
				var parsed struct {
					HTTPMetadata struct {
						ContentType string `json:"contentType"`
					} `json:"httpMetadata"`
					CustomMetadata map[string]string `json:"customMetadata"`
				}
				if err := json.Unmarshal([]byte(optsJSON), &parsed); err == nil {
					if parsed.HTTPMetadata.ContentType != "" {
						putOpts.ContentType = parsed.HTTPMetadata.ContentType
					}
					putOpts.CustomMetadata = parsed.CustomMetadata
				}
			}

			r2obj, err := store.Put(key, valueBytes, putOpts)
			if err != nil {
				return "", err
			}

			result := map[string]interface{}{
				"key":            key,
				"size":           r2obj.Size,
				"etag":           r2obj.ETag,
				"contentType":    r2obj.ContentType,
				"customMetadata": r2obj.CustomMetadata,
			}
			data, _ := json.Marshal(result)
			return string(data), nil
		}); err != nil {
			return fmt.Errorf("registering __r2_put_sab: %w", err)
		}
	}

	// Define the __makeR2 factory function.
	r2FactoryJS := `
globalThis.__makeR2 = function(bindingName) {
	return {
		get: function(key) {
			var reqID = String(globalThis.__requestID);
			return new Promise(function(resolve, reject) {
				try {
					var resultStr, obj, bodyBytes;
					if (typeof __r2_get_sab === 'function') {
						resultStr = __r2_get_sab(reqID, bindingName, String(key));
						if (resultStr === "null") { resolve(null); return; }
						obj = JSON.parse(resultStr);
						bodyBytes = new Uint8Array(globalThis.__tmp_r2_body);
						delete globalThis.__tmp_r2_body;
					} else {
						resultStr = __r2_get(reqID, bindingName, String(key));
						if (resultStr === "null") { resolve(null); return; }
						obj = JSON.parse(resultStr);
						bodyBytes = Uint8Array.from(atob(obj.bodyB64), function(c) { return c.charCodeAt(0); });
					}
					resolve({
						key: obj.key, size: obj.size, etag: obj.etag,
						httpEtag: '"' + obj.etag + '"', version: obj.etag,
						httpMetadata: { contentType: obj.contentType || null },
						customMetadata: obj.customMetadata || {},
						uploaded: new Date(obj.uploaded),
						text: function() { return Promise.resolve(new TextDecoder().decode(bodyBytes)); },
						arrayBuffer: function() { return Promise.resolve(bodyBytes.buffer); },
						json: function() { return Promise.resolve(JSON.parse(new TextDecoder().decode(bodyBytes))); },
						bodyUsed: false
					});
				} catch(e) { reject(e); }
			});
		},
		put: function(key, value, opts) {
			var reqID = String(globalThis.__requestID);
			return new Promise(function(resolve, reject) {
				try {
					var bytes;
					if (typeof value === "string") { bytes = new TextEncoder().encode(value); }
					else if (value instanceof ArrayBuffer) { bytes = new Uint8Array(value); }
					else if (ArrayBuffer.isView(value)) { bytes = new Uint8Array(value.buffer, value.byteOffset, value.byteLength); }
					else { reject(new Error("unsupported body type")); return; }
					var optsJSON = opts ? JSON.stringify({
						httpMetadata: { contentType: (opts.httpMetadata && opts.httpMetadata.contentType) || null },
						customMetadata: opts.customMetadata || {}
					}) : "{}";
					var resultStr;
					if (typeof __r2_put_sab === 'function') {
						var _bm = globalThis.__binary_mode || 'sab';
						var _buf = (_bm === 'sab') ? new SharedArrayBuffer(bytes.byteLength) : new ArrayBuffer(bytes.byteLength);
						new Uint8Array(_buf).set(bytes);
						globalThis.__tmp_r2_put_sab = _buf;
						resultStr = __r2_put_sab(reqID, bindingName, String(key), optsJSON);
					} else {
						var _parts = [];
						for (var _ci = 0; _ci < bytes.length; _ci += 8192) {
							_parts.push(String.fromCharCode.apply(null, bytes.subarray(_ci, Math.min(_ci + 8192, bytes.length))));
						}
						var bodyB64 = btoa(_parts.join(''));
						resultStr = __r2_put(reqID, bindingName, String(key), bodyB64, optsJSON);
					}
					var obj = JSON.parse(resultStr);
					resolve({
						key: obj.key, size: obj.size, etag: obj.etag,
						httpEtag: '"' + obj.etag + '"', version: obj.etag,
						httpMetadata: { contentType: obj.contentType || null },
						customMetadata: obj.customMetadata || {}
					});
				} catch(e) { reject(e); }
			});
		},
		delete: function(keys) {
			var reqID = String(globalThis.__requestID);
			var keysArray = Array.isArray(keys) ? keys : [String(keys)];
			return new Promise(function(resolve, reject) {
				try { __r2_delete(reqID, bindingName, JSON.stringify(keysArray)); resolve(); }
				catch(e) { reject(e); }
			});
		},
		head: function(key) {
			var reqID = String(globalThis.__requestID);
			var resultStr = __r2_head(reqID, bindingName, String(key));
			return new Promise(function(resolve, reject) {
				try {
					if (resultStr === "null") { resolve(null); return; }
					var obj = JSON.parse(resultStr);
					resolve({
						key: obj.key, size: obj.size, etag: obj.etag,
						httpEtag: '"' + obj.etag + '"', version: obj.etag,
						httpMetadata: { contentType: obj.contentType || null },
						customMetadata: obj.customMetadata || {},
						uploaded: new Date(obj.uploaded)
					});
				} catch(e) { reject(e); }
			});
		},
		list: function(opts) {
			var reqID = String(globalThis.__requestID);
			var optsJSON = opts ? JSON.stringify({
				prefix: opts.prefix || "", cursor: opts.cursor || "",
				delimiter: opts.delimiter || "", limit: opts.limit || 1000
			}) : "{}";
			return new Promise(function(resolve, reject) {
				try {
					var resultStr = __r2_list(reqID, bindingName, optsJSON);
					var result = JSON.parse(resultStr);
					resolve({
						objects: (result.objects || []).map(function(o) {
							return { key: o.key, size: o.size, etag: o.etag,
								httpEtag: '"' + o.etag + '"', uploaded: new Date(o.uploaded) };
						}),
						truncated: result.truncated, cursor: result.cursor,
						delimitedPrefixes: result.delimitedPrefixes || []
					});
				} catch(e) { reject(e); }
			});
		},
		createSignedUrl: function(key, opts) {
			var reqID = String(globalThis.__requestID);
			var expiresIn = (opts && opts.expiresIn) || 3600;
			return new Promise(function(resolve, reject) {
				try { var url = __r2_presigned_url(reqID, bindingName, String(key), expiresIn); resolve(url); }
				catch(e) { reject(e); }
			});
		},
		publicUrl: function(key) {
			var reqID = String(globalThis.__requestID);
			return __r2_public_url(reqID, bindingName, String(key));
		}
	};
};
`
	if err := rt.Eval(r2FactoryJS); err != nil {
		return fmt.Errorf("evaluating R2 factory JS: %w", err)
	}

	return nil
}
