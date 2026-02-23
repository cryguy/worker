package worker

import (
	"time"

	"modernc.org/quickjs"
)

// timersJS is the JavaScript polyfill for setTimeout/setInterval/clearTimeout/clearInterval.
// It stores callbacks in globalThis.__timerCallbacks and delegates scheduling to Go via
// __timerRegister/__timerClear registered functions.
const timersJS = `
(function() {
	globalThis.__timerCallbacks = {};
	globalThis.setTimeout = function(fn, delay) {
		if (arguments.length === 0 || typeof fn !== 'function') {
			return 0;
		}
		var args = [];
		for (var i = 2; i < arguments.length; i++) args.push(arguments[i]);
		var id = __timerRegister(delay || 0, false);
		globalThis.__timerCallbacks[id] = { fn: fn, args: args };
		return id;
	};
	globalThis.setInterval = function(fn, interval) {
		if (arguments.length === 0 || typeof fn !== 'function') {
			return 0;
		}
		var args = [];
		for (var i = 2; i < arguments.length; i++) args.push(arguments[i]);
		var id = __timerRegister(interval || 0, true);
		globalThis.__timerCallbacks[id] = { fn: fn, args: args, interval: true };
		return id;
	};
	globalThis.clearTimeout = globalThis.clearInterval = function(id) {
		if (arguments.length === 0 || typeof id !== 'number') {
			return;
		}
		__timerClear(id);
		delete globalThis.__timerCallbacks[id];
	};
})();
`

// setupTimers registers Go-backed setTimeout/setInterval/clearTimeout/clearInterval.
// Timer callbacks are stored on the JS side in __timerCallbacks; Go only tracks
// scheduling metadata (delay, interval, deadline). Callbacks fire during
// eventLoop.drain() which is called by Engine.Execute after the handler returns.
func setupTimers(vm *quickjs.VM, el *eventLoop) error {
	// __timerRegister(delayMs, isInterval) -> timerID
	registerGoFunc(vm, "__timerRegister", func(delayMs int, isInterval bool) int {
		delay := time.Duration(delayMs) * time.Millisecond
		return el.registerTimer(delay, isInterval)
	}, false)

	// __timerClear(id)
	registerGoFunc(vm, "__timerClear", func(id int) {
		el.clearTimer(id)
	}, false)

	// Install the JS polyfill that wraps these Go functions.
	return evalDiscard(vm, timersJS)
}
