package worker

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"modernc.org/quickjs"
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

// setupGlobals registers structuredClone, performance.now(), navigator,
// queueMicrotask, and the Event/EventTarget base classes.
func setupGlobals(vm *quickjs.VM, _ *eventLoop) error {
	// __sendBeacon: Go-backed fire-and-forget POST with SSRF protection.
	registerGoFunc(vm, "__sendBeacon", func(targetURL, body, contentType string) (int, error) {
		if isPrivateHostname(targetURL) {
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
	}, false)

	// __performanceNow: Go-backed high-resolution timer.
	startTime := time.Now()
	registerGoFunc(vm, "__performanceNow", func() float64 {
		return float64(time.Since(startTime).Nanoseconds()) / 1e6
	}, false)

	// Evaluate pure-JS polyfills.
	if err := evalDiscard(vm, globalsJS); err != nil {
		return fmt.Errorf("evaluating globals.js: %w", err)
	}

	// Set up performance object with Go-backed now().
	if err := evalDiscard(vm, `
		globalThis.performance = {
			now: function() { return __performanceNow(); }
		};
	`); err != nil {
		return fmt.Errorf("setting up performance: %w", err)
	}

	// Set up waitUntil tracking.
	return evalDiscard(vm, waitUntilJS)
}

// reportErrorJS defines ErrorEvent and reportError.
const reportErrorJS = `
class ErrorEvent extends Event {
	constructor(type, init) {
		super(type);
		this.error = init && init.error !== undefined ? init.error : null;
		this.message = (init && init.message) || '';
		this.filename = (init && init.filename) || '';
		this.lineno = (init && init.lineno) || 0;
		this.colno = (init && init.colno) || 0;
	}
}
globalThis.ErrorEvent = ErrorEvent;
globalThis.reportError = function(error) {
	var msg = '';
	if (error !== null && error !== undefined) {
		msg = error.message !== undefined ? error.message : String(error);
	}
	var ev = new ErrorEvent('error', { error: error, message: msg });
	globalThis.dispatchEvent(ev);
};
`

// setupReportError evaluates the reportError/ErrorEvent polyfill.
func setupReportError(vm *quickjs.VM, _ *eventLoop) error {
	if err := evalDiscard(vm, `
		if (typeof globalThis.addEventListener !== 'function') {
			var __gt = new EventTarget();
			globalThis.addEventListener = __gt.addEventListener.bind(__gt);
			globalThis.removeEventListener = __gt.removeEventListener.bind(__gt);
			globalThis.dispatchEvent = __gt.dispatchEvent.bind(__gt);
			globalThis._listeners = __gt._listeners;
		}
	`); err != nil {
		return fmt.Errorf("setting up globalThis as EventTarget: %w", err)
	}
	return evalDiscard(vm, reportErrorJS)
}

// buildExecContext constructs the JS execution context {waitUntil, passThroughOnException}.
// Sets the result as globalThis.__ctx.
func buildExecContext(vm *quickjs.VM) error {
	return evalDiscard(vm, `
		globalThis.__waitUntilPromises = [];
		globalThis.__ctx = {
			waitUntil: function(promise) {
				globalThis.__waitUntilPromises.push(Promise.resolve(promise));
			},
			passThroughOnException: function() {}
		};
	`)
}

// drainWaitUntil drains any promises registered via ctx.waitUntil().
func drainWaitUntil(vm *quickjs.VM, deadline time.Time) {
	// Set up await for all waitUntil promises.
	_ = evalDiscard(vm, `
		if (globalThis.__waitUntilPromises && globalThis.__waitUntilPromises.length > 0) {
			globalThis.__waitUntilSettled = false;
			Promise.allSettled(globalThis.__waitUntilPromises).then(function() {
				globalThis.__waitUntilSettled = true;
			});
			globalThis.__waitUntilPromises = [];
		} else {
			globalThis.__waitUntilSettled = true;
		}
	`)

	// Pump microtasks until settled or deadline.
	for {
		settled, _ := evalBool(vm, "!!globalThis.__waitUntilSettled")
		if settled {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		executePendingJobs(vm)
		time.Sleep(1 * time.Millisecond)
	}

	_ = evalDiscard(vm, "delete globalThis.__waitUntilSettled;")
}

// errMissingArg returns a formatted error for functions called with too few arguments.
func errMissingArg(name string, required int) error {
	return fmt.Errorf("%s requires at least %d argument(s)", name, required)
}

// errInvalidArg returns a formatted error for invalid argument values.
func errInvalidArg(name, reason string) error {
	return fmt.Errorf("%s: %s", name, reason)
}
