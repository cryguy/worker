package worker

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"modernc.org/quickjs"
)

const maxObjectSize = 128 * 1024 * 1024 // 128 MB

// setupStorage registers global Go functions for R2 storage operations.
func setupStorage(vm *quickjs.VM, el *eventLoop) error {
	// __r2_get(reqIDStr, bindingName, key) -> JSON or "null"
	err := registerGoFunc(vm, "__r2_get", func(reqIDStr, bindingName, key string) (string, error) {
		reqID := parseReqID(reqIDStr)
		state := getRequestState(reqID)
		if state == nil || state.env == nil || state.env.Storage == nil {
			return "null", nil
		}
		store, ok := state.env.Storage[bindingName]
		if !ok {
			return "null", nil
		}

		data, r2obj, err := store.Get(key)
		if err != nil || r2obj == nil {
			return "null", nil
		}

		if r2obj.Size > int64(maxObjectSize) {
			return "", fmt.Errorf("object too large: %d bytes (max %d)", r2obj.Size, maxObjectSize)
		}

		bodyB64 := base64.StdEncoding.EncodeToString(data)
		result := map[string]interface{}{
			"key":            r2obj.Key,
			"size":           r2obj.Size,
			"etag":           r2obj.ETag,
			"contentType":    r2obj.ContentType,
			"customMetadata": r2obj.CustomMetadata,
			"bodyB64":        bodyB64,
			"uploaded":       r2obj.LastModified.UnixMilli(),
		}
		resultJSON, _ := json.Marshal(result)
		return string(resultJSON), nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __r2_get: %w", err)
	}

	// __r2_put(reqIDStr, bindingName, key, bodyB64, optsJSON) -> JSON R2Object or error
	err = registerGoFunc(vm, "__r2_put", func(reqIDStr, bindingName, key, bodyB64, optsJSON string) (string, error) {
		reqID := parseReqID(reqIDStr)
		state := getRequestState(reqID)
		if state == nil || state.env == nil || state.env.Storage == nil {
			return "", fmt.Errorf("Storage not available")
		}
		store, ok := state.env.Storage[bindingName]
		if !ok {
			return "", fmt.Errorf("Storage binding %q not found", bindingName)
		}

		valueBytes, err := base64.StdEncoding.DecodeString(bodyB64)
		if err != nil {
			return "", fmt.Errorf("invalid base64 body: %w", err)
		}

		putOpts := R2PutOptions{}
		if optsJSON != "" && optsJSON != "{}" {
			var parsed struct {
				HTTPMetadata struct {
					ContentType string `json:"contentType"`
				} `json:"httpMetadata"`
				CustomMetadata map[string]string `json:"customMetadata"`
			}
			if err := json.Unmarshal([]byte(optsJSON), &parsed); err == nil {
				if parsed.HTTPMetadata.ContentType != "" {
					putOpts.ContentType = parsed.HTTPMetadata.ContentType
				}
				putOpts.CustomMetadata = parsed.CustomMetadata
			}
		}

		r2obj, err := store.Put(key, valueBytes, putOpts)
		if err != nil {
			return "", err
		}

		result := map[string]interface{}{
			"key":            key,
			"size":           r2obj.Size,
			"etag":           r2obj.ETag,
			"contentType":    r2obj.ContentType,
			"customMetadata": r2obj.CustomMetadata,
		}
		data, _ := json.Marshal(result)
		return string(data), nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __r2_put: %w", err)
	}

	// __r2_delete(reqIDStr, bindingName, keysJSON) -> "" or error
	err = registerGoFunc(vm, "__r2_delete", func(reqIDStr, bindingName, keysJSON string) (string, error) {
		reqID := parseReqID(reqIDStr)
		state := getRequestState(reqID)
		if state == nil || state.env == nil || state.env.Storage == nil {
			return "", fmt.Errorf("Storage not available")
		}
		store, ok := state.env.Storage[bindingName]
		if !ok {
			return "", fmt.Errorf("Storage binding %q not found", bindingName)
		}

		var keys []string
		if err := json.Unmarshal([]byte(keysJSON), &keys); err != nil {
			return "", fmt.Errorf("invalid keys JSON: %w", err)
		}

		// R2 delete is best-effort: ignore error
		_ = store.Delete(keys)
		return "", nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __r2_delete: %w", err)
	}

	// __r2_head(reqIDStr, bindingName, key) -> JSON R2Object or "null"
	err = registerGoFunc(vm, "__r2_head", func(reqIDStr, bindingName, key string) (string, error) {
		reqID := parseReqID(reqIDStr)
		state := getRequestState(reqID)
		if state == nil || state.env == nil || state.env.Storage == nil {
			return "null", nil
		}
		store, ok := state.env.Storage[bindingName]
		if !ok {
			return "null", nil
		}

		r2obj, err := store.Head(key)
		if err != nil || r2obj == nil {
			return "null", nil
		}

		result := map[string]interface{}{
			"key":            key,
			"size":           r2obj.Size,
			"etag":           r2obj.ETag,
			"contentType":    r2obj.ContentType,
			"customMetadata": r2obj.CustomMetadata,
			"uploaded":       r2obj.LastModified.UnixMilli(),
		}
		data, _ := json.Marshal(result)
		return string(data), nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __r2_head: %w", err)
	}

	// __r2_list(reqIDStr, bindingName, optsJSON) -> JSON result
	err = registerGoFunc(vm, "__r2_list", func(reqIDStr, bindingName, optsJSON string) (string, error) {
		reqID := parseReqID(reqIDStr)
		state := getRequestState(reqID)
		if state == nil || state.env == nil || state.env.Storage == nil {
			return "", fmt.Errorf("Storage not available")
		}
		store, ok := state.env.Storage[bindingName]
		if !ok {
			return "", fmt.Errorf("Storage binding %q not found", bindingName)
		}

		listOpts := R2ListOptions{Limit: 1000}
		if optsJSON != "" && optsJSON != "{}" {
			var opts struct {
				Prefix    string `json:"prefix"`
				Cursor    string `json:"cursor"`
				Delimiter string `json:"delimiter"`
				Limit     int    `json:"limit"`
			}
			if err := json.Unmarshal([]byte(optsJSON), &opts); err == nil {
				listOpts.Prefix = opts.Prefix
				listOpts.Cursor = opts.Cursor
				listOpts.Delimiter = opts.Delimiter
				listOpts.Limit = opts.Limit
			}
		}

		listResult, err := store.List(listOpts)
		if err != nil {
			// Return empty result on error (matches R2 behavior)
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
		return string(data), nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __r2_list: %w", err)
	}

	// __r2_presigned_url(reqIDStr, bindingName, key, expiresIn) -> URL string
	err = registerGoFunc(vm, "__r2_presigned_url", func(reqIDStr, bindingName, key string, expiresIn int) (string, error) {
		reqID := parseReqID(reqIDStr)
		state := getRequestState(reqID)
		if state == nil || state.env == nil || state.env.Storage == nil {
			return "", fmt.Errorf("Storage not available")
		}
		store, ok := state.env.Storage[bindingName]
		if !ok {
			return "", fmt.Errorf("Storage binding %q not found", bindingName)
		}

		if expiresIn < 1 {
			expiresIn = 1
		}
		if expiresIn > 604800 {
			expiresIn = 604800 // cap at 7 days
		}

		urlStr, err := store.PresignedGetURL(key, time.Duration(expiresIn)*time.Second)
		if err != nil {
			return "", err
		}
		return urlStr, nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __r2_presigned_url: %w", err)
	}

	// __r2_public_url(reqIDStr, bindingName, key) -> URL string
	err = registerGoFunc(vm, "__r2_public_url", func(reqIDStr, bindingName, key string) (string, error) {
		reqID := parseReqID(reqIDStr)
		state := getRequestState(reqID)
		if state == nil || state.env == nil || state.env.Storage == nil {
			return "", fmt.Errorf("Storage not available")
		}
		store, ok := state.env.Storage[bindingName]
		if !ok {
			return "", fmt.Errorf("Storage binding %q not found", bindingName)
		}

		urlStr, err := store.PublicURL(key)
		if err != nil {
			return "", err
		}
		return urlStr, nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __r2_public_url: %w", err)
	}

	return nil
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
