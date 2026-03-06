package webapi

import (
	"encoding/json"
	"fmt"
	"strings"

	whatwgUrl "github.com/nlnwa/whatwg-url/url"

	"github.com/cryguy/worker/v2/internal/core"
	"github.com/cryguy/worker/v2/internal/eventloop"
)

// webAPIsJS defines the Web API classes (Headers, Request, Response, URL,
// URLSearchParams, TextEncoder, TextDecoder) in JavaScript.
const webAPIsJS = `
class Headers {
	constructor(init) {
		this._map = {};
		if (init) {
			if (init instanceof Headers) {
				for (const [k, v] of init.entries()) {
					const key = k.toLowerCase();
					if (!this._map[key]) this._map[key] = [];
					this._map[key].push(v);
				}
			} else if (Array.isArray(init)) {
				for (const [k, v] of init) {
					const key = k.toLowerCase();
					if (!this._map[key]) this._map[key] = [];
					this._map[key].push(String(v));
				}
			} else {
				for (const [k, v] of Object.entries(init)) this._map[k.toLowerCase()] = [String(v)];
			}
		}
	}
	get(name) { return this._map[name.toLowerCase()]?.join(', ') ?? null; }
	set(name, value) { this._map[name.toLowerCase()] = [String(value)]; }
	has(name) { return name.toLowerCase() in this._map; }
	delete(name) { delete this._map[name.toLowerCase()]; }
	append(name, value) {
		const key = name.toLowerCase();
		if (!this._map[key]) this._map[key] = [];
		this._map[key].push(String(value));
	}
	forEach(cb) { for (const [k, vs] of Object.entries(this._map)) cb(vs.join(', '), k, this); }
	entries() { return Object.entries(this._map).map(([k, vs]) => [k, vs.join(', ')])[Symbol.iterator](); }
	keys() { return Object.keys(this._map)[Symbol.iterator](); }
	values() { return Object.entries(this._map).map(([, vs]) => vs.join(', '))[Symbol.iterator](); }
	getSetCookie() { return [...(this._map['set-cookie'] || [])]; }
	get [Symbol.toStringTag]() { return 'Headers'; }
	[Symbol.iterator]() { return this.entries(); }
}

class URL {
	constructor(input, base) {
		if (typeof input === 'object' && input !== null) input = String(input);
		if (base !== undefined && typeof base === 'object' && base !== null) base = String(base);
		const parsed = JSON.parse(__parseURL(input, base || ''));
		if (parsed.error) throw new TypeError(parsed.error);
		this._applyParsed(parsed);
		this._searchParams = new URLSearchParams(this._search);
		this._searchParams._url = this;
	}
	_applyParsed(parsed) {
		this._protocol = parsed.protocol;
		this._hostname = parsed.hostname;
		this._port = parsed.port;
		this._pathname = parsed.pathname;
		this._search = parsed.search;
		this._hash = parsed.hash;
		this._origin = parsed.origin;
		this._host = parsed.host;
		this._username = parsed.username || '';
		this._password = parsed.password || '';
		this._href = parsed.href;
	}
	_set(field, value) {
		const parsed = JSON.parse(__setURL(this._href, field, String(value)));
		if (parsed.error) return;
		this._applyParsed(parsed);
	}
	get href() { return this._href; }
	set href(v) {
		if (typeof v === 'object' && v !== null) v = String(v);
		const parsed = JSON.parse(__setURL(this._href, 'href', v));
		if (parsed.error) throw new TypeError(parsed.error);
		this._applyParsed(parsed);
		this._rebuildSearchParams();
	}
	get protocol() { return this._protocol; }
	set protocol(v) { this._set('protocol', v); }
	get hostname() { return this._hostname; }
	set hostname(v) { this._set('hostname', v); }
	get port() { return this._port; }
	set port(v) { this._set('port', v); }
	get pathname() { return this._pathname; }
	set pathname(v) { this._set('pathname', v); }
	get search() { return this._search; }
	set search(v) {
		this._set('search', v);
		this._rebuildSearchParams();
	}
	get hash() { return this._hash; }
	set hash(v) { this._set('hash', v); }
	get origin() { return this._origin; }
	get host() { return this._host; }
	set host(v) { this._set('host', v); }
	get username() { return this._username; }
	set username(v) { this._set('username', v); }
	get password() { return this._password; }
	set password(v) { this._set('password', v); }
	get searchParams() { return this._searchParams; }
	_rebuildSearchParams() {
		if (!this._searchParams) return;
		const s = this._search.startsWith('?') ? this._search.slice(1) : this._search;
		this._searchParams._entries = URLSearchParams._parseStr(s);
	}
	toString() { return this.href; }
	toJSON() { return this.href; }
	get [Symbol.toStringTag]() { return 'URL'; }
	static parse(url, base) {
		try { return new URL(url, base); } catch { return null; }
	}
	static canParse(url, base) {
		try {
			if (url === null || url === undefined) {
				url = String(url);
			}
			if (base !== undefined && base !== null) {
				base = String(base);
			}
			new URL(url, base);
			return true;
		} catch {
			return false;
		}
	}
}

class URLSearchParams {
	static _pctDecode(s) {
		s = s.replace(/\+/g, ' ');
		try { return decodeURIComponent(s); } catch {}
		var r = '', i = 0;
		while (i < s.length) {
			if (s[i] === '%' && i + 2 < s.length && /[0-9A-Fa-f]/.test(s[i+1]) && /[0-9A-Fa-f]/.test(s[i+2])) {
				var enc = '';
				while (i < s.length && s[i] === '%' && i + 2 < s.length && /[0-9A-Fa-f]/.test(s[i+1]) && /[0-9A-Fa-f]/.test(s[i+2])) {
					enc += s.slice(i, i + 3); i += 3;
				}
				try { r += decodeURIComponent(enc); } catch { r += enc; }
			} else { r += s[i]; i++; }
		}
		return r;
	}
	static _parseStr(s) {
		var entries = [];
		if (!s) return entries;
		var pairs = s.split('&');
		for (var i = 0; i < pairs.length; i++) {
			if (pairs[i] === '') continue;
			var idx = pairs[i].indexOf('=');
			var name, value;
			if (idx === -1) { name = pairs[i]; value = ''; }
			else { name = pairs[i].slice(0, idx); value = pairs[i].slice(idx + 1); }
			entries.push([URLSearchParams._pctDecode(name), URLSearchParams._pctDecode(value)]);
		}
		return entries;
	}
	static _formEncode(s) {
		return encodeURIComponent(s).replace(/%20/g, '+').replace(/%2[aA]/g, '*').replace(/[!'()~]/g, function(c) {
			return '%' + c.charCodeAt(0).toString(16).toUpperCase();
		});
	}
	constructor(init) {
		this._entries = [];
		if (init instanceof URLSearchParams) {
			this._entries = init._entries.map(e => [...e]);
		} else if (Array.isArray(init)) {
			for (const pair of init) this._entries.push([String(pair[0]), String(pair[1])]);
		} else if (typeof init === 'object' && init !== null && !Array.isArray(init) && !(init instanceof URLSearchParams)) {
			for (const [k, v] of Object.entries(init)) this._entries.push([String(k), String(v)]);
		} else if (typeof init === 'string') {
			const s = init.startsWith('?') ? init.slice(1) : init;
			this._entries = URLSearchParams._parseStr(s);
		}
	}
	get(name) {
		name = String(name);
		const e = this._entries.find(([k]) => k === name);
		return e ? e[1] : null;
	}
	has(name, value) {
		name = String(name);
		return (arguments.length > 1 && value !== undefined)
			? this._entries.some(([k, v]) => k === name && v === String(value))
			: this._entries.some(([k]) => k === name);
	}
	get size() { return this._entries.length; }
	toString() { return this._entries.map(([k, v]) => URLSearchParams._formEncode(k) + '=' + URLSearchParams._formEncode(v)).join('&'); }
	forEach(cb) { for (var i = 0; i < this._entries.length; i++) cb(this._entries[i][1], this._entries[i][0], this); }
	entries() {
		var self = this, i = 0;
		return { next: function() { return i >= self._entries.length ? {done:true} : {done:false, value:self._entries[i++]}; }, [Symbol.iterator]: function() { return this; } };
	}
	keys() {
		var self = this, i = 0;
		return { next: function() { return i >= self._entries.length ? {done:true} : {done:false, value:self._entries[i++][0]}; }, [Symbol.iterator]: function() { return this; } };
	}
	values() {
		var self = this, i = 0;
		return { next: function() { return i >= self._entries.length ? {done:true} : {done:false, value:self._entries[i++][1]}; }, [Symbol.iterator]: function() { return this; } };
	}
	get [Symbol.toStringTag]() { return 'URLSearchParams'; }
	[Symbol.iterator]() { return this.entries(); }
}

class Request {
	constructor(input, init) {
		init = init || {};
		this._bodyUsed = false;
		if (input instanceof Request) {
			this.url = input.url;
			this.method = input.method;
			this.headers = new Headers(input.headers);
			this._body = input._body;
			this.redirect = input.redirect;
			this.mode = input.mode;
			this.credentials = input.credentials;
			this.cache = input.cache;
			this.referrer = input.referrer;
			this.referrerPolicy = input.referrerPolicy;
			this.integrity = input.integrity;
			this.keepalive = input.keepalive;
			this.signal = input.signal;
			this.destination = input.destination;
		} else {
			try { this.url = new URL(String(input)).href; } catch(e) { this.url = String(input); }
			this.method = (init.method || 'GET').toUpperCase();
			this.headers = new Headers(init.headers);
			this._body = init.body !== undefined ? init.body : null;
		}
		if (init.method) this.method = init.method.toUpperCase();
		if (init.headers) this.headers = new Headers(init.headers);
		if (init.body !== undefined) this._body = init.body;
		if (['CONNECT','TRACE','TRACK'].indexOf(this.method) !== -1) throw new TypeError('Forbidden method: ' + this.method);
		this.redirect = init.redirect || this.redirect || 'follow';
		this.mode = init.mode || this.mode || 'cors';
		this.credentials = init.credentials || this.credentials || 'same-origin';
		this.cache = init.cache || this.cache || 'default';
		this.referrer = init.referrer !== undefined ? init.referrer : (this.referrer !== undefined ? this.referrer : 'about:client');
		this.referrerPolicy = init.referrerPolicy || this.referrerPolicy || '';
		this.integrity = init.integrity || this.integrity || '';
		this.keepalive = init.keepalive !== undefined ? !!init.keepalive : (this.keepalive !== undefined ? this.keepalive : false);
		this.signal = init.signal !== undefined ? init.signal : (this.signal !== undefined ? this.signal : null);
		this.destination = this.destination || '';
	}
	get body() {
		if (this._body === null || this._body === undefined) return null;
		if (this._body instanceof ReadableStream) return this._body;
		const content = this._body;
		const stream = new ReadableStream({
			start(controller) {
				if (typeof content === 'string') {
					controller.enqueue(new TextEncoder().encode(content));
				} else if (content instanceof ArrayBuffer) {
					controller.enqueue(new Uint8Array(content));
				} else if (ArrayBuffer.isView(content)) {
					controller.enqueue(new Uint8Array(content.buffer, content.byteOffset, content.byteLength));
				} else {
					controller.enqueue(new TextEncoder().encode(String(content)));
				}
				controller.close();
			}
		});
		this._body = stream;
		return stream;
	}
	get bodyUsed() {
		return this._bodyUsed || (this._body instanceof ReadableStream && this._body._locked);
	}
	async text() {
		if (this._body instanceof ReadableStream) {
			if (this._bodyUsed) throw new TypeError('body already consumed');
			this._bodyUsed = true;
			const reader = this._body.getReader();
			const chunks = [];
			while (true) {
				const {done, value} = await reader.read();
				if (done) break;
				chunks.push(value);
			}
			const dec = new TextDecoder();
			return chunks.map(c => dec.decode(c, {stream: true})).join('') + dec.decode();
		}
		return this._body !== null && this._body !== undefined ? String(this._body) : '';
	}
	async json() { return JSON.parse(await this.text()); }
	async arrayBuffer() {
		if (this._body instanceof ReadableStream) {
			if (this._bodyUsed) throw new TypeError('body already consumed');
			this._bodyUsed = true;
			const reader = this._body.getReader();
			const chunks = [];
			while (true) {
				const {done, value} = await reader.read();
				if (done) break;
				chunks.push(value);
			}
			const totalLen = chunks.reduce((s, c) => s + c.byteLength, 0);
			const result = new Uint8Array(totalLen);
			let offset = 0;
			for (const c of chunks) { result.set(c, offset); offset += c.byteLength; }
			return result.buffer;
		}
		const t = this._body !== null && this._body !== undefined ? String(this._body) : '';
		const enc = new TextEncoder();
		return enc.encode(t).buffer;
	}
	async bytes() {
		if (this._body instanceof ReadableStream) {
			if (this._bodyUsed) throw new TypeError('body already consumed');
			this._bodyUsed = true;
			const reader = this._body.getReader();
			const chunks = [];
			while (true) {
				const {done, value} = await reader.read();
				if (done) break;
				chunks.push(value);
			}
			const totalLen = chunks.reduce((s, c) => s + c.byteLength, 0);
			const result = new Uint8Array(totalLen);
			let offset = 0;
			for (const c of chunks) { result.set(c, offset); offset += c.byteLength; }
			return result;
		}
		const t = this._body !== null && this._body !== undefined ? String(this._body) : '';
		return new TextEncoder().encode(t);
	}
	clone() { return new Request(this); }
	get [Symbol.toStringTag]() { return 'Request'; }
}

class Response {
	constructor(body, init) {
		init = init || {};
		this._body = body !== undefined && body !== null ? body : null;
		this._bodyUsed = false;
		this.type = 'default';
		this.status = init.status !== undefined ? init.status : 200;
		if (init.status !== undefined && init.status !== 0 && (init.status < 100 || init.status > 599)) {
			throw new RangeError('Invalid status code: ' + init.status);
		}
		this.statusText = init.statusText || '';
		this.headers = new Headers(init.headers);
		this.redirected = false;
		this.url = init.url || '';
		this.webSocket = init.webSocket || null;
	}
	get ok() { return this.status >= 200 && this.status < 300; }
	get body() {
		if (this._body === null || this._body === undefined) return null;
		if (this._body instanceof ReadableStream) return this._body;
		const content = this._body;
		const stream = new ReadableStream({
			start(controller) {
				if (typeof content === 'string') {
					controller.enqueue(new TextEncoder().encode(content));
				} else if (content instanceof ArrayBuffer) {
					controller.enqueue(new Uint8Array(content));
				} else if (ArrayBuffer.isView(content)) {
					controller.enqueue(new Uint8Array(content.buffer, content.byteOffset, content.byteLength));
				} else {
					controller.enqueue(new TextEncoder().encode(String(content)));
				}
				controller.close();
			}
		});
		this._body = stream;
		return stream;
	}
	get bodyUsed() {
		return this._bodyUsed || (this._body instanceof ReadableStream && this._body._locked);
	}
	async text() {
		if (this._body instanceof ReadableStream) {
			if (this._bodyUsed) throw new TypeError('body already consumed');
			this._bodyUsed = true;
			const reader = this._body.getReader();
			const chunks = [];
			while (true) {
				const {done, value} = await reader.read();
				if (done) break;
				chunks.push(value);
			}
			const dec = new TextDecoder();
			return chunks.map(c => dec.decode(c, {stream: true})).join('') + dec.decode();
		}
		return this._body !== null && this._body !== undefined ? String(this._body) : '';
	}
	async json() { return JSON.parse(await this.text()); }
	async arrayBuffer() {
		if (this._body instanceof ReadableStream) {
			if (this._bodyUsed) throw new TypeError('body already consumed');
			this._bodyUsed = true;
			const reader = this._body.getReader();
			const chunks = [];
			while (true) {
				const {done, value} = await reader.read();
				if (done) break;
				chunks.push(value);
			}
			const totalLen = chunks.reduce((s, c) => s + c.byteLength, 0);
			const result = new Uint8Array(totalLen);
			let offset = 0;
			for (const c of chunks) { result.set(c, offset); offset += c.byteLength; }
			return result.buffer;
		}
		const t = this._body !== null && this._body !== undefined ? String(this._body) : '';
		const enc = new TextEncoder();
		return enc.encode(t).buffer;
	}
	async bytes() {
		if (this._body instanceof ReadableStream) {
			if (this._bodyUsed) throw new TypeError('body already consumed');
			this._bodyUsed = true;
			const reader = this._body.getReader();
			const chunks = [];
			while (true) {
				const {done, value} = await reader.read();
				if (done) break;
				chunks.push(value);
			}
			const totalLen = chunks.reduce((s, c) => s + c.byteLength, 0);
			const result = new Uint8Array(totalLen);
			let offset = 0;
			for (const c of chunks) { result.set(c, offset); offset += c.byteLength; }
			return result;
		}
		const t = this._body !== null && this._body !== undefined ? String(this._body) : '';
		return new TextEncoder().encode(t);
	}
	clone() {
		if (this._bodyUsed) throw new TypeError('Cannot clone a consumed response');
		const r = new Response(this._body, {
			status: this.status,
			statusText: this.statusText,
			headers: new Headers(this.headers),
		});
		r.type = this.type;
		r.url = this.url;
		r.redirected = this.redirected;
		return r;
	}
	static json(data, init) {
		init = init || {};
		const body = JSON.stringify(data);
		const headers = new Headers(init.headers);
		if (!headers.has('content-type')) headers.set('content-type', 'application/json');
		return new Response(body, { ...init, headers });
	}
	static redirect(url, status) {
		status = status || 302;
		if ([301, 302, 303, 307, 308].indexOf(status) === -1) {
			throw new RangeError('Invalid redirect status: ' + status);
		}
		return new Response(null, { status, headers: { location: url } });
	}
	static error() {
		const r = new Response(null, { status: 0, statusText: '' });
		r.type = 'error';
		r.status = 0;
		return r;
	}
	get [Symbol.toStringTag]() { return 'Response'; }
}

if (typeof TextEncoder === 'undefined') {
	globalThis.TextEncoder = class TextEncoder {
		get encoding() { return 'utf-8'; }
		encode(str) {
			str = String(str);
			const buf = [];
			for (let i = 0; i < str.length; i++) {
				let c = str.charCodeAt(i);
				if (c < 0x80) {
					buf.push(c);
				} else if (c < 0x800) {
					buf.push(0xc0 | (c >> 6), 0x80 | (c & 0x3f));
				} else if (c >= 0xd800 && c <= 0xdbff && i + 1 < str.length) {
					const next = str.charCodeAt(++i);
					const cp = ((c - 0xd800) << 10) + (next - 0xdc00) + 0x10000;
					buf.push(0xf0 | (cp >> 18), 0x80 | ((cp >> 12) & 0x3f), 0x80 | ((cp >> 6) & 0x3f), 0x80 | (cp & 0x3f));
				} else {
					buf.push(0xe0 | (c >> 12), 0x80 | ((c >> 6) & 0x3f), 0x80 | (c & 0x3f));
				}
			}
			return new Uint8Array(buf);
		}
		encodeInto(source, destination) {
			source = String(source);
			const encoded = this.encode(source);
			const written = Math.min(encoded.length, destination.length);
			destination.set(encoded.subarray(0, written));
			let read = 0;
			let byteCount = 0;
			for (let i = 0; i < source.length && byteCount < written; i++) {
				let c = source.charCodeAt(i);
				let charBytes;
				if (c < 0x80) charBytes = 1;
				else if (c < 0x800) charBytes = 2;
				else if (c >= 0xd800 && c <= 0xdbff) { charBytes = 4; }
				else charBytes = 3;
				if (byteCount + charBytes > written) break;
				byteCount += charBytes;
				read++;
				if (charBytes === 4) i++;
			}
			return { read, written: byteCount };
		}
		get [Symbol.toStringTag]() { return 'TextEncoder'; }
	};
}

globalThis.TextDecoder = class TextDecoder {
		constructor(encoding, options) {
			var label = (encoding || 'utf-8').toLowerCase().trim();
			// Normalize label aliases to canonical names.
			if (label === 'utf8' || label === 'unicode-1-1-utf-8') label = 'utf-8';
			else if (label === 'latin1' || label === 'iso-8859-1' || label === 'ascii' ||
			         label === 'us-ascii' || label === 'iso8859-1' || label === 'iso_8859-1') label = 'windows-1252';
			this._encoding = label;
			this._fatal = !!(options && options.fatal);
			this._ignoreBOM = !!(options && options.ignoreBOM);
			this._bomSeen = false;
			this._pending = [];
		}
		get encoding() { return this._encoding; }
		get fatal() { return this._fatal; }
		get ignoreBOM() { return this._ignoreBOM; }
		get [Symbol.toStringTag]() { return 'TextDecoder'; }
		decode(buf, options) {
			var stream = !!(options && options.stream);
			// Build byte array: pending bytes prepended to new input.
			var incoming;
			if (!buf) {
				incoming = new Uint8Array(0);
			} else if (buf instanceof ArrayBuffer) {
				incoming = new Uint8Array(buf);
			} else if (ArrayBuffer.isView(buf)) {
				incoming = new Uint8Array(buf.buffer, buf.byteOffset, buf.byteLength);
			} else {
				incoming = new Uint8Array(buf);
			}
			// Prepend any pending bytes from previous stream call.
			var bytes;
			if (this._pending.length > 0) {
				bytes = new Uint8Array(this._pending.length + incoming.length);
				bytes.set(this._pending);
				bytes.set(incoming, this._pending.length);
				this._pending = [];
			} else {
				bytes = incoming;
			}
			var start = 0;
			// BOM handling: strip UTF-8 BOM (EF BB BF) on first decode unless ignoreBOM.
			// Only attempt BOM detection once we have at least 3 bytes, or on the
			// final (non-stream) flush. While streaming with fewer than 3 bytes,
			// defer the check by leaving _bomSeen false.
			if (!this._bomSeen) {
				if (bytes.length >= 3) {
					// We have enough bytes to make the BOM decision.
					if (!this._ignoreBOM &&
					    bytes[0] === 0xEF && bytes[1] === 0xBB && bytes[2] === 0xBF) {
						start = 3;
					}
					this._bomSeen = true;
				} else if (!stream) {
					// Final flush with < 3 bytes: no BOM possible, mark seen.
					this._bomSeen = true;
				}
				// else: streaming with < 3 bytes — keep _bomSeen false, buffer below.
			}
			var result = '';
			var i = start;
			while (i < bytes.length) {
				var b = bytes[i];
				if (b < 0x80) {
					result += String.fromCharCode(b);
					i++;
				} else if ((b & 0xe0) === 0xc0) {
					// 2-byte sequence
					if (i + 1 >= bytes.length) {
						// Incomplete: need 1 more byte
						if (stream) { this._pending = Array.from(bytes.subarray(i)); break; }
						if (this._fatal) throw new TypeError('The encoded data was not valid utf-8');
						result += '\uFFFD'; i++;
					} else {
						var b1 = bytes[i+1];
						if ((b1 & 0xc0) !== 0x80) {
							if (this._fatal) throw new TypeError('The encoded data was not valid utf-8');
							result += '\uFFFD'; i++;
						} else {
							result += String.fromCharCode(((b & 0x1f) << 6) | (b1 & 0x3f));
							i += 2;
						}
					}
				} else if ((b & 0xf0) === 0xe0) {
					// 3-byte sequence
					if (i + 2 >= bytes.length) {
						// Incomplete
						if (stream) { this._pending = Array.from(bytes.subarray(i)); break; }
						if (this._fatal) throw new TypeError('The encoded data was not valid utf-8');
						result += '\uFFFD'; i++;
					} else {
						var b1 = bytes[i+1], b2 = bytes[i+2];
						if ((b1 & 0xc0) !== 0x80 || (b2 & 0xc0) !== 0x80) {
							if (this._fatal) throw new TypeError('The encoded data was not valid utf-8');
							result += '\uFFFD'; i++;
						} else {
							result += String.fromCharCode(((b & 0x0f) << 12) | ((b1 & 0x3f) << 6) | (b2 & 0x3f));
							i += 3;
						}
					}
				} else if ((b & 0xf8) === 0xf0) {
					// 4-byte sequence
					if (i + 3 >= bytes.length) {
						// Incomplete
						if (stream) { this._pending = Array.from(bytes.subarray(i)); break; }
						if (this._fatal) throw new TypeError('The encoded data was not valid utf-8');
						result += '\uFFFD'; i++;
					} else {
						var b1 = bytes[i+1], b2 = bytes[i+2], b3 = bytes[i+3];
						if ((b1 & 0xc0) !== 0x80 || (b2 & 0xc0) !== 0x80 || (b3 & 0xc0) !== 0x80) {
							if (this._fatal) throw new TypeError('The encoded data was not valid utf-8');
							result += '\uFFFD'; i++;
						} else {
							var cp = ((b & 0x07) << 18) | ((b1 & 0x3f) << 12) | ((b2 & 0x3f) << 6) | (b3 & 0x3f);
							result += String.fromCodePoint(cp);
							i += 4;
						}
					}
				} else {
					// Invalid lead byte (continuation byte without lead, or 0xF8-0xFF)
					if (this._fatal) throw new TypeError('The encoded data was not valid utf-8');
					result += '\uFFFD'; i++;
				}
			}
			return result;
		}
	};

globalThis.Headers = Headers;
globalThis.URL = URL;
globalThis.URLSearchParams = URLSearchParams;
globalThis.Request = Request;
globalThis.Response = Response;
`

// bufferSourceJS provides __bufferSourceToB64 and __b64ToBuffer helpers.
const bufferSourceJS = `
globalThis.__bufferSourceToB64 = function(data) {
	var bytes;
	if (data instanceof ArrayBuffer) {
		bytes = new Uint8Array(data);
	} else if (ArrayBuffer.isView(data)) {
		bytes = new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
	} else if (typeof data === 'string') {
		return btoa(data);
	} else {
		bytes = new Uint8Array(data);
	}
	var parts = [];
	for (var i = 0; i < bytes.length; i += 8192) {
		var chunk = bytes.subarray(i, Math.min(i + 8192, bytes.length));
		parts.push(String.fromCharCode.apply(null, chunk));
	}
	return btoa(parts.join(''));
};

globalThis.__b64ToBuffer = function(b64) {
	var binary = atob(b64);
	var bytes = new Uint8Array(binary.length);
	for (var i = 0; i < binary.length; i++) {
		bytes[i] = binary.charCodeAt(i);
	}
	return bytes.buffer;
};
`

// urlSearchParamsExtJS patches URLSearchParams with mutation methods and URL sync.
const urlSearchParamsExtJS = `
(function() {
var USP = URLSearchParams.prototype;

USP._sync = function() {
	if (this._url) {
		var s = this.toString();
		this._url.search = s ? '?' + s : '';
	}
};

USP.getAll = function(name) {
	name = String(name);
	return this._entries.filter(function(e) { return e[0] === name; }).map(function(e) { return e[1]; });
};

USP.set = function(name, value) {
	name = String(name);
	var s = String(value);
	var found = false;
	var filtered = [];
	for (var i = 0; i < this._entries.length; i++) {
		var entry = this._entries[i];
		if (entry[0] === name) {
			if (!found) {
				filtered.push([name, s]);
				found = true;
			}
		} else {
			filtered.push(entry);
		}
	}
	if (!found) filtered.push([name, s]);
	this._entries = filtered;
	this._sync();
};

USP.append = function(name, value) {
	this._entries.push([String(name), String(value)]);
	this._sync();
};

USP['delete'] = function(name, value) {
	name = String(name);
	if (arguments.length > 1 && value !== undefined) {
		var v = String(value);
		this._entries = this._entries.filter(function(e) { return !(e[0] === name && e[1] === v); });
	} else {
		this._entries = this._entries.filter(function(e) { return e[0] !== name; });
	}
	this._sync();
};

USP.sort = function() {
	this._entries.sort(function(a, b) { return a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0; });
	this._sync();
};

})();
`

// URLParsed is the JSON structure returned by __parseURL.
type URLParsed struct {
	Href     string `json:"href"`
	Protocol string `json:"protocol"`
	Hostname string `json:"hostname"`
	Port     string `json:"port"`
	Pathname string `json:"pathname"`
	Search   string `json:"search"`
	Hash     string `json:"hash"`
	Origin   string `json:"origin"`
	Host     string `json:"host"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// whatwgParser is a package-level URL parser using the WHATWG URL standard.
var whatwgParser = whatwgUrl.NewParser(whatwgUrl.WithAcceptInvalidCodepoints())

// __escNull replaces literal U+0000 with %00 so C-string FFI doesn't truncate.
func __escNull(s string) string {
	return strings.ReplaceAll(s, "\x00", "%00")
}

// urlOrigin computes the origin for a parsed URL per the WHATWG spec.
func urlOrigin(u *whatwgUrl.Url) string {
	scheme := u.Scheme()
	switch scheme {
	case "http", "https", "ftp", "ws", "wss":
		return u.Protocol() + "//" + u.Host()
	case "blob":
		// Try parsing the path as a URL to get its origin.
		inner, err := whatwgParser.Parse(u.Pathname())
		if err == nil {
			return urlOrigin(inner)
		}
		return "null"
	default:
		return "null"
	}
}

// urlToParsed extracts all fields from a whatwg-url Url into a URLParsed.
func urlToParsed(u *whatwgUrl.Url) *URLParsed {
	return &URLParsed{
		Href:     __escNull(u.Href(false)),
		Protocol: __escNull(u.Protocol()),
		Hostname: __escNull(u.Hostname()),
		Port:     u.Port(),
		Pathname: __escNull(u.Pathname()),
		Search:   __escNull(u.Search()),
		Hash:     __escNull(u.Hash()),
		Origin:   __escNull(urlOrigin(u)),
		Host:     __escNull(u.Host()),
		Username: __escNull(u.Username()),
		Password: __escNull(u.Password()),
	}
}

func ParseURL(rawURL, base string) (*URLParsed, error) {
	var u *whatwgUrl.Url
	var err error

	if base != "" {
		u, err = whatwgParser.ParseRef(base, rawURL)
	} else {
		u, err = whatwgParser.Parse(rawURL)
	}
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %s", rawURL)
	}

	return urlToParsed(u), nil
}

// SetURL applies a setter operation on a parsed URL and returns the updated URLParsed.
func SetURL(href, field, value string) (*URLParsed, error) {
	u, err := whatwgParser.Parse(href)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %s", href)
	}
	switch field {
	case "protocol":
		u.SetProtocol(value)
	case "username":
		u.SetUsername(value)
	case "password":
		u.SetPassword(value)
	case "host":
		u.SetHost(value)
	case "hostname":
		u.SetHostname(value)
	case "port":
		u.SetPort(value)
	case "pathname":
		u.SetPathname(value)
	case "search":
		u.SetSearch(value)
	case "hash":
		u.SetHash(value)
	case "href":
		// No SetHref — re-parse the value as a new URL.
		u2, err2 := whatwgParser.Parse(value)
		if err2 != nil {
			return nil, fmt.Errorf("invalid URL: %s", value)
		}
		return urlToParsed(u2), nil
	default:
		return nil, fmt.Errorf("unknown URL field: %s", field)
	}
	return urlToParsed(u), nil
}

// SetupWebAPIs registers Go-backed helpers and evaluates the JS class
// definitions that form the Web API surface available to workers.
func SetupWebAPIs(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	// Register Go-backed URL parser.
	if err := rt.RegisterFunc("__parseURL", func(rawURL, base string) (string, error) {
		parsed, err := ParseURL(rawURL, base)
		if err != nil {
			return fmt.Sprintf(`{"error":%q}`, err.Error()), nil
		}
		data, _ := json.Marshal(parsed)
		return string(data), nil
	}); err != nil {
		return err
	}

	// Register Go-backed URL setter.
	if err := rt.RegisterFunc("__setURL", func(href, field, value string) (string, error) {
		parsed, err := SetURL(href, field, value)
		if err != nil {
			return fmt.Sprintf(`{"error":%q}`, err.Error()), nil
		}
		data, _ := json.Marshal(parsed)
		return string(data), nil
	}); err != nil {
		return err
	}

	// Evaluate the JS class definitions.
	if err := rt.Eval(webAPIsJS); err != nil {
		return fmt.Errorf("evaluating webapi.js: %w", err)
	}

	// Evaluate __bufferSourceToB64 and __b64ToBuffer helpers
	// (these depend on btoa/atob, so must come after encoding setup,
	// but we install them here so they're available early -- they'll
	// work once btoa/atob are set up by SetupEncoding).
	return rt.Eval(bufferSourceJS)
}

// SetupURLSearchParamsExt evaluates the URLSearchParams extension polyfill.
func SetupURLSearchParamsExt(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	return rt.Eval(urlSearchParamsExtJS)
}
