package worker

import (
	"crypto/hmac"
	"encoding/base64"
	"fmt"
	"hash"
	"math"

	"modernc.org/quickjs"
)

// hkdfDeriveBits implements HKDF (RFC 5869) Extract+Expand.
func hkdfDeriveBits(hashFn func() hash.Hash, ikm, salt, info []byte, lengthBits int) ([]byte, error) {
	lengthBytes := lengthBits / 8
	if lengthBits%8 != 0 {
		return nil, fmt.Errorf("HKDF: length must be a multiple of 8")
	}

	// Extract: PRK = HMAC-Hash(salt, IKM)
	if len(salt) == 0 {
		salt = make([]byte, hashFn().Size())
	}
	extractor := hmac.New(hashFn, salt)
	extractor.Write(ikm)
	prk := extractor.Sum(nil)

	// Expand
	hashLen := hashFn().Size()
	n := int(math.Ceil(float64(lengthBytes) / float64(hashLen)))
	if n > 255 {
		return nil, fmt.Errorf("HKDF: output length too large")
	}

	okm := make([]byte, 0, lengthBytes)
	var prev []byte
	for i := 1; i <= n; i++ {
		h := hmac.New(hashFn, prk)
		h.Write(prev)
		h.Write(info)
		h.Write([]byte{byte(i)})
		prev = h.Sum(nil)
		okm = append(okm, prev...)
	}

	return okm[:lengthBytes], nil
}

// pbkdf2DeriveBits implements PBKDF2 (RFC 2898).
func pbkdf2DeriveBits(hashFn func() hash.Hash, password, salt []byte, iterations, lengthBits int) ([]byte, error) {
	lengthBytes := lengthBits / 8
	if lengthBits%8 != 0 {
		return nil, fmt.Errorf("PBKDF2: length must be a multiple of 8")
	}
	if iterations < 1 {
		return nil, fmt.Errorf("PBKDF2: iterations must be at least 1")
	}

	hashLen := hashFn().Size()
	numBlocks := (lengthBytes + hashLen - 1) / hashLen

	dk := make([]byte, 0, numBlocks*hashLen)
	for block := 1; block <= numBlocks; block++ {
		mac := hmac.New(hashFn, password)
		mac.Write(salt)
		mac.Write([]byte{byte(block >> 24), byte(block >> 16), byte(block >> 8), byte(block)})
		u := mac.Sum(nil)
		result := make([]byte, len(u))
		copy(result, u)

		for i := 2; i <= iterations; i++ {
			mac = hmac.New(hashFn, password)
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range result {
				result[j] ^= u[j]
			}
		}
		dk = append(dk, result...)
	}

	return dk[:lengthBytes], nil
}

// cryptoDeriveJS adds deriveBits and deriveKey to crypto.subtle.
const cryptoDeriveJS = `
(function() {
var subtle = crypto.subtle;

subtle.deriveBits = async function(algorithm, baseKey, length) {
	var algo = typeof algorithm === 'string' ? { name: algorithm } : algorithm;
	var hashName = algo.hash ? (typeof algo.hash === 'string' ? algo.hash : algo.hash.name) : '';
	var saltB64 = algo.salt ? __bufferSourceToB64(algo.salt) : '';
	var infoB64 = algo.info ? __bufferSourceToB64(algo.info) : '';
	var iterations = algo.iterations || 0;
	var resultB64 = __cryptoDeriveBits(algo.name, baseKey._id, length, hashName, saltB64, infoB64, iterations);
	return __b64ToBuffer(resultB64);
};

subtle.deriveKey = async function(algorithm, baseKey, derivedKeyAlgorithm, extractable, usages) {
	var dkAlgo = typeof derivedKeyAlgorithm === 'string' ? { name: derivedKeyAlgorithm } : derivedKeyAlgorithm;
	var length = dkAlgo.length || 0;
	if (!length) {
		var algoName = (dkAlgo.name || '').toUpperCase();
		if (algoName === 'HMAC') {
			var h = dkAlgo.hash ? (typeof dkAlgo.hash === 'string' ? dkAlgo.hash : dkAlgo.hash.name) : 'SHA-256';
			switch (h) {
				case 'SHA-1': length = 160; break;
				case 'SHA-384': length = 384; break;
				case 'SHA-512': length = 512; break;
				default: length = 256; break;
			}
		} else if (algoName.indexOf('AES') === 0) {
			length = 256;
		} else {
			length = 256;
		}
	}
	var bits = await subtle.deriveBits(algorithm, baseKey, length);
	return subtle.importKey('raw', bits, derivedKeyAlgorithm, extractable, usages);
};

})();
`

// setupCryptoDerive registers HKDF and PBKDF2 deriveBits/deriveKey.
// Must run after setupCryptoExt.
func setupCryptoDerive(vm *quickjs.VM, _ *eventLoop) error {
	registerGoFunc(vm, "__cryptoDeriveBits", func(algoName string, keyID int, lengthBits int, hashName, saltB64, infoB64 string, iterations int) (string, error) {
		reqID := getReqIDFromJS(vm)
		entry := getCryptoKey(reqID, keyID)
		if entry == nil {
			return "", fmt.Errorf("deriveBits: key not found")
		}

		hashFn := hashFuncFromAlgo(hashName)
		if hashFn == nil {
			return "", fmt.Errorf("deriveBits: unsupported hash %q", hashName)
		}

		switch normalizeAlgo(algoName) {
		case "HKDF":
			salt, err := base64.StdEncoding.DecodeString(saltB64)
			if err != nil {
				return "", fmt.Errorf("deriveBits: invalid salt base64")
			}
			infoBytes, err := base64.StdEncoding.DecodeString(infoB64)
			if err != nil {
				return "", fmt.Errorf("deriveBits: invalid info base64")
			}
			result, err := hkdfDeriveBits(hashFn, entry.data, salt, infoBytes, lengthBits)
			if err != nil {
				return "", fmt.Errorf("deriveBits: %s", err.Error())
			}
			return base64.StdEncoding.EncodeToString(result), nil

		case "PBKDF2":
			salt, err := base64.StdEncoding.DecodeString(saltB64)
			if err != nil {
				return "", fmt.Errorf("deriveBits: invalid salt base64")
			}
			result, err := pbkdf2DeriveBits(hashFn, entry.data, salt, iterations, lengthBits)
			if err != nil {
				return "", fmt.Errorf("deriveBits: %s", err.Error())
			}
			return base64.StdEncoding.EncodeToString(result), nil

		default:
			return "", fmt.Errorf("deriveBits: unsupported algorithm %q", algoName)
		}
	}, false)

	if err := evalDiscard(vm, cryptoDeriveJS); err != nil {
		return fmt.Errorf("evaluating crypto_derive.js: %w", err)
	}
	return nil
}
