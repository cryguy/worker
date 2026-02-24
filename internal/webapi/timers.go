package webapi

import (
	"time"

	"github.com/cryguy/worker/internal/core"
	"github.com/cryguy/worker/internal/eventloop"
)

// timersJS is the JavaScript polyfill for setTimeout/setInterval/clearTimeout/clearInterval.
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

// SetupTimers registers Go-backed setTimeout/setInterval/clearTimeout/clearInterval.
func SetupTimers(rt core.JSRuntime, el *eventloop.EventLoop) error {
	if err := rt.RegisterFunc("__timerRegister", func(delayMs int, isInterval bool) int {
		delay := time.Duration(delayMs) * time.Millisecond
		return el.RegisterTimer(delay, isInterval)
	}); err != nil {
		return err
	}

	if err := rt.RegisterFunc("__timerClear", func(id int) {
		el.ClearTimer(id)
	}); err != nil {
		return err
	}

	return rt.Eval(timersJS)
}
