package worker

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	v8 "github.com/tommie/v8go"
)

const maxObjectSize = 128 * 1024 * 1024 // 128 MB

// buildStorageBinding creates a JS object with R2-compatible get/put/delete/head/list
// methods backed by the given R2Store.
//
// All operations run synchronously on the JS thread (same rationale as KV bindings
// in kv.go). Minio-go calls are HTTP to localhost SeaweedFS and respond quickly.
func buildStorageBinding(iso *v8.Isolate, ctx *v8.Context, store R2Store) (*v8.Value, error) {
	bucket, err := newJSObject(iso, ctx)
	if err != nil {
		return nil, fmt.Errorf("creating storage binding object: %w", err)
	}

	// get(key) -> Promise<R2ObjectBody|null>
	_ = bucket.Set("get", v8.NewFunctionTemplate(iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
		resolver, _ := v8.NewPromiseResolver(ctx)
		args := info.Args()
		if len(args) == 0 {
			errVal, _ := v8.NewValue(iso, "BUCKET.get requires a key argument")
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}
		key := args[0].String()

		data, r2obj, err := store.Get(key)
		if err != nil || r2obj == nil {
			resolver.Resolve(v8.Null(iso))
			return resolver.GetPromise().Value
		}

		if r2obj.Size > int64(maxObjectSize) {
			errVal, _ := v8.NewValue(iso, fmt.Sprintf("object too large: %d bytes (max %d)", r2obj.Size, maxObjectSize))
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}

		r2body, err := buildR2ObjectBody(iso, ctx, key, data, r2obj)
		if err != nil {
			errVal, _ := v8.NewValue(iso, err.Error())
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}
		resolver.Resolve(r2body.Value)
		return resolver.GetPromise().Value
	}).GetFunction(ctx))

	// put(key, value, opts?) -> Promise<R2Object>
	_ = bucket.Set("put", v8.NewFunctionTemplate(iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
		resolver, _ := v8.NewPromiseResolver(ctx)
		args := info.Args()
		if len(args) < 2 {
			errVal, _ := v8.NewValue(iso, "BUCKET.put requires key and value arguments")
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}
		key := args[0].String()

		// Coerce body to bytes via JS. Binary data (ArrayBuffer/TypedArray) is
		// copied into a SharedArrayBuffer so Go can read the backing store
		// directly — no base64 encoding or string serialisation needed.
		_ = ctx.Global().Set("__tmp_put_body", args[1])
		coerceResult, jsErr := ctx.RunScript(`(function() {
			var v = globalThis.__tmp_put_body;
			delete globalThis.__tmp_put_body;
			if (typeof v === 'string') return 'string';
			if (v instanceof ArrayBuffer || ArrayBuffer.isView(v)) {
				var src = ArrayBuffer.isView(v)
					? new Uint8Array(v.buffer, v.byteOffset, v.byteLength)
					: new Uint8Array(v);
				var sab = new SharedArrayBuffer(src.byteLength);
				new Uint8Array(sab).set(src);
				globalThis.__tmp_put_sab = sab;
				return 'binary';
			}
			if (typeof Blob !== 'undefined' && v instanceof Blob) {
				var parts = v._parts;
				if (!parts) return 'blob_error';
				var result = '';
				for (var i = 0; i < parts.length; i++) result += String(parts[i]);
				globalThis.__tmp_put_blob_str = result;
				return 'blob';
			}
			return 'unsupported';
		})()`, "storage_coerce.js")
		if jsErr != nil {
			errVal, _ := v8.NewValue(iso, fmt.Sprintf("BUCKET.put body coercion: %s", jsErr.Error()))
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}

		var valueBytes []byte
		switch coerceResult.String() {
		case "string":
			valueBytes = []byte(args[1].String())
		case "binary":
			sabVal, err := ctx.Global().Get("__tmp_put_sab")
			_, _ = ctx.RunScript("delete globalThis.__tmp_put_sab", "storage_cleanup.js")
			if err != nil || sabVal == nil {
				errVal, _ := v8.NewValue(iso, "BUCKET.put: failed to retrieve SharedArrayBuffer")
				resolver.Reject(errVal)
				return resolver.GetPromise().Value
			}
			data, release, err := sabVal.SharedArrayBufferGetContents()
			if err != nil {
				errVal, _ := v8.NewValue(iso, fmt.Sprintf("BUCKET.put: %s", err.Error()))
				resolver.Reject(errVal)
				return resolver.GetPromise().Value
			}
			valueBytes = make([]byte, len(data))
			copy(valueBytes, data)
			release()
		case "blob":
			blobVal, err := ctx.Global().Get("__tmp_put_blob_str")
			_, _ = ctx.RunScript("delete globalThis.__tmp_put_blob_str", "storage_cleanup.js")
			if err != nil || blobVal == nil {
				errVal, _ := v8.NewValue(iso, "BUCKET.put: failed to retrieve Blob string")
				resolver.Reject(errVal)
				return resolver.GetPromise().Value
			}
			valueBytes = []byte(blobVal.String())
		case "blob_error":
			errVal, _ := v8.NewValue(iso, "BUCKET.put: Blob has no _parts")
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		case "unsupported":
			errVal, _ := v8.NewValue(iso, "BUCKET.put: unsupported body type: use string, ArrayBuffer, TypedArray, or Blob")
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}

		// Extract options via JS.
		putOpts := R2PutOptions{}

		if len(args) > 2 && args[2].IsObject() {
			_ = ctx.Global().Set("__tmp_put_opts", args[2])
			optsResult, err := ctx.RunScript(`(function() {
				var o = globalThis.__tmp_put_opts;
				delete globalThis.__tmp_put_opts;
				var result = {httpMetadata:{},customMetadata:{}};
				if (o.httpMetadata && typeof o.httpMetadata === 'object') {
					var h = o.httpMetadata;
					if (h.contentType != null) result.httpMetadata.contentType = String(h.contentType);
				}
				if (o.customMetadata && typeof o.customMetadata === 'object') {
					for (var k in o.customMetadata) {
						if (o.customMetadata.hasOwnProperty(k)) result.customMetadata[k] = String(o.customMetadata[k]);
					}
				}
				return JSON.stringify(result);
			})()`, "storage_put_opts.js")
			if err == nil {
				var parsed struct {
					HTTPMetadata struct {
						ContentType string `json:"contentType"`
					} `json:"httpMetadata"`
					CustomMetadata map[string]string `json:"customMetadata"`
				}
				if json.Unmarshal([]byte(optsResult.String()), &parsed) == nil {
					if parsed.HTTPMetadata.ContentType != "" {
						putOpts.ContentType = parsed.HTTPMetadata.ContentType
					}
					putOpts.CustomMetadata = parsed.CustomMetadata
				}
			}
		}

		r2obj, err := store.Put(key, valueBytes, putOpts)
		if err != nil {
			errVal, _ := v8.NewValue(iso, fmt.Sprintf("putting object: %s", err.Error()))
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}

		jsObj, err := buildR2Object(iso, ctx, key, r2obj.Size, r2obj.ETag, r2obj.ContentType, r2obj.CustomMetadata)
		if err != nil {
			errVal, _ := v8.NewValue(iso, err.Error())
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}
		resolver.Resolve(jsObj.Value)
		return resolver.GetPromise().Value
	}).GetFunction(ctx))

	// delete(key|keys) -> Promise<void>
	_ = bucket.Set("delete", v8.NewFunctionTemplate(iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
		resolver, _ := v8.NewPromiseResolver(ctx)
		args := info.Args()
		if len(args) == 0 {
			errVal, _ := v8.NewValue(iso, "BUCKET.delete requires a key argument")
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}

		// Support single key (string) or array of keys via JS extraction.
		_ = ctx.Global().Set("__tmp_del_arg", args[0])
		keysResult, err := ctx.RunScript(`(function() {
			var v = globalThis.__tmp_del_arg;
			delete globalThis.__tmp_del_arg;
			if (Array.isArray(v)) return JSON.stringify(v.map(String));
			return JSON.stringify([String(v)]);
		})()`, "storage_del_keys.js")
		if err != nil {
			errVal, _ := v8.NewValue(iso, fmt.Sprintf("BUCKET.delete: %s", err.Error()))
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}

		var keys []string
		if err := json.Unmarshal([]byte(keysResult.String()), &keys); err != nil {
			errVal, _ := v8.NewValue(iso, "BUCKET.delete: failed to parse keys")
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}

		// R2 delete is best-effort: resolve undefined even on error.
		_ = store.Delete(keys)

		resolver.Resolve(v8.Undefined(iso))
		return resolver.GetPromise().Value
	}).GetFunction(ctx))

	// head(key) -> Promise<R2Object|null>
	_ = bucket.Set("head", v8.NewFunctionTemplate(iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
		resolver, _ := v8.NewPromiseResolver(ctx)
		args := info.Args()
		if len(args) == 0 {
			errVal, _ := v8.NewValue(iso, "BUCKET.head requires a key argument")
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}
		key := args[0].String()

		r2obj, err := store.Head(key)
		if err != nil || r2obj == nil {
			resolver.Resolve(v8.Null(iso))
			return resolver.GetPromise().Value
		}

		jsObj, err := buildR2Object(iso, ctx, key, r2obj.Size, r2obj.ETag, r2obj.ContentType, r2obj.CustomMetadata)
		if err != nil {
			errVal, _ := v8.NewValue(iso, err.Error())
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}
		resolver.Resolve(jsObj.Value)
		return resolver.GetPromise().Value
	}).GetFunction(ctx))

	// list(opts?) -> Promise<R2Objects>
	_ = bucket.Set("list", v8.NewFunctionTemplate(iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
		resolver, _ := v8.NewPromiseResolver(ctx)
		args := info.Args()

		listOpts := R2ListOptions{
			Limit: 1000,
		}

		if len(args) > 0 && args[0].IsObject() {
			_ = ctx.Global().Set("__tmp_list_opts", args[0])
			optsResult, err := ctx.RunScript(`(function() {
				var o = globalThis.__tmp_list_opts;
				delete globalThis.__tmp_list_opts;
				return JSON.stringify({
					prefix: o.prefix != null ? String(o.prefix) : '',
					cursor: o.cursor != null ? String(o.cursor) : '',
					delimiter: o.delimiter != null ? String(o.delimiter) : '',
					limit: o.limit != null ? Number(o.limit) : 1000,
				});
			})()`, "storage_list_opts.js")
			if err == nil {
				var opts struct {
					Prefix    string `json:"prefix"`
					Cursor    string `json:"cursor"`
					Delimiter string `json:"delimiter"`
					Limit     int    `json:"limit"`
				}
				if json.Unmarshal([]byte(optsResult.String()), &opts) == nil {
					listOpts.Prefix = opts.Prefix
					listOpts.Cursor = opts.Cursor
					listOpts.Delimiter = opts.Delimiter
					listOpts.Limit = opts.Limit
				}
			}
		}

		listResult, err := store.List(listOpts)
		if err != nil {
			// Return empty result on error (matches R2 behavior for unavailable backends).
			listResult = &R2ListResult{}
		}

		var objects []map[string]interface{}
		for _, obj := range listResult.Objects {
			objects = append(objects, map[string]interface{}{
				"key":      obj.Key,
				"size":     obj.Size,
				"etag":     obj.ETag,
				"uploaded": obj.LastModified.UnixMilli(),
			})
		}

		result := map[string]interface{}{
			"objects":           objects,
			"truncated":         listResult.Truncated,
			"cursor":            listResult.Cursor,
			"delimitedPrefixes": listResult.DelimitedPrefixes,
		}
		data, _ := json.Marshal(result)

		jsResult, err := ctx.RunScript(fmt.Sprintf("JSON.parse(%q)", string(data)), "storage_list_result.js")
		if err != nil {
			errVal, _ := v8.NewValue(iso, err.Error())
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}
		resolver.Resolve(jsResult)
		return resolver.GetPromise().Value
	}).GetFunction(ctx))

	// createSignedUrl(key, opts?) -> Promise<string>
	_ = bucket.Set("createSignedUrl", v8.NewFunctionTemplate(iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
		resolver, _ := v8.NewPromiseResolver(ctx)
		args := info.Args()
		if len(args) == 0 {
			errVal, _ := v8.NewValue(iso, "BUCKET.createSignedUrl requires a key argument")
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}
		key := args[0].String()

		expiry := 3600 // default 1 hour
		if len(args) > 1 && args[1].IsObject() {
			_ = ctx.Global().Set("__tmp_sign_opts", args[1])
			optsResult, err := ctx.RunScript(`(function() {
				var o = globalThis.__tmp_sign_opts;
				delete globalThis.__tmp_sign_opts;
				return o.expiresIn != null ? Number(o.expiresIn) : 3600;
			})()`, "storage_sign_opts.js")
			if err == nil {
				expiry = int(optsResult.Int32())
			}
		}
		if expiry < 1 {
			expiry = 1
		}
		if expiry > 604800 {
			expiry = 604800 // cap at 7 days
		}

		urlStr, err := store.PresignedGetURL(key, time.Duration(expiry)*time.Second)
		if err != nil {
			errVal, _ := v8.NewValue(iso, fmt.Sprintf("creating signed URL: %s", err.Error()))
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}

		urlVal, _ := v8.NewValue(iso, urlStr)
		resolver.Resolve(urlVal)
		return resolver.GetPromise().Value
	}).GetFunction(ctx))

	// publicUrl(key) -> string (synchronous)
	_ = bucket.Set("publicUrl", v8.NewFunctionTemplate(iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
		args := info.Args()
		if len(args) == 0 {
			return throwError(iso, "BUCKET.publicUrl requires a key argument")
		}
		key := args[0].String()

		urlStr, err := store.PublicURL(key)
		if err != nil {
			return throwError(iso, fmt.Sprintf("creating public object URL: %s", err.Error()))
		}

		val, _ := v8.NewValue(iso, urlStr)
		return val
	}).GetFunction(ctx))

	return bucket.Value, nil
}

// buildPublicObjectURL returns an object URL using the configured public S3 base.
func buildPublicObjectURL(publicBase string, bucket string, key string) (string, error) {
	pub, err := url.Parse(publicBase)
	if err != nil {
		return "", err
	}
	if pub.Scheme == "" || pub.Host == "" {
		return "", fmt.Errorf("public S3 URL must include scheme and host")
	}

	cleanBucket := strings.Trim(bucket, "/")
	cleanKey := strings.TrimPrefix(key, "/")
	base := strings.TrimRight(pub.Path, "/")
	pub.Path = base + "/" + cleanBucket + "/" + cleanKey
	pub.RawPath = base + "/" + url.PathEscape(cleanBucket) + "/" + escapePathSegments(cleanKey)
	pub.RawQuery = ""
	pub.Fragment = ""

	return pub.String(), nil
}

func escapePathSegments(path string) string {
	if path == "" {
		return ""
	}
	parts := strings.Split(path, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// buildR2Object creates a JS object matching the Cloudflare R2Object shape.
func buildR2Object(iso *v8.Isolate, ctx *v8.Context, key string, size int64, etag string, contentType string, customMeta map[string]string) (*v8.Object, error) {
	obj, err := newJSObject(iso, ctx)
	if err != nil {
		return nil, fmt.Errorf("creating R2Object: %w", err)
	}

	keyVal, _ := v8.NewValue(iso, key)
	_ = obj.Set("key", keyVal)
	sizeVal, _ := v8.NewValue(iso, float64(size))
	_ = obj.Set("size", sizeVal)
	etagVal, _ := v8.NewValue(iso, etag)
	_ = obj.Set("etag", etagVal)
	httpEtagVal, _ := v8.NewValue(iso, "\""+etag+"\"")
	_ = obj.Set("httpEtag", httpEtagVal)
	versionVal, _ := v8.NewValue(iso, etag)
	_ = obj.Set("version", versionVal)
	scVal, _ := v8.NewValue(iso, "STANDARD")
	_ = obj.Set("storageClass", scVal)

	httpMeta, _ := newJSObject(iso, ctx)
	if contentType != "" {
		ctVal, _ := v8.NewValue(iso, contentType)
		_ = httpMeta.Set("contentType", ctVal)
	}
	_ = obj.Set("httpMetadata", httpMeta)

	cm, _ := newJSObject(iso, ctx)
	for k, v := range customMeta {
		vVal, _ := v8.NewValue(iso, v)
		_ = cm.Set(k, vVal)
	}
	_ = obj.Set("customMetadata", cm)

	checksums, _ := newJSObject(iso, ctx)
	_ = obj.Set("checksums", checksums)

	return obj, nil
}

// buildR2ObjectBody extends R2Object with body reading methods.
func buildR2ObjectBody(iso *v8.Isolate, ctx *v8.Context, key string, data []byte, r2obj *R2Object) (*v8.Object, error) {
	obj, err := buildR2Object(iso, ctx, key, r2obj.Size, r2obj.ETag, r2obj.ContentType, r2obj.CustomMetadata)
	if err != nil {
		return nil, err
	}

	uploadedVal, _ := v8.NewValue(iso, float64(r2obj.LastModified.UnixMilli()))
	_ = obj.Set("uploaded", uploadedVal)

	bodyUsed := false
	bodyData := string(data)

	// text() -> Promise<string>
	_ = obj.Set("text", v8.NewFunctionTemplate(iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
		resolver, _ := v8.NewPromiseResolver(ctx)
		if bodyUsed {
			errVal, _ := v8.NewValue(iso, "body already consumed")
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}
		bodyUsed = true
		boolVal, _ := v8.NewValue(iso, true)
		_ = obj.Set("bodyUsed", boolVal)
		textVal, _ := v8.NewValue(iso, bodyData)
		resolver.Resolve(textVal)
		return resolver.GetPromise().Value
	}).GetFunction(ctx))

	// arrayBuffer() -> Promise<ArrayBuffer>
	_ = obj.Set("arrayBuffer", v8.NewFunctionTemplate(iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
		resolver, _ := v8.NewPromiseResolver(ctx)
		if bodyUsed {
			errVal, _ := v8.NewValue(iso, "body already consumed")
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}
		bodyUsed = true
		boolVal, _ := v8.NewValue(iso, true)
		_ = obj.Set("bodyUsed", boolVal)
		// Create ArrayBuffer via SharedArrayBuffer bridge: allocate a SAB
		// in JS, write Go bytes directly into its backing store, then copy
		// to a regular ArrayBuffer. No base64 round-trip needed.
		lenVal, _ := v8.NewValue(iso, int32(len(data)))
		_ = ctx.Global().Set("__tmp_ab_len", lenVal)
		_, err := ctx.RunScript(`globalThis.__tmp_ab_sab = new SharedArrayBuffer(globalThis.__tmp_ab_len); delete globalThis.__tmp_ab_len;`, "r2_sab_alloc.js")
		if err != nil {
			errVal, _ := v8.NewValue(iso, fmt.Sprintf("BUCKET.get arrayBuffer: %s", err.Error()))
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}
		sabVal, _ := ctx.Global().Get("__tmp_ab_sab")
		sabBytes, release, sabErr := sabVal.SharedArrayBufferGetContents()
		if sabErr != nil {
			_, _ = ctx.RunScript("delete globalThis.__tmp_ab_sab", "r2_sab_cleanup.js")
			errVal, _ := v8.NewValue(iso, fmt.Sprintf("BUCKET.get arrayBuffer: %s", sabErr.Error()))
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}
		copy(sabBytes, data)
		release()
		abResult, err := ctx.RunScript(`(function() {
			var sab = globalThis.__tmp_ab_sab;
			delete globalThis.__tmp_ab_sab;
			var buf = new ArrayBuffer(sab.byteLength);
			new Uint8Array(buf).set(new Uint8Array(sab));
			return buf;
		})()`, "r2_arraybuffer.js")
		if err != nil {
			errVal, _ := v8.NewValue(iso, err.Error())
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}
		resolver.Resolve(abResult)
		return resolver.GetPromise().Value
	}).GetFunction(ctx))

	// json() -> Promise<any>
	_ = obj.Set("json", v8.NewFunctionTemplate(iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
		resolver, _ := v8.NewPromiseResolver(ctx)
		if bodyUsed {
			errVal, _ := v8.NewValue(iso, "body already consumed")
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}
		bodyUsed = true
		boolVal, _ := v8.NewValue(iso, true)
		_ = obj.Set("bodyUsed", boolVal)
		if !json.Valid([]byte(bodyData)) {
			errVal, _ := v8.NewValue(iso, "invalid JSON")
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}
		parsed, err := ctx.RunScript(fmt.Sprintf("JSON.parse(%q)", bodyData), "r2_json_parse.js")
		if err != nil {
			errVal, _ := v8.NewValue(iso, err.Error())
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}
		resolver.Resolve(parsed)
		return resolver.GetPromise().Value
	}).GetFunction(ctx))

	falseVal, _ := v8.NewValue(iso, false)
	_ = obj.Set("bodyUsed", falseVal)

	return obj, nil
}
