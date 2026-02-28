package webapi

import (
	"encoding/json"
	"fmt"
	"net/url"

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
				for (const [k, v] of Object.entries(init._map)) this._map[k] = v;
			} else if (Array.isArray(init)) {
				for (const [k, v] of init) this._map[k.toLowerCase()] = String(v);
			} else {
				for (const [k, v] of Object.entries(init)) this._map[k.toLowerCase()] = String(v);
			}
		}
	}
	get(name) { return this._map[name.toLowerCase()] ?? null; }
	set(name, value) { this._map[name.toLowerCase()] = String(value); }
	has(name) { return name.toLowerCase() in this._map; }
	delete(name) { delete this._map[name.toLowerCase()]; }
	append(name, value) {
		const key = name.toLowerCase();
		this._map[key] = this._map[key] ? this._map[key] + ', ' + String(value) : String(value);
	}
	forEach(cb) { for (const [k, v] of Object.entries(this._map)) cb(v, k, this); }
	entries() { return Object.entries(this._map)[Symbol.iterator](); }
	keys() { return Object.keys(this._map)[Symbol.iterator](); }
	values() { return Object.values(this._map)[Symbol.iterator](); }
}

class URL {
	constructor(input, base) {
		const parsed = JSON.parse(__parseURL(input, base || ''));
		if (parsed.error) throw new TypeError(parsed.error);
		this.href = parsed.href;
		this.protocol = parsed.protocol;
		this.hostname = parsed.hostname;
		this.port = parsed.port;
		this.pathname = parsed.pathname;
		this.search = parsed.search;
		this.hash = parsed.hash;
		this.origin = parsed.origin;
		this.host = parsed.host;
		this.username = parsed.username || '';
		this.password = parsed.password || '';
		this.searchParams = new URLSearchParams(this.search);
		this.searchParams._url = this;
	}
	toString() { return this.href; }
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
	constructor(init) {
		this._entries = [];
		if (typeof init === 'string') {
			const s = init.startsWith('?') ? init.slice(1) : init;
			if (s) {
				for (const pair of s.split('&')) {
					const [k, ...rest] = pair.split('=');
					this._entries.push([decodeURIComponent(k.replace(/\+/g, '%20')), decodeURIComponent(rest.join('=').replace(/\+/g, '%20'))]);
				}
			}
		}
	}
	get(name) {
		const e = this._entries.find(([k]) => k === name);
		return e ? e[1] : null;
	}
	has(name) { return this._entries.some(([k]) => k === name); }
	toString() { return this._entries.map(([k, v]) => encodeURIComponent(k) + '=' + encodeURIComponent(v)).join('&'); }
	forEach(cb) { for (const [k, v] of this._entries) cb(v, k, this); }
	entries() { return this._entries[Symbol.iterator](); }
	keys() { return this._entries.map(([k]) => k)[Symbol.iterator](); }
	values() { return this._entries.map(([, v]) => v)[Symbol.iterator](); }
}

class Request {
	constructor(input, init) {
		init = init || {};
		if (input instanceof Request) {
			this.url = input.url;
			this.method = input.method;
			this.headers = new Headers(input.headers);
			this._body = input._body;
		} else {
			try { this.url = new URL(String(input)).href; } catch(e) { this.url = String(input); }
			this.method = (init.method || 'GET').toUpperCase();
			this.headers = new Headers(init.headers);
			this._body = init.body !== undefined ? init.body : null;
		}
		if (init.method) this.method = init.method.toUpperCase();
		if (init.headers) this.headers = new Headers(init.headers);
		if (init.body !== undefined) this._body = init.body;
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
		if (this._body instanceof ReadableStream) return this._body._locked;
		return false;
	}
	async text() { return this._body !== null && this._body !== undefined ? String(this._body) : ''; }
	async json() { return JSON.parse(await this.text()); }
	async arrayBuffer() {
		const t = await this.text();
		const enc = new TextEncoder();
		return enc.encode(t).buffer;
	}
	async bytes() {
		const t = await this.text();
		return new TextEncoder().encode(t);
	}
	clone() { return new Request(this); }
}

class Response {
	constructor(body, init) {
		init = init || {};
		this._body = body !== undefined && body !== null ? body : null;
		this.status = init.status !== undefined ? init.status : 200;
		this.statusText = init.statusText || '';
		this.headers = new Headers(init.headers);
		this.ok = this.status >= 200 && this.status < 300;
		this.url = init.url || '';
		this.webSocket = init.webSocket || null;
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
		if (this._body instanceof ReadableStream) return this._body._locked;
		return false;
	}
	async text() { return this._body !== null && this._body !== undefined ? String(this._body) : ''; }
	async json() { return JSON.parse(await this.text()); }
	async arrayBuffer() {
		const t = await this.text();
		const enc = new TextEncoder();
		return enc.encode(t).buffer;
	}
	async bytes() {
		const t = await this.text();
		return new TextEncoder().encode(t);
	}
	clone() {
		return new Response(this._body, {
			status: this.status,
			statusText: this.statusText,
			headers: new Headers(this.headers),
		});
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
		return r;
	}
}

if (typeof TextEncoder === 'undefined') {
	globalThis.TextEncoder = class TextEncoder {
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
		this._url.href = this._url.origin + this._url.pathname + this._url.search + this._url.hash;
	}
};

USP.getAll = function(name) {
	return this._entries.filter(function(e) { return e[0] === name; }).map(function(e) { return e[1]; });
};

USP.set = function(name, value) {
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
	this._entries.push([name, String(value)]);
	this._sync();
};

var origDelete = USP['delete'];
USP['delete'] = function(name) {
	this._entries = this._entries.filter(function(e) { return e[0] !== name; });
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

func ParseURL(rawURL, base string) (*URLParsed, error) {
	var u *url.URL
	var err error

	if base != "" {
		baseURL, berr := url.Parse(base)
		if berr != nil {
			return nil, fmt.Errorf("invalid base URL: %s", base)
		}
		ref, rerr := url.Parse(rawURL)
		if rerr != nil {
			return nil, fmt.Errorf("invalid URL: %s", rawURL)
		}
		u = baseURL.ResolveReference(ref)
	} else {
		u, err = url.Parse(rawURL)
		if err != nil {
			return nil, fmt.Errorf("invalid URL: %s", rawURL)
		}
	}

	if u.Scheme == "" {
		return nil, fmt.Errorf("invalid URL: %s", rawURL)
	}

	protocol := u.Scheme + ":"
	hostname := u.Hostname()
	port := u.Port()
	host := hostname
	if port != "" {
		host = hostname + ":" + port
	}
	origin := protocol + "//" + host
	search := ""
	if u.RawQuery != "" {
		search = "?" + u.RawQuery
	}
	hash := ""
	if u.Fragment != "" {
		hash = "#" + u.Fragment
	}

	var username, password string
	if u.User != nil {
		username = u.User.Username()
		password, _ = u.User.Password()
	}

	pathname := u.Path
	if pathname == "" {
		pathname = "/"
	}

	userInfo := ""
	if u.User != nil {
		userInfo = u.User.String() + "@"
	}
	href := protocol + "//" + userInfo + host + pathname + search + hash

	return &URLParsed{
		Href:     href,
		Protocol: protocol,
		Hostname: hostname,
		Port:     port,
		Pathname: pathname,
		Search:   search,
		Hash:     hash,
		Origin:   origin,
		Host:     host,
		Username: username,
		Password: password,
	}, nil
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
