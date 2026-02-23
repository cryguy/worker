package worker

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"

	"modernc.org/quickjs"
)

// KVValueWithMetadata holds a value and its associated metadata.
type KVValueWithMetadata struct {
	Value    string
	Metadata *string
}

// maxKVValueSize is the maximum size of a KV value (1 MB).
const maxKVValueSize = 1 << 20

// KVListResult holds the result of a List operation with pagination info.
type KVListResult struct {
	Keys         []map[string]interface{}
	ListComplete bool
	Cursor       string // base64-encoded offset, empty when list is complete
}

// decodeCursor decodes a base64-encoded cursor to an integer offset.
func decodeCursor(cursor string) int {
	if cursor == "" {
		return 0
	}
	data, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return 0
	}
	offset, err := strconv.Atoi(string(data))
	if err != nil {
		return 0
	}
	return offset
}

// encodeCursor encodes an integer offset to a base64 cursor string.
func encodeCursor(offset int) string {
	return base64.StdEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

// setupKV registers global Go functions for KV namespace operations.
// The actual KV binding objects are built in buildEnvObject via JS wrappers.
func setupKV(vm *quickjs.VM, el *eventLoop) error {
	// __kv_get(reqIDStr, bindingName, key, valType) -> JSON string or "null"
	err := registerGoFunc(vm, "__kv_get", func(reqIDStr, bindingName, key, valType string) (string, error) {
		reqID := parseReqID(reqIDStr)
		state := getRequestState(reqID)
		if state == nil || state.env == nil || state.env.KV == nil {
			return "null", nil
		}
		store, ok := state.env.KV[bindingName]
		if !ok {
			return "null", nil
		}

		val, err := store.Get(key)
		if err != nil {
			return "", err
		}
		if val == nil {
			return "null", nil
		}

		result := map[string]interface{}{"value": *val}
		data, _ := json.Marshal(result)
		return string(data), nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __kv_get: %w", err)
	}

	// __kv_get_with_metadata(reqIDStr, bindingName, key, valType) -> JSON string
	err = registerGoFunc(vm, "__kv_get_with_metadata", func(reqIDStr, bindingName, key, valType string) (string, error) {
		reqID := parseReqID(reqIDStr)
		state := getRequestState(reqID)
		if state == nil || state.env == nil || state.env.KV == nil {
			return `{"value":null,"metadata":null}`, nil
		}
		store, ok := state.env.KV[bindingName]
		if !ok {
			return `{"value":null,"metadata":null}`, nil
		}

		result, err := store.GetWithMetadata(key)
		if err != nil {
			return "", err
		}
		if result == nil {
			return `{"value":null,"metadata":null}`, nil
		}

		response := map[string]interface{}{
			"value":    result.Value,
			"metadata": result.Metadata,
		}
		data, _ := json.Marshal(response)
		return string(data), nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __kv_get_with_metadata: %w", err)
	}

	// __kv_put(reqIDStr, bindingName, key, value, optsJSON) -> "" or error
	err = registerGoFunc(vm, "__kv_put", func(reqIDStr, bindingName, key, value, optsJSON string) (string, error) {
		reqID := parseReqID(reqIDStr)
		state := getRequestState(reqID)
		if state == nil || state.env == nil || state.env.KV == nil {
			return "", fmt.Errorf("KV not available")
		}
		store, ok := state.env.KV[bindingName]
		if !ok {
			return "", fmt.Errorf("KV binding %q not found", bindingName)
		}

		var metadata *string
		var ttl *int
		if optsJSON != "" && optsJSON != "{}" {
			var opts struct {
				Metadata      *string `json:"metadata"`
				ExpirationTtl *int    `json:"expirationTtl"`
			}
			if err := json.Unmarshal([]byte(optsJSON), &opts); err == nil {
				metadata = opts.Metadata
				ttl = opts.ExpirationTtl
			}
		}

		if err := store.Put(key, value, metadata, ttl); err != nil {
			return "", err
		}
		return "", nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __kv_put: %w", err)
	}

	// __kv_delete(reqIDStr, bindingName, key) -> "" or error
	err = registerGoFunc(vm, "__kv_delete", func(reqIDStr, bindingName, key string) (string, error) {
		reqID := parseReqID(reqIDStr)
		state := getRequestState(reqID)
		if state == nil || state.env == nil || state.env.KV == nil {
			return "", fmt.Errorf("KV not available")
		}
		store, ok := state.env.KV[bindingName]
		if !ok {
			return "", fmt.Errorf("KV binding %q not found", bindingName)
		}

		if err := store.Delete(key); err != nil {
			return "", err
		}
		return "", nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __kv_delete: %w", err)
	}

	// __kv_list(reqIDStr, bindingName, optsJSON) -> JSON result
	err = registerGoFunc(vm, "__kv_list", func(reqIDStr, bindingName, optsJSON string) (string, error) {
		reqID := parseReqID(reqIDStr)
		state := getRequestState(reqID)
		if state == nil || state.env == nil || state.env.KV == nil {
			return "", fmt.Errorf("KV not available")
		}
		store, ok := state.env.KV[bindingName]
		if !ok {
			return "", fmt.Errorf("KV binding %q not found", bindingName)
		}

		var prefix string
		var cursor string
		limit := 1000
		if optsJSON != "" && optsJSON != "{}" {
			var opts struct {
				Prefix string `json:"prefix"`
				Limit  int    `json:"limit"`
				Cursor string `json:"cursor"`
			}
			if err := json.Unmarshal([]byte(optsJSON), &opts); err == nil {
				prefix = opts.Prefix
				limit = opts.Limit
				cursor = opts.Cursor
			}
		}

		listResult, err := store.List(prefix, limit, cursor)
		if err != nil {
			return "", err
		}

		result := map[string]interface{}{
			"keys":          listResult.Keys,
			"list_complete": listResult.ListComplete,
		}
		if listResult.Cursor != "" {
			result["cursor"] = listResult.Cursor
		}

		data, _ := json.Marshal(result)
		return string(data), nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __kv_list: %w", err)
	}

	return nil
}
