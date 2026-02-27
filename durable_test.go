package worker

import (
	"encoding/json"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Durable Objects JS-level binding tests
// ---------------------------------------------------------------------------

// doTestSetup creates an engine and env with a DurableObjectStore bound to "MY_DO".
func doTestSetup(t *testing.T) (*Engine, *Env, *mockDurableObjectStore) {
	t.Helper()
	e := newTestEngine(t)
	store := newMockDurableObjectStore()
	env := &Env{
		Vars:    make(map[string]string),
		Secrets: make(map[string]string),
		DurableObjects: map[string]DurableObjectStore{
			"MY_DO": store,
		},
	}
	return e, env, store
}

// ---------------------------------------------------------------------------
// 1. Basic get/put — null returned for missing, value returned after put
// ---------------------------------------------------------------------------

func TestDO_PutAndGet(t *testing.T) {
	e, env, _ := doTestSetup(t)

	// Values stored via storage.put are JSON-serialized automatically by the JS bridge.
	source := `export default {
  async fetch(request, env) {
    const stub = env.MY_DO.get("obj1");
    await stub.storage.put("counter", 42);
    const val = await stub.storage.get("counter");
    return new Response(String(val));
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)
	if string(r.Response.Body) != "42" {
		t.Errorf("body = %q, want %q", r.Response.Body, "42")
	}
}

func TestDO_GetMissingKeyReturnsNull(t *testing.T) {
	e, env, _ := doTestSetup(t)

	// The bridge returns "null" for missing keys, which JSON.parse yields null (not undefined).
	source := `export default {
  async fetch(request, env) {
    const stub = env.MY_DO.get("obj1");
    const val = await stub.storage.get("nonexistent");
    return Response.json({ val: val, isNull: val === null });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]interface{}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["isNull"] != true {
		t.Errorf("isNull = %v, want true (missing keys return null)", data["isNull"])
	}
}

func TestDO_OverwriteValue(t *testing.T) {
	e, env, _ := doTestSetup(t)

	source := `export default {
  async fetch(request, env) {
    const stub = env.MY_DO.get("obj1");
    await stub.storage.put("k", "first");
    await stub.storage.put("k", "second");
    const val = await stub.storage.get("k");
    return new Response(val);
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)
	if string(r.Response.Body) != "second" {
		t.Errorf("body = %q, want %q", r.Response.Body, "second")
	}
}

// ---------------------------------------------------------------------------
// 2. Delete — single key
// ---------------------------------------------------------------------------

func TestDO_DeleteKey(t *testing.T) {
	e, env, _ := doTestSetup(t)

	// After delete, get returns null.
	source := `export default {
  async fetch(request, env) {
    const stub = env.MY_DO.get("obj1");
    await stub.storage.put("key", "value");
    await stub.storage.delete("key");
    const val = await stub.storage.get("key");
    return Response.json({ val: val, gone: val === null });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]interface{}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["gone"] != true {
		t.Errorf("gone = %v, want true", data["gone"])
	}
}

func TestDO_DeleteNonexistentKeyIsNoop(t *testing.T) {
	e, env, _ := doTestSetup(t)

	source := `export default {
  async fetch(request, env) {
    const stub = env.MY_DO.get("obj1");
    // Deleting a key that does not exist should not throw.
    const result = await stub.storage.delete("ghost");
    return new Response(String(result));
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)
	if string(r.Response.Body) != "true" {
		t.Errorf("body = %q, want %q", r.Response.Body, "true")
	}
}

// ---------------------------------------------------------------------------
// 3. Multi-key delete via storage.delete([array])
// ---------------------------------------------------------------------------

func TestDO_DeleteMultiViaArray(t *testing.T) {
	e, env, _ := doTestSetup(t)

	// storage.delete([...]) returns the count of deleted keys.
	source := `export default {
  async fetch(request, env) {
    const stub = env.MY_DO.get("obj1");
    await stub.storage.put("p", 1);
    await stub.storage.put("q", 2);
    await stub.storage.put("r", 3);
    const deleted = await stub.storage.delete(["p", "q"]);
    const pVal = await stub.storage.get("p");
    const rVal = await stub.storage.get("r");
    return Response.json({ deleted, pGone: pVal === null, rStays: rVal === 3 });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]interface{}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["deleted"] != float64(2) {
		t.Errorf("deleted = %v, want 2", data["deleted"])
	}
	if data["pGone"] != true {
		t.Errorf("pGone = %v, want true", data["pGone"])
	}
	if data["rStays"] != true {
		t.Errorf("rStays = %v, want true", data["rStays"])
	}
}

// ---------------------------------------------------------------------------
// 4. Multi-key get via storage.get([array]) — returns a Map
// ---------------------------------------------------------------------------

func TestDO_GetMultiViaArray(t *testing.T) {
	e, env, _ := doTestSetup(t)

	// storage.get([keys]) returns a Map; use Map.get() to read values.
	source := `export default {
  async fetch(request, env) {
    const stub = env.MY_DO.get("obj1");
    await stub.storage.put("a", 1);
    await stub.storage.put("b", 2);
    await stub.storage.put("c", 3);
    const result = await stub.storage.get(["a", "b", "missing"]);
    return Response.json({
      a: result.get("a"),
      b: result.get("b"),
      hasMissing: result.has("missing"),
      size: result.size,
    });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]interface{}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["a"] != float64(1) {
		t.Errorf("a = %v, want 1", data["a"])
	}
	if data["b"] != float64(2) {
		t.Errorf("b = %v, want 2", data["b"])
	}
	// "missing" should not be in the Map
	if data["hasMissing"] != false {
		t.Errorf("hasMissing = %v, want false", data["hasMissing"])
	}
}

// ---------------------------------------------------------------------------
// 5. Multi-key put via storage.put(object) — overloaded signature
// ---------------------------------------------------------------------------

func TestDO_PutMultiViaObject(t *testing.T) {
	e, env, _ := doTestSetup(t)

	// storage.put(obj) with a single object argument triggers putMulti.
	source := `export default {
  async fetch(request, env) {
    const stub = env.MY_DO.get("obj1");
    await stub.storage.put({ x: 10, y: 20, z: 30 });
    const x = await stub.storage.get("x");
    const y = await stub.storage.get("y");
    const z = await stub.storage.get("z");
    return Response.json({ x, y, z });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]interface{}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["x"] != float64(10) || data["y"] != float64(20) || data["z"] != float64(30) {
		t.Errorf("data = %v, want x=10 y=20 z=30", data)
	}
}

// ---------------------------------------------------------------------------
// 6. deleteAll — clears all keys for this objectID
// ---------------------------------------------------------------------------

func TestDO_DeleteAll(t *testing.T) {
	e, env, _ := doTestSetup(t)

	source := `export default {
  async fetch(request, env) {
    const stub = env.MY_DO.get("obj1");
    await stub.storage.put("a", 1);
    await stub.storage.put("b", 2);
    await stub.storage.put("c", 3);
    await stub.storage.deleteAll();
    const a = await stub.storage.get("a");
    const b = await stub.storage.get("b");
    return Response.json({ aGone: a === null, bGone: b === null });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]interface{}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["aGone"] != true {
		t.Errorf("aGone = %v, want true", data["aGone"])
	}
	if data["bGone"] != true {
		t.Errorf("bGone = %v, want true", data["bGone"])
	}
}

func TestDO_DeleteAllDoesNotAffectOtherObjects(t *testing.T) {
	e, env, store := doTestSetup(t)

	// Pre-seed obj2 directly via Go with a JSON-encoded value.
	if err := store.Put("MY_DO", "obj2", "safe", `"safe-value"`); err != nil {
		t.Fatalf("Go Put: %v", err)
	}

	source := `export default {
  async fetch(request, env) {
    const stub1 = env.MY_DO.get("obj1");
    await stub1.storage.put("k", "doomed");
    await stub1.storage.deleteAll();
    const k = await stub1.storage.get("k");

    // obj2 data should survive obj1's deleteAll.
    const stub2 = env.MY_DO.get("obj2");
    const safe = await stub2.storage.get("safe");
    return Response.json({ k1Gone: k === null, safe });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]interface{}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["k1Gone"] != true {
		t.Errorf("k1Gone = %v, want true", data["k1Gone"])
	}
	if data["safe"] != "safe-value" {
		t.Errorf("safe = %v, want %q", data["safe"], "safe-value")
	}
}

// ---------------------------------------------------------------------------
// 7. List — returns a Map ordered by key
// ---------------------------------------------------------------------------

func TestDO_ListWithPrefix(t *testing.T) {
	e, env, _ := doTestSetup(t)

	// list() returns a Map; spread keys via Array.from(map.keys()).
	source := `export default {
  async fetch(request, env) {
    const stub = env.MY_DO.get("obj1");
    await stub.storage.put("item:a", "alpha");
    await stub.storage.put("item:b", "beta");
    await stub.storage.put("item:c", "gamma");
    await stub.storage.put("other", "skip");
    const m = await stub.storage.list({ prefix: "item:" });
    const keys = Array.from(m.keys());
    return Response.json({ count: m.size, keys });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Count int      `json:"count"`
		Keys  []string `json:"keys"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Count != 3 {
		t.Errorf("count = %d, want 3", data.Count)
	}
	for _, k := range data.Keys {
		if !strings.HasPrefix(k, "item:") {
			t.Errorf("unexpected key %q in list result", k)
		}
	}
}

func TestDO_ListWithLimit(t *testing.T) {
	e, env, _ := doTestSetup(t)

	source := `export default {
  async fetch(request, env) {
    const stub = env.MY_DO.get("obj1");
    for (let i = 0; i < 5; i++) {
      await stub.storage.put("k" + i, i);
    }
    const m = await stub.storage.list({ limit: 2 });
    return Response.json({ count: m.size });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Count != 2 {
		t.Errorf("count = %d, want 2", data.Count)
	}
}

func TestDO_ListReverse(t *testing.T) {
	e, env, _ := doTestSetup(t)

	// Map preserves insertion order; the bridge inserts in reverse order when reverse:true.
	source := `export default {
  async fetch(request, env) {
    const stub = env.MY_DO.get("obj1");
    await stub.storage.put("a", 1);
    await stub.storage.put("b", 2);
    await stub.storage.put("c", 3);
    const fwd = await stub.storage.list({ reverse: false });
    const rev = await stub.storage.list({ reverse: true });
    const fwdKeys = Array.from(fwd.keys());
    const revKeys = Array.from(rev.keys());
    return Response.json({
      firstFwd: fwdKeys[0],
      firstRev: revKeys[0],
    });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]string
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["firstFwd"] != "a" {
		t.Errorf("firstFwd = %q, want %q", data["firstFwd"], "a")
	}
	if data["firstRev"] != "c" {
		t.Errorf("firstRev = %q, want %q", data["firstRev"], "c")
	}
}

func TestDO_ListEmpty(t *testing.T) {
	e, env, _ := doTestSetup(t)

	source := `export default {
  async fetch(request, env) {
    const stub = env.MY_DO.get("emptyobj");
    const m = await stub.storage.list({});
    return Response.json({ count: m.size });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Count != 0 {
		t.Errorf("count = %d, want 0", data.Count)
	}
}

// ---------------------------------------------------------------------------
// 8. Object isolation — different objectIDs are separate namespaces
// ---------------------------------------------------------------------------

func TestDO_ObjectIsolation(t *testing.T) {
	e, env, _ := doTestSetup(t)

	source := `export default {
  async fetch(request, env) {
    const obj1 = env.MY_DO.get("user:1");
    const obj2 = env.MY_DO.get("user:2");
    await obj1.storage.put("score", 100);
    await obj2.storage.put("score", 200);
    const s1 = await obj1.storage.get("score");
    const s2 = await obj2.storage.get("score");
    return Response.json({ s1, s2 });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]interface{}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["s1"] != float64(100) {
		t.Errorf("s1 = %v, want 100", data["s1"])
	}
	if data["s2"] != float64(200) {
		t.Errorf("s2 = %v, want 200", data["s2"])
	}
}

// ---------------------------------------------------------------------------
// 9. Namespace isolation — separate DO bindings don't share storage
// ---------------------------------------------------------------------------

func TestDO_NamespaceIsolation(t *testing.T) {
	e := newTestEngine(t)

	store1 := newMockDurableObjectStore()
	store2 := newMockDurableObjectStore()
	env := &Env{
		Vars:    make(map[string]string),
		Secrets: make(map[string]string),
		DurableObjects: map[string]DurableObjectStore{
			"NS1": store1,
			"NS2": store2,
		},
	}

	source := `export default {
  async fetch(request, env) {
    const o1 = env.NS1.get("obj");
    const o2 = env.NS2.get("obj");
    await o1.storage.put("k", "ns1-value");
    await o2.storage.put("k", "ns2-value");
    const v1 = await o1.storage.get("k");
    const v2 = await o2.storage.get("k");
    return Response.json({ v1, v2 });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]string
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["v1"] != "ns1-value" {
		t.Errorf("v1 = %q, want %q", data["v1"], "ns1-value")
	}
	if data["v2"] != "ns2-value" {
		t.Errorf("v2 = %q, want %q", data["v2"], "ns2-value")
	}
}

// ---------------------------------------------------------------------------
// 10. JSON value round-trip
// ---------------------------------------------------------------------------

func TestDO_JSONValueRoundtrip(t *testing.T) {
	e, env, _ := doTestSetup(t)

	source := `export default {
  async fetch(request, env) {
    const stub = env.MY_DO.get("obj1");
    const orig = { name: "alice", scores: [10, 20, 30], active: true };
    await stub.storage.put("profile", orig);
    const parsed = await stub.storage.get("profile");
    return Response.json({
      name: parsed.name,
      total: parsed.scores.reduce((a, b) => a + b, 0),
      active: parsed.active,
    });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]interface{}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["name"] != "alice" {
		t.Errorf("name = %v, want %q", data["name"], "alice")
	}
	if data["total"] != float64(60) {
		t.Errorf("total = %v, want 60", data["total"])
	}
	if data["active"] != true {
		t.Errorf("active = %v, want true", data["active"])
	}
}

// ---------------------------------------------------------------------------
// 11. Persistence across requests (same Go store object)
// ---------------------------------------------------------------------------

func TestDO_PersistenceAcrossRequests(t *testing.T) {
	e, env, _ := doTestSetup(t)
	siteID := "test-do-persist"

	writeSrc := `export default {
  async fetch(request, env) {
    const stub = env.MY_DO.get("session");
    await stub.storage.put("hits", 1);
    return new Response("written");
  },
};`
	if _, err := e.CompileAndCache(siteID, "deploy1", writeSrc); err != nil {
		t.Fatalf("CompileAndCache: %v", err)
	}
	r1 := e.Execute(siteID, "deploy1", env, getReq("http://localhost/"))
	assertOK(t, r1)

	readSrc := `export default {
  async fetch(request, env) {
    const stub = env.MY_DO.get("session");
    const v = await stub.storage.get("hits");
    return new Response(v === null ? "gone" : String(v));
  },
};`
	siteID2 := "test-do-persist-read"
	if _, err := e.CompileAndCache(siteID2, "deploy1", readSrc); err != nil {
		t.Fatalf("CompileAndCache: %v", err)
	}
	r2 := e.Execute(siteID2, "deploy1", env, getReq("http://localhost/"))
	assertOK(t, r2)

	if string(r2.Response.Body) != "1" {
		t.Errorf("body = %q, want %q", r2.Response.Body, "1")
	}
}

// ---------------------------------------------------------------------------
// 12. Go-side pre-seeded data (JSON-encoded) is visible in JS
// ---------------------------------------------------------------------------

func TestDO_GoSidePreseededDataVisibleInJS(t *testing.T) {
	e, env, store := doTestSetup(t)

	// Store JSON-encoded values on the Go side before JS runs.
	if err := store.Put("MY_DO", "config-obj", "db_host", `"db.internal"`); err != nil {
		t.Fatalf("Go Put: %v", err)
	}
	if err := store.Put("MY_DO", "config-obj", "db_port", `5432`); err != nil {
		t.Fatalf("Go Put: %v", err)
	}

	source := `export default {
  async fetch(request, env) {
    const stub = env.MY_DO.get("config-obj");
    const host = await stub.storage.get("db_host");
    const port = await stub.storage.get("db_port");
    return Response.json({ host, port });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]interface{}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["host"] != "db.internal" {
		t.Errorf("host = %v, want %q", data["host"], "db.internal")
	}
	if data["port"] != float64(5432) {
		t.Errorf("port = %v, want 5432", data["port"])
	}
}

// ---------------------------------------------------------------------------
// 13. JS writes are reflected on the Go side (raw JSON stored)
// ---------------------------------------------------------------------------

func TestDO_JSWritesReflectedInGo(t *testing.T) {
	e, env, store := doTestSetup(t)

	source := `export default {
  async fetch(request, env) {
    const stub = env.MY_DO.get("shared");
    await stub.storage.put("event", { type: "click", ts: 12345 });
    return new Response("ok");
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	// Verify from Go side — value is stored as JSON.
	raw, err := store.Get("MY_DO", "shared", "event")
	if err != nil {
		t.Fatalf("Go Get: %v", err)
	}
	if raw == "" {
		t.Fatal("expected data in Go store, got empty string")
	}
	var event map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		t.Fatalf("unmarshal Go value %q: %v", raw, err)
	}
	if event["type"] != "click" {
		t.Errorf("type = %v, want %q", event["type"], "click")
	}
	if event["ts"] != float64(12345) {
		t.Errorf("ts = %v, want 12345", event["ts"])
	}
}

// ---------------------------------------------------------------------------
// 14. Missing DO binding — accessing an unbound name throws in JS
// ---------------------------------------------------------------------------

func TestDO_MissingBindingErrors(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    try {
      const stub = env.UNBOUND_DO.get("obj");
      return new Response("should not reach");
    } catch (e) {
      return new Response("caught: " + e.message, { status: 400 });
    }
  },
};`

	// Env has no DurableObjects configured.
	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)
	if r.Response.StatusCode != 400 {
		t.Errorf("status = %d, want 400", r.Response.StatusCode)
	}
	if !strings.Contains(string(r.Response.Body), "caught") {
		t.Errorf("body = %q, expected 'caught'", r.Response.Body)
	}
}

// ---------------------------------------------------------------------------
// 15. Coexistence with other bindings (KV + Vars + DO)
// ---------------------------------------------------------------------------

func TestDO_CoexistsWithKVAndVars(t *testing.T) {
	e := newTestEngine(t)

	kvStore := newMockKVStore()
	doStore := newMockDurableObjectStore()

	env := &Env{
		Vars:    map[string]string{"APP_NAME": "my-app"},
		Secrets: make(map[string]string),
		KV:      map[string]KVStore{"CACHE": kvStore},
		DurableObjects: map[string]DurableObjectStore{
			"SESSIONS": doStore,
		},
	}

	source := `export default {
  async fetch(request, env) {
    await env.CACHE.put("greeting", "hello");
    const stub = env.SESSIONS.get("user:abc");
    await stub.storage.put("lastSeen", "2024-01-01");

    const greeting = await env.CACHE.get("greeting");
    const lastSeen = await stub.storage.get("lastSeen");
    const appName = env.APP_NAME;

    return Response.json({ greeting, lastSeen, appName });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]string
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["greeting"] != "hello" {
		t.Errorf("greeting = %q, want %q", data["greeting"], "hello")
	}
	if data["lastSeen"] != "2024-01-01" {
		t.Errorf("lastSeen = %q, want %q", data["lastSeen"], "2024-01-01")
	}
	if data["appName"] != "my-app" {
		t.Errorf("appName = %q, want %q", data["appName"], "my-app")
	}
}

// ---------------------------------------------------------------------------
// 16. Large number of keys via put(object) batch
// ---------------------------------------------------------------------------

func TestDO_ManyKeys(t *testing.T) {
	e, env, _ := doTestSetup(t)

	// Build 50 entries in JS as an object and use put(obj) for batch insert.
	source := `export default {
  async fetch(request, env) {
    const stub = env.MY_DO.get("bulk");
    const entries = {};
    for (let i = 0; i < 50; i++) {
      entries["key" + i] = i;
    }
    await stub.storage.put(entries);
    const m = await stub.storage.list({});
    return Response.json({ total: m.size });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Total != 50 {
		t.Errorf("total = %d, want 50", data.Total)
	}
}

// ---------------------------------------------------------------------------
// 17. idFromName — deterministic ID generation
// ---------------------------------------------------------------------------

func TestDO_IdFromName(t *testing.T) {
	e, env, _ := doTestSetup(t)

	source := `export default {
  async fetch(request, env) {
    const id1 = env.MY_DO.idFromName("room-A").toString();
    const id2 = env.MY_DO.idFromName("room-A").toString();
    const id3 = env.MY_DO.idFromName("room-B").toString();
    return Response.json({
      consistent: id1 === id2,
      different: id1 !== id3,
      len: id1.length,
    });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]interface{}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["consistent"] != true {
		t.Error("idFromName should be deterministic for the same name")
	}
	if data["different"] != true {
		t.Error("idFromName should differ for different names")
	}
	// SHA-256 hex = 64 chars
	if data["len"] != float64(64) {
		t.Errorf("id length = %v, want 64", data["len"])
	}
}

// ---------------------------------------------------------------------------
// 18. newUniqueId — random ID generation
// ---------------------------------------------------------------------------

func TestDO_NewUniqueId(t *testing.T) {
	e, env, _ := doTestSetup(t)

	source := `export default {
  async fetch(request, env) {
    const id1 = env.MY_DO.newUniqueId().toString();
    const id2 = env.MY_DO.newUniqueId().toString();
    return Response.json({
      unique: id1 !== id2,
      len1: id1.length,
    });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]interface{}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["unique"] != true {
		t.Error("newUniqueId should return a different ID each call")
	}
	// 16 random bytes -> 32 hex chars
	if data["len1"] != float64(32) {
		t.Errorf("id length = %v, want 32", data["len1"])
	}
}
