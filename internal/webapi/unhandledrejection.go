package webapi

import (
	"fmt"
	"runtime"
	"time"

	"github.com/cryguy/worker/internal/core"
	"github.com/cryguy/worker/internal/eventloop"
)

// unhandledRejectionJS provides a best-effort polyfill for unhandled promise
// rejection tracking.
const unhandledRejectionJS = `
(function() {

class PromiseRejectionEvent extends Event {
	constructor(type, init) {
		super(type, init);
		this.promise = (init && init.promise) || null;
		this.reason = (init && init.reason) !== undefined ? init.reason : undefined;
	}
}

const _pendingRejections = new Map();
let _rejectionId = 0;

const _origPromise = globalThis.Promise;

const _origThen = _origPromise.prototype.then;

_origPromise.prototype.then = function(onFulfilled, onRejected) {
	const result = _origThen.call(this, onFulfilled, onRejected);
	if (typeof onRejected === 'function' && this.__rejectionId !== undefined) {
		_pendingRejections.delete(this.__rejectionId);
	}
	return result;
};

const _origCatch = _origPromise.prototype.catch;
_origPromise.prototype.catch = function(onRejected) {
	const result = _origCatch.call(this, onRejected);
	if (typeof onRejected === 'function' && this.__rejectionId !== undefined) {
		_pendingRejections.delete(this.__rejectionId);
	}
	return result;
};

globalThis.__trackRejection = function(promise, reason) {
	const id = ++_rejectionId;
	try {
		Object.defineProperty(promise, '__rejectionId', { value: id, writable: true, configurable: true });
	} catch(e) {
		return;
	}
	_pendingRejections.set(id, { promise, reason });
	queueMicrotask(function() {
		if (_pendingRejections.has(id)) {
			_pendingRejections.delete(id);
			const event = new PromiseRejectionEvent('unhandledrejection', {
				promise: promise,
				reason: reason,
				cancelable: true,
			});
			globalThis.dispatchEvent(event);
		}
	});
};

if (typeof globalThis.addEventListener !== 'function') {
	const et = new EventTarget();
	globalThis.addEventListener = et.addEventListener.bind(et);
	globalThis.removeEventListener = et.removeEventListener.bind(et);
	globalThis.dispatchEvent = et.dispatchEvent.bind(et);
}

globalThis.PromiseRejectionEvent = PromiseRejectionEvent;

})();
`

// SetupUnhandledRejection registers PromiseRejectionEvent and best-effort
// unhandled rejection tracking on globalThis.
func SetupUnhandledRejection(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	if err := rt.Eval(unhandledRejectionJS); err != nil {
		return fmt.Errorf("evaluating unhandledrejection.js: %w", err)
	}
	return nil
}

// DrainWaitUntil drains any promises registered via ctx.waitUntil().
func DrainWaitUntil(rt core.JSRuntime, deadline time.Time) {
	_ = rt.Eval(`
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

	for {
		settled, _ := rt.EvalBool("!!globalThis.__waitUntilSettled")
		if settled {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		rt.RunMicrotasks()
		time.Sleep(1 * time.Millisecond)
	}

	_ = rt.Eval("delete globalThis.__waitUntilSettled;")
}

// AwaitValue resolves a potentially-promise value stored in a global variable
// by pumping the microtask queue. The global variable is updated in-place
// with the resolved value. Optionally drains the event loop between pumps.
func AwaitValue(rt core.JSRuntime, globalVar string, deadline time.Time, el *eventloop.EventLoop) error {
	// Check if the value is a Promise.
	isPromise, err := rt.EvalBool(fmt.Sprintf("globalThis.%s instanceof Promise", globalVar))
	if err != nil || !isPromise {
		return nil
	}

	// Set up Promise.then() to capture the resolved/rejected value.
	setupJS := fmt.Sprintf(`
		delete globalThis.__awaited_result;
		delete globalThis.__awaited_state;
		Promise.resolve(globalThis.%s).then(
			function(r) { globalThis.__awaited_result = r; globalThis.__awaited_state = 'fulfilled'; },
			function(e) { globalThis.__awaited_result = e; globalThis.__awaited_state = 'rejected'; }
		);
	`, globalVar)
	if err := rt.Eval(setupJS); err != nil {
		return fmt.Errorf("setting up promise await: %w", err)
	}

	// Pump microtasks (and optionally the event loop) until the promise settles.
	for {
		rt.RunMicrotasks()

		if el != nil && el.HasPending() {
			shortDeadline := time.Now().Add(10 * time.Millisecond)
			if shortDeadline.After(deadline) {
				shortDeadline = deadline
			}
			el.Drain(rt, shortDeadline)
			rt.RunMicrotasks()
		}

		stateStr, err := rt.EvalString("String(globalThis.__awaited_state)")
		if err != nil {
			return fmt.Errorf("checking promise state: %w", err)
		}
		if stateStr != "undefined" {
			break
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("promise resolution timed out")
		}
		runtime.Gosched()
	}

	stateStr, _ := rt.EvalString("String(globalThis.__awaited_state)")

	if stateStr == "rejected" {
		errMsg, _ := rt.EvalString("String(globalThis.__awaited_result)")
		_ = rt.Eval("delete globalThis.__awaited_result; delete globalThis.__awaited_state;")
		return fmt.Errorf("promise rejected: %s", errMsg)
	}

	// Move the resolved value back to the original global.
	_ = rt.Eval(fmt.Sprintf(
		"globalThis.%s = globalThis.__awaited_result; delete globalThis.__awaited_result; delete globalThis.__awaited_state;",
		globalVar))

	return nil
}
