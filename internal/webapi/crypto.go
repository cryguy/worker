package webapi

import (
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"hash"

	"github.com/cryguy/worker/internal/core"
	"github.com/cryguy/worker/internal/eventloop"
)

// cryptoJS wires up the global crypto object with getRandomValues and randomUUID
// backed by Go helper functions, plus a crypto.subtle proxy that delegates
// digest/sign/verify/encrypt/decrypt/importKey/exportKey to Go-backed functions.
//
// Key material is scoped per-request via __requestID — no global key store.
const cryptoJS = `
(function() {
	// Pure-JS base64 encode/decode for the crypto internals.
	const _b64e = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/';
	const _b64d = new Uint8Array(128);
	for (let i = 0; i < _b64e.length; i++) _b64d[_b64e.charCodeAt(i)] = i;

	const crypto = {};

	crypto.getRandomValues = function(typedArray) {
		if (!typedArray || typeof typedArray.length !== 'number') {
			throw new TypeError('getRandomValues requires a TypedArray');
		}
		const b64 = __cryptoGetRandomBytes(typedArray.length);
		let j = 0;
		for (let i = 0; i < b64.length; i += 4) {
			const a = _b64d[b64.charCodeAt(i)];
			const b = _b64d[b64.charCodeAt(i + 1)];
			const c = _b64d[b64.charCodeAt(i + 2)];
			const d = _b64d[b64.charCodeAt(i + 3)];
			if (j < typedArray.length) typedArray[j++] = (a << 2) | (b >> 4);
			if (j < typedArray.length) typedArray[j++] = ((b & 15) << 4) | (c >> 2);
			if (j < typedArray.length) typedArray[j++] = ((c & 3) << 6) | d;
		}
		return typedArray;
	};

	crypto.randomUUID = function() {
		return __cryptoRandomUUID();
	};

	// --- crypto.subtle ---
	const subtle = {};

	subtle.digest = async function(algorithm, data) {
		const algo = typeof algorithm === 'string' ? algorithm : algorithm.name;
		const b64 = __bufferSourceToB64(data);
		const resultB64 = __cryptoDigest(algo, b64);
		return __b64ToBuffer(resultB64);
	};

	class CryptoKey {
		constructor(id, algorithm, type, extractable, usages) {
			this._id = id;
			this.algorithm = algorithm;
			this.type = type;
			this.extractable = extractable;
			this.usages = usages;
		}
	}

	subtle.importKey = async function(format, keyData, algorithm, extractable, usages) {
		const algo = typeof algorithm === 'string' ? { name: algorithm } : algorithm;
		if (format !== 'raw') {
			throw new TypeError('importKey: only raw format is supported');
		}
		const b64 = __bufferSourceToB64(keyData);
		const hashName = algo.hash ? (typeof algo.hash === 'string' ? algo.hash : algo.hash.name) : '';
		const id = __cryptoImportKey(algo.name, hashName, b64);
		const keyType = 'secret';
		return new CryptoKey(id, algo, keyType, extractable, usages);
	};

	subtle.exportKey = async function(format, key) {
		if (format !== 'raw') throw new TypeError('exportKey: only raw format is supported');
		if (!key.extractable) throw new DOMException('key is not extractable', 'InvalidAccessError');
		const b64 = __cryptoExportKey(key._id);
		return __b64ToBuffer(b64);
	};

	subtle.sign = async function(algorithm, key, data) {
		if (key.usages && !key.usages.includes('sign')) {
			throw new TypeError('key usages do not permit this operation');
		}
		const algo = typeof algorithm === 'string' ? { name: algorithm } : algorithm;
		const dataB64 = __bufferSourceToB64(data);
		const resultB64 = __cryptoSign(algo.name, key._id, dataB64);
		return __b64ToBuffer(resultB64);
	};

	subtle.verify = async function(algorithm, key, signature, data) {
		if (key.usages && !key.usages.includes('verify')) {
			throw new TypeError('key usages do not permit this operation');
		}
		const algo = typeof algorithm === 'string' ? { name: algorithm } : algorithm;
		const sigB64 = __bufferSourceToB64(signature);
		const dataB64 = __bufferSourceToB64(data);
		return !!__cryptoVerify(algo.name, key._id, sigB64, dataB64);
	};

	subtle.encrypt = async function(algorithm, key, data) {
		if (key.usages && !key.usages.includes('encrypt')) {
			throw new TypeError('key usages do not permit this operation');
		}
		const algo = typeof algorithm === 'string' ? { name: algorithm } : algorithm;
		const dataB64 = __bufferSourceToB64(data);
		let ivB64 = '';
		if (algo.iv) {
			ivB64 = __bufferSourceToB64(algo.iv);
		}
		let aadB64 = '';
		if (algo.additionalData) {
			aadB64 = __bufferSourceToB64(algo.additionalData);
		}
		const resultB64 = __cryptoEncrypt(algo.name, key._id, dataB64, ivB64, aadB64);
		return __b64ToBuffer(resultB64);
	};

	subtle.decrypt = async function(algorithm, key, data) {
		if (key.usages && !key.usages.includes('decrypt')) {
			throw new TypeError('key usages do not permit this operation');
		}
		const algo = typeof algorithm === 'string' ? { name: algorithm } : algorithm;
		const dataB64 = __bufferSourceToB64(data);
		let ivB64 = '';
		if (algo.iv) {
			ivB64 = __bufferSourceToB64(algo.iv);
		}
		let aadB64 = '';
		if (algo.additionalData) {
			aadB64 = __bufferSourceToB64(algo.additionalData);
		}
		const resultB64 = __cryptoDecrypt(algo.name, key._id, dataB64, ivB64, aadB64);
		return __b64ToBuffer(resultB64);
	};

	// Helper: convert any BufferSource or TypedArray to base64.
	function __bufferSourceToB64(data) {
		let arr;
		if (data instanceof ArrayBuffer) {
			arr = new Uint8Array(data);
		} else if (data && data.buffer instanceof ArrayBuffer) {
			arr = new Uint8Array(data.buffer, data.byteOffset || 0, data.byteLength || data.length);
		} else if (data && typeof data.length === 'number') {
			arr = new Uint8Array(data.length);
			for (let i = 0; i < data.length; i++) arr[i] = data[i];
		} else {
			throw new TypeError('expected BufferSource');
		}
		const len = arr.length;
		const parts = [];
		for (let i = 0; i < len; i += 3) {
			const a = arr[i];
			const b = i + 1 < len ? arr[i + 1] : 0;
			const c = i + 2 < len ? arr[i + 2] : 0;
			parts.push(
				_b64e[a >> 2],
				_b64e[((a & 3) << 4) | (b >> 4)],
				i + 1 < len ? _b64e[((b & 15) << 2) | (c >> 6)] : '=',
				i + 2 < len ? _b64e[c & 63] : '='
			);
		}
		return parts.join('');
	}

	// Helper: convert base64 to ArrayBuffer.
	function __b64ToBuffer(b64) {
		let pad = 0;
		if (b64.length > 0 && b64[b64.length - 1] === '=') pad++;
		if (b64.length > 1 && b64[b64.length - 2] === '=') pad++;
		const outLen = (b64.length * 3 / 4) - pad;
		const buf = new ArrayBuffer(outLen);
		const out = new Uint8Array(buf);
		let j = 0;
		for (let i = 0; i < b64.length; i += 4) {
			const a = _b64d[b64.charCodeAt(i)];
			const b = _b64d[b64.charCodeAt(i + 1)];
			const c = _b64d[b64.charCodeAt(i + 2)];
			const d = _b64d[b64.charCodeAt(i + 3)];
			out[j++] = (a << 2) | (b >> 4);
			if (j < outLen) out[j++] = ((b & 15) << 4) | (c >> 2);
			if (j < outLen) out[j++] = ((c & 3) << 6) | d;
		}
		return buf;
	}

	crypto.subtle = subtle;
	globalThis.crypto = crypto;
	globalThis.CryptoKey = CryptoKey;
	// Expose helpers globally so crypto_ext.js can use them.
	globalThis.__bufferSourceToB64 = __bufferSourceToB64;
	globalThis.__b64ToBuffer = __b64ToBuffer;
})();
`

// SetupCrypto registers Go-backed crypto helpers and evaluates the JS wrapper.
func SetupCrypto(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	// __cryptoGetRandomBytes(n) -> base64 string of n random bytes.
	if err := rt.RegisterFunc("__cryptoGetRandomBytes", func(n int) (string, error) {
		if n <= 0 || n > 65536 {
			return "", fmt.Errorf("getRandomValues: byte length must be 1-65536")
		}
		buf := make([]byte, n)
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("crypto/rand: %v", err)
		}
		return base64.StdEncoding.EncodeToString(buf), nil
	}); err != nil {
		return err
	}

	// __cryptoRandomUUID() -> UUID v4 string.
	if err := rt.RegisterFunc("__cryptoRandomUUID", func() (string, error) {
		var uuid [16]byte
		if _, err := rand.Read(uuid[:]); err != nil {
			return "", fmt.Errorf("crypto/rand: %v", err)
		}
		uuid[6] = (uuid[6] & 0x0f) | 0x40
		uuid[8] = (uuid[8] & 0x3f) | 0x80
		s := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
			uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16])
		return s, nil
	}); err != nil {
		return err
	}

	// __cryptoDigest(algorithm, dataBase64) -> resultBase64
	if err := rt.RegisterFunc("__cryptoDigest", func(algo string, dataB64 string) (string, error) {
		data, err := base64.StdEncoding.DecodeString(dataB64)
		if err != nil {
			return "", fmt.Errorf("digest: invalid base64 data")
		}
		var h hash.Hash
		switch NormalizeAlgo(algo) {
		case "SHA-1":
			h = sha1.New()
		case "SHA-256":
			h = sha256.New()
		case "SHA-384":
			h = sha512.New384()
		case "SHA-512":
			h = sha512.New()
		default:
			return "", fmt.Errorf("digest: unsupported algorithm %q", algo)
		}
		h.Write(data)
		return base64.StdEncoding.EncodeToString(h.Sum(nil)), nil
	}); err != nil {
		return err
	}

	// Note: __cryptoExportKey is registered by SetupCryptoExt (not here)
	// to handle both simple keys and ECDSA EC keys.
	// __cryptoImportKey, __cryptoSign, __cryptoVerify, __cryptoEncrypt,
	// and __cryptoDecrypt are also registered by SetupCryptoExt which runs
	// after SetupCrypto.

	// Evaluate the JS wrapper that builds the crypto global.
	if err := rt.Eval(cryptoJS); err != nil {
		return fmt.Errorf("evaluating crypto.js: %w", err)
	}

	// Override __bufferSourceToB64 with a Go-backed hybrid when BinaryTransferer
	// is available: small buffers (<=64KB) use fast pure-JS btoa, large buffers
	// use the binary bridge with Go's base64.StdEncoding.EncodeToString.
	if bt, ok := rt.(core.BinaryTransferer); ok {
		_ = rt.SetGlobal("__binary_mode", bt.BinaryMode())

		if err := rt.RegisterFunc("__bufferSourceToB64_go", func() (string, error) {
			data, err := bt.ReadBinaryFromJS("__tmp_b64_buf")
			if err != nil {
				return "", fmt.Errorf("bufferSourceToB64: %w", err)
			}
			return base64.StdEncoding.EncodeToString(data), nil
		}); err != nil {
			return fmt.Errorf("registering __bufferSourceToB64_go: %w", err)
		}

		if err := rt.Eval(`globalThis.__bufferSourceToB64 = function(data) {
			var arr;
			if (data instanceof ArrayBuffer) {
				arr = new Uint8Array(data);
			} else if (data && data.buffer instanceof ArrayBuffer) {
				arr = new Uint8Array(data.buffer, data.byteOffset || 0, data.byteLength || data.length);
			} else if (data && typeof data.length === 'number') {
				arr = new Uint8Array(data.length);
				for (var i = 0; i < data.length; i++) arr[i] = data[i];
			} else {
				throw new TypeError('expected BufferSource');
			}
			if (arr.byteLength <= 65536) {
				var _parts = [];
				for (var _i = 0; _i < arr.length; _i += 8192) {
					_parts.push(String.fromCharCode.apply(null, arr.subarray(_i, Math.min(_i + 8192, arr.length))));
				}
				return btoa(_parts.join(''));
			}
			var _bm = globalThis.__binary_mode || 'sab';
			var _buf = (_bm === 'sab') ? new SharedArrayBuffer(arr.byteLength) : new ArrayBuffer(arr.byteLength);
			new Uint8Array(_buf).set(arr);
			globalThis.__tmp_b64_buf = _buf;
			return __bufferSourceToB64_go();
		};`); err != nil {
			return fmt.Errorf("overriding __bufferSourceToB64: %w", err)
		}
	}

	return nil
}

// HashFuncFromAlgo returns the hash.Hash constructor for the given algorithm name.
func HashFuncFromAlgo(algo string) func() hash.Hash {
	switch NormalizeAlgo(algo) {
	case "SHA-1":
		return sha1.New
	case "SHA-256":
		return sha256.New
	case "SHA-384":
		return sha512.New384
	case "SHA-512":
		return sha512.New
	default:
		if algo == "" {
			return sha256.New
		}
		return nil
	}
}

// NormalizeAlgo normalizes algorithm names to their canonical form.
func NormalizeAlgo(name string) string {
	switch name {
	case "sha-1", "SHA-1", "sha1", "SHA1":
		return "SHA-1"
	case "sha-256", "SHA-256", "sha256", "SHA256":
		return "SHA-256"
	case "sha-384", "SHA-384", "sha384", "SHA384":
		return "SHA-384"
	case "sha-512", "SHA-512", "sha512", "SHA512":
		return "SHA-512"
	case "hmac", "HMAC", "Hmac":
		return "HMAC"
	case "aes-gcm", "AES-GCM", "Aes-Gcm":
		return "AES-GCM"
	case "aes-cbc", "AES-CBC", "Aes-Cbc":
		return "AES-CBC"
	case "aes-ctr", "AES-CTR", "Aes-Ctr":
		return "AES-CTR"
	case "aes-kw", "AES-KW", "Aes-Kw":
		return "AES-KW"
	case "ecdsa", "ECDSA", "Ecdsa":
		return "ECDSA"
	case "hkdf", "HKDF", "Hkdf":
		return "HKDF"
	case "pbkdf2", "PBKDF2", "Pbkdf2":
		return "PBKDF2"
	case "rsa-oaep", "RSA-OAEP", "Rsa-Oaep":
		return "RSA-OAEP"
	case "rsassa-pkcs1-v1_5", "RSASSA-PKCS1-v1_5", "RSASSA-PKCS1-V1_5":
		return "RSASSA-PKCS1-v1_5"
	case "rsa-pss", "RSA-PSS", "Rsa-Pss":
		return "RSA-PSS"
	case "ed25519", "Ed25519", "ED25519":
		return "Ed25519"
	default:
		return name
	}
}
