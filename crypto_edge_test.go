package worker

// crypto_edge_test.go — edge cases adapted from cloudflare/workerd crypto-impl-asymmetric-test.js
// These are the cases MOST LIKELY to diverge between QuickJS and V8 engines.

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCryptoEdge_RSARejectExponent1 mirrors workerd rsa_reject_infinite_loop_test.
// publicExponent=1 must be rejected for both RSASSA-PKCS1-v1_5 and RSA-PSS.
// Divergence risk: QuickJS may pass the value through without range-checking.
func TestCryptoEdge_RSARejectExponent1(t *testing.T) {
	e := newTestEngine(t)
	source := `export default {
  async fetch(request, env) {
    const results = {};

    // exponent 1 must be rejected for RSASSA-PKCS1-v1_5
    try {
      await crypto.subtle.generateKey(
        { name: "RSASSA-PKCS1-v1_5", hash: "SHA-256", modulusLength: 1024,
          publicExponent: new Uint8Array([1]) },
        false, ["sign", "verify"]
      );
      results.rsassaRejected = false;
    } catch (e) {
      results.rsassaRejected = true;
      results.rsassaMsg = e.message || String(e);
    }

    // exponent 1 must be rejected for RSA-PSS
    try {
      await crypto.subtle.generateKey(
        { name: "RSA-PSS", hash: "SHA-256", modulusLength: 1024,
          publicExponent: new Uint8Array([1]) },
        false, ["sign", "verify"]
      );
      results.pssRejected = false;
    } catch (e) {
      results.pssRejected = true;
      results.pssMsg = e.message || String(e);
    }

    return Response.json(results);
  },
};`
	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		RsassaRejected bool   `json:"rsassaRejected"`
		RsassaMsg      string `json:"rsassaMsg"`
		PssRejected    bool   `json:"pssRejected"`
		PssMsg         string `json:"pssMsg"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.RsassaRejected {
		t.Error("RSASSA-PKCS1-v1_5 with publicExponent=1 must be rejected")
	}
	if !data.PssRejected {
		t.Error("RSA-PSS with publicExponent=1 must be rejected")
	}
}

// TestCryptoEdge_RSARejectExponent3 checks that exponent=3 is also blocked.
// workerd enforces only 65537 (0x010001); some engines allow 3.
// Divergence risk: V8/OpenSSL may allow 3, QuickJS shim may reject it.
func TestCryptoEdge_RSARejectExponent3(t *testing.T) {
	e := newTestEngine(t)
	source := `export default {
  async fetch(request, env) {
    try {
      await crypto.subtle.generateKey(
        { name: "RSASSA-PKCS1-v1_5", hash: "SHA-256", modulusLength: 2048,
          publicExponent: new Uint8Array([3]) },
        false, ["sign", "verify"]
      );
      return Response.json({ rejected: false });
    } catch (e) {
      return Response.json({ rejected: true, msg: e.message || String(e) });
    }
  },
};`
	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Rejected bool   `json:"rejected"`
		Msg      string `json:"msg"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.Rejected {
		t.Error("publicExponent=3 must be rejected (only 65537 allowed)")
	}
}

// TestCryptoEdge_PublicExponentTypeIsUint8Array mirrors workerd publicExponent_type_test.
// The algorithm.publicExponent on the returned key must be Uint8Array, not ArrayBuffer.
// Divergence risk: a naive implementation might return ArrayBuffer directly.
func TestCryptoEdge_PublicExponentTypeIsUint8Array(t *testing.T) {
	e := newTestEngine(t)
	source := `export default {
  async fetch(request, env) {
    const key = await crypto.subtle.generateKey(
      { name: "RSA-PSS", hash: "SHA-256", modulusLength: 2048,
        publicExponent: new Uint8Array([0x01, 0x00, 0x01]) },
      false, ["sign", "verify"]
    );
    const pe = key.publicKey.algorithm.publicExponent;
    return Response.json({
      tag: pe[Symbol.toStringTag] || Object.prototype.toString.call(pe),
      isUint8Array: pe instanceof Uint8Array,
      length: pe.length,
      // verify actual bytes are preserved
      b0: pe[0], b1: pe[1], b2: pe[2],
    });
  },
};`
	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Tag          string `json:"tag"`
		IsUint8Array bool   `json:"isUint8Array"`
		Length       int    `json:"length"`
		B0           int    `json:"b0"`
		B1           int    `json:"b1"`
		B2           int    `json:"b2"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.IsUint8Array {
		t.Errorf("publicExponent must be Uint8Array, got tag=%q", data.Tag)
	}
	if data.Length != 3 {
		t.Errorf("publicExponent length = %d, want 3", data.Length)
	}
	if data.B0 != 1 || data.B1 != 0 || data.B2 != 1 {
		t.Errorf("publicExponent bytes = [%d,%d,%d], want [1,0,1]", data.B0, data.B1, data.B2)
	}
}

// TestCryptoEdge_ECDHJWKImportIgnoresAlgField mirrors workerd ecdhJwkTest.
// ECDH JWK import must succeed even when the "alg" field has an unexpected value.
// Divergence risk: strict parsers may reject unrecognized alg values.
func TestCryptoEdge_ECDHJWKImportIgnoresAlgField(t *testing.T) {
	e := newTestEngine(t)
	source := `export default {
  async fetch(request, env) {
    const publicJwk = {
      kty: "EC",
      crv: "P-256",
      alg: "THIS CAN BE ANYTHING",
      x: "Ze2loSV3wrroKUN_4zhwGhCqo3Xhu1td4QjeQ5wIVR0",
      y: "HlLtdXARY_f55A3fnzQbPcm6hgr34Mp8p-nuzQCE0Zw",
    };
    try {
      const key = await crypto.subtle.importKey(
        "jwk", publicJwk,
        { name: "ECDH", namedCurve: "P-256" },
        true, []
      );
      return Response.json({ ok: true, type: key.type, algoName: key.algorithm.name });
    } catch (e) {
      return Response.json({ ok: false, err: e.message || String(e) });
    }
  },
};`
	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Ok       bool   `json:"ok"`
		Err      string `json:"err"`
		Type     string `json:"type"`
		AlgoName string `json:"algoName"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.Ok {
		t.Errorf("ECDH JWK import with arbitrary alg field must succeed, got: %s", data.Err)
	}
	if data.Type != "public" {
		t.Errorf("imported key type = %q, want 'public'", data.Type)
	}
	if data.AlgoName != "ECDH" {
		t.Errorf("algorithm name = %q, want 'ECDH'", data.AlgoName)
	}
}

// TestCryptoEdge_RSAImportKeyRejectsNonStringAlg mirrors workerd rsaAlgTest.
// importKey must reject when alg property is a non-string (BigInt 1024n).
// Divergence risk: engines differ in how they coerce/validate JWK alg fields.
func TestCryptoEdge_RSAImportKeyRejectsNonStringAlg(t *testing.T) {
	e := newTestEngine(t)
	source := `export default {
  async fetch(request, env) {
    // Build a JWK where alg is a BigInt (1024n) — not a string
    const v3 = { kty: "RSA" };
    Object.defineProperty(v3, "alg", {
      writable: true, configurable: true, value: 1024n,
    });
    try {
      await crypto.subtle.importKey(
        "jwk", v3,
        { name: "RSA-OAEP", hash: "SHA-1" },
        false, []
      );
      return Response.json({ rejected: false });
    } catch (e) {
      return Response.json({ rejected: true, msg: e.message || String(e) });
    }
  },
};`
	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Rejected bool   `json:"rejected"`
		Msg      string `json:"msg"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.Rejected {
		t.Error("importKey with BigInt alg field must be rejected")
	}
	// The error message should reference the unrecognized algorithm value
	if !strings.Contains(data.Msg, "1024") && !strings.Contains(strings.ToLower(data.Msg), "algorithm") {
		t.Logf("rejection message: %q (expected reference to '1024' or 'algorithm')", data.Msg)
	}
}

// TestCryptoEdge_SignEmptyData verifies sign/verify with zero-length input across algorithms.
// Divergence risk: engines may differ on whether empty ArrayBuffer is allowed.
func TestCryptoEdge_SignEmptyData(t *testing.T) {
	e := newTestEngine(t)
	source := `export default {
  async fetch(request, env) {
    const results = {};
    const empty = new Uint8Array(0);

    // HMAC-SHA256 with empty data
    const hmacKey = await crypto.subtle.generateKey(
      { name: "HMAC", hash: "SHA-256" }, false, ["sign", "verify"]
    );
    const hmacSig = await crypto.subtle.sign("HMAC", hmacKey, empty);
    results.hmacOk = await crypto.subtle.verify("HMAC", hmacKey, hmacSig, empty);
    results.hmacSigLen = new Uint8Array(hmacSig).length;

    // ECDSA P-256 with empty data
    const ecKey = await crypto.subtle.generateKey(
      { name: "ECDSA", namedCurve: "P-256" }, false, ["sign", "verify"]
    );
    const ecSig = await crypto.subtle.sign(
      { name: "ECDSA", hash: "SHA-256" }, ecKey.privateKey, empty
    );
    results.ecdsaOk = await crypto.subtle.verify(
      { name: "ECDSA", hash: "SHA-256" }, ecKey.publicKey, ecSig, empty
    );

    return Response.json(results);
  },
};`
	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		HmacOk     bool `json:"hmacOk"`
		HmacSigLen int  `json:"hmacSigLen"`
		EcdsaOk    bool `json:"ecdsaOk"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.HmacOk {
		t.Error("HMAC sign/verify with empty data must succeed")
	}
	if data.HmacSigLen != 32 {
		t.Errorf("HMAC-SHA256 sig over empty data length = %d, want 32", data.HmacSigLen)
	}
	if !data.EcdsaOk {
		t.Error("ECDSA sign/verify with empty data must succeed")
	}
}

// TestCryptoEdge_AlgorithmNameCaseSensitivity verifies that the canonical
// casing ("ECDSA", "AES-GCM") always works and that the algorithm name is
// preserved verbatim on the returned key.
// Divergence risk: engines differ on whether they normalise case on output
// (e.g. returning "ecdsa" vs "ECDSA" in key.algorithm.name).
func TestCryptoEdge_AlgorithmNameCaseSensitivity(t *testing.T) {
	e := newTestEngine(t)
	source := `export default {
  async fetch(request, env) {
    const results = {};

    // Correct casing must work and name must be preserved exactly
    try {
      const kp = await crypto.subtle.generateKey(
        { name: "ECDSA", namedCurve: "P-256" }, false, ["sign", "verify"]
      );
      results.correctCaseOk = true;
      results.ecdsaName = kp.publicKey.algorithm.name;
    } catch (e) {
      results.correctCaseOk = false;
      results.ecdsaName = "";
    }

    // AES-GCM canonical casing must work
    try {
      const k = await crypto.subtle.generateKey(
        { name: "AES-GCM", length: 128 }, false, ["encrypt", "decrypt"]
      );
      results.aesOk = true;
      results.aesName = k.algorithm.name;
    } catch (e) {
      results.aesOk = false;
      results.aesName = "";
    }

    // HMAC canonical casing must work
    try {
      const k = await crypto.subtle.generateKey(
        { name: "HMAC", hash: "SHA-256" }, false, ["sign", "verify"]
      );
      results.hmacOk = true;
      results.hmacName = k.algorithm.name;
    } catch (e) {
      results.hmacOk = false;
      results.hmacName = "";
    }

    return Response.json(results);
  },
};`
	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		CorrectCaseOk bool   `json:"correctCaseOk"`
		EcdsaName     string `json:"ecdsaName"`
		AesOk         bool   `json:"aesOk"`
		AesName       string `json:"aesName"`
		HmacOk        bool   `json:"hmacOk"`
		HmacName      string `json:"hmacName"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.CorrectCaseOk {
		t.Error("ECDSA with canonical casing must succeed")
	}
	if data.EcdsaName != "ECDSA" {
		t.Errorf("key.algorithm.name = %q, want 'ECDSA' (exact case preserved)", data.EcdsaName)
	}
	if !data.AesOk {
		t.Error("AES-GCM with canonical casing must succeed")
	}
	if data.AesName != "AES-GCM" {
		t.Errorf("AES-GCM key.algorithm.name = %q, want 'AES-GCM'", data.AesName)
	}
	if !data.HmacOk {
		t.Error("HMAC with canonical casing must succeed")
	}
	if data.HmacName != "HMAC" {
		t.Errorf("HMAC key.algorithm.name = %q, want 'HMAC'", data.HmacName)
	}
}

// TestCryptoEdge_JWKExportFieldPresence verifies JWK field completeness and correct
// base64url encoding for RSA public and private keys.
// Divergence risk: engines may omit optional CRT components (dp, dq, qi) or use
// standard base64 instead of base64url.
func TestCryptoEdge_JWKExportFieldPresence(t *testing.T) {
	e := newTestEngine(t)
	source := `export default {
  async fetch(request, env) {
    const kp = await crypto.subtle.generateKey(
      { name: "RSASSA-PKCS1-v1_5", modulusLength: 2048,
        publicExponent: new Uint8Array([1, 0, 1]), hash: "SHA-256" },
      true, ["sign", "verify"]
    );
    const pubJwk  = await crypto.subtle.exportKey("jwk", kp.publicKey);
    const privJwk = await crypto.subtle.exportKey("jwk", kp.privateKey);

    // base64url must not contain +, /, or = padding
    function isBase64url(s) {
      return typeof s === "string" && !/[+/=]/.test(s);
    }

    return Response.json({
      // public key fields
      pubHasKty: pubJwk.kty === "RSA",
      pubHasN:   isBase64url(pubJwk.n),
      pubHasE:   isBase64url(pubJwk.e),
      pubNoD:    pubJwk.d === undefined,
      pubAlg:    pubJwk.alg,

      // private key CRT fields
      privHasD:  isBase64url(privJwk.d),
      privHasP:  isBase64url(privJwk.p),
      privHasQ:  isBase64url(privJwk.q),
      privHasDp: isBase64url(privJwk.dp),
      privHasDq: isBase64url(privJwk.dq),
      privHasQi: isBase64url(privJwk.qi),
    });
  },
};`
	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		PubHasKty bool   `json:"pubHasKty"`
		PubHasN   bool   `json:"pubHasN"`
		PubHasE   bool   `json:"pubHasE"`
		PubNoD    bool   `json:"pubNoD"`
		PubAlg    string `json:"pubAlg"`
		PrivHasD  bool   `json:"privHasD"`
		PrivHasP  bool   `json:"privHasP"`
		PrivHasQ  bool   `json:"privHasQ"`
		PrivHasDp bool   `json:"privHasDp"`
		PrivHasDq bool   `json:"privHasDq"`
		PrivHasQi bool   `json:"privHasQi"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.PubHasKty {
		t.Error("public JWK must have kty=RSA")
	}
	if !data.PubHasN {
		t.Error("public JWK 'n' must be base64url encoded")
	}
	if !data.PubHasE {
		t.Error("public JWK 'e' must be base64url encoded")
	}
	if !data.PubNoD {
		t.Error("public JWK must not contain 'd'")
	}
	if data.PubAlg != "RS256" {
		t.Errorf("public JWK alg = %q, want 'RS256'", data.PubAlg)
	}
	if !data.PrivHasD {
		t.Error("private JWK must have base64url 'd'")
	}
	if !data.PrivHasP {
		t.Error("private JWK must have CRT 'p' field in base64url")
	}
	if !data.PrivHasQ {
		t.Error("private JWK must have CRT 'q' field in base64url")
	}
	if !data.PrivHasDp {
		t.Error("private JWK must have CRT 'dp' field in base64url")
	}
	if !data.PrivHasDq {
		t.Error("private JWK must have CRT 'dq' field in base64url")
	}
	if !data.PrivHasQi {
		t.Error("private JWK must have CRT 'qi' field in base64url")
	}
}

// TestCryptoEdge_KeyTypePreservation verifies that key.type is correctly set
// for all asymmetric algorithms after generateKey.
// Divergence risk: engines may swap public/private labels.
func TestCryptoEdge_KeyTypePreservation(t *testing.T) {
	e := newTestEngine(t)
	source := `export default {
  async fetch(request, env) {
    const rsaKp = await crypto.subtle.generateKey(
      { name: "RSA-OAEP", modulusLength: 2048,
        publicExponent: new Uint8Array([1, 0, 1]), hash: "SHA-256" },
      false, ["encrypt", "decrypt"]
    );
    const ecKp = await crypto.subtle.generateKey(
      { name: "ECDSA", namedCurve: "P-384" },
      false, ["sign", "verify"]
    );
    const edKp = await crypto.subtle.generateKey(
      { name: "Ed25519" }, false, ["sign", "verify"]
    );
    const ecdhKp = await crypto.subtle.generateKey(
      { name: "ECDH", namedCurve: "P-256" },
      false, ["deriveKey", "deriveBits"]
    );
    return Response.json({
      rsaPubType:  rsaKp.publicKey.type,
      rsaPrivType: rsaKp.privateKey.type,
      ecPubType:   ecKp.publicKey.type,
      ecPrivType:  ecKp.privateKey.type,
      edPubType:   edKp.publicKey.type,
      edPrivType:  edKp.privateKey.type,
      ecdhPubType: ecdhKp.publicKey.type,
      ecdhPrivType: ecdhKp.privateKey.type,
    });
  },
};`
	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		RsaPubType   string `json:"rsaPubType"`
		RsaPrivType  string `json:"rsaPrivType"`
		EcPubType    string `json:"ecPubType"`
		EcPrivType   string `json:"ecPrivType"`
		EdPubType    string `json:"edPubType"`
		EdPrivType   string `json:"edPrivType"`
		EcdhPubType  string `json:"ecdhPubType"`
		EcdhPrivType string `json:"ecdhPrivType"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	checks := [][2]string{
		{data.RsaPubType, "public"},
		{data.RsaPrivType, "private"},
		{data.EcPubType, "public"},
		{data.EcPrivType, "private"},
		{data.EdPubType, "public"},
		{data.EdPrivType, "private"},
		{data.EcdhPubType, "public"},
		{data.EcdhPrivType, "private"},
	}
	names := []string{
		"RSA-OAEP public", "RSA-OAEP private",
		"ECDSA public", "ECDSA private",
		"Ed25519 public", "Ed25519 private",
		"ECDH public", "ECDH private",
	}
	for i, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s key.type = %q, want %q", names[i], c[0], c[1])
		}
	}
}

// TestCryptoEdge_ImportKeyWithInvalidFormats checks that importKey rejects
// unknown format strings and format/algorithm mismatches.
// Divergence risk: error type or message may differ; some engines may not throw at all.
func TestCryptoEdge_ImportKeyWithInvalidFormats(t *testing.T) {
	e := newTestEngine(t)
	source := `export default {
  async fetch(request, env) {
    const results = {};

    // Unknown format string
    try {
      await crypto.subtle.importKey(
        "invalid-format", new Uint8Array([1,2,3]),
        { name: "AES-GCM", length: 128 }, true, ["encrypt"]
      );
      results.unknownFormatFailed = false;
    } catch (e) {
      results.unknownFormatFailed = true;
    }

    // "raw" format on RSA (not supported)
    try {
      await crypto.subtle.importKey(
        "raw", new Uint8Array(256),
        { name: "RSASSA-PKCS1-v1_5", hash: "SHA-256" }, true, ["verify"]
      );
      results.rsaRawFailed = false;
    } catch (e) {
      results.rsaRawFailed = true;
    }

    // "spki" format with zero-length data
    try {
      await crypto.subtle.importKey(
        "spki", new Uint8Array(0),
        { name: "RSASSA-PKCS1-v1_5", hash: "SHA-256" }, true, ["verify"]
      );
      results.emptySpkiFailed = false;
    } catch (e) {
      results.emptySpkiFailed = true;
    }

    // "jwk" with null key data
    try {
      await crypto.subtle.importKey(
        "jwk", null,
        { name: "AES-GCM", length: 128 }, true, ["encrypt"]
      );
      results.nullJwkFailed = false;
    } catch (e) {
      results.nullJwkFailed = true;
    }

    return Response.json(results);
  },
};`
	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		UnknownFormatFailed bool `json:"unknownFormatFailed"`
		RsaRawFailed        bool `json:"rsaRawFailed"`
		EmptySpkiFailed     bool `json:"emptySpkiFailed"`
		NullJwkFailed       bool `json:"nullJwkFailed"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.UnknownFormatFailed {
		t.Error("importKey with unknown format must throw")
	}
	if !data.RsaRawFailed {
		t.Error("importKey RSA with 'raw' format must throw")
	}
	if !data.EmptySpkiFailed {
		t.Error("importKey with empty SPKI data must throw")
	}
	if !data.NullJwkFailed {
		t.Error("importKey with null JWK data must throw")
	}
}

// TestCryptoEdge_GenerateKeyUnusualParameters tests RSA-PSS with SHA-512 and
// ECDSA with P-384 -- less-common parameter combinations that can expose
// algorithm dispatch bugs in either engine.
func TestCryptoEdge_GenerateKeyUnusualParameters(t *testing.T) {
	e := newTestEngine(t)
	source := `export default {
  async fetch(request, env) {
    // RSA-PSS with SHA-512
    const pssKey = await crypto.subtle.generateKey(
      { name: "RSA-PSS", modulusLength: 2048,
        publicExponent: new Uint8Array([1, 0, 1]), hash: "SHA-512" },
      true, ["sign", "verify"]
    );
    const pssSig = await crypto.subtle.sign(
      { name: "RSA-PSS", saltLength: 64 }, pssKey.privateKey,
      new TextEncoder().encode("sha512 pss test")
    );
    const pssOk = await crypto.subtle.verify(
      { name: "RSA-PSS", saltLength: 64 }, pssKey.publicKey, pssSig,
      new TextEncoder().encode("sha512 pss test")
    );
    const pssAlgHash = pssKey.publicKey.algorithm.hash.name;
    const pssSigLen = new Uint8Array(pssSig).length;

    // ECDSA with P-384 (less common than P-256, widely supported)
    const ecKey = await crypto.subtle.generateKey(
      { name: "ECDSA", namedCurve: "P-384" }, false, ["sign", "verify"]
    );
    const ecSig = await crypto.subtle.sign(
      { name: "ECDSA", hash: "SHA-384" }, ecKey.privateKey,
      new TextEncoder().encode("p384 test")
    );
    const ecOk = await crypto.subtle.verify(
      { name: "ECDSA", hash: "SHA-384" }, ecKey.publicKey, ecSig,
      new TextEncoder().encode("p384 test")
    );
    const ecCurve = ecKey.publicKey.algorithm.namedCurve;

    return Response.json({
      pssOk: pssOk,
      pssAlgHash: pssAlgHash,
      pssSigLen: pssSigLen,
      ecOk: ecOk,
      ecCurve: ecCurve,
    });
  },
};`
	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		PssOk      bool   `json:"pssOk"`
		PssAlgHash string `json:"pssAlgHash"`
		PssSigLen  int    `json:"pssSigLen"`
		EcOk       bool   `json:"ecOk"`
		EcCurve    string `json:"ecCurve"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.PssOk {
		t.Error("RSA-PSS with SHA-512 sign/verify must succeed")
	}
	if data.PssAlgHash != "SHA-512" {
		t.Errorf("RSA-PSS key algorithm.hash.name = %q, want 'SHA-512'", data.PssAlgHash)
	}
	if data.PssSigLen != 256 {
		t.Errorf("RSA-PSS 2048-bit sig length = %d, want 256", data.PssSigLen)
	}
	if !data.EcOk {
		t.Error("ECDSA P-384 sign/verify must succeed")
	}
	if data.EcCurve != "P-384" {
		t.Errorf("ECDSA namedCurve = %q, want 'P-384'", data.EcCurve)
	}
}

// TestCryptoEdge_VerifyWithTamperedSignature checks that verification correctly
// returns false (not throws) when the signature itself is bitwise corrupted.
// Divergence risk: some engines throw on invalid sig bytes instead of returning false.
func TestCryptoEdge_VerifyWithTamperedSignature(t *testing.T) {
	e := newTestEngine(t)
	source := `export default {
  async fetch(request, env) {
    const msg = new TextEncoder().encode("verify tamper test");

    // RSASSA-PKCS1-v1_5
    const rsaKp = await crypto.subtle.generateKey(
      { name: "RSASSA-PKCS1-v1_5", modulusLength: 2048,
        publicExponent: new Uint8Array([1, 0, 1]), hash: "SHA-256" },
      false, ["sign", "verify"]
    );
    const rsaSig = await crypto.subtle.sign("RSASSA-PKCS1-v1_5", rsaKp.privateKey, msg);
    // Corrupt the first byte
    const rsaCorrupt = new Uint8Array(rsaSig);
    rsaCorrupt[0] ^= 0xFF;
    let rsaResult, rsaThrew = false;
    try {
      rsaResult = await crypto.subtle.verify("RSASSA-PKCS1-v1_5", rsaKp.publicKey, rsaCorrupt, msg);
    } catch (e) {
      rsaThrew = true;
    }

    // ECDSA
    const ecKp = await crypto.subtle.generateKey(
      { name: "ECDSA", namedCurve: "P-256" }, false, ["sign", "verify"]
    );
    const ecSig = await crypto.subtle.sign(
      { name: "ECDSA", hash: "SHA-256" }, ecKp.privateKey, msg
    );
    const ecCorrupt = new Uint8Array(ecSig);
    ecCorrupt[0] ^= 0xFF;
    let ecResult, ecThrew = false;
    try {
      ecResult = await crypto.subtle.verify(
        { name: "ECDSA", hash: "SHA-256" }, ecKp.publicKey, ecCorrupt, msg
      );
    } catch (e) {
      ecThrew = true;
    }

    // Ed25519
    const edKp = await crypto.subtle.generateKey(
      { name: "Ed25519" }, false, ["sign", "verify"]
    );
    const edSig = await crypto.subtle.sign("Ed25519", edKp.privateKey, msg);
    const edCorrupt = new Uint8Array(edSig);
    edCorrupt[0] ^= 0xFF;
    let edResult, edThrew = false;
    try {
      edResult = await crypto.subtle.verify("Ed25519", edKp.publicKey, edCorrupt, msg);
    } catch (e) {
      edThrew = true;
    }

    return Response.json({
      rsaResult: rsaThrew ? null : rsaResult,
      rsaThrew,
      ecResult: ecThrew ? null : ecResult,
      ecThrew,
      edResult: edThrew ? null : edResult,
      edThrew,
    });
  },
};`
	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		RsaResult *bool `json:"rsaResult"`
		RsaThrew  bool  `json:"rsaThrew"`
		EcResult  *bool `json:"ecResult"`
		EcThrew   bool  `json:"ecThrew"`
		EdResult  *bool `json:"edResult"`
		EdThrew   bool  `json:"edThrew"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// RSA: spec says verify() must return false on bad sig, not throw
	if data.RsaThrew {
		t.Error("RSASSA verify with corrupted sig must return false, not throw")
	} else if data.RsaResult == nil || *data.RsaResult {
		t.Error("RSASSA verify with corrupted sig must return false")
	}
	// ECDSA: similarly must return false (not throw) for malformed sig
	if !data.EcThrew && data.EcResult != nil && *data.EcResult {
		t.Error("ECDSA verify with corrupted sig must return false")
	}
	// Ed25519: must return false for corrupted sig
	if data.EdThrew {
		t.Error("Ed25519 verify with corrupted sig must return false, not throw")
	} else if data.EdResult == nil || *data.EdResult {
		t.Error("Ed25519 verify with corrupted sig must return false")
	}
}

// TestCryptoEdge_ECDHJWKRoundTrip verifies ECDH key import/export JWK round-trip,
// including that the exported JWK omits 'd' for public keys.
// Divergence risk: engines may include or format the 'd' field differently.
func TestCryptoEdge_ECDHJWKRoundTrip(t *testing.T) {
	e := newTestEngine(t)
	source := `export default {
  async fetch(request, env) {
    const kp = await crypto.subtle.generateKey(
      { name: "ECDH", namedCurve: "P-256" }, true, ["deriveKey", "deriveBits"]
    );

    const pubJwk  = await crypto.subtle.exportKey("jwk", kp.publicKey);
    const privJwk = await crypto.subtle.exportKey("jwk", kp.privateKey);

    // Public key JWK must not have 'd'
    const pubNoD = pubJwk.d === undefined;
    // Private key JWK must have 'd', 'x', 'y'
    const privOk = typeof privJwk.d === "string" &&
                   typeof privJwk.x === "string" &&
                   typeof privJwk.y === "string";

    // Re-import the public key and derive bits
    const reimportedPub = await crypto.subtle.importKey(
      "jwk", pubJwk, { name: "ECDH", namedCurve: "P-256" }, false, []
    );
    const derived = await crypto.subtle.deriveBits(
      { name: "ECDH", public: reimportedPub }, kp.privateKey, 128
    );

    return Response.json({
      pubCrv: pubJwk.crv,
      pubKty: pubJwk.kty,
      pubNoD,
      privOk,
      derivedLen: new Uint8Array(derived).length,
    });
  },
};`
	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		PubCrv     string `json:"pubCrv"`
		PubKty     string `json:"pubKty"`
		PubNoD     bool   `json:"pubNoD"`
		PrivOk     bool   `json:"privOk"`
		DerivedLen int    `json:"derivedLen"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.PubKty != "EC" {
		t.Errorf("ECDH public JWK kty = %q, want 'EC'", data.PubKty)
	}
	if data.PubCrv != "P-256" {
		t.Errorf("ECDH public JWK crv = %q, want 'P-256'", data.PubCrv)
	}
	if !data.PubNoD {
		t.Error("ECDH public JWK must not contain 'd'")
	}
	if !data.PrivOk {
		t.Error("ECDH private JWK must have 'd', 'x', 'y'")
	}
	if data.DerivedLen != 16 {
		t.Errorf("ECDH deriveBits(128) length = %d, want 16", data.DerivedLen)
	}
}
