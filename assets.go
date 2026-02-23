package worker

import (
	"encoding/json"
	"fmt"
	"mime"
	"path/filepath"
	"strings"

	"modernc.org/quickjs"
)

// contentType guesses the MIME type from the file extension.
func contentType(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	if ext == "" {
		return "application/octet-stream"
	}
	ct := mime.TypeByExtension(ext)
	if ct == "" {
		return "application/octet-stream"
	}
	return ct
}

// setupAssets registers global Go functions for Assets operations.
func setupAssets(vm *quickjs.VM, el *eventLoop) error {
	// __assets_fetch(reqIDStr, reqJSON) -> JSON response or error
	err := registerGoFunc(vm, "__assets_fetch", func(reqIDStr, reqJSON string) (string, error) {
		reqID := parseReqID(reqIDStr)
		state := getRequestState(reqID)
		if state == nil || state.env == nil || state.env.Assets == nil {
			return "", fmt.Errorf("ASSETS not available")
		}

		var reqData struct {
			URL     string            `json:"url"`
			Method  string            `json:"method"`
			Headers map[string]string `json:"headers"`
			Body    *string           `json:"body"`
		}
		if err := json.Unmarshal([]byte(reqJSON), &reqData); err != nil {
			return "", fmt.Errorf("invalid request JSON: %w", err)
		}

		goReq := &WorkerRequest{
			URL:     reqData.URL,
			Method:  reqData.Method,
			Headers: reqData.Headers,
		}
		if reqData.Body != nil {
			goReq.Body = []byte(*reqData.Body)
		}

		resp, err := state.env.Assets.Fetch(goReq)
		if err != nil {
			return "", err
		}

		respJSON := map[string]interface{}{
			"status":  resp.StatusCode,
			"headers": resp.Headers,
			"body":    string(resp.Body),
		}
		data, _ := json.Marshal(respJSON)
		return string(data), nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __assets_fetch: %w", err)
	}

	return nil
}

// buildEnvObject constructs the full env JS object with all bindings.
// This replaces the stub in stubs.go.
func buildEnvObject(vm *quickjs.VM, env *Env, reqID uint64) error {
	// Create the env object
	envObj, err := newJSObject(vm)
	if err != nil {
		return fmt.Errorf("creating env object: %w", err)
	}
	defer envObj.Free()

	// Set __env global to hold our env object
	if err := setGlobal(vm, "__env", envObj); err != nil {
		return fmt.Errorf("setting __env: %w", err)
	}

	// Add plain vars
	if env.Vars != nil {
		for k, v := range env.Vars {
			js := fmt.Sprintf("globalThis.__env[%s] = %s;", jsEscape(k), jsEscape(v))
			if err := evalDiscard(vm, js); err != nil {
				return fmt.Errorf("setting var %q: %w", k, err)
			}
		}
	}

	// Add secrets
	if env.Secrets != nil {
		for k, v := range env.Secrets {
			js := fmt.Sprintf("globalThis.__env[%s] = %s;", jsEscape(k), jsEscape(v))
			if err := evalDiscard(vm, js); err != nil {
				return fmt.Errorf("setting secret %q: %w", k, err)
			}
		}
	}

	// Add KV bindings
	if env.KV != nil {
		for name := range env.KV {
			kvJS := fmt.Sprintf(`
				globalThis.__env[%s] = {
					get: function(key, opts) {
						if (arguments.length === 0) {
							return Promise.reject(new Error("get requires at least 1 argument"));
						}
						var type = (opts && opts.type) || "text";
						var reqID = String(globalThis.__requestID);
						var resultStr = __kv_get(reqID, %s, String(key), type);
						return new Promise(function(resolve, reject) {
							try {
								if (resultStr === "null") {
									resolve(null);
									return;
								}
								var result = JSON.parse(resultStr);
								var val = result.value;
								if (type === "json") {
									resolve(JSON.parse(val));
								} else if (type === "arrayBuffer") {
									var enc = new TextEncoder();
									resolve(enc.encode(val).buffer);
								} else if (type === "stream") {
									var enc = new TextEncoder();
									var bytes = enc.encode(val);
									resolve(new ReadableStream({
										start(controller) {
											controller.enqueue(bytes);
											controller.close();
										}
									}));
								} else {
									resolve(val);
								}
							} catch(e) {
								reject(e);
							}
						});
					},
					getWithMetadata: function(key, opts) {
						var type = (opts && opts.type) || "text";
						var reqID = String(globalThis.__requestID);
						var resultStr = __kv_get_with_metadata(reqID, %s, String(key), type);
						return new Promise(function(resolve, reject) {
							try {
								var result = JSON.parse(resultStr);
								if (result.value === null) {
									resolve({value: null, metadata: null});
									return;
								}
								var val = result.value;
								var processedVal = val;
								if (type === "json") {
									processedVal = JSON.parse(val);
								} else if (type === "arrayBuffer") {
									var enc = new TextEncoder();
									processedVal = enc.encode(val).buffer;
								} else if (type === "stream") {
									var enc = new TextEncoder();
									var bytes = enc.encode(val);
									processedVal = new ReadableStream({
										start(controller) {
											controller.enqueue(bytes);
											controller.close();
										}
									});
								}
								var metadata = result.metadata;
								if (typeof metadata === "string") {
									try { metadata = JSON.parse(metadata); } catch(e) {}
								}
								resolve({value: processedVal, metadata: metadata});
							} catch(e) {
								reject(e);
							}
						});
					},
					put: function(key, value, opts) {
						var reqID = String(globalThis.__requestID);
						var valueStr = typeof value === "string" ? value : JSON.stringify(value);
						var optsJSON = opts ? JSON.stringify({
							metadata: opts.metadata ? JSON.stringify(opts.metadata) : null,
							expirationTtl: opts.expirationTtl || null
						}) : "{}";
						return new Promise(function(resolve, reject) {
							try {
								var err = __kv_put(reqID, %s, String(key), valueStr, optsJSON);
								if (err) {
									reject(new Error(err));
								} else {
									resolve();
								}
							} catch(e) {
								reject(e);
							}
						});
					},
					delete: function(key) {
						if (arguments.length === 0) {
							return Promise.reject(new Error("delete requires at least 1 argument"));
						}
						var reqID = String(globalThis.__requestID);
						return new Promise(function(resolve, reject) {
							try {
								var err = __kv_delete(reqID, %s, String(key));
								if (err) {
									reject(new Error(err));
								} else {
									resolve();
								}
							} catch(e) {
								reject(e);
							}
						});
					},
					list: function(opts) {
						var reqID = String(globalThis.__requestID);
						var optsJSON = opts ? JSON.stringify({
							prefix: opts.prefix || "",
							limit: opts.limit || 1000,
							cursor: opts.cursor || ""
						}) : "{}";
						return new Promise(function(resolve, reject) {
							try {
								var resultStr = __kv_list(reqID, %s, optsJSON);
								resolve(JSON.parse(resultStr));
							} catch(e) {
								reject(e);
							}
						});
					}
				};
			`, jsEscape(name), jsEscape(name), jsEscape(name), jsEscape(name), jsEscape(name), jsEscape(name))
			if err := evalDiscard(vm, kvJS); err != nil {
				return fmt.Errorf("building KV binding %q: %w", name, err)
			}
		}
	}

	// Add Storage (R2) bindings
	if env.Storage != nil {
		for name := range env.Storage {
			storageJS := fmt.Sprintf(`
				globalThis.__env[%s] = {
					get: function(key) {
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
								resolve({
									key: obj.key,
									size: obj.size,
									etag: obj.etag,
									httpEtag: '"' + obj.etag + '"',
									version: obj.etag,
									httpMetadata: { contentType: obj.contentType || null },
									customMetadata: obj.customMetadata || {},
									uploaded: new Date(obj.uploaded),
									text: function() { return Promise.resolve(new TextDecoder().decode(bodyBytes)); },
									arrayBuffer: function() { return Promise.resolve(bodyBytes.buffer); },
									json: function() { return Promise.resolve(JSON.parse(new TextDecoder().decode(bodyBytes))); },
									bodyUsed: false
								});
							} catch(e) {
								reject(e);
							}
						});
					},
					put: function(key, value, opts) {
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
								reject(e);
							}
						});
					},
					delete: function(keys) {
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
									httpMetadata: { contentType: obj.contentType || null },
									customMetadata: obj.customMetadata || {},
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
						var reqID = String(globalThis.__requestID);
						return __r2_public_url(reqID, %s, String(key));
					}
				};
			`, jsEscape(name), jsEscape(name), jsEscape(name), jsEscape(name), jsEscape(name), jsEscape(name), jsEscape(name), jsEscape(name))
			if err := evalDiscard(vm, storageJS); err != nil {
				return fmt.Errorf("building Storage binding %q: %w", name, err)
			}
		}
	}

	// Add Queue bindings
	if env.Queues != nil {
		for name := range env.Queues {
			queueJS := fmt.Sprintf(`
				globalThis.__env[%s] = {
					send: function(body, opts) {
						var reqID = String(globalThis.__requestID);
						var bodyStr = typeof body === "string" ? body : JSON.stringify(body);
						var contentType = (opts && opts.contentType) || "json";
						return new Promise(function(resolve, reject) {
							try {
								var err = __queue_send(reqID, %s, bodyStr, contentType);
								if (err) {
									reject(new Error(err));
								} else {
									resolve();
								}
							} catch(e) {
								reject(e);
							}
						});
					},
					sendBatch: function(messages) {
						if (arguments.length === 0) {
							return Promise.reject(new TypeError("sendBatch requires an array argument"));
						}
						if (!Array.isArray(messages)) {
							return Promise.resolve();
						}
						var reqID = String(globalThis.__requestID);
						var formatted = messages.map(function(m) {
							return {
								body: typeof m.body === "string" ? m.body : JSON.stringify(m.body),
								contentType: m.contentType || "json"
							};
						});
						return new Promise(function(resolve, reject) {
							try {
								var err = __queue_send_batch(reqID, %s, JSON.stringify(formatted));
								if (err) {
									reject(new Error(err));
								} else {
									resolve();
								}
							} catch(e) {
								reject(e);
							}
						});
					}
				};
			`, jsEscape(name), jsEscape(name), jsEscape(name))
			if err := evalDiscard(vm, queueJS); err != nil {
				return fmt.Errorf("building Queue binding %q: %w", name, err)
			}
		}
	}

	// Add D1 bindings
	if env.D1Bindings != nil {
		state := getRequestState(reqID)
		for name, dbID := range env.D1Bindings {
			// Open D1 database and add to request state
			var bridge *D1Bridge
			var err error
			if env.D1DataDir != "" {
				bridge, err = OpenD1Database(env.D1DataDir, dbID)
			} else {
				bridge, err = NewD1BridgeMemory(dbID)
			}
			if err != nil {
				return fmt.Errorf("opening D1 database %q: %w", name, err)
			}
			if state != nil {
				state.d1Bridges = append(state.d1Bridges, bridge)
			}

			d1JS := fmt.Sprintf(`
				globalThis.__env[%s] = {
					prepare: function(sql) {
						var stmt = {
							_sql: sql,
							_bindings: [],
							bind: function() {
								var newStmt = Object.create(stmt);
								newStmt._bindings = Array.prototype.slice.call(arguments);
								return newStmt;
							},
							_exec: function() {
								var reqID = String(globalThis.__requestID);
								var resultStr = __d1_exec(reqID, %s, this._sql, JSON.stringify(this._bindings));
								var result = JSON.parse(resultStr);
								if (result.error) throw new Error(result.error);
								return result;
							},
							all: function() {
								try {
									var result = this._exec();
								} catch(e) { return Promise.reject(e); }
								var results = [];
								if (result.columns && result.rows) {
									for (var i = 0; i < result.rows.length; i++) {
										var obj = {};
										for (var j = 0; j < result.columns.length; j++) {
											obj[result.columns[j]] = result.rows[i][j];
										}
										results.push(obj);
									}
								}
								return Promise.resolve({ results: results, success: true, meta: result.meta || {} });
							},
							first: function(column) {
								try {
									var result = this._exec();
								} catch(e) { return Promise.reject(e); }
								if (!result.rows || result.rows.length === 0) {
									return Promise.resolve(null);
								}
								var row = {};
								for (var j = 0; j < result.columns.length; j++) {
									row[result.columns[j]] = result.rows[0][j];
								}
								if (column !== undefined && column !== null) {
									return Promise.resolve(row[column] !== undefined ? row[column] : null);
								}
								return Promise.resolve(row);
							},
							raw: function(opts) {
								try {
									var result = this._exec();
								} catch(e) { return Promise.reject(e); }
								var rows = result.rows || [];
								if (opts && opts.columnNames) {
									rows = [result.columns].concat(rows);
								}
								return Promise.resolve(rows);
							},
							run: function() {
								try {
									var result = this._exec();
								} catch(e) { return Promise.reject(e); }
								return Promise.resolve({ results: [], success: true, meta: result.meta || {} });
							}
						};
						return stmt;
					},
					batch: function(statements) {
						return new Promise(function(resolve, reject) {
							try {
								var results = [];
								for (var i = 0; i < statements.length; i++) {
									var stmt = statements[i];
									var result = stmt._exec();
									var rows = [];
									if (result.columns && result.rows) {
										for (var r = 0; r < result.rows.length; r++) {
											var obj = {};
											for (var c = 0; c < result.columns.length; c++) {
												obj[result.columns[c]] = result.rows[r][c];
											}
											rows.push(obj);
										}
									}
									results.push({ results: rows, success: true, meta: result.meta || {} });
								}
								resolve(results);
							} catch(e) {
								reject(e);
							}
						});
					},
					exec: function(sql) {
						var reqID = String(globalThis.__requestID);
						return new Promise(function(resolve, reject) {
							try {
								var statements = [];
								var current = '';
								var inStr = false;
								for (var i = 0; i < sql.length; i++) {
									var ch = sql[i];
									if (ch === "'" && !inStr) {
										inStr = true;
										current += ch;
									} else if (ch === "'" && inStr) {
										if (i + 1 < sql.length && sql[i + 1] === "'") {
											current += "''";
											i++;
										} else {
											inStr = false;
											current += ch;
										}
									} else if (ch === ';' && !inStr) {
										if (current.trim().length > 0) statements.push(current.trim());
										current = '';
									} else {
										current += ch;
									}
								}
								if (current.trim().length > 0) statements.push(current.trim());

								var count = 0;
								for (var i = 0; i < statements.length; i++) {
									var resultStr = __d1_exec(reqID, %s, statements[i], "[]");
									var result = JSON.parse(resultStr);
									if (result.error) throw new Error(result.error);
									count++;
								}
								resolve({ count: count, duration: 0 });
							} catch(e) {
								reject(e);
							}
						});
					},
					dump: function() {
						return Promise.reject(new Error("D1 dump() is not supported in this runtime"));
					}
				};
			`, jsEscape(name), jsEscape(dbID), jsEscape(dbID))
			if err := evalDiscard(vm, d1JS); err != nil {
				return fmt.Errorf("building D1 binding %q: %w", name, err)
			}
		}
	}

	// Add Durable Object bindings
	if env.DurableObjects != nil {
		for name := range env.DurableObjects {
			doJS := fmt.Sprintf(`
				globalThis.__env[%s] = {
					idFromName: function(name) {
						var hexID = __do_id_from_name(%s, String(name));
						return {
							_hex: hexID,
							toString: function() { return this._hex; },
							equals: function(other) { if (arguments.length === 0 || other === undefined || other === null) return false; return other._hex === this._hex; }
						};
					},
					idFromString: function(hexID) {
						return {
							_hex: String(hexID),
							toString: function() { return this._hex; },
							equals: function(other) { if (arguments.length === 0 || other === undefined || other === null) return false; return other._hex === this._hex; }
						};
					},
					newUniqueId: function() {
						var hexID = __do_unique_id();
						return {
							_hex: hexID,
							toString: function() { return this._hex; },
							equals: function(other) { if (arguments.length === 0 || other === undefined || other === null) return false; return other._hex === this._hex; }
						};
					},
					get: function(id) {
						var hexID = id._hex || String(id);
						var namespace = %s;
						return {
							id: id,
							fetch: function() {
								return Promise.resolve(new Response("ok"));
							},
							storage: {
								get: function(key) {
									var reqID = String(globalThis.__requestID);
									if (Array.isArray(key)) {
										var keysJSON = JSON.stringify(key);
										var resultStr = __do_storage_get_multi(reqID, namespace, hexID, keysJSON);
										var resultMap = JSON.parse(resultStr);
										var m = new Map();
										for (var k in resultMap) {
											m.set(k, JSON.parse(resultMap[k]));
										}
										return Promise.resolve(m);
									} else {
										var resultStr = __do_storage_get(reqID, namespace, hexID, String(key));
										if (resultStr === "null") {
											return Promise.resolve(null);
										}
										return Promise.resolve(JSON.parse(resultStr));
									}
								},
								put: function(keyOrEntries, value) {
									var reqID = String(globalThis.__requestID);
									if (arguments.length >= 2) {
										var valueJSON = JSON.stringify(value);
										__do_storage_put(reqID, namespace, hexID, String(keyOrEntries), valueJSON);
									} else {
										var entries = {};
										for (var k in keyOrEntries) {
											entries[k] = JSON.stringify(keyOrEntries[k]);
										}
										__do_storage_put_multi(reqID, namespace, hexID, JSON.stringify(entries));
									}
									return Promise.resolve();
								},
								delete: function(key) {
									var reqID = String(globalThis.__requestID);
									if (Array.isArray(key)) {
										var resultStr = __do_storage_delete_multi(reqID, namespace, hexID, JSON.stringify(key));
										var result = JSON.parse(resultStr);
										return Promise.resolve(result.count);
									} else {
										__do_storage_delete(reqID, namespace, hexID, String(key));
										return Promise.resolve(true);
									}
								},
								deleteAll: function() {
									var reqID = String(globalThis.__requestID);
									__do_storage_delete_all(reqID, namespace, hexID);
									return Promise.resolve();
								},
								list: function(opts) {
									var reqID = String(globalThis.__requestID);
									var optsJSON = opts ? JSON.stringify({
										prefix: opts.prefix || "",
										limit: opts.limit || 128,
										reverse: opts.reverse || false
									}) : "{}";
									var resultStr = __do_storage_list(reqID, namespace, hexID, optsJSON);
									var pairs = JSON.parse(resultStr);
									var m = new Map();
									for (var i = 0; i < pairs.length; i++) {
										m.set(pairs[i][0], JSON.parse(pairs[i][1]));
									}
									return Promise.resolve(m);
								}
							}
						};
					}
				};
			`, jsEscape(name), jsEscape(name), jsEscape(name))
			if err := evalDiscard(vm, doJS); err != nil {
				return fmt.Errorf("building DurableObject binding %q: %w", name, err)
			}
		}
	}

	// Add ServiceBinding bindings
	if env.ServiceBindings != nil {
		for name := range env.ServiceBindings {
			sbJS := fmt.Sprintf(`
				globalThis.__env[%s] = {
					fetch: function(input, init) {
						if (arguments.length === 0) {
							return Promise.reject(new Error('fetch() requires at least one argument'));
						}
						var reqID = String(globalThis.__requestID);
						return new Promise(function(resolve, reject) {
							try {
								var url = '', method = 'GET', headers = {}, body = null;
								if (typeof input === 'string') {
									url = input;
								} else if (input && typeof input === 'object') {
									url = input.url || '';
									method = input.method || 'GET';
									if (input.headers && input.headers._map) {
										for (var k in input.headers._map) headers[k] = input.headers._map[k];
									}
									body = input._body || null;
								}
								if (init) {
									if (init.method) method = init.method;
									if (init.headers) {
										if (init.headers._map) {
											for (var k in init.headers._map) headers[k] = init.headers._map[k];
										} else if (typeof init.headers === 'object') {
											for (var k in init.headers) headers[k] = init.headers[k];
										}
									}
									if (init.body !== undefined) body = String(init.body);
								}
								var reqJSON = JSON.stringify({
									url: url || 'https://fake-host/',
									method: method,
									headers: headers,
									body: body
								});
								var respStr = __sb_fetch(reqID, %s, reqJSON);
								var respData = JSON.parse(respStr);
								var h = new Headers();
								if (respData.headers) {
									for (var k in respData.headers) h.set(k, respData.headers[k]);
								}
								resolve(new Response(respData.body, { status: respData.status, headers: h }));
							} catch(e) {
								reject(e);
							}
						});
					}
				};
			`, jsEscape(name), jsEscape(name))
			if err := evalDiscard(vm, sbJS); err != nil {
				return fmt.Errorf("building ServiceBinding %q: %w", name, err)
			}
		}
	}

	// Add ASSETS binding
	if env.Assets != nil {
		assetsJS := `
			globalThis.__env.ASSETS = {
				fetch: function(input) {
					var reqID = String(globalThis.__requestID);
					return new Promise(function(resolve, reject) {
						try {
							var url = '', method = 'GET', headers = {}, body = null;
							if (typeof input === 'string') {
								url = input;
							} else if (input && typeof input === 'object') {
								url = input.url || '';
								method = input.method || 'GET';
								if (input.headers && input.headers._map) {
									headers = input.headers._map;
								}
								body = input._bodyStr || null;
							}
							var reqJSON = JSON.stringify({
								url: url,
								method: method,
								headers: headers,
								body: body
							});
							var respStr = __assets_fetch(reqID, reqJSON);
							var respData = JSON.parse(respStr);
							var h = new Headers();
							if (respData.headers) {
								for (var k in respData.headers) h.set(k, respData.headers[k]);
							}
							resolve(new Response(respData.body, { status: respData.status, headers: h }));
						} catch(e) {
							reject(e);
						}
					});
				}
			};
		`
		if err := evalDiscard(vm, assetsJS); err != nil {
			return fmt.Errorf("building ASSETS binding: %w", err)
		}
	}

	// Add CustomBindings
	if env.CustomBindings != nil {
		for name, bindFn := range env.CustomBindings {
			val, err := bindFn(vm)
			if err != nil {
				return fmt.Errorf("building custom binding %q: %w", name, err)
			}
			// Store the custom binding value as a temporary global, then assign it to __env.
			tmpName := fmt.Sprintf("__custom_bind_%s", name)
			if err := setGlobal(vm, tmpName, val); err != nil {
				val.Free()
				return fmt.Errorf("setting temp global for custom binding %q: %w", name, err)
			}
			val.Free()

			js := fmt.Sprintf("globalThis.__env[%s] = globalThis[%s]; delete globalThis[%s];",
				jsEscape(name), jsEscape(tmpName), jsEscape(tmpName))
			if err := evalDiscard(vm, js); err != nil {
				return fmt.Errorf("assigning custom binding %q to __env: %w", name, err)
			}
		}
	}

	return nil
}
