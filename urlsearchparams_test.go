package worker

import (
	"encoding/json"
	"testing"
)

func TestURLSearchParams_Set(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const p = new URLSearchParams("a=1&b=2&a=3");
    p.set("a", "99");
    return Response.json({ result: p.toString() });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Result != "a=99&b=2" {
		t.Errorf("set: got %q, want %q", data.Result, "a=99&b=2")
	}
}

func TestURLSearchParams_Append(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const p = new URLSearchParams("a=1&b=2");
    p.append("c", "3");
    return Response.json({ result: p.toString() });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Result != "a=1&b=2&c=3" {
		t.Errorf("append: got %q, want %q", data.Result, "a=1&b=2&c=3")
	}
}

func TestURLSearchParams_Delete(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const p = new URLSearchParams("a=1&b=2&a=3");
    p.delete("a");
    return Response.json({ result: p.toString(), hasA: p.has("a"), hasB: p.has("b") });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Result string `json:"result"`
		HasA   bool   `json:"hasA"`
		HasB   bool   `json:"hasB"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Result != "b=2" {
		t.Errorf("delete: got %q, want %q", data.Result, "b=2")
	}
	if data.HasA {
		t.Error("has('a') should be false after delete")
	}
	if !data.HasB {
		t.Error("has('b') should still be true")
	}
}

func TestURLSearchParams_DeleteNonexistent(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const p = new URLSearchParams("a=1");
    p.delete("nonexistent");
    return Response.json({ result: p.toString() });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Result != "a=1" {
		t.Errorf("delete nonexistent: got %q, want %q", data.Result, "a=1")
	}
}

func TestURLSearchParams_GetAll(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const q = new URLSearchParams("x=1&x=2&x=3");
    const all = q.getAll("x");
    const missing = q.getAll("nope");
    return Response.json({ all: JSON.stringify(all), missing: JSON.stringify(missing) });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		All     string `json:"all"`
		Missing string `json:"missing"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.All != `["1","2","3"]` {
		t.Errorf("getAll: got %q, want %q", data.All, `["1","2","3"]`)
	}
	if data.Missing != `[]` {
		t.Errorf("getAll missing: got %q, want %q", data.Missing, `[]`)
	}
}

func TestURLSearchParams_Sort(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const s = new URLSearchParams("c=3&a=1&b=2&a=0");
    s.sort();
    return Response.json({ result: s.toString() });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Result != "a=1&a=0&b=2&c=3" {
		t.Errorf("sort stable: got %q, want %q", data.Result, "a=1&a=0&b=2&c=3")
	}
}

func TestURLSearchParams_SetOnEmpty(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const p = new URLSearchParams();
    p.set("a", "1");
    return Response.json({ result: p.toString() });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Result != "a=1" {
		t.Errorf("set on empty: got %q, want %q", data.Result, "a=1")
	}
}

func TestURLSearchParams_URLIntegration(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const url = new URL("https://example.com/?foo=bar");
    url.searchParams.set("foo", "baz");
    url.searchParams.append("key", "val");
    return Response.json({
      search: url.search,
      hrefHasParams: url.href.includes("foo=baz&key=val"),
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Search        string `json:"search"`
		HrefHasParams bool   `json:"hrefHasParams"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Search != "?foo=baz&key=val" {
		t.Errorf("URL.search = %q, want %q", data.Search, "?foo=baz&key=val")
	}
	if !data.HrefHasParams {
		t.Error("URL.href should reflect searchParams mutations")
	}
}

func TestURLSearchParams_SymbolIterator(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const p = new URLSearchParams("a=1&b=2&c=3");
    const pairs = [];
    for (const [k, v] of p) {
      pairs.push(k + "=" + v);
    }
    return Response.json({ result: pairs.join(",") });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Result != "a=1,b=2,c=3" {
		t.Errorf("URLSearchParams iterator: got %q, want %q", data.Result, "a=1,b=2,c=3")
	}
}

func TestURLSearchParams_SpreadOperator(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const p = new URLSearchParams("x=10&y=20");
    const arr = [...p];
    return Response.json({ length: arr.length, first: arr[0], second: arr[1] });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Length int      `json:"length"`
		First  []string `json:"first"`
		Second []string `json:"second"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Length != 2 {
		t.Errorf("spread length: got %d, want 2", data.Length)
	}
	if len(data.First) != 2 || data.First[0] != "x" || data.First[1] != "10" {
		t.Errorf("spread first: got %v, want [x 10]", data.First)
	}
}

func TestHeaders_SymbolIterator(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const h = new Headers({ "content-type": "text/plain", "x-custom": "hello" });
    const pairs = [];
    for (const [k, v] of h) {
      pairs.push(k + ":" + v);
    }
    pairs.sort();
    return Response.json({ result: pairs.join(",") });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Result != "content-type:text/plain,x-custom:hello" {
		t.Errorf("Headers iterator: got %q, want %q", data.Result, "content-type:text/plain,x-custom:hello")
	}
}

func TestHeaders_SpreadOperator(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const h = new Headers({ "a": "1" });
    const arr = [...h];
    return Response.json({ length: arr.length, entry: arr[0] });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Length int      `json:"length"`
		Entry  []string `json:"entry"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Length != 1 {
		t.Errorf("spread length: got %d, want 1", data.Length)
	}
	if len(data.Entry) != 2 || data.Entry[0] != "a" || data.Entry[1] != "1" {
		t.Errorf("spread entry: got %v, want [a 1]", data.Entry)
	}
}

func TestURLSearchParams_URLSearchParamsIterable(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const url = new URL("https://example.com/?a=1&b=2");
    const pairs = [];
    for (const [k, v] of url.searchParams) {
      pairs.push(k + "=" + v);
    }
    return Response.json({ result: pairs.join(",") });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Result != "a=1,b=2" {
		t.Errorf("url.searchParams iterator: got %q, want %q", data.Result, "a=1,b=2")
	}
}

func TestURLSearchParams_CombinedMutations(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const p = new URLSearchParams("a=1&b=2&a=3");
    p.set("a", "99");
    p.append("c", "4");
    p.delete("b");
    return Response.json({ result: p.toString() });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Result != "a=99&c=4" {
		t.Errorf("combined: got %q, want %q", data.Result, "a=99&c=4")
	}
}

// ---------------------------------------------------------------------------
// Spec compliance: URLSearchParams constructor from array of pairs
// ---------------------------------------------------------------------------

func TestURLSearchParams_ConstructorFromArray(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const p = new URLSearchParams([['a', '1'], ['b', '2']]);
    return Response.json({
      str: p.toString(),
      a: p.get('a'),
      b: p.get('b'),
      size: p.size,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Str  string `json:"str"`
		A    string `json:"a"`
		B    string `json:"b"`
		Size int    `json:"size"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Str != "a=1&b=2" {
		t.Errorf("toString() = %q, want %q", data.Str, "a=1&b=2")
	}
	if data.A != "1" {
		t.Errorf("get('a') = %q, want %q", data.A, "1")
	}
	if data.B != "2" {
		t.Errorf("get('b') = %q, want %q", data.B, "2")
	}
	if data.Size != 2 {
		t.Errorf("size = %d, want 2", data.Size)
	}
}

// ---------------------------------------------------------------------------
// Spec compliance: URLSearchParams constructor from object
// ---------------------------------------------------------------------------

func TestURLSearchParams_ConstructorFromObject(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const p = new URLSearchParams({a: '1', b: '2'});
    return Response.json({
      a: p.get('a'),
      b: p.get('b'),
      size: p.size,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		A    string `json:"a"`
		B    string `json:"b"`
		Size int    `json:"size"`
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
	if data.Size != 2 {
		t.Errorf("size = %d, want 2", data.Size)
	}
}

// ---------------------------------------------------------------------------
// Spec compliance: URLSearchParams constructor from existing USP (copy)
// ---------------------------------------------------------------------------

func TestURLSearchParams_ConstructorFromUSP(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const original = new URLSearchParams('a=1&b=2');
    const copy = new URLSearchParams(original);
    copy.set('a', '99');
    return Response.json({
      originalA: original.get('a'),
      copyA: copy.get('a'),
      originalStr: original.toString(),
      copyStr: copy.toString(),
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		OriginalA   string `json:"originalA"`
		CopyA       string `json:"copyA"`
		OriginalStr string `json:"originalStr"`
		CopyStr     string `json:"copyStr"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.OriginalA != "1" {
		t.Errorf("original.get('a') = %q, want %q", data.OriginalA, "1")
	}
	if data.CopyA != "99" {
		t.Errorf("copy.get('a') = %q, want %q", data.CopyA, "99")
	}
	if data.OriginalStr != "a=1&b=2" {
		t.Errorf("original.toString() = %q, want %q", data.OriginalStr, "a=1&b=2")
	}
	if data.CopyStr != "a=99&b=2" {
		t.Errorf("copy.toString() = %q, want %q", data.CopyStr, "a=99&b=2")
	}
}

// ---------------------------------------------------------------------------
// Spec compliance: URLSearchParams.size
// ---------------------------------------------------------------------------

func TestURLSearchParams_Size(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const p = new URLSearchParams('a=1&b=2&c=3');
    return Response.json({
      size: p.size,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Size int `json:"size"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Size != 3 {
		t.Errorf("size = %d, want 3", data.Size)
	}
}

// ---------------------------------------------------------------------------
// Spec compliance: URLSearchParams.has(name, value)
// ---------------------------------------------------------------------------

func TestURLSearchParams_HasWithValue(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const p = new URLSearchParams('a=1&b=2&a=3');
    return Response.json({
      hasA1: p.has('a', '1'),
      hasA3: p.has('a', '3'),
      hasAWrong: p.has('a', 'wrong'),
      hasA: p.has('a'),
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		HasA1     bool `json:"hasA1"`
		HasA3     bool `json:"hasA3"`
		HasAWrong bool `json:"hasAWrong"`
		HasA      bool `json:"hasA"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.HasA1 {
		t.Error("has('a', '1') should be true")
	}
	if !data.HasA3 {
		t.Error("has('a', '3') should be true")
	}
	if data.HasAWrong {
		t.Error("has('a', 'wrong') should be false")
	}
	if !data.HasA {
		t.Error("has('a') should be true")
	}
}

// ---------------------------------------------------------------------------
// Spec compliance: URLSearchParams.delete(name, value)
// ---------------------------------------------------------------------------

func TestURLSearchParams_DeleteWithValue(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const p = new URLSearchParams('a=1&b=2&a=3');
    p.delete('a', '1');
    return Response.json({
      str: p.toString(),
      hasA: p.has('a'),
      getA: p.get('a'),
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Str  string  `json:"str"`
		HasA bool    `json:"hasA"`
		GetA *string `json:"getA"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Str != "b=2&a=3" {
		t.Errorf("toString() = %q, want %q", data.Str, "b=2&a=3")
	}
	if !data.HasA {
		t.Error("has('a') should still be true (a=3 remains)")
	}
	if data.GetA == nil || *data.GetA != "3" {
		t.Errorf("get('a') = %v, want '3'", data.GetA)
	}
}

// ---------------------------------------------------------------------------
// Spec compliance: URLSearchParams Symbol.toStringTag
// ---------------------------------------------------------------------------

func TestURLSearchParams_SymbolToStringTag(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const tag = Object.prototype.toString.call(new URLSearchParams());
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
	if data.Tag != "[object URLSearchParams]" {
		t.Errorf("toStringTag = %q, want %q", data.Tag, "[object URLSearchParams]")
	}
}
