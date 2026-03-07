package webapi

import (
	"fmt"

	"github.com/cryguy/worker/v2/internal/core"
	"github.com/cryguy/worker/v2/internal/eventloop"
)

// streamsJS implements ReadableStream, WritableStream, and TransformStream
// as pure JS polyfills.
const streamsJS = `
(function() {

// --- ReadableStream ---

class ReadableStreamDefaultController {
	constructor(stream) {
		this._stream = stream;
		this._closeRequested = false;
	}
	enqueue(chunk) {
		if (this._closeRequested) throw new TypeError('Cannot enqueue after close');
		this._stream._queue.push(chunk);
		this._stream._pull();
	}
	close() {
		this._closeRequested = true;
		this._stream._closeInternal();
	}
	error(e) {
		this._stream._errorInternal(e);
	}
	get desiredSize() {
		return this._stream._highWaterMark - this._stream._queue.length;
	}
}

class ReadableByteStreamController {
	constructor(stream) {
		this._stream = stream;
		this._closeRequested = false;
	}
	enqueue(chunk) {
		if (this._closeRequested) throw new TypeError('Cannot enqueue after close');
		var bytes;
		if (chunk instanceof Uint8Array) bytes = chunk;
		else if (chunk instanceof ArrayBuffer) bytes = new Uint8Array(chunk);
		else if (ArrayBuffer.isView(chunk)) bytes = new Uint8Array(chunk.buffer, chunk.byteOffset, chunk.byteLength);
		else throw new TypeError('chunk must be an ArrayBufferView');
		this._stream._queue.push(bytes);
		this._stream._pull();
	}
	close() {
		this._closeRequested = true;
		this._stream._closeInternal();
	}
	error(e) {
		this._stream._errorInternal(e);
	}
	get desiredSize() {
		return this._stream._highWaterMark - this._stream._queue.length;
	}
	get byobRequest() { return null; }
}

class ReadableStreamDefaultReader {
	constructor(stream) {
		if (stream._locked) throw new TypeError('ReadableStream is already locked');
		this._stream = stream;
		stream._locked = true;
		stream._reader = this;
		this._closed = false;
		const self = this;
		this._closedPromise = new Promise(function(resolve, reject) {
			self._closedResolve = resolve;
			self._closedReject = reject;
		});
		if (stream._closed) {
			this._closedResolve();
		}
	}
	async read() {
		const stream = this._stream;
		stream._disturbed = true;
		if (stream._queue.length > 0) {
			return { value: stream._queue.shift(), done: false };
		}
		if (stream._closed) {
			return { value: undefined, done: true };
		}
		if (stream._errored) {
			throw stream._error;
		}
		return new Promise((resolve, reject) => {
			stream._pendingReads.push({ resolve, reject });
			if (stream._pullFn && !stream._pulling) {
				stream._pulling = true;
				Promise.resolve().then(function pullLoop() {
					stream._pulling = false;
					if (stream._closed || stream._errored) return;
					try {
						var r = stream._pullFn(stream._controller);
						function afterPull() {
							if (stream._pendingReads.length > 0 && stream._queue.length === 0 && !stream._closed && !stream._errored && stream._pullFn) {
								stream._pulling = true;
								Promise.resolve().then(pullLoop);
							}
						}
						if (r && typeof r.then === "function") r.then(afterPull, function(e) { stream._errorInternal(e); });
						else afterPull();
					} catch(e) { stream._errorInternal(e); }
				});
			}
		});
	}
	releaseLock() {
		if (this._stream) {
			this._stream._locked = false;
			this._stream._reader = null;
		}
	}
	get closed() {
		return this._closedPromise;
	}
	cancel(reason) {
		return this._stream.cancel(reason);
	}
}

class ReadableStreamBYOBReader {
	constructor(stream) {
		if (stream._locked) throw new TypeError('ReadableStream is already locked');
		if (!stream._byteStream) throw new TypeError('ReadableStreamBYOBReader can only be used with byte streams');
		this._stream = stream;
		stream._locked = true;
		stream._reader = this;
		this._closed = false;
		var self = this;
		this._closedPromise = new Promise(function(resolve, reject) {
			self._closedResolve = resolve;
			self._closedReject = reject;
		});
		if (stream._closed) {
			this._closedResolve();
		}
	}
	read(view) {
		if (!ArrayBuffer.isView(view)) return Promise.reject(new TypeError('view must be an ArrayBufferView'));
		if (view.byteLength === 0) return Promise.reject(new TypeError('view must have non-zero byteLength'));
		var stream = this._stream;
		stream._disturbed = true;
		var writeView = new Uint8Array(view.buffer, view.byteOffset, view.byteLength);
		if (stream._queue.length > 0) {
			var bytesCopied = 0;
			while (bytesCopied < writeView.byteLength && stream._queue.length > 0) {
				var front = stream._queue[0];
				var toCopy = Math.min(front.length, writeView.byteLength - bytesCopied);
				writeView.set(front.subarray(0, toCopy), bytesCopied);
				bytesCopied += toCopy;
				if (toCopy >= front.length) { stream._queue.shift(); }
				else { stream._queue[0] = front.subarray(toCopy); }
			}
			return Promise.resolve({ value: new Uint8Array(view.buffer, view.byteOffset, bytesCopied), done: false });
		}
		if (stream._closed) {
			return Promise.resolve({ value: new Uint8Array(view.buffer, view.byteOffset, 0), done: true });
		}
		if (stream._errored) {
			return Promise.reject(stream._error);
		}
		return new Promise(function(resolve, reject) {
			stream._pendingBYOBReads.push({
				view: writeView, buffer: view.buffer, byteOffset: view.byteOffset,
				resolve: resolve, reject: reject
			});
			if (stream._pullFn && !stream._pulling) {
				stream._pulling = true;
				Promise.resolve().then(function pullLoop() {
					stream._pulling = false;
					if (stream._closed || stream._errored) return;
					try {
						var r = stream._pullFn(stream._controller);
						function afterPull() {
							if (stream._pendingBYOBReads.length > 0 && stream._queue.length === 0 && !stream._closed && !stream._errored && stream._pullFn) {
								stream._pulling = true;
								Promise.resolve().then(pullLoop);
							}
						}
						if (r && typeof r.then === "function") r.then(afterPull, function(e) { stream._errorInternal(e); });
						else afterPull();
					} catch(e) { stream._errorInternal(e); }
				});
			}
		});
	}
	releaseLock() {
		if (this._stream) {
			this._stream._locked = false;
			this._stream._reader = null;
		}
	}
	get closed() { return this._closedPromise; }
	cancel(reason) { return this._stream.cancel(reason); }
}

class ReadableStream {
	constructor(underlyingSource, strategy) {
		this._queue = [];
		this._locked = false;
		this._disturbed = false;
		this._reader = null;
		this._closed = false;
		this._errored = false;
		this._error = null;
		this._pendingReads = [];
		this._pendingBYOBReads = [];
		this._pulling = false;
		this._highWaterMark = (strategy && strategy.highWaterMark) || 1;
		this._byteStream = false;

		if (underlyingSource && underlyingSource.type === 'bytes') {
			this._byteStream = true;
			this._controller = new ReadableByteStreamController(this);
		} else {
			this._controller = new ReadableStreamDefaultController(this);
		}
		this._pullFn = null;
		this._cancelFn = null;

		if (underlyingSource) {
			if (typeof underlyingSource.pull === 'function') {
				this._pullFn = underlyingSource.pull.bind(underlyingSource);
			}
			if (typeof underlyingSource.cancel === 'function') {
				this._cancelFn = underlyingSource.cancel.bind(underlyingSource);
			}
			if (typeof underlyingSource.start === 'function') {
				underlyingSource.start(this._controller);
			}
		}
	}

	getReader(options) {
		if (options && options.mode === 'byob') {
			return new ReadableStreamBYOBReader(this);
		}
		return new ReadableStreamDefaultReader(this);
	}

	cancel(reason) {
		this._disturbed = true;
		this._closed = true;
		if (this._cancelFn) this._cancelFn(reason);
		this._drainPending();
		return Promise.resolve();
	}

	get locked() { return this._locked; }

	pipeTo(destination, options) {
		if (this._locked) return Promise.reject(new TypeError('ReadableStream is locked'));
		if (!(destination instanceof WritableStream)) return Promise.reject(new TypeError('pipeTo requires a WritableStream'));
		options = options || {};
		const preventClose = !!options.preventClose;
		const preventAbort = !!options.preventAbort;
		const preventCancel = !!options.preventCancel;
		const reader = this.getReader();
		const writer = destination.getWriter();
		async function pump() {
			try {
				while (true) {
					const { value, done } = await reader.read();
					if (done) {
						if (!preventClose) await writer.close();
						else writer.releaseLock();
						break;
					}
					await writer.write(value);
				}
			} catch(e) {
				if (!preventAbort) {
					try { await writer.abort(e); } catch(_) {}
				}
				if (!preventCancel) {
					try { await reader.cancel(e); } catch(_) {}
				}
				throw e;
			} finally {
				reader.releaseLock();
			}
		}
		return pump();
	}

	pipeThrough(transform, options) {
		if (this._locked) throw new TypeError('ReadableStream is locked');
		if (!transform || typeof transform !== 'object') throw new TypeError('pipeThrough requires a transform object');
		if (!(transform.writable instanceof WritableStream)) throw new TypeError('pipeThrough requires transform.writable to be a WritableStream');
		this.pipeTo(transform.writable, options);
		return transform.readable;
	}

	tee() {
		if (this._locked) throw new TypeError('ReadableStream is locked');
		const reader = this.getReader();
		let closed = false;
		let branch1Controller;
		let branch2Controller;
		const branch1 = new ReadableStream({
			start(controller) { branch1Controller = controller; },
		});
		const branch2 = new ReadableStream({
			start(controller) { branch2Controller = controller; },
		});
		async function pump() {
			try {
				while (true) {
					const { value, done } = await reader.read();
					if (done) {
						if (!closed) {
							closed = true;
							branch1Controller.close();
							branch2Controller.close();
						}
						return;
					}
					branch1Controller.enqueue(value);
					branch2Controller.enqueue(value);
				}
			} catch(e) {
				branch1Controller.error(e);
				branch2Controller.error(e);
			}
		}
		pump();
		return [branch1, branch2];
	}

	_pull() {
		while (this._queue.length > 0 && this._pendingReads.length > 0) {
			var chunk = this._queue.shift();
			var { resolve } = this._pendingReads.shift();
			resolve({ value: chunk, done: false });
		}
		this._pullBYOB();
	}

	_pullBYOB() {
		while (this._queue.length > 0 && this._pendingBYOBReads.length > 0) {
			var req = this._pendingBYOBReads[0];
			var wv = req.view;
			var bc = 0;
			while (bc < wv.byteLength && this._queue.length > 0) {
				var f = this._queue[0];
				var tc = Math.min(f.length, wv.byteLength - bc);
				wv.set(f.subarray(0, tc), bc);
				bc += tc;
				if (tc >= f.length) { this._queue.shift(); }
				else { this._queue[0] = f.subarray(tc); }
			}
			if (bc > 0) {
				this._pendingBYOBReads.shift();
				req.resolve({ value: new Uint8Array(req.buffer, req.byteOffset, bc), done: false });
			} else { break; }
		}
	}

	_closeInternal() {
		this._closed = true;
		this._drainPending();
		while (this._pendingBYOBReads.length > 0) {
			var req = this._pendingBYOBReads.shift();
			req.resolve({ value: new Uint8Array(req.buffer, req.byteOffset, 0), done: true });
		}
		if (this._reader && this._reader._closedResolve) {
			this._reader._closedResolve();
		}
	}

	_errorInternal(e) {
		this._errored = true;
		this._error = e;
		for (var _ei = 0; _ei < this._pendingReads.length; _ei++) {
			this._pendingReads[_ei].reject(e);
		}
		this._pendingReads = [];
		for (var _bi = 0; _bi < this._pendingBYOBReads.length; _bi++) {
			this._pendingBYOBReads[_bi].reject(e);
		}
		this._pendingBYOBReads = [];
		if (this._reader && this._reader._closedReject) {
			this._reader._closedReject(e);
		}
	}

	_drainPending() {
		while (this._pendingReads.length > 0) {
			var { resolve } = this._pendingReads.shift();
			if (this._queue.length > 0) {
				resolve({ value: this._queue.shift(), done: false });
			} else {
				resolve({ value: undefined, done: true });
			}
		}
	}

	values(options) {
		const preventCancel = !!(options && options.preventCancel);
		const reader = this.getReader();
		return {
			[Symbol.asyncIterator]() { return this; },
			async next() {
				const { done, value } = await reader.read();
				if (done) {
					reader.releaseLock();
					return { done: true, value: undefined };
				}
				return { done: false, value };
			},
			async return(v) {
				if (!preventCancel) await reader.cancel(v);
				reader.releaseLock();
				return { done: true, value: v };
			}
		};
	}

	[Symbol.asyncIterator]() { return this.values(); }

	get [Symbol.toStringTag]() { return 'ReadableStream'; }
}

// --- WritableStream ---

class WritableStreamDefaultController {
	constructor(stream) {
		this._stream = stream;
	}
	error(e) {
		this._stream._errorInternal(e);
	}
}

class WritableStreamDefaultWriter {
	constructor(stream) {
		if (stream._locked) throw new TypeError('WritableStream is already locked');
		this._stream = stream;
		stream._locked = true;
		const self = this;
		this._closedPromise = new Promise(function(resolve, reject) {
			self._closedResolve = resolve;
			self._closedReject = reject;
		});
		if (stream._closed) {
			this._closedResolve();
		}
	}
	write(chunk) {
		if (this._stream._closed) throw new TypeError('Cannot write to a closed stream');
		if (this._stream._errored) throw this._stream._error;
		if (this._stream._writeFn) {
			const result = this._stream._writeFn(chunk, this._stream._controller);
			if (result && typeof result.then === 'function') return result;
		}
		return Promise.resolve();
	}
	close() {
		const self = this;
		if (this._stream._closeFn) {
			const result = this._stream._closeFn();
			if (result && typeof result.then === 'function') {
				return result.then(function() {
					self._stream._closed = true;
					if (self._closedResolve) self._closedResolve();
				});
			}
		}
		this._stream._closed = true;
		if (this._closedResolve) this._closedResolve();
		return Promise.resolve();
	}
	abort(reason) {
		const self = this;
		if (this._stream._abortFn) {
			const result = this._stream._abortFn(reason);
			if (result && typeof result.then === 'function') {
				return result.then(function() {
					self._stream._closed = true;
					if (self._closedResolve) self._closedResolve();
				});
			}
		}
		this._stream._closed = true;
		if (this._closedResolve) this._closedResolve();
		return Promise.resolve();
	}
	releaseLock() {
		this._stream._locked = false;
	}
	get closed() {
		return this._closedPromise;
	}
	get ready() { return Promise.resolve(); }
}

class WritableStream {
	constructor(underlyingSink, strategy) {
		this._locked = false;
		this._closed = false;
		this._errored = false;
		this._error = null;
		this._controller = new WritableStreamDefaultController(this);
		this._writeFn = null;
		this._closeFn = null;
		this._abortFn = null;

		if (underlyingSink) {
			if (typeof underlyingSink.write === 'function') {
				this._writeFn = underlyingSink.write.bind(underlyingSink);
			}
			if (typeof underlyingSink.close === 'function') {
				this._closeFn = underlyingSink.close.bind(underlyingSink);
			}
			if (typeof underlyingSink.abort === 'function') {
				this._abortFn = underlyingSink.abort.bind(underlyingSink);
			}
			if (typeof underlyingSink.start === 'function') {
				underlyingSink.start(this._controller);
			}
		}
	}

	getWriter() {
		return new WritableStreamDefaultWriter(this);
	}

	get locked() { return this._locked; }

	abort(reason) {
		const writer = this.getWriter();
		const p = writer.abort(reason);
		writer.releaseLock();
		return p;
	}

	close() {
		const writer = this.getWriter();
		const p = writer.close();
		writer.releaseLock();
		return p;
	}

	_errorInternal(e) {
		this._errored = true;
		this._error = e;
	}

	get [Symbol.toStringTag]() { return 'WritableStream'; }
}

// --- TransformStream ---

class TransformStream {
	constructor(transformer, writableStrategy, readableStrategy) {
		const self = this;
		let readableController;

		this.readable = new ReadableStream({
			start(controller) {
				readableController = controller;
			}
		}, readableStrategy);

		const transformFn = transformer && typeof transformer.transform === 'function'
			? transformer.transform.bind(transformer)
			: null;
		const flushFn = transformer && typeof transformer.flush === 'function'
			? transformer.flush.bind(transformer)
			: null;

		const transformController = {
			enqueue(chunk) { readableController.enqueue(chunk); },
			error(e) { readableController.error(e); },
			terminate() { readableController.close(); },
		};

		this.writable = new WritableStream({
			async write(chunk) {
				if (transformFn) {
					await transformFn(chunk, transformController);
				} else {
					readableController.enqueue(chunk);
				}
			},
			async close() {
				if (flushFn) {
					await flushFn(transformController);
				}
				readableController.close();
			}
		}, writableStrategy);
	}

	get [Symbol.toStringTag]() { return 'TransformStream'; }
}

// --- ReadableStream.from() ---

ReadableStream.from = function(asyncIterable) {
	if (asyncIterable == null) {
		throw new TypeError('ReadableStream.from called on null or undefined');
	}
	const asyncIteratorMethod = asyncIterable[Symbol.asyncIterator];
	const iteratorMethod = asyncIterable[Symbol.iterator];
	if (typeof asyncIteratorMethod !== 'function' && typeof iteratorMethod !== 'function') {
		throw new TypeError('ReadableStream.from requires an iterable or async iterable');
	}
	const iterator = typeof asyncIteratorMethod === 'function'
		? asyncIterable[Symbol.asyncIterator]()
		: asyncIterable[Symbol.iterator]();
	return new ReadableStream({
		async pull(controller) {
			const { value, done } = await iterator.next();
			if (done) {
				controller.close();
			} else {
				controller.enqueue(value);
			}
		}
	});
};

// --- FixedLengthStream ---

class FixedLengthStream {
	constructor(expectedLength) {
		if (typeof expectedLength !== 'number' || expectedLength < 0) {
			throw new TypeError('FixedLengthStream requires a non-negative number');
		}
		this._expectedLength = expectedLength;
		let bytesWritten = 0;
		const expected = expectedLength;
		const ts = new TransformStream({
			transform(chunk, controller) {
				const len = (chunk && typeof chunk.byteLength === 'number')
					? chunk.byteLength
					: (chunk && typeof chunk.length === 'number' ? chunk.length : 0);
				bytesWritten += len;
				if (bytesWritten > expected) {
					throw new TypeError(
						'FixedLengthStream: exceeded expected length of ' + expected + ' bytes (wrote ' + bytesWritten + ')'
					);
				}
				controller.enqueue(chunk);
			},
			flush(controller) {
				if (bytesWritten !== expected) {
					throw new TypeError(
						'FixedLengthStream: expected ' + expected + ' bytes but received ' + bytesWritten
					);
				}
			}
		});
		this.readable = ts.readable;
		this.writable = ts.writable;
	}
}

globalThis.ReadableStream = ReadableStream;
globalThis.ReadableStreamDefaultReader = ReadableStreamDefaultReader;
globalThis.ReadableStreamBYOBReader = ReadableStreamBYOBReader;
globalThis.WritableStream = WritableStream;
globalThis.WritableStreamDefaultWriter = WritableStreamDefaultWriter;
globalThis.TransformStream = TransformStream;
globalThis.FixedLengthStream = FixedLengthStream;

})();
`

// queuingStrategiesJS defines ByteLengthQueuingStrategy and CountQueuingStrategy.
const queuingStrategiesJS = `
class ByteLengthQueuingStrategy {
	constructor(init) {
		this.highWaterMark = init.highWaterMark;
	}
	size(chunk) {
		return chunk.byteLength;
	}
}

class CountQueuingStrategy {
	constructor(init) {
		this.highWaterMark = init.highWaterMark;
	}
	size(chunk) {
		return 1;
	}
}

globalThis.ByteLengthQueuingStrategy = ByteLengthQueuingStrategy;
globalThis.CountQueuingStrategy = CountQueuingStrategy;
`

// SetupStreams evaluates the Streams API polyfills.
func SetupStreams(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	if err := rt.Eval(streamsJS); err != nil {
		return fmt.Errorf("evaluating streams.js: %w", err)
	}
	if err := rt.Eval(queuingStrategiesJS); err != nil {
		return fmt.Errorf("evaluating queuing_strategies.js: %w", err)
	}
	return nil
}
