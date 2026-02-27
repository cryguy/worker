package worker

import (
	"encoding/json"
	"testing"
)

// r2TestSetup creates an engine and env with an R2 bucket bound to "BUCKET".
func r2TestSetup(t *testing.T) (*Engine, *Env, *mockR2Store) {
	t.Helper()
	e := newTestEngine(t)
	r2 := newMockR2Store()
	env := &Env{
		Vars:    make(map[string]string),
		Secrets: make(map[string]string),
		Storage: map[string]R2Store{"BUCKET": r2},
	}
	return e, env, r2
}

// ---------------------------------------------------------------------------
// Put / Get
// ---------------------------------------------------------------------------

func TestR2_PutAndGet(t *testing.T) {
	e, env, _ := r2TestSetup(t)

	source := `export default {
  async fetch(request, env) {
    await env.BUCKET.put("hello.txt", "hello world");
    const obj = await env.BUCKET.get("hello.txt");
    const text = await obj.text();
    return new Response(text);
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)
	if string(r.Response.Body) != "hello world" {
		t.Errorf("body = %q, want %q", r.Response.Body, "hello world")
	}
}

func TestR2_GetNonexistent(t *testing.T) {
	e, env, _ := r2TestSetup(t)

	source := `export default {
  async fetch(request, env) {
    const obj = await env.BUCKET.get("missing.txt");
    return Response.json({ found: obj !== null });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]interface{}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["found"] != false {
		t.Errorf("found = %v, want false", data["found"])
	}
}

func TestR2_PutReturnsObject(t *testing.T) {
	e, env, _ := r2TestSetup(t)

	source := `export default {
  async fetch(request, env) {
    const obj = await env.BUCKET.put("key.txt", "data");
    return Response.json({
      key: obj.key,
      hasEtag: obj.etag !== undefined && obj.etag !== "",
      size: obj.size,
    });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]interface{}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["key"] != "key.txt" {
		t.Errorf("key = %v, want key.txt", data["key"])
	}
	if data["hasEtag"] != true {
		t.Errorf("hasEtag = %v, want true", data["hasEtag"])
	}
	if data["size"] != float64(4) {
		t.Errorf("size = %v, want 4", data["size"])
	}
}

func TestR2_PutOverwrite(t *testing.T) {
	e, env, _ := r2TestSetup(t)

	source := `export default {
  async fetch(request, env) {
    await env.BUCKET.put("k", "first");
    await env.BUCKET.put("k", "second");
    const obj = await env.BUCKET.get("k");
    const text = await obj.text();
    return new Response(text);
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)
	if string(r.Response.Body) != "second" {
		t.Errorf("body = %q, want %q", r.Response.Body, "second")
	}
}

// ---------------------------------------------------------------------------
// Get body formats
// ---------------------------------------------------------------------------

func TestR2_GetBodyAsArrayBuffer(t *testing.T) {
	e, env, _ := r2TestSetup(t)

	source := `export default {
  async fetch(request, env) {
    await env.BUCKET.put("bin.dat", "ABC");
    const obj = await env.BUCKET.get("bin.dat");
    const buf = await obj.arrayBuffer();
    return Response.json({ byteLength: buf.byteLength });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]interface{}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["byteLength"] != float64(3) {
		t.Errorf("byteLength = %v, want 3", data["byteLength"])
	}
}

func TestR2_GetBodyAsJSON(t *testing.T) {
	e, env, _ := r2TestSetup(t)

	source := `export default {
  async fetch(request, env) {
    await env.BUCKET.put("data.json", JSON.stringify({ msg: "hi", n: 7 }));
    const obj = await env.BUCKET.get("data.json");
    const parsed = await obj.json();
    return Response.json({ msg: parsed.msg, n: parsed.n });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]interface{}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["msg"] != "hi" {
		t.Errorf("msg = %v, want hi", data["msg"])
	}
	if data["n"] != float64(7) {
		t.Errorf("n = %v, want 7", data["n"])
	}
}

// ---------------------------------------------------------------------------
// Get object metadata
// ---------------------------------------------------------------------------

func TestR2_GetObjectMetadata(t *testing.T) {
	e, env, _ := r2TestSetup(t)

	source := `export default {
  async fetch(request, env) {
    await env.BUCKET.put("doc.txt", "content");
    const obj = await env.BUCKET.get("doc.txt");
    return Response.json({
      key: obj.key,
      size: obj.size,
      hasEtag: typeof obj.etag === "string" && obj.etag.length > 0,
    });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]interface{}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["key"] != "doc.txt" {
		t.Errorf("key = %v, want doc.txt", data["key"])
	}
	if data["size"] != float64(7) {
		t.Errorf("size = %v, want 7", data["size"])
	}
	if data["hasEtag"] != true {
		t.Errorf("hasEtag = %v, want true", data["hasEtag"])
	}
}

func TestR2_PutWithContentType(t *testing.T) {
	e, env, _ := r2TestSetup(t)

	source := `export default {
  async fetch(request, env) {
    await env.BUCKET.put("page.html", "<h1>Hi</h1>", { httpMetadata: { contentType: "text/html" } });
    const obj = await env.BUCKET.get("page.html");
    return Response.json({ contentType: obj.httpMetadata?.contentType ?? obj.contentType ?? "" });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]interface{}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["contentType"] != "text/html" {
		t.Errorf("contentType = %v, want text/html", data["contentType"])
	}
}

func TestR2_PutWithCustomMetadata(t *testing.T) {
	e, env, _ := r2TestSetup(t)

	source := `export default {
  async fetch(request, env) {
    await env.BUCKET.put("tagged.txt", "data", {
      customMetadata: { author: "alice", version: "2" },
    });
    const obj = await env.BUCKET.get("tagged.txt");
    return Response.json({
      author: obj.customMetadata?.author ?? "",
      version: obj.customMetadata?.version ?? "",
    });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]interface{}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["author"] != "alice" {
		t.Errorf("author = %v, want alice", data["author"])
	}
	if data["version"] != "2" {
		t.Errorf("version = %v, want 2", data["version"])
	}
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestR2_DeleteSingleKey(t *testing.T) {
	e, env, _ := r2TestSetup(t)

	source := `export default {
  async fetch(request, env) {
    await env.BUCKET.put("gone.txt", "bye");
    await env.BUCKET.delete("gone.txt");
    const obj = await env.BUCKET.get("gone.txt");
    return Response.json({ found: obj !== null });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]interface{}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["found"] != false {
		t.Errorf("found = %v, want false", data["found"])
	}
}

func TestR2_DeleteMultipleKeys(t *testing.T) {
	e, env, _ := r2TestSetup(t)

	source := `export default {
  async fetch(request, env) {
    await env.BUCKET.put("a.txt", "a");
    await env.BUCKET.put("b.txt", "b");
    await env.BUCKET.put("c.txt", "c");
    await env.BUCKET.delete(["a.txt", "b.txt"]);
    const objA = await env.BUCKET.get("a.txt");
    const objB = await env.BUCKET.get("b.txt");
    const objC = await env.BUCKET.get("c.txt");
    return Response.json({
      aFound: objA !== null,
      bFound: objB !== null,
      cFound: objC !== null,
    });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]interface{}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["aFound"] != false {
		t.Errorf("aFound = %v, want false", data["aFound"])
	}
	if data["bFound"] != false {
		t.Errorf("bFound = %v, want false", data["bFound"])
	}
	if data["cFound"] != true {
		t.Errorf("cFound = %v, want true", data["cFound"])
	}
}

func TestR2_DeleteNonexistent(t *testing.T) {
	e, env, _ := r2TestSetup(t)

	// Deleting a key that doesn't exist should not error.
	source := `export default {
  async fetch(request, env) {
    await env.BUCKET.delete("nope.txt");
    return new Response("ok");
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)
	if string(r.Response.Body) != "ok" {
		t.Errorf("body = %q, want ok", r.Response.Body)
	}
}

// ---------------------------------------------------------------------------
// Head
// ---------------------------------------------------------------------------

func TestR2_Head(t *testing.T) {
	e, env, _ := r2TestSetup(t)

	source := `export default {
  async fetch(request, env) {
    await env.BUCKET.put("meta.txt", "some content");
    const obj = await env.BUCKET.head("meta.txt");
    return Response.json({
      key: obj.key,
      size: obj.size,
      hasEtag: typeof obj.etag === "string" && obj.etag.length > 0,
    });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]interface{}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["key"] != "meta.txt" {
		t.Errorf("key = %v, want meta.txt", data["key"])
	}
	if data["size"] != float64(12) {
		t.Errorf("size = %v, want 12", data["size"])
	}
	if data["hasEtag"] != true {
		t.Errorf("hasEtag = %v, want true", data["hasEtag"])
	}
}

func TestR2_HeadNonexistent(t *testing.T) {
	e, env, _ := r2TestSetup(t)

	source := `export default {
  async fetch(request, env) {
    const obj = await env.BUCKET.head("ghost.txt");
    return Response.json({ found: obj !== null });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]interface{}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["found"] != false {
		t.Errorf("found = %v, want false", data["found"])
	}
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

func TestR2_ListAll(t *testing.T) {
	e, env, _ := r2TestSetup(t)

	source := `export default {
  async fetch(request, env) {
    await env.BUCKET.put("a.txt", "1");
    await env.BUCKET.put("b.txt", "2");
    await env.BUCKET.put("c.txt", "3");
    const result = await env.BUCKET.list();
    return Response.json({ count: result.objects.length });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]interface{}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["count"] != float64(3) {
		t.Errorf("count = %v, want 3", data["count"])
	}
}

func TestR2_ListWithPrefix(t *testing.T) {
	e, env, _ := r2TestSetup(t)

	source := `export default {
  async fetch(request, env) {
    await env.BUCKET.put("images/cat.jpg", "x");
    await env.BUCKET.put("images/dog.jpg", "x");
    await env.BUCKET.put("docs/readme.md", "x");
    const result = await env.BUCKET.list({ prefix: "images/" });
    return Response.json({
      count: result.objects.length,
      keys: result.objects.map(o => o.key).sort(),
    });
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
	if data.Count != 2 {
		t.Errorf("count = %d, want 2", data.Count)
	}
	if len(data.Keys) == 2 && data.Keys[0] != "images/cat.jpg" {
		t.Errorf("keys[0] = %q, want images/cat.jpg", data.Keys[0])
	}
}

func TestR2_ListWithLimit(t *testing.T) {
	e, env, _ := r2TestSetup(t)

	source := `export default {
  async fetch(request, env) {
    for (let i = 0; i < 5; i++) {
      await env.BUCKET.put("file" + i + ".txt", "data");
    }
    const result = await env.BUCKET.list({ limit: 2 });
    return Response.json({
      count: result.objects.length,
      truncated: result.truncated,
    });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]interface{}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["count"] != float64(2) {
		t.Errorf("count = %v, want 2", data["count"])
	}
	if data["truncated"] != true {
		t.Errorf("truncated = %v, want true", data["truncated"])
	}
}

func TestR2_ListEmpty(t *testing.T) {
	e, env, _ := r2TestSetup(t)

	source := `export default {
  async fetch(request, env) {
    const result = await env.BUCKET.list();
    return Response.json({
      count: result.objects.length,
      truncated: result.truncated,
    });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]interface{}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["count"] != float64(0) {
		t.Errorf("count = %v, want 0", data["count"])
	}
	if data["truncated"] != false {
		t.Errorf("truncated = %v, want false", data["truncated"])
	}
}

func TestR2_ListObjectsHaveMetadata(t *testing.T) {
	e, env, _ := r2TestSetup(t)

	source := `export default {
  async fetch(request, env) {
    await env.BUCKET.put("item.txt", "hello");
    const result = await env.BUCKET.list();
    const obj = result.objects[0];
    return Response.json({
      key: obj.key,
      size: obj.size,
      hasEtag: typeof obj.etag === "string",
    });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]interface{}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["key"] != "item.txt" {
		t.Errorf("key = %v, want item.txt", data["key"])
	}
	if data["size"] != float64(5) {
		t.Errorf("size = %v, want 5", data["size"])
	}
	if data["hasEtag"] != true {
		t.Errorf("hasEtag = %v, want true", data["hasEtag"])
	}
}

// ---------------------------------------------------------------------------
// Multiple buckets
// ---------------------------------------------------------------------------

func TestR2_MultipleBuckets(t *testing.T) {
	e := newTestEngine(t)

	env := &Env{
		Vars:    make(map[string]string),
		Secrets: make(map[string]string),
		Storage: map[string]R2Store{
			"BUCKET_A": newMockR2Store(),
			"BUCKET_B": newMockR2Store(),
		},
	}

	source := `export default {
  async fetch(request, env) {
    await env.BUCKET_A.put("k", "from-a");
    await env.BUCKET_B.put("k", "from-b");
    const objA = await env.BUCKET_A.get("k");
    const objB = await env.BUCKET_B.get("k");
    return Response.json({
      a: await objA.text(),
      b: await objB.text(),
    });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]string
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["a"] != "from-a" {
		t.Errorf("a = %q, want from-a", data["a"])
	}
	if data["b"] != "from-b" {
		t.Errorf("b = %q, want from-b", data["b"])
	}
}

// ---------------------------------------------------------------------------
// Go-side verification
// ---------------------------------------------------------------------------

func TestR2_GoSideVerifyPut(t *testing.T) {
	e, env, store := r2TestSetup(t)

	source := `export default {
  async fetch(request, env) {
    await env.BUCKET.put("verify.txt", "from-js");
    return new Response("done");
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	// Verify Go side received the write.
	data, obj, err := store.Get("verify.txt")
	if err != nil {
		t.Fatalf("Go-side Get: %v", err)
	}
	if obj == nil {
		t.Fatal("Go-side Get returned nil object")
	}
	if string(data) != "from-js" {
		t.Errorf("data = %q, want from-js", data)
	}
}

func TestR2_GoSideVerifyDelete(t *testing.T) {
	e, env, store := r2TestSetup(t)

	// Pre-populate via Go side.
	if _, err := store.Put("pre.txt", []byte("pre-loaded"), R2PutOptions{}); err != nil {
		t.Fatalf("Go-side Put: %v", err)
	}

	source := `export default {
  async fetch(request, env) {
    await env.BUCKET.delete("pre.txt");
    return new Response("done");
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	// Verify Go side reflects the delete.
	_, obj, err := store.Get("pre.txt")
	if err == nil && obj != nil {
		t.Error("Go-side object should be gone after JS delete")
	}
}

func TestR2_GoSidePrePopulate(t *testing.T) {
	e, env, store := r2TestSetup(t)

	// Pre-populate via Go side before JS runs.
	if _, err := store.Put("seeded.txt", []byte("seed value"), R2PutOptions{ContentType: "text/plain"}); err != nil {
		t.Fatalf("Go-side Put: %v", err)
	}

	source := `export default {
  async fetch(request, env) {
    const obj = await env.BUCKET.get("seeded.txt");
    const text = await obj.text();
    return new Response(text);
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)
	if string(r.Response.Body) != "seed value" {
		t.Errorf("body = %q, want seed value", r.Response.Body)
	}
}

// ---------------------------------------------------------------------------
// Large object
// ---------------------------------------------------------------------------

func TestR2_LargeObject(t *testing.T) {
	e, env, store := r2TestSetup(t)

	// Pre-populate a 64 KB object via Go to avoid QuickJS argument-count
	// limits that arise when building large payloads inside JS.
	const size = 64 * 1024
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = 'A'
	}
	if _, err := store.Put("large.dat", payload, R2PutOptions{}); err != nil {
		t.Fatalf("Go-side Put: %v", err)
	}

	// JS reads the object back and reports its size via ArrayBuffer.
	source := `export default {
  async fetch(request, env) {
    const obj = await env.BUCKET.get("large.dat");
    const buf = await obj.arrayBuffer();
    return Response.json({ size: buf.byteLength, key: obj.key });
  },
};`

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data map[string]interface{}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["size"] != float64(size) {
		t.Errorf("size = %v, want %d", data["size"], size)
	}
	if data["key"] != "large.dat" {
		t.Errorf("key = %v, want large.dat", data["key"])
	}
}
