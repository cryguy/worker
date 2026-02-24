package webapi

import (
	"fmt"

	"github.com/cryguy/worker/internal/core"
	"github.com/cryguy/worker/internal/eventloop"
)

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

// SetupReportError evaluates the reportError/ErrorEvent polyfill.
func SetupReportError(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	if err := rt.Eval(`
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
	return rt.Eval(reportErrorJS)
}
