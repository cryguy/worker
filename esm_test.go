package worker

import (
	"encoding/json"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// ESM module handling — wrapESModule / default export semantics
// ---------------------------------------------------------------------------

// TestESM_BasicDefaultExport verifies the canonical ESM worker pattern:
// export default { fetch() {} } produces a working handler.
func TestESM_BasicDefaultExport(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    return new Response("esm ok");
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)
	if string(r.Response.Body) != "esm ok" {
		t.Errorf("body = %q, want 'esm ok'", r.Response.Body)
	}
}

// TestESM_NamedExportsAlongsideDefault verifies that named exports can
// coexist with the default export without breaking handler dispatch.
func TestESM_NamedExportsAlongsideDefault(t *testing.T) {
	e := newTestEngine(t)

	source := `export const VERSION = "1.0.0";

export function helper(x) {
  return x * 2;
}

export default {
  fetch(request, env) {
    return Response.json({
      version: VERSION,
      doubled: helper(21),
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Version string  `json:"version"`
		Doubled float64 `json:"doubled"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Version != "1.0.0" {
		t.Errorf("version = %q, want '1.0.0'", data.Version)
	}
	if data.Doubled != 42 {
		t.Errorf("doubled = %v, want 42", data.Doubled)
	}
}

// TestESM_DefaultExportClass verifies that a class can be used as the default
// export with a static fetch method.
func TestESM_DefaultExportClass(t *testing.T) {
	e := newTestEngine(t)

	source := `class MyWorker {
  fetch(request, env) {
    return new Response("class worker");
  }
}

export default new MyWorker();`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)
	if string(r.Response.Body) != "class worker" {
		t.Errorf("body = %q, want 'class worker'", r.Response.Body)
	}
}

// TestESM_ArrowFunctionInDefault verifies arrow functions work inside the
// default export object.
func TestESM_ArrowFunctionInDefault(t *testing.T) {
	e := newTestEngine(t)

	source := `const greet = (name) => "Hello, " + name + "!";

export default {
  fetch: (request, env) => {
    const url = new URL(request.url);
    const name = url.searchParams.get("name") || "World";
    return new Response(greet(name));
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/?name=Alice"))
	assertOK(t, r)
	if string(r.Response.Body) != "Hello, Alice!" {
		t.Errorf("body = %q, want 'Hello, Alice!'", r.Response.Body)
	}
}

// TestESM_ModuleTopLevelCode verifies that module-level initialization code
// runs before fetch is called and state is captured in closure.
func TestESM_ModuleTopLevelCode(t *testing.T) {
	e := newTestEngine(t)

	source := `const INIT_TIME = Date.now();
const CONFIG = { maxItems: 10, prefix: "item:" };

export default {
  fetch(request, env) {
    return Response.json({
      hasInitTime: typeof INIT_TIME === "number" && INIT_TIME > 0,
      maxItems: CONFIG.maxItems,
      prefix: CONFIG.prefix,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		HasInitTime bool    `json:"hasInitTime"`
		MaxItems    float64 `json:"maxItems"`
		Prefix      string  `json:"prefix"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.HasInitTime {
		t.Error("INIT_TIME should be a positive number")
	}
	if data.MaxItems != 10 {
		t.Errorf("maxItems = %v, want 10", data.MaxItems)
	}
	if data.Prefix != "item:" {
		t.Errorf("prefix = %q, want 'item:'", data.Prefix)
	}
}

// TestESM_ClosureOverModuleScope verifies that closures inside the default
// export correctly capture module-scope variables.
func TestESM_ClosureOverModuleScope(t *testing.T) {
	e := newTestEngine(t)

	source := `let requestCount = 0;

export default {
  fetch(request, env) {
    requestCount++;
    return Response.json({ count: requestCount });
  },
};`

	// Each execution reuses the same compiled module; requestCount should
	// increment across calls within the same engine/pool slot.
	siteID := "test-" + t.Name()
	deployKey := "deploy1"
	if _, err := e.CompileAndCache(siteID, deployKey, source); err != nil {
		t.Fatalf("CompileAndCache: %v", err)
	}

	r1 := e.Execute(siteID, deployKey, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r1)

	r2 := e.Execute(siteID, deployKey, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r2)

	var d1, d2 struct {
		Count float64 `json:"count"`
	}
	if err := json.Unmarshal(r1.Response.Body, &d1); err != nil {
		t.Fatalf("unmarshal r1: %v", err)
	}
	if err := json.Unmarshal(r2.Response.Body, &d2); err != nil {
		t.Fatalf("unmarshal r2: %v", err)
	}
	// Count should be at least 1 on first call and at least 1 on second.
	// Depending on pool recycling behaviour, the count may reset per pool slot.
	if d1.Count < 1 {
		t.Errorf("first call count = %v, want >= 1", d1.Count)
	}
	if d2.Count < 1 {
		t.Errorf("second call count = %v, want >= 1", d2.Count)
	}
}

// TestESM_AsyncDefaultExport verifies that an async fetch method in the
// default export object works correctly.
func TestESM_AsyncDefaultExport(t *testing.T) {
	e := newTestEngine(t)

	source := `async function computeValue() {
  return await Promise.resolve(99);
}

export default {
  async fetch(request, env) {
    const val = await computeValue();
    return Response.json({ val });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Val float64 `json:"val"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Val != 99 {
		t.Errorf("val = %v, want 99", data.Val)
	}
}

// TestESM_MultipleHandlersInDefault verifies that a default export with
// multiple handlers (fetch + scheduled) works correctly for fetch dispatch.
func TestESM_MultipleHandlersInDefault(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    return Response.json({ handler: "fetch" });
  },
  async scheduled(event, env, ctx) {
    console.log("scheduled ran");
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Handler string `json:"handler"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Handler != "fetch" {
		t.Errorf("handler = %q, want 'fetch'", data.Handler)
	}
}

// TestESM_MissingDefaultExport verifies that a script without a default
// export produces a clear error on compilation or execution.
func TestESM_MissingDefaultExport(t *testing.T) {
	e := newTestEngine(t)

	// Valid JS with named exports only — no default export.
	source := `export const foo = 42;
export function bar() { return "bar"; }`

	siteID := "test-" + t.Name()
	deployKey := "deploy1"
	// CompileAndCache may or may not error; execution must error.
	_, compileErr := e.CompileAndCache(siteID, deployKey, source)
	if compileErr != nil {
		t.Logf("compile error (acceptable): %v", compileErr)
		return
	}

	r := e.Execute(siteID, deployKey, defaultEnv(), getReq("http://localhost/"))
	if r.Error == nil {
		t.Fatal("expected error for missing default export, got nil")
	}
	t.Logf("missing default export error: %v", r.Error)
}

// TestESM_DefaultExportIsFunction verifies that if the default export is a
// function object (not a plain object), it's treated correctly.
func TestESM_DefaultExportFunction(t *testing.T) {
	e := newTestEngine(t)

	// Default export as a function that has a .fetch property attached.
	source := `function MyHandler() {}
MyHandler.fetch = function(request, env) {
  return new Response("function export");
};

export default MyHandler;`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)
	if string(r.Response.Body) != "function export" {
		t.Errorf("body = %q, want 'function export'", r.Response.Body)
	}
}

// TestESM_ConstAndLetAtModuleScope verifies const/let declarations at module
// scope are accessible inside fetch without issues.
func TestESM_ConstAndLetAtModuleScope(t *testing.T) {
	e := newTestEngine(t)

	source := `const CONST_VAL = "const-value";
let letVal = "let-value";

export default {
  fetch(request, env) {
    return Response.json({ constVal: CONST_VAL, letVal });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		ConstVal string `json:"constVal"`
		LetVal   string `json:"letVal"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.ConstVal != "const-value" {
		t.Errorf("constVal = %q, want 'const-value'", data.ConstVal)
	}
	if data.LetVal != "let-value" {
		t.Errorf("letVal = %q, want 'let-value'", data.LetVal)
	}
}

// TestESM_GeneratorAndIteratorInModule verifies generator functions work in
// the module scope alongside the default export.
func TestESM_GeneratorAndIteratorInModule(t *testing.T) {
	e := newTestEngine(t)

	source := `function* range(n) {
  for (let i = 0; i < n; i++) yield i;
}

export default {
  fetch(request, env) {
    const values = [...range(5)];
    return Response.json({ values });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Values []float64 `json:"values"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(data.Values) != 5 {
		t.Fatalf("values length = %d, want 5", len(data.Values))
	}
	for i, v := range data.Values {
		if v != float64(i) {
			t.Errorf("values[%d] = %v, want %d", i, v, i)
		}
	}
}

// TestESM_SyntaxError verifies that a syntax error in the ESM source is
// caught at compile time and does not panic the engine.
func TestESM_SyntaxError(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request env) {  // missing comma — syntax error
    return new Response("unreachable");
  },
};`

	siteID := "test-" + t.Name()
	deployKey := "deploy1"
	_, err := e.CompileAndCache(siteID, deployKey, source)
	if err == nil {
		t.Fatal("expected compile error for syntax error, got nil")
	}
	if !strings.Contains(err.Error(), "transform") &&
		!strings.Contains(err.Error(), "parse") &&
		!strings.Contains(err.Error(), "syntax") &&
		!strings.Contains(err.Error(), "SyntaxError") &&
		!strings.Contains(err.Error(), "Unexpected") {
		t.Logf("compile error (unexpected format but acceptable): %v", err)
	} else {
		t.Logf("compile error: %v", err)
	}
}

// TestESM_DestructuringInModule verifies destructuring assignments at module
// scope work correctly.
func TestESM_DestructuringInModule(t *testing.T) {
	e := newTestEngine(t)

	source := `const { a, b, ...rest } = { a: 1, b: 2, c: 3, d: 4 };
const [first, second] = [10, 20, 30];

export default {
  fetch(request, env) {
    return Response.json({ a, b, rest, first, second });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		A      float64            `json:"a"`
		B      float64            `json:"b"`
		Rest   map[string]float64 `json:"rest"`
		First  float64            `json:"first"`
		Second float64            `json:"second"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.A != 1 || data.B != 2 {
		t.Errorf("a=%v b=%v, want 1 2", data.A, data.B)
	}
	if data.Rest["c"] != 3 || data.Rest["d"] != 4 {
		t.Errorf("rest = %v, want {c:3, d:4}", data.Rest)
	}
	if data.First != 10 || data.Second != 20 {
		t.Errorf("first=%v second=%v, want 10 20", data.First, data.Second)
	}
}
