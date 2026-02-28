package webapi

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cryguy/worker/v2/internal/core"
	"github.com/cryguy/worker/v2/internal/eventloop"
)

// globalsJS defines pure-JS polyfills for simple global APIs.
const globalsJS = `
globalThis.structuredClone = (function() {
	var TYPED_ARRAY_CONSTRUCTORS = [
		typeof Uint8Array !== 'undefined' && Uint8Array,
		typeof Int8Array !== 'undefined' && Int8Array,
		typeof Uint8ClampedArray !== 'undefined' && Uint8ClampedArray,
		typeof Int16Array !== 'undefined' && Int16Array,
		typeof Uint16Array !== 'undefined' && Uint16Array,
		typeof Int32Array !== 'undefined' && Int32Array,
		typeof Uint32Array !== 'undefined' && Uint32Array,
		typeof Float32Array !== 'undefined' && Float32Array,
		typeof Float64Array !== 'undefined' && Float64Array,
		typeof BigInt64Array !== 'undefined' && BigInt64Array,
		typeof BigUint64Array !== 'undefined' && BigUint64Array,
	].filter(Boolean);

	function cloneError(msg) {
		return new DOMException(msg, 'DataCloneError');
	}

	function deepClone(value, seen) {
		if (value === undefined) throw cloneError('value could not be cloned');
		if (value === null) return null;

		var type = typeof value;
		if (type === 'boolean' || type === 'number' || type === 'string' || type === 'bigint') return value;
		if (type === 'function') throw cloneError('value could not be cloned');
		if (type === 'symbol') throw cloneError('value could not be cloned');

		if (typeof WeakMap !== 'undefined' && value instanceof WeakMap) throw cloneError('WeakMap cannot be cloned');
		if (typeof WeakSet !== 'undefined' && value instanceof WeakSet) throw cloneError('WeakSet cannot be cloned');
		if (typeof Promise !== 'undefined' && value instanceof Promise) throw cloneError('Promise cannot be cloned');

		if (seen.has(value)) throw cloneError('value could not be cloned: circular reference');
		seen.set(value, true);

		if (value instanceof Date) return new Date(value.getTime());
		if (value instanceof RegExp) return new RegExp(value.source, value.flags);
		if (value instanceof ArrayBuffer) { return value.slice(0); }

		for (var ti = 0; ti < TYPED_ARRAY_CONSTRUCTORS.length; ti++) {
			var TA = TYPED_ARRAY_CONSTRUCTORS[ti];
			if (value instanceof TA) {
				var clonedBuf = value.buffer.slice(value.byteOffset, value.byteOffset + value.byteLength);
				return new TA(clonedBuf);
			}
		}

		if (typeof DataView !== 'undefined' && value instanceof DataView) {
			var dvBuf = value.buffer.slice(value.byteOffset, value.byteOffset + value.byteLength);
			return new DataView(dvBuf);
		}

		if (typeof Map !== 'undefined' && value instanceof Map) {
			var clonedMap = new Map();
			value.forEach(function(v, k) {
				clonedMap.set(deepClone(k, seen), deepClone(v, seen));
			});
			return clonedMap;
		}

		if (typeof Set !== 'undefined' && value instanceof Set) {
			var clonedSet = new Set();
			value.forEach(function(v) {
				clonedSet.add(deepClone(v, seen));
			});
			return clonedSet;
		}

		if (Array.isArray(value)) {
			var arr = new Array(value.length);
			for (var i = 0; i < value.length; i++) {
				arr[i] = deepClone(value[i], seen);
			}
			return arr;
		}

		var result = {};
		var keys = Object.keys(value);
		for (var j = 0; j < keys.length; j++) {
			result[keys[j]] = deepClone(value[keys[j]], seen);
		}
		return result;
	}

	return function structuredClone(value) {
		return deepClone(value, new WeakMap());
	};
})();

globalThis.queueMicrotask = function(fn) {
	Promise.resolve().then(fn);
};

Object.defineProperty(globalThis, 'navigator', {
	value: {
		userAgent: "hostedat-worker/1.0",
		scheduling: { isInputPending: function() { return false; } },
		sendBeacon: function(url, data) {
			var body = '';
			var ct = 'text/plain;charset=UTF-8';
			if (data !== undefined && data !== null) {
				body = String(data);
			}
			try {
				return !!__sendBeacon(url, body, ct);
			} catch(e) {
				return false;
			}
		},
	},
	writable: true,
	configurable: true,
});
`

// waitUntilJS provides ctx.waitUntil support and the drainWaitUntil mechanism.
const waitUntilJS = `
globalThis.__waitUntilPromises = [];
`

// SetupGlobals registers structuredClone, performance.now(), navigator,
// queueMicrotask, and the Event/EventTarget base classes.
func SetupGlobals(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	// __sendBeacon: Go-backed fire-and-forget POST with SSRF protection.
	if err := rt.RegisterFunc("__sendBeacon", func(targetURL, body, contentType string) (int, error) {
		if IsPrivateHostname(targetURL) {
			return 0, nil
		}

		go func() {
			httpReq, err := http.NewRequest("POST", targetURL, strings.NewReader(body))
			if err != nil {
				return
			}
			httpReq.Header.Set("Content-Type", contentType)
			client := &http.Client{
				Timeout: 10 * time.Second,
				Transport: &http.Transport{
					DialContext: ssrfSafeDialContext,
				},
			}
			resp, err := client.Do(httpReq)
			if err != nil {
				return
			}
			_ = resp.Body.Close()
		}()

		return 1, nil
	}); err != nil {
		return err
	}

	// __performanceNow: Go-backed high-resolution timer.
	startTime := time.Now()
	if err := rt.RegisterFunc("__performanceNow", func() float64 {
		return float64(time.Since(startTime).Nanoseconds()) / 1e6
	}); err != nil {
		return err
	}

	// Evaluate pure-JS polyfills.
	if err := rt.Eval(globalsJS); err != nil {
		return fmt.Errorf("evaluating globals.js: %w", err)
	}

	// Set up performance object with Go-backed now().
	if err := rt.Eval(`
		globalThis.performance = {
			now: function() { return __performanceNow(); }
		};
	`); err != nil {
		return fmt.Errorf("setting up performance: %w", err)
	}

	// Set up waitUntil tracking.
	return rt.Eval(waitUntilJS)
}

// ErrMissingArg returns a formatted error for functions called with too few arguments.
func ErrMissingArg(name string, required int) error {
	return fmt.Errorf("%s requires at least %d argument(s)", name, required)
}

// ErrInvalidArg returns a formatted error for invalid argument values.
func ErrInvalidArg(name, reason string) error {
	return fmt.Errorf("%s: %s", name, reason)
}
