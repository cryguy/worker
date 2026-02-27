package worker

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// Pool state isolation and Go-backed function survival tests
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 1. CRITICAL REGRESSION: KV Go-backed functions survive pool recycling
// ---------------------------------------------------------------------------

// TestPool_GoBackedFunctionsSurviveRecycling verifies that the Go-backed
// __kv_* functions registered at pool creation are NOT wiped during the
// per-request cleanup phase (only __tmp_* and __fn_arg_* are deleted).
// This is a regression test for a bug where wildcard deletion of __kv_* and
// __do_* globals caused "function is not defined" errors on recycled VMs.
func TestPool_GoBackedFunctionsSurviveRecycling(t *testing.T) {
	cfg := testCfg()
	cfg.PoolSize = 1 // single slot so every request reuses the same VM
	e := NewEngine(cfg, nilSourceLoader{})
	t.Cleanup(func() { e.Shutdown() })

	kvStore := newMockKVStore()
	env := &Env{
		Vars:    make(map[string]string),
		Secrets: make(map[string]string),
		KV:      map[string]KVStore{"NS": kvStore},
	}

	source := `export default {
  async fetch(request, env) {
    await env.NS.put("k", "v1");
    const val = await env.NS.get("k");
    return new Response(val);
  },
};`

	siteID := "kv-survive-" + t.Name()
	if _, err := e.CompileAndCache(siteID, "deploy1", source); err != nil {
		t.Fatalf("CompileAndCache: %v", err)
	}

	for i := 0; i < 5; i++ {
		r := e.Execute(siteID, "deploy1", env, getReq("http://localhost/"))
		if r.Error != nil {
			t.Fatalf("request %d: unexpected error (KV functions may have been wiped on recycle): %v", i, r.Error)
		}
		assertOK(t, r)
		if string(r.Response.Body) != "v1" {
			t.Errorf("request %d: body = %q, want \"v1\"", i, r.Response.Body)
		}
	}
}

// ---------------------------------------------------------------------------
// 2. CRITICAL REGRESSION: Durable Object Go-backed functions survive recycling
// ---------------------------------------------------------------------------

// TestPool_DOFunctionsSurviveRecycling is the DO equivalent of the KV
// regression above — confirms __do_* functions are not wiped on recycle.
func TestPool_DOFunctionsSurviveRecycling(t *testing.T) {
	cfg := testCfg()
	cfg.PoolSize = 1
	e := NewEngine(cfg, nilSourceLoader{})
	t.Cleanup(func() { e.Shutdown() })

	doStore := newMockDurableObjectStore()
	env := &Env{
		Vars:           make(map[string]string),
		Secrets:        make(map[string]string),
		DurableObjects: map[string]DurableObjectStore{"MY_DO": doStore},
	}

	source := `export default {
  async fetch(request, env) {
    const stub = env.MY_DO.get("obj1");
    await stub.storage.put("counter", 99);
    const val = await stub.storage.get("counter");
    return new Response(String(val));
  },
};`

	siteID := "do-survive-" + t.Name()
	if _, err := e.CompileAndCache(siteID, "deploy1", source); err != nil {
		t.Fatalf("CompileAndCache: %v", err)
	}

	for i := 0; i < 5; i++ {
		r := e.Execute(siteID, "deploy1", env, getReq("http://localhost/"))
		if r.Error != nil {
			t.Fatalf("request %d: unexpected error (DO functions may have been wiped on recycle): %v", i, r.Error)
		}
		assertOK(t, r)
		if string(r.Response.Body) != "99" {
			t.Errorf("request %d: body = %q, want \"99\"", i, r.Response.Body)
		}
	}
}

// ---------------------------------------------------------------------------
// 3. Global state isolation between requests
// ---------------------------------------------------------------------------

// TestPool_GlobalStateIsolation verifies that __tmp_* globals set during one
// request are cleaned up before the next request executes, so that no state
// leaks between requests via globalThis.
func TestPool_GlobalStateIsolation(t *testing.T) {
	cfg := testCfg()
	cfg.PoolSize = 1
	e := NewEngine(cfg, nilSourceLoader{})
	t.Cleanup(func() { e.Shutdown() })

	// Request 1 sets a custom global; request 2 checks whether it is still set.
	setSource := `export default {
  fetch(request, env) {
    globalThis.__testLeak = "leaked";
    return new Response("set");
  },
};`

	checkSource := `export default {
  fetch(request, env) {
    const val = globalThis.__testLeak;
    return new Response(val === undefined ? "clean" : val);
  },
};`

	siteSet := "global-set-" + t.Name()
	siteCheck := "global-check-" + t.Name()

	if _, err := e.CompileAndCache(siteSet, "deploy1", setSource); err != nil {
		t.Fatalf("compile set: %v", err)
	}
	if _, err := e.CompileAndCache(siteCheck, "deploy1", checkSource); err != nil {
		t.Fatalf("compile check: %v", err)
	}

	env := defaultEnv()

	r1 := e.Execute(siteSet, "deploy1", env, getReq("http://localhost/"))
	assertOK(t, r1)
	if string(r1.Response.Body) != "set" {
		t.Fatalf("r1 body = %q, want \"set\"", r1.Response.Body)
	}

	r2 := e.Execute(siteCheck, "deploy1", env, getReq("http://localhost/"))
	assertOK(t, r2)
	body := string(r2.Response.Body)
	if strings.HasPrefix(body, "leaked") {
		t.Errorf("global state leaked between requests: got %q, want \"clean\"", body)
	}
}

// ---------------------------------------------------------------------------
// 4. Concurrent requests return distinct, uncontaminated payloads
// ---------------------------------------------------------------------------

// TestPool_ConcurrentDistinctPayloads fires 20 concurrent goroutines, each
// with a unique URL path, and verifies that every response echoes back the
// correct path for that goroutine (no cross-request contamination).
func TestPool_ConcurrentDistinctPayloads(t *testing.T) {
	cfg := testCfg()
	cfg.PoolSize = 4
	e := NewEngine(cfg, nilSourceLoader{})
	t.Cleanup(func() { e.Shutdown() })

	source := `export default {
  fetch(request, env) {
    const url = new URL(request.url);
    return new Response(url.pathname);
  },
};`

	siteID := "concurrent-payloads-" + t.Name()
	if _, err := e.CompileAndCache(siteID, "deploy1", source); err != nil {
		t.Fatalf("CompileAndCache: %v", err)
	}

	const numWorkers = 20
	type result struct {
		want string
		got  string
		err  error
	}
	results := make([]result, numWorkers)
	var wg sync.WaitGroup

	for i := 0; i < numWorkers; i++ {
		i := i
		path := fmt.Sprintf("/worker/%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := e.Execute(siteID, "deploy1", defaultEnv(), getReq("http://localhost"+path))
			if r.Error != nil {
				results[i] = result{want: path, err: r.Error}
				return
			}
			results[i] = result{want: path, got: string(r.Response.Body)}
		}()
	}
	wg.Wait()

	for _, res := range results {
		if res.err != nil {
			t.Errorf("path %s: error %v", res.want, res.err)
			continue
		}
		if res.got != res.want {
			t.Errorf("path %s: got %q (cross-request contamination)", res.want, res.got)
		}
	}
}

// ---------------------------------------------------------------------------
// 5. Concurrent KV operations — no cross-contamination
// ---------------------------------------------------------------------------

// TestPool_ConcurrentKVIsolation fires 10 concurrent goroutines, each writing
// a unique key to a shared KV store and reading it back, verifying no values
// are swapped between goroutines.
func TestPool_ConcurrentKVIsolation(t *testing.T) {
	cfg := testCfg()
	cfg.PoolSize = 4
	e := NewEngine(cfg, nilSourceLoader{})
	t.Cleanup(func() { e.Shutdown() })

	source := `export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const key = url.searchParams.get("key");
    const val = url.searchParams.get("val");
    await env.KV.put(key, val);
    const got = await env.KV.get(key);
    return new Response(got);
  },
};`

	siteID := "concurrent-kv-" + t.Name()
	if _, err := e.CompileAndCache(siteID, "deploy1", source); err != nil {
		t.Fatalf("CompileAndCache: %v", err)
	}

	// Shared KV store — concurrent writes go to distinct keys.
	kvStore := newMockKVStore()
	sharedEnv := &Env{
		Vars:    make(map[string]string),
		Secrets: make(map[string]string),
		KV:      map[string]KVStore{"KV": kvStore},
	}

	const numWorkers = 10
	type result struct {
		key string
		val string
		got string
		err error
	}
	results := make([]result, numWorkers)
	var wg sync.WaitGroup

	for i := 0; i < numWorkers; i++ {
		i := i
		key := fmt.Sprintf("key-%d", i)
		val := fmt.Sprintf("value-%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			url := fmt.Sprintf("http://localhost/?key=%s&val=%s", key, val)
			r := e.Execute(siteID, "deploy1", sharedEnv, getReq(url))
			if r.Error != nil {
				results[i] = result{key: key, val: val, err: r.Error}
				return
			}
			results[i] = result{key: key, val: val, got: string(r.Response.Body)}
		}()
	}
	wg.Wait()

	for _, res := range results {
		if res.err != nil {
			t.Errorf("key %s: error %v", res.key, res.err)
			continue
		}
		if res.got != res.val {
			t.Errorf("key %s: got %q, want %q (KV contamination)", res.key, res.got, res.val)
		}
	}
}

// ---------------------------------------------------------------------------
// 6. Engine recovers after execution timeout
// ---------------------------------------------------------------------------

// TestPool_TimeoutRecovery verifies that after a worker times out, the engine
// can still successfully execute subsequent requests (pool slot is returned to
// a healthy state after timeout recovery).
func TestPool_TimeoutRecovery(t *testing.T) {
	cfg := testCfg()
	cfg.PoolSize = 1
	cfg.ExecutionTimeout = 1000 // 1 second
	e := NewEngine(cfg, nilSourceLoader{})
	t.Cleanup(func() { e.Shutdown() })

	loopSource := `export default {
  fetch() {
    while (true) {}
    return new Response("unreachable");
  },
};`

	okSource := `export default {
  fetch() { return new Response("recovered"); }
};`

	siteLoop := "timeout-loop-" + t.Name()
	siteOK := "timeout-ok-" + t.Name()

	if _, err := e.CompileAndCache(siteLoop, "deploy1", loopSource); err != nil {
		t.Fatalf("compile loop: %v", err)
	}
	if _, err := e.CompileAndCache(siteOK, "deploy1", okSource); err != nil {
		t.Fatalf("compile ok: %v", err)
	}

	env := defaultEnv()

	// First request should time out.
	rLoop := e.Execute(siteLoop, "deploy1", env, getReq("http://localhost/"))
	if rLoop.Error == nil {
		t.Fatal("expected timeout error for infinite loop, got nil")
	}
	if !strings.Contains(rLoop.Error.Error(), "timed out") {
		t.Errorf("error = %v, expected 'timed out'", rLoop.Error)
	}

	// Second request on a different site should succeed (engine recovered).
	rOK := e.Execute(siteOK, "deploy1", env, getReq("http://localhost/"))
	assertOK(t, rOK)
	if string(rOK.Response.Body) != "recovered" {
		t.Errorf("body = %q, want \"recovered\"", rOK.Response.Body)
	}
}

// ---------------------------------------------------------------------------
// 7. Engine recovers after OOM (memory limit exceeded)
// ---------------------------------------------------------------------------

// TestPool_MemoryLimitRecovery verifies that after a worker exhausts the
// memory limit, the engine can still serve subsequent requests successfully.
func TestPool_MemoryLimitRecovery(t *testing.T) {
	cfg := testCfg()
	cfg.PoolSize = 1
	cfg.MemoryLimitMB = 8
	cfg.ExecutionTimeout = 5000
	e := NewEngine(cfg, nilSourceLoader{})
	t.Cleanup(func() { e.Shutdown() })

	oomSource := `export default {
  fetch() {
    let a = [];
    while (true) { a.push(new ArrayBuffer(1024 * 1024)); }
    return new Response("unreachable");
  },
};`

	okSource := `export default {
  fetch() { return new Response("recovered"); }
};`

	siteOOM := "oom-" + t.Name()
	siteOK := "oom-ok-" + t.Name()

	if _, err := e.CompileAndCache(siteOOM, "deploy1", oomSource); err != nil {
		t.Fatalf("compile oom: %v", err)
	}
	if _, err := e.CompileAndCache(siteOK, "deploy1", okSource); err != nil {
		t.Fatalf("compile ok: %v", err)
	}

	env := defaultEnv()

	// First request should fail with OOM.
	rOOM := e.Execute(siteOOM, "deploy1", env, getReq("http://localhost/"))
	if rOOM.Error == nil {
		t.Fatal("expected error for OOM, got nil")
	}
	t.Logf("OOM error (expected): %v", rOOM.Error)

	// Second request should succeed (engine recovered).
	rOK := e.Execute(siteOK, "deploy1", env, getReq("http://localhost/"))
	assertOK(t, rOK)
	if string(rOK.Response.Body) != "recovered" {
		t.Errorf("body = %q, want \"recovered\"", rOK.Response.Body)
	}
}
