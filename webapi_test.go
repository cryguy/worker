package worker

import (
	"encoding/json"
	"testing"
)

func TestParseURL(t *testing.T) {
	tests := []struct {
		name     string
		rawURL   string
		base     string
		wantErr  bool
		href     string
		protocol string
		hostname string
		pathname string
		search   string
		hash     string
	}{
		{
			name:     "absolute URL with query and hash",
			rawURL:   "https://example.com/path?q=1#hash",
			href:     "https://example.com/path?q=1#hash",
			protocol: "https:",
			hostname: "example.com",
			pathname: "/path",
			search:   "?q=1",
			hash:     "#hash",
		},
		{
			name:     "with port",
			rawURL:   "http://localhost:8080/api",
			href:     "http://localhost:8080/api",
			protocol: "http:",
			hostname: "localhost",
			pathname: "/api",
		},
		{
			name:     "relative with base",
			rawURL:   "/path",
			base:     "https://example.com",
			href:     "https://example.com/path",
			protocol: "https:",
			hostname: "example.com",
			pathname: "/path",
		},
		{
			name:    "no scheme errors",
			rawURL:  "not-a-url",
			wantErr: true,
		},
		{
			name:     "simple https",
			rawURL:   "https://test.com",
			href:     "https://test.com/",
			protocol: "https:",
			hostname: "test.com",
			pathname: "/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := parseURL(tt.rawURL, tt.base)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseURL(%q, %q) error = %v, wantErr %v", tt.rawURL, tt.base, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if parsed.Href != tt.href {
				t.Errorf("href = %q, want %q", parsed.Href, tt.href)
			}
			if parsed.Protocol != tt.protocol {
				t.Errorf("protocol = %q, want %q", parsed.Protocol, tt.protocol)
			}
			if parsed.Hostname != tt.hostname {
				t.Errorf("hostname = %q, want %q", parsed.Hostname, tt.hostname)
			}
			if parsed.Pathname != tt.pathname {
				t.Errorf("pathname = %q, want %q", parsed.Pathname, tt.pathname)
			}
			if tt.search != "" && parsed.Search != tt.search {
				t.Errorf("search = %q, want %q", parsed.Search, tt.search)
			}
			if tt.hash != "" && parsed.Hash != tt.hash {
				t.Errorf("hash = %q, want %q", parsed.Hash, tt.hash)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Integration: Response.redirect
// ---------------------------------------------------------------------------

func TestWebAPI_ResponseRedirect(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const r = Response.redirect("https://example.com/new", 301);
    return Response.json({
      status: r.status,
      location: r.headers.get("location"),
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Status   int    `json:"status"`
		Location string `json:"location"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Status != 301 {
		t.Errorf("status = %d, want 301", data.Status)
	}
	if data.Location != "https://example.com/new" {
		t.Errorf("location = %q", data.Location)
	}
}

func TestWebAPI_ResponseRedirectDefault302(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const r = Response.redirect("https://example.com/default");
    return Response.json({ status: r.status });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Status int `json:"status"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Status != 302 {
		t.Errorf("status = %d, want 302", data.Status)
	}
}

func TestWebAPI_ResponseRedirectValidStatuses(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const valid = [301, 302, 303, 307, 308];
    const results = [];
    for (const s of valid) {
      try {
        const r = Response.redirect("https://example.com/ok", s);
        results.push({ status: s, ok: true, actual: r.status });
      } catch(e) {
        results.push({ status: s, ok: false, error: e.message });
      }
    }
    return Response.json({ results });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Results []struct {
			Status int  `json:"status"`
			Ok     bool `json:"ok"`
			Actual int  `json:"actual"`
		} `json:"results"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	for _, res := range data.Results {
		if !res.Ok {
			t.Errorf("Response.redirect with status %d should succeed", res.Status)
		}
		if res.Actual != res.Status {
			t.Errorf("status %d: actual = %d", res.Status, res.Actual)
		}
	}
}

func TestWebAPI_ResponseRedirectInvalidStatusThrows(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const invalid = [200, 204, 400, 404, 500];
    const results = [];
    for (const s of invalid) {
      try {
        Response.redirect("https://example.com/bad", s);
        results.push({ status: s, threw: false });
      } catch(e) {
        results.push({ status: s, threw: true, name: e.constructor.name, msg: e.message });
      }
    }
    return Response.json({ results });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Results []struct {
			Status int    `json:"status"`
			Threw  bool   `json:"threw"`
			Name   string `json:"name"`
			Msg    string `json:"msg"`
		} `json:"results"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	for _, res := range data.Results {
		if !res.Threw {
			t.Errorf("Response.redirect with status %d should throw RangeError", res.Status)
		}
		if res.Name != "RangeError" {
			t.Errorf("status %d: error type = %q, want RangeError", res.Status, res.Name)
		}
	}
}

// ---------------------------------------------------------------------------
// Integration: Request.clone
// ---------------------------------------------------------------------------

func TestWebAPI_RequestClone(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    const req = new Request("https://example.com/path", {
      method: "POST",
      headers: { "x-custom": "value" },
      body: "original body",
    });
    const clone = req.clone();
    // Mutating original shouldn't affect clone.
    req.headers.set("x-custom", "changed");
    const cloneText = await clone.text();
    return Response.json({
      cloneMethod: clone.method,
      cloneURL: clone.url,
      cloneHeader: clone.headers.get("x-custom"),
      cloneBody: cloneText,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		CloneMethod string `json:"cloneMethod"`
		CloneURL    string `json:"cloneURL"`
		CloneHeader string `json:"cloneHeader"`
		CloneBody   string `json:"cloneBody"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.CloneMethod != "POST" {
		t.Errorf("clone method = %q", data.CloneMethod)
	}
	if data.CloneBody != "original body" {
		t.Errorf("clone body = %q", data.CloneBody)
	}
}

// ---------------------------------------------------------------------------
// Integration: Response.clone
// ---------------------------------------------------------------------------

func TestWebAPI_ResponseClone(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    const resp = new Response("hello", { status: 201, headers: { "x-test": "val" } });
    const clone = resp.clone();
    return Response.json({
      status: clone.status,
      body: await clone.text(),
      header: clone.headers.get("x-test"),
      ok: clone.ok,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
		Header string `json:"header"`
		Ok     bool   `json:"ok"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Status != 201 {
		t.Errorf("status = %d, want 201", data.Status)
	}
	if data.Body != "hello" {
		t.Errorf("body = %q", data.Body)
	}
	if data.Header != "val" {
		t.Errorf("header = %q", data.Header)
	}
	if !data.Ok {
		t.Error("201 should be ok")
	}
}

// ---------------------------------------------------------------------------
// Integration: Headers API
// ---------------------------------------------------------------------------

func TestWebAPI_HeadersOperations(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const h = new Headers({"Content-Type": "text/html", "X-Custom": "abc"});
    h.append("X-Custom", "def");
    const appended = h.get("x-custom");
    h.set("x-custom", "replaced");
    const afterSet = h.get("x-custom");
    const hasCT = h.has("content-type");
    h.delete("content-type");
    const hasCTAfterDel = h.has("content-type");

    const keys = [];
    const vals = [];
    h.forEach((v, k) => { keys.push(k); vals.push(v); });

    return Response.json({
      appended,
      afterSet,
      hasCT,
      hasCTAfterDel,
      keys,
      vals,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Appended      string   `json:"appended"`
		AfterSet      string   `json:"afterSet"`
		HasCT         bool     `json:"hasCT"`
		HasCTAfterDel bool     `json:"hasCTAfterDel"`
		Keys          []string `json:"keys"`
		Vals          []string `json:"vals"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Appended != "abc, def" {
		t.Errorf("appended = %q, want 'abc, def'", data.Appended)
	}
	if data.AfterSet != "replaced" {
		t.Errorf("afterSet = %q", data.AfterSet)
	}
	if !data.HasCT {
		t.Error("should have content-type before delete")
	}
	if data.HasCTAfterDel {
		t.Error("should not have content-type after delete")
	}
}

func TestWebAPI_HeadersFromArray(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const h = new Headers([["Content-Type", "text/plain"], ["X-Foo", "bar"]]);
    return Response.json({
      ct: h.get("content-type"),
      foo: h.get("x-foo"),
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		CT  string `json:"ct"`
		Foo string `json:"foo"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.CT != "text/plain" {
		t.Errorf("ct = %q", data.CT)
	}
	if data.Foo != "bar" {
		t.Errorf("foo = %q", data.Foo)
	}
}

// ---------------------------------------------------------------------------
// Integration: URLSearchParams mutations
// ---------------------------------------------------------------------------

func TestWebAPI_URLSearchParamsMutations(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const url = new URL("https://example.com/path?a=1&b=2&a=3");
    const sp = url.searchParams;
    const getA = sp.get("a");
    const getAll = sp.getAll("a");

    sp.set("a", "99");
    const afterSet = sp.get("a");
    const afterSetAll = sp.getAll("a");

    sp.append("c", "4");
    const hasC = sp.has("c");

    sp.delete("b");
    const hasB = sp.has("b");

    sp.sort();
    const sorted = sp.toString();

    return Response.json({ getA, getAll, afterSet, afterSetAll, hasC, hasB, sorted });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		GetA        string   `json:"getA"`
		GetAll      []string `json:"getAll"`
		AfterSet    string   `json:"afterSet"`
		AfterSetAll []string `json:"afterSetAll"`
		HasC        bool     `json:"hasC"`
		HasB        bool     `json:"hasB"`
		Sorted      string   `json:"sorted"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.GetA != "1" {
		t.Errorf("get(a) = %q, want '1'", data.GetA)
	}
	if len(data.GetAll) != 2 || data.GetAll[0] != "1" || data.GetAll[1] != "3" {
		t.Errorf("getAll(a) = %v, want [1,3]", data.GetAll)
	}
	if data.AfterSet != "99" {
		t.Errorf("afterSet = %q, want '99'", data.AfterSet)
	}
	if len(data.AfterSetAll) != 1 {
		t.Errorf("afterSetAll = %v, want [99]", data.AfterSetAll)
	}
	if !data.HasC {
		t.Error("should have c after append")
	}
	if data.HasB {
		t.Error("should not have b after delete")
	}
}

// ---------------------------------------------------------------------------
// Integration: Response with non-ok status
// ---------------------------------------------------------------------------

func TestWebAPI_ResponseNonOkStatus(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const r404 = new Response("not found", { status: 404 });
    const r500 = new Response("error", { status: 500 });
    const r200 = new Response("ok", { status: 200 });
    return Response.json({
      ok404: r404.ok,
      ok500: r500.ok,
      ok200: r200.ok,
      status404: r404.status,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Ok404     bool `json:"ok404"`
		Ok500     bool `json:"ok500"`
		Ok200     bool `json:"ok200"`
		Status404 int  `json:"status404"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Ok404 {
		t.Error("404 should not be ok")
	}
	if data.Ok500 {
		t.Error("500 should not be ok")
	}
	if !data.Ok200 {
		t.Error("200 should be ok")
	}
	if data.Status404 != 404 {
		t.Errorf("status = %d", data.Status404)
	}
}

// ---------------------------------------------------------------------------
// Integration: Response body as ArrayBuffer
// ---------------------------------------------------------------------------

func TestWebAPI_ResponseArrayBufferBody(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    const resp = new Response("hello world");
    const ab = await resp.arrayBuffer();
    const decoded = new TextDecoder().decode(ab);
    return Response.json({ decoded, byteLen: ab.byteLength });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Decoded string `json:"decoded"`
		ByteLen int    `json:"byteLen"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Decoded != "hello world" {
		t.Errorf("decoded = %q", data.Decoded)
	}
	if data.ByteLen != 11 {
		t.Errorf("byteLen = %d, want 11", data.ByteLen)
	}
}

// ---------------------------------------------------------------------------
// Integration: URL edge cases
// ---------------------------------------------------------------------------

func TestWebAPI_URLComponents(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const url = new URL("https://user:pass@example.com:8443/path?q=1#frag");
    return Response.json({
      protocol: url.protocol,
      hostname: url.hostname,
      port: url.port,
      pathname: url.pathname,
      search: url.search,
      hash: url.hash,
      origin: url.origin,
      host: url.host,
      str: url.toString(),
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Protocol string `json:"protocol"`
		Hostname string `json:"hostname"`
		Port     string `json:"port"`
		Pathname string `json:"pathname"`
		Search   string `json:"search"`
		Hash     string `json:"hash"`
		Origin   string `json:"origin"`
		Host     string `json:"host"`
		Str      string `json:"str"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Protocol != "https:" {
		t.Errorf("protocol = %q", data.Protocol)
	}
	if data.Hostname != "example.com" {
		t.Errorf("hostname = %q", data.Hostname)
	}
	if data.Port != "8443" {
		t.Errorf("port = %q", data.Port)
	}
	if data.Pathname != "/path" {
		t.Errorf("pathname = %q", data.Pathname)
	}
	if data.Search != "?q=1" {
		t.Errorf("search = %q", data.Search)
	}
	if data.Hash != "#frag" {
		t.Errorf("hash = %q", data.Hash)
	}
}

func TestWebAPI_URLInvalidThrows(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    let threw = false;
    try {
      new URL("not-a-valid-url");
    } catch(e) {
      threw = true;
    }
    return Response.json({ threw });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Threw bool `json:"threw"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if !data.Threw {
		t.Error("new URL with invalid input should throw")
	}
}

// ---------------------------------------------------------------------------
// Integration: Response.json with custom headers
// ---------------------------------------------------------------------------

func TestWebAPI_ResponseJsonCustomHeaders(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const r = Response.json({ key: "value" }, {
      status: 201,
      headers: { "x-custom": "test" },
    });
    return Response.json({
      status: r.status,
      ct: r.headers.get("content-type"),
      custom: r.headers.get("x-custom"),
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Status int    `json:"status"`
		CT     string `json:"ct"`
		Custom string `json:"custom"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Status != 201 {
		t.Errorf("status = %d, want 201", data.Status)
	}
	if data.CT != "application/json" {
		t.Errorf("content-type = %q", data.CT)
	}
	if data.Custom != "test" {
		t.Errorf("custom = %q", data.Custom)
	}
}

// ---------------------------------------------------------------------------
// Integration: Response with null body
// ---------------------------------------------------------------------------

func TestWebAPI_ResponseNullBody(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    const r = new Response(null, { status: 204 });
    const text = await r.text();
    return Response.json({ status: r.status, body: text, empty: text === "" });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Status int  `json:"status"`
		Empty  bool `json:"empty"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Status != 204 {
		t.Errorf("status = %d", data.Status)
	}
	if !data.Empty {
		t.Error("null body should produce empty text")
	}
}

func TestWebAPI_URLSearchParamsToString(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request) {
    const params = new URLSearchParams("a=1&b=2&c=hello+world");
    return Response.json({
      str: params.toString(),
      has: params.has("b"),
      missing: params.has("z"),
      getC: params.get("c"),
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Str     string `json:"str"`
		Has     bool   `json:"has"`
		Missing bool   `json:"missing"`
		GetC    string `json:"getC"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if !data.Has {
		t.Error("has('b') should be true")
	}
	if data.Missing {
		t.Error("has('z') should be false")
	}
	if data.GetC != "hello+world" && data.GetC != "hello world" {
		t.Errorf("get('c') = %q, want 'hello+world' or 'hello world'", data.GetC)
	}
}

func TestWebAPI_URLEdgeCases(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request) {
    // URL with port.
    const u1 = new URL("https://example.com:8080/path");
    // URL with auth.
    const u2 = new URL("https://user:pass@example.com/secret");
    // URL with fragment.
    const u3 = new URL("https://example.com/page#section");
    // URL with empty query.
    const u4 = new URL("https://example.com/page?");

    return Response.json({
      port: u1.port,
      pathname1: u1.pathname,
      hostname2: u2.hostname,
      hash3: u3.hash,
      search4: u4.search,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Port      string `json:"port"`
		Pathname1 string `json:"pathname1"`
		Hostname2 string `json:"hostname2"`
		Hash3     string `json:"hash3"`
		Search4   string `json:"search4"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Port != "8080" {
		t.Errorf("port = %q, want 8080", data.Port)
	}
	if data.Pathname1 != "/path" {
		t.Errorf("pathname = %q", data.Pathname1)
	}
	if data.Hostname2 != "example.com" {
		t.Errorf("hostname = %q", data.Hostname2)
	}
}

func TestWebAPI_HeadersIteration(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request) {
    const h = new Headers();
    h.set("X-One", "1");
    h.set("X-Two", "2");
    h.set("X-Three", "3");

    // entries()
    const entries = [];
    for (const [k, v] of h.entries()) {
      entries.push(k + "=" + v);
    }

    // keys()
    const keys = [];
    for (const k of h.keys()) {
      keys.push(k);
    }

    // values()
    const values = [];
    for (const v of h.values()) {
      values.push(v);
    }

    return Response.json({
      entryCount: entries.length,
      keyCount: keys.length,
      valueCount: values.length,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		EntryCount int `json:"entryCount"`
		KeyCount   int `json:"keyCount"`
		ValueCount int `json:"valueCount"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.EntryCount != 3 {
		t.Errorf("entries = %d, want 3", data.EntryCount)
	}
	if data.KeyCount != 3 {
		t.Errorf("keys = %d, want 3", data.KeyCount)
	}
	if data.ValueCount != 3 {
		t.Errorf("values = %d, want 3", data.ValueCount)
	}
}

func TestWebAPI_ResponseStatusText(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request) {
    const r = new Response("ok", { status: 200, statusText: "Custom OK" });
    return Response.json({
      status: r.status,
      statusText: r.statusText,
      ok: r.ok,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Status     int    `json:"status"`
		StatusText string `json:"statusText"`
		OK         bool   `json:"ok"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Status != 200 {
		t.Errorf("status = %d", data.Status)
	}
	if !data.OK {
		t.Error("ok should be true for 200")
	}
}

func TestWebAPI_URLRelativeWithBase(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const url = new URL("/path?q=1", "https://example.com:8080");
    return Response.json({
      href: url.href,
      hostname: url.hostname,
      port: url.port,
      pathname: url.pathname,
      search: url.search,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Href     string `json:"href"`
		Hostname string `json:"hostname"`
		Port     string `json:"port"`
		Pathname string `json:"pathname"`
		Search   string `json:"search"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Hostname != "example.com" {
		t.Errorf("hostname = %q", data.Hostname)
	}
	if data.Port != "8080" {
		t.Errorf("port = %q", data.Port)
	}
	if data.Pathname != "/path" {
		t.Errorf("pathname = %q", data.Pathname)
	}
}

func TestWebAPI_URLInvalid(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    let threw = false;
    try {
      new URL("not a valid url");
    } catch(e) {
      threw = true;
    }
    return Response.json({ threw });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Threw bool `json:"threw"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if !data.Threw {
		t.Error("new URL('not a valid url') should throw")
	}
}

func TestWebAPI_URLSearchParamsDelete(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const params = new URLSearchParams("a=1&b=2&c=3");
    params.delete("b");
    return Response.json({
      str: params.toString(),
      hasB: params.has("b"),
      hasA: params.has("a"),
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Str  string `json:"str"`
		HasB bool   `json:"hasB"`
		HasA bool   `json:"hasA"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.HasB {
		t.Error("params should not have 'b' after delete")
	}
	if !data.HasA {
		t.Error("params should still have 'a'")
	}
}

func TestWebAPI_URLSearchParamsSort(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const params = new URLSearchParams("c=3&a=1&b=2");
    params.sort();
    const keys = [];
    params.forEach(function(value, key) {
      keys.push(key);
    });
    return Response.json({ keys });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Keys []string `json:"keys"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Keys) != 3 || data.Keys[0] != "a" || data.Keys[1] != "b" || data.Keys[2] != "c" {
		t.Errorf("keys after sort = %v, want [a b c]", data.Keys)
	}
}

func TestWebAPI_ResponseCloneWithHeaders(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    const original = new Response("body text", {
      status: 201,
      headers: { "X-Custom": "value" }
    });
    const cloned = original.clone();
    const origText = await original.text();
    const clonedText = await cloned.text();
    return Response.json({
      origText,
      clonedText,
      status: cloned.status,
      header: cloned.headers.get("X-Custom"),
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		OrigText   string `json:"origText"`
		ClonedText string `json:"clonedText"`
		Status     int    `json:"status"`
		Header     string `json:"header"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.OrigText != "body text" {
		t.Errorf("origText = %q", data.OrigText)
	}
	if data.ClonedText != "body text" {
		t.Errorf("clonedText = %q", data.ClonedText)
	}
	if data.Status != 201 {
		t.Errorf("status = %d", data.Status)
	}
}

func TestWebAPI_RequestProperties(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const url = new URL(request.url);
    return Response.json({
      method: request.method,
      url: request.url,
      pathname: url.pathname,
      headerKeys: [...request.headers.keys()],
    });
  },
};`

	req := &WorkerRequest{
		Method:  "DELETE",
		URL:     "http://localhost/items/123?force=true",
		Headers: map[string]string{"Authorization": "Bearer abc", "Accept": "application/json"},
	}

	r := execJS(t, e, source, defaultEnv(), req)
	assertOK(t, r)

	var data struct {
		Method     string   `json:"method"`
		URL        string   `json:"url"`
		Pathname   string   `json:"pathname"`
		HeaderKeys []string `json:"headerKeys"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Method != "DELETE" {
		t.Errorf("method = %q", data.Method)
	}
	if data.Pathname != "/items/123" {
		t.Errorf("pathname = %q", data.Pathname)
	}
	if len(data.HeaderKeys) < 2 {
		t.Errorf("expected at least 2 header keys, got %d", len(data.HeaderKeys))
	}
}

// ---------------------------------------------------------------------------
// Integration: Binary response body (Uint8Array) — covers jsResponseToGo base64 path
// ---------------------------------------------------------------------------

func TestWebAPI_ResponseBinaryBody(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request) {
    const bytes = new Uint8Array([72, 101, 108, 108, 111]); // "Hello"
    return new Response(bytes, {
      headers: { "content-type": "application/octet-stream" },
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	if string(r.Response.Body) != "Hello" {
		t.Errorf("body = %q, want 'Hello'", string(r.Response.Body))
	}
}

func TestWebAPI_ResponseArrayBufferDirectBody(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request) {
    const encoder = new TextEncoder();
    const buf = encoder.encode("binary data");
    return new Response(buf);
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	if string(r.Response.Body) != "binary data" {
		t.Errorf("body = %q, want 'binary data'", string(r.Response.Body))
	}
}

// ---------------------------------------------------------------------------
// Integration: Worker returning null/undefined (covers jsResponseToGo error)
// ---------------------------------------------------------------------------

func TestWebAPI_ResponseNull(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request) {
    return null;
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	if r.Error == nil {
		t.Fatal("returning null should produce an error")
	}
}

func TestWebAPI_ResponseUndefined(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request) {
    return undefined;
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	if r.Error == nil {
		t.Fatal("returning undefined should produce an error")
	}
}

// ---------------------------------------------------------------------------
// Integration: POST request with body (covers goRequestToJS body path)
// ---------------------------------------------------------------------------

func TestWebAPI_PostRequestWithBody(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request) {
    const body = await request.text();
    return Response.json({
      method: request.method,
      body: body,
      ct: request.headers.get("content-type"),
    });
  },
};`

	req := &WorkerRequest{
		Method:  "POST",
		URL:     "http://localhost/api/data",
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    []byte(`{"key":"value"}`),
	}

	r := execJS(t, e, source, defaultEnv(), req)
	assertOK(t, r)

	var data struct {
		Method string `json:"method"`
		Body   string `json:"body"`
		CT     string `json:"ct"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Method != "POST" {
		t.Errorf("method = %q", data.Method)
	}
	if data.Body != `{"key":"value"}` {
		t.Errorf("body = %q", data.Body)
	}
	if data.CT != "application/json" {
		t.Errorf("content-type = %q", data.CT)
	}
}

// ---------------------------------------------------------------------------
// Integration: ctx.waitUntil (covers buildExecContext)
// ---------------------------------------------------------------------------

func TestWebAPI_CtxWaitUntil(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env, ctx) {
    let called = false;
    ctx.waitUntil(Promise.resolve().then(() => { called = true; }));
    await new Promise(r => setTimeout(r, 10));
    return Response.json({ called });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Called bool `json:"called"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if !data.Called {
		t.Error("waitUntil promise should have resolved")
	}
}

func TestWebAPI_CtxPassThroughOnException(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env, ctx) {
    // Should not throw; it's a no-op.
    ctx.passThroughOnException();
    return new Response("ok");
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)
}

// ---------------------------------------------------------------------------
// Integration: TextEncoder/TextDecoder
// ---------------------------------------------------------------------------

func TestWebAPI_TextEncoderDecoder(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request) {
    const encoder = new TextEncoder();
    const encoded = encoder.encode("Hello World");
    const decoder = new TextDecoder();
    const decoded = decoder.decode(encoded);
    return Response.json({
      decoded,
      byteLen: encoded.byteLength,
      encoding: encoder.encoding,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Decoded  string `json:"decoded"`
		ByteLen  int    `json:"byteLen"`
		Encoding string `json:"encoding"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Decoded != "Hello World" {
		t.Errorf("decoded = %q", data.Decoded)
	}
	if data.ByteLen != 11 {
		t.Errorf("byteLen = %d, want 11", data.ByteLen)
	}
}

func TestWebAPI_HeadersSetGetDelete(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const h = new Headers();
    h.set("Content-Type", "text/html");
    h.set("X-Custom", "value");
    h.append("X-Multi", "a");
    h.append("X-Multi", "b");
    const beforeDelete = h.has("X-Custom");
    h.delete("X-Custom");
    const afterDelete = h.has("X-Custom");
    return Response.json({
      ct: h.get("Content-Type"),
      multi: h.get("X-Multi"),
      beforeDelete,
      afterDelete,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		CT           string `json:"ct"`
		Multi        string `json:"multi"`
		BeforeDelete bool   `json:"beforeDelete"`
		AfterDelete  bool   `json:"afterDelete"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.CT != "text/html" {
		t.Errorf("content-type = %q", data.CT)
	}
	if !data.BeforeDelete {
		t.Error("should have X-Custom before delete")
	}
	if data.AfterDelete {
		t.Error("should not have X-Custom after delete")
	}
}

// ---------------------------------------------------------------------------
// Response.redirect edge cases
// ---------------------------------------------------------------------------

func TestWebAPI_ResponseRedirectRelativeURL(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    let threw = false;
    let location = null;
    let status = 0;
    try {
      const r = Response.redirect("/relative");
      location = r.headers.get("location");
      status = r.status;
    } catch(e) {
      threw = true;
    }
    return Response.json({ threw, location, status });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Threw    bool   `json:"threw"`
		Location string `json:"location"`
		Status   int    `json:"status"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Either it throws (relative URL not allowed) or produces location=/relative
	if data.Threw {
		return
	}
	if data.Location != "/relative" {
		t.Errorf("location = %q, want '/relative'", data.Location)
	}
	if data.Status != 302 {
		t.Errorf("status = %d, want 302", data.Status)
	}
}

// ---------------------------------------------------------------------------
// Integration: Response.error()
// ---------------------------------------------------------------------------

func TestResponse_Error(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const r = Response.error();
    return Response.json({
      status: r.status,
      statusText: r.statusText,
      bodyIsNull: r._body === null,
      type: r.type,
      headerCount: Object.keys(r.headers._map).length,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Status      int    `json:"status"`
		StatusText  string `json:"statusText"`
		BodyIsNull  bool   `json:"bodyIsNull"`
		Type        string `json:"type"`
		HeaderCount int    `json:"headerCount"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Status != 0 {
		t.Errorf("status = %d, want 0", data.Status)
	}
	if data.StatusText != "" {
		t.Errorf("statusText = %q, want empty", data.StatusText)
	}
	if !data.BodyIsNull {
		t.Error("Response.error() body should be null")
	}
	if data.Type != "error" {
		t.Errorf("type = %q, want 'error'", data.Type)
	}
	if data.HeaderCount != 0 {
		t.Errorf("headerCount = %d, want 0", data.HeaderCount)
	}
}

// ---------------------------------------------------------------------------
// Integration: Request.bytes()
// ---------------------------------------------------------------------------

func TestRequest_Bytes(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    const req = new Request("https://example.com/", {
      method: "POST",
      body: "hello",
    });
    const bytes = await req.bytes();
    const decoded = new TextDecoder().decode(bytes);
    return Response.json({
      isUint8Array: bytes instanceof Uint8Array,
      byteLength: bytes.byteLength,
      decoded,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		IsUint8Array bool   `json:"isUint8Array"`
		ByteLength   int    `json:"byteLength"`
		Decoded      string `json:"decoded"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.IsUint8Array {
		t.Error("bytes() should return a Uint8Array")
	}
	if data.ByteLength != 5 {
		t.Errorf("byteLength = %d, want 5", data.ByteLength)
	}
	if data.Decoded != "hello" {
		t.Errorf("decoded = %q, want 'hello'", data.Decoded)
	}
}

// ---------------------------------------------------------------------------
// Integration: Response.bytes()
// ---------------------------------------------------------------------------

func TestResponse_Bytes(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    const resp = new Response("world");
    const bytes = await resp.bytes();
    const decoded = new TextDecoder().decode(bytes);
    return Response.json({
      isUint8Array: bytes instanceof Uint8Array,
      byteLength: bytes.byteLength,
      decoded,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		IsUint8Array bool   `json:"isUint8Array"`
		ByteLength   int    `json:"byteLength"`
		Decoded      string `json:"decoded"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.IsUint8Array {
		t.Error("bytes() should return a Uint8Array")
	}
	if data.ByteLength != 5 {
		t.Errorf("byteLength = %d, want 5", data.ByteLength)
	}
	if data.Decoded != "world" {
		t.Errorf("decoded = %q, want 'world'", data.Decoded)
	}
}

func TestResponse_Bytes_EmptyBody(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    const resp = new Response(null);
    const bytes = await resp.bytes();
    return Response.json({
      isUint8Array: bytes instanceof Uint8Array,
      byteLength: bytes.byteLength,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		IsUint8Array bool `json:"isUint8Array"`
		ByteLength   int  `json:"byteLength"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.IsUint8Array {
		t.Error("bytes() on null body should return a Uint8Array")
	}
	if data.ByteLength != 0 {
		t.Errorf("byteLength = %d, want 0", data.ByteLength)
	}
}

func TestResponse_Bytes_CalledTwice(t *testing.T) {
	e := newTestEngine(t)

	// Note: the current implementation uses .text() which reads _body as a
	// string (no locking), so calling bytes() twice works the same as calling
	// text() twice. This test verifies the second call still succeeds and
	// returns the same data (consistent with the existing arrayBuffer behaviour).
	source := `export default {
  async fetch(request, env) {
    const resp = new Response("data");
    const b1 = await resp.bytes();
    const b2 = await resp.bytes();
    return Response.json({
      first: new TextDecoder().decode(b1),
      second: new TextDecoder().decode(b2),
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		First  string `json:"first"`
		Second string `json:"second"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.First != "data" {
		t.Errorf("first = %q, want 'data'", data.First)
	}
	if data.Second != "data" {
		t.Errorf("second = %q, want 'data'", data.Second)
	}
}

func TestWebAPI_ResponseRedirectBodyIsNull(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const r = Response.redirect("https://example.com", 302);
    return Response.json({
      bodyIsNull: r.body === null,
      status: r.status,
      location: r.headers.get("location"),
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		BodyIsNull bool   `json:"bodyIsNull"`
		Status     int    `json:"status"`
		Location   string `json:"location"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.BodyIsNull {
		t.Error("Response.redirect body should be null")
	}
	if data.Status != 302 {
		t.Errorf("status = %d, want 302", data.Status)
	}
	if data.Location != "https://example.com" {
		t.Errorf("location = %q, want 'https://example.com'", data.Location)
	}
}

// ---------------------------------------------------------------------------
// Integration: URL.canParse static method
// ---------------------------------------------------------------------------

func TestURL_CanParse(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const results = {
      isFunction:            typeof URL.canParse === 'function',
      absoluteHTTPS:         URL.canParse("https://example.com"),
      absoluteWithPathQuery: URL.canParse("https://example.com/path?q=1#hash"),
      notAURL:               URL.canParse("not a url"),
      empty:                 URL.canParse(""),
      relativeWithBase:      URL.canParse("/path", "https://example.com"),
      relativeNoBase:        URL.canParse("/path"),
      withPort:              URL.canParse("https://example.com:8080"),
      ipv6:                  URL.canParse("http://[::1]"),
      nullArg:               URL.canParse(null),
      undefinedArg:          URL.canParse(undefined),
    };
    return Response.json(results);
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		IsFunction            bool `json:"isFunction"`
		AbsoluteHTTPS         bool `json:"absoluteHTTPS"`
		AbsoluteWithPathQuery bool `json:"absoluteWithPathQuery"`
		NotAURL               bool `json:"notAURL"`
		Empty                 bool `json:"empty"`
		RelativeWithBase      bool `json:"relativeWithBase"`
		RelativeNoBase        bool `json:"relativeNoBase"`
		WithPort              bool `json:"withPort"`
		IPv6                  bool `json:"ipv6"`
		NullArg               bool `json:"nullArg"`
		UndefinedArg          bool `json:"undefinedArg"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !data.IsFunction {
		t.Error("URL.canParse should be a function")
	}
	if !data.AbsoluteHTTPS {
		t.Error("URL.canParse('https://example.com') should be true")
	}
	if !data.AbsoluteWithPathQuery {
		t.Error("URL.canParse('https://example.com/path?q=1#hash') should be true")
	}
	if data.NotAURL {
		t.Error("URL.canParse('not a url') should be false")
	}
	if data.Empty {
		t.Error("URL.canParse('') should be false")
	}
	if !data.RelativeWithBase {
		t.Error("URL.canParse('/path', 'https://example.com') should be true")
	}
	if data.RelativeNoBase {
		t.Error("URL.canParse('/path') without base should be false")
	}
	if !data.WithPort {
		t.Error("URL.canParse('https://example.com:8080') should be true")
	}
	if !data.IPv6 {
		t.Error("URL.canParse('http://[::1]') should be true")
	}
	// null coerces to string "null" which has no scheme, so should be false
	if data.NullArg {
		t.Error("URL.canParse(null) should be false")
	}
	// undefined coerces to string "undefined" which has no scheme, so should be false
	if data.UndefinedArg {
		t.Error("URL.canParse(undefined) should be false")
	}
}

func TestHeaders_FromHeadersInstance(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const h1 = new Headers({ "x-one": "1", "x-two": "2" });
    const h2 = new Headers(h1);
    return Response.json({
      one: h2.get("x-one"),
      two: h2.get("x-two"),
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		One string `json:"one"`
		Two string `json:"two"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.One != "1" {
		t.Errorf("x-one = %q, want '1'", data.One)
	}
	if data.Two != "2" {
		t.Errorf("x-two = %q, want '2'", data.Two)
	}
}

func TestURLSearchParams_ForEach(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const url = new URL("http://localhost/?a=1&b=2&c=3");
    const pairs = [];
    url.searchParams.forEach(function(value, key) {
      pairs.push(key + "=" + value);
    });
    return Response.json({ pairs: pairs.sort() });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Pairs []string `json:"pairs"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(data.Pairs) != 3 {
		t.Fatalf("expected 3 pairs, got %d", len(data.Pairs))
	}
	if data.Pairs[0] != "a=1" || data.Pairs[1] != "b=2" || data.Pairs[2] != "c=3" {
		t.Errorf("pairs = %v", data.Pairs)
	}
}

func TestRequest_BodyUsed(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    const req = new Request("http://localhost/", {
      method: "POST",
      body: "hello",
    });
    const beforeUsed = req.bodyUsed;
    const reader = req.body.getReader();
    const afterUsed = req.bodyUsed;
    const { value } = await reader.read();
    return Response.json({ beforeUsed, afterUsed });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		BeforeUsed bool `json:"beforeUsed"`
		AfterUsed  bool `json:"afterUsed"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.BeforeUsed {
		t.Error("bodyUsed should be false before consuming")
	}
	if !data.AfterUsed {
		t.Error("bodyUsed should be true after consuming")
	}
}

// ---------------------------------------------------------------------------
// Bug 3: URLSearchParams should decode + as space
// ---------------------------------------------------------------------------

func TestURLSearchParams_PlusDecodedAsSpace(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const url = new URL("https://example.com/search?q=hello+world&tag=foo+bar+baz");
    return Response.json({
      q: url.searchParams.get("q"),
      tag: url.searchParams.get("tag"),
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Q   string `json:"q"`
		Tag string `json:"tag"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Q != "hello world" {
		t.Errorf("q = %q, want %q", data.Q, "hello world")
	}
	if data.Tag != "foo bar baz" {
		t.Errorf("tag = %q, want %q", data.Tag, "foo bar baz")
	}
}

// ---------------------------------------------------------------------------
// Bug 5: URL should expose username and password
// ---------------------------------------------------------------------------

func TestURL_UsernamePassword(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const url = new URL("https://user:pass@example.com:8443/path?q=1#frag");
    const noAuth = new URL("https://example.com/path");
    return Response.json({
      username: url.username,
      password: url.password,
      noAuthUser: noAuth.username,
      noAuthPass: noAuth.password,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Username   string `json:"username"`
		Password   string `json:"password"`
		NoAuthUser string `json:"noAuthUser"`
		NoAuthPass string `json:"noAuthPass"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Username != "user" {
		t.Errorf("username = %q, want %q", data.Username, "user")
	}
	if data.Password != "pass" {
		t.Errorf("password = %q, want %q", data.Password, "pass")
	}
	if data.NoAuthUser != "" {
		t.Errorf("noAuthUser = %q, want empty", data.NoAuthUser)
	}
	if data.NoAuthPass != "" {
		t.Errorf("noAuthPass = %q, want empty", data.NoAuthPass)
	}
}

// ---------------------------------------------------------------------------
// Bug 4: Request URL should be normalized, empty pathname should be "/"
// ---------------------------------------------------------------------------

func TestRequest_URLNormalized(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const r1 = new Request("https://example.com");
    const r2 = new Request("https://example.com/path?q=1");
    return Response.json({
      url1: r1.url,
      url2: r2.url,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		URL1 string `json:"url1"`
		URL2 string `json:"url2"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.URL1 != "https://example.com/" {
		t.Errorf("url1 = %q, want %q", data.URL1, "https://example.com/")
	}
	if data.URL2 != "https://example.com/path?q=1" {
		t.Errorf("url2 = %q, want %q", data.URL2, "https://example.com/path?q=1")
	}
}

func TestRequest_ArrayBuffer(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    const req = new Request("http://localhost/", {
      method: "POST",
      body: "hello",
    });
    const buf = await req.arrayBuffer();
    const view = new Uint8Array(buf);
    const text = new TextDecoder().decode(view);
    return Response.json({ text, length: buf.byteLength });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Text   string `json:"text"`
		Length int    `json:"length"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Text != "hello" {
		t.Errorf("text = %q, want 'hello'", data.Text)
	}
	if data.Length != 5 {
		t.Errorf("length = %d, want 5", data.Length)
	}
}

// ---------------------------------------------------------------------------
// Phase 2 edge cases: Headers, Response, Request
// ---------------------------------------------------------------------------

func TestHeaders_CaseInsensitiveAppend(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    const h = new Headers();
    h.set("Content-Type", "text/plain");
    h.append("content-type", "text/html");
    return Response.json({ ct: h.get("content-type") });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		CT string `json:"ct"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Headers.append with same case-insensitive name should produce a
	// comma-separated value containing both values.
	if data.CT == "" {
		t.Fatal("content-type header is empty")
	}
	if !containsAll(data.CT, "text/plain", "text/html") {
		t.Errorf("content-type = %q, want both 'text/plain' and 'text/html'", data.CT)
	}
}

// containsAll returns true if s contains all of the given substrings.
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func TestResponse_Redirect(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    return Response.redirect("https://example.com", 302);
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	if r.Response.StatusCode != 302 {
		t.Errorf("status = %d, want 302", r.Response.StatusCode)
	}
	loc := r.Response.Headers["location"]
	if loc != "https://example.com" {
		t.Errorf("location = %q, want 'https://example.com'", loc)
	}
}

func TestRequest_Clone(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    const cloned = request.clone();
    const t1 = await request.text();
    const t2 = await cloned.text();
    return Response.json({ same: t1 === t2, body: t1 });
  },
};`

	req := &WorkerRequest{
		Method:  "POST",
		URL:     "http://localhost/",
		Headers: map[string]string{"content-type": "text/plain"},
		Body:    []byte("test body"),
	}
	r := execJS(t, e, source, defaultEnv(), req)
	assertOK(t, r)

	var data struct {
		Same bool   `json:"same"`
		Body string `json:"body"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.Same {
		t.Error("cloned request body should equal original request body")
	}
	if data.Body != "test body" {
		t.Errorf("body = %q, want 'test body'", data.Body)
	}
}

// ---------------------------------------------------------------------------
// Spec compliance: Headers.getSetCookie()
// ---------------------------------------------------------------------------

func TestHeaders_GetSetCookie(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const h = new Headers();
    h.append('set-cookie', 'a=1');
    h.append('set-cookie', 'b=2');
    const cookies = h.getSetCookie();
    return Response.json({
      cookies: JSON.stringify(cookies),
      joined: h.get('set-cookie'),
      length: cookies.length,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Cookies string `json:"cookies"`
		Joined  string `json:"joined"`
		Length  int    `json:"length"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Cookies != `["a=1","b=2"]` {
		t.Errorf("getSetCookie() = %q, want %q", data.Cookies, `["a=1","b=2"]`)
	}
	if data.Joined != "a=1, b=2" {
		t.Errorf("get('set-cookie') = %q, want %q", data.Joined, "a=1, b=2")
	}
	if data.Length != 2 {
		t.Errorf("getSetCookie().length = %d, want 2", data.Length)
	}
}

// ---------------------------------------------------------------------------
// Spec compliance: Headers multi-value append
// ---------------------------------------------------------------------------

func TestHeaders_MultiValueAppend(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const h = new Headers();
    h.append('x-custom', 'val1');
    h.append('x-custom', 'val2');
    return Response.json({
      combined: h.get('x-custom'),
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Combined string `json:"combined"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Combined != "val1, val2" {
		t.Errorf("get('x-custom') = %q, want %q", data.Combined, "val1, val2")
	}
}

// ---------------------------------------------------------------------------
// Spec compliance: Headers Symbol.toStringTag
// ---------------------------------------------------------------------------

func TestHeaders_SymbolToStringTag(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const tag = Object.prototype.toString.call(new Headers());
    return Response.json({ tag });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Tag string `json:"tag"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Tag != "[object Headers]" {
		t.Errorf("toStringTag = %q, want %q", data.Tag, "[object Headers]")
	}
}

// ---------------------------------------------------------------------------
// Spec compliance: Headers constructor from array of pairs
// ---------------------------------------------------------------------------

func TestHeaders_ConstructorFromArray(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const h = new Headers([['a', '1'], ['b', '2']]);
    return Response.json({
      a: h.get('a'),
      b: h.get('b'),
      hasA: h.has('a'),
      hasB: h.has('b'),
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		A    string `json:"a"`
		B    string `json:"b"`
		HasA bool   `json:"hasA"`
		HasB bool   `json:"hasB"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.A != "1" {
		t.Errorf("get('a') = %q, want %q", data.A, "1")
	}
	if data.B != "2" {
		t.Errorf("get('b') = %q, want %q", data.B, "2")
	}
	if !data.HasA {
		t.Error("has('a') should be true")
	}
	if !data.HasB {
		t.Error("has('b') should be true")
	}
}

// ---------------------------------------------------------------------------
// Spec compliance: URL property setters update href
// ---------------------------------------------------------------------------

func TestURL_PropertySetters(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const url = new URL('https://example.com/path?q=1#old');
    url.pathname = '/new';
    url.search = '?x=2';
    url.hash = '#fresh';
    return Response.json({
      href: url.href,
      pathname: url.pathname,
      search: url.search,
      hash: url.hash,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Href     string `json:"href"`
		Pathname string `json:"pathname"`
		Search   string `json:"search"`
		Hash     string `json:"hash"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Href != "https://example.com/new?x=2#fresh" {
		t.Errorf("href = %q, want %q", data.Href, "https://example.com/new?x=2#fresh")
	}
	if data.Pathname != "/new" {
		t.Errorf("pathname = %q, want %q", data.Pathname, "/new")
	}
	if data.Search != "?x=2" {
		t.Errorf("search = %q, want %q", data.Search, "?x=2")
	}
	if data.Hash != "#fresh" {
		t.Errorf("hash = %q, want %q", data.Hash, "#fresh")
	}
}

// ---------------------------------------------------------------------------
// Spec compliance: URL.search setter updates searchParams
// ---------------------------------------------------------------------------

func TestURL_SetSearch_UpdatesSearchParams(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const url = new URL('https://example.com/?a=1');
    url.search = '?x=10&y=20';
    const x = url.searchParams.get('x');
    const y = url.searchParams.get('y');
    const a = url.searchParams.get('a');
    return Response.json({ x, y, a });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		X *string `json:"x"`
		Y *string `json:"y"`
		A *string `json:"a"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.X == nil || *data.X != "10" {
		t.Errorf("searchParams.get('x') = %v, want '10'", data.X)
	}
	if data.Y == nil || *data.Y != "20" {
		t.Errorf("searchParams.get('y') = %v, want '20'", data.Y)
	}
	if data.A != nil {
		t.Errorf("searchParams.get('a') = %v, want null (old param should be gone)", data.A)
	}
}

// ---------------------------------------------------------------------------
// Spec compliance: URL.toJSON()
// ---------------------------------------------------------------------------

func TestURL_ToJSON(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const url = new URL('https://example.com/');
    return Response.json({
      json: url.toJSON(),
      href: url.href,
      same: url.toJSON() === url.href,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		JSON string `json:"json"`
		Href string `json:"href"`
		Same bool   `json:"same"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.JSON != "https://example.com/" {
		t.Errorf("toJSON() = %q, want %q", data.JSON, "https://example.com/")
	}
	if !data.Same {
		t.Error("toJSON() should return the same value as href")
	}
}

// ---------------------------------------------------------------------------
// Spec compliance: URL Symbol.toStringTag
// ---------------------------------------------------------------------------

func TestURL_SymbolToStringTag(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const tag = Object.prototype.toString.call(new URL('https://example.com'));
    return Response.json({ tag });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Tag string `json:"tag"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Tag != "[object URL]" {
		t.Errorf("toStringTag = %q, want %q", data.Tag, "[object URL]")
	}
}

// ---------------------------------------------------------------------------
// Spec compliance: URL.hostname setter updates href and host
// ---------------------------------------------------------------------------

func TestURL_SetHostname(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const url = new URL('https://old.example.com:8080/path');
    url.hostname = 'new.example.com';
    return Response.json({
      hostname: url.hostname,
      host: url.host,
      href: url.href,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Hostname string `json:"hostname"`
		Host     string `json:"host"`
		Href     string `json:"href"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Hostname != "new.example.com" {
		t.Errorf("hostname = %q, want %q", data.Hostname, "new.example.com")
	}
	if data.Host != "new.example.com:8080" {
		t.Errorf("host = %q, want %q", data.Host, "new.example.com:8080")
	}
	if data.Href != "https://new.example.com:8080/path" {
		t.Errorf("href = %q, want %q", data.Href, "https://new.example.com:8080/path")
	}
}

// ---------------------------------------------------------------------------
// Spec compliance: URL.port setter updates href and host
// ---------------------------------------------------------------------------

func TestURL_SetPort(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const url = new URL('https://example.com:8080/path');
    url.port = '9090';
    return Response.json({
      port: url.port,
      host: url.host,
      href: url.href,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Port string `json:"port"`
		Host string `json:"host"`
		Href string `json:"href"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Port != "9090" {
		t.Errorf("port = %q, want %q", data.Port, "9090")
	}
	if data.Host != "example.com:9090" {
		t.Errorf("host = %q, want %q", data.Host, "example.com:9090")
	}
	if data.Href != "https://example.com:9090/path" {
		t.Errorf("href = %q, want %q", data.Href, "https://example.com:9090/path")
	}
}

// ---------------------------------------------------------------------------
// Spec compliance: Request constructor validation
// ---------------------------------------------------------------------------

func TestRequest_ForbiddenMethodThrows(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    let caught = false;
    let errorName = '';
    try {
      new Request('http://x.com', { method: 'TRACE' });
    } catch(e) {
      caught = true;
      errorName = e.constructor.name;
    }
    return Response.json({ caught, errorName });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Caught    bool   `json:"caught"`
		ErrorName string `json:"errorName"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.Caught {
		t.Error("new Request with TRACE method should throw")
	}
	if data.ErrorName != "TypeError" {
		t.Errorf("error type = %q, want TypeError", data.ErrorName)
	}
}

func TestRequest_BodyWithGetThrows(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    let caught = false;
    let errorName = '';
    try {
      new Request('http://x.com', { method: 'GET', body: 'hello' });
    } catch(e) {
      caught = true;
      errorName = e.constructor.name;
    }
    return Response.json({ caught, errorName });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Caught    bool   `json:"caught"`
		ErrorName string `json:"errorName"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.Caught {
		t.Error("new Request with GET and body should throw")
	}
	if data.ErrorName != "TypeError" {
		t.Errorf("error type = %q, want TypeError", data.ErrorName)
	}
}

func TestRequest_DefaultProperties(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const r = new Request('http://example.com');
    return Response.json({
      redirect: r.redirect,
      mode: r.mode,
      credentials: r.credentials,
      cache: r.cache,
      referrer: r.referrer,
      referrerPolicy: r.referrerPolicy,
      integrity: r.integrity,
      keepalive: r.keepalive,
      destination: r.destination,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Redirect       string `json:"redirect"`
		Mode           string `json:"mode"`
		Credentials    string `json:"credentials"`
		Cache          string `json:"cache"`
		Referrer       string `json:"referrer"`
		ReferrerPolicy string `json:"referrerPolicy"`
		Integrity      string `json:"integrity"`
		Keepalive      bool   `json:"keepalive"`
		Destination    string `json:"destination"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Redirect != "follow" {
		t.Errorf("redirect = %q, want 'follow'", data.Redirect)
	}
	if data.Mode != "cors" {
		t.Errorf("mode = %q, want 'cors'", data.Mode)
	}
	if data.Credentials != "same-origin" {
		t.Errorf("credentials = %q, want 'same-origin'", data.Credentials)
	}
	if data.Cache != "default" {
		t.Errorf("cache = %q, want 'default'", data.Cache)
	}
	if data.Referrer != "about:client" {
		t.Errorf("referrer = %q, want 'about:client'", data.Referrer)
	}
	if data.ReferrerPolicy != "" {
		t.Errorf("referrerPolicy = %q, want empty", data.ReferrerPolicy)
	}
	if data.Integrity != "" {
		t.Errorf("integrity = %q, want empty", data.Integrity)
	}
	if data.Keepalive {
		t.Error("keepalive should be false by default")
	}
	if data.Destination != "" {
		t.Errorf("destination = %q, want empty", data.Destination)
	}
}

func TestRequest_SymbolToStringTag(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const tag = Object.prototype.toString.call(new Request('http://x.com'));
    return Response.json({ tag });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Tag string `json:"tag"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Tag != "[object Request]" {
		t.Errorf("tag = %q, want '[object Request]'", data.Tag)
	}
}

func TestRequest_ClonePreservesProperties(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const r = new Request('http://example.com', {
      method: 'POST',
      body: 'data',
      redirect: 'follow',
      mode: 'cors',
      credentials: 'same-origin',
    });
    const c = r.clone();
    return Response.json({
      url: c.url,
      method: c.method,
      redirect: c.redirect,
      mode: c.mode,
      credentials: c.credentials,
      cache: c.cache,
      referrer: c.referrer,
      referrerPolicy: c.referrerPolicy,
      integrity: c.integrity,
      keepalive: c.keepalive,
      destination: c.destination,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		URL            string `json:"url"`
		Method         string `json:"method"`
		Redirect       string `json:"redirect"`
		Mode           string `json:"mode"`
		Credentials    string `json:"credentials"`
		Cache          string `json:"cache"`
		Referrer       string `json:"referrer"`
		ReferrerPolicy string `json:"referrerPolicy"`
		Integrity      string `json:"integrity"`
		Keepalive      bool   `json:"keepalive"`
		Destination    string `json:"destination"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.URL != "http://example.com/" {
		t.Errorf("url = %q, want 'http://example.com/'", data.URL)
	}
	if data.Method != "POST" {
		t.Errorf("method = %q, want 'POST'", data.Method)
	}
	if data.Redirect != "follow" {
		t.Errorf("redirect = %q, want 'follow'", data.Redirect)
	}
	if data.Mode != "cors" {
		t.Errorf("mode = %q, want 'cors'", data.Mode)
	}
	if data.Credentials != "same-origin" {
		t.Errorf("credentials = %q, want 'same-origin'", data.Credentials)
	}
	if data.Cache != "default" {
		t.Errorf("cache = %q, want 'default'", data.Cache)
	}
	if data.Referrer != "about:client" {
		t.Errorf("referrer = %q, want 'about:client'", data.Referrer)
	}
	if data.ReferrerPolicy != "" {
		t.Errorf("referrerPolicy = %q, want empty", data.ReferrerPolicy)
	}
	if data.Integrity != "" {
		t.Errorf("integrity = %q, want empty", data.Integrity)
	}
	if data.Keepalive {
		t.Error("keepalive should be false")
	}
	if data.Destination != "" {
		t.Errorf("destination = %q, want empty", data.Destination)
	}
}

// ---------------------------------------------------------------------------
// Spec compliance: Response type, ok, status, clone, redirected, bodyUsed
// ---------------------------------------------------------------------------

func TestResponse_TypeDefault(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const r = new Response('hi');
    return Response.json({ type: r.type });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Type != "default" {
		t.Errorf("type = %q, want 'default'", data.Type)
	}
}

func TestResponse_TypeOnError(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const r = Response.error();
    return Response.json({ type: r.type });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Type != "error" {
		t.Errorf("type = %q, want 'error'", data.Type)
	}
}

func TestResponse_OkIsGetter(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const ok200 = new Response('hi', { status: 200 }).ok;
    const ok299 = new Response('hi', { status: 299 }).ok;
    const notOk404 = new Response('hi', { status: 404 }).ok;
    const notOk500 = new Response('hi', { status: 500 }).ok;
    return Response.json({ ok200, ok299, notOk404, notOk500 });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		OK200    bool `json:"ok200"`
		OK299    bool `json:"ok299"`
		NotOK404 bool `json:"notOk404"`
		NotOK500 bool `json:"notOk500"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.OK200 {
		t.Error("ok should be true for status 200")
	}
	if !data.OK299 {
		t.Error("ok should be true for status 299")
	}
	if data.NotOK404 {
		t.Error("ok should be false for status 404")
	}
	if data.NotOK500 {
		t.Error("ok should be false for status 500")
	}
}

func TestResponse_StatusValidation(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    let caught = false;
    let errorName = '';
    try {
      new Response(null, { status: 1000 });
    } catch(e) {
      caught = true;
      errorName = e.constructor.name;
    }
    return Response.json({ caught, errorName });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Caught    bool   `json:"caught"`
		ErrorName string `json:"errorName"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.Caught {
		t.Error("new Response with status 1000 should throw")
	}
	if data.ErrorName != "RangeError" {
		t.Errorf("error type = %q, want RangeError", data.ErrorName)
	}
}

func TestResponse_ClonePreservesTypeAndUrl(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const original = new Response('body', { status: 200 });
    original.url = 'http://example.com/';
    const cloned = original.clone();
    return Response.json({
      type: cloned.type,
      url: cloned.url,
      redirected: cloned.redirected,
      sameType: original.type === cloned.type,
      sameUrl: original.url === cloned.url,
      sameRedirected: original.redirected === cloned.redirected,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Type           string `json:"type"`
		URL            string `json:"url"`
		Redirected     bool   `json:"redirected"`
		SameType       bool   `json:"sameType"`
		SameURL        bool   `json:"sameUrl"`
		SameRedirected bool   `json:"sameRedirected"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Type != "default" {
		t.Errorf("type = %q, want 'default'", data.Type)
	}
	if !data.SameType {
		t.Error("clone type should match original")
	}
	if !data.SameURL {
		t.Error("clone url should match original")
	}
	if !data.SameRedirected {
		t.Error("clone redirected should match original")
	}
}

func TestResponse_Redirected(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const r = new Response('hi');
    return Response.json({ redirected: r.redirected });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Redirected bool `json:"redirected"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Redirected {
		t.Error("new Response().redirected should be false")
	}
}

func TestResponse_SymbolToStringTag(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const tag = Object.prototype.toString.call(new Response('hi'));
    return Response.json({ tag });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Tag string `json:"tag"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Tag != "[object Response]" {
		t.Errorf("tag = %q, want '[object Response]'", data.Tag)
	}
}

func TestResponse_BodyUsedAfterText(t *testing.T) {
	e := newTestEngine(t)

	// Access .body first to convert the string body into a ReadableStream,
	// which enables bodyUsed tracking and double-consume rejection.
	source := `export default {
  async fetch(request, env) {
    const r = new Response('hello');
    const beforeUsed = r.bodyUsed;
    // Touch .body to promote to ReadableStream so bodyUsed tracking activates.
    void r.body;
    const text = await r.text();
    const afterUsed = r.bodyUsed;
    let secondFailed = false;
    try {
      await r.text();
    } catch(e) {
      secondFailed = true;
    }
    return Response.json({ beforeUsed, afterUsed, text, secondFailed });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		BeforeUsed   bool   `json:"beforeUsed"`
		AfterUsed    bool   `json:"afterUsed"`
		Text         string `json:"text"`
		SecondFailed bool   `json:"secondFailed"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.BeforeUsed {
		t.Error("bodyUsed should be false before text()")
	}
	if data.Text != "hello" {
		t.Errorf("text = %q, want 'hello'", data.Text)
	}
	if !data.AfterUsed {
		t.Error("bodyUsed should be true after text()")
	}
	if !data.SecondFailed {
		t.Error("second text() call should reject after body consumed")
	}
}

// ---------------------------------------------------------------------------
// Spec compliance: TextEncoder / TextDecoder
// ---------------------------------------------------------------------------

func TestTextEncoder_Encoding(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const enc = new TextEncoder();
    return Response.json({ encoding: enc.encoding });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Encoding string `json:"encoding"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Encoding != "utf-8" {
		t.Errorf("encoding = %q, want 'utf-8'", data.Encoding)
	}
}

func TestTextEncoder_EncodeInto(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const enc = new TextEncoder();
    const buf = new Uint8Array(10);
    const result = enc.encodeInto('Hello', buf);
    return Response.json({ read: result.read, written: result.written });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Read    int `json:"read"`
		Written int `json:"written"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Read != 5 {
		t.Errorf("read = %d, want 5", data.Read)
	}
	if data.Written != 5 {
		t.Errorf("written = %d, want 5", data.Written)
	}
}

func TestTextEncoder_EncodeIntoPartial(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const enc = new TextEncoder();
    const buf = new Uint8Array(3);
    const result = enc.encodeInto('Hello', buf);
    return Response.json({ read: result.read, written: result.written });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Read    int `json:"read"`
		Written int `json:"written"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Read != 3 {
		t.Errorf("read = %d, want 3", data.Read)
	}
	if data.Written != 3 {
		t.Errorf("written = %d, want 3", data.Written)
	}
}

func TestTextEncoder_SymbolToStringTag(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const tag = Object.prototype.toString.call(new TextEncoder());
    return Response.json({ tag });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Tag string `json:"tag"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Tag != "[object TextEncoder]" {
		t.Errorf("tag = %q, want '[object TextEncoder]'", data.Tag)
	}
}

func TestTextDecoder_SymbolToStringTag(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const tag = Object.prototype.toString.call(new TextDecoder());
    return Response.json({ tag });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Tag string `json:"tag"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Tag != "[object TextDecoder]" {
		t.Errorf("tag = %q, want '[object TextDecoder]'", data.Tag)
	}
}
