package webapi

import (
	"fmt"

	"github.com/cryguy/worker/v2/internal/core"
	"github.com/cryguy/worker/v2/internal/eventloop"
)

// encodingJS implements global atob() and btoa() as pure JavaScript.
const encodingJS = `
(function() {
	const _e = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/';
	const _d = new Uint8Array(128);
	for (let i = 0; i < _e.length; i++) _d[_e.charCodeAt(i)] = i;
	const _v = new Uint8Array(128);
	for (let i = 0; i < _e.length; i++) _v[_e.charCodeAt(i)] = 1;
	_v[61] = 1; // '='

	globalThis.btoa = function(data) {
		if (arguments.length < 1) throw new TypeError("btoa requires at least 1 argument(s)");
		const s = String(data);
		const len = s.length;
		if (len === 0) return '';
		const bytes = new Uint8Array(len);
		for (let i = 0; i < len; i++) {
			const ch = s.charCodeAt(i);
			if (ch > 255) throw new Error("btoa: string contains characters outside of the Latin1 range");
			bytes[i] = ch;
		}
		const out = [];
		for (let i = 0; i < len; i += 3) {
			const a = bytes[i];
			const b = i + 1 < len ? bytes[i + 1] : 0;
			const c = i + 2 < len ? bytes[i + 2] : 0;
			out.push(
				_e[a >> 2],
				_e[((a & 3) << 4) | (b >> 4)],
				i + 1 < len ? _e[((b & 15) << 2) | (c >> 6)] : '=',
				i + 2 < len ? _e[c & 63] : '='
			);
		}
		return out.join('');
	};

	globalThis.atob = function(data) {
		if (arguments.length < 1) throw new TypeError("atob requires at least 1 argument(s)");
		let b64 = String(data);
		b64 = b64.replace(/[\t\n\f\r ]/g, '');
		if (b64.length === 0) return '';
		if (b64.length % 4 === 0) {
			if (b64[b64.length - 1] === '=') {
				b64 = b64.slice(0, b64[b64.length - 2] === '=' ? -2 : -1);
			}
		}
		if (b64.length % 4 === 1) {
			throw new Error("atob: invalid base64 string");
		}
		for (let i = 0; i < b64.length; i++) {
			const ch = b64.charCodeAt(i);
			if (ch >= 128 || !_v[ch] || ch === 61) {
				throw new Error("atob: invalid base64 string");
			}
		}
		while (b64.length % 4 !== 0) b64 += '=';
		let pad = 0;
		if (b64[b64.length - 1] === '=') pad++;
		if (b64[b64.length - 2] === '=') pad++;
		const outLen = (b64.length / 4) * 3 - pad;
		const bytes = new Uint8Array(outLen);
		let j = 0;
		for (let i = 0; i < b64.length; i += 4) {
			const a = _d[b64.charCodeAt(i)];
			const b = _d[b64.charCodeAt(i + 1)];
			const c = _d[b64.charCodeAt(i + 2)];
			const d = _d[b64.charCodeAt(i + 3)];
			bytes[j++] = (a << 2) | (b >> 4);
			if (j < outLen) bytes[j++] = ((b & 15) << 4) | (c >> 2);
			if (j < outLen) bytes[j++] = ((c & 3) << 6) | d;
		}
		const CHUNK = 4096;
		let result = '';
		for (let i = 0; i < outLen; i += CHUNK) {
			const end = Math.min(i + CHUNK, outLen);
			result += String.fromCharCode.apply(null, bytes.subarray(i, end));
		}
		return result;
	};
})();
`

// SetupEncoding evaluates the pure-JS atob/btoa implementations.
func SetupEncoding(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	if err := rt.Eval(encodingJS); err != nil {
		return fmt.Errorf("evaluating encoding.js: %w", err)
	}
	return nil
}
