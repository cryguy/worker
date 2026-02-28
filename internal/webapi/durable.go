package webapi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/cryguy/worker/v2/internal/core"
	"github.com/cryguy/worker/v2/internal/eventloop"
)

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

// SetupDurableObjects registers global Go functions for Durable Object operations.
func SetupDurableObjects(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	// __do_id_from_name(namespace, name) -> hex ID
	if err := rt.RegisterFunc("__do_id_from_name", func(namespace, name string) (string, error) {
		return durableObjectID(namespace, name), nil
	}); err != nil {
		return fmt.Errorf("registering __do_id_from_name: %w", err)
	}

	// __do_unique_id() -> random hex ID
	if err := rt.RegisterFunc("__do_unique_id", func() (string, error) {
		return durableObjectUniqueID()
	}); err != nil {
		return fmt.Errorf("registering __do_unique_id: %w", err)
	}

	// __do_storage_get(reqIDStr, namespace, objectID, key) -> JSON value or "null"
	if err := rt.RegisterFunc("__do_storage_get", func(reqIDStr, namespace, objectID, key string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil || state.Env == nil || state.Env.DurableObjects == nil {
			return "null", nil
		}
		store, ok := state.Env.DurableObjects[namespace]
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
	}); err != nil {
		return fmt.Errorf("registering __do_storage_get: %w", err)
	}

	// __do_storage_get_multi(reqIDStr, namespace, objectID, keysJSON) -> JSON map
	if err := rt.RegisterFunc("__do_storage_get_multi", func(reqIDStr, namespace, objectID, keysJSON string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil || state.Env == nil || state.Env.DurableObjects == nil {
			return "{}", nil
		}
		store, ok := state.Env.DurableObjects[namespace]
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
	}); err != nil {
		return fmt.Errorf("registering __do_storage_get_multi: %w", err)
	}

	// __do_storage_put(reqIDStr, namespace, objectID, key, valueJSON) -> "" or error
	if err := rt.RegisterFunc("__do_storage_put", func(reqIDStr, namespace, objectID, key, valueJSON string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil || state.Env == nil || state.Env.DurableObjects == nil {
			return "", fmt.Errorf("DurableObjects not available")
		}
		store, ok := state.Env.DurableObjects[namespace]
		if !ok {
			return "", fmt.Errorf("DurableObject namespace %q not found", namespace)
		}

		if err := store.Put(namespace, objectID, key, valueJSON); err != nil {
			return "", err
		}
		return "", nil
	}); err != nil {
		return fmt.Errorf("registering __do_storage_put: %w", err)
	}

	// __do_storage_put_multi(reqIDStr, namespace, objectID, entriesJSON) -> "" or error
	if err := rt.RegisterFunc("__do_storage_put_multi", func(reqIDStr, namespace, objectID, entriesJSON string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil || state.Env == nil || state.Env.DurableObjects == nil {
			return "", fmt.Errorf("DurableObjects not available")
		}
		store, ok := state.Env.DurableObjects[namespace]
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
	}); err != nil {
		return fmt.Errorf("registering __do_storage_put_multi: %w", err)
	}

	// __do_storage_delete(reqIDStr, namespace, objectID, key) -> "" or error
	if err := rt.RegisterFunc("__do_storage_delete", func(reqIDStr, namespace, objectID, key string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil || state.Env == nil || state.Env.DurableObjects == nil {
			return "", fmt.Errorf("DurableObjects not available")
		}
		store, ok := state.Env.DurableObjects[namespace]
		if !ok {
			return "", fmt.Errorf("DurableObject namespace %q not found", namespace)
		}

		if err := store.Delete(namespace, objectID, key); err != nil {
			return "", err
		}
		return "", nil
	}); err != nil {
		return fmt.Errorf("registering __do_storage_delete: %w", err)
	}

	// __do_storage_delete_multi(reqIDStr, namespace, objectID, keysJSON) -> JSON count or error
	if err := rt.RegisterFunc("__do_storage_delete_multi", func(reqIDStr, namespace, objectID, keysJSON string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil || state.Env == nil || state.Env.DurableObjects == nil {
			return "", fmt.Errorf("DurableObjects not available")
		}
		store, ok := state.Env.DurableObjects[namespace]
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
	}); err != nil {
		return fmt.Errorf("registering __do_storage_delete_multi: %w", err)
	}

	// __do_storage_delete_all(reqIDStr, namespace, objectID) -> "" or error
	if err := rt.RegisterFunc("__do_storage_delete_all", func(reqIDStr, namespace, objectID string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil || state.Env == nil || state.Env.DurableObjects == nil {
			return "", fmt.Errorf("DurableObjects not available")
		}
		store, ok := state.Env.DurableObjects[namespace]
		if !ok {
			return "", fmt.Errorf("DurableObject namespace %q not found", namespace)
		}

		if err := store.DeleteAll(namespace, objectID); err != nil {
			return "", err
		}
		return "", nil
	}); err != nil {
		return fmt.Errorf("registering __do_storage_delete_all: %w", err)
	}

	// __do_storage_list(reqIDStr, namespace, objectID, optsJSON) -> JSON array of pairs
	if err := rt.RegisterFunc("__do_storage_list", func(reqIDStr, namespace, objectID, optsJSON string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil || state.Env == nil || state.Env.DurableObjects == nil {
			return "", fmt.Errorf("DurableObjects not available")
		}
		store, ok := state.Env.DurableObjects[namespace]
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

		// Return as array of [key, value] pairs to preserve order.
		pairs := make([][2]string, len(entries))
		for i, e := range entries {
			pairs[i] = [2]string{e.Key, e.Value}
		}
		data, _ := json.Marshal(pairs)
		return string(data), nil
	}); err != nil {
		return fmt.Errorf("registering __do_storage_list: %w", err)
	}

	// Define the __makeDO factory function.
	doFactoryJS := `
globalThis.__makeDO = function(namespace) {
	function makeStub(objectID) {
		return {
			storage: {
				get: function(key) {
					var reqID = String(globalThis.__requestID);
					if (Array.isArray(key)) {
						return new Promise(function(resolve, reject) {
							try {
								var resultStr = __do_storage_get_multi(reqID, namespace, objectID, JSON.stringify(key));
								var obj = JSON.parse(resultStr);
								var map = new Map();
								for (var k in obj) {
									try { map.set(k, JSON.parse(obj[k])); }
									catch(e2) { map.set(k, obj[k]); }
								}
								resolve(map);
							} catch(e) { reject(e); }
						});
					}
					return new Promise(function(resolve, reject) {
						try {
							var resultStr = __do_storage_get(reqID, namespace, objectID, String(key));
							resolve(resultStr === "null" ? null : JSON.parse(resultStr));
						} catch(e) { reject(e); }
					});
				},
				put: function(key, value) {
					var reqID = String(globalThis.__requestID);
					if (typeof key === "object" && key !== null && !(typeof value !== "undefined")) {
						var entries = {};
						for (var k in key) entries[k] = JSON.stringify(key[k]);
						return new Promise(function(resolve, reject) {
							try { __do_storage_put_multi(reqID, namespace, objectID, JSON.stringify(entries)); resolve(); }
							catch(e) { reject(e); }
						});
					}
					return new Promise(function(resolve, reject) {
						try { __do_storage_put(reqID, namespace, objectID, String(key), JSON.stringify(value)); resolve(); }
						catch(e) { reject(e); }
					});
				},
				delete: function(key) {
					var reqID = String(globalThis.__requestID);
					if (Array.isArray(key)) {
						return new Promise(function(resolve, reject) {
							try {
								var resultStr = __do_storage_delete_multi(reqID, namespace, objectID, JSON.stringify(key));
								resolve(JSON.parse(resultStr).count);
							} catch(e) { reject(e); }
						});
					}
					return new Promise(function(resolve, reject) {
						try { __do_storage_delete(reqID, namespace, objectID, String(key)); resolve(true); }
						catch(e) { reject(e); }
					});
				},
				deleteAll: function() {
					var reqID = String(globalThis.__requestID);
					return new Promise(function(resolve, reject) {
						try { __do_storage_delete_all(reqID, namespace, objectID); resolve(); }
						catch(e) { reject(e); }
					});
				},
				list: function(opts) {
					var reqID = String(globalThis.__requestID);
					var optsJSON = opts ? JSON.stringify({
						prefix: opts.prefix || "",
						limit: opts.limit || 128,
						reverse: opts.reverse || false
					}) : "{}";
					return new Promise(function(resolve, reject) {
						try {
							var resultStr = __do_storage_list(reqID, namespace, objectID, optsJSON);
							var pairs = JSON.parse(resultStr);
							var map = new Map();
							for (var i = 0; i < pairs.length; i++) {
								try { map.set(pairs[i][0], JSON.parse(pairs[i][1])); }
								catch(e) { map.set(pairs[i][0], pairs[i][1]); }
							}
							resolve(map);
						} catch(e) { reject(e); }
					});
				}
			}
		};
	}
	return {
		idFromName: function(name) {
			return { toString: function() { return __do_id_from_name(namespace, String(name)); } };
		},
		idFromString: function(id) {
			return { toString: function() { return String(id); } };
		},
		newUniqueId: function() {
			var id = __do_unique_id();
			return { toString: function() { return id; } };
		},
		get: function(id) {
			return makeStub(String(id));
		}
	};
};
`
	if err := rt.Eval(doFactoryJS); err != nil {
		return fmt.Errorf("evaluating DurableObject factory JS: %w", err)
	}

	return nil
}
