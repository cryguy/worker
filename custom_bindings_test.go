package worker

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Custom env binding tests
// ---------------------------------------------------------------------------

func TestCustomBinding_StringValue(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    return new Response(env.MY_CUSTOM);
  },
};`

	env := defaultEnv()
	env.CustomBindings = map[string]EnvBindingFunc{
		"MY_CUSTOM": func(rt JSRuntime) (any, error) {
			return "custom-value", nil
		},
	}

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	if string(r.Response.Body) != "custom-value" {
		t.Errorf("body = %q, want %q", r.Response.Body, "custom-value")
	}
}

func TestCustomBinding_FunctionBinding(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const result = env.greet("world");
    return new Response(result);
  },
};`

	env := defaultEnv()
	env.CustomBindings = map[string]EnvBindingFunc{
		"greet": func(rt JSRuntime) (any, error) {
			// Register Go function and construct a JS wrapper.
			if err := rt.RegisterFunc("__custom_greet", func(name string) string {
				return "Hello, " + name + "!"
			}); err != nil {
				return nil, err
			}
			if err := rt.Eval("globalThis.__tmp_custom_val = function(name) { return __custom_greet(name); };"); err != nil {
				return nil, err
			}
			return nil, nil
		},
	}

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	if string(r.Response.Body) != "Hello, world!" {
		t.Errorf("body = %q, want %q", r.Response.Body, "Hello, world!")
	}
}

func TestCustomBinding_ObjectWithMethods(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const sum = env.math.add(3, 4);
    return new Response(String(sum));
  },
};`

	env := defaultEnv()
	env.CustomBindings = map[string]EnvBindingFunc{
		"math": func(rt JSRuntime) (any, error) {
			if err := rt.RegisterFunc("__custom_math_add", func(a, b int) int {
				return a + b
			}); err != nil {
				return nil, err
			}
			if err := rt.Eval("globalThis.__tmp_custom_val = { add: function(a, b) { return __custom_math_add(a, b); } };"); err != nil {
				return nil, err
			}
			return nil, nil
		},
	}

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	if string(r.Response.Body) != "7" {
		t.Errorf("body = %q, want %q", r.Response.Body, "7")
	}
}

func TestCustomBinding_ErrorInBindingFunc(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    return new Response("should not reach");
  },
};`

	env := defaultEnv()
	env.CustomBindings = map[string]EnvBindingFunc{
		"BAD": func(rt JSRuntime) (any, error) {
			return nil, fmt.Errorf("binding setup failed")
		},
	}

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	if r.Error == nil {
		t.Fatal("expected error from broken custom binding")
	}
	if got := r.Error.Error(); !strings.Contains(got, "custom binding") || !strings.Contains(got, "BAD") {
		t.Errorf("error = %q, want mention of custom binding BAD", got)
	}
}

func TestCustomBinding_CoexistsWithBuiltinBindings(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const parts = [env.MY_VAR, env.custom_val];
    return new Response(parts.join(","));
  },
};`

	env := defaultEnv()
	env.Vars["MY_VAR"] = "from-vars"
	env.CustomBindings = map[string]EnvBindingFunc{
		"custom_val": func(rt JSRuntime) (any, error) {
			return "from-custom", nil
		},
	}

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	if string(r.Response.Body) != "from-vars,from-custom" {
		t.Errorf("body = %q, want %q", r.Response.Body, "from-vars,from-custom")
	}
}

func TestCustomBinding_MultipleBindings(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    return new Response(env.A + "-" + env.B + "-" + env.C);
  },
};`

	env := defaultEnv()
	env.CustomBindings = map[string]EnvBindingFunc{
		"A": func(rt JSRuntime) (any, error) { return "alpha", nil },
		"B": func(rt JSRuntime) (any, error) { return "beta", nil },
		"C": func(rt JSRuntime) (any, error) { return "gamma", nil },
	}

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	if string(r.Response.Body) != "alpha-beta-gamma" {
		t.Errorf("body = %q, want %q", r.Response.Body, "alpha-beta-gamma")
	}
}

func TestCustomBinding_NilMapIsNoOp(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    return new Response("ok");
  },
};`

	env := defaultEnv()
	// CustomBindings is nil by default — should not cause any issues.

	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	if string(r.Response.Body) != "ok" {
		t.Errorf("body = %q, want %q", r.Response.Body, "ok")
	}
}

// ---------------------------------------------------------------------------
// ExecuteFunction tests
// ---------------------------------------------------------------------------

// execFn compiles a worker from source and calls a named function via ExecuteFunction.
func execFn(t *testing.T, e *Engine, source string, env *Env, fnName string, args ...any) *WorkerResult {
	t.Helper()
	siteID := "test-" + t.Name()
	deployKey := "deploy1"

	if _, err := e.CompileAndCache(siteID, deployKey, source); err != nil {
		t.Fatalf("CompileAndCache: %v", err)
	}
	return e.ExecuteFunction(siteID, deployKey, env, fnName, args...)
}

func TestExecuteFunction_BasicReturn(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  run(env) {
    return { message: "hello from plugin" };
  },
};`

	r := execFn(t, e, source, defaultEnv(), "run")
	if r.Error != nil {
		t.Fatalf("unexpected error: %v", r.Error)
	}

	var data map[string]string
	if err := json.Unmarshal([]byte(r.Data), &data); err != nil {
		t.Fatalf("parsing Data: %v (raw: %q)", err, r.Data)
	}
	if data["message"] != "hello from plugin" {
		t.Errorf("message = %q, want %q", data["message"], "hello from plugin")
	}
}

func TestExecuteFunction_WithArgs(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  add(env, a, b) {
    return { sum: a + b };
  },
};`

	r := execFn(t, e, source, defaultEnv(), "add", 3, 4)
	if r.Error != nil {
		t.Fatalf("unexpected error: %v", r.Error)
	}

	var data map[string]float64
	if err := json.Unmarshal([]byte(r.Data), &data); err != nil {
		t.Fatalf("parsing Data: %v (raw: %q)", err, r.Data)
	}
	if data["sum"] != 7 {
		t.Errorf("sum = %v, want 7", data["sum"])
	}
}

func TestExecuteFunction_WithCustomBindings(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  run(env) {
    const val = env.db.get("key1");
    return { fetched: val };
  },
};`

	env := defaultEnv()
	env.CustomBindings = map[string]EnvBindingFunc{
		"db": func(rt JSRuntime) (any, error) {
			if err := rt.RegisterFunc("__custom_db_get", func(key string) string {
				return "value-for-" + key
			}); err != nil {
				return nil, err
			}
			if err := rt.Eval("globalThis.__tmp_custom_val = { get: function(k) { return __custom_db_get(k); } };"); err != nil {
				return nil, err
			}
			return nil, nil
		},
	}

	r := execFn(t, e, source, env, "run")
	if r.Error != nil {
		t.Fatalf("unexpected error: %v", r.Error)
	}

	var data map[string]string
	if err := json.Unmarshal([]byte(r.Data), &data); err != nil {
		t.Fatalf("parsing Data: %v (raw: %q)", err, r.Data)
	}
	if data["fetched"] != "value-for-key1" {
		t.Errorf("fetched = %q, want %q", data["fetched"], "value-for-key1")
	}
}

func TestExecuteFunction_AsyncFunction(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async run(env) {
    return { async: true, val: 42 };
  },
};`

	r := execFn(t, e, source, defaultEnv(), "run")
	if r.Error != nil {
		t.Fatalf("unexpected error: %v", r.Error)
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(r.Data), &data); err != nil {
		t.Fatalf("parsing Data: %v (raw: %q)", err, r.Data)
	}
	if data["async"] != true {
		t.Errorf("async = %v, want true", data["async"])
	}
	if data["val"] != float64(42) {
		t.Errorf("val = %v, want 42", data["val"])
	}
}

func TestExecuteFunction_ReturnsNull(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  run(env) {
    return null;
  },
};`

	r := execFn(t, e, source, defaultEnv(), "run")
	if r.Error != nil {
		t.Fatalf("unexpected error: %v", r.Error)
	}
	if r.Data != "null" {
		t.Errorf("Data = %q, want %q", r.Data, "null")
	}
}

func TestExecuteFunction_ReturnsUndefined(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  run(env) {
    // no return
  },
};`

	r := execFn(t, e, source, defaultEnv(), "run")
	if r.Error != nil {
		t.Fatalf("unexpected error: %v", r.Error)
	}
	if r.Data != "null" {
		t.Errorf("Data = %q, want %q", r.Data, "null")
	}
}

func TestExecuteFunction_MissingFunction(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    return new Response("ok");
  },
};`

	r := execFn(t, e, source, defaultEnv(), "nonexistent")
	if r.Error == nil {
		t.Fatal("expected error for missing function")
	}
	if !strings.Contains(r.Error.Error(), "nonexistent") {
		t.Errorf("error = %q, want mention of nonexistent", r.Error.Error())
	}
}

func TestExecuteFunction_StringArg(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  greet(env, name) {
    return { greeting: "Hello, " + name + "!" };
  },
};`

	r := execFn(t, e, source, defaultEnv(), "greet", "Alice")
	if r.Error != nil {
		t.Fatalf("unexpected error: %v", r.Error)
	}

	var data map[string]string
	if err := json.Unmarshal([]byte(r.Data), &data); err != nil {
		t.Fatalf("parsing Data: %v (raw: %q)", err, r.Data)
	}
	if data["greeting"] != "Hello, Alice!" {
		t.Errorf("greeting = %q, want %q", data["greeting"], "Hello, Alice!")
	}
}

func TestExecuteFunction_ObjectArg(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  process(env, config) {
    return { mode: config.mode, count: config.items.length };
  },
};`

	arg := map[string]any{
		"mode":  "batch",
		"items": []string{"a", "b", "c"},
	}

	r := execFn(t, e, source, defaultEnv(), "process", arg)
	if r.Error != nil {
		t.Fatalf("unexpected error: %v", r.Error)
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(r.Data), &data); err != nil {
		t.Fatalf("parsing Data: %v (raw: %q)", err, r.Data)
	}
	if data["mode"] != "batch" {
		t.Errorf("mode = %v, want %q", data["mode"], "batch")
	}
	if data["count"] != float64(3) {
		t.Errorf("count = %v, want 3", data["count"])
	}
}

func TestExecuteFunction_PluginCallsBackIntoGo(t *testing.T) {
	e := newTestEngine(t)

	// Simulates the full plugin pattern: JS orchestrates, Go does the work.
	source := `export default {
  run(env, items) {
    let saved = 0;
    for (const item of items) {
      env.store.save(item.name, item.value);
      saved++;
    }
    return { saved: saved, total: env.store.count() };
  },
};`

	var store []string
	env := defaultEnv()
	env.CustomBindings = map[string]EnvBindingFunc{
		"store": func(rt JSRuntime) (any, error) {
			if err := rt.RegisterFunc("__custom_store_save", func(name, value string) {
				store = append(store, name+"="+value)
			}); err != nil {
				return nil, err
			}
			if err := rt.RegisterFunc("__custom_store_count", func() int {
				return len(store)
			}); err != nil {
				return nil, err
			}
			if err := rt.Eval(`globalThis.__tmp_custom_val = {
				save: function(name, value) { __custom_store_save(name, value); },
				count: function() { return __custom_store_count(); }
			};`); err != nil {
				return nil, err
			}
			return nil, nil
		},
	}

	items := []map[string]string{
		{"name": "key1", "value": "val1"},
		{"name": "key2", "value": "val2"},
		{"name": "key3", "value": "val3"},
	}

	r := execFn(t, e, source, env, "run", items)
	if r.Error != nil {
		t.Fatalf("unexpected error: %v", r.Error)
	}

	// Verify Go side received the calls.
	if len(store) != 3 {
		t.Fatalf("store has %d entries, want 3", len(store))
	}
	if store[0] != "key1=val1" {
		t.Errorf("store[0] = %q, want %q", store[0], "key1=val1")
	}

	// Verify JS return value.
	var data map[string]float64
	if err := json.Unmarshal([]byte(r.Data), &data); err != nil {
		t.Fatalf("parsing Data: %v (raw: %q)", err, r.Data)
	}
	if data["saved"] != 3 {
		t.Errorf("saved = %v, want 3", data["saved"])
	}
	if data["total"] != 3 {
		t.Errorf("total = %v, want 3", data["total"])
	}
}

func TestExecuteFunction_CapturesLogs(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  run(env) {
    console.log("plugin started");
    console.warn("something fishy");
    return { done: true };
  },
};`

	r := execFn(t, e, source, defaultEnv(), "run")
	if r.Error != nil {
		t.Fatalf("unexpected error: %v", r.Error)
	}

	if len(r.Logs) < 2 {
		t.Fatalf("expected at least 2 log entries, got %d", len(r.Logs))
	}

	foundLog := false
	foundWarn := false
	for _, entry := range r.Logs {
		if entry.Level == "log" && strings.Contains(entry.Message, "plugin started") {
			foundLog = true
		}
		if entry.Level == "warn" && strings.Contains(entry.Message, "something fishy") {
			foundWarn = true
		}
	}
	if !foundLog {
		t.Error("missing console.log entry")
	}
	if !foundWarn {
		t.Error("missing console.warn entry")
	}
}

func TestExecuteFunction_ReturnsScalar(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  version(env) {
    return 42;
  },
};`

	r := execFn(t, e, source, defaultEnv(), "version")
	if r.Error != nil {
		t.Fatalf("unexpected error: %v", r.Error)
	}
	if r.Data != "42" {
		t.Errorf("Data = %q, want %q", r.Data, "42")
	}
}

func TestExecuteFunction_ReturnsString(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  name(env) {
    return "my-plugin";
  },
};`

	r := execFn(t, e, source, defaultEnv(), "name")
	if r.Error != nil {
		t.Fatalf("unexpected error: %v", r.Error)
	}
	if r.Data != `"my-plugin"` {
		t.Errorf("Data = %q, want %q", r.Data, `"my-plugin"`)
	}
}
