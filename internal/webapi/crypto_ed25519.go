package webapi

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/cryguy/worker/v2/internal/core"
	"github.com/cryguy/worker/v2/internal/eventloop"
)

// cryptoEd25519JS patches crypto.subtle to support Ed25519 sign/verify/import/export/generate.
// Uses chain-of-responsibility: saves references to previous implementations
// and delegates non-Ed25519 calls to them.
const cryptoEd25519JS = `
(function() {
var subtle = crypto.subtle;
var CK = CryptoKey;
var _prevSign = subtle.sign;
var _prevVerify = subtle.verify;
var _prevImportKey = subtle.importKey;
var _prevExportKey = subtle.exportKey;
var _prevGenerateKey = subtle.generateKey;

subtle.sign = async function(algorithm, key, data) {
	var algo = typeof algorithm === 'string' ? { name: algorithm } : algorithm;
	if (algo.name === 'Ed25519') {
		var resultB64 = __cryptoSignEd25519(key._id, __bufferSourceToB64(data));
		return __b64ToBuffer(resultB64);
	}
	return _prevSign.call(this, algorithm, key, data);
};

subtle.verify = async function(algorithm, key, signature, data) {
	var algo = typeof algorithm === 'string' ? { name: algorithm } : algorithm;
	if (algo.name === 'Ed25519') {
		return !!__cryptoVerifyEd25519(key._id, __bufferSourceToB64(signature), __bufferSourceToB64(data));
	}
	return _prevVerify.call(this, algorithm, key, signature, data);
};

subtle.importKey = async function(format, keyData, algorithm, extractable, usages) {
	var algo = typeof algorithm === 'string' ? { name: algorithm } : algorithm;
	if (algo.name === 'Ed25519') {
		var dataStr;
		if (format === 'jwk') {
			dataStr = JSON.stringify(keyData);
		} else {
			dataStr = __bufferSourceToB64(keyData);
		}
		var resultJSON = __cryptoImportKeyEd25519(format, dataStr, extractable);
		var result = JSON.parse(resultJSON);
		if (result.error) throw new TypeError(result.error);
		return new CK(result.keyId, { name: 'Ed25519' }, result.keyType, extractable, usages);
	}
	return _prevImportKey.call(this, format, keyData, algorithm, extractable, usages);
};

subtle.exportKey = async function(format, key) {
	if (key.algorithm.name === 'Ed25519') {
		if (!key.extractable) throw new DOMException('key is not extractable', 'InvalidAccessError');
		var resultStr = __cryptoExportKeyEd25519(key._id, format);
		if (format === 'jwk') {
			return JSON.parse(resultStr);
		}
		return __b64ToBuffer(resultStr);
	}
	return _prevExportKey.call(this, format, key);
};

subtle.generateKey = async function(algorithm, extractable, usages) {
	var algo = typeof algorithm === 'string' ? { name: algorithm } : algorithm;
	if (algo.name === 'Ed25519') {
		var resultJSON = __cryptoGenerateKeyEd25519(extractable);
		var result = JSON.parse(resultJSON);
		if (result.error) throw new TypeError(result.error);
		return {
			privateKey: new CK(result.privateKeyId, { name: 'Ed25519' }, 'private', extractable,
				usages.filter(function(u) { return u === 'sign'; })),
			publicKey: new CK(result.publicKeyId, { name: 'Ed25519' }, 'public', extractable,
				usages.filter(function(u) { return u === 'verify'; })),
		};
	}
	return _prevGenerateKey.call(this, algorithm, extractable, usages);
};

})();
`

// SetupCryptoEd25519 registers Ed25519 sign/verify/import/export/generate.
// Must run after SetupCryptoExt.
func SetupCryptoEd25519(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	// __cryptoSignEd25519(keyID, dataB64) -> sigB64
	if err := rt.RegisterFunc("__cryptoSignEd25519", func(keyID int, dataB64 string) (string, error) {
		data, err := base64.StdEncoding.DecodeString(dataB64)
		if err != nil {
			return "", fmt.Errorf("signEd25519: invalid base64")
		}

		reqID := GetReqIDFromJS(rt)
		entry := core.GetCryptoKey(reqID, keyID)
		if entry == nil {
			return "", fmt.Errorf("signEd25519: key not found")
		}

		privKey, ok := entry.EcKey.(ed25519.PrivateKey)
		if !ok {
			return "", fmt.Errorf("signEd25519: key is not an Ed25519 private key")
		}

		sig := ed25519.Sign(privKey, data)
		return base64.StdEncoding.EncodeToString(sig), nil
	}); err != nil {
		return err
	}

	// __cryptoVerifyEd25519(keyID, sigB64, dataB64) -> bool
	if err := rt.RegisterFunc("__cryptoVerifyEd25519", func(keyID int, sigB64, dataB64 string) (int, error) {
		sig, err := base64.StdEncoding.DecodeString(sigB64)
		if err != nil {
			return 0, fmt.Errorf("verifyEd25519: invalid signature base64")
		}
		data, err := base64.StdEncoding.DecodeString(dataB64)
		if err != nil {
			return 0, fmt.Errorf("verifyEd25519: invalid data base64")
		}

		reqID := GetReqIDFromJS(rt)
		entry := core.GetCryptoKey(reqID, keyID)
		if entry == nil {
			return 0, fmt.Errorf("verifyEd25519: key not found")
		}

		var pubKey ed25519.PublicKey
		switch k := entry.EcKey.(type) {
		case ed25519.PublicKey:
			pubKey = k
		case ed25519.PrivateKey:
			pubKey = k.Public().(ed25519.PublicKey)
		default:
			return 0, fmt.Errorf("verifyEd25519: key is not an Ed25519 key")
		}

		return core.BoolToInt(ed25519.Verify(pubKey, data, sig)), nil
	}); err != nil {
		return err
	}

	// __cryptoGenerateKeyEd25519(extractable) -> JSON { privateKeyId, publicKeyId }
	if err := rt.RegisterFunc("__cryptoGenerateKeyEd25519", func(extractableVal bool) (string, error) {
		reqID := GetReqIDFromJS(rt)
		if core.GetRequestState(reqID) == nil {
			return `{"error":"no active request state"}`, nil
		}

		pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return fmt.Sprintf(`{"error":"key generation failed: %s"}`, err.Error()), nil
		}

		privID := core.ImportCryptoKeyFull(reqID, &core.CryptoKeyEntry{
			AlgoName: "Ed25519", KeyType: "private", EcKey: privKey, Extractable: extractableVal,
		})
		pubID := core.ImportCryptoKeyFull(reqID, &core.CryptoKeyEntry{
			AlgoName: "Ed25519", KeyType: "public", EcKey: pubKey, Extractable: extractableVal,
		})

		return fmt.Sprintf(`{"privateKeyId":%d,"publicKeyId":%d}`, privID, pubID), nil
	}); err != nil {
		return err
	}

	// __cryptoImportKeyEd25519(format, dataStr, extractable) -> JSON { keyId, keyType }
	if err := rt.RegisterFunc("__cryptoImportKeyEd25519", func(format, dataStr string, extractableVal bool) (string, error) {
		reqID := GetReqIDFromJS(rt)
		if core.GetRequestState(reqID) == nil {
			return `{"error":"no active request state"}`, nil
		}

		switch format {
		case "raw":
			keyData, err := base64.StdEncoding.DecodeString(dataStr)
			if err != nil {
				return `{"error":"invalid base64"}`, nil
			}
			if len(keyData) == ed25519.PublicKeySize {
				id := core.ImportCryptoKeyFull(reqID, &core.CryptoKeyEntry{
					AlgoName: "Ed25519", KeyType: "public",
					EcKey: ed25519.PublicKey(keyData), Extractable: extractableVal,
				})
				return fmt.Sprintf(`{"keyId":%d,"keyType":"public"}`, id), nil
			}
			if len(keyData) == ed25519.SeedSize {
				privKey := ed25519.NewKeyFromSeed(keyData)
				id := core.ImportCryptoKeyFull(reqID, &core.CryptoKeyEntry{
					AlgoName: "Ed25519", KeyType: "private",
					EcKey: privKey, Extractable: extractableVal,
				})
				return fmt.Sprintf(`{"keyId":%d,"keyType":"private"}`, id), nil
			}
			if len(keyData) == ed25519.PrivateKeySize {
				id := core.ImportCryptoKeyFull(reqID, &core.CryptoKeyEntry{
					AlgoName: "Ed25519", KeyType: "private",
					EcKey: ed25519.PrivateKey(keyData), Extractable: extractableVal,
				})
				return fmt.Sprintf(`{"keyId":%d,"keyType":"private"}`, id), nil
			}
			return fmt.Sprintf(`{"error":"invalid Ed25519 key length: %d"}`, len(keyData)), nil

		case "jwk":
			var jwk map[string]interface{}
			if err := json.Unmarshal([]byte(dataStr), &jwk); err != nil {
				return `{"error":"invalid JWK JSON"}`, nil
			}
			kty, _ := jwk["kty"].(string)
			crv, _ := jwk["crv"].(string)
			if kty != "OKP" || crv != "Ed25519" {
				return `{"error":"JWK must have kty=OKP and crv=Ed25519"}`, nil
			}
			xB64, _ := jwk["x"].(string)
			xBytes, err := base64.RawURLEncoding.DecodeString(xB64)
			if err != nil || len(xBytes) != ed25519.PublicKeySize {
				return `{"error":"invalid JWK x value"}`, nil
			}

			dB64, hasD := jwk["d"].(string)
			if hasD && dB64 != "" {
				dBytes, err := base64.RawURLEncoding.DecodeString(dB64)
				if err != nil || len(dBytes) != ed25519.SeedSize {
					return `{"error":"invalid JWK d value"}`, nil
				}
				privKey := ed25519.NewKeyFromSeed(dBytes)
				id := core.ImportCryptoKeyFull(reqID, &core.CryptoKeyEntry{
					AlgoName: "Ed25519", KeyType: "private", EcKey: privKey, Extractable: extractableVal,
				})
				return fmt.Sprintf(`{"keyId":%d,"keyType":"private"}`, id), nil
			}

			id := core.ImportCryptoKeyFull(reqID, &core.CryptoKeyEntry{
				AlgoName: "Ed25519", KeyType: "public",
				EcKey: ed25519.PublicKey(xBytes), Extractable: extractableVal,
			})
			return fmt.Sprintf(`{"keyId":%d,"keyType":"public"}`, id), nil

		default:
			return fmt.Sprintf(`{"error":"unsupported format %q"}`, format), nil
		}
	}); err != nil {
		return err
	}

	// __cryptoExportKeyEd25519(keyID, format) -> base64 or JSON string
	if err := rt.RegisterFunc("__cryptoExportKeyEd25519", func(keyID int, format string) (string, error) {
		reqID := GetReqIDFromJS(rt)
		entry := core.GetCryptoKey(reqID, keyID)
		if entry == nil {
			return "", fmt.Errorf("exportKeyEd25519: key not found")
		}
		if !entry.Extractable {
			return "", fmt.Errorf("key is not extractable")
		}

		switch format {
		case "raw":
			switch k := entry.EcKey.(type) {
			case ed25519.PublicKey:
				return base64.StdEncoding.EncodeToString(k), nil
			case ed25519.PrivateKey:
				// Export the seed (first 32 bytes) for raw private key export
				return base64.StdEncoding.EncodeToString(k.Seed()), nil
			default:
				return "", fmt.Errorf("exportKeyEd25519: not an Ed25519 key")
			}

		case "jwk":
			jwk := map[string]string{
				"kty": "OKP",
				"crv": "Ed25519",
			}
			switch k := entry.EcKey.(type) {
			case ed25519.PublicKey:
				jwk["x"] = base64.RawURLEncoding.EncodeToString(k)
			case ed25519.PrivateKey:
				pubKey := k.Public().(ed25519.PublicKey)
				jwk["x"] = base64.RawURLEncoding.EncodeToString(pubKey)
				jwk["d"] = base64.RawURLEncoding.EncodeToString(k.Seed())
			default:
				return "", fmt.Errorf("exportKeyEd25519: not an Ed25519 key")
			}
			data, _ := json.Marshal(jwk)
			return string(data), nil

		default:
			return "", fmt.Errorf("exportKeyEd25519: unsupported format %q", format)
		}
	}); err != nil {
		return err
	}

	if err := rt.Eval(cryptoEd25519JS); err != nil {
		return fmt.Errorf("evaluating crypto_ed25519.js: %w", err)
	}
	return nil
}
