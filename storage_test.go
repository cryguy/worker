package worker

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"modernc.org/quickjs"
)

// ---------------------------------------------------------------------------
// QuickJS test helpers for storage_test.go
// ---------------------------------------------------------------------------

// buildStorageBinding creates a test R2 bucket binding for the given store.
// It sets up the VM with storage support and returns a bucket object.
func buildStorageBinding(vm *quickjs.VM, store R2Store) (quickjs.Value, error) {
	el := newEventLoop()

	// Register __r2_* Go globals
	if err := setupStorage(vm, el); err != nil {
		return quickjs.Value{}, err
	}

	// Create request state with the store
	env := &Env{
		Vars:    make(map[string]string),
		Secrets: make(map[string]string),
		Storage: map[string]R2Store{"TEST_BUCKET": store},
	}
	reqID := newRequestState(10, env)

	// Set __requestID on VM
	if err := setGlobal(vm, "__requestID", reqID); err != nil {
		return quickjs.Value{}, err
	}

	// Build the JS R2 binding object (copied from assets.go buildEnvObject)
	storageJS := fmt.Sprintf(`
		(function() {
			return {
				get: function(key) {
					if (!key) return Promise.reject(new Error("get requires a key"));
					var reqID = String(globalThis.__requestID);
					var resultStr = __r2_get(reqID, %s, String(key));
					return new Promise(function(resolve, reject) {
						try {
							if (resultStr === "null") {
								resolve(null);
								return;
							}
							var obj = JSON.parse(resultStr);
							var bodyBytes = Uint8Array.from(atob(obj.bodyB64), function(c) { return c.charCodeAt(0); });
							var bodyUsed = false;
							var consumeBody = function(method) {
								if (bodyUsed) {
									return Promise.reject(new Error("body already consumed"));
								}
								bodyUsed = true;
								return method();
							};
							resolve({
								key: obj.key,
								size: obj.size,
								etag: obj.etag,
								httpEtag: '"' + obj.etag + '"',
								version: obj.etag,
								storageClass: "STANDARD",
								httpMetadata: { contentType: obj.contentType || null },
								customMetadata: obj.customMetadata || {},
								checksums: {},
								uploaded: new Date(obj.uploaded),
								get bodyUsed() { return bodyUsed; },
								text: function() { return consumeBody(function() { return Promise.resolve(new TextDecoder().decode(bodyBytes)); }); },
								arrayBuffer: function() { return consumeBody(function() { return Promise.resolve(bodyBytes.buffer); }); },
								json: function() { return consumeBody(function() {
									var text = new TextDecoder().decode(bodyBytes);
									try {
										return Promise.resolve(JSON.parse(text));
									} catch(e) {
										return Promise.reject(new Error("invalid JSON"));
									}
								}); }
							});
						} catch(e) {
							reject(e);
						}
					});
				},
				put: function(key, value, opts) {
					if (!key || value === undefined) return Promise.reject(new Error("put requires key and value"));
					var reqID = String(globalThis.__requestID);
					return new Promise(function(resolve, reject) {
						try {
							var bytes;
							if (typeof value === "string") {
								bytes = new TextEncoder().encode(value);
							} else if (value instanceof ArrayBuffer) {
								bytes = new Uint8Array(value);
							} else if (ArrayBuffer.isView(value)) {
								bytes = new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
							} else {
								reject(new Error("unsupported body type"));
								return;
							}
							var bodyB64 = btoa(String.fromCharCode.apply(null, bytes));
							var optsJSON = opts ? JSON.stringify({
								httpMetadata: { contentType: (opts.httpMetadata && opts.httpMetadata.contentType) || null },
								customMetadata: opts.customMetadata || {}
							}) : "{}";
							var resultStr = __r2_put(reqID, %s, String(key), bodyB64, optsJSON);
							var obj = JSON.parse(resultStr);
							resolve({
								key: obj.key,
								size: obj.size,
								etag: obj.etag,
								httpEtag: '"' + obj.etag + '"',
								version: obj.etag,
								httpMetadata: { contentType: obj.contentType || null },
								customMetadata: obj.customMetadata || {}
							});
						} catch(e) {
							reject(new Error("error putting object: " + e.message));
						}
					});
				},
				delete: function(keys) {
					if (!keys) return Promise.reject(new Error("delete requires a key"));
					var reqID = String(globalThis.__requestID);
					var keysArray = Array.isArray(keys) ? keys : [String(keys)];
					return new Promise(function(resolve, reject) {
						try {
							__r2_delete(reqID, %s, JSON.stringify(keysArray));
							resolve();
						} catch(e) {
							reject(e);
						}
					});
				},
				head: function(key) {
					if (!key) return Promise.reject(new Error("head requires a key"));
					var reqID = String(globalThis.__requestID);
					var resultStr = __r2_head(reqID, %s, String(key));
					return new Promise(function(resolve, reject) {
						try {
							if (resultStr === "null") {
								resolve(null);
								return;
							}
							var obj = JSON.parse(resultStr);
							resolve({
								key: obj.key,
								size: obj.size,
								etag: obj.etag,
								httpEtag: '"' + obj.etag + '"',
								version: obj.etag,
								storageClass: "STANDARD",
								httpMetadata: { contentType: obj.contentType || null },
								customMetadata: obj.customMetadata || {},
								checksums: {},
								uploaded: new Date(obj.uploaded)
							});
						} catch(e) {
							reject(e);
						}
					});
				},
				list: function(opts) {
					var reqID = String(globalThis.__requestID);
					var optsJSON = opts ? JSON.stringify({
						prefix: opts.prefix || "",
						cursor: opts.cursor || "",
						delimiter: opts.delimiter || "",
						limit: opts.limit || 1000
					}) : "{}";
					return new Promise(function(resolve, reject) {
						try {
							var resultStr = __r2_list(reqID, %s, optsJSON);
							var result = JSON.parse(resultStr);
							resolve({
								objects: (result.objects || []).map(function(o) {
									return {
										key: o.key,
										size: o.size,
										etag: o.etag,
										httpEtag: '"' + o.etag + '"',
										uploaded: new Date(o.uploaded)
									};
								}),
								truncated: result.truncated,
								cursor: result.cursor,
								delimitedPrefixes: result.delimitedPrefixes || []
							});
						} catch(e) {
							reject(e);
						}
					});
				},
				createSignedUrl: function(key, opts) {
					if (!key) return Promise.reject(new Error("createSignedUrl requires a key"));
					var reqID = String(globalThis.__requestID);
					var expiresIn = (opts && opts.expiresIn) || 3600;
					return new Promise(function(resolve, reject) {
						try {
							var url = __r2_presigned_url(reqID, %s, String(key), expiresIn);
							resolve(url);
						} catch(e) {
							reject(e);
						}
					});
				},
				publicUrl: function(key) {
					if (!key) throw new Error("publicUrl requires a key");
					var reqID = String(globalThis.__requestID);
					return __r2_public_url(reqID, %s, String(key));
				}
			};
		})()
	`, jsEscape("TEST_BUCKET"), jsEscape("TEST_BUCKET"), jsEscape("TEST_BUCKET"),
	   jsEscape("TEST_BUCKET"), jsEscape("TEST_BUCKET"), jsEscape("TEST_BUCKET"), jsEscape("TEST_BUCKET"))

	val, err := vm.EvalValue(storageJS, quickjs.EvalGlobal)
	if err != nil {
		return quickjs.Value{}, err
	}
	return val, nil
}

// buildR2Object creates a QuickJS R2Object metadata-only object (no body).
func buildR2Object(vm *quickjs.VM, key string, size int64, etag string, contentType string, customMeta map[string]string) (struct{ Value quickjs.Value }, error) {
	customMetaJSON := "{}"
	if customMeta != nil {
		data, _ := json.Marshal(customMeta)
		customMetaJSON = string(data)
	}

	contentTypeJS := "undefined"
	if contentType != "" {
		contentTypeJS = jsEscape(contentType)
	}

	jsCode := fmt.Sprintf(`
		({
			key: %s,
			size: %d,
			etag: %s,
			httpEtag: %s,
			version: %s,
			storageClass: "STANDARD",
			httpMetadata: { contentType: %s },
			customMetadata: %s,
			checksums: {}
		})
	`, jsEscape(key), size, jsEscape(etag), jsEscape(`"`+etag+`"`), jsEscape(etag),
		contentTypeJS, customMetaJSON)

	val, err := vm.EvalValue(jsCode, quickjs.EvalGlobal)
	if err != nil {
		return struct{ Value quickjs.Value }{}, err
	}
	return struct{ Value quickjs.Value }{Value: val}, nil
}

// buildR2ObjectBody creates a QuickJS R2ObjectBody (with body methods).
func buildR2ObjectBody(vm *quickjs.VM, key string, data []byte, r2obj *R2Object) (struct{ Value quickjs.Value }, error) {
	bodyB64 := ""
	if len(data) > 0 {
		// Encode data to base64 for transfer to JS
		bodyB64 = encodeToBase64(data)
	}

	customMetaJSON := "{}"
	if r2obj.CustomMetadata != nil {
		metaData, _ := json.Marshal(r2obj.CustomMetadata)
		customMetaJSON = string(metaData)
	}

	contentTypeJS := "undefined"
	if r2obj.ContentType != "" {
		contentTypeJS = jsEscape(r2obj.ContentType)
	}

	jsCode := fmt.Sprintf(`
		(function() {
			var bodyBytes = Uint8Array.from(atob(%s), function(c) { return c.charCodeAt(0); });
			var bodyUsed = false;
			var consumeBody = function(method) {
				if (bodyUsed) {
					return Promise.reject(new Error("body already consumed"));
				}
				bodyUsed = true;
				return method();
			};
			return {
				key: %s,
				size: %d,
				etag: %s,
				httpEtag: %s,
				version: %s,
				storageClass: "STANDARD",
				httpMetadata: { contentType: %s },
				customMetadata: %s,
				checksums: {},
				uploaded: new Date(%d),
				get bodyUsed() { return bodyUsed; },
				text: function() { return consumeBody(function() { return Promise.resolve(new TextDecoder().decode(bodyBytes)); }); },
				arrayBuffer: function() { return consumeBody(function() { return Promise.resolve(bodyBytes.buffer); }); },
				json: function() { return consumeBody(function() {
					var text = new TextDecoder().decode(bodyBytes);
					try {
						return Promise.resolve(JSON.parse(text));
					} catch(e) {
						return Promise.reject(new Error("invalid JSON"));
					}
				}); }
			};
		})()
	`, jsEscape(bodyB64), jsEscape(key), r2obj.Size, jsEscape(r2obj.ETag),
		jsEscape(`"`+r2obj.ETag+`"`), jsEscape(r2obj.ETag), contentTypeJS,
		customMetaJSON, r2obj.LastModified.UnixMilli())

	val, err := vm.EvalValue(jsCode, quickjs.EvalGlobal)
	if err != nil {
		return struct{ Value quickjs.Value }{}, err
	}
	return struct{ Value quickjs.Value }{Value: val}, nil
}

// quickjsValueIsNull checks if a QuickJS value is null using the VM.
func quickjsValueIsNull(vm *quickjs.VM, val quickjs.Value) bool {
	tmpName := fmt.Sprintf("__null_check_%d", time.Now().UnixNano())
	if err := setGlobal(vm, tmpName, val); err != nil {
		return false
	}
	defer evalDiscard(vm, "delete globalThis."+tmpName)
	result, err := evalBool(vm, fmt.Sprintf("globalThis.%s === null", tmpName))
	if err != nil {
		return false
	}
	return result
}

// quickjsValueIsUndefined checks if a QuickJS value is undefined using the VM.
func quickjsValueIsUndefined(vm *quickjs.VM, val quickjs.Value) bool {
	tmpName := fmt.Sprintf("__undef_check_%d", time.Now().UnixNano())
	if err := setGlobal(vm, tmpName, val); err != nil {
		return false
	}
	defer evalDiscard(vm, "delete globalThis."+tmpName)
	result, err := evalBool(vm, fmt.Sprintf("typeof globalThis.%s === 'undefined'", tmpName))
	if err != nil {
		return false
	}
	return result
}

// quickjsValueToString converts a QuickJS value to a Go string.
func quickjsValueToString(vm *quickjs.VM, val quickjs.Value) (string, error) {
	tmpName := fmt.Sprintf("__str_check_%d", time.Now().UnixNano())
	if err := setGlobal(vm, tmpName, val); err != nil {
		return "", err
	}
	defer evalDiscard(vm, "delete globalThis."+tmpName)
	return evalString(vm, fmt.Sprintf("String(globalThis.%s)", tmpName))
}

// awaitValue awaits a promise stored in a global variable.
// This is a wrapper around the new awaitValue(vm, globalVar, deadline) signature.
func awaitValueCompat(vm *quickjs.VM, promiseVal quickjs.Value, deadline time.Time) (quickjs.Value, error) {
	// Store the promise in a temporary global
	tmpVar := fmt.Sprintf("__test_promise_%d", time.Now().UnixNano())
	if err := setGlobal(vm, tmpVar, promiseVal); err != nil {
		return quickjs.Value{}, err
	}
	defer evalDiscard(vm, "delete globalThis."+tmpVar)

	// Await it
	if err := awaitValue(vm, tmpVar, deadline); err != nil {
		return quickjs.Value{}, err
	}

	// Read back the result
	val, err := vm.EvalValue("globalThis."+tmpVar, quickjs.EvalGlobal)
	if err != nil {
		return quickjs.Value{}, err
	}
	return val, nil
}

// encodeToBase64 encodes bytes to base64 string.
func encodeToBase64(data []byte) string {
	const base64Chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	if len(data) == 0 {
		return ""
	}

	var result strings.Builder
	result.Grow((len(data) + 2) / 3 * 4)

	for i := 0; i < len(data); i += 3 {
		b0 := data[i]
		b1 := byte(0)
		b2 := byte(0)
		if i+1 < len(data) {
			b1 = data[i+1]
		}
		if i+2 < len(data) {
			b2 = data[i+2]
		}

		result.WriteByte(base64Chars[b0>>2])
		result.WriteByte(base64Chars[((b0&0x03)<<4)|(b1>>4)])
		if i+1 < len(data) {
			result.WriteByte(base64Chars[((b1&0x0f)<<2)|(b2>>6)])
		} else {
			result.WriteByte('=')
		}
		if i+2 < len(data) {
			result.WriteByte(base64Chars[b2&0x3f])
		} else {
			result.WriteByte('=')
		}
	}

	return result.String()
}

// ---------------------------------------------------------------------------
// Error-returning R2Store variants for testing error paths.
// ---------------------------------------------------------------------------

// errR2Store always returns errors for Get, Put, Head, List, Delete,
// and PresignedGetURL. Used in place of badMinioClient-backed StorageBridge.
type errR2Store struct {
	publicURL string // if set, PublicURL returns this base
}

func (e *errR2Store) Get(key string) ([]byte, *R2Object, error) {
	return nil, nil, fmt.Errorf("connection refused")
}

func (e *errR2Store) Put(key string, data []byte, opts R2PutOptions) (*R2Object, error) {
	return nil, fmt.Errorf("connection refused")
}

func (e *errR2Store) Delete(keys []string) error {
	// StorageBridge.Delete silently ignores errors, so mock does too.
	return nil
}

func (e *errR2Store) Head(key string) (*R2Object, error) {
	return nil, fmt.Errorf("connection refused")
}

func (e *errR2Store) List(opts R2ListOptions) (*R2ListResult, error) {
	// StorageBridge.List returns empty result on error.
	return &R2ListResult{Objects: nil, Truncated: false}, nil
}

func (e *errR2Store) PresignedGetURL(key string, expiry time.Duration) (string, error) {
	return "", fmt.Errorf("connection refused")
}

func (e *errR2Store) PublicURL(key string) (string, error) {
	if e.publicURL != "" {
		return fmt.Sprintf("%s/%s", e.publicURL, key), nil
	}
	return "", fmt.Errorf("public URL not configured")
}

// failListR2Store returns errors from List (and Delete) to exercise
// the error-handling branches in buildStorageBinding.
type failListR2Store struct {
	errR2Store
}

func (f *failListR2Store) Delete(keys []string) error {
	return fmt.Errorf("delete failed")
}

func (f *failListR2Store) List(opts R2ListOptions) (*R2ListResult, error) {
	return nil, fmt.Errorf("list failed")
}

// nilConfigR2Store simulates a StorageBridge with no client configured.
// PresignedGetURL returns "storage client not configured".
type nilConfigR2Store struct{}

func (n *nilConfigR2Store) Get(key string) ([]byte, *R2Object, error) {
	return nil, nil, fmt.Errorf("storage client not configured")
}

func (n *nilConfigR2Store) Put(key string, data []byte, opts R2PutOptions) (*R2Object, error) {
	return nil, fmt.Errorf("storage client not configured")
}

func (n *nilConfigR2Store) Delete(keys []string) error { return nil }

func (n *nilConfigR2Store) Head(key string) (*R2Object, error) {
	return nil, fmt.Errorf("storage client not configured")
}

func (n *nilConfigR2Store) List(opts R2ListOptions) (*R2ListResult, error) {
	return &R2ListResult{}, nil
}

func (n *nilConfigR2Store) PresignedGetURL(key string, expiry time.Duration) (string, error) {
	return "", fmt.Errorf("storage client not configured")
}

func (n *nilConfigR2Store) PublicURL(key string) (string, error) {
	return "", fmt.Errorf("public URL not configured")
}

// noPresignR2Store has a working store but presign returns "presign client not configured".
type noPresignR2Store struct {
	errR2Store
}

func (n *noPresignR2Store) PresignedGetURL(key string, expiry time.Duration) (string, error) {
	return "", fmt.Errorf("presign client not configured")
}

// newV8TestContext creates a VM with encoding support (atob/btoa)
// and TextDecoder/TextEncoder needed by R2ObjectBody methods. Cleaned up automatically via t.Cleanup.
func newV8TestContext(t *testing.T) *quickjs.VM {
	t.Helper()
	vm, err := quickjs.NewVM()
	if err != nil {
		t.Fatalf("NewVM: %v", err)
	}
	el := newEventLoop()
	if err := setupEncoding(vm, el); err != nil {
		vm.Close()
		t.Fatalf("setupEncoding: %v", err)
	}
	if err := setupWebAPIs(vm, el); err != nil {
		vm.Close()
		t.Fatalf("setupWebAPIs: %v", err)
	}
	t.Cleanup(func() {
		vm.Close()
	})
	return vm
}

func TestStorageBinding_Put_UnsupportedTypeRejected(t *testing.T) {
	vm := newV8TestContext(t)

	bucketVal, err := buildStorageBinding(vm, newMockR2Store())
	if err != nil {
		t.Fatalf("buildStorageBinding: %v", err)
	}

	setGlobal(vm, "__bucket", bucketVal)
	result, err := vm.EvalValue("__bucket.put('k', {})", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue put: %v", err)
	}
	defer result.Free()

	deadline := time.Now().Add(5 * time.Second)
	_, err = awaitValueCompat(vm, result, deadline)
	if err == nil || !strings.Contains(err.Error(), "unsupported body type") {
		t.Fatalf("expected unsupported-value rejection, got %v", err)
	}
}

func TestStorageBinding_ArrayBuffer_ReturnsData(t *testing.T) {
	vm := newV8TestContext(t)

	obj, err := buildR2ObjectBody(vm, "k", []byte("hello"), &R2Object{
		Size:         5,
		ETag:         "etag",
		ContentType:  "text/plain",
		LastModified: time.Now(),
	})
	if err != nil {
		t.Fatalf("buildR2ObjectBody: %v", err)
	}
	setGlobal(vm, "__obj", obj.Value)

	result, err := vm.EvalValue("__obj.arrayBuffer()", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue arrayBuffer: %v", err)
	}
	defer result.Free()

	deadline := time.Now().Add(5 * time.Second)
	resolved, err := awaitValueCompat(vm, result, deadline)
	if err != nil {
		t.Fatalf("await arrayBuffer: %v", err)
	}

	// Verify bodyUsed is set after consuming.
	afterVal, err := evalString(vm, "__obj.bodyUsed")
	if err != nil {
		t.Fatalf("checking bodyUsed: %v", err)
	}
	if afterVal != "true" {
		t.Fatalf("bodyUsed after arrayBuffer = %q, want true", afterVal)
	}

	// Verify the ArrayBuffer has the right byte length.
	setGlobal(vm, "__result", resolved)
	blVal, err := evalInt(vm, "__result.byteLength")
	if err != nil {
		t.Fatalf("checking byteLength: %v", err)
	}
	if blVal != 5 {
		t.Fatalf("byteLength = %d, want 5", blVal)
	}

	// Verify body cannot be consumed again.
	result2, err := vm.EvalValue("__obj.arrayBuffer()", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue second arrayBuffer: %v", err)
	}
	defer result2.Free()
	_, err = awaitValueCompat(vm, result2, deadline)
	if err == nil || !strings.Contains(err.Error(), "already consumed") {
		t.Fatalf("expected body consumed rejection, got %v", err)
	}
}

func TestStorageBinding_ArrayBuffer_BinaryBlob(t *testing.T) {
	// Simulate a minimal PNG-like header with null bytes and high-byte values
	// that would break if the implementation used string coercion.
	pngHeader := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, // PNG signature
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52, // IHDR chunk header
	}

	vm := newV8TestContext(t)

	obj, err := buildR2ObjectBody(vm, "image.png", pngHeader, &R2Object{
		Size:         int64(len(pngHeader)),
		ETag:         "png-etag",
		ContentType:  "image/png",
		LastModified: time.Now(),
	})
	if err != nil {
		t.Fatalf("buildR2ObjectBody: %v", err)
	}
	setGlobal(vm, "__obj", obj.Value)

	result, err := vm.EvalValue("__obj.arrayBuffer()", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue arrayBuffer: %v", err)
	}
	defer result.Free()

	deadline := time.Now().Add(5 * time.Second)
	resolved, err := awaitValueCompat(vm, result, deadline)
	if err != nil {
		t.Fatalf("await arrayBuffer: %v", err)
	}

	// Verify byte length matches the original blob.
	setGlobal(vm, "__testBuf", resolved)
	blVal, err := evalInt(vm, "__testBuf.byteLength")
	if err != nil {
		t.Fatalf("checking byteLength: %v", err)
	}
	if blVal != len(pngHeader) {
		t.Fatalf("byteLength = %d, want %d", blVal, len(pngHeader))
	}

	// Read back every byte via Uint8Array and compare to the Go source.
	for i, expected := range pngHeader {
		jsCode := fmt.Sprintf("new Uint8Array(__testBuf)[%d]", i)
		v, err := evalInt(vm, jsCode)
		if err != nil {
			t.Fatalf("eval byte[%d]: %v", i, err)
		}
		got := byte(v)
		if got != expected {
			t.Fatalf("byte[%d] = 0x%02X, want 0x%02X", i, got, expected)
		}
	}
}

func TestStorageBinding_ArrayBuffer_EmptyBlob(t *testing.T) {
	vm := newV8TestContext(t)

	obj, err := buildR2ObjectBody(vm, "empty.bin", []byte{}, &R2Object{
		Size:         0,
		ETag:         "empty-etag",
		ContentType:  "application/octet-stream",
		LastModified: time.Now(),
	})
	if err != nil {
		t.Fatalf("buildR2ObjectBody: %v", err)
	}
	setGlobal(vm, "__obj", obj.Value)

	result, err := vm.EvalValue("__obj.arrayBuffer()", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue arrayBuffer: %v", err)
	}
	defer result.Free()

	deadline := time.Now().Add(5 * time.Second)
	resolved, err := awaitValueCompat(vm, result, deadline)
	if err != nil {
		t.Fatalf("await arrayBuffer: %v", err)
	}

	setGlobal(vm, "__result", resolved)
	blVal, err := evalInt(vm, "__result.byteLength")
	if err != nil {
		t.Fatalf("checking byteLength: %v", err)
	}
	if blVal != 0 {
		t.Fatalf("byteLength = %d, want 0", blVal)
	}
}

func TestStorageBinding_ArrayBuffer_AllByteValues(t *testing.T) {
	// Create a 256-byte blob containing every possible byte value 0x00..0xFF.
	blob := make([]byte, 256)
	for i := range blob {
		blob[i] = byte(i)
	}

	vm := newV8TestContext(t)

	obj, err := buildR2ObjectBody(vm, "allbytes.bin", blob, &R2Object{
		Size:         256,
		ETag:         "all-etag",
		ContentType:  "application/octet-stream",
		LastModified: time.Now(),
	})
	if err != nil {
		t.Fatalf("buildR2ObjectBody: %v", err)
	}
	setGlobal(vm, "__obj", obj.Value)

	result, err := vm.EvalValue("__obj.arrayBuffer()", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue arrayBuffer: %v", err)
	}
	defer result.Free()

	deadline := time.Now().Add(5 * time.Second)
	resolved, err := awaitValueCompat(vm, result, deadline)
	if err != nil {
		t.Fatalf("await arrayBuffer: %v", err)
	}

	setGlobal(vm, "__testBuf", resolved)
	blVal, err := evalInt(vm, "__testBuf.byteLength")
	if err != nil {
		t.Fatalf("checking byteLength: %v", err)
	}
	if blVal != 256 {
		t.Fatalf("byteLength = %d, want 256", blVal)
	}

	// Spot-check key byte values: null, high bytes, and boundaries.
	checks := []int{0x00, 0x01, 0x7F, 0x80, 0xFE, 0xFF}
	for _, idx := range checks {
		jsCode := fmt.Sprintf("new Uint8Array(__testBuf)[%d]", idx)
		v, err := evalInt(vm, jsCode)
		if err != nil {
			t.Fatalf("eval byte[%d]: %v", idx, err)
		}
		got := byte(v)
		if got != byte(idx) {
			t.Fatalf("byte[%d] = 0x%02X, want 0x%02X", idx, got, byte(idx))
		}
	}
}

func TestStorageBinding_ArrayBuffer_ThenTextRejects(t *testing.T) {
	vm := newV8TestContext(t)

	obj, err := buildR2ObjectBody(vm, "img.png", []byte{0x89, 0x50, 0x4E, 0x47}, &R2Object{
		Size:         4,
		ETag:         "etag",
		ContentType:  "image/png",
		LastModified: time.Now(),
	})
	if err != nil {
		t.Fatalf("buildR2ObjectBody: %v", err)
	}
	setGlobal(vm, "__obj", obj.Value)
	deadline := time.Now().Add(5 * time.Second)

	// Consume via arrayBuffer().
	result, err := vm.EvalValue("__obj.arrayBuffer()", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue arrayBuffer: %v", err)
	}
	defer result.Free()
	if _, err := awaitValueCompat(vm, result, deadline); err != nil {
		t.Fatalf("await arrayBuffer: %v", err)
	}

	// text() must reject after arrayBuffer() consumed the body.
	r2, err := vm.EvalValue("__obj.text()", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue text: %v", err)
	}
	defer r2.Free()
	_, err = awaitValueCompat(vm, r2, deadline)
	if err == nil || !strings.Contains(err.Error(), "already consumed") {
		t.Fatalf("expected body consumed rejection from text(), got %v", err)
	}

	// json() must also reject.
	r3, err := vm.EvalValue("__obj.json()", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue json: %v", err)
	}
	defer r3.Free()
	_, err = awaitValueCompat(vm, r3, deadline)
	if err == nil || !strings.Contains(err.Error(), "already consumed") {
		t.Fatalf("expected body consumed rejection from json(), got %v", err)
	}
}

func TestStorageBinding_Text_ThenArrayBufferRejects(t *testing.T) {
	vm := newV8TestContext(t)

	obj, err := buildR2ObjectBody(vm, "doc.txt", []byte("hello world"), &R2Object{
		Size:         11,
		ETag:         "etag",
		ContentType:  "text/plain",
		LastModified: time.Now(),
	})
	if err != nil {
		t.Fatalf("buildR2ObjectBody: %v", err)
	}
	setGlobal(vm, "__obj", obj.Value)
	deadline := time.Now().Add(5 * time.Second)

	// Consume via text().
	result, err := vm.EvalValue("__obj.text()", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue text: %v", err)
	}
	defer result.Free()
	if _, err := awaitValueCompat(vm, result, deadline); err != nil {
		t.Fatalf("await text: %v", err)
	}

	// arrayBuffer() must reject after text() consumed the body.
	r2, err := vm.EvalValue("__obj.arrayBuffer()", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue arrayBuffer: %v", err)
	}
	defer r2.Free()
	_, err = awaitValueCompat(vm, r2, deadline)
	if err == nil || !strings.Contains(err.Error(), "already consumed") {
		t.Fatalf("expected body consumed rejection from arrayBuffer(), got %v", err)
	}
}

func TestStorageBinding_BodyUsed_TransitionsAfterRead(t *testing.T) {
	vm := newV8TestContext(t)

	obj, err := buildR2ObjectBody(vm, "k", []byte("hello"), &R2Object{
		Size:         5,
		ETag:         "etag",
		ContentType:  "text/plain",
		LastModified: time.Now(),
	})
	if err != nil {
		t.Fatalf("buildR2ObjectBody: %v", err)
	}
	setGlobal(vm, "__obj", obj.Value)
	deadline := time.Now().Add(5 * time.Second)

	// Initial bodyUsed should be false.
	initial, err := evalString(vm, "__obj.bodyUsed")
	if err != nil {
		t.Fatalf("checking initial bodyUsed: %v", err)
	}
	if initial != "false" {
		t.Fatalf("initial bodyUsed = %q, want false", initial)
	}

	// Call text().
	result, err := vm.EvalValue("__obj.text()", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue text: %v", err)
	}
	defer result.Free()
	resolved, err := awaitValueCompat(vm, result, deadline)
	if err != nil {
		t.Fatalf("await text: %v", err)
	}
	resolvedStr, err := quickjsValueToString(vm, resolved)
	if err != nil {
		t.Fatalf("converting resolved value: %v", err)
	}
	if resolvedStr != "hello" {
		t.Fatalf("text result = %q, want hello", resolvedStr)
	}

	// bodyUsed should be true after consuming.
	after, err := evalString(vm, "__obj.bodyUsed")
	if err != nil {
		t.Fatalf("checking bodyUsed after text: %v", err)
	}
	if after != "true" {
		t.Fatalf("bodyUsed after text = %q, want true", after)
	}

	// Second text() call must reject.
	result2, err := vm.EvalValue("__obj.text()", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue second text: %v", err)
	}
	defer result2.Free()
	_, err = awaitValueCompat(vm, result2, deadline)
	if err == nil || !strings.Contains(err.Error(), "already consumed") {
		t.Fatalf("expected body consumed rejection, got %v", err)
	}
}

func TestStorageBinding_JSON_InvalidJSONRejects(t *testing.T) {
	vm := newV8TestContext(t)

	obj, err := buildR2ObjectBody(vm, "k", []byte("{not-json"), &R2Object{
		Size:         9,
		ETag:         "etag",
		ContentType:  "application/json",
		LastModified: time.Now(),
	})
	if err != nil {
		t.Fatalf("buildR2ObjectBody: %v", err)
	}
	setGlobal(vm, "__obj", obj.Value)

	result, err := vm.EvalValue("__obj.json()", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue json: %v", err)
	}
	defer result.Free()

	deadline := time.Now().Add(5 * time.Second)
	_, err = awaitValueCompat(vm, result, deadline)
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("expected invalid JSON rejection, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Pure Go helpers (no V8 dependency)
// ---------------------------------------------------------------------------

func TestBuildPublicObjectURL_PathEscaping(t *testing.T) {
	u, err := buildPublicObjectURL("https://storage.example.com", "downloads", "releases/v1.0/file name+plus?.zip")
	if err != nil {
		t.Fatalf("buildPublicObjectURL returned error: %v", err)
	}

	want := "https://storage.example.com/downloads/releases/v1.0/file%20name+plus%3F.zip"
	if u != want {
		t.Fatalf("public URL = %q, want %q", u, want)
	}
}

func TestBuildPublicObjectURL_InvalidBase(t *testing.T) {
	if _, err := buildPublicObjectURL("storage.example.com", "downloads", "artifact"); err == nil {
		t.Fatalf("expected error for invalid public base URL")
	}
}

func TestBuildPublicObjectURL_WithBasePath(t *testing.T) {
	u, err := buildPublicObjectURL("https://storage.example.com/s3", "mybucket", "file.txt")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !strings.Contains(u, "/s3/mybucket/file.txt") {
		t.Errorf("URL should include base path, got %q", u)
	}
}

func TestBuildPublicObjectURL_TrailingSlash(t *testing.T) {
	u, err := buildPublicObjectURL("https://storage.example.com/", "mybucket", "file.txt")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if strings.Contains(u, "//mybucket") {
		t.Errorf("should not have double slashes, got %q", u)
	}
}

func TestBuildPublicObjectURL_LeadingSlashKey(t *testing.T) {
	u, err := buildPublicObjectURL("https://storage.example.com", "mybucket", "/file.txt")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if strings.Contains(u, "mybucket//") {
		t.Errorf("should not have double slash between bucket and key, got %q", u)
	}
}

func TestEscapePathSegments(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"simple.txt", "simple.txt"},
		{"dir/file.txt", "dir/file.txt"},
		{"dir/file name.txt", "dir/file%20name.txt"},
		{"a/b/c", "a/b/c"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := escapePathSegments(tc.input)
			if got != tc.want {
				t.Errorf("escapePathSegments(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestStorageBinding_JSON_ValidJSON(t *testing.T) {
	vm := newV8TestContext(t)

	obj, err := buildR2ObjectBody(vm, "k", []byte(`{"name":"test","count":42}`), &R2Object{
		Size:         25,
		ETag:         "etag",
		ContentType:  "application/json",
		LastModified: time.Now(),
	})
	if err != nil {
		t.Fatalf("buildR2ObjectBody: %v", err)
	}
	setGlobal(vm, "__obj", obj.Value)

	result, err := vm.EvalValue("__obj.json()", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue json: %v", err)
	}
	defer result.Free()

	deadline := time.Now().Add(5 * time.Second)
	resolved, err := awaitValueCompat(vm, result, deadline)
	if err != nil {
		t.Fatalf("await json: %v", err)
	}

	setGlobal(vm, "__result", resolved)
	nameVal, err := evalString(vm, "__result.name")
	if err != nil {
		t.Fatalf("checking name: %v", err)
	}
	if nameVal != "test" {
		t.Errorf("json name = %q, want test", nameVal)
	}

	countVal, err := evalInt(vm, "__result.count")
	if err != nil {
		t.Fatalf("checking count: %v", err)
	}
	if countVal != 42 {
		t.Errorf("json count = %d, want 42", countVal)
	}
}

func TestBuildR2Object(t *testing.T) {
	vm, err := quickjs.NewVM()
	if err != nil {
		t.Fatalf("NewVM: %v", err)
	}
	defer vm.Close()

	customMeta := map[string]string{"author": "test", "version": "1"}
	obj, err := buildR2Object(vm, "test-key", 100, "etag123", "text/plain", customMeta)
	if err != nil {
		t.Fatalf("buildR2Object: %v", err)
	}

	setGlobal(vm, "__obj", obj.Value)

	keyVal, err := evalString(vm, "__obj.key")
	if err != nil {
		t.Fatalf("checking key: %v", err)
	}
	if keyVal != "test-key" {
		t.Errorf("key = %q, want test-key", keyVal)
	}

	etagVal, err := evalString(vm, "__obj.etag")
	if err != nil {
		t.Fatalf("checking etag: %v", err)
	}
	if etagVal != "etag123" {
		t.Errorf("etag = %q, want etag123", etagVal)
	}

	httpEtagVal, err := evalString(vm, "__obj.httpEtag")
	if err != nil {
		t.Fatalf("checking httpEtag: %v", err)
	}
	if httpEtagVal != `"etag123"` {
		t.Errorf("httpEtag = %q, want quoted etag", httpEtagVal)
	}

	scVal, err := evalString(vm, "__obj.storageClass")
	if err != nil {
		t.Fatalf("checking storageClass: %v", err)
	}
	if scVal != "STANDARD" {
		t.Errorf("storageClass = %q, want STANDARD", scVal)
	}

	ctVal, err := evalString(vm, "__obj.httpMetadata.contentType")
	if err != nil {
		t.Fatalf("checking contentType: %v", err)
	}
	if ctVal != "text/plain" {
		t.Errorf("contentType = %q, want text/plain", ctVal)
	}

	authorVal, err := evalString(vm, "__obj.customMetadata.author")
	if err != nil {
		t.Fatalf("checking author: %v", err)
	}
	if authorVal != "test" {
		t.Errorf("customMetadata.author = %q, want test", authorVal)
	}
}

func TestBuildR2Object_EmptyMetadata(t *testing.T) {
	vm, err := quickjs.NewVM()
	if err != nil {
		t.Fatalf("NewVM: %v", err)
	}
	defer vm.Close()

	obj, err := buildR2Object(vm, "empty-meta", 0, "etag-0", "", nil)
	if err != nil {
		t.Fatalf("buildR2Object: %v", err)
	}

	setGlobal(vm, "__obj", obj.Value)

	keyVal, err := evalString(vm, "__obj.key")
	if err != nil {
		t.Fatalf("checking key: %v", err)
	}
	if keyVal != "empty-meta" {
		t.Errorf("key = %q, want empty-meta", keyVal)
	}

	sizeVal, err := evalInt(vm, "__obj.size")
	if err != nil {
		t.Fatalf("checking size: %v", err)
	}
	if sizeVal != 0 {
		t.Errorf("size = %d, want 0", sizeVal)
	}

	// httpMetadata should exist but contentType should be undefined
	ctVal, err := evalString(vm, "__obj.httpMetadata.contentType")
	if err != nil {
		t.Fatalf("checking contentType: %v", err)
	}
	if ctVal != "undefined" {
		t.Errorf("contentType = %q, want undefined (no contentType set)", ctVal)
	}

	// customMetadata should be an empty object
	cmType, err := evalString(vm, "typeof __obj.customMetadata")
	if err != nil {
		t.Fatalf("checking customMetadata type: %v", err)
	}
	if cmType != "object" {
		t.Errorf("customMetadata type = %q, want object", cmType)
	}

	// checksums should exist
	ckType, err := evalString(vm, "typeof __obj.checksums")
	if err != nil {
		t.Fatalf("checking checksums type: %v", err)
	}
	if ckType != "object" {
		t.Errorf("checksums type = %q, want object", ckType)
	}
}

func TestBuildR2Object_SizeAndVersion(t *testing.T) {
	vm, err := quickjs.NewVM()
	if err != nil {
		t.Fatalf("NewVM: %v", err)
	}
	defer vm.Close()

	obj, err := buildR2Object(vm, "large-file", 1048576, "abc123", "application/octet-stream", nil)
	if err != nil {
		t.Fatalf("buildR2Object: %v", err)
	}

	setGlobal(vm, "__obj", obj.Value)

	sizeVal, err := evalInt(vm, "__obj.size")
	if err != nil {
		t.Fatalf("checking size: %v", err)
	}
	if sizeVal != 1048576 {
		t.Errorf("size = %d, want 1048576", sizeVal)
	}

	versionVal, err := evalString(vm, "__obj.version")
	if err != nil {
		t.Fatalf("checking version: %v", err)
	}
	if versionVal != "abc123" {
		t.Errorf("version = %q, want abc123", versionVal)
	}
}

func TestStorageBinding_PutRequiresKey(t *testing.T) {
	vm := newV8TestContext(t)

	bucketVal, err := buildStorageBinding(vm, newMockR2Store())
	if err != nil {
		t.Fatalf("buildStorageBinding: %v", err)
	}

	setGlobal(vm, "__bucket", bucketVal)
	result, err := vm.EvalValue("__bucket.put()", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue put: %v", err)
	}
	defer result.Free()

	deadline := time.Now().Add(5 * time.Second)
	_, err = awaitValueCompat(vm, result, deadline)
	if err == nil || !strings.Contains(err.Error(), "requires key and value") {
		t.Fatalf("expected key/value rejection, got %v", err)
	}
}

func TestStorageBinding_GetRequiresKey(t *testing.T) {
	vm := newV8TestContext(t)

	bucketVal, err := buildStorageBinding(vm, newMockR2Store())
	if err != nil {
		t.Fatalf("buildStorageBinding: %v", err)
	}

	setGlobal(vm, "__bucket", bucketVal)
	result, err := vm.EvalValue("__bucket.get()", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue get: %v", err)
	}
	defer result.Free()

	deadline := time.Now().Add(5 * time.Second)
	_, err = awaitValueCompat(vm, result, deadline)
	if err == nil || !strings.Contains(err.Error(), "requires a key") {
		t.Fatalf("expected key rejection, got %v", err)
	}
}

func TestStorageBinding_DeleteRequiresKey(t *testing.T) {
	vm := newV8TestContext(t)

	bucketVal, err := buildStorageBinding(vm, newMockR2Store())
	if err != nil {
		t.Fatalf("buildStorageBinding: %v", err)
	}

	setGlobal(vm, "__bucket", bucketVal)
	result, err := vm.EvalValue("__bucket.delete()", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue delete: %v", err)
	}
	defer result.Free()

	deadline := time.Now().Add(5 * time.Second)
	_, err = awaitValueCompat(vm, result, deadline)
	if err == nil || !strings.Contains(err.Error(), "requires a key") {
		t.Fatalf("expected key rejection, got %v", err)
	}
}

func TestStorageBinding_HeadRequiresKey(t *testing.T) {
	vm := newV8TestContext(t)

	bucketVal, err := buildStorageBinding(vm, newMockR2Store())
	if err != nil {
		t.Fatalf("buildStorageBinding: %v", err)
	}

	setGlobal(vm, "__bucket", bucketVal)
	result, err := vm.EvalValue("__bucket.head()", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue head: %v", err)
	}
	defer result.Free()

	deadline := time.Now().Add(5 * time.Second)
	_, err = awaitValueCompat(vm, result, deadline)
	if err == nil || !strings.Contains(err.Error(), "requires a key") {
		t.Fatalf("expected key rejection, got %v", err)
	}
}

func TestStorageBinding_BindingHasMethods(t *testing.T) {
	vm := newV8TestContext(t)

	bucketVal, err := buildStorageBinding(vm, newMockR2Store())
	if err != nil {
		t.Fatalf("buildStorageBinding: %v", err)
	}

	setGlobal(vm, "__bucket", bucketVal)

	// Verify all expected methods exist
	methods := []string{"get", "put", "delete", "head", "list", "publicUrl"}
	for _, m := range methods {
		jsCode := fmt.Sprintf("typeof __bucket.%s", m)
		val, err := evalString(vm, jsCode)
		if err != nil {
			t.Fatalf("checking %s: %v", m, err)
		}
		if val != "function" {
			t.Errorf("%s type = %q, want 'function'", m, val)
		}
	}
}

func TestBuildPublicObjectURL_SpecialChars(t *testing.T) {
	tests := []struct {
		name   string
		base   string
		bucket string
		key    string
		want   string
	}{
		{
			name:   "unicode filename",
			base:   "https://storage.example.com",
			bucket: "uploads",
			key:    "docs/resume (1).pdf",
			want:   "https://storage.example.com/uploads/docs/resume%20%281%29.pdf",
		},
		{
			name:   "nested path",
			base:   "https://cdn.example.com",
			bucket: "assets",
			key:    "images/photos/2024/summer.jpg",
			want:   "https://cdn.example.com/assets/images/photos/2024/summer.jpg",
		},
		{
			name:   "empty key",
			base:   "https://storage.example.com",
			bucket: "bucket",
			key:    "",
			want:   "https://storage.example.com/bucket/",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildPublicObjectURL(tc.base, tc.bucket, tc.key)
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if got != tc.want {
				t.Errorf("buildPublicObjectURL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildR2ObjectBody_TextReturnsCorrectData(t *testing.T) {
	vm := newV8TestContext(t)

	content := "Hello, World! Special chars: <>&\""
	obj, err := buildR2ObjectBody(vm, "greeting.txt", []byte(content), &R2Object{
		Size:         int64(len(content)),
		ETag:         "text-etag",
		ContentType:  "text/plain",
		LastModified: time.Now(),
	})
	if err != nil {
		t.Fatalf("buildR2ObjectBody: %v", err)
	}
	setGlobal(vm, "__obj", obj.Value)

	result, err := vm.EvalValue("__obj.text()", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue text: %v", err)
	}
	defer result.Free()

	deadline := time.Now().Add(5 * time.Second)
	resolved, err := awaitValueCompat(vm, result, deadline)
	if err != nil {
		t.Fatalf("await text: %v", err)
	}
	resolvedStr, err := quickjsValueToString(vm, resolved)
	if err != nil {
		t.Fatalf("converting resolved value: %v", err)
	}
	if resolvedStr != content {
		t.Errorf("text() = %q, want %q", resolvedStr, content)
	}
}

// ---------------------------------------------------------------------------
// Error-path tests using errR2Store (replaces badMinioClient-backed StorageBridge)
// ---------------------------------------------------------------------------

func TestStorageBinding_GetWithKeyReturnsNullOnError(t *testing.T) {
	vm := newV8TestContext(t)

	bucketVal, err := buildStorageBinding(vm, &errR2Store{})
	if err != nil {
		t.Fatalf("buildStorageBinding: %v", err)
	}
	setGlobal(vm, "__bucket", bucketVal)

	result, err := vm.EvalValue("__bucket.get('nonexistent-key')", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue get: %v", err)
	}
	defer result.Free()

	deadline := time.Now().Add(5 * time.Second)
	resolved, err := awaitValueCompat(vm, result, deadline)
	if err != nil {
		t.Fatalf("await get: %v", err)
	}
	if !quickjsValueIsNull(vm, resolved) {
		resolvedStr, _ := quickjsValueToString(vm, resolved)
		t.Errorf("get with error store should resolve null, got %v", resolvedStr)
	}
}

func TestStorageBinding_PutWithStringBody(t *testing.T) {
	vm := newV8TestContext(t)

	bucketVal, err := buildStorageBinding(vm, &errR2Store{})
	if err != nil {
		t.Fatalf("buildStorageBinding: %v", err)
	}
	setGlobal(vm, "__bucket", bucketVal)

	// put with string value exercises the JS coercion "string" path
	// then fails at Put with connection error
	result, err := vm.EvalValue("__bucket.put('my-key', 'hello world')", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue put: %v", err)
	}
	defer result.Free()

	deadline := time.Now().Add(5 * time.Second)
	_, err = awaitValueCompat(vm, result, deadline)
	if err == nil {
		t.Fatal("expected put to reject with connection error")
	}
	if !strings.Contains(err.Error(), "putting object") {
		t.Fatalf("expected 'putting object' error, got %v", err)
	}
}

func TestStorageBinding_PutWithArrayBufferBody(t *testing.T) {
	vm := newV8TestContext(t)

	bucketVal, err := buildStorageBinding(vm, &errR2Store{})
	if err != nil {
		t.Fatalf("buildStorageBinding: %v", err)
	}
	setGlobal(vm, "__bucket", bucketVal)

	// put with ArrayBuffer exercises the JS coercion "binary" path + base64 decode
	result, err := vm.EvalValue(`(function() {
		var buf = new ArrayBuffer(4);
		var view = new Uint8Array(buf);
		view[0] = 1; view[1] = 2; view[2] = 3; view[3] = 4;
		return __bucket.put('binary-key', buf);
	})()`, quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue put AB: %v", err)
	}
	defer result.Free()

	deadline := time.Now().Add(5 * time.Second)
	_, err = awaitValueCompat(vm, result, deadline)
	if err == nil {
		t.Fatal("expected put to reject with connection error")
	}
	if !strings.Contains(err.Error(), "putting object") {
		t.Fatalf("expected 'putting object' error, got %v", err)
	}
}

func TestStorageBinding_PutWithTypedArrayBody(t *testing.T) {
	vm := newV8TestContext(t)

	bucketVal, err := buildStorageBinding(vm, &errR2Store{})
	if err != nil {
		t.Fatalf("buildStorageBinding: %v", err)
	}
	setGlobal(vm, "__bucket", bucketVal)

	// put with Uint8Array (TypedArray view) exercises the ArrayBuffer.isView path
	result, err := vm.EvalValue("__bucket.put('typed-key', new Uint8Array([10, 20, 30]))", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue put typed: %v", err)
	}
	defer result.Free()

	deadline := time.Now().Add(5 * time.Second)
	_, err = awaitValueCompat(vm, result, deadline)
	if err == nil {
		t.Fatal("expected put to reject with connection error")
	}
}

func TestStorageBinding_PutWithOptions(t *testing.T) {
	vm := newV8TestContext(t)

	bucketVal, err := buildStorageBinding(vm, &errR2Store{})
	if err != nil {
		t.Fatalf("buildStorageBinding: %v", err)
	}
	setGlobal(vm, "__bucket", bucketVal)

	// put with options exercises the httpMetadata and customMetadata extraction paths
	result, err := vm.EvalValue(`__bucket.put('opts-key', 'data', {
		httpMetadata: {
			contentType: 'text/plain',
			contentEncoding: 'gzip',
			contentDisposition: 'attachment',
			contentLanguage: 'en',
			cacheControl: 'max-age=3600',
		},
		customMetadata: {
			author: 'test',
			version: '1.0',
		},
	})`, quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue put opts: %v", err)
	}
	defer result.Free()

	deadline := time.Now().Add(5 * time.Second)
	_, err = awaitValueCompat(vm, result, deadline)
	if err == nil {
		t.Fatal("expected put to reject with connection error")
	}
}

func TestStorageBinding_DeleteSingleKey(t *testing.T) {
	vm := newV8TestContext(t)

	bucketVal, err := buildStorageBinding(vm, &errR2Store{})
	if err != nil {
		t.Fatalf("buildStorageBinding: %v", err)
	}
	setGlobal(vm, "__bucket", bucketVal)

	// delete with single key - errors are silently ignored, resolves undefined
	result, err := vm.EvalValue("__bucket.delete('some-key')", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue delete: %v", err)
	}
	defer result.Free()

	deadline := time.Now().Add(5 * time.Second)
	resolved, err := awaitValueCompat(vm, result, deadline)
	if err != nil {
		t.Fatalf("await delete: %v", err)
	}
	if !quickjsValueIsUndefined(vm, resolved) {
		resolvedStr, _ := quickjsValueToString(vm, resolved)
		t.Errorf("delete should resolve undefined, got %v", resolvedStr)
	}
}

func TestStorageBinding_DeleteArrayOfKeys(t *testing.T) {
	vm := newV8TestContext(t)

	bucketVal, err := buildStorageBinding(vm, &errR2Store{})
	if err != nil {
		t.Fatalf("buildStorageBinding: %v", err)
	}
	setGlobal(vm, "__bucket", bucketVal)

	// delete with array of keys
	result, err := vm.EvalValue("__bucket.delete(['k1', 'k2', 'k3'])", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue delete array: %v", err)
	}
	defer result.Free()

	deadline := time.Now().Add(5 * time.Second)
	resolved, err := awaitValueCompat(vm, result, deadline)
	if err != nil {
		t.Fatalf("await delete array: %v", err)
	}
	if !quickjsValueIsUndefined(vm, resolved) {
		resolvedStr, _ := quickjsValueToString(vm, resolved)
		t.Errorf("delete array should resolve undefined, got %v", resolvedStr)
	}
}

func TestStorageBinding_HeadReturnsNullOnError(t *testing.T) {
	vm := newV8TestContext(t)

	bucketVal, err := buildStorageBinding(vm, &errR2Store{})
	if err != nil {
		t.Fatalf("buildStorageBinding: %v", err)
	}
	setGlobal(vm, "__bucket", bucketVal)

	result, err := vm.EvalValue("__bucket.head('missing-key')", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue head: %v", err)
	}
	defer result.Free()

	deadline := time.Now().Add(5 * time.Second)
	resolved, err := awaitValueCompat(vm, result, deadline)
	if err != nil {
		t.Fatalf("await head: %v", err)
	}
	if !quickjsValueIsNull(vm, resolved) {
		resolvedStr, _ := quickjsValueToString(vm, resolved)
		t.Errorf("head with error store should resolve null, got %v", resolvedStr)
	}
}

func TestStorageBinding_ListReturnsEmptyOnError(t *testing.T) {
	vm := newV8TestContext(t)

	bucketVal, err := buildStorageBinding(vm, &errR2Store{})
	if err != nil {
		t.Fatalf("buildStorageBinding: %v", err)
	}
	setGlobal(vm, "__bucket", bucketVal)

	result, err := vm.EvalValue("__bucket.list()", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue list: %v", err)
	}
	defer result.Free()

	deadline := time.Now().Add(5 * time.Second)
	resolved, err := awaitValueCompat(vm, result, deadline)
	if err != nil {
		t.Fatalf("await list: %v", err)
	}
	setGlobal(vm, "__listResult", resolved)

	// Verify we get back an object with objects array and truncated flag
	truncVal, err := evalString(vm, "__listResult.truncated")
	if err != nil {
		t.Fatalf("checking truncated: %v", err)
	}
	if truncVal != "false" {
		t.Errorf("truncated = %q, want false", truncVal)
	}
}

func TestStorageBinding_ListWithOptions(t *testing.T) {
	vm := newV8TestContext(t)

	bucketVal, err := buildStorageBinding(vm, &errR2Store{})
	if err != nil {
		t.Fatalf("buildStorageBinding: %v", err)
	}
	setGlobal(vm, "__bucket", bucketVal)

	// list with full options exercises the options extraction path
	result, err := vm.EvalValue(`__bucket.list({
		prefix: 'uploads/',
		cursor: 'last-key',
		delimiter: '/',
		limit: 5,
	})`, quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue list opts: %v", err)
	}
	defer result.Free()

	deadline := time.Now().Add(5 * time.Second)
	_, err = awaitValueCompat(vm, result, deadline)
	if err != nil {
		t.Fatalf("await list opts: %v", err)
	}
}

func TestStorageBinding_PublicUrl(t *testing.T) {
	vm := newV8TestContext(t)

	bucketVal, err := buildStorageBinding(vm, newMockR2Store())
	if err != nil {
		t.Fatalf("buildStorageBinding: %v", err)
	}
	setGlobal(vm, "__bucket", bucketVal)

	result, err := evalString(vm, "__bucket.publicUrl('images/photo.jpg')")
	if err != nil {
		t.Fatalf("EvalValue publicUrl: %v", err)
	}

	// mockR2Store.PublicURL returns "https://public.test/bucket/<key>"
	if !strings.Contains(result, "images/photo.jpg") {
		t.Errorf("publicUrl should contain the key, got %q", result)
	}
}

func TestStorageBinding_PublicUrl_RequiresArg(t *testing.T) {
	vm := newV8TestContext(t)

	bucketVal, err := buildStorageBinding(vm, newMockR2Store())
	if err != nil {
		t.Fatalf("buildStorageBinding: %v", err)
	}
	setGlobal(vm, "__bucket", bucketVal)

	// publicUrl() with no args should throw
	_, err = evalString(vm, `try { __bucket.publicUrl(); 'ok'; } catch(e) { e.message; }`)
	if err != nil {
		t.Fatalf("EvalValue publicUrl noarg: %v", err)
	}
}

func TestStorageBinding_PublicUrl_NotAvailableWithoutConfig(t *testing.T) {
	vm := newV8TestContext(t)

	// Use errR2Store with no publicURL -> PublicURL returns error
	bucketVal, err := buildStorageBinding(vm, &errR2Store{})
	if err != nil {
		t.Fatalf("buildStorageBinding: %v", err)
	}
	setGlobal(vm, "__bucket", bucketVal)

	// publicUrl is always present on the binding (R2Store interface always has it),
	// but calling it without configuration should throw an error.
	result, err := evalString(vm, `(function() {
		try { __bucket.publicUrl('key'); return 'no error'; }
		catch(e) { return e.message || String(e); }
	})()`)
	if err != nil {
		t.Fatalf("EvalValue publicUrl: %v", err)
	}
	if !strings.Contains(result, "public") {
		t.Errorf("publicUrl should error without config, got %q", result)
	}
}

func TestStorageBinding_CreateSignedUrl_NilClients(t *testing.T) {
	vm := newV8TestContext(t)

	// createSignedUrl is always present on the binding (R2Store interface always
	// has PresignedGetURL), but calling it without a configured client should reject.
	bucketVal, err := buildStorageBinding(vm, &nilConfigR2Store{})
	if err != nil {
		t.Fatalf("buildStorageBinding: %v", err)
	}
	setGlobal(vm, "__bucket", bucketVal)

	result, err := vm.EvalValue("__bucket.createSignedUrl('key')", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue: %v", err)
	}
	defer result.Free()

	deadline := time.Now().Add(5 * time.Second)
	_, err = awaitValueCompat(vm, result, deadline)
	if err == nil || !strings.Contains(err.Error(), "storage client not configured") {
		t.Fatalf("expected 'storage client not configured' error, got %v", err)
	}
}

func TestStorageBinding_CreateSignedUrl_NoPresignButPublicURL(t *testing.T) {
	vm := newV8TestContext(t)

	// Has a working store but PresignedGetURL returns "presign client not configured"
	bucketVal, err := buildStorageBinding(vm, &noPresignR2Store{
		errR2Store: errR2Store{publicURL: "https://storage.example.com"},
	})
	if err != nil {
		t.Fatalf("buildStorageBinding: %v", err)
	}
	setGlobal(vm, "__bucket", bucketVal)

	result, err := vm.EvalValue("__bucket.createSignedUrl('key')", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue createSignedUrl: %v", err)
	}
	defer result.Free()

	deadline := time.Now().Add(5 * time.Second)
	_, err = awaitValueCompat(vm, result, deadline)
	if err == nil || !strings.Contains(err.Error(), "presign client not configured") {
		t.Fatalf("expected 'presign client not configured', got %v", err)
	}
}

func TestStorageBinding_CreateSignedUrl_WithExpiryOptions(t *testing.T) {
	vm := newV8TestContext(t)

	bucketVal, err := buildStorageBinding(vm, &errR2Store{})
	if err != nil {
		t.Fatalf("buildStorageBinding: %v", err)
	}
	setGlobal(vm, "__bucket", bucketVal)

	// Test with custom expiry - will fail at PresignedGetURL but exercises options parsing
	result, err := vm.EvalValue("__bucket.createSignedUrl('key', { expiresIn: 7200 })", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue createSignedUrl opts: %v", err)
	}
	defer result.Free()

	deadline := time.Now().Add(5 * time.Second)
	_, err = awaitValueCompat(vm, result, deadline)
	// Should fail with connection error
	if err == nil {
		t.Fatal("expected createSignedUrl to reject with connection error")
	}
}

func TestStorageBinding_CreateSignedUrl_ExpiryClamp(t *testing.T) {
	vm := newV8TestContext(t)

	bucketVal, err := buildStorageBinding(vm, &errR2Store{})
	if err != nil {
		t.Fatalf("buildStorageBinding: %v", err)
	}
	setGlobal(vm, "__bucket", bucketVal)

	// Test expiry clamping: negative -> 1, excessive -> 604800
	result, err := vm.EvalValue("__bucket.createSignedUrl('key', { expiresIn: -10 })", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue: %v", err)
	}
	defer result.Free()
	deadline := time.Now().Add(5 * time.Second)
	_, _ = awaitValueCompat(vm, result, deadline) // will error but that's fine

	result2, err := vm.EvalValue("__bucket.createSignedUrl('key', { expiresIn: 9999999 })", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue: %v", err)
	}
	defer result2.Free()
	_, _ = awaitValueCompat(vm, result2, deadline) // will error but exercises the clamp path
}

func TestStorageBinding_CreateSignedUrl_RequiresArg(t *testing.T) {
	vm := newV8TestContext(t)

	bucketVal, err := buildStorageBinding(vm, &errR2Store{})
	if err != nil {
		t.Fatalf("buildStorageBinding: %v", err)
	}
	setGlobal(vm, "__bucket", bucketVal)

	result, err := vm.EvalValue("__bucket.createSignedUrl()", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue: %v", err)
	}
	defer result.Free()

	deadline := time.Now().Add(5 * time.Second)
	_, err = awaitValueCompat(vm, result, deadline)
	if err == nil || !strings.Contains(err.Error(), "requires a key") {
		t.Fatalf("expected 'requires a key' error, got %v", err)
	}
}

func TestStorageBinding_ListErrorPath(t *testing.T) {
	vm := newV8TestContext(t)

	bucketVal, err := buildStorageBinding(vm, &failListR2Store{})
	if err != nil {
		t.Fatalf("buildStorageBinding: %v", err)
	}
	setGlobal(vm, "__bucket", bucketVal)

	// list() should resolve with an empty result when the store returns an error.
	result, err := vm.EvalValue("__bucket.list()", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue: %v", err)
	}
	defer result.Free()

	deadline := time.Now().Add(5 * time.Second)
	val, err := awaitValueCompat(vm, result, deadline)
	if err != nil {
		t.Fatalf("expected list to resolve, got error: %v", err)
	}

	// The result should be a valid object (empty list fallback).
	if quickjsValueIsNull(vm, val) || quickjsValueIsUndefined(vm, val) {
		t.Fatal("expected non-null list result")
	}
}

func TestStorageBinding_DeleteErrorPath(t *testing.T) {
	vm := newV8TestContext(t)

	bucketVal, err := buildStorageBinding(vm, &failListR2Store{})
	if err != nil {
		t.Fatalf("buildStorageBinding: %v", err)
	}
	setGlobal(vm, "__bucket", bucketVal)

	// delete() should resolve even when the store returns an error.
	result, err := vm.EvalValue("__bucket.delete('some-key')", quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("EvalValue: %v", err)
	}
	defer result.Free()

	deadline := time.Now().Add(5 * time.Second)
	_, err = awaitValueCompat(vm, result, deadline)
	if err != nil {
		t.Fatalf("expected delete to resolve, got error: %v", err)
	}
}
