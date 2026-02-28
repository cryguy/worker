package webapi

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/cryguy/worker/v2/internal/core"
	"github.com/cryguy/worker/v2/internal/eventloop"
)

// CryptoHashFromAlgo returns the crypto.Hash for the given algorithm name.
func CryptoHashFromAlgo(algo string) crypto.Hash {
	switch NormalizeAlgo(algo) {
	case "SHA-1":
		return crypto.SHA1
	case "SHA-256":
		return crypto.SHA256
	case "SHA-384":
		return crypto.SHA384
	case "SHA-512":
		return crypto.SHA512
	default:
		return 0
	}
}

// RsaJWKAlg returns the JWK "alg" value for an RSA algorithm+hash combination.
func RsaJWKAlg(algoName, hashAlgo string) string {
	h := NormalizeAlgo(hashAlgo)
	switch NormalizeAlgo(algoName) {
	case "RSASSA-PKCS1-v1_5":
		switch h {
		case "SHA-1":
			return "RS1"
		case "SHA-256":
			return "RS256"
		case "SHA-384":
			return "RS384"
		case "SHA-512":
			return "RS512"
		}
	case "RSA-PSS":
		switch h {
		case "SHA-256":
			return "PS256"
		case "SHA-384":
			return "PS384"
		case "SHA-512":
			return "PS512"
		}
	case "RSA-OAEP":
		switch h {
		case "SHA-1":
			return "RSA-OAEP"
		case "SHA-256":
			return "RSA-OAEP-256"
		case "SHA-384":
			return "RSA-OAEP-384"
		case "SHA-512":
			return "RSA-OAEP-512"
		}
	}
	return ""
}

// cryptoRSAJS patches crypto.subtle with RSA support using chain-of-responsibility.
const cryptoRSAJS = `
(function() {
var subtle = crypto.subtle;
var CK = CryptoKey;
var _prevSign = subtle.sign;
var _prevVerify = subtle.verify;
var _prevEncrypt = subtle.encrypt;
var _prevDecrypt = subtle.decrypt;
var _prevImportKey = subtle.importKey;
var _prevExportKey = subtle.exportKey;
var _prevGenerateKey = subtle.generateKey;

function isRSA(name) {
	return name === 'RSASSA-PKCS1-v1_5' || name === 'RSA-PSS' || name === 'RSA-OAEP';
}

subtle.sign = async function(algorithm, key, data) {
	if (key.usages && !key.usages.includes('sign')) {
		throw new TypeError('key usages do not permit this operation');
	}
	var algo = typeof algorithm === 'string' ? { name: algorithm } : algorithm;
	if (algo.name === 'RSASSA-PKCS1-v1_5' || algo.name === 'RSA-PSS') {
		var hashName = key.algorithm.hash ? (typeof key.algorithm.hash === 'string' ? key.algorithm.hash : key.algorithm.hash.name) : '';
		var saltLength = algo.saltLength || 0;
		var resultB64 = __cryptoSignRSA(algo.name, key._id, __bufferSourceToB64(data), hashName, saltLength);
		return __b64ToBuffer(resultB64);
	}
	return _prevSign.call(this, algorithm, key, data);
};

subtle.verify = async function(algorithm, key, signature, data) {
	if (key.usages && !key.usages.includes('verify')) {
		throw new TypeError('key usages do not permit this operation');
	}
	var algo = typeof algorithm === 'string' ? { name: algorithm } : algorithm;
	if (algo.name === 'RSASSA-PKCS1-v1_5' || algo.name === 'RSA-PSS') {
		var hashName = key.algorithm.hash ? (typeof key.algorithm.hash === 'string' ? key.algorithm.hash : key.algorithm.hash.name) : '';
		var saltLength = algo.saltLength || 0;
		return !!__cryptoVerifyRSA(algo.name, key._id, __bufferSourceToB64(signature), __bufferSourceToB64(data), hashName, saltLength);
	}
	return _prevVerify.call(this, algorithm, key, signature, data);
};

subtle.encrypt = async function(algorithm, key, data) {
	if (key.usages && !key.usages.includes('encrypt')) {
		throw new TypeError('key usages do not permit this operation');
	}
	var algo = typeof algorithm === 'string' ? { name: algorithm } : algorithm;
	if (algo.name === 'RSA-OAEP') {
		var labelB64 = algo.label ? __bufferSourceToB64(algo.label) : '';
		var resultB64 = __cryptoEncryptRSA(key._id, __bufferSourceToB64(data), labelB64);
		return __b64ToBuffer(resultB64);
	}
	return _prevEncrypt.call(this, algorithm, key, data);
};

subtle.decrypt = async function(algorithm, key, data) {
	if (key.usages && !key.usages.includes('decrypt')) {
		throw new TypeError('key usages do not permit this operation');
	}
	var algo = typeof algorithm === 'string' ? { name: algorithm } : algorithm;
	if (algo.name === 'RSA-OAEP') {
		var labelB64 = algo.label ? __bufferSourceToB64(algo.label) : '';
		var resultB64 = __cryptoDecryptRSA(key._id, __bufferSourceToB64(data), labelB64);
		return __b64ToBuffer(resultB64);
	}
	return _prevDecrypt.call(this, algorithm, key, data);
};

subtle.importKey = async function(format, keyData, algorithm, extractable, usages) {
	var algo = typeof algorithm === 'string' ? { name: algorithm } : algorithm;
	if (isRSA(algo.name)) {
		var hashName = algo.hash ? (typeof algo.hash === 'string' ? algo.hash : algo.hash.name) : '';
		var dataStr;
		if (format === 'jwk') {
			dataStr = JSON.stringify(keyData);
		} else {
			dataStr = __bufferSourceToB64(keyData);
		}
		var resultJSON = __cryptoImportKeyRSA(format, dataStr, algo.name, hashName, extractable);
		var result = JSON.parse(resultJSON);
		if (result.error) throw new TypeError(result.error);
		var keyAlgo = { name: algo.name, hash: { name: hashName } };
		if (result.modulusLength) keyAlgo.modulusLength = result.modulusLength;
		if (result.publicExponent) {
			var peBytes = [];
			var pe = result.publicExponent;
			if (pe > 0) {
				while (pe > 0) { peBytes.unshift(pe & 0xFF); pe = pe >>> 8; }
			}
			keyAlgo.publicExponent = new Uint8Array(peBytes);
		}
		return new CK(result.keyId, keyAlgo, result.keyType, extractable, usages);
	}
	return _prevImportKey.call(this, format, keyData, algorithm, extractable, usages);
};

subtle.exportKey = async function(format, key) {
	if (isRSA(key.algorithm.name)) {
		if (!key.extractable) throw new DOMException('key is not extractable', 'InvalidAccessError');
		var hashName = key.algorithm.hash ? (typeof key.algorithm.hash === 'string' ? key.algorithm.hash : key.algorithm.hash.name) : '';
		var resultStr = __cryptoExportKeyRSA(key._id, format, key.algorithm.name, hashName);
		if (format === 'jwk') {
			return JSON.parse(resultStr);
		}
		return __b64ToBuffer(resultStr);
	}
	return _prevExportKey.call(this, format, key);
};

subtle.generateKey = async function(algorithm, extractable, usages) {
	var algo = typeof algorithm === 'string' ? { name: algorithm } : algorithm;
	if (isRSA(algo.name)) {
		var hashName = algo.hash ? (typeof algo.hash === 'string' ? algo.hash : algo.hash.name) : '';
		var modulusLength = algo.modulusLength || 2048;
		var pubExp = 65537;
		if (algo.publicExponent) {
			var pe = algo.publicExponent;
			pubExp = 0;
			for (var i = 0; i < pe.length; i++) {
				pubExp = (pubExp << 8) | pe[i];
			}
		}
		var resultJSON = __cryptoGenerateKeyRSA(algo.name, modulusLength, hashName, pubExp, extractable);
		var result = JSON.parse(resultJSON);
		if (result.error) throw new TypeError(result.error);
		var keyAlgo = { name: algo.name, hash: { name: hashName }, modulusLength: modulusLength };
		if (algo.publicExponent) keyAlgo.publicExponent = algo.publicExponent;
		else keyAlgo.publicExponent = new Uint8Array([1, 0, 1]);
		return {
			privateKey: new CK(result.privateKeyId, keyAlgo, 'private', extractable,
				usages.filter(function(u) { return u === 'sign' || u === 'decrypt'; })),
			publicKey: new CK(result.publicKeyId, keyAlgo, 'public', extractable,
				usages.filter(function(u) { return u === 'verify' || u === 'encrypt'; })),
		};
	}
	return _prevGenerateKey.call(this, algorithm, extractable, usages);
};

})();
`

// SetupCryptoRSA registers RSA Go functions and evaluates the JS patches.
// Must run after SetupCryptoExt.
func SetupCryptoRSA(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	// __cryptoSignRSA(algoName, keyID, dataB64, hashAlgo, saltLength) -> sigB64
	if err := rt.RegisterFunc("__cryptoSignRSA", func(algoName string, keyID int, dataB64, hashAlgo string, saltLength int) (string, error) {
		data, err := base64.StdEncoding.DecodeString(dataB64)
		if err != nil {
			return "", fmt.Errorf("signRSA: invalid base64")
		}

		reqID := GetReqIDFromJS(rt)
		entry := core.GetCryptoKey(reqID, keyID)
		if entry == nil {
			return "", fmt.Errorf("signRSA: key not found")
		}

		privKey, ok := entry.EcKey.(*rsa.PrivateKey)
		if !ok {
			return "", fmt.Errorf("signRSA: key is not an RSA private key")
		}

		ch := CryptoHashFromAlgo(hashAlgo)
		if ch == 0 {
			return "", fmt.Errorf("signRSA: unsupported hash %q", hashAlgo)
		}
		hashFn := HashFuncFromAlgo(hashAlgo)
		h := hashFn()
		h.Write(data)
		digest := h.Sum(nil)

		var sig []byte
		switch NormalizeAlgo(algoName) {
		case "RSASSA-PKCS1-v1_5":
			sig, err = rsa.SignPKCS1v15(rand.Reader, privKey, ch, digest)
		case "RSA-PSS":
			opts := &rsa.PSSOptions{SaltLength: saltLength}
			if saltLength == 0 {
				opts.SaltLength = rsa.PSSSaltLengthEqualsHash
			}
			sig, err = rsa.SignPSS(rand.Reader, privKey, ch, digest, opts)
		default:
			return "", fmt.Errorf("signRSA: unsupported algorithm %q", algoName)
		}

		if err != nil {
			return "", fmt.Errorf("signRSA: %s", err.Error())
		}
		return base64.StdEncoding.EncodeToString(sig), nil
	}); err != nil {
		return err
	}

	// __cryptoVerifyRSA(algoName, keyID, sigB64, dataB64, hashAlgo, saltLength) -> bool
	if err := rt.RegisterFunc("__cryptoVerifyRSA", func(algoName string, keyID int, sigB64, dataB64, hashAlgo string, saltLength int) (int, error) {
		sig, err := base64.StdEncoding.DecodeString(sigB64)
		if err != nil {
			return 0, fmt.Errorf("verifyRSA: invalid signature base64")
		}
		data, err := base64.StdEncoding.DecodeString(dataB64)
		if err != nil {
			return 0, fmt.Errorf("verifyRSA: invalid data base64")
		}

		reqID := GetReqIDFromJS(rt)
		entry := core.GetCryptoKey(reqID, keyID)
		if entry == nil {
			return 0, fmt.Errorf("verifyRSA: key not found")
		}

		var pubKey *rsa.PublicKey
		switch k := entry.EcKey.(type) {
		case *rsa.PublicKey:
			pubKey = k
		case *rsa.PrivateKey:
			pubKey = &k.PublicKey
		default:
			return 0, fmt.Errorf("verifyRSA: key is not an RSA key")
		}

		ch := CryptoHashFromAlgo(hashAlgo)
		if ch == 0 {
			return 0, fmt.Errorf("verifyRSA: unsupported hash %q", hashAlgo)
		}
		hashFn := HashFuncFromAlgo(hashAlgo)
		h := hashFn()
		h.Write(data)
		digest := h.Sum(nil)

		switch NormalizeAlgo(algoName) {
		case "RSASSA-PKCS1-v1_5":
			err = rsa.VerifyPKCS1v15(pubKey, ch, digest, sig)
		case "RSA-PSS":
			opts := &rsa.PSSOptions{SaltLength: saltLength}
			if saltLength == 0 {
				opts.SaltLength = rsa.PSSSaltLengthEqualsHash
			}
			err = rsa.VerifyPSS(pubKey, ch, digest, sig, opts)
		default:
			return 0, fmt.Errorf("verifyRSA: unsupported algorithm %q", algoName)
		}

		return core.BoolToInt(err == nil), nil
	}); err != nil {
		return err
	}

	// __cryptoEncryptRSA(keyID, dataB64, labelB64) -> ctB64
	if err := rt.RegisterFunc("__cryptoEncryptRSA", func(keyID int, dataB64, labelB64 string) (string, error) {
		data, err := base64.StdEncoding.DecodeString(dataB64)
		if err != nil {
			return "", fmt.Errorf("encryptRSA: invalid base64")
		}

		var label []byte
		if labelB64 != "" {
			label, err = base64.StdEncoding.DecodeString(labelB64)
			if err != nil {
				return "", fmt.Errorf("encryptRSA: invalid label base64")
			}
		}

		reqID := GetReqIDFromJS(rt)
		entry := core.GetCryptoKey(reqID, keyID)
		if entry == nil {
			return "", fmt.Errorf("encryptRSA: key not found")
		}

		var pubKey *rsa.PublicKey
		switch k := entry.EcKey.(type) {
		case *rsa.PublicKey:
			pubKey = k
		case *rsa.PrivateKey:
			pubKey = &k.PublicKey
		default:
			return "", fmt.Errorf("encryptRSA: key is not an RSA key")
		}

		hashAlgo := entry.HashAlgo
		if hashAlgo == "" {
			hashAlgo = "SHA-256"
		}
		hashFn := HashFuncFromAlgo(hashAlgo)
		if hashFn == nil {
			return "", fmt.Errorf("encryptRSA: unsupported hash %q", hashAlgo)
		}

		ct, err := rsa.EncryptOAEP(hashFn(), rand.Reader, pubKey, data, label)
		if err != nil {
			return "", fmt.Errorf("encryptRSA: %s", err.Error())
		}
		return base64.StdEncoding.EncodeToString(ct), nil
	}); err != nil {
		return err
	}

	// __cryptoDecryptRSA(keyID, ctB64, labelB64) -> ptB64
	if err := rt.RegisterFunc("__cryptoDecryptRSA", func(keyID int, ctB64, labelB64 string) (string, error) {
		ct, err := base64.StdEncoding.DecodeString(ctB64)
		if err != nil {
			return "", fmt.Errorf("decryptRSA: invalid base64")
		}

		var label []byte
		if labelB64 != "" {
			label, err = base64.StdEncoding.DecodeString(labelB64)
			if err != nil {
				return "", fmt.Errorf("decryptRSA: invalid label base64")
			}
		}

		reqID := GetReqIDFromJS(rt)
		entry := core.GetCryptoKey(reqID, keyID)
		if entry == nil {
			return "", fmt.Errorf("decryptRSA: key not found")
		}

		privKey, ok := entry.EcKey.(*rsa.PrivateKey)
		if !ok {
			return "", fmt.Errorf("decryptRSA: key is not an RSA private key")
		}

		hashAlgo := entry.HashAlgo
		if hashAlgo == "" {
			hashAlgo = "SHA-256"
		}
		hashFn := HashFuncFromAlgo(hashAlgo)
		if hashFn == nil {
			return "", fmt.Errorf("decryptRSA: unsupported hash %q", hashAlgo)
		}

		pt, err := rsa.DecryptOAEP(hashFn(), rand.Reader, privKey, ct, label)
		if err != nil {
			return "", fmt.Errorf("decryptRSA: %s", err.Error())
		}
		return base64.StdEncoding.EncodeToString(pt), nil
	}); err != nil {
		return err
	}

	// __cryptoGenerateKeyRSA(algoName, modulusLength, hashAlgo, publicExponent, extractable) -> JSON
	if err := rt.RegisterFunc("__cryptoGenerateKeyRSA", func(algoName string, modulusLength int, hashAlgo string, pubExp int, extractableVal bool) (string, error) {
		reqID := GetReqIDFromJS(rt)
		if core.GetRequestState(reqID) == nil {
			return `{"error":"no active request state"}`, nil
		}

		if pubExp == 0 {
			pubExp = 65537
		}

		// Reject non-standard exponents (security fix)
		if pubExp != 65537 {
			return `{"error":"only publicExponent 65537 is supported"}`, nil
		}

		if modulusLength != 2048 && modulusLength != 3072 && modulusLength != 4096 {
			return `{"error":"modulusLength must be 2048, 3072, or 4096"}`, nil
		}

		privKey, err := rsa.GenerateKey(rand.Reader, modulusLength)
		if err != nil {
			return fmt.Sprintf(`{"error":"key generation failed: %s"}`, err.Error()), nil
		}

		privID := core.ImportCryptoKeyFull(reqID, &core.CryptoKeyEntry{
			AlgoName: NormalizeAlgo(algoName), HashAlgo: hashAlgo,
			KeyType: "private", EcKey: privKey, Extractable: extractableVal,
		})
		pubID := core.ImportCryptoKeyFull(reqID, &core.CryptoKeyEntry{
			AlgoName: NormalizeAlgo(algoName), HashAlgo: hashAlgo,
			KeyType: "public", EcKey: &privKey.PublicKey, Extractable: extractableVal,
		})

		return fmt.Sprintf(`{"privateKeyId":%d,"publicKeyId":%d}`, privID, pubID), nil
	}); err != nil {
		return err
	}

	// __cryptoImportKeyRSA(format, dataStr, algoName, hashAlgo, extractable) -> JSON
	if err := rt.RegisterFunc("__cryptoImportKeyRSA", func(format, dataStr, algoName, hashAlgo string, extractableVal bool) (string, error) {
		reqID := GetReqIDFromJS(rt)
		if core.GetRequestState(reqID) == nil {
			return `{"error":"no active request state"}`, nil
		}

		switch format {
		case "jwk":
			return importRSAJWK(reqID, dataStr, algoName, hashAlgo, extractableVal)
		case "spki":
			return importRSASPKI(reqID, dataStr, algoName, hashAlgo, extractableVal)
		case "pkcs8":
			return importRSAPKCS8(reqID, dataStr, algoName, hashAlgo, extractableVal)
		default:
			return fmt.Sprintf(`{"error":"unsupported format %q for RSA"}`, format), nil
		}
	}); err != nil {
		return err
	}

	// __cryptoExportKeyRSA(keyID, format, algoName, hashAlgo) -> base64 or JSON string
	if err := rt.RegisterFunc("__cryptoExportKeyRSA", func(keyID int, format, algoName, hashAlgo string) (string, error) {
		reqID := GetReqIDFromJS(rt)
		entry := core.GetCryptoKey(reqID, keyID)
		if entry == nil {
			return "", fmt.Errorf("exportKeyRSA: key not found")
		}
		if !entry.Extractable {
			return "", fmt.Errorf("exportKey: key is not extractable")
		}

		switch format {
		case "jwk":
			return exportRSAJWK(entry, algoName, hashAlgo)
		case "spki":
			return exportRSASPKI(entry)
		case "pkcs8":
			return exportRSAPKCS8(entry)
		default:
			return "", fmt.Errorf("exportKeyRSA: unsupported format %q", format)
		}
	}); err != nil {
		return err
	}

	if err := rt.Eval(cryptoRSAJS); err != nil {
		return fmt.Errorf("evaluating crypto_rsa.js: %w", err)
	}
	return nil
}

// importRSAJWK imports an RSA key from JWK format.
func importRSAJWK(reqID uint64, jwkJSON, algoName, hashAlgo string, extractable bool) (string, error) {
	var jwk map[string]interface{}
	if err := json.Unmarshal([]byte(jwkJSON), &jwk); err != nil {
		return `{"error":"invalid JWK JSON"}`, nil
	}

	kty, _ := jwk["kty"].(string)
	if kty != "RSA" {
		return `{"error":"JWK kty must be RSA"}`, nil
	}

	// Decode n and e (required for both public and private keys)
	nB64, _ := jwk["n"].(string)
	eB64, _ := jwk["e"].(string)
	if nB64 == "" || eB64 == "" {
		return `{"error":"JWK missing required n or e field"}`, nil
	}
	nBytes, err := base64.RawURLEncoding.DecodeString(nB64)
	if err != nil {
		return `{"error":"invalid JWK n value"}`, nil
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(eB64)
	if err != nil {
		return `{"error":"invalid JWK e value"}`, nil
	}

	n := new(big.Int).SetBytes(nBytes)
	e := int(new(big.Int).SetBytes(eBytes).Int64())

	pubKey := &rsa.PublicKey{N: n, E: e}

	// Check if private key fields are present
	dB64, hasD := jwk["d"].(string)
	if hasD && dB64 != "" {
		dBytes, err := base64.RawURLEncoding.DecodeString(dB64)
		if err != nil {
			return `{"error":"invalid JWK d value"}`, nil
		}
		privKey := &rsa.PrivateKey{
			PublicKey: *pubKey,
			D:         new(big.Int).SetBytes(dBytes),
		}

		// Optional CRT values
		if pB64, ok := jwk["p"].(string); ok {
			pBytes, _ := base64.RawURLEncoding.DecodeString(pB64)
			qB64, _ := jwk["q"].(string)
			qBytes, _ := base64.RawURLEncoding.DecodeString(qB64)
			if len(pBytes) > 0 && len(qBytes) > 0 {
				privKey.Primes = []*big.Int{
					new(big.Int).SetBytes(pBytes),
					new(big.Int).SetBytes(qBytes),
				}
			}
		}

		privKey.Precompute()

		id := core.ImportCryptoKeyFull(reqID, &core.CryptoKeyEntry{
			AlgoName: NormalizeAlgo(algoName), HashAlgo: hashAlgo,
			KeyType: "private", EcKey: privKey, Extractable: extractable,
		})
		return fmt.Sprintf(
			`{"keyId":%d,"keyType":"private","modulusLength":%d,"publicExponent":%d}`,
			id, pubKey.N.BitLen(), e), nil
	}

	id := core.ImportCryptoKeyFull(reqID, &core.CryptoKeyEntry{
		AlgoName: NormalizeAlgo(algoName), HashAlgo: hashAlgo,
		KeyType: "public", EcKey: pubKey, Extractable: extractable,
	})
	return fmt.Sprintf(
		`{"keyId":%d,"keyType":"public","modulusLength":%d,"publicExponent":%d}`,
		id, pubKey.N.BitLen(), e), nil
}

// importRSASPKI imports an RSA public key from SPKI (DER) format.
func importRSASPKI(reqID uint64, dataB64, algoName, hashAlgo string, extractable bool) (string, error) {
	derBytes, err := base64.StdEncoding.DecodeString(dataB64)
	if err != nil {
		return `{"error":"invalid base64"}`, nil
	}

	pub, err := x509.ParsePKIXPublicKey(derBytes)
	if err != nil {
		return fmt.Sprintf(`{"error":"invalid SPKI: %s"}`, err.Error()), nil
	}

	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return `{"error":"SPKI key is not RSA"}`, nil
	}

	id := core.ImportCryptoKeyFull(reqID, &core.CryptoKeyEntry{
		AlgoName: NormalizeAlgo(algoName), HashAlgo: hashAlgo,
		KeyType: "public", EcKey: rsaPub, Extractable: extractable,
	})
	return fmt.Sprintf(
		`{"keyId":%d,"keyType":"public","modulusLength":%d,"publicExponent":%d}`,
		id, rsaPub.N.BitLen(), rsaPub.E), nil
}

// importRSAPKCS8 imports an RSA private key from PKCS#8 (DER) format.
func importRSAPKCS8(reqID uint64, dataB64, algoName, hashAlgo string, extractable bool) (string, error) {
	derBytes, err := base64.StdEncoding.DecodeString(dataB64)
	if err != nil {
		return `{"error":"invalid base64"}`, nil
	}

	key, err := x509.ParsePKCS8PrivateKey(derBytes)
	if err != nil {
		return fmt.Sprintf(`{"error":"invalid PKCS8: %s"}`, err.Error()), nil
	}

	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return `{"error":"PKCS8 key is not RSA"}`, nil
	}

	id := core.ImportCryptoKeyFull(reqID, &core.CryptoKeyEntry{
		AlgoName: NormalizeAlgo(algoName), HashAlgo: hashAlgo,
		KeyType: "private", EcKey: rsaKey, Extractable: extractable,
	})
	return fmt.Sprintf(
		`{"keyId":%d,"keyType":"private","modulusLength":%d,"publicExponent":%d}`,
		id, rsaKey.PublicKey.N.BitLen(), rsaKey.PublicKey.E), nil
}

// exportRSAJWK exports an RSA key to JWK format.
func exportRSAJWK(entry *core.CryptoKeyEntry, algoName, hashAlgo string) (string, error) {
	jwk := map[string]interface{}{"kty": "RSA"}

	alg := RsaJWKAlg(algoName, hashAlgo)
	if alg != "" {
		jwk["alg"] = alg
	}

	switch k := entry.EcKey.(type) {
	case *rsa.PublicKey:
		jwk["n"] = base64.RawURLEncoding.EncodeToString(k.N.Bytes())
		jwk["e"] = base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes())
		jwk["key_ops"] = []string{"verify", "encrypt"}
	case *rsa.PrivateKey:
		jwk["n"] = base64.RawURLEncoding.EncodeToString(k.PublicKey.N.Bytes())
		jwk["e"] = base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.PublicKey.E)).Bytes())
		jwk["d"] = base64.RawURLEncoding.EncodeToString(k.D.Bytes())
		if len(k.Primes) >= 2 {
			jwk["p"] = base64.RawURLEncoding.EncodeToString(k.Primes[0].Bytes())
			jwk["q"] = base64.RawURLEncoding.EncodeToString(k.Primes[1].Bytes())
			if k.Precomputed.Dp != nil {
				jwk["dp"] = base64.RawURLEncoding.EncodeToString(k.Precomputed.Dp.Bytes())
				jwk["dq"] = base64.RawURLEncoding.EncodeToString(k.Precomputed.Dq.Bytes())
				jwk["qi"] = base64.RawURLEncoding.EncodeToString(k.Precomputed.Qinv.Bytes())
			}
		}
		jwk["key_ops"] = []string{"sign", "decrypt"}
	default:
		return "", fmt.Errorf("exportKeyRSA: not an RSA key")
	}

	data, _ := json.Marshal(jwk)
	return string(data), nil
}

// exportRSASPKI exports an RSA public key to SPKI (DER) format.
func exportRSASPKI(entry *core.CryptoKeyEntry) (string, error) {
	var pubKey *rsa.PublicKey
	switch k := entry.EcKey.(type) {
	case *rsa.PublicKey:
		pubKey = k
	case *rsa.PrivateKey:
		pubKey = &k.PublicKey
	default:
		return "", fmt.Errorf("exportKeyRSA: not an RSA key")
	}

	derBytes, err := x509.MarshalPKIXPublicKey(pubKey)
	if err != nil {
		return "", fmt.Errorf("exportKeyRSA: %s", err.Error())
	}
	return base64.StdEncoding.EncodeToString(derBytes), nil
}

// exportRSAPKCS8 exports an RSA private key to PKCS#8 (DER) format.
func exportRSAPKCS8(entry *core.CryptoKeyEntry) (string, error) {
	privKey, ok := entry.EcKey.(*rsa.PrivateKey)
	if !ok {
		return "", fmt.Errorf("exportKeyRSA: not an RSA private key")
	}

	derBytes, err := x509.MarshalPKCS8PrivateKey(privKey)
	if err != nil {
		return "", fmt.Errorf("exportKeyRSA: %s", err.Error())
	}
	return base64.StdEncoding.EncodeToString(derBytes), nil
}
