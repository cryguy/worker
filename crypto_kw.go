package worker

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"

	"modernc.org/quickjs"
)

// aesKeyWrap implements RFC 3394 AES Key Wrap.
// The plaintext must be a multiple of 8 bytes (64-bit blocks).
func aesKeyWrap(kek, plaintext []byte) ([]byte, error) {
	if len(plaintext)%8 != 0 || len(plaintext) < 16 {
		return nil, fmt.Errorf("plaintext must be a multiple of 8 bytes and at least 16 bytes")
	}

	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}

	n := len(plaintext) / 8 // number of 64-bit blocks

	// Initialize: A = IV (default 0xA6A6A6A6A6A6A6A6), R[1..n] = plaintext blocks
	var a [8]byte
	for i := range a {
		a[i] = 0xA6
	}

	r := make([][]byte, n)
	for i := 0; i < n; i++ {
		r[i] = make([]byte, 8)
		copy(r[i], plaintext[i*8:(i+1)*8])
	}

	// Wrap: 6 rounds
	buf := make([]byte, 16)
	for j := 0; j < 6; j++ {
		for i := 0; i < n; i++ {
			copy(buf[:8], a[:])
			copy(buf[8:], r[i])
			block.Encrypt(buf, buf)
			copy(a[:], buf[:8])
			// XOR counter t = n*j + i + 1 into A
			t := uint64(n*j + i + 1)
			var tBytes [8]byte
			binary.BigEndian.PutUint64(tBytes[:], t)
			for k := 0; k < 8; k++ {
				a[k] ^= tBytes[k]
			}
			copy(r[i], buf[8:])
		}
	}

	// Output: A || R[1] || R[2] || ... || R[n]
	out := make([]byte, 8+n*8)
	copy(out[:8], a[:])
	for i := 0; i < n; i++ {
		copy(out[8+i*8:8+(i+1)*8], r[i])
	}
	return out, nil
}

// aesKeyUnwrap implements RFC 3394 AES Key Unwrap.
func aesKeyUnwrap(kek, ciphertext []byte) ([]byte, error) {
	if len(ciphertext)%8 != 0 || len(ciphertext) < 24 {
		return nil, fmt.Errorf("ciphertext must be a multiple of 8 bytes and at least 24 bytes")
	}

	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}

	n := (len(ciphertext) / 8) - 1 // number of 64-bit blocks (excluding A)

	// Initialize: A = C[0], R[1..n] = C[1..n]
	var a [8]byte
	copy(a[:], ciphertext[:8])

	r := make([][]byte, n)
	for i := 0; i < n; i++ {
		r[i] = make([]byte, 8)
		copy(r[i], ciphertext[(i+1)*8:(i+2)*8])
	}

	// Unwrap: 6 rounds in reverse
	buf := make([]byte, 16)
	for j := 5; j >= 0; j-- {
		for i := n - 1; i >= 0; i-- {
			// XOR counter t = n*j + i + 1 out of A
			t := uint64(n*j + i + 1)
			var tBytes [8]byte
			binary.BigEndian.PutUint64(tBytes[:], t)
			for k := 0; k < 8; k++ {
				a[k] ^= tBytes[k]
			}
			copy(buf[:8], a[:])
			copy(buf[8:], r[i])
			block.Decrypt(buf, buf)
			copy(a[:], buf[:8])
			copy(r[i], buf[8:])
		}
	}

	// Verify IV
	for i := 0; i < 8; i++ {
		if a[i] != 0xA6 {
			return nil, fmt.Errorf("AES-KW unwrap: integrity check failed")
		}
	}

	// Output: R[1] || R[2] || ... || R[n]
	out := make([]byte, n*8)
	for i := 0; i < n; i++ {
		copy(out[i*8:(i+1)*8], r[i])
	}
	return out, nil
}

// cryptoAesCtrKwJS patches crypto.subtle with AES-CTR encrypt/decrypt and
// AES-KW wrapKey/unwrapKey using the chain-of-responsibility pattern.
const cryptoAesCtrKwJS = `
(function() {
var subtle = crypto.subtle;
var CK = CryptoKey;
var _prevEncrypt = subtle.encrypt;
var _prevDecrypt = subtle.decrypt;
var _prevGenerateKey = subtle.generateKey;
var _prevWrapKey = subtle.wrapKey;
var _prevUnwrapKey = subtle.unwrapKey;

subtle.encrypt = async function(algorithm, key, data) {
	var algo = typeof algorithm === 'string' ? { name: algorithm } : algorithm;
	if (algo.name === 'AES-CTR') {
		if (!algo.counter) throw new TypeError('AES-CTR requires counter parameter');
		var counterB64 = __bufferSourceToB64(algo.counter);
		var length = algo.length || 64;
		var dataB64 = __bufferSourceToB64(data);
		var resultB64 = __cryptoEncryptAesCtr(key._id, dataB64, counterB64, length);
		return __b64ToBuffer(resultB64);
	}
	return _prevEncrypt.call(this, algorithm, key, data);
};

subtle.decrypt = async function(algorithm, key, data) {
	var algo = typeof algorithm === 'string' ? { name: algorithm } : algorithm;
	if (algo.name === 'AES-CTR') {
		if (!algo.counter) throw new TypeError('AES-CTR requires counter parameter');
		var counterB64 = __bufferSourceToB64(algo.counter);
		var length = algo.length || 64;
		var dataB64 = __bufferSourceToB64(data);
		var resultB64 = __cryptoDecryptAesCtr(key._id, dataB64, counterB64, length);
		return __b64ToBuffer(resultB64);
	}
	return _prevDecrypt.call(this, algorithm, key, data);
};

subtle.generateKey = async function(algorithm, extractable, usages) {
	var algo = typeof algorithm === 'string' ? { name: algorithm } : algorithm;
	if (algo.name === 'AES-CTR' || algo.name === 'AES-KW') {
		var length = algo.length || 256;
		var resultJSON = __cryptoGenerateKeyAes(algo.name, length, extractable);
		var result = JSON.parse(resultJSON);
		if (result.error) throw new TypeError(result.error);
		return new CK(result.keyId, algo, 'secret', extractable, usages);
	}
	return _prevGenerateKey.call(this, algorithm, extractable, usages);
};

subtle.wrapKey = async function(format, key, wrappingKey, wrapAlgorithm) {
	var wrapAlgo = typeof wrapAlgorithm === 'string' ? { name: wrapAlgorithm } : wrapAlgorithm;
	if (wrapAlgo.name === 'AES-KW') {
		var exported = await subtle.exportKey('raw', key);
		var dataB64 = __bufferSourceToB64(exported);
		var resultB64 = __cryptoWrapKeyAESKW(wrappingKey._id, dataB64);
		return __b64ToBuffer(resultB64);
	}
	return _prevWrapKey.call(this, format, key, wrappingKey, wrapAlgorithm);
};

subtle.unwrapKey = async function(format, wrappedKey, unwrappingKey, unwrapAlgorithm, unwrappedKeyAlgorithm, extractable, keyUsages) {
	var unwrapAlgo = typeof unwrapAlgorithm === 'string' ? { name: unwrapAlgorithm } : unwrapAlgorithm;
	if (unwrapAlgo.name === 'AES-KW') {
		var wrappedB64 = __bufferSourceToB64(wrappedKey);
		var resultB64 = __cryptoUnwrapKeyAESKW(unwrappingKey._id, wrappedB64);
		var unwrappedData = __b64ToBuffer(resultB64);
		return subtle.importKey('raw', unwrappedData, unwrappedKeyAlgorithm, extractable, keyUsages);
	}
	return _prevUnwrapKey.call(this, format, wrappedKey, unwrappingKey, unwrapAlgorithm, unwrappedKeyAlgorithm, extractable, keyUsages);
};

})();
`

// setupCryptoAesCtrKw registers AES-CTR and AES-KW Go functions and evaluates
// the JS patches. Must run after setupCryptoRSA (or at least after setupCryptoExt).
func setupCryptoAesCtrKw(vm *quickjs.VM, _ *eventLoop) error {
	// __cryptoEncryptAesCtr(keyID, dataB64, counterB64, length) -> resultB64
	registerGoFunc(vm, "__cryptoEncryptAesCtr", func(keyID int, dataB64, counterB64 string, length int) (string, error) {
		data, err := base64.StdEncoding.DecodeString(dataB64)
		if err != nil {
			return "", fmt.Errorf("encryptAesCtr: invalid base64 data")
		}
		counter, err := base64.StdEncoding.DecodeString(counterB64)
		if err != nil {
			return "", fmt.Errorf("encryptAesCtr: invalid counter base64")
		}
		if len(counter) != 16 {
			return "", fmt.Errorf("encryptAesCtr: counter must be exactly 16 bytes, got %d", len(counter))
		}

		reqID := getReqIDFromJS(vm)
		entry := getCryptoKey(reqID, keyID)
		if entry == nil {
			return "", fmt.Errorf("encryptAesCtr: key not found")
		}

		block, err := aes.NewCipher(entry.data)
		if err != nil {
			return "", fmt.Errorf("encryptAesCtr: %s", err.Error())
		}

		ct := make([]byte, len(data))
		stream := cipher.NewCTR(block, counter)
		stream.XORKeyStream(ct, data)

		return base64.StdEncoding.EncodeToString(ct), nil
	}, false)

	// __cryptoDecryptAesCtr(keyID, dataB64, counterB64, length) -> resultB64
	registerGoFunc(vm, "__cryptoDecryptAesCtr", func(keyID int, dataB64, counterB64 string, length int) (string, error) {
		data, err := base64.StdEncoding.DecodeString(dataB64)
		if err != nil {
			return "", fmt.Errorf("decryptAesCtr: invalid base64 data")
		}
		counter, err := base64.StdEncoding.DecodeString(counterB64)
		if err != nil {
			return "", fmt.Errorf("decryptAesCtr: invalid counter base64")
		}
		if len(counter) != 16 {
			return "", fmt.Errorf("decryptAesCtr: counter must be exactly 16 bytes, got %d", len(counter))
		}

		reqID := getReqIDFromJS(vm)
		entry := getCryptoKey(reqID, keyID)
		if entry == nil {
			return "", fmt.Errorf("decryptAesCtr: key not found")
		}

		block, err := aes.NewCipher(entry.data)
		if err != nil {
			return "", fmt.Errorf("decryptAesCtr: %s", err.Error())
		}

		pt := make([]byte, len(data))
		stream := cipher.NewCTR(block, counter)
		stream.XORKeyStream(pt, data)

		return base64.StdEncoding.EncodeToString(pt), nil
	}, false)

	// __cryptoGenerateKeyAes(algoName, length, extractable) -> JSON { keyId } or { error }
	registerGoFunc(vm, "__cryptoGenerateKeyAes", func(algoName string, bitLength int, extractableVal bool) (string, error) {
		var byteLength int
		switch bitLength {
		case 128:
			byteLength = 16
		case 192:
			byteLength = 24
		case 256:
			byteLength = 32
		default:
			return fmt.Sprintf(`{"error":"generateKey: unsupported key length %d for %s"}`, bitLength, algoName), nil
		}

		reqID := getReqIDFromJS(vm)
		if getRequestState(reqID) == nil {
			return `{"error":"no active request state"}`, nil
		}

		keyData := make([]byte, byteLength)
		if _, err := rand.Read(keyData); err != nil {
			return fmt.Sprintf(`{"error":"key generation failed: %s"}`, err.Error()), nil
		}

		id := importCryptoKeyFull(reqID, &cryptoKeyEntry{
			data:        keyData,
			algoName:    normalizeAlgo(algoName),
			keyType:     "secret",
			extractable: extractableVal,
		})
		return fmt.Sprintf(`{"keyId":%d}`, id), nil
	}, false)

	// __cryptoWrapKeyAESKW(wrappingKeyID, dataB64) -> wrappedB64
	registerGoFunc(vm, "__cryptoWrapKeyAESKW", func(wrappingKeyID int, dataB64 string) (string, error) {
		data, err := base64.StdEncoding.DecodeString(dataB64)
		if err != nil {
			return "", fmt.Errorf("wrapKeyAESKW: invalid base64 data")
		}

		reqID := getReqIDFromJS(vm)
		wrappingEntry := getCryptoKey(reqID, wrappingKeyID)
		if wrappingEntry == nil {
			return "", fmt.Errorf("wrapKeyAESKW: wrapping key not found")
		}

		// Key data to wrap must be a multiple of 8 bytes
		if len(data)%8 != 0 {
			return "", fmt.Errorf("wrapKeyAESKW: key data must be a multiple of 8 bytes")
		}
		if len(data) < 16 {
			return "", fmt.Errorf("wrapKeyAESKW: key data must be at least 16 bytes")
		}

		wrapped, err := aesKeyWrap(wrappingEntry.data, data)
		if err != nil {
			return "", fmt.Errorf("wrapKeyAESKW: %s", err.Error())
		}

		return base64.StdEncoding.EncodeToString(wrapped), nil
	}, false)

	// __cryptoUnwrapKeyAESKW(unwrappingKeyID, wrappedB64) -> unwrappedB64
	registerGoFunc(vm, "__cryptoUnwrapKeyAESKW", func(unwrappingKeyID int, wrappedB64 string) (string, error) {
		wrappedData, err := base64.StdEncoding.DecodeString(wrappedB64)
		if err != nil {
			return "", fmt.Errorf("unwrapKeyAESKW: invalid base64 data")
		}

		reqID := getReqIDFromJS(vm)
		unwrappingEntry := getCryptoKey(reqID, unwrappingKeyID)
		if unwrappingEntry == nil {
			return "", fmt.Errorf("unwrapKeyAESKW: unwrapping key not found")
		}

		unwrapped, err := aesKeyUnwrap(unwrappingEntry.data, wrappedData)
		if err != nil {
			return "", fmt.Errorf("unwrapKeyAESKW: %s", err.Error())
		}

		return base64.StdEncoding.EncodeToString(unwrapped), nil
	}, false)

	// Evaluate the JS patches.
	if err := evalDiscard(vm, cryptoAesCtrKwJS); err != nil {
		return fmt.Errorf("evaluating crypto_kw.js: %w", err)
	}

	return nil
}
