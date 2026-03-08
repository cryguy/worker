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
		this._enqueuing = false;
	}
	enqueue(chunk) {
		if (this._closeRequested) throw new TypeError('Cannot enqueue after close');
		if (this._stream._errored) throw this._stream._error;
		if (this._stream._sizeFn) {
			if (this._enqueuing) throw new TypeError('Cannot enqueue reentrantly');
			this._enqueuing = true;
			var chunkSize;
			try {
				chunkSize = this._stream._sizeFn(chunk);
			} catch(e) {
				this._enqueuing = false;
				this._stream._errorInternal(e);
				throw e;
			}
			this._enqueuing = false;
			chunkSize = Number(chunkSize);
			if (chunkSize !== chunkSize || chunkSize === Infinity || chunkSize === -Infinity) {
				var rangeErr = new RangeError('Invalid chunk size');
				this._stream._errorInternal(rangeErr);
				throw rangeErr;
			}
		}
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
		if (this._stream._errored) return null;
		if (this._closeRequested || this._stream._closed) return 0;
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
		if (this._stream._errored) return null;
		if (this._closeRequested || this._stream._closed) return 0;
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
						var qBefore = stream._queue.length;
						var r = stream._pullFn(stream._controller);
						function afterPull() {
							var madeProgress = stream._queue.length > qBefore || stream._closed || stream._errored || stream._pendingReads.length === 0;
							if (!madeProgress) return;
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
		return this._stream._cancelSteps(reason);
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
						var qBefore = stream._queue.length;
						var r = stream._pullFn(stream._controller);
						function afterPull() {
							var madeProgress = stream._queue.length > qBefore || stream._closed || stream._errored || stream._pendingBYOBReads.length === 0;
							if (!madeProgress) return;
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
	cancel(reason) { return this._stream._cancelSteps(reason); }
}

class ReadableStream {
	constructor(underlyingSource, strategy) {
		// Extract strategy properties first (spec conversion order).
		var hwm = 1;
		var sizeFn;
		if (strategy !== undefined && strategy !== null) {
			if (strategy.highWaterMark !== undefined) {
				hwm = Number(strategy.highWaterMark);
			}
			sizeFn = strategy.size;
		}

		// Validate underlyingSource.
		if (underlyingSource === null) throw new TypeError("The provided value 'null' is not of type 'object'.");
		if (underlyingSource !== undefined && typeof underlyingSource !== 'object' && typeof underlyingSource !== 'function') {
			throw new TypeError("parameter 1 is not of type 'object'.");
		}

		// Validate highWaterMark.
		if (hwm !== hwm || hwm < 0) throw new RangeError('Invalid highWaterMark');

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
		this._highWaterMark = hwm;
		this._sizeFn = sizeFn;
		this._byteStream = false;

		var sourceType = underlyingSource ? underlyingSource.type : undefined;
		if (sourceType !== undefined && sourceType !== 'bytes') {
			throw new RangeError("Invalid type: " + sourceType);
		}

		if (sourceType === 'bytes') {
			if (sizeFn !== undefined) throw new RangeError('The strategy for a byte stream cannot have a size function');
			this._byteStream = true;
			var autoAllocateChunkSize = underlyingSource.autoAllocateChunkSize;
			if (autoAllocateChunkSize === 0) throw new TypeError('autoAllocateChunkSize must be greater than 0');
			this._controller = new ReadableByteStreamController(this);
		} else {
			this._controller = new ReadableStreamDefaultController(this);
		}
		this._pullFn = null;
		this._cancelFn = null;

		if (underlyingSource) {
			var startFn = underlyingSource.start;
			var pullFn = underlyingSource.pull;
			var cancelFn = underlyingSource.cancel;
			if (startFn !== undefined && typeof startFn !== 'function') throw new TypeError("start is not a function");
			if (pullFn !== undefined && typeof pullFn !== 'function') throw new TypeError("pull is not a function");
			if (cancelFn !== undefined && typeof cancelFn !== 'function') throw new TypeError("cancel is not a function");
			if (typeof pullFn === 'function') {
				this._pullFn = pullFn.bind(underlyingSource);
			}
			if (typeof cancelFn === 'function') {
				this._cancelFn = cancelFn.bind(underlyingSource);
			}
			if (typeof startFn === 'function') {
				startFn.call(underlyingSource, this._controller);
			}
		}
	}

	getReader(options) {
		if (options !== undefined && options !== null && options.mode !== undefined) {
			var mode = String(options.mode);
			if (mode === 'byob') return new ReadableStreamBYOBReader(this);
			throw new RangeError("Invalid mode: " + mode);
		}
		return new ReadableStreamDefaultReader(this);
	}

	cancel(reason) {
		if (this._locked) return Promise.reject(new TypeError('Cannot cancel a locked ReadableStream'));
		return this._cancelSteps(reason);
	}
	_cancelSteps(reason) {
		this._disturbed = true;
		this._closed = true;
		var cancelResult;
		if (this._cancelFn) {
			try {
				cancelResult = this._cancelFn(reason);
			} catch(e) {
				this._drainPending();
				if (this._reader && this._reader._closedResolve) {
					this._reader._closedResolve();
				}
				return Promise.reject(e);
			}
		}
		this._drainPending();
		if (this._reader && this._reader._closedResolve) {
			this._reader._closedResolve();
		}
		if (cancelResult && typeof cancelResult.then === 'function') {
			return cancelResult.then(function() { return undefined; });
		}
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
		if (!stream || !(stream instanceof WritableStream) || stream._controller !== undefined) {
			throw new TypeError('WritableStreamDefaultController constructor is not directly constructible');
		}
		this._stream = stream;
	}
	error(e) {
		this._stream._errorInternal(e);
	}
}

class WritableStreamDefaultWriter {
	constructor(stream) {
		if (!(stream instanceof WritableStream)) throw new TypeError('WritableStreamDefaultWriter requires a WritableStream argument');
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
	get desiredSize() {
		if (!this._stream) throw new TypeError('Writer has been released');
		var s = this._stream;
		if (s._errored) return null;
		if (s._closed) return 0;
		return s._highWaterMark - (s._queueSize || 0);
	}
	write(chunk) {
		if (!this._stream) return Promise.reject(new TypeError('Writer has been released'));
		if (this._stream._closed) throw new TypeError('Cannot write to a closed stream');
		if (this._stream._errored) throw this._stream._error;
		var s = this._stream;
		s._queueSize++;
		if (s._writeFn) {
			const result = s._writeFn(chunk, s._controller);
			if (result && typeof result.then === 'function') {
				return result.then(function() { s._queueSize--; }, function(e) { s._queueSize--; throw e; });
			}
		}
		s._queueSize--;
		return Promise.resolve();
	}
	close() {
		if (!this._stream) return Promise.reject(new TypeError('Writer has been released'));
		var stream = this._stream;
		var self = this;
		if (stream._closeFn) {
			var result = stream._closeFn();
			if (result && typeof result.then === 'function') {
				return result.then(function() {
					stream._closed = true;
					if (self._closedResolve) self._closedResolve();
				});
			}
		}
		stream._closed = true;
		if (this._closedResolve) this._closedResolve();
		return Promise.resolve();
	}
	abort(reason) {
		if (!this._stream) return Promise.reject(new TypeError('Writer has been released'));
		var stream = this._stream;
		var self = this;
		if (stream._abortFn) {
			var result = stream._abortFn(reason);
			if (result && typeof result.then === 'function') {
				return result.then(function() {
					stream._closed = true;
					if (self._closedResolve) self._closedResolve();
				});
			}
		}
		stream._closed = true;
		if (this._closedResolve) this._closedResolve();
		return Promise.resolve();
	}
	releaseLock() {
		if (this._stream) {
			this._stream._locked = false;
			this._stream = null;
		}
	}
	get closed() {
		return this._closedPromise;
	}
	get ready() { return Promise.resolve(); }
}

class WritableStream {
	constructor(underlyingSink, strategy) {
		// Extract strategy properties first (spec conversion order).
		this._highWaterMark = 1;
		var sizeFn;
		if (strategy !== undefined && strategy !== null) {
			if (strategy.highWaterMark !== undefined) {
				this._highWaterMark = Number(strategy.highWaterMark);
			}
			sizeFn = strategy.size;
		}

		// Validate highWaterMark.
		if (this._highWaterMark !== this._highWaterMark || this._highWaterMark < 0) throw new RangeError('Invalid highWaterMark');

		this._locked = false;
		this._closed = false;
		this._errored = false;
		this._error = null;
		this._queueSize = 0;
		this._controller = new WritableStreamDefaultController(this);
		this._writeFn = null;
		this._closeFn = null;
		this._abortFn = null;

		if (underlyingSink) {
			if (underlyingSink.type !== undefined) {
				throw new RangeError('WritableStream does not support a type');
			}
			var sinkStart = underlyingSink.start;
			var sinkWrite = underlyingSink.write;
			var sinkClose = underlyingSink.close;
			var sinkAbort = underlyingSink.abort;
			if (sinkStart !== undefined && typeof sinkStart !== 'function') throw new TypeError("start is not a function");
			if (sinkWrite !== undefined && typeof sinkWrite !== 'function') throw new TypeError("write is not a function");
			if (sinkClose !== undefined && typeof sinkClose !== 'function') throw new TypeError("close is not a function");
			if (sinkAbort !== undefined && typeof sinkAbort !== 'function') throw new TypeError("abort is not a function");
			if (typeof sinkWrite === 'function') {
				this._writeFn = sinkWrite.bind(underlyingSink);
			}
			if (typeof sinkClose === 'function') {
				this._closeFn = sinkClose.bind(underlyingSink);
			}
			if (typeof sinkAbort === 'function') {
				this._abortFn = sinkAbort.bind(underlyingSink);
			}
			if (typeof sinkStart === 'function') {
				sinkStart.call(underlyingSink, this._controller);
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
		if (transformer !== undefined && transformer !== null) {
			if (transformer.readableType !== undefined) throw new RangeError('readableType is not supported');
			if (transformer.writableType !== undefined) throw new RangeError('writableType is not supported');
		}
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
			get desiredSize() { return readableController.desiredSize; },
		};

		if (transformer && typeof transformer.start === 'function') {
			transformer.start(transformController);
		}

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
	var asyncIteratorMethod = asyncIterable[Symbol.asyncIterator];
	var iteratorMethod;
	var isAsync;
	if (asyncIteratorMethod != null) {
		if (typeof asyncIteratorMethod !== 'function') {
			throw new TypeError('ReadableStream.from requires an iterable or async iterable');
		}
		isAsync = true;
	} else {
		iteratorMethod = asyncIterable[Symbol.iterator];
		if (typeof iteratorMethod !== 'function') {
			throw new TypeError('ReadableStream.from requires an iterable or async iterable');
		}
		isAsync = false;
	}
	var iterator = isAsync
		? asyncIteratorMethod.call(asyncIterable)
		: iteratorMethod.call(asyncIterable);
	if (iterator == null || (typeof iterator !== 'object' && typeof iterator !== 'function')) {
		throw new TypeError('The result of the iterator method is not an object');
	}
	var nextMethod = iterator.next;
	var iteratorReturn = iterator.return;
	var iteratorFinished = false;
	return new ReadableStream({
		async pull(controller) {
			var result;
			try {
				result = await Promise.resolve(nextMethod.call(iterator));
			} catch(e) {
				controller.error(e);
				throw e;
			}
			if (result == null || typeof result !== 'object') {
				var err = new TypeError('Iterator result is not an object');
				controller.error(err);
				throw err;
			}
			if (result.done) {
				iteratorFinished = true;
				controller.close();
			} else {
				controller.enqueue(isAsync ? result.value : await result.value);
			}
		},
		cancel(reason) {
			if (iteratorFinished) return undefined;
			iteratorFinished = true;
			if (iteratorReturn === undefined || iteratorReturn === null) {
				return undefined;
			}
			if (typeof iteratorReturn !== 'function') {
				throw new TypeError('iterator.return is not a function');
			}
			return Promise.resolve(iteratorReturn.call(iterator, reason)).then(function(r) {
				if (r == null || typeof r !== 'object') {
					throw new TypeError('Iterator result is not an object');
				}
			});
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
		if (typeof init !== 'object' || init === null) throw new TypeError('ByteLengthQueuingStrategy requires an object argument');
		if (!('highWaterMark' in init)) throw new TypeError("Required member 'highWaterMark' is undefined.");
		this.highWaterMark = Number(init.highWaterMark);
	}
	size(chunk) {
		return chunk.byteLength;
	}
}

class CountQueuingStrategy {
	constructor(init) {
		if (typeof init !== 'object' || init === null) throw new TypeError('CountQueuingStrategy requires an object argument');
		if (!('highWaterMark' in init)) throw new TypeError("Required member 'highWaterMark' is undefined.");
		this.highWaterMark = Number(init.highWaterMark);
	}
	size() {
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
