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
var _hdrIterProto = Object.create(Object.getPrototypeOf(Object.getPrototypeOf([][Symbol.iterator]())));
Object.defineProperty(_hdrIterProto, 'next', {
	value: function() {
		var entries = this._h._sortedEntries();
		if (this._i >= entries.length) return { value: undefined, done: true };
		var e = entries[this._i++];
		var k = this._k;
		if (k === 0) return { value: e[0], done: false };
		if (k === 1) return { value: e[1], done: false };
		return { value: e, done: false };
	}, writable: true, enumerable: true, configurable: true
});
_hdrIterProto[Symbol.iterator] = function() { return this; };
function _hdrNormVal(v) {
	var s = String(v);
	s = s.replace(/^[\t\n\r ]+|[\t\n\r ]+$/g, '');
	for (var i = 0; i < s.length; i++) {
		var c = s.charCodeAt(i);
		if (c === 0 || c === 0x0a || c === 0x0d || c > 0xff) throw new TypeError('Invalid header value');
	}
	return s;
}
function _hdrNormName(n) {
	var s = String(n);
	if (!/^[!#$%&'*+\-.^_|~0-9A-Za-z\x60]+$/.test(s)) throw new TypeError('Invalid header name: ' + s);
	return s.toLowerCase();
}
var _forbiddenRequestHeaders = ['accept-charset','accept-encoding','access-control-request-headers','access-control-request-method','connection','content-length','cookie','cookie2','date','dnt','expect','host','keep-alive','origin','referer','set-cookie','te','trailer','transfer-encoding','upgrade','via'];
function _isForbiddenReqHdr(name) {
	if (_forbiddenRequestHeaders.indexOf(name) !== -1) return true;
	if (name.length >= 4 && name.substring(0, 4) === 'sec-') return true;
	if (name.length >= 6 && name.substring(0, 6) === 'proxy-') return true;
	return false;
}
function _corsSafe(name, value) {
	if (value.length > 128) return false;
	if (name === 'accept') {
		for (var i = 0; i < value.length; i++) {
			var c = value.charCodeAt(i);
			if ((c < 0x20 && c !== 0x09) || c === 0x22 || c === 0x28 || c === 0x29 ||
				c === 0x3a || c === 0x3c || c === 0x3e || c === 0x3f || c === 0x40 ||
				c === 0x5b || c === 0x5c || c === 0x5d || c === 0x7b || c === 0x7d || c === 0x7f) return false;
		}
		return true;
	}
	if (name === 'accept-language' || name === 'content-language') {
		for (var i = 0; i < value.length; i++) {
			var c = value.charCodeAt(i);
			if (!((c >= 0x30 && c <= 0x39) || (c >= 0x41 && c <= 0x5a) ||
				  (c >= 0x61 && c <= 0x7a) || c === 0x20 || c === 0x2a ||
				  c === 0x2c || c === 0x2d || c === 0x2e || c === 0x3b || c === 0x3d)) return false;
		}
		return true;
	}
	if (name === 'content-type') {
		var essence = value.split(';')[0].trim().toLowerCase();
		return essence === 'application/x-www-form-urlencoded' ||
			   essence === 'multipart/form-data' || essence === 'text/plain';
	}
	if (name === 'range') return /^bytes=\d+-\d*$/.test(value);
	return false;
}
class Headers {
	constructor(init) {
		this._map = {};
		this._guard = 'none';
		if (init === undefined) return;
		if (init === null) throw new TypeError('Headers constructor: null is not allowed');
		if (typeof init !== 'object' && typeof init !== 'function') throw new TypeError('Headers constructor: invalid argument');
		var iterFn = init[Symbol.iterator];
		if (typeof iterFn === 'function') {
			for (const pair of init) {
				if (typeof pair !== 'object' && typeof pair !== 'string') throw new TypeError('Invalid header pair');
				var arr = (typeof pair === 'string') ? pair : [...pair];
				if (arr.length !== 2) throw new TypeError('Header pair must have exactly two items');
				this.append(arr[0], arr[1]);
			}
		} else {
			var ks = Reflect.ownKeys(init);
			for (var i = 0; i < ks.length; i++) {
				var key = ks[i];
				var desc = Object.getOwnPropertyDescriptor(init, key);
				if (desc !== undefined && desc.enumerable) {
					if (typeof key === 'symbol') throw new TypeError('Invalid header name');
					var nm = _hdrNormName(key);
					var vl = _hdrNormVal(init[key]);
					if (!this._map[nm]) this._map[nm] = [];
					this._map[nm].push(vl);
				}
			}
		}
	}
	get(name) {
		var key = _hdrNormName(name);
		var vs = this._map[key];
		if (!vs) return null;
		return vs.join(', ');
	}
	set(name, value) {
		var key = _hdrNormName(name);
		var val = _hdrNormVal(value);
		var g = this._guard;
		if (g === 'immutable') throw new TypeError('Headers are immutable');
		if (g === 'request' && _isForbiddenReqHdr(key)) return;
		if (g === 'request-no-cors' && !_corsSafe(key, val)) return;
		if (g === 'response' && (key === 'set-cookie' || key === 'set-cookie2')) return;
		this._map[key] = [val];
	}
	has(name) { return _hdrNormName(name) in this._map; }
	delete(name) {
		var key = _hdrNormName(name);
		var g = this._guard;
		if (g === 'immutable') throw new TypeError('Headers are immutable');
		if (g === 'request' && _isForbiddenReqHdr(key)) return;
		if (g === 'request-no-cors') {
			if (['accept','accept-language','content-language','content-type'].indexOf(key) === -1) return;
		}
		if (g === 'response' && (key === 'set-cookie' || key === 'set-cookie2')) return;
		delete this._map[key];
	}
	append(name, value) {
		var key = _hdrNormName(name);
		var val = _hdrNormVal(value);
		var g = this._guard;
		if (g === 'immutable') throw new TypeError('Headers are immutable');
		if (g === 'request' && _isForbiddenReqHdr(key)) return;
		if (g === 'request-no-cors') {
			var tmp = this._map[key] ? this._map[key].join(', ') + ', ' + val : val;
			if (!_corsSafe(key, tmp)) return;
		}
		if (g === 'response' && (key === 'set-cookie' || key === 'set-cookie2')) return;
		if (!this._map[key]) this._map[key] = [];
		this._map[key].push(val);
	}
	_sortedEntries() {
		var keys = Object.keys(this._map).sort();
		var out = [];
		for (var i = 0; i < keys.length; i++) {
			var k = keys[i];
			if (k === 'set-cookie') {
				for (var j = 0; j < this._map[k].length; j++) out.push([k, this._map[k][j]]);
			} else {
				out.push([k, this._map[k].join(', ')]);
			}
		}
		return out;
	}
	_iter(kind) {
		var it = Object.create(_hdrIterProto);
		it._h = this; it._i = 0; it._k = kind;
		return it;
	}
	forEach(cb) { for (var e = this._iter(2), r = e.next(); !r.done; r = e.next()) cb(r.value[1], r.value[0], this); }
	entries() { return this._iter(2); }
	keys() { return this._iter(0); }
	values() { return this._iter(1); }
	getSetCookie() { return this._map['set-cookie'] ? this._map['set-cookie'].slice() : []; }
	get [Symbol.toStringTag]() { return 'Headers'; }
	[Symbol.iterator]() { return this.entries(); }
}

class URL {
	constructor(input, base) {
		if (typeof input === 'object' && input !== null) input = String(input);
		else input = String(input);
		// Strip leading/trailing C0 controls and space per WHATWG URL spec.
		input = input.replace(/^[\x00-\x1f\x20]+|[\x00-\x1f\x20]+$/g, '');
		if (base !== undefined) {
			if (typeof base === 'object' && base !== null) base = String(base);
			else base = String(base);
			base = base.replace(/^[\x00-\x1f\x20]+|[\x00-\x1f\x20]+$/g, '');
		}
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
		// Fallback: decode byte-by-byte, replacing invalid UTF-8 with U+FFFD.
		var bytes = [];
		for (var i = 0; i < s.length; ) {
			if (s[i] === '%' && i + 2 < s.length && /[0-9A-Fa-f]/.test(s[i+1]) && /[0-9A-Fa-f]/.test(s[i+2])) {
				bytes.push(parseInt(s[i+1] + s[i+2], 16));
				i += 3;
			} else {
				bytes.push(s.charCodeAt(i));
				i++;
			}
		}
		// Decode UTF-8 bytes, replacing invalid sequences with U+FFFD.
		var r = '', bi = 0;
		while (bi < bytes.length) {
			var b = bytes[bi];
			if (b < 0x80) { r += String.fromCharCode(b); bi++; }
			else if ((b & 0xE0) === 0xC0) {
				if (bi + 1 < bytes.length && (bytes[bi+1] & 0xC0) === 0x80) {
					var cp = ((b & 0x1F) << 6) | (bytes[bi+1] & 0x3F);
					r += cp >= 0x80 ? String.fromCodePoint(cp) : '\uFFFD';
					bi += 2;
				} else { r += '\uFFFD'; bi++; }
			} else if ((b & 0xF0) === 0xE0) {
				if (bi + 2 < bytes.length && (bytes[bi+1] & 0xC0) === 0x80 && (bytes[bi+2] & 0xC0) === 0x80) {
					var cp = ((b & 0x0F) << 12) | ((bytes[bi+1] & 0x3F) << 6) | (bytes[bi+2] & 0x3F);
					r += cp >= 0x800 ? String.fromCodePoint(cp) : '\uFFFD';
					bi += 3;
				} else { r += '\uFFFD'; bi++; }
			} else if ((b & 0xF8) === 0xF0) {
				if (bi + 3 < bytes.length && (bytes[bi+1] & 0xC0) === 0x80 && (bytes[bi+2] & 0xC0) === 0x80 && (bytes[bi+3] & 0xC0) === 0x80) {
					var cp = ((b & 0x07) << 18) | ((bytes[bi+1] & 0x3F) << 12) | ((bytes[bi+2] & 0x3F) << 6) | (bytes[bi+3] & 0x3F);
					r += (cp >= 0x10000 && cp <= 0x10FFFF) ? String.fromCodePoint(cp) : '\uFFFD';
					bi += 4;
				} else { r += '\uFFFD'; bi++; }
			} else { r += '\uFFFD'; bi++; }
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
			entries.push([URLSearchParams._toUSV(URLSearchParams._pctDecode(name)), URLSearchParams._toUSV(URLSearchParams._pctDecode(value))]);
		}
		return entries;
	}
	static _toUSV(s) {
		var r = '';
		for (var i = 0; i < s.length; i++) {
			var c = s.charCodeAt(i);
			if (c >= 0xD800 && c <= 0xDBFF) {
				var next = (i + 1 < s.length) ? s.charCodeAt(i + 1) : 0;
				if (next >= 0xDC00 && next <= 0xDFFF) {
					r += s[i] + s[i + 1]; i++;
				} else { r += '\uFFFD'; }
			} else if (c >= 0xDC00 && c <= 0xDFFF) {
				r += '\uFFFD';
			} else { r += s[i]; }
		}
		return r;
	}
	static _formEncode(s) {
		s = URLSearchParams._toUSV(s);
		return encodeURIComponent(s).replace(/%20/g, '+').replace(/%2[aA]/g, '*').replace(/[!'()~]/g, function(c) {
			return '%' + c.charCodeAt(0).toString(16).toUpperCase();
		});
	}
	constructor(init) {
		this._entries = [];
		if (init === undefined || init === null) {
			// no entries
		} else if (typeof init === 'string') {
			const s = init.startsWith('?') ? init.slice(1) : init;
			this._entries = URLSearchParams._parseStr(s);
		} else if (typeof init === 'object' || typeof init === 'function') {
			if (typeof init[Symbol.iterator] === 'function') {
				for (const pair of init) {
					var items = Array.from(pair);
					if (items.length !== 2) throw new TypeError('Each pair must have exactly two items');
					this._entries.push([URLSearchParams._toUSV(String(items[0])), URLSearchParams._toUSV(String(items[1]))]);
				}
			} else {
				var map = new Map();
				for (const [k, v] of Object.entries(init)) {
					map.set(URLSearchParams._toUSV(String(k)), URLSearchParams._toUSV(String(v)));
				}
				for (const [k, v] of map) this._entries.push([k, v]);
			}
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

function _extractBodyContentType(bodyInit) {
	if (typeof bodyInit === 'string') return 'text/plain;charset=UTF-8';
	if (typeof Blob !== 'undefined' && bodyInit instanceof Blob && bodyInit.type) return bodyInit.type;
	if (typeof URLSearchParams !== 'undefined' && bodyInit instanceof URLSearchParams) return 'application/x-www-form-urlencoded;charset=UTF-8';
	if (typeof FormData !== 'undefined' && bodyInit instanceof FormData) {
		var boundary = '----FormDataBoundary' + Math.random().toString(36).slice(2);
		bodyInit._boundary = boundary;
		return 'multipart/form-data; boundary=' + boundary;
	}
	if (typeof Blob !== 'undefined' && bodyInit instanceof Blob) return null;
	if (bodyInit instanceof ArrayBuffer || ArrayBuffer.isView(bodyInit)) return null;
	if (bodyInit instanceof ReadableStream) return null;
	if (typeof bodyInit === 'object' && bodyInit !== null) return 'text/plain;charset=UTF-8';
	return null;
}
function _bodyToBytes(body) {
	if (body === null || body === undefined) return new Uint8Array(0);
	if (body instanceof Uint8Array) return body;
	if (body instanceof ArrayBuffer) return new Uint8Array(body);
	if (ArrayBuffer.isView(body)) return new Uint8Array(body.buffer, body.byteOffset, body.byteLength);
	if (typeof Blob !== 'undefined' && body instanceof Blob) {
		var text = body._parts.join('');
		return new TextEncoder().encode(text);
	}
	if (typeof URLSearchParams !== 'undefined' && body instanceof URLSearchParams) return new TextEncoder().encode(body.toString());
	if (typeof FormData !== 'undefined' && body instanceof FormData) {
		var boundary = body._boundary || ('----FormDataBoundary' + Math.random().toString(36).slice(2));
		var result = '';
		body.forEach(function(value, name) {
			result += '--' + boundary + '\r\n';
			if (typeof value === 'string') {
				result += 'Content-Disposition: form-data; name="' + name + '"\r\n\r\n';
				result += value + '\r\n';
			} else {
				var fname = value.name || 'blob';
				result += 'Content-Disposition: form-data; name="' + name + '"; filename="' + fname + '"\r\n';
				if (value.type) result += 'Content-Type: ' + value.type + '\r\n';
				result += '\r\n';
				result += value._parts.join('') + '\r\n';
			}
		});
		result += '--' + boundary + '--\r\n';
		return new TextEncoder().encode(result);
	}
	if (typeof body === 'string') return new TextEncoder().encode(body);
	return new TextEncoder().encode(String(body));
}
async function _consumeBody(obj) {
	if (obj._bodyUsed) throw new TypeError('body already consumed');
	obj._bodyUsed = true;
	if (obj._body instanceof ReadableStream) {
		const reader = obj._body.getReader();
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
	return _bodyToBytes(obj._body);
}
var _validModes = ['cors','no-cors','same-origin','navigate'];
var _validCredentials = ['omit','same-origin','include'];
var _validCache = ['default','no-store','reload','no-cache','force-cache','only-if-cached'];
var _validRedirect = ['follow','error','manual'];
var _validReferrerPolicy = ['','no-referrer','no-referrer-when-downgrade','same-origin','origin','strict-origin','origin-when-cross-origin','strict-origin-when-cross-origin','unsafe-url'];
var _validPriority = ['high','low','auto'];
class Request {
	constructor(input, init) {
		init = init || {};
		this._bodyUsed = false;
		this._body = null;
		if (init.window !== undefined && init.window !== null) throw new TypeError('RequestInit window must be null');
		if (input instanceof Request) {
			if (input.bodyUsed && init.body === undefined) throw new TypeError('Cannot construct Request from a disturbed body');
			this._url = input.url;
			this._method = input.method;
			this._headers = new Headers();
			this._body = input._body;
			this._redirect = input.redirect;
			this._mode = input.mode;
			this._credentials = input.credentials;
			this._cache = input.cache;
			this._referrer = input.referrer;
			this._referrerPolicy = input.referrerPolicy;
			this._integrity = input.integrity;
			this._keepalive = input.keepalive;
			this._signal = input.signal;
			this._priority = input._priority || 'auto';
		} else {
			var _inStr = String(input);
			var _parsedUrl;
			try { _parsedUrl = new URL(_inStr); } catch(e) {
				if (/^[a-zA-Z][a-zA-Z0-9+\-.]*:/.test(_inStr)) throw new TypeError('Invalid URL: ' + _inStr);
				_parsedUrl = null;
			}
			if (_parsedUrl) {
				if (_parsedUrl.username || _parsedUrl.password) throw new TypeError('Request URL cannot contain credentials');
				this._url = _parsedUrl.href;
			} else {
				this._url = _inStr;
			}
			this._method = 'GET';
			this._headers = new Headers();
			this._redirect = 'follow';
			this._mode = 'cors';
			this._credentials = 'same-origin';
			this._cache = 'default';
			this._referrer = 'about:client';
			this._referrerPolicy = '';
			this._integrity = '';
			this._keepalive = false;
			this._signal = null;
			this._priority = 'auto';
		}
		if (init.method !== undefined) {
			var _m = String(init.method);
			if (!/^[!#$%&'*+\-.^_\x60|~0-9A-Za-z]+$/.test(_m)) throw new TypeError('Invalid HTTP method: ' + _m);
			this._method = _m.toUpperCase();
			if (['CONNECT','TRACE','TRACK'].indexOf(this._method) !== -1) throw new TypeError('Forbidden method: ' + this._method);
		}
		if (init.mode !== undefined) {
			var _mv = String(init.mode);
			if (_mv === 'navigate') throw new TypeError('Cannot set mode to navigate');
			if (_validModes.indexOf(_mv) === -1) throw new TypeError('Invalid mode: ' + _mv);
			this._mode = _mv;
		}
		if (init.credentials !== undefined) {
			var _cv = String(init.credentials);
			if (_validCredentials.indexOf(_cv) === -1) throw new TypeError('Invalid credentials: ' + _cv);
			this._credentials = _cv;
		}
		if (init.cache !== undefined) {
			var _chv = String(init.cache);
			if (_validCache.indexOf(_chv) === -1) throw new TypeError('Invalid cache: ' + _chv);
			this._cache = _chv;
		}
		if (init.redirect !== undefined) {
			var _rv = String(init.redirect);
			if (_validRedirect.indexOf(_rv) === -1) throw new TypeError('Invalid redirect: ' + _rv);
			this._redirect = _rv;
		}
		if (init.referrerPolicy !== undefined) {
			var _rpv = String(init.referrerPolicy);
			if (_validReferrerPolicy.indexOf(_rpv) === -1) throw new TypeError('Invalid referrerPolicy: ' + _rpv);
			this._referrerPolicy = _rpv;
		}
		if (init.referrer !== undefined) {
			var _ref = String(init.referrer);
			if (_ref !== '' && _ref !== 'about:client' && _ref !== 'no-referrer') {
				try { new URL(_ref); } catch(e) { throw new TypeError('Invalid referrer URL: ' + _ref); }
			}
			this._referrer = _ref;
		}
		if (init.integrity !== undefined) this._integrity = String(init.integrity);
		if (init.keepalive !== undefined) this._keepalive = !!init.keepalive;
		if (init.signal !== undefined) this._signal = init.signal;
		if (init.priority !== undefined) {
			var _pv = String(init.priority);
			if (_validPriority.indexOf(_pv) === -1) throw new TypeError('Invalid priority: ' + _pv);
			this._priority = _pv;
		}
		if (init.duplex !== undefined && String(init.duplex) !== 'half') throw new TypeError('Invalid duplex: only "half" is allowed');
		if (this._mode === 'no-cors' && ['GET','HEAD','POST'].indexOf(this._method) === -1) throw new TypeError('Method not allowed in no-cors mode');
		if (this._cache === 'only-if-cached' && this._mode !== 'same-origin') throw new TypeError('only-if-cached requires same-origin mode');
		this._headers = new Headers();
		if (this._mode === 'no-cors') this._headers._guard = 'request-no-cors';
		else this._headers._guard = 'request';
		var headersSrc = init.headers !== undefined ? init.headers : (input instanceof Request ? input.headers : undefined);
		if (headersSrc !== undefined) {
			var fill = (headersSrc instanceof Headers) ? headersSrc : new Headers(headersSrc);
			fill.forEach(function(v, k) { this._headers.append(k, v); }.bind(this));
		}
		var _bodyFromInit = init.body !== undefined;
		var bodyInit = _bodyFromInit ? init.body : (input instanceof Request ? input._body : null);
		if (bodyInit != null) {
			if (this._method === 'GET' || this._method === 'HEAD') throw new TypeError('Request with GET/HEAD method cannot have body.');
			if (bodyInit instanceof ReadableStream) {
				if (_bodyFromInit && init.duplex === undefined) throw new TypeError('ReadableStream body requires duplex: "half"');
				if (bodyInit._locked || bodyInit._disturbed) throw new TypeError('Body stream is locked or disturbed');
			}
			if (this._keepalive && bodyInit instanceof ReadableStream) throw new TypeError('keepalive not supported with ReadableStream body');
			if (!_bodyFromInit && bodyInit instanceof ReadableStream) {
				var _q = bodyInit._queue ? bodyInit._queue.slice() : [];
				this._body = new ReadableStream({ start: function(c) { for (var _i=0;_i<_q.length;_i++) c.enqueue(_q[_i]); c.close(); } });
			} else {
				this._body = bodyInit;
			}
			if (!this._headers.has('Content-Type')) {
				var ct = _extractBodyContentType(bodyInit);
				if (ct) this._headers.append('Content-Type', ct);
			}
		}
		if (input instanceof Request && input._body !== null && input._body !== undefined) {
			input._bodyUsed = true;
		}
	}
	get method() { return this._method; }
	get url() { return this._url; }
	get headers() { return this._headers; }
	set headers(v) {}
	get destination() { return ''; }
	get referrer() { return this._referrer; }
	get referrerPolicy() { return this._referrerPolicy; }
	get mode() { return this._mode; }
	get credentials() { return this._credentials; }
	get cache() { return this._cache; }
	get redirect() { return this._redirect; }
	get integrity() { return this._integrity; }
	get keepalive() { return this._keepalive; }
	get signal() { return this._signal; }
	get isReloadNavigation() { return false; }
	get isHistoryNavigation() { return false; }
	get priority() { return this._priority || 'auto'; }
	get duplex() { return 'half'; }
	get body() {
		if (this._body === null || this._body === undefined) return null;
		if (this._body instanceof ReadableStream) return this._body;
		const content = this._body;
		const stream = new ReadableStream({
			type: 'bytes',
			start(controller) {
				controller.enqueue(_bodyToBytes(content));
				controller.close();
			}
		});
		this._body = stream;
		return stream;
	}
	get bodyUsed() { return this._bodyUsed || (this._body instanceof ReadableStream && this._body._disturbed); }
	async text() {
		var bytes = await _consumeBody(this);
		return new TextDecoder().decode(bytes);
	}
	async json() { return JSON.parse(await this.text()); }
	async arrayBuffer() { return (await _consumeBody(this)).buffer; }
	async bytes() { return await _consumeBody(this); }
	async blob() {
		var bytes = await _consumeBody(this);
		var ct = this._headers ? this._headers.get('Content-Type') : null;
		return new Blob([bytes], ct ? {type: ct} : {});
	}
	async formData() {
		var ct = this._headers ? this._headers.get('Content-Type') : '';
		var txt = await this.text();
		if (ct && ct.indexOf('application/x-www-form-urlencoded') !== -1) {
			var fd = new FormData();
			new URLSearchParams(txt).forEach(function(v, k) { fd.append(k, v); });
			return fd;
		}
		throw new TypeError('Could not parse content as FormData');
	}
	clone() {
		if (this.bodyUsed) throw new TypeError('Cannot clone a disturbed Request');
		var r = Object.create(Request.prototype);
		r._url = this._url;
		r._method = this._method;
		r._headers = new Headers(this._headers);
		r._body = this._body;
		r._bodyUsed = false;
		r._redirect = this._redirect;
		r._mode = this._mode;
		r._credentials = this._credentials;
		r._cache = this._cache;
		r._referrer = this._referrer;
		r._referrerPolicy = this._referrerPolicy;
		r._integrity = this._integrity;
		r._keepalive = this._keepalive;
		r._signal = this._signal;
		r._priority = this._priority;
		if (this._mode === 'no-cors') r._headers._guard = 'request-no-cors';
		else r._headers._guard = 'request';
		return r;
	}
	get [Symbol.toStringTag]() { return 'Request'; }
}

class Response {
	constructor(body, init) {
		init = init || {};
		var _status = init.status !== undefined ? Number(init.status) : 200;
		if (_status < 200 || _status > 599 || _status !== (_status | 0)) {
			throw new RangeError('Invalid status code: ' + init.status);
		}
		var _statusText = init.statusText !== undefined ? String(init.statusText) : '';
		for (var _sti = 0; _sti < _statusText.length; _sti++) {
			var _stc = _statusText.charCodeAt(_sti);
			if (_stc === 0x7F || (_stc < 0x20 && _stc !== 0x09) || _stc > 0xFF) {
				throw new TypeError('Invalid statusText');
			}
		}
		var _nullBodyStatus = [204, 205, 304];
		if (body !== null && body !== undefined && _nullBodyStatus.indexOf(_status) !== -1) {
			throw new TypeError('Response with null body status cannot have body');
		}
		if (body instanceof ReadableStream) {
			if (body._locked) throw new TypeError('ReadableStream is locked');
			if (body._disturbed) throw new TypeError('ReadableStream is disturbed');
		}
		this._body = body !== undefined && body !== null ? body : null;
		this._bodyUsed = false;
		this.type = 'default';
		this.status = _status;
		this.statusText = _statusText;
		this.headers = new Headers(init.headers);
		this.headers._guard = 'response';
		if (this._body !== null) {
			var _ct = _extractBodyContentType(this._body);
			if (_ct && !this.headers.has('content-type')) {
				this.headers.append('content-type', _ct);
			}
		}
		this.redirected = false;
		this.url = init.url || '';
		this.webSocket = init.webSocket || null;
	}
	get ok() { return this.status >= 200 && this.status < 300; }
	get body() {
		if (this._body === null || this._body === undefined) return null;
		if (this._body instanceof ReadableStream) return this._body;
		var content = this._body;
		var stream = new ReadableStream({
			type: 'bytes',
			start(controller) {
				controller.enqueue(_bodyToBytes(content));
				controller.close();
			}
		});
		this._body = stream;
		return stream;
	}
	get bodyUsed() {
		return this._bodyUsed || (this._body instanceof ReadableStream && this._body._disturbed);
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
		if (this._body instanceof ReadableStream && this._body._disturbed) throw new TypeError('Cannot clone a disturbed response');
		var r = Object.create(Response.prototype);
		r._bodyUsed = false;
		r.type = this.type;
		r.status = this.status;
		r.statusText = this.statusText;
		r.headers = new Headers(this.headers);
		r.headers._guard = this.headers._guard;
		r.url = this.url;
		r.redirected = this.redirected;
		r.webSocket = this.webSocket;
		if (this._body instanceof ReadableStream) {
			var _q = this._body._queue ? this._body._queue.slice() : [];
			var _closed = this._body._closed;
			r._body = new ReadableStream({ start: function(c) {
				for (var _i=0;_i<_q.length;_i++) {
					var _ch = _q[_i];
					if (_ch instanceof ArrayBuffer) { c.enqueue(_ch.slice(0)); }
					else if (typeof DataView !== 'undefined' && _ch instanceof DataView) { c.enqueue(new DataView(_ch.buffer.slice(0), _ch.byteOffset, _ch.byteLength)); }
					else if (ArrayBuffer.isView(_ch)) { c.enqueue(new _ch.constructor(_ch.buffer.slice(_ch.byteOffset, _ch.byteOffset + _ch.byteLength))); }
					else { c.enqueue(_ch); }
				}
				if (_closed) c.close();
			} });
			var _origQ = this._body._queue ? this._body._queue.slice() : [];
			var _origClosed = this._body._closed;
			this._body = new ReadableStream({ start: function(c) { for (var _i=0;_i<_origQ.length;_i++) c.enqueue(_origQ[_i]); if (_origClosed) c.close(); } });
		} else {
			r._body = this._body;
		}
		return r;
	}
	static json(data, init) {
		init = init || {};
		var body = JSON.stringify(data);
		if (body === undefined) throw new TypeError('Failed to serialize data to JSON');
		var headers = new Headers(init.headers);
		if (!headers.has('content-type')) headers.set('content-type', 'application/json');
		return new Response(body, { ...init, headers });
	}
	static redirect(url, status) {
		var _rUrl = String(url);
		if (/^[a-zA-Z][a-zA-Z0-9+\-.]*:/.test(_rUrl)) { try { new URL(_rUrl); } catch(e) { throw new TypeError('Invalid URL: ' + _rUrl); } }
		status = status || 302;
		if ([301, 302, 303, 307, 308].indexOf(status) === -1) {
			throw new RangeError('Invalid redirect status: ' + status);
		}
		return new Response(null, { status, headers: { location: url } });
	}
	static error() {
		var r = Object.create(Response.prototype);
		r._body = null;
		r._bodyUsed = false;
		r.type = 'error';
		r.status = 0;
		r.statusText = '';
		r.headers = new Headers();
		r.headers._guard = 'immutable';
		r.redirected = false;
		r.url = '';
		r.webSocket = null;
		return r;
	}
	get [Symbol.toStringTag]() { return 'Response'; }
}

if (typeof TextEncoder === 'undefined') {
	globalThis.TextEncoder = class TextEncoder {
		get encoding() { return 'utf-8'; }
		encode(str) {
			if (str === undefined) str = '';
			str = String(str);
			const buf = [];
			for (let i = 0; i < str.length; i++) {
				let c = str.charCodeAt(i);
				if (c < 0x80) {
					buf.push(c);
				} else if (c < 0x800) {
					buf.push(0xc0 | (c >> 6), 0x80 | (c & 0x3f));
				} else if (c >= 0xd800 && c <= 0xdbff) {
					if (i + 1 < str.length) {
						const next = str.charCodeAt(i + 1);
						if (next >= 0xdc00 && next <= 0xdfff) {
							const cp = ((c - 0xd800) << 10) + (next - 0xdc00) + 0x10000;
							buf.push(0xf0 | (cp >> 18), 0x80 | ((cp >> 12) & 0x3f), 0x80 | ((cp >> 6) & 0x3f), 0x80 | (cp & 0x3f));
							i++;
						} else {
							buf.push(0xef, 0xbf, 0xbd);
						}
					} else {
						buf.push(0xef, 0xbf, 0xbd);
					}
				} else if (c >= 0xdc00 && c <= 0xdfff) {
					buf.push(0xef, 0xbf, 0xbd);
				} else {
					buf.push(0xe0 | (c >> 12), 0x80 | ((c >> 6) & 0x3f), 0x80 | (c & 0x3f));
				}
			}
			return new Uint8Array(buf);
		}
		encodeInto(source, destination) {
			if (!(destination instanceof Uint8Array)) throw new TypeError("encodeInto destination must be a Uint8Array");
			source = String(source);
			const encoded = this.encode(source);
			const maxLen = destination.length;
			let read = 0;
			let byteCount = 0;
			for (let i = 0; i < source.length; i++) {
				let c = source.charCodeAt(i);
				let charBytes;
				if (c < 0x80) charBytes = 1;
				else if (c < 0x800) charBytes = 2;
				else if (c >= 0xd800 && c <= 0xdbff && i + 1 < source.length && source.charCodeAt(i+1) >= 0xdc00 && source.charCodeAt(i+1) <= 0xdfff) { charBytes = 4; }
				else if (c >= 0xd800 && c <= 0xdfff) { charBytes = 3; }
				else charBytes = 3;
				if (byteCount + charBytes > maxLen) break;
				byteCount += charBytes;
				read++;
				if (charBytes === 4) { i++; read++; }
			}
			destination.set(encoded.subarray(0, byteCount));
			return { read, written: byteCount };
		}
		get [Symbol.toStringTag]() { return 'TextEncoder'; }
	};
}

globalThis.TextDecoder = class TextDecoder {
		constructor(encoding, options) {
			var raw = encoding === undefined ? 'utf-8' : String(encoding);
			// Strip only ASCII whitespace per Encoding spec.
			var label = raw.replace(/^[\x09\x0a\x0c\x0d\x20]+|[\x09\x0a\x0c\x0d\x20]+$/g, '').toLowerCase();
			// Map valid UTF-8 aliases to canonical name.
			var validLabels = {
				'unicode-1-1-utf-8': 'utf-8', 'unicode11utf8': 'utf-8',
				'unicode20utf8': 'utf-8', 'utf-8': 'utf-8', 'utf8': 'utf-8',
				'x-unicode20utf8': 'utf-8'
			};
			if (validLabels[label] !== undefined) {
				label = validLabels[label];
			} else {
				throw new RangeError("The encoding label provided ('" + raw + "') is invalid.");
			}
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
				} else if (b >= 0xC2 && b <= 0xDF) {
					// 2-byte sequence
					if (i + 1 >= bytes.length) {
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
				} else if (b >= 0xE0 && b <= 0xEF) {
					// 3-byte sequence
					var lower2 = (b === 0xE0) ? 0xA0 : 0x80;
					var upper2 = (b === 0xED) ? 0x9F : 0xBF;
					if (i + 1 >= bytes.length) {
						if (stream) { this._pending = Array.from(bytes.subarray(i)); break; }
						if (this._fatal) throw new TypeError('The encoded data was not valid utf-8');
						result += '\uFFFD'; i++;
					} else {
						var b1 = bytes[i+1];
						if (b1 < lower2 || b1 > upper2) {
							if (this._fatal) throw new TypeError('The encoded data was not valid utf-8');
							result += '\uFFFD'; i++;
						} else if (i + 2 >= bytes.length) {
							if (stream) { this._pending = Array.from(bytes.subarray(i)); break; }
							if (this._fatal) throw new TypeError('The encoded data was not valid utf-8');
							result += '\uFFFD'; i += 2;
						} else {
							var b2 = bytes[i+2];
							if ((b2 & 0xc0) !== 0x80) {
								if (this._fatal) throw new TypeError('The encoded data was not valid utf-8');
								result += '\uFFFD'; i += 2;
							} else {
								result += String.fromCharCode(((b & 0x0f) << 12) | ((b1 & 0x3f) << 6) | (b2 & 0x3f));
								i += 3;
							}
						}
					}
				} else if (b >= 0xF0 && b <= 0xF4) {
					// 4-byte sequence
					var lower2 = (b === 0xF0) ? 0x90 : 0x80;
					var upper2 = (b === 0xF4) ? 0x8F : 0xBF;
					if (i + 1 >= bytes.length) {
						if (stream) { this._pending = Array.from(bytes.subarray(i)); break; }
						if (this._fatal) throw new TypeError('The encoded data was not valid utf-8');
						result += '\uFFFD'; i++;
					} else {
						var b1 = bytes[i+1];
						if (b1 < lower2 || b1 > upper2) {
							if (this._fatal) throw new TypeError('The encoded data was not valid utf-8');
							result += '\uFFFD'; i++;
						} else if (i + 2 >= bytes.length) {
							if (stream) { this._pending = Array.from(bytes.subarray(i)); break; }
							if (this._fatal) throw new TypeError('The encoded data was not valid utf-8');
							result += '\uFFFD'; i += 2;
						} else {
							var b2 = bytes[i+2];
							if ((b2 & 0xc0) !== 0x80) {
								if (this._fatal) throw new TypeError('The encoded data was not valid utf-8');
								result += '\uFFFD'; i += 2;
							} else if (i + 3 >= bytes.length) {
								if (stream) { this._pending = Array.from(bytes.subarray(i)); break; }
								if (this._fatal) throw new TypeError('The encoded data was not valid utf-8');
								result += '\uFFFD'; i += 3;
							} else {
								var b3 = bytes[i+3];
								if ((b3 & 0xc0) !== 0x80) {
									if (this._fatal) throw new TypeError('The encoded data was not valid utf-8');
									result += '\uFFFD'; i += 3;
								} else {
									var cp = ((b & 0x07) << 18) | ((b1 & 0x3f) << 12) | ((b2 & 0x3f) << 6) | (b3 & 0x3f);
									result += String.fromCodePoint(cp);
									i += 4;
								}
							}
						}
					}
				} else {
					// Invalid: 0x80-0xBF (continuation), 0xC0-0xC1 (overlong), 0xF5-0xFF
					if (this._fatal) throw new TypeError('The encoded data was not valid utf-8');
					result += '\uFFFD'; i++;
				}
			}
			if (!stream) { this._bomSeen = false; this._pending = []; }
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
var whatwgParser = whatwgUrl.NewParser()

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
		// Per spec, blob origin is derived from the inner URL, but only
		// if the inner URL has an http(s) scheme. Non-http blob URLs and
		// nested blob: URLs return "null".
		inner, err := whatwgParser.Parse(u.Pathname())
		if err == nil {
			innerScheme := inner.Scheme()
			if innerScheme == "http" || innerScheme == "https" {
				return inner.Protocol() + "//" + inner.Host()
			}
		}
		return "null"
	default:
		return "null"
	}
}

// urlToParsed extracts all fields from a whatwg-url Url into a URLParsed.
func urlToParsed(u *whatwgUrl.Url) *URLParsed {
	parsed := &URLParsed{
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
	// Encode ^ as %5E in pathname per WHATWG URL spec path percent-encode set.
	// Only for non-opaque paths; opaque paths use the C0 control set (no ^).
	if !u.OpaquePath() && strings.Contains(parsed.Pathname, "^") {
		old := parsed.Pathname
		parsed.Pathname = strings.ReplaceAll(parsed.Pathname, "^", "%5E")
		parsed.Href = strings.Replace(parsed.Href, old, parsed.Pathname, 1)
	}
	return parsed
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

	parsed := urlToParsed(u)

	// Per WHATWG spec: opaque-path URLs encode only the last trailing space
	// as %20 when followed by query or fragment.
	if u.OpaquePath() {
		path := u.Pathname()
		if len(path) > 0 && path[len(path)-1] == ' ' && (u.Search() != "" || u.Hash() != "") {
			parsed.Pathname = path[:len(path)-1] + "%20"
			// Rebuild href.
			parsed.Href = parsed.Protocol + parsed.Pathname
			if parsed.Search != "" {
				parsed.Href += parsed.Search
			}
			if parsed.Hash != "" {
				parsed.Href += parsed.Hash
			}
		}
	}

	return parsed, nil
}

// SetURL applies a setter operation on a parsed URL and returns the updated URLParsed.
func SetURL(href, field, value string) (*URLParsed, error) {
	u, err := whatwgParser.Parse(href)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %s", href)
	}

	// For opaque-path URLs, capture the original path before mutation.
	// When removing query/hash, trailing spaces in the opaque path
	// must be percent-encoded per the WHATWG spec.
	isOpaque := u.OpaquePath()
	var origPath string
	hadQuery := u.Search() != ""
	hadHash := u.Hash() != ""
	if isOpaque {
		origPath = u.Pathname()
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
		u2, err2 := whatwgParser.Parse(value)
		if err2 != nil {
			return nil, fmt.Errorf("invalid URL: %s", value)
		}
		return urlToParsed(u2), nil
	default:
		return nil, fmt.Errorf("unknown URL field: %s", field)
	}

	parsed := urlToParsed(u)

	// Per WHATWG spec: when removing query or hash from opaque-path URLs,
	// only the final trailing space becomes %20.
	if isOpaque {
		removedQuery := hadQuery && u.Search() == ""
		removedHash := hadHash && u.Hash() == ""
		if removedQuery || removedHash {
			// Use the original path (which preserves internal spaces),
			// and encode only the last trailing space.
			path := origPath
			if len(path) > 0 && path[len(path)-1] == ' ' {
				path = path[:len(path)-1] + "%20"
			}
			parsed.Pathname = path
			// Rebuild href from components.
			parsed.Href = parsed.Protocol + parsed.Pathname
			if parsed.Search != "" {
				parsed.Href += parsed.Search
			}
			if parsed.Hash != "" {
				parsed.Href += parsed.Hash
			}
		}
	}

	return parsed, nil
}

// SetupWebAPIs registers Go-backed helpers and evaluates the JS class
// definitions that form the Web API surface available to workers.
func SetupWebAPIs(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	// Register Go-backed URL parser.
	if err := rt.RegisterFunc("__parseURL", func(rawURL, base string) (string, error) {
		parsed, err := ParseURL(rawURL, base)
		if err != nil {
			errStr, _ := json.Marshal(err.Error())
			return fmt.Sprintf(`{"error":%s}`, errStr), nil
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
			errStr, _ := json.Marshal(err.Error())
			return fmt.Sprintf(`{"error":%s}`, errStr), nil
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
