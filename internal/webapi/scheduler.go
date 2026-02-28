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
};
`

// SetupScheduler registers the scheduler global with wait().
func SetupScheduler(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	if err := rt.Eval(schedulerJS); err != nil {
		return fmt.Errorf("evaluating scheduler.js: %w", err)
	}
	return nil
}
