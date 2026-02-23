package worker

import (
	"fmt"

	"modernc.org/quickjs"
)

// schedulerJS defines globalThis.scheduler with a wait() method
// that returns a Promise resolving after the given milliseconds.
const schedulerJS = `
globalThis.scheduler = {
	wait: function(ms) {
		return new Promise(function(resolve) {
			setTimeout(resolve, ms || 0);
		});
	},
};
`

// setupScheduler registers the scheduler global with wait().
func setupScheduler(vm *quickjs.VM, _ *eventLoop) error {
	if err := evalDiscard(vm, schedulerJS); err != nil {
		return fmt.Errorf("evaluating scheduler.js: %w", err)
	}
	return nil
}
