package worker

import (
	"encoding/json"
	"fmt"
	"testing"

	"modernc.org/quickjs"
)

// ---------------------------------------------------------------------------
// Go bridge tests (using mockDurableObjectStore)
// ---------------------------------------------------------------------------

func TestDurableBridge_PutAndGet(t *testing.T) {
	b := newMockDurableObjectStore()

	if err := b.Put("ns1", "obj1", "greeting", `"hello"`); err != nil {
		t.Fatalf("Put: %v", err)
	}

	val, err := b.Get("ns1", "obj1", "greeting")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != `"hello"` {
		t.Errorf("Get = %q, want %q", val, `"hello"`)
	}
}

func TestDurableBridge_GetNotFound(t *testing.T) {
	b := newMockDurableObjectStore()

	val, err := b.Get("ns1", "obj1", "missing")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "" {
		t.Errorf("Get = %q, want empty", val)
	}
}

func TestDurableBridge_PutOverwrite(t *testing.T) {
	b := newMockDurableObjectStore()

	_ = b.Put("ns1", "obj1", "key", `"v1"`)
	_ = b.Put("ns1", "obj1", "key", `"v2"`)

	val, err := b.Get("ns1", "obj1", "key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != `"v2"` {
		t.Errorf("Get = %q, want %q", val, `"v2"`)
	}
}

func TestDurableBridge_Delete(t *testing.T) {
	b := newMockDurableObjectStore()

	_ = b.Put("ns1", "obj1", "key", `"val"`)
	if err := b.Delete("ns1", "obj1", "key"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	val, err := b.Get("ns1", "obj1", "key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "" {
		t.Errorf("Get after delete = %q, want empty", val)
	}
}

func TestDurableBridge_DeleteAll(t *testing.T) {
	b := newMockDurableObjectStore()

	_ = b.Put("ns1", "obj1", "a", `1`)
	_ = b.Put("ns1", "obj1", "b", `2`)
	_ = b.Put("ns1", "obj1", "c", `3`)

	if err := b.DeleteAll("ns1", "obj1"); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}

	entries, err := b.List("ns1", "obj1", "", 0, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("List after deleteAll: got %d entries, want 0", len(entries))
	}
}

func TestDurableBridge_DeleteMulti(t *testing.T) {
	b := newMockDurableObjectStore()

	_ = b.Put("ns1", "obj1", "a", `1`)
	_ = b.Put("ns1", "obj1", "b", `2`)
	_ = b.Put("ns1", "obj1", "c", `3`)

	count, err := b.DeleteMulti("ns1", "obj1", []string{"a", "b"})
	if err != nil {
		t.Fatalf("DeleteMulti: %v", err)
	}
	if count != 2 {
		t.Errorf("DeleteMulti count = %d, want 2", count)
	}

	val, _ := b.Get("ns1", "obj1", "c")
	if val != `3` {
		t.Errorf("remaining entry: %q, want %q", val, `3`)
	}
}

func TestDurableBridge_ListPrefix(t *testing.T) {
	b := newMockDurableObjectStore()

	_ = b.Put("ns1", "obj1", "user:1", `"alice"`)
	_ = b.Put("ns1", "obj1", "user:2", `"bob"`)
	_ = b.Put("ns1", "obj1", "other:1", `"nope"`)

	entries, err := b.List("ns1", "obj1", "user:", 0, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("List count = %d, want 2", len(entries))
	}
}

func TestDurableBridge_GetMulti(t *testing.T) {
	b := newMockDurableObjectStore()

	_ = b.Put("ns1", "obj1", "a", `"alpha"`)
	_ = b.Put("ns1", "obj1", "b", `"bravo"`)

	result, err := b.GetMulti("ns1", "obj1", []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("GetMulti: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("GetMulti count = %d, want 2", len(result))
	}
	if result["a"] != `"alpha"` {
		t.Errorf("a = %q", result["a"])
	}
}

func TestDurableBridge_CrossSiteIsolation(t *testing.T) {
	b := newMockDurableObjectStore()

	// Site A uses prefixed namespace "siteA:ns1", site B uses "siteB:ns1"
	nsA := "siteA:ns1"
	nsB := "siteB:ns1"
	objID := "shared-obj-id"

	// Put data in site A's namespace
	if err := b.Put(nsA, objID, "secret", `"site-a-data"`); err != nil {
		t.Fatalf("Put siteA: %v", err)
	}

	// Site B should NOT see site A's data
	val, err := b.Get(nsB, objID, "secret")
	if err != nil {
		t.Fatalf("Get siteB: %v", err)
	}
	if val != "" {
		t.Errorf("site B should not see site A's data, got %q", val)
	}

	// Put data in site B's namespace
	if err := b.Put(nsB, objID, "secret", `"site-b-data"`); err != nil {
		t.Fatalf("Put siteB: %v", err)
	}

	// Each site should see only its own data
	valA, _ := b.Get(nsA, objID, "secret")
	valB, _ := b.Get(nsB, objID, "secret")
	if valA != `"site-a-data"` {
		t.Errorf("site A data = %q, want \"site-a-data\"", valA)
	}
	if valB != `"site-b-data"` {
		t.Errorf("site B data = %q, want \"site-b-data\"", valB)
	}
}

func TestDurableID_NamespaceIsolation(t *testing.T) {
	// Same object name in different site-prefixed namespaces should produce different IDs
	idA := durableObjectID("siteA:ns1", "myobj")
	idB := durableObjectID("siteB:ns1", "myobj")
	if idA == idB {
		t.Error("same name in different site-prefixed namespaces should produce different IDs")
	}
}

func TestDurableID_Deterministic(t *testing.T) {
	id1 := durableObjectID("ns", "myobj")
	id2 := durableObjectID("ns", "myobj")
	if id1 != id2 {
		t.Errorf("idFromName not deterministic: %q != %q", id1, id2)
	}
	if len(id1) != 64 {
		t.Errorf("id length = %d, want 64 (sha256 hex)", len(id1))
	}
}

func TestDurableID_DifferentInputs(t *testing.T) {
	id1 := durableObjectID("ns", "obj1")
	id2 := durableObjectID("ns", "obj2")
	if id1 == id2 {
		t.Error("different names should produce different IDs")
	}
}

func TestDurableUniqueID(t *testing.T) {
	id1, err := durableObjectUniqueID()
	if err != nil {
		t.Fatal(err)
	}
	id2, err := durableObjectUniqueID()
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Error("newUniqueId should return different IDs")
	}
	if len(id1) != 32 {
		t.Errorf("unique id length = %d, want 32", len(id1))
	}
}

// ---------------------------------------------------------------------------
// JS-level binding tests
// ---------------------------------------------------------------------------

// doTestCtx creates a QuickJS VM with the Response polyfill and
// a Durable Object binding on globalThis.MY_DO for testing.
func doTestCtx(t *testing.T) (*quickjs.VM, *mockDurableObjectStore) {
	t.Helper()
	mock := newMockDurableObjectStore()

	vm, err := quickjs.NewVM()
	if err != nil {
		t.Fatalf("NewVM: %v", err)
	}
	el := newEventLoop()

	t.Cleanup(func() {
		vm.Close()
	})

	// Set up Response and Response.json polyfills needed for tests.
	if err := setupWebAPIs(vm, el); err != nil {
		t.Fatalf("setupWebAPIs: %v", err)
	}

	// Register __do_* global functions
	if err := setupDurableObjects(vm, el); err != nil {
		t.Fatalf("setupDurableObjects: %v", err)
	}

	// Create request state with the mock store in env
	env := &Env{
		Vars:           make(map[string]string),
		Secrets:        make(map[string]string),
		DurableObjects: map[string]DurableObjectStore{"MY_DO": mock},
	}
	reqID := newRequestState(10, env)

	// Set __requestID global for storage operations
	if err := evalDiscard(vm, fmt.Sprintf("globalThis.__requestID = %d", reqID)); err != nil {
		t.Fatalf("setting __requestID: %v", err)
	}

	// Initialize __env object
	if err := evalDiscard(vm, "globalThis.__env = {}"); err != nil {
		t.Fatalf("initializing __env: %v", err)
	}

	// Build the DO binding JS object using the same template from assets.go
	doJS := fmt.Sprintf(`
		globalThis.__env["MY_DO"] = {
			idFromName: function(name) {
				var hexID = __do_id_from_name("MY_DO", String(name));
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
				var namespace = "MY_DO";
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
		globalThis.MY_DO = globalThis.__env["MY_DO"];
	`)
	if err := evalDiscard(vm, doJS); err != nil {
		t.Fatalf("building DurableObject binding: %v", err)
	}

	t.Cleanup(func() {
		clearRequestState(reqID)
	})

	return vm, mock
}

// runDOScript runs a JS script in the DO test context and returns the JSON result.
func runDOScript(t *testing.T, vm *quickjs.VM, script string) map[string]interface{} {
	t.Helper()

	// Evaluate the script and convert to JSON
	jsonStr, err := evalString(vm, fmt.Sprintf("JSON.stringify((%s))", script))
	if err != nil {
		t.Fatalf("JSON.stringify: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		t.Fatalf("unmarshal %q: %v", jsonStr, err)
	}
	return result
}

func TestDurable_JSIdFromName(t *testing.T) {
	vm, _ := doTestCtx(t)

	data := runDOScript(t, vm, `(function() {
		var id = MY_DO.idFromName("test-object");
		var id2 = MY_DO.idFromName("test-object");
		var id3 = MY_DO.idFromName("different-object");
		return {
			hex: id.toString(),
			hexLen: id.toString().length,
			same: id.toString() === id2.toString(),
			different: id.toString() !== id3.toString(),
			equals: id.equals(id2),
			notEquals: !id.equals(id3),
		};
	})()`)

	if data["hex"] == "" {
		t.Error("hex should not be empty")
	}
	if data["hexLen"].(float64) != 64 {
		t.Errorf("hex length = %v, want 64", data["hexLen"])
	}
	if data["same"] != true {
		t.Error("same name should produce same ID")
	}
	if data["different"] != true {
		t.Error("different names should produce different IDs")
	}
	if data["equals"] != true {
		t.Error("equals should return true for same IDs")
	}
	if data["notEquals"] != true {
		t.Error("notEquals should be true for different IDs")
	}
}

func TestDurable_JSIdFromString(t *testing.T) {
	vm, _ := doTestCtx(t)

	data := runDOScript(t, vm, `(function() {
		var id = MY_DO.idFromName("test-object");
		var hex = id.toString();
		var id2 = MY_DO.idFromString(hex);
		return {
			roundTrip: id2.toString() === hex,
			equals: id.equals(id2),
		};
	})()`)

	if data["roundTrip"] != true {
		t.Error("idFromString round-trip failed")
	}
	if data["equals"] != true {
		t.Error("equals should be true after round-trip")
	}
}

func TestDurable_JSNewUniqueId(t *testing.T) {
	vm, _ := doTestCtx(t)

	data := runDOScript(t, vm, `(function() {
		var id1 = MY_DO.newUniqueId();
		var id2 = MY_DO.newUniqueId();
		return {
			hex1: id1.toString(),
			hex2: id2.toString(),
			len1: id1.toString().length,
			different: id1.toString() !== id2.toString(),
			notEquals: !id1.equals(id2),
		};
	})()`)

	if data["hex1"] == "" || data["hex2"] == "" {
		t.Error("unique IDs should not be empty")
	}
	if data["len1"].(float64) != 32 {
		t.Errorf("unique id length = %v, want 32", data["len1"])
	}
	if data["different"] != true {
		t.Error("newUniqueId should return different IDs")
	}
	if data["notEquals"] != true {
		t.Error("notEquals should be true for unique IDs")
	}
}

func TestDurable_JSIdEquals(t *testing.T) {
	vm, _ := doTestCtx(t)

	data := runDOScript(t, vm, `(function() {
		var id1 = MY_DO.idFromName("same");
		var id2 = MY_DO.idFromName("same");
		var id3 = MY_DO.idFromName("other");
		return {
			sameEquals: id1.equals(id2),
			diffNotEquals: !id1.equals(id3),
			noArgFalse: id1.equals() === false,
		};
	})()`)

	if data["sameEquals"] != true {
		t.Error("equals should be true for same name")
	}
	if data["diffNotEquals"] != true {
		t.Error("notEquals should be true for different names")
	}
	if data["noArgFalse"] != true {
		t.Error("equals() with no arg should return false")
	}
}

// Note: The storage-related tests would require async/promise handling
// which is more complex in quickjs. For brevity, I'm including the
// integration tests that use the full engine below.

// ---------------------------------------------------------------------------
// Integration tests — exercise Durable Objects through the full engine pipeline
// ---------------------------------------------------------------------------

// doEnv creates an Env with a Durable Object binding wired up.
func doEnv() *Env {
	return &Env{
		Vars:           make(map[string]string),
		Secrets:        make(map[string]string),
		DurableObjects: map[string]DurableObjectStore{"MY_DO": newMockDurableObjectStore()},
	}
}

func TestDurableIntegration_IdFromName(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    var id = env.MY_DO.idFromName("test");
    var id2 = env.MY_DO.idFromName("test");
    var id3 = env.MY_DO.idFromName("other");
    return Response.json({
      hex: id.toString(),
      hexLen: id.toString().length,
      same: id.toString() === id2.toString(),
      different: id.toString() !== id3.toString(),
    });
  },
};`

	env := doEnv()
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Hex       string `json:"hex"`
		HexLen    int    `json:"hexLen"`
		Same      bool   `json:"same"`
		Different bool   `json:"different"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.HexLen != 64 {
		t.Errorf("hexLen = %d, want 64", data.HexLen)
	}
	if !data.Same {
		t.Error("same name should produce same ID")
	}
	if !data.Different {
		t.Error("different names should produce different IDs")
	}
}

func TestDurableIntegration_GetStubFetch(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    var id = env.MY_DO.idFromName("fetch-obj");
    var stub = env.MY_DO.get(id);
    var resp = await stub.fetch("http://fake/");
    var text = await resp.text();
    return Response.json({ status: resp.status, body: text });
  },
};`

	env := doEnv()
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Status != 200 {
		t.Errorf("status = %d, want 200", data.Status)
	}
	if data.Body != "ok" {
		t.Errorf("body = %q, want ok", data.Body)
	}
}

func TestDurableIntegration_StoragePutGetDelete(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    var id = env.MY_DO.idFromName("storage-obj");
    var stub = env.MY_DO.get(id);

    // put and get
    await stub.storage.put("greeting", "hello world");
    var val = await stub.storage.get("greeting");

    // put object
    await stub.storage.put("data", { name: "alice", age: 30 });
    var obj = await stub.storage.get("data");

    // delete
    await stub.storage.put("temp", "gone");
    await stub.storage.delete("temp");
    var afterDel = await stub.storage.get("temp");

    return Response.json({
      val: val,
      name: obj.name,
      age: obj.age,
      afterDelNull: afterDel === null,
    });
  },
};`

	env := doEnv()
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Val          string `json:"val"`
		Name         string `json:"name"`
		Age          int    `json:"age"`
		AfterDelNull bool   `json:"afterDelNull"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Val != "hello world" {
		t.Errorf("val = %q, want hello world", data.Val)
	}
	if data.Name != "alice" {
		t.Errorf("name = %q, want alice", data.Name)
	}
	if data.Age != 30 {
		t.Errorf("age = %d, want 30", data.Age)
	}
	if !data.AfterDelNull {
		t.Error("after delete should be null")
	}
}

func TestDurableIntegration_StorageDeleteAllAndList(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    var id = env.MY_DO.idFromName("list-obj");
    var stub = env.MY_DO.get(id);

    await stub.storage.put("user:1", "alice");
    await stub.storage.put("user:2", "bob");
    await stub.storage.put("other:1", "nope");

    var all = await stub.storage.list();
    var prefixed = await stub.storage.list({ prefix: "user:" });

    await stub.storage.deleteAll();
    var afterClear = await stub.storage.list();

    return Response.json({
      allSize: all.size,
      prefixedSize: prefixed.size,
      afterClearSize: afterClear.size,
    });
  },
};`

	env := doEnv()
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		AllSize        int `json:"allSize"`
		PrefixedSize   int `json:"prefixedSize"`
		AfterClearSize int `json:"afterClearSize"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.AllSize != 3 {
		t.Errorf("allSize = %d, want 3", data.AllSize)
	}
	if data.PrefixedSize != 2 {
		t.Errorf("prefixedSize = %d, want 2", data.PrefixedSize)
	}
	if data.AfterClearSize != 0 {
		t.Errorf("afterClearSize = %d, want 0", data.AfterClearSize)
	}
}

func TestDurableIntegration_NoBinding(t *testing.T) {
	e := newTestEngine(t)

	// Worker tries to access env.MY_DO when no binding is configured.
	source := `export default {
  async fetch(request, env) {
    var hasDO = typeof env.MY_DO !== 'undefined';
    return Response.json({ hasDO: hasDO });
  },
};`

	env := defaultEnv()
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		HasDO bool `json:"hasDO"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.HasDO {
		t.Error("env.MY_DO should not exist when no binding is configured")
	}
}

func TestDurableIntegration_MultipleBindings(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    // Two separate DO namespaces
    var id1 = env.DO_A.idFromName("shared-name");
    var id2 = env.DO_B.idFromName("shared-name");

    var stub1 = env.DO_A.get(id1);
    var stub2 = env.DO_B.get(id2);

    await stub1.storage.put("key", "from-A");
    await stub2.storage.put("key", "from-B");

    var val1 = await stub1.storage.get("key");
    var val2 = await stub2.storage.get("key");

    return Response.json({
      val1: val1,
      val2: val2,
      isolated: val1 !== val2,
    });
  },
};`

	env := &Env{
		Vars:           make(map[string]string),
		Secrets:        make(map[string]string),
		DurableObjects: map[string]DurableObjectStore{"DO_A": newMockDurableObjectStore(), "DO_B": newMockDurableObjectStore()},
	}
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Val1     string `json:"val1"`
		Val2     string `json:"val2"`
		Isolated bool   `json:"isolated"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Val1 != "from-A" {
		t.Errorf("val1 = %q, want from-A", data.Val1)
	}
	if data.Val2 != "from-B" {
		t.Errorf("val2 = %q, want from-B", data.Val2)
	}
	if !data.Isolated {
		t.Error("different namespaces should have isolated storage")
	}
}

// TestDurableIntegration_CombinedBindings exercises buildEnvObject with KV + D1 + DO together.
func TestDurableIntegration_CombinedBindings(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    var hasKV = typeof env.MY_KV !== 'undefined' && typeof env.MY_KV.get === 'function';
    var hasD1 = typeof env.MY_DB !== 'undefined' && typeof env.MY_DB.exec === 'function';
    var hasDO = typeof env.MY_DO !== 'undefined' && typeof env.MY_DO.idFromName === 'function';
    var hasQueue = typeof env.MY_QUEUE !== 'undefined' && typeof env.MY_QUEUE.send === 'function';
    return Response.json({ hasKV, hasD1, hasDO, hasQueue });
  },
};`

	env := &Env{
		Vars:           map[string]string{"FOO": "bar"},
		Secrets:        map[string]string{"SECRET": "shh"},
		KV:             map[string]KVStore{"MY_KV": newMockKVStore()},
		Queues:         map[string]QueueSender{"MY_QUEUE": &mockQueueSender{}},
		D1Bindings:     map[string]string{"MY_DB": "db1"},
		DurableObjects: map[string]DurableObjectStore{"MY_DO": newMockDurableObjectStore()},
	}
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		HasKV    bool `json:"hasKV"`
		HasD1    bool `json:"hasD1"`
		HasDO    bool `json:"hasDO"`
		HasQueue bool `json:"hasQueue"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if !data.HasKV {
		t.Error("should have KV binding")
	}
	if !data.HasD1 {
		t.Error("should have D1 binding")
	}
	if !data.HasDO {
		t.Error("should have DO binding")
	}
	if !data.HasQueue {
		t.Error("should have Queue binding")
	}
}
