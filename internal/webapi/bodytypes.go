package webapi

import (
	"fmt"

	"github.com/cryguy/worker/v2/internal/core"
	"github.com/cryguy/worker/v2/internal/eventloop"
)

// bodyTypesJS patches Request and Response prototypes with extended body handling.
const bodyTypesJS = `
(function() {

function bodyToString(body) {
	if (body === null || body === undefined) return '';
	if (typeof body === 'string') return body;
	if (body instanceof ArrayBuffer) {
		var arr = new Uint8Array(body);
		var s = '';
		for (var i = 0; i < arr.length; i++) s += String.fromCharCode(arr[i]);
		return s;
	}
	if (ArrayBuffer.isView(body)) {
		var arr2 = new Uint8Array(body.buffer, body.byteOffset, body.byteLength);
		var s2 = '';
		for (var i2 = 0; i2 < arr2.length; i2++) s2 += String.fromCharCode(arr2[i2]);
		return s2;
	}
	if (body instanceof Blob) {
		return body._parts.join('');
	}
	if (body instanceof URLSearchParams) {
		return body.toString();
	}
	if (body instanceof FormData) {
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
		return result;
	}
	if (body instanceof ReadableStream) {
		var s3 = '';
		for (var i3 = 0; i3 < body._queue.length; i3++) {
			var chunk = body._queue[i3];
			if (typeof chunk === 'string') { s3 += chunk; }
			else if (chunk instanceof Uint8Array) {
				for (var j = 0; j < chunk.length; j++) s3 += String.fromCharCode(chunk[j]);
			} else { s3 += String(chunk); }
		}
		body._queue = [];
		return s3;
	}
	return String(body);
}

function parseMultipart(text, contentType) {
	var fd = new FormData();
	var m = contentType.match(/boundary=([^\s;]+)/);
	if (!m) return fd;
	var boundary = m[1];
	var parts = text.split('--' + boundary);
	for (var i = 1; i < parts.length; i++) {
		var part = parts[i];
		if (part.indexOf('--') === 0) break;
		var sepIdx = part.indexOf('\r\n\r\n');
		if (sepIdx === -1) continue;
		var headerSection = part.slice(0, sepIdx);
		var body = part.slice(sepIdx + 4).replace(/\r\n$/, '');
		var dispMatch = headerSection.match(/Content-Disposition:\s*form-data;\s*name="([^"]+)"(?:;\s*filename="([^"]+)")?/i);
		if (!dispMatch) continue;
		var name = dispMatch[1];
		var filename = dispMatch[2];
		if (filename !== undefined) {
			var ctMatch = headerSection.match(/Content-Type:\s*([^\r\n]+)/i);
			var ftype = ctMatch ? ctMatch[1].trim() : '';
			fd.append(name, new File([body], filename, { type: ftype }));
		} else {
			fd.append(name, body);
		}
	}
	return fd;
}

async function __readStreamBytes(stream) {
	var reader = stream.getReader();
	var chunks = [];
	var totalLen = 0;
	for (;;) {
		var result = await reader.read();
		if (result.done) break;
		var chunk = result.value;
		var bytes;
		if (chunk instanceof Uint8Array) { bytes = chunk; }
		else if (chunk instanceof ArrayBuffer) { bytes = new Uint8Array(chunk); }
		else if (ArrayBuffer.isView(chunk)) { bytes = new Uint8Array(chunk.buffer, chunk.byteOffset, chunk.byteLength); }
		else { throw new TypeError('Response body stream chunk is not Uint8Array'); }
		chunks.push(bytes);
		totalLen += bytes.length;
	}
	var merged = new Uint8Array(totalLen);
	var offset = 0;
	for (var i = 0; i < chunks.length; i++) {
		merged.set(chunks[i], offset);
		offset += chunks[i].length;
	}
	return merged;
}

function __markBodyUsed(obj) {
	if (obj._bodyUsed) throw new TypeError('body already consumed');
	if (obj._body instanceof ReadableStream && obj._body._disturbed) throw new TypeError('body stream already disturbed');
	if (obj._body !== null && obj._body !== undefined) obj._bodyUsed = true;
}

function __bodyToBytes(body) {
	if (body === null || body === undefined) return new Uint8Array(0);
	if (body instanceof Uint8Array) return body;
	if (body instanceof ArrayBuffer) return new Uint8Array(body);
	if (ArrayBuffer.isView(body)) return new Uint8Array(body.buffer.slice(body.byteOffset, body.byteOffset + body.byteLength));
	if (body instanceof ReadableStream) return null;
	var s = bodyToString(body);
	return new TextEncoder().encode(s);
}

Request.prototype.text = async function() {
	__markBodyUsed(this);
	if (this._body instanceof ReadableStream) {
		var bytes = await __readStreamBytes(this._body);
		return new TextDecoder().decode(bytes);
	}
	return bodyToString(this._body);
};

Response.prototype.text = async function() {
	__markBodyUsed(this);
	var _s = this.body;
	if (_s === null) return '';
	var bytes = await __readStreamBytes(_s);
	return new TextDecoder().decode(bytes);
};

Request.prototype.arrayBuffer = async function() {
	__markBodyUsed(this);
	if (this._body instanceof ArrayBuffer) return this._body;
	if (ArrayBuffer.isView(this._body)) return this._body.buffer.slice(this._body.byteOffset, this._body.byteOffset + this._body.byteLength);
	if (this._body instanceof ReadableStream) {
		var bytes = await __readStreamBytes(this._body);
		return bytes.buffer;
	}
	var t = bodyToString(this._body);
	var enc = new TextEncoder();
	return enc.encode(t).buffer;
};

Response.prototype.arrayBuffer = async function() {
	__markBodyUsed(this);
	var _s = this.body;
	if (_s === null) return new ArrayBuffer(0);
	var bytes = await __readStreamBytes(_s);
	return bytes.buffer;
};

Request.prototype.json = async function() {
	var t = await this.text();
	return JSON.parse(t);
};

Response.prototype.json = async function() {
	__markBodyUsed(this);
	var _s = this.body;
	if (_s === null) return JSON.parse('');
	var bytes = await __readStreamBytes(_s);
	return JSON.parse(new TextDecoder().decode(bytes));
};

Request.prototype.bytes = async function() {
	__markBodyUsed(this);
	if (this._body instanceof ReadableStream) {
		return await __readStreamBytes(this._body);
	}
	return __bodyToBytes(this._body);
};

Response.prototype.bytes = async function() {
	__markBodyUsed(this);
	var _s = this.body;
	if (_s === null) return new Uint8Array(0);
	return await __readStreamBytes(_s);
};

Request.prototype.blob = async function() {
	__markBodyUsed(this);
	var ct = this.headers.get('content-type') || '';
	if (this._body instanceof ReadableStream) {
		var bytes = await __readStreamBytes(this._body);
		return new Blob([bytes], { type: ct });
	}
	var bytes2 = __bodyToBytes(this._body);
	return new Blob([bytes2], { type: ct });
};

Response.prototype.blob = async function() {
	__markBodyUsed(this);
	var ct = this.headers.get('content-type') || '';
	var _s = this.body;
	if (_s === null) return new Blob([], { type: ct });
	var bytes = await __readStreamBytes(_s);
	return new Blob([bytes], { type: ct });
};

Request.prototype.formData = async function() {
	__markBodyUsed(this);
	var ct = this.headers.get('content-type') || '';
	var text = (this._body === null || this._body === undefined) ? '' : bodyToString(this._body);
	if (ct.indexOf('application/x-www-form-urlencoded') !== -1) {
		var fd = new FormData();
		var params = new URLSearchParams(text);
		params.forEach(function(v, k) { fd.append(k, v); });
		return fd;
	}
	if (ct.indexOf('multipart/form-data') !== -1) {
		if (!text) throw new TypeError('Could not parse content as FormData');
		return parseMultipart(text, ct);
	}
	throw new TypeError('Could not parse content as FormData');
};

Response.prototype.formData = async function() {
	__markBodyUsed(this);
	var ct = this.headers.get('content-type') || '';
	var _s = this.body;
	var text = '';
	if (_s !== null) {
		var bytes = await __readStreamBytes(_s);
		text = new TextDecoder('utf-8', { ignoreBOM: true }).decode(bytes);
	}
	if (ct.indexOf('application/x-www-form-urlencoded') !== -1) {
		var fd = new FormData();
		var params = new URLSearchParams(text);
		params.forEach(function(v, k) { fd.append(k, v); });
		return fd;
	}
	if (ct.indexOf('multipart/form-data') !== -1) {
		if (!text) throw new TypeError('Could not parse content as FormData');
		return parseMultipart(text, ct);
	}
	throw new TypeError('Could not parse content as FormData');
};

})();
`

// SetupBodyTypes patches Request/Response with extended body type support.
// Must be called after SetupWebAPIs, SetupStreams, and SetupFormData.
func SetupBodyTypes(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	if err := rt.Eval(bodyTypesJS); err != nil {
		return fmt.Errorf("evaluating bodytypes.js: %w", err)
	}
	return nil
}
