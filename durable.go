package worker

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"modernc.org/quickjs"
)

// KVPair represents an ordered key-value pair returned by List.
type KVPair struct {
	Key   string
	Value string
}

// durableObjectID generates a deterministic hex ID from a namespace and name.
func durableObjectID(namespace, name string) string {
	h := sha256.New()
	h.Write([]byte(namespace))
	h.Write([]byte(":"))
	h.Write([]byte(name))
	return hex.EncodeToString(h.Sum(nil))
}

// durableObjectUniqueID generates a random 32-char hex ID.
func durableObjectUniqueID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// setupDurableObjects registers global Go functions for Durable Object operations.
func setupDurableObjects(vm *quickjs.VM, el *eventLoop) error {
	// __do_id_from_name(namespace, name) -> hex ID
	err := registerGoFunc(vm, "__do_id_from_name", func(namespace, name string) (string, error) {
		return durableObjectID(namespace, name), nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __do_id_from_name: %w", err)
	}

	// __do_unique_id() -> random hex ID
	err = registerGoFunc(vm, "__do_unique_id", func() (string, error) {
		return durableObjectUniqueID()
	}, false)
	if err != nil {
		return fmt.Errorf("registering __do_unique_id: %w", err)
	}

	// __do_storage_get(reqIDStr, namespace, objectID, key) -> JSON value or "null"
	err = registerGoFunc(vm, "__do_storage_get", func(reqIDStr, namespace, objectID, key string) (string, error) {
		reqID := parseReqID(reqIDStr)
		state := getRequestState(reqID)
		if state == nil || state.env == nil || state.env.DurableObjects == nil {
			return "null", nil
		}
		store, ok := state.env.DurableObjects[namespace]
		if !ok {
			return "null", nil
		}

		val, err := store.Get(namespace, objectID, key)
		if err != nil {
			return "", err
		}
		if val == "" {
			return "null", nil
		}
		return val, nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __do_storage_get: %w", err)
	}

	// __do_storage_get_multi(reqIDStr, namespace, objectID, keysJSON) -> JSON map
	err = registerGoFunc(vm, "__do_storage_get_multi", func(reqIDStr, namespace, objectID, keysJSON string) (string, error) {
		reqID := parseReqID(reqIDStr)
		state := getRequestState(reqID)
		if state == nil || state.env == nil || state.env.DurableObjects == nil {
			return "{}", nil
		}
		store, ok := state.env.DurableObjects[namespace]
		if !ok {
			return "{}", nil
		}

		var keys []string
		if err := json.Unmarshal([]byte(keysJSON), &keys); err != nil {
			return "", fmt.Errorf("invalid keys JSON: %w", err)
		}

		result, err := store.GetMulti(namespace, objectID, keys)
		if err != nil {
			return "", err
		}

		data, _ := json.Marshal(result)
		return string(data), nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __do_storage_get_multi: %w", err)
	}

	// __do_storage_put(reqIDStr, namespace, objectID, key, valueJSON) -> "" or error
	err = registerGoFunc(vm, "__do_storage_put", func(reqIDStr, namespace, objectID, key, valueJSON string) (string, error) {
		reqID := parseReqID(reqIDStr)
		state := getRequestState(reqID)
		if state == nil || state.env == nil || state.env.DurableObjects == nil {
			return "", fmt.Errorf("DurableObjects not available")
		}
		store, ok := state.env.DurableObjects[namespace]
		if !ok {
			return "", fmt.Errorf("DurableObject namespace %q not found", namespace)
		}

		if err := store.Put(namespace, objectID, key, valueJSON); err != nil {
			return "", err
		}
		return "", nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __do_storage_put: %w", err)
	}

	// __do_storage_put_multi(reqIDStr, namespace, objectID, entriesJSON) -> "" or error
	err = registerGoFunc(vm, "__do_storage_put_multi", func(reqIDStr, namespace, objectID, entriesJSON string) (string, error) {
		reqID := parseReqID(reqIDStr)
		state := getRequestState(reqID)
		if state == nil || state.env == nil || state.env.DurableObjects == nil {
			return "", fmt.Errorf("DurableObjects not available")
		}
		store, ok := state.env.DurableObjects[namespace]
		if !ok {
			return "", fmt.Errorf("DurableObject namespace %q not found", namespace)
		}

		var entries map[string]string
		if err := json.Unmarshal([]byte(entriesJSON), &entries); err != nil {
			return "", fmt.Errorf("invalid entries JSON: %w", err)
		}

		if err := store.PutMulti(namespace, objectID, entries); err != nil {
			return "", err
		}
		return "", nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __do_storage_put_multi: %w", err)
	}

	// __do_storage_delete(reqIDStr, namespace, objectID, key) -> "" or error
	err = registerGoFunc(vm, "__do_storage_delete", func(reqIDStr, namespace, objectID, key string) (string, error) {
		reqID := parseReqID(reqIDStr)
		state := getRequestState(reqID)
		if state == nil || state.env == nil || state.env.DurableObjects == nil {
			return "", fmt.Errorf("DurableObjects not available")
		}
		store, ok := state.env.DurableObjects[namespace]
		if !ok {
			return "", fmt.Errorf("DurableObject namespace %q not found", namespace)
		}

		if err := store.Delete(namespace, objectID, key); err != nil {
			return "", err
		}
		return "", nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __do_storage_delete: %w", err)
	}

	// __do_storage_delete_multi(reqIDStr, namespace, objectID, keysJSON) -> JSON count or error
	err = registerGoFunc(vm, "__do_storage_delete_multi", func(reqIDStr, namespace, objectID, keysJSON string) (string, error) {
		reqID := parseReqID(reqIDStr)
		state := getRequestState(reqID)
		if state == nil || state.env == nil || state.env.DurableObjects == nil {
			return "", fmt.Errorf("DurableObjects not available")
		}
		store, ok := state.env.DurableObjects[namespace]
		if !ok {
			return "", fmt.Errorf("DurableObject namespace %q not found", namespace)
		}

		var keys []string
		if err := json.Unmarshal([]byte(keysJSON), &keys); err != nil {
			return "", fmt.Errorf("invalid keys JSON: %w", err)
		}

		count, err := store.DeleteMulti(namespace, objectID, keys)
		if err != nil {
			return "", err
		}

		result := map[string]int{"count": count}
		data, _ := json.Marshal(result)
		return string(data), nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __do_storage_delete_multi: %w", err)
	}

	// __do_storage_delete_all(reqIDStr, namespace, objectID) -> "" or error
	err = registerGoFunc(vm, "__do_storage_delete_all", func(reqIDStr, namespace, objectID string) (string, error) {
		reqID := parseReqID(reqIDStr)
		state := getRequestState(reqID)
		if state == nil || state.env == nil || state.env.DurableObjects == nil {
			return "", fmt.Errorf("DurableObjects not available")
		}
		store, ok := state.env.DurableObjects[namespace]
		if !ok {
			return "", fmt.Errorf("DurableObject namespace %q not found", namespace)
		}

		if err := store.DeleteAll(namespace, objectID); err != nil {
			return "", err
		}
		return "", nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __do_storage_delete_all: %w", err)
	}

	// __do_storage_list(reqIDStr, namespace, objectID, optsJSON) -> JSON array of pairs
	err = registerGoFunc(vm, "__do_storage_list", func(reqIDStr, namespace, objectID, optsJSON string) (string, error) {
		reqID := parseReqID(reqIDStr)
		state := getRequestState(reqID)
		if state == nil || state.env == nil || state.env.DurableObjects == nil {
			return "", fmt.Errorf("DurableObjects not available")
		}
		store, ok := state.env.DurableObjects[namespace]
		if !ok {
			return "", fmt.Errorf("DurableObject namespace %q not found", namespace)
		}

		var prefix string
		limit := 128
		reverse := false
		if optsJSON != "" && optsJSON != "{}" {
			var opts struct {
				Prefix  string `json:"prefix"`
				Limit   int    `json:"limit"`
				Reverse bool   `json:"reverse"`
			}
			if err := json.Unmarshal([]byte(optsJSON), &opts); err == nil {
				prefix = opts.Prefix
				limit = opts.Limit
				reverse = opts.Reverse
			}
		}

		entries, err := store.List(namespace, objectID, prefix, limit, reverse)
		if err != nil {
			return "", err
		}

		// Return as array of [key, value] pairs to preserve order
		pairs := make([][2]string, len(entries))
		for i, e := range entries {
			pairs[i] = [2]string{e.Key, e.Value}
		}
		data, _ := json.Marshal(pairs)
		return string(data), nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __do_storage_list: %w", err)
	}

	return nil
}
