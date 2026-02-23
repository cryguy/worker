package worker

import (
	"fmt"

	"modernc.org/quickjs"
)

// textStreamsJS implements TextEncoderStream, TextDecoderStream, and
// IdentityTransformStream as pure JS polyfills wrapping TransformStream.
// These are part of the Cloudflare Workers Streams API surface.
const textStreamsJS = `
(function() {

class TextEncoderStream extends TransformStream {
	constructor() {
		const encoder = new TextEncoder();
		super({
			transform(chunk, controller) {
				if (typeof chunk !== 'string') {
					throw new TypeError('TextEncoderStream expects string chunks');
				}
				controller.enqueue(encoder.encode(chunk));
			}
		});
		this._encoding = 'utf-8';
	}
	get encoding() { return this._encoding; }
}

class TextDecoderStream extends TransformStream {
	constructor(label, options) {
		const decoder = new TextDecoder(label, options);
		super({
			transform(chunk, controller) {
				let data;
				if (chunk instanceof ArrayBuffer) {
					data = new Uint8Array(chunk);
				} else if (ArrayBuffer.isView(chunk)) {
					data = new Uint8Array(chunk.buffer, chunk.byteOffset, chunk.byteLength);
				} else {
					throw new TypeError('TextDecoderStream expects BufferSource chunks');
				}
				const text = decoder.decode(data, { stream: true });
				if (text.length > 0) controller.enqueue(text);
			},
			flush(controller) {
				const text = decoder.decode();
				if (text.length > 0) controller.enqueue(text);
			}
		});
		this._encoding = decoder.encoding || 'utf-8';
		this._fatal = !!(options && options.fatal);
		this._ignoreBOM = !!(options && options.ignoreBOM);
	}
	get encoding() { return this._encoding; }
	get fatal() { return this._fatal; }
	get ignoreBOM() { return this._ignoreBOM; }
}

class IdentityTransformStream extends TransformStream {
	constructor() {
		super();
	}
}

globalThis.TextEncoderStream = TextEncoderStream;
globalThis.TextDecoderStream = TextDecoderStream;
globalThis.IdentityTransformStream = IdentityTransformStream;

})();
`

// setupTextStreams evaluates the text stream polyfills.
// Must run after setupStreams and setupWebAPIs (for TextEncoder/TextDecoder).
func setupTextStreams(vm *quickjs.VM, _ *eventLoop) error {
	if err := evalDiscard(vm, textStreamsJS); err != nil {
		return fmt.Errorf("evaluating textstreams.js: %w", err)
	}
	return nil
}
