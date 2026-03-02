package webapi

import (
	"fmt"

	"github.com/cryguy/worker/v2/internal/core"
	"github.com/cryguy/worker/v2/internal/eventloop"
)

// schedulerJS defines globalThis.scheduler with a wait() method.
const schedulerJS = `
globalThis.scheduler = {
	wait: function(ms) {
		return new Promise(function(resolve) {
			setTimeout(resolve, ms || 0);
		});
	},
	postTask: function(callback, options) {
		var delay = (options && options.delay) || 0;
		var signal = options && options.signal;
		return new Promise(function(resolve, reject) {
			if (signal && signal.aborted) {
				reject(signal.reason || new DOMException('The operation was aborted', 'AbortError'));
				return;
			}
			var id = setTimeout(function() {
				try { resolve(callback()); }
				catch(e) { reject(e); }
			}, delay);
			if (signal) {
				signal.addEventListener('abort', function() {
					clearTimeout(id);
					reject(signal.reason || new DOMException('The operation was aborted', 'AbortError'));
				});
			}
		});
	},
};
`

// SetupScheduler registers the scheduler global with wait().
func SetupScheduler(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	if err := rt.Eval(schedulerJS); err != nil {
		return fmt.Errorf("evaluating scheduler.js: %w", err)
	}
	return nil
}
