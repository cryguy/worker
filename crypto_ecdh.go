package worker

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"modernc.org/quickjs"
)

// ecdhCurveFromName returns the ecdh.Curve for the given Web Crypto curve name.
func ecdhCurveFromName(name string) ecdh.Curve {
	switch name {
	case "P-256":
		return ecdh.P256()
	case "P-384":
		return ecdh.P384()
	case "P-521":
		return ecdh.P521()
	case "X25519":
		return ecdh.X25519()
	default:
		return nil
	}
}

// cryptoECDHJS patches crypto.subtle to support ECDH and X25519 key agreement.
// Uses chain-of-responsibility: saves references to previous implementations
// and delegates non-ECDH/X25519 calls to them.
const cryptoECDHJS = `
(function() {
var subtle = crypto.subtle;
var CK = CryptoKey;
var _prevGenerateKey = subtle.generateKey;
var _prevDeriveBits = subtle.deriveBits;
var _prevDeriveKey = subtle.deriveKey;
var _prevImportKey = subtle.importKey;
var _prevExportKey = subtle.exportKey;

subtle.generateKey = async function(algorithm, extractable, usages) {
	var algo = typeof algorithm === 'string' ? { name: algorithm } : algorithm;
	if (algo.name === 'ECDH') {
		var curve = algo.namedCurve || 'P-256';
		var resultJSON = __cryptoGenerateECDH(curve, extractable);
		var result = JSON.parse(resultJSON);
		if (result.error) throw new TypeError(result.error);
		return {
			privateKey: new CK(result.privateKeyId, { name: 'ECDH', namedCurve: curve }, 'private', extractable,
				usages.filter(function(u) { return u === 'deriveBits' || u === 'deriveKey'; })),
			publicKey: new CK(result.publicKeyId, { name: 'ECDH', namedCurve: curve }, 'public', extractable, []),
		};
	}
	if (algo.name === 'X25519') {
		var resultJSON = __cryptoGenerateX25519(extractable);
		var result = JSON.parse(resultJSON);
		if (result.error) throw new TypeError(result.error);
		return {
			privateKey: new CK(result.privateKeyId, { name: 'X25519' }, 'private', extractable,
				usages.filter(function(u) { return u === 'deriveBits' || u === 'deriveKey'; })),
			publicKey: new CK(result.publicKeyId, { name: 'X25519' }, 'public', extractable, []),
		};
	}
	return _prevGenerateKey.call(this, algorithm, extractable, usages);
};

subtle.deriveBits = async function(algorithm, baseKey, length) {
	var algo = typeof algorithm === 'string' ? { name: algorithm } : algorithm;
	if (algo.name === 'ECDH') {
		var pubKey = algo.public;
		if (!pubKey || !pubKey._id && pubKey._id !== 0) throw new TypeError('ECDH deriveBits requires algorithm.public');
		var resultB64 = __cryptoDeriveECDH(baseKey._id, pubKey._id, length);
		return __b64ToBuffer(resultB64);
	}
	if (algo.name === 'X25519') {
		var pubKey = algo.public;
		if (!pubKey || !pubKey._id && pubKey._id !== 0) throw new TypeError('X25519 deriveBits requires algorithm.public');
		var resultB64 = __cryptoDeriveX25519(baseKey._id, pubKey._id, length);
		return __b64ToBuffer(resultB64);
	}
	return _prevDeriveBits.call(this, algorithm, baseKey, length);
};

subtle.deriveKey = async function(algorithm, baseKey, derivedKeyAlgorithm, extractable, usages) {
	var algo = typeof algorithm === 'string' ? { name: algorithm } : algorithm;
	if (algo.name === 'ECDH' || algo.name === 'X25519') {
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
	}
	return _prevDeriveKey.call(this, algorithm, baseKey, derivedKeyAlgorithm, extractable, usages);
};

subtle.importKey = async function(format, keyData, algorithm, extractable, usages) {
	var algo = typeof algorithm === 'string' ? { name: algorithm } : algorithm;
	if (algo.name === 'ECDH') {
		var curve = algo.namedCurve || 'P-256';
		var dataStr;
		if (format === 'jwk') {
			dataStr = JSON.stringify(keyData);
		} else {
			dataStr = __bufferSourceToB64(keyData);
		}
		var resultJSON = __cryptoImportECDH(format, dataStr, curve, extractable);
		var result = JSON.parse(resultJSON);
		if (result.error) throw new TypeError(result.error);
		return new CK(result.keyId, { name: 'ECDH', namedCurve: curve }, result.keyType, extractable, usages);
	}
	if (algo.name === 'X25519') {
		var dataStr = __bufferSourceToB64(keyData);
		var keyType = 'public';
		if (usages && (usages.indexOf('deriveBits') >= 0 || usages.indexOf('deriveKey') >= 0)) {
			keyType = 'private';
		}
		var resultJSON = __cryptoImportX25519(format, dataStr, keyType, extractable);
		var result = JSON.parse(resultJSON);
		if (result.error) throw new TypeError(result.error);
		return new CK(result.keyId, { name: 'X25519' }, result.keyType, extractable, usages);
	}
	return _prevImportKey.call(this, format, keyData, algorithm, extractable, usages);
};

subtle.exportKey = async function(format, key) {
	if (key.algorithm.name === 'ECDH') {
		if (!key.extractable) throw new DOMException('key is not extractable', 'InvalidAccessError');
		var resultStr = __cryptoExportECDH(key._id, format);
		if (format === 'jwk') {
			return JSON.parse(resultStr);
		}
		return __b64ToBuffer(resultStr);
	}
	if (key.algorithm.name === 'X25519') {
		if (!key.extractable) throw new DOMException('key is not extractable', 'InvalidAccessError');
		var resultStr = __cryptoExportX25519(key._id, format);
		return __b64ToBuffer(resultStr);
	}
	return _prevExportKey.call(this, format, key);
};

})();
`

// setupCryptoECDH registers ECDH and X25519 key agreement operations.
// Must run after setupCryptoDerive.
func setupCryptoECDH(vm *quickjs.VM, _ *eventLoop) error {
	// __cryptoGenerateECDH(curve, extractable) -> JSON { privateKeyId, publicKeyId }
	registerGoFunc(vm, "__cryptoGenerateECDH", func(curveName string, extractableVal bool) (string, error) {
		reqID := getReqIDFromJS(vm)
		if getRequestState(reqID) == nil {
			return `{"error":"no active request state"}`, nil
		}

		curve := ecdhCurveFromName(curveName)
		if curve == nil {
			return fmt.Sprintf(`{"error":"unsupported curve %q"}`, curveName), nil
		}

		privKey, err := curve.GenerateKey(rand.Reader)
		if err != nil {
			return fmt.Sprintf(`{"error":"key generation failed: %s"}`, err.Error()), nil
		}

		privID := importCryptoKeyFull(reqID, &cryptoKeyEntry{
			algoName: "ECDH", keyType: "private", namedCurve: curveName, ecKey: privKey, extractable: extractableVal,
		})
		pubID := importCryptoKeyFull(reqID, &cryptoKeyEntry{
			algoName: "ECDH", keyType: "public", namedCurve: curveName, ecKey: privKey.PublicKey(), extractable: extractableVal,
		})

		return fmt.Sprintf(`{"privateKeyId":%d,"publicKeyId":%d}`, privID, pubID), nil
	}, false)

	// __cryptoDeriveECDH(privateKeyID, publicKeyID, lengthBits) -> base64 shared secret
	registerGoFunc(vm, "__cryptoDeriveECDH", func(privKeyID, pubKeyID int, lengthBits int) (string, error) {
		reqID := getReqIDFromJS(vm)
		privEntry := getCryptoKey(reqID, privKeyID)
		if privEntry == nil {
			return "", fmt.Errorf("deriveECDH: private key not found")
		}
		pubEntry := getCryptoKey(reqID, pubKeyID)
		if pubEntry == nil {
			return "", fmt.Errorf("deriveECDH: public key not found")
		}

		privKey, ok := privEntry.ecKey.(*ecdh.PrivateKey)
		if !ok {
			return "", fmt.Errorf("deriveECDH: key is not an ECDH private key")
		}
		pubKey, ok := pubEntry.ecKey.(*ecdh.PublicKey)
		if !ok {
			return "", fmt.Errorf("deriveECDH: key is not an ECDH public key")
		}

		shared, err := privKey.ECDH(pubKey)
		if err != nil {
			return "", fmt.Errorf("deriveECDH: %s", err.Error())
		}

		// Truncate to requested bit length
		lengthBytes := lengthBits / 8
		if lengthBytes > len(shared) {
			lengthBytes = len(shared)
		}

		return base64.StdEncoding.EncodeToString(shared[:lengthBytes]), nil
	}, false)

	// __cryptoImportECDH(format, dataB64, curve, extractable) -> JSON { keyId, keyType }
	registerGoFunc(vm, "__cryptoImportECDH", func(format, dataStr, curveName string, extractableVal bool) (string, error) {
		reqID := getReqIDFromJS(vm)
		if getRequestState(reqID) == nil {
			return `{"error":"no active request state"}`, nil
		}

		curve := ecdhCurveFromName(curveName)
		if curve == nil {
			return fmt.Sprintf(`{"error":"unsupported curve %q"}`, curveName), nil
		}

		switch format {
		case "raw":
			keyData, err := base64.StdEncoding.DecodeString(dataStr)
			if err != nil {
				return `{"error":"invalid base64"}`, nil
			}
			// Raw format is always a public key (uncompressed point)
			pubKey, err := curve.NewPublicKey(keyData)
			if err != nil {
				return fmt.Sprintf(`{"error":"invalid ECDH public key: %s"}`, err.Error()), nil
			}
			id := importCryptoKeyFull(reqID, &cryptoKeyEntry{
				algoName: "ECDH", keyType: "public", namedCurve: curveName, ecKey: pubKey, extractable: extractableVal,
			})
			return fmt.Sprintf(`{"keyId":%d,"keyType":"public"}`, id), nil

		case "jwk":
			return importECDHJWK(reqID, dataStr, curveName, curve, extractableVal)

		default:
			return fmt.Sprintf(`{"error":"unsupported format %q"}`, format), nil
		}
	}, false)

	// __cryptoExportECDH(keyID, format) -> base64 or JSON string
	registerGoFunc(vm, "__cryptoExportECDH", func(keyID int, format string) (string, error) {
		reqID := getReqIDFromJS(vm)
		entry := getCryptoKey(reqID, keyID)
		if entry == nil {
			return "", fmt.Errorf("exportECDH: key not found")
		}
		if !entry.extractable {
			return "", fmt.Errorf("key is not extractable")
		}

		switch format {
		case "raw":
			switch k := entry.ecKey.(type) {
			case *ecdh.PublicKey:
				return base64.StdEncoding.EncodeToString(k.Bytes()), nil
			case *ecdh.PrivateKey:
				// Export public key bytes for raw format
				return base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
			default:
				return "", fmt.Errorf("exportECDH: not an ECDH key")
			}

		case "jwk":
			return exportECDHJWK(entry)

		default:
			return "", fmt.Errorf("exportECDH: unsupported format %q", format)
		}
	}, false)

	// --- X25519 callbacks ---

	// __cryptoGenerateX25519(extractable) -> JSON { privateKeyId, publicKeyId }
	registerGoFunc(vm, "__cryptoGenerateX25519", func(extractableVal bool) (string, error) {
		reqID := getReqIDFromJS(vm)
		if getRequestState(reqID) == nil {
			return `{"error":"no active request state"}`, nil
		}

		privKey, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return fmt.Sprintf(`{"error":"key generation failed: %s"}`, err.Error()), nil
		}

		privID := importCryptoKeyFull(reqID, &cryptoKeyEntry{
			algoName: "X25519", keyType: "private", ecKey: privKey, extractable: extractableVal,
		})
		pubID := importCryptoKeyFull(reqID, &cryptoKeyEntry{
			algoName: "X25519", keyType: "public", ecKey: privKey.PublicKey(), extractable: extractableVal,
		})

		return fmt.Sprintf(`{"privateKeyId":%d,"publicKeyId":%d}`, privID, pubID), nil
	}, false)

	// __cryptoDeriveX25519(privateKeyID, publicKeyID, lengthBits) -> base64 shared secret
	registerGoFunc(vm, "__cryptoDeriveX25519", func(privKeyID, pubKeyID int, lengthBits int) (string, error) {
		reqID := getReqIDFromJS(vm)
		privEntry := getCryptoKey(reqID, privKeyID)
		if privEntry == nil {
			return "", fmt.Errorf("deriveX25519: private key not found")
		}
		pubEntry := getCryptoKey(reqID, pubKeyID)
		if pubEntry == nil {
			return "", fmt.Errorf("deriveX25519: public key not found")
		}

		privKey, ok := privEntry.ecKey.(*ecdh.PrivateKey)
		if !ok {
			return "", fmt.Errorf("deriveX25519: key is not an X25519 private key")
		}
		pubKey, ok := pubEntry.ecKey.(*ecdh.PublicKey)
		if !ok {
			return "", fmt.Errorf("deriveX25519: key is not an X25519 public key")
		}

		shared, err := privKey.ECDH(pubKey)
		if err != nil {
			return "", fmt.Errorf("deriveX25519: %s", err.Error())
		}

		// Truncate to requested bit length
		lengthBytes := lengthBits / 8
		if lengthBytes > len(shared) {
			lengthBytes = len(shared)
		}

		return base64.StdEncoding.EncodeToString(shared[:lengthBytes]), nil
	}, false)

	// __cryptoImportX25519(format, dataB64, keyType, extractable) -> JSON { keyId, keyType }
	registerGoFunc(vm, "__cryptoImportX25519", func(format, dataStr, keyType string, extractableVal bool) (string, error) {
		reqID := getReqIDFromJS(vm)
		if getRequestState(reqID) == nil {
			return `{"error":"no active request state"}`, nil
		}

		if format != "raw" {
			return fmt.Sprintf(`{"error":"X25519 only supports raw format, got %q"}`, format), nil
		}

		keyData, err := base64.StdEncoding.DecodeString(dataStr)
		if err != nil {
			return `{"error":"invalid base64"}`, nil
		}

		if len(keyData) != 32 {
			return fmt.Sprintf(`{"error":"X25519 key must be 32 bytes, got %d"}`, len(keyData)), nil
		}

		curve := ecdh.X25519()
		if keyType == "private" {
			privKey, err := curve.NewPrivateKey(keyData)
			if err != nil {
				return fmt.Sprintf(`{"error":"invalid X25519 private key: %s"}`, err.Error()), nil
			}
			id := importCryptoKeyFull(reqID, &cryptoKeyEntry{
				algoName: "X25519", keyType: "private", ecKey: privKey, extractable: extractableVal,
			})
			return fmt.Sprintf(`{"keyId":%d,"keyType":"private"}`, id), nil
		}

		pubKey, err := curve.NewPublicKey(keyData)
		if err != nil {
			return fmt.Sprintf(`{"error":"invalid X25519 public key: %s"}`, err.Error()), nil
		}
		id := importCryptoKeyFull(reqID, &cryptoKeyEntry{
			algoName: "X25519", keyType: "public", ecKey: pubKey, extractable: extractableVal,
		})
		return fmt.Sprintf(`{"keyId":%d,"keyType":"public"}`, id), nil
	}, false)

	// __cryptoExportX25519(keyID, format) -> base64
	registerGoFunc(vm, "__cryptoExportX25519", func(keyID int, format string) (string, error) {
		if format != "raw" {
			return "", fmt.Errorf("exportX25519: only raw format supported, got %q", format)
		}

		reqID := getReqIDFromJS(vm)
		entry := getCryptoKey(reqID, keyID)
		if entry == nil {
			return "", fmt.Errorf("exportX25519: key not found")
		}
		if !entry.extractable {
			return "", fmt.Errorf("key is not extractable")
		}

		switch k := entry.ecKey.(type) {
		case *ecdh.PublicKey:
			return base64.StdEncoding.EncodeToString(k.Bytes()), nil
		case *ecdh.PrivateKey:
			return base64.StdEncoding.EncodeToString(k.Bytes()), nil
		default:
			return "", fmt.Errorf("exportX25519: not an X25519 key")
		}
	}, false)

	if err := evalDiscard(vm, cryptoECDHJS); err != nil {
		return fmt.Errorf("evaluating crypto_ecdh.js: %w", err)
	}
	return nil
}

// importECDHJWK imports an ECDH key from JWK format.
func importECDHJWK(reqID uint64, dataStr, curveName string, curve ecdh.Curve, extractable bool) (string, error) {
	var jwk struct {
		Kty string `json:"kty"`
		Crv string `json:"crv"`
		X   string `json:"x"`
		Y   string `json:"y"`
		D   string `json:"d"`
	}
	if err := json.Unmarshal([]byte(dataStr), &jwk); err != nil {
		return `{"error":"invalid JWK JSON"}`, nil
	}
	if jwk.Kty != "EC" {
		return `{"error":"JWK kty must be EC for ECDH"}`, nil
	}
	if jwk.Crv != curveName {
		return fmt.Sprintf(`{"error":"JWK crv %q does not match algorithm curve %q"}`, jwk.Crv, curveName), nil
	}

	xBytes, err := base64.RawURLEncoding.DecodeString(jwk.X)
	if err != nil {
		return `{"error":"invalid JWK x value"}`, nil
	}
	yBytes, err := base64.RawURLEncoding.DecodeString(jwk.Y)
	if err != nil {
		return `{"error":"invalid JWK y value"}`, nil
	}

	if jwk.D != "" {
		// Private key
		dBytes, err := base64.RawURLEncoding.DecodeString(jwk.D)
		if err != nil {
			return `{"error":"invalid JWK d value"}`, nil
		}
		privKey, err := curve.NewPrivateKey(dBytes)
		if err != nil {
			return fmt.Sprintf(`{"error":"invalid ECDH private key: %s"}`, err.Error()), nil
		}
		id := importCryptoKeyFull(reqID, &cryptoKeyEntry{
			algoName: "ECDH", keyType: "private", namedCurve: curveName, ecKey: privKey, extractable: extractable,
		})
		return fmt.Sprintf(`{"keyId":%d,"keyType":"private"}`, id), nil
	}

	// Public key - reconstruct uncompressed point: 0x04 || x || y
	uncompressed := make([]byte, 1+len(xBytes)+len(yBytes))
	uncompressed[0] = 0x04
	copy(uncompressed[1:], xBytes)
	copy(uncompressed[1+len(xBytes):], yBytes)

	pubKey, err := curve.NewPublicKey(uncompressed)
	if err != nil {
		return fmt.Sprintf(`{"error":"invalid ECDH public key: %s"}`, err.Error()), nil
	}
	id := importCryptoKeyFull(reqID, &cryptoKeyEntry{
		algoName: "ECDH", keyType: "public", namedCurve: curveName, ecKey: pubKey, extractable: extractable,
	})
	return fmt.Sprintf(`{"keyId":%d,"keyType":"public"}`, id), nil
}

// exportECDHJWK exports an ECDH key as JWK.
func exportECDHJWK(entry *cryptoKeyEntry) (string, error) {
	jwk := map[string]string{
		"kty": "EC",
		"crv": entry.namedCurve,
	}

	var pubBytes []byte
	switch k := entry.ecKey.(type) {
	case *ecdh.PublicKey:
		pubBytes = k.Bytes()
	case *ecdh.PrivateKey:
		pubBytes = k.PublicKey().Bytes()
		jwk["d"] = base64.RawURLEncoding.EncodeToString(k.Bytes())
	default:
		return "", fmt.Errorf("exportECDH: not an ECDH key")
	}

	// Uncompressed point: 0x04 || x || y
	if len(pubBytes) < 3 || pubBytes[0] != 0x04 {
		return "", fmt.Errorf("exportECDH: unexpected public key format")
	}
	coordLen := (len(pubBytes) - 1) / 2
	jwk["x"] = base64.RawURLEncoding.EncodeToString(pubBytes[1 : 1+coordLen])
	jwk["y"] = base64.RawURLEncoding.EncodeToString(pubBytes[1+coordLen:])

	data, _ := json.Marshal(jwk)
	return string(data), nil
}
