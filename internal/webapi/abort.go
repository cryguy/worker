package webapi

import (
	"fmt"

	"github.com/cryguy/worker/internal/core"
	"github.com/cryguy/worker/internal/eventloop"
)

// abortJS defines EventTarget, Event, AbortSignal, AbortController,
// DOMException, ScheduledEvent, and CustomEvent as pure JS polyfills.
const abortJS = `
class Event {
	constructor(type, options) {
		this.type = type;
		this.bubbles = !!(options && options.bubbles);
		this.cancelable = !!(options && options.cancelable);
		this.defaultPrevented = false;
		this.target = null;
		this.currentTarget = null;
		this.timeStamp = performance.now();
	}
	preventDefault() {
		if (this.cancelable) this.defaultPrevented = true;
	}
	stopPropagation() {}
	stopImmediatePropagation() {}
}

class EventTarget {
	constructor() {
		this._listeners = {};
	}
	addEventListener(type, callback, options) {
		if (typeof callback !== 'function') return;
		if (!this._listeners[type]) this._listeners[type] = [];
		const once = options && options.once;
		this._listeners[type].push({ callback, once });
	}
	removeEventListener(type, callback) {
		if (!this._listeners[type]) return;
		this._listeners[type] = this._listeners[type].filter(l => l.callback !== callback);
	}
	dispatchEvent(event) {
		event.target = this;
		event.currentTarget = this;
		const listeners = this._listeners[event.type];
		if (!listeners) return true;
		const copy = listeners.slice();
		for (const entry of copy) {
			entry.callback.call(this, event);
			if (entry.once) {
				this.removeEventListener(event.type, entry.callback);
			}
		}
		return !event.defaultPrevented;
	}
}

class AbortSignal extends EventTarget {
	constructor() {
		super();
		this.aborted = false;
		this.reason = undefined;
		this.onabort = null;
	}
	throwIfAborted() {
		if (this.aborted) throw this.reason;
	}
	static abort(reason) {
		const signal = new AbortSignal();
		signal.aborted = true;
		signal.reason = reason !== undefined ? reason : new DOMException('The operation was aborted.', 'AbortError');
		return signal;
	}
	static timeout(ms) {
		const signal = new AbortSignal();
		setTimeout(function() {
			signal.aborted = true;
			signal.reason = new DOMException('The operation timed out.', 'TimeoutError');
			var ev = new Event('abort');
			if (signal.onabort) signal.onabort(ev);
			signal.dispatchEvent(ev);
		}, ms);
		return signal;
	}
	static any(signals) {
		const signal = new AbortSignal();
		for (const s of signals) {
			if (s.aborted) {
				signal.aborted = true;
				signal.reason = s.reason;
				return signal;
			}
		}
		for (const s of signals) {
			s.addEventListener('abort', function() {
				if (!signal.aborted) {
					signal.aborted = true;
					signal.reason = s.reason;
					var ev = new Event('abort');
					if (signal.onabort) signal.onabort(ev);
					signal.dispatchEvent(ev);
				}
			});
		}
		return signal;
	}
}

class AbortController {
	constructor() {
		this.signal = new AbortSignal();
	}
	abort(reason) {
		if (this.signal.aborted) return;
		this.signal.aborted = true;
		this.signal.reason = reason !== undefined ? reason : new DOMException('The operation was aborted.', 'AbortError');
		var ev = new Event('abort');
		if (this.signal.onabort) this.signal.onabort(ev);
		this.signal.dispatchEvent(ev);
	}
}

class DOMException extends Error {
	constructor(message, name) {
		super(message || '');
		this.name = name || 'Error';
		this.message = message || '';
		this.code = 0;
	}
}

class ScheduledEvent extends Event {
	constructor(scheduledTime, cron) {
		super('scheduled');
		this.scheduledTime = scheduledTime;
		this.cron = cron || '';
		this._waitUntilPromises = [];
		this.noRetry = function() {};
	}
	waitUntil(promise) {
		this._waitUntilPromises.push(Promise.resolve(promise));
	}
}

class CustomEvent extends Event {
	constructor(type, init) {
		super(type, init);
		this.detail = (init && init.detail !== undefined) ? init.detail : null;
	}
}

globalThis.Event = Event;
globalThis.EventTarget = EventTarget;
globalThis.AbortSignal = AbortSignal;
globalThis.AbortController = AbortController;
globalThis.DOMException = DOMException;
globalThis.ScheduledEvent = ScheduledEvent;
globalThis.CustomEvent = CustomEvent;
`

// SetupAbort evaluates Event, EventTarget, AbortSignal, AbortController,
// DOMException, ScheduledEvent, and CustomEvent polyfills.
func SetupAbort(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	if err := rt.Eval(abortJS); err != nil {
		return fmt.Errorf("evaluating abort.js: %w", err)
	}
	return nil
}
