package webapi

import (
	"fmt"

	"github.com/cryguy/worker/v2/internal/core"
	"github.com/cryguy/worker/v2/internal/eventloop"
)

// abortJS defines EventTarget, Event, AbortSignal, AbortController,
// DOMException, ScheduledEvent, and CustomEvent as pure JS polyfills.
const abortJS = `
class Event {
	constructor(type, options) {
		this.type = type;
		this.bubbles = !!(options && options.bubbles);
		this.cancelable = !!(options && options.cancelable);
		this.composed = !!(options && options.composed);
		this.eventPhase = 0;
		this.isTrusted = false;
		this.defaultPrevented = false;
		this.target = null;
		this.currentTarget = null;
		this.timeStamp = performance.now();
	}
	static get NONE() { return 0; }
	static get CAPTURING_PHASE() { return 1; }
	static get AT_TARGET() { return 2; }
	static get BUBBLING_PHASE() { return 3; }
	preventDefault() {
		if (this.cancelable) this.defaultPrevented = true;
	}
	stopPropagation() {}
	stopImmediatePropagation() {}
	composedPath() { return this.target ? [this.target] : []; }
	get [Symbol.toStringTag]() { return 'Event'; }
}

class EventTarget {
	constructor() {
		this._listeners = {};
	}
	addEventListener(type, callback, options) {
		if (!callback) return;
		if (!this._listeners) this._listeners = {};
		if (!this._listeners[type]) this._listeners[type] = [];
		const opts = typeof options === 'object' ? options : { capture: !!options };
		const entry = { callback, once: !!opts.once, capture: !!opts.capture };
		this._listeners[type].push(entry);
		if (opts.signal) {
			opts.signal.addEventListener('abort', () => {
				this.removeEventListener(type, callback, options);
			});
		}
	}
	removeEventListener(type, callback, options) {
		if (!this._listeners || !this._listeners[type]) return;
		const capture = typeof options === 'object' ? !!options.capture : !!options;
		this._listeners[type] = this._listeners[type].filter(
			l => !(l.callback === callback && l.capture === capture)
		);
	}
	dispatchEvent(event) {
		event.target = this;
		if (!this._listeners || !this._listeners[event.type]) return true;
		const listeners = [...this._listeners[event.type]];
		for (const l of listeners) {
			l.callback.call(this, event);
			if (l.once) this.removeEventListener(event.type, l.callback, { capture: l.capture });
		}
		return !event.defaultPrevented;
	}
	get [Symbol.toStringTag]() { return 'EventTarget'; }
}

class AbortSignal extends EventTarget {
	constructor() {
		super();
		this._aborted = false;
		this._reason = undefined;
		this.onabort = null;
	}
	get aborted() { return this._aborted; }
	get reason() { return this._reason; }
	throwIfAborted() {
		if (this._aborted) throw this._reason;
	}
	static abort(reason) {
		const signal = new AbortSignal();
		signal._aborted = true;
		signal._reason = reason !== undefined ? reason : new DOMException('The operation was aborted.', 'AbortError');
		return signal;
	}
	static timeout(ms) {
		const signal = new AbortSignal();
		setTimeout(function() {
			signal._aborted = true;
			signal._reason = new DOMException('The operation timed out.', 'TimeoutError');
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
				signal._aborted = true;
				signal._reason = s.reason;
				return signal;
			}
		}
		for (const s of signals) {
			s.addEventListener('abort', function() {
				if (!signal._aborted) {
					signal._aborted = true;
					signal._reason = s.reason;
					var ev = new Event('abort');
					if (signal.onabort) signal.onabort(ev);
					signal.dispatchEvent(ev);
				}
			});
		}
		return signal;
	}
	get [Symbol.toStringTag]() { return 'AbortSignal'; }
}

class AbortController {
	constructor() {
		this.signal = new AbortSignal();
	}
	abort(reason) {
		if (this.signal.aborted) return;
		this.signal._aborted = true;
		this.signal._reason = reason !== undefined ? reason : new DOMException('The operation was aborted.', 'AbortError');
		var ev = new Event('abort');
		if (this.signal.onabort) this.signal.onabort(ev);
		this.signal.dispatchEvent(ev);
	}
	get [Symbol.toStringTag]() { return 'AbortController'; }
}

class DOMException extends Error {
	constructor(message, name) {
		super(message || '');
		this.name = name || 'Error';
		this.message = message || '';
		const codes = {
			IndexSizeError: 1, HierarchyRequestError: 3, WrongDocumentError: 4,
			InvalidCharacterError: 5, NoModificationAllowedError: 7, NotFoundError: 8,
			NotSupportedError: 9, InvalidStateError: 11, SyntaxError: 12,
			InvalidModificationError: 13, NamespaceError: 14, InvalidAccessError: 15,
			TypeMismatchError: 17, SecurityError: 18, NetworkError: 19,
			AbortError: 20, URLMismatchError: 21, QuotaExceededError: 22,
			TimeoutError: 23, DataCloneError: 25
		};
		this.code = codes[this.name] || 0;
	}
	get [Symbol.toStringTag]() { return 'DOMException'; }
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
