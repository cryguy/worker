package worker

import (
	"encoding/json"
	"fmt"

	"modernc.org/quickjs"
)

// cacheJS defines the Cache and CacheStorage classes available to workers.
const cacheJS = `
(function() {

class Cache {
	constructor(name) {
		this._name = name;
	}

	match(request, options) {
		var url;
		if (typeof request === 'string') {
			url = request;
		} else if (request && request.url) {
			url = request.url;
		} else {
			return Promise.resolve(undefined);
		}

		var reqID = String(globalThis.__requestID);
		var result = __cache_match(reqID, this._name, url);
		if (result === 'null' || result === null || result === undefined) {
			return Promise.resolve(undefined);
		}

		try {
			var parsed = JSON.parse(result);
			var hdrs = new Headers(parsed.headers || {});
			var resp = new Response(parsed.body, {
				status: parsed.status,
				headers: hdrs,
			});
			return Promise.resolve(resp);
		} catch(e) {
			return Promise.resolve(undefined);
		}
	}

	put(request, response) {
		var url;
		if (typeof request === 'string') {
			url = request;
		} else if (request && request.url) {
			url = request.url;
		} else {
			return Promise.reject(new Error('Cache.put requires a request'));
		}

		if (!response) {
			return Promise.reject(new Error('Cache.put requires a response'));
		}

		// Extract Cache-Control max-age for TTL.
		var ttl = -1;
		var cc = '';
		if (response.headers && typeof response.headers.get === 'function') {
			cc = response.headers.get('Cache-Control') || '';
		}
		if (cc) {
			var match = cc.match(/max-age=(\d+)/);
			if (match) {
				ttl = parseInt(match[1], 10);
			}
		}

		// Serialize headers.
		var hdrs = {};
		if (response.headers) {
			if (typeof response.headers.forEach === 'function') {
				response.headers.forEach(function(v, k) { hdrs[k] = v; });
			} else if (response.headers._map) {
				var m = response.headers._map;
				for (var k in m) { if (m.hasOwnProperty(k)) hdrs[k] = String(m[k]); }
			}
		}

		var body = '';
		if (response._body !== null && response._body !== undefined) {
			body = String(response._body);
		}

		var reqID = String(globalThis.__requestID);
		__cache_put(
			reqID,
			this._name,
			url,
			response.status || 200,
			JSON.stringify(hdrs),
			body,
			ttl
		);

		return Promise.resolve(undefined);
	}

	delete(request, options) {
		var url;
		if (typeof request === 'string') {
			url = request;
		} else if (request && request.url) {
			url = request.url;
		} else {
			return Promise.resolve(false);
		}

		var reqID = String(globalThis.__requestID);
		var result = __cache_delete(reqID, this._name, url);
		return Promise.resolve(result === 'true' || result === true);
	}
}

class CacheStorage {
	constructor() {
		this._caches = {};
		this.default = new Cache('default');
	}

	open(name) {
		if (!this._caches[name]) {
			this._caches[name] = new Cache(name);
		}
		return Promise.resolve(this._caches[name]);
	}
}

globalThis.caches = new CacheStorage();

})();
`

// setupCache registers the Cache API JS classes and Go-backed functions.
func setupCache(vm *quickjs.VM, el *eventLoop) error {
	// __cache_match(reqIDStr, cacheName, url) -> JSON string or "null"
	err := registerGoFunc(vm, "__cache_match", func(reqIDStr, cacheName, url string) (string, error) {
		reqID := parseReqID(reqIDStr)
		state := getRequestState(reqID)
		if state == nil || state.env == nil || state.env.Cache == nil {
			return "null", nil
		}

		entry, err := state.env.Cache.Match(cacheName, url)
		if err != nil || entry == nil {
			return "null", nil
		}

		var headers map[string]string
		if entry.Headers != "" {
			_ = json.Unmarshal([]byte(entry.Headers), &headers)
		}
		if headers == nil {
			headers = make(map[string]string)
		}

		result := map[string]interface{}{
			"status":  entry.Status,
			"headers": headers,
			"body":    string(entry.Body),
		}
		data, _ := json.Marshal(result)
		return string(data), nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __cache_match: %w", err)
	}

	// __cache_put(reqIDStr, cacheName, url, status, headersJSON, body, ttl)
	err = registerGoFunc(vm, "__cache_put", func(reqIDStr, cacheName, url string, status int, headersJSON, body string, ttl int) (string, error) {
		reqID := parseReqID(reqIDStr)
		state := getRequestState(reqID)
		if state == nil || state.env == nil || state.env.Cache == nil {
			return "", nil
		}

		var ttlPtr *int
		if ttl > 0 {
			ttlPtr = &ttl
		}

		_ = state.env.Cache.Put(cacheName, url, status, headersJSON, []byte(body), ttlPtr)
		return "", nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __cache_put: %w", err)
	}

	// __cache_delete(reqIDStr, cacheName, url) -> "true" or "false"
	err = registerGoFunc(vm, "__cache_delete", func(reqIDStr, cacheName, url string) (string, error) {
		reqID := parseReqID(reqIDStr)
		state := getRequestState(reqID)
		if state == nil || state.env == nil || state.env.Cache == nil {
			return "false", nil
		}

		deleted, err := state.env.Cache.Delete(cacheName, url)
		if err != nil || !deleted {
			return "false", nil
		}
		return "true", nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __cache_delete: %w", err)
	}

	// Evaluate JS classes
	if err := evalDiscard(vm, cacheJS); err != nil {
		return fmt.Errorf("evaluating cache.js: %w", err)
	}

	return nil
}
