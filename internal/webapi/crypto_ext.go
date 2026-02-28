package webapi

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	cryptosubtle "crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/cryguy/worker/v2/internal/core"
	"github.com/cryguy/worker/v2/internal/eventloop"
)

// cryptoExtJS patches crypto.subtle with JWK import/export, ECDSA, generateKey,
// and AES-CBC support. Must be evaluated AFTER the base cryptoJS.
const cryptoExtJS = `
(function() {
var subtle = crypto.subtle;
var CK = CryptoKey;

subtle.importKey = async function(format, keyData, algorithm, extractable, usages) {
	var algo = typeof algorithm === 'string' ? { name: algorithm } : algorithm;
	var hashName = algo.hash ? (typeof algo.hash === 'string' ? algo.hash : algo.hash.name) : '';
	var namedCurve = algo.namedCurve || '';
	if (format === 'raw') {
		var b64 = __bufferSourceToB64(keyData);
		var id = __cryptoImportKey(algo.name, hashName, b64, namedCurve, extractable);
		var keyType = (namedCurve && (algo.name === 'ECDSA' || algo.name === 'ECDH')) ? 'public' : 'secret';
		return new CK(id, algo, keyType, extractable, usages);
	} else if (format === 'jwk') {
		var jwkJSON = JSON.stringify(keyData);
		var resultJSON = __cryptoImportKeyJWK(algo.name, hashName, jwkJSON, namedCurve, extractable);
		var result = JSON.parse(resultJSON);
		if (result.error) throw new TypeError(result.error);
		return new CK(result.keyId, algo, result.keyType || 'secret', extractable, usages);
	}
	throw new TypeError('importKey: unsupported format "' + format + '"');
};

subtle.exportKey = async function(format, key) {
	if (!key.extractable) throw new DOMException('key is not extractable', 'InvalidAccessError');
	if (format === 'raw') {
		var b64 = __cryptoExportKey(key._id);
		return __b64ToBuffer(b64);
	} else if (format === 'jwk') {
		var algoName = key.algorithm.name || '';
		var hashName = key.algorithm.hash ? (typeof key.algorithm.hash === 'string' ? key.algorithm.hash : key.algorithm.hash.name) : '';
		var namedCurve = key.algorithm.namedCurve || '';
		var resultJSON = __cryptoExportKeyJWK(key._id, algoName, hashName, namedCurve);
		return JSON.parse(resultJSON);
	}
	throw new TypeError('exportKey: unsupported format "' + format + '"');
};

subtle.generateKey = async function(algorithm, extractable, usages) {
	var algo = typeof algorithm === 'string' ? { name: algorithm } : algorithm;
	var hashName = algo.hash ? (typeof algo.hash === 'string' ? algo.hash : algo.hash.name) : '';
	var namedCurve = algo.namedCurve || '';
	var keyLength = algo.length || 0;
	var resultJSON = __cryptoGenerateKey(algo.name, hashName, namedCurve, extractable, keyLength);
	var result = JSON.parse(resultJSON);
	if (result.error) throw new TypeError(result.error);
	if (result.privateKeyId !== undefined) {
		return {
			privateKey: new CK(result.privateKeyId, algo, 'private', extractable,
				usages.filter(function(u) { return u === 'sign'; })),
			publicKey: new CK(result.publicKeyId, algo, 'public', extractable,
				usages.filter(function(u) { return u === 'verify'; })),
		};
	}
	return new CK(result.keyId, algo, 'secret', extractable, usages);
};

subtle.sign = async function(algorithm, key, data) {
	if (key.usages && !key.usages.includes('sign')) {
		throw new TypeError('key usages do not permit this operation');
	}
	var algo = typeof algorithm === 'string' ? { name: algorithm } : algorithm;
	var dataB64 = __bufferSourceToB64(data);
	var hashName = algo.hash ? (typeof algo.hash === 'string' ? algo.hash : algo.hash.name) : '';
	var resultB64 = __cryptoSign(algo.name, key._id, dataB64, hashName);
	return __b64ToBuffer(resultB64);
};

subtle.verify = async function(algorithm, key, signature, data) {
	if (key.usages && !key.usages.includes('verify')) {
		throw new TypeError('key usages do not permit this operation');
	}
	var algo = typeof algorithm === 'string' ? { name: algorithm } : algorithm;
	var sigB64 = __bufferSourceToB64(signature);
	var dataB64 = __bufferSourceToB64(data);
	var hashName = algo.hash ? (typeof algo.hash === 'string' ? algo.hash : algo.hash.name) : '';
	return !!__cryptoVerify(algo.name, key._id, sigB64, dataB64, hashName);
};

subtle.wrapKey = async function(format, key, wrappingKey, wrapAlgorithm) {
	var exported = await subtle.exportKey(format, key);
	var data;
	if (format === 'raw') {
		data = exported;
	} else if (format === 'jwk') {
		data = new TextEncoder().encode(JSON.stringify(exported));
	} else {
		data = exported;
	}
	var wrapAlgo = typeof wrapAlgorithm === 'string' ? { name: wrapAlgorithm } : wrapAlgorithm;
	return subtle.encrypt(wrapAlgo, wrappingKey, data);
};

subtle.unwrapKey = async function(format, wrappedKey, unwrappingKey, unwrapAlgorithm, unwrappedKeyAlgorithm, extractable, keyUsages) {
	var unwrapAlgo = typeof unwrapAlgorithm === 'string' ? { name: unwrapAlgorithm } : unwrapAlgorithm;
	var decrypted = await subtle.decrypt(unwrapAlgo, unwrappingKey, wrappedKey);
	var keyData;
	if (format === 'jwk') {
		keyData = JSON.parse(new TextDecoder().decode(decrypted));
	} else {
		keyData = decrypted;
	}
	return subtle.importKey(format, keyData, unwrappedKeyAlgorithm, extractable, keyUsages);
};

})();
`

// CurveFromName returns the elliptic curve for the given name.
func CurveFromName(name string) elliptic.Curve {
	switch name {
	case "P-256":
		return elliptic.P256()
	case "P-384":
		return elliptic.P384()
	default:
		return nil
	}
}

// PadBytes left-pads b with zeroes to the given length.
func PadBytes(b []byte, length int) []byte {
	if len(b) >= length {
		return b
	}
	padded := make([]byte, length)
	copy(padded[length-len(b):], b)
	return padded
}

// SetupCryptoExt registers extended crypto Go functions and evaluates the JS
// patches for JWK, ECDSA, generateKey, and AES-CBC. Must run after SetupCrypto.
func SetupCryptoExt(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	// Override __cryptoImportKey to accept namedCurve, extractable, and handle ECDSA raw keys.
	if err := rt.RegisterFunc("__cryptoImportKey", func(algoName, hashAlgo, dataB64, namedCurve string, extractableVal bool) (int, error) {
		keyData, err := base64.StdEncoding.DecodeString(dataB64)
		if err != nil {
			return 0, fmt.Errorf("importKey: invalid base64")
		}

		reqID := GetReqIDFromJS(rt)

		if NormalizeAlgo(algoName) == "ECDSA" && namedCurve != "" {
			curve := CurveFromName(namedCurve)
			if curve == nil {
				return 0, fmt.Errorf("importKey: unsupported curve %q", namedCurve)
			}
			var ecdhCurve ecdh.Curve
			switch namedCurve {
			case "P-256":
				ecdhCurve = ecdh.P256()
			case "P-384":
				ecdhCurve = ecdh.P384()
			default:
				return 0, fmt.Errorf("importKey: unsupported curve %q", namedCurve)
			}
			ecdhKey, err := ecdhCurve.NewPublicKey(keyData)
			if err != nil {
				return 0, fmt.Errorf("importKey: invalid EC public key")
			}
			// Convert ecdh.PublicKey to ecdsa.PublicKey via raw bytes.
			rawBytes := ecdhKey.Bytes() // uncompressed: 0x04 || X || Y
			coordLen := (len(rawBytes) - 1) / 2
			x := new(big.Int).SetBytes(rawBytes[1 : 1+coordLen])
			y := new(big.Int).SetBytes(rawBytes[1+coordLen:])
			pubKey := &ecdsa.PublicKey{Curve: curve, X: x, Y: y}
			id := core.ImportCryptoKeyFull(reqID, &core.CryptoKeyEntry{
				AlgoName:    "ECDSA",
				HashAlgo:    hashAlgo,
				KeyType:     "public",
				NamedCurve:  namedCurve,
				EcKey:       pubKey,
				Extractable: extractableVal,
			})
			return id, nil
		}

		id := core.ImportCryptoKey(reqID, hashAlgo, keyData)
		if id < 0 {
			return 0, fmt.Errorf("importKey: no active request state")
		}
		return id, nil
	}); err != nil {
		return err
	}

	// Override __cryptoExportKey to handle ECDSA EC keys (which store key
	// material in EcKey, not Data).
	if err := rt.RegisterFunc("__cryptoExportKey", func(keyID int) (string, error) {
		reqID := GetReqIDFromJS(rt)
		entry := core.GetCryptoKey(reqID, keyID)
		if entry == nil {
			return "", fmt.Errorf("exportKey: key not found")
		}
		if !entry.Extractable {
			return "", fmt.Errorf("exportKey: key is not extractable")
		}
		// For ECDSA keys, serialize the EC public key as uncompressed point.
		if entry.EcKey != nil {
			switch pub := entry.EcKey.(type) {
			case *ecdsa.PublicKey:
				ecdhPub, err := pub.ECDH()
				if err != nil {
					return "", fmt.Errorf("exportKey: %s", err.Error())
				}
				return base64.StdEncoding.EncodeToString(ecdhPub.Bytes()), nil
			case *ecdsa.PrivateKey:
				ecdhPub, err := pub.PublicKey.ECDH()
				if err != nil {
					return "", fmt.Errorf("exportKey: %s", err.Error())
				}
				return base64.StdEncoding.EncodeToString(ecdhPub.Bytes()), nil
			}
		}
		return base64.StdEncoding.EncodeToString(entry.Data), nil
	}); err != nil {
		return err
	}

	// __cryptoImportKeyJWK(algoName, hashAlgo, jwkJSON, namedCurve, extractable) -> JSON result
	if err := rt.RegisterFunc("__cryptoImportKeyJWK", func(algoName, hashAlgo, jwkJSON, namedCurve string, extractableVal bool) (string, error) {
		reqID := GetReqIDFromJS(rt)
		if core.GetRequestState(reqID) == nil {
			return `{"error":"no active request state"}`, nil
		}

		var jwk map[string]interface{}
		if err := json.Unmarshal([]byte(jwkJSON), &jwk); err != nil {
			return `{"error":"invalid JWK JSON"}`, nil
		}

		kty, _ := jwk["kty"].(string)
		switch kty {
		case "oct":
			kB64URL, _ := jwk["k"].(string)
			keyData, err := base64.RawURLEncoding.DecodeString(kB64URL)
			if err != nil {
				return `{"error":"invalid JWK k value"}`, nil
			}
			entry := &core.CryptoKeyEntry{
				Data:        keyData,
				HashAlgo:    hashAlgo,
				AlgoName:    NormalizeAlgo(algoName),
				KeyType:     "secret",
				Extractable: extractableVal,
			}
			id := core.ImportCryptoKeyFull(reqID, entry)
			return fmt.Sprintf(`{"keyId":%d,"keyType":"secret"}`, id), nil

		case "EC":
			crv, _ := jwk["crv"].(string)
			if namedCurve == "" {
				namedCurve = crv
			}
			curve := CurveFromName(namedCurve)
			if curve == nil {
				return fmt.Sprintf(`{"error":"unsupported curve %q"}`, namedCurve), nil
			}
			xB64, _ := jwk["x"].(string)
			yB64, _ := jwk["y"].(string)
			xBytes, err := base64.RawURLEncoding.DecodeString(xB64)
			if err != nil {
				return `{"error":"invalid JWK x value"}`, nil
			}
			yBytes, err := base64.RawURLEncoding.DecodeString(yB64)
			if err != nil {
				return `{"error":"invalid JWK y value"}`, nil
			}
			x := new(big.Int).SetBytes(xBytes)
			y := new(big.Int).SetBytes(yBytes)
			pubKey := &ecdsa.PublicKey{Curve: curve, X: x, Y: y}

			dB64, hasD := jwk["d"].(string)
			if hasD && dB64 != "" {
				dBytes, err := base64.RawURLEncoding.DecodeString(dB64)
				if err != nil {
					return `{"error":"invalid JWK d value"}`, nil
				}
				privKey := &ecdsa.PrivateKey{
					PublicKey: *pubKey,
					D:         new(big.Int).SetBytes(dBytes),
				}
				id := core.ImportCryptoKeyFull(reqID, &core.CryptoKeyEntry{
					AlgoName: "ECDSA", HashAlgo: hashAlgo, KeyType: "private",
					NamedCurve: namedCurve, EcKey: privKey, Extractable: extractableVal,
				})
				return fmt.Sprintf(`{"keyId":%d,"keyType":"private"}`, id), nil
			}

			id := core.ImportCryptoKeyFull(reqID, &core.CryptoKeyEntry{
				AlgoName: "ECDSA", HashAlgo: hashAlgo, KeyType: "public",
				NamedCurve: namedCurve, EcKey: pubKey, Extractable: extractableVal,
			})
			return fmt.Sprintf(`{"keyId":%d,"keyType":"public"}`, id), nil

		default:
			return fmt.Sprintf(`{"error":"unsupported JWK kty %q"}`, kty), nil
		}
	}); err != nil {
		return err
	}

	// __cryptoExportKeyJWK(keyID, algoName, hashAlgo, namedCurve) -> JSON JWK
	if err := rt.RegisterFunc("__cryptoExportKeyJWK", func(keyID int, algoName, hashAlgo, namedCurve string) (string, error) {
		reqID := GetReqIDFromJS(rt)
		entry := core.GetCryptoKey(reqID, keyID)
		if entry == nil {
			return "", fmt.Errorf("exportKeyJWK: key not found")
		}
		if !entry.Extractable {
			return "", fmt.Errorf("exportKey: key is not extractable")
		}

		if entry.EcKey != nil {
			jwk := map[string]string{"kty": "EC"}
			curveName := entry.NamedCurve
			if curveName == "" {
				curveName = "P-256"
			}
			jwk["crv"] = curveName

			switch k := entry.EcKey.(type) {
			case *ecdsa.PrivateKey:
				byteLen := (k.Curve.Params().BitSize + 7) / 8
				jwk["x"] = base64.RawURLEncoding.EncodeToString(PadBytes(k.PublicKey.X.Bytes(), byteLen))
				jwk["y"] = base64.RawURLEncoding.EncodeToString(PadBytes(k.PublicKey.Y.Bytes(), byteLen))
				jwk["d"] = base64.RawURLEncoding.EncodeToString(PadBytes(k.D.Bytes(), byteLen))
			case *ecdsa.PublicKey:
				byteLen := (k.Curve.Params().BitSize + 7) / 8
				jwk["x"] = base64.RawURLEncoding.EncodeToString(PadBytes(k.X.Bytes(), byteLen))
				jwk["y"] = base64.RawURLEncoding.EncodeToString(PadBytes(k.Y.Bytes(), byteLen))
			}
			data, _ := json.Marshal(jwk)
			return string(data), nil
		}

		jwk := map[string]string{
			"kty": "oct",
			"k":   base64.RawURLEncoding.EncodeToString(entry.Data),
		}
		algo := NormalizeAlgo(algoName)
		h := NormalizeAlgo(hashAlgo)
		switch algo {
		case "HMAC":
			switch h {
			case "SHA-256":
				jwk["alg"] = "HS256"
			case "SHA-384":
				jwk["alg"] = "HS384"
			case "SHA-512":
				jwk["alg"] = "HS512"
			}
		case "AES-GCM":
			switch len(entry.Data) {
			case 16:
				jwk["alg"] = "A128GCM"
			case 32:
				jwk["alg"] = "A256GCM"
			}
		}
		data, _ := json.Marshal(jwk)
		return string(data), nil
	}); err != nil {
		return err
	}

	// __cryptoGenerateKey(algoName, hashAlgo, namedCurve, extractable, length) -> JSON result
	if err := rt.RegisterFunc("__cryptoGenerateKey", func(algoName, hashAlgo, namedCurve string, extractableVal bool, length int) (string, error) {
		reqID := GetReqIDFromJS(rt)
		if core.GetRequestState(reqID) == nil {
			return `{"error":"no active request state"}`, nil
		}

		switch NormalizeAlgo(algoName) {
		case "ECDSA":
			curve := CurveFromName(namedCurve)
			if curve == nil {
				return fmt.Sprintf(`{"error":"unsupported curve %q"}`, namedCurve), nil
			}
			privKey, err := ecdsa.GenerateKey(curve, rand.Reader)
			if err != nil {
				return fmt.Sprintf(`{"error":"key generation failed: %s"}`, err.Error()), nil
			}
			privID := core.ImportCryptoKeyFull(reqID, &core.CryptoKeyEntry{
				AlgoName: "ECDSA", HashAlgo: hashAlgo, KeyType: "private",
				NamedCurve: namedCurve, EcKey: privKey, Extractable: extractableVal,
			})
			pubID := core.ImportCryptoKeyFull(reqID, &core.CryptoKeyEntry{
				AlgoName: "ECDSA", HashAlgo: hashAlgo, KeyType: "public",
				NamedCurve: namedCurve, EcKey: &privKey.PublicKey, Extractable: extractableVal,
			})
			return fmt.Sprintf(`{"privateKeyId":%d,"publicKeyId":%d}`, privID, pubID), nil

		case "HMAC":
			keyLen := 32
			switch NormalizeAlgo(hashAlgo) {
			case "SHA-384":
				keyLen = 48
			case "SHA-512":
				keyLen = 64
			case "SHA-1":
				keyLen = 20
			}
			keyData := make([]byte, keyLen)
			if _, err := rand.Read(keyData); err != nil {
				return fmt.Sprintf(`{"error":"key generation failed: %s"}`, err.Error()), nil
			}
			id := core.ImportCryptoKeyFull(reqID, &core.CryptoKeyEntry{
				Data: keyData, HashAlgo: hashAlgo, AlgoName: "HMAC",
				KeyType: "secret", Extractable: extractableVal,
			})
			return fmt.Sprintf(`{"keyId":%d}`, id), nil

		case "AES-GCM", "AES-CBC", "AES-CTR":
			keyLen := 32 // default 256-bit
			if length == 128 {
				keyLen = 16
			} else if length == 192 {
				keyLen = 24
			} else if length != 0 && length != 256 {
				return `{"error":"AES: length must be 128, 192, or 256"}`, nil
			}
			keyData := make([]byte, keyLen)
			if _, err := rand.Read(keyData); err != nil {
				return fmt.Sprintf(`{"error":"key generation failed: %s"}`, err.Error()), nil
			}
			id := core.ImportCryptoKeyFull(reqID, &core.CryptoKeyEntry{
				Data: keyData, HashAlgo: hashAlgo, AlgoName: NormalizeAlgo(algoName),
				KeyType: "secret", Extractable: extractableVal,
			})
			return fmt.Sprintf(`{"keyId":%d}`, id), nil

		default:
			return fmt.Sprintf(`{"error":"generateKey: unsupported algorithm %q"}`, algoName), nil
		}
	}); err != nil {
		return err
	}

	// Override __cryptoSign to support ECDSA + extra hash arg.
	if err := rt.RegisterFunc("__cryptoSign", func(algo string, keyID int, dataB64, signHashAlgo string) (string, error) {
		data, err := base64.StdEncoding.DecodeString(dataB64)
		if err != nil {
			return "", fmt.Errorf("sign: invalid base64")
		}

		reqID := GetReqIDFromJS(rt)
		entry := core.GetCryptoKey(reqID, keyID)
		if entry == nil {
			return "", fmt.Errorf("sign: key not found")
		}

		switch NormalizeAlgo(algo) {
		case "HMAC":
			hashFn := HashFuncFromAlgo(entry.HashAlgo)
			if hashFn == nil {
				return "", fmt.Errorf("sign: unsupported HMAC hash %q", entry.HashAlgo)
			}
			mac := hmac.New(hashFn, entry.Data)
			mac.Write(data)
			sig := mac.Sum(nil)
			return base64.StdEncoding.EncodeToString(sig), nil

		case "ECDSA":
			privKey, ok := entry.EcKey.(*ecdsa.PrivateKey)
			if !ok {
				return "", fmt.Errorf("sign: key is not an ECDSA private key")
			}
			ha := signHashAlgo
			if ha == "" {
				ha = entry.HashAlgo
			}
			hashFn := HashFuncFromAlgo(ha)
			if hashFn == nil {
				return "", fmt.Errorf("sign: unsupported hash %q", ha)
			}
			h := hashFn()
			h.Write(data)
			digest := h.Sum(nil)

			r, s, err := ecdsa.Sign(rand.Reader, privKey, digest)
			if err != nil {
				return "", fmt.Errorf("sign: %s", err.Error())
			}
			byteLen := (privKey.Curve.Params().BitSize + 7) / 8
			sig := make([]byte, byteLen*2)
			copy(sig[:byteLen], PadBytes(r.Bytes(), byteLen))
			copy(sig[byteLen:], PadBytes(s.Bytes(), byteLen))
			return base64.StdEncoding.EncodeToString(sig), nil

		default:
			return "", fmt.Errorf("sign: unsupported algorithm %q", algo)
		}
	}); err != nil {
		return err
	}

	// Override __cryptoVerify to support ECDSA + extra hash arg.
	if err := rt.RegisterFunc("__cryptoVerify", func(algo string, keyID int, sigB64, dataB64, verifyHashAlgo string) (int, error) {
		sig, err := base64.StdEncoding.DecodeString(sigB64)
		if err != nil {
			return 0, fmt.Errorf("verify: invalid signature base64")
		}
		data, err := base64.StdEncoding.DecodeString(dataB64)
		if err != nil {
			return 0, fmt.Errorf("verify: invalid data base64")
		}

		reqID := GetReqIDFromJS(rt)
		entry := core.GetCryptoKey(reqID, keyID)
		if entry == nil {
			return 0, fmt.Errorf("verify: key not found")
		}

		switch NormalizeAlgo(algo) {
		case "HMAC":
			hashFn := HashFuncFromAlgo(entry.HashAlgo)
			if hashFn == nil {
				return 0, fmt.Errorf("verify: unsupported HMAC hash %q", entry.HashAlgo)
			}
			mac := hmac.New(hashFn, entry.Data)
			mac.Write(data)
			expected := mac.Sum(nil)
			return core.BoolToInt(hmac.Equal(sig, expected)), nil

		case "ECDSA":
			var pubKey *ecdsa.PublicKey
			switch k := entry.EcKey.(type) {
			case *ecdsa.PublicKey:
				pubKey = k
			case *ecdsa.PrivateKey:
				pubKey = &k.PublicKey
			default:
				return 0, fmt.Errorf("verify: key is not an ECDSA key")
			}
			ha := verifyHashAlgo
			if ha == "" {
				ha = entry.HashAlgo
			}
			hashFn := HashFuncFromAlgo(ha)
			if hashFn == nil {
				return 0, fmt.Errorf("verify: unsupported hash %q", ha)
			}
			h := hashFn()
			h.Write(data)
			digest := h.Sum(nil)

			byteLen := (pubKey.Curve.Params().BitSize + 7) / 8
			if len(sig) != byteLen*2 {
				return 0, nil
			}
			r := new(big.Int).SetBytes(sig[:byteLen])
			s := new(big.Int).SetBytes(sig[byteLen:])
			return core.BoolToInt(ecdsa.Verify(pubKey, digest, r, s)), nil

		default:
			return 0, fmt.Errorf("verify: unsupported algorithm %q", algo)
		}
	}); err != nil {
		return err
	}

	// Override __cryptoEncrypt to add AES-CBC.
	if err := rt.RegisterFunc("__cryptoEncrypt", func(algo string, keyID int, dataB64, ivB64, aadB64 string) (string, error) {
		data, err := base64.StdEncoding.DecodeString(dataB64)
		if err != nil {
			return "", fmt.Errorf("encrypt: invalid base64 data")
		}
		reqID := GetReqIDFromJS(rt)
		entry := core.GetCryptoKey(reqID, keyID)
		if entry == nil {
			return "", fmt.Errorf("encrypt: key not found")
		}

		switch NormalizeAlgo(algo) {
		case "AES-GCM":
			iv, err := base64.StdEncoding.DecodeString(ivB64)
			if err != nil {
				return "", fmt.Errorf("encrypt: invalid IV base64")
			}
			if len(iv) != 12 {
				return "", fmt.Errorf("encrypt: AES-GCM IV must be exactly 12 bytes, got %d", len(iv))
			}
			var aad []byte
			if aadB64 != "" {
				aad, err = base64.StdEncoding.DecodeString(aadB64)
				if err != nil {
					return "", fmt.Errorf("encrypt: invalid AAD base64")
				}
			}
			block, err := aes.NewCipher(entry.Data)
			if err != nil {
				return "", fmt.Errorf("encrypt: %s", err.Error())
			}
			gcm, err := cipher.NewGCM(block)
			if err != nil {
				return "", fmt.Errorf("encrypt: %s", err.Error())
			}
			ct := gcm.Seal(nil, iv, data, aad)
			return base64.StdEncoding.EncodeToString(ct), nil

		case "AES-CBC":
			iv, err := base64.StdEncoding.DecodeString(ivB64)
			if err != nil {
				return "", fmt.Errorf("encrypt: invalid IV base64")
			}
			if len(iv) != aes.BlockSize {
				return "", fmt.Errorf("encrypt: AES-CBC IV must be exactly %d bytes", aes.BlockSize)
			}
			block, err := aes.NewCipher(entry.Data)
			if err != nil {
				return "", fmt.Errorf("encrypt: %s", err.Error())
			}
			padLen := aes.BlockSize - (len(data) % aes.BlockSize)
			padded := make([]byte, len(data)+padLen)
			copy(padded, data)
			for i := len(data); i < len(padded); i++ {
				padded[i] = byte(padLen)
			}
			mode := cipher.NewCBCEncrypter(block, iv)
			ct := make([]byte, len(padded))
			mode.CryptBlocks(ct, padded)
			return base64.StdEncoding.EncodeToString(ct), nil

		default:
			return "", fmt.Errorf("encrypt: unsupported algorithm %q", algo)
		}
	}); err != nil {
		return err
	}

	// Override __cryptoDecrypt to add AES-CBC.
	if err := rt.RegisterFunc("__cryptoDecrypt", func(algo string, keyID int, dataB64, ivB64, aadB64 string) (string, error) {
		data, err := base64.StdEncoding.DecodeString(dataB64)
		if err != nil {
			return "", fmt.Errorf("decrypt: invalid base64 data")
		}
		reqID := GetReqIDFromJS(rt)
		entry := core.GetCryptoKey(reqID, keyID)
		if entry == nil {
			return "", fmt.Errorf("decrypt: key not found")
		}

		switch NormalizeAlgo(algo) {
		case "AES-GCM":
			iv, err := base64.StdEncoding.DecodeString(ivB64)
			if err != nil {
				return "", fmt.Errorf("decrypt: invalid IV base64")
			}
			if len(iv) != 12 {
				return "", fmt.Errorf("decrypt: AES-GCM IV must be exactly 12 bytes, got %d", len(iv))
			}
			var aad []byte
			if aadB64 != "" {
				aad, err = base64.StdEncoding.DecodeString(aadB64)
				if err != nil {
					return "", fmt.Errorf("decrypt: invalid AAD base64")
				}
			}
			block, err := aes.NewCipher(entry.Data)
			if err != nil {
				return "", fmt.Errorf("decrypt: %s", err.Error())
			}
			gcm, err := cipher.NewGCM(block)
			if err != nil {
				return "", fmt.Errorf("decrypt: %s", err.Error())
			}
			pt, err := gcm.Open(nil, iv, data, aad)
			if err != nil {
				return "", fmt.Errorf("decrypt: %s", err.Error())
			}
			return base64.StdEncoding.EncodeToString(pt), nil

		case "AES-CBC":
			iv, err := base64.StdEncoding.DecodeString(ivB64)
			if err != nil {
				return "", fmt.Errorf("decrypt: invalid IV base64")
			}
			if len(iv) != aes.BlockSize {
				return "", fmt.Errorf("decrypt: AES-CBC IV must be exactly %d bytes", aes.BlockSize)
			}
			if len(data)%aes.BlockSize != 0 {
				return "", fmt.Errorf("decrypt: ciphertext not a multiple of block size")
			}
			block, err := aes.NewCipher(entry.Data)
			if err != nil {
				return "", fmt.Errorf("decrypt: %s", err.Error())
			}
			mode := cipher.NewCBCDecrypter(block, iv)
			pt := make([]byte, len(data))
			mode.CryptBlocks(pt, data)
			if len(pt) == 0 {
				return base64.StdEncoding.EncodeToString(pt), nil
			}
			// Constant-time PKCS7 padding validation
			padLen := int(pt[len(pt)-1])
			good := 1
			// Check padLen is in range [1, aes.BlockSize]
			if padLen < 1 || padLen > aes.BlockSize {
				good = 0
			}
			// Check all padding bytes in constant time
			for i := 0; i < aes.BlockSize; i++ {
				if i < padLen && good == 1 {
					if cryptosubtle.ConstantTimeByteEq(pt[len(pt)-1-i], byte(padLen)) != 1 {
						good = 0
					}
				}
			}
			if good != 1 {
				return "", fmt.Errorf("decrypt: invalid PKCS7 padding")
			}
			pt = pt[:len(pt)-padLen]
			return base64.StdEncoding.EncodeToString(pt), nil

		default:
			return "", fmt.Errorf("decrypt: unsupported algorithm %q", algo)
		}
	}); err != nil {
		return err
	}

	// Evaluate the JS patches.
	if err := rt.Eval(cryptoExtJS); err != nil {
		return fmt.Errorf("evaluating crypto_ext.js: %w", err)
	}

	return nil
}
