package webapi

import (
	"fmt"

	"github.com/cryguy/worker/internal/core"
	"github.com/cryguy/worker/internal/eventloop"
)

// SetupConsole replaces globalThis.console with a Go-backed version
// that captures output into the per-request log buffer.
func SetupConsole(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	// Register Go-backed __console function.
	if err := rt.RegisterFunc("__console", func(reqIDStr, level, message string) {
		reqID := uint64(0)
		if reqIDStr != "" && reqIDStr != "undefined" {
			fmt.Sscanf(reqIDStr, "%d", &reqID)
		}
		core.AddLog(reqID, level, message)
	}); err != nil {
		return err
	}

	// Build console object in JS that calls __console.
	consoleJS := `
(function() {
	var levels = ['log', 'info', 'warn', 'error', 'debug'];
	var con = {};
	for (var i = 0; i < levels.length; i++) {
		(function(lvl) {
			con[lvl] = function() {
				var parts = [];
				for (var j = 0; j < arguments.length; j++) {
					var arg = arguments[j];
					if (typeof arg === 'object' && arg !== null) {
						parts.push('[object Object]');
					} else {
						parts.push(String(arg));
					}
				}
				var reqID = globalThis.__requestID || '';
				__console(reqID, lvl, parts.join(' '));
			};
		})(levels[i]);
	}
	globalThis.console = con;
})();
`
	return rt.Eval(consoleJS)
}

// consoleExtJS adds extended console methods (time, count, assert, table, etc.)
const consoleExtJS = `
(function() {
var __timers = {};
var __counters = {};
var __groupDepth = 0;

console.time = function(label) {
	__timers[label || 'default'] = performance.now();
};
console.timeEnd = function(label) {
	var l = label || 'default';
	var start = __timers[l];
	if (start === undefined) { console.warn('Timer "' + l + '" does not exist'); return; }
	var elapsed = performance.now() - start;
	delete __timers[l];
	console.log(l + ': ' + elapsed.toFixed(3) + 'ms');
};
console.timeLog = function(label) {
	var l = label || 'default';
	var start = __timers[l];
	if (start === undefined) { console.warn('Timer "' + l + '" does not exist'); return; }
	var elapsed = performance.now() - start;
	var args = Array.prototype.slice.call(arguments, 1);
	if (args.length > 0) {
		console.log(l + ': ' + elapsed.toFixed(3) + 'ms', args.join(' '));
	} else {
		console.log(l + ': ' + elapsed.toFixed(3) + 'ms');
	}
};
console.count = function(label) {
	var l = label || 'default';
	__counters[l] = (__counters[l] || 0) + 1;
	console.log(l + ': ' + __counters[l]);
};
console.countReset = function(label) {
	__counters[label || 'default'] = 0;
};
console.assert = function(cond) {
	if (!cond) {
		var args = Array.prototype.slice.call(arguments, 1);
		if (args.length > 0) {
			console.error('Assertion failed:', args.join(' '));
		} else {
			console.error('Assertion failed');
		}
	}
};
console.table = function(data) {
	console.log(JSON.stringify(data, null, 2));
};
console.trace = function() {
	var args = Array.prototype.slice.call(arguments);
	if (args.length > 0) {
		console.log('Trace:', args.join(' '));
	} else {
		console.log('Trace');
	}
};
console.group = function(label) {
	if (label) console.log(label);
	__groupDepth++;
};
console.groupEnd = function() {
	if (__groupDepth > 0) __groupDepth--;
};
console.dir = function(obj) {
	console.log(JSON.stringify(obj, null, 2));
};
})();
`

// SetupConsoleExt evaluates the extended console methods polyfill.
func SetupConsoleExt(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	return rt.Eval(consoleExtJS)
}
