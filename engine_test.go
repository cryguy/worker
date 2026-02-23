package worker

import (
	"strings"
	"testing"

	"modernc.org/quickjs"
)

func TestModuleDefaultExportFetch(t *testing.T) {
	source := `export default {
  fetch(request, env, ctx) {
    return new Response("it works");
  }
};`

	vm, err := quickjs.NewVM()
	if err != nil {
		t.Fatalf("NewVM: %v", err)
	}
	defer vm.Close()

	el := newEventLoop()
	if err := setupWebAPIs(vm, el); err != nil {
		t.Fatalf("setupWebAPIs: %v", err)
	}

	// Run the wrapped module source (converts `export default` to globalThis.__worker_module__).
	wrapped := wrapESModule(source)
	val, err := vm.EvalValue(wrapped, quickjs.EvalGlobal)
	if err != nil {
		t.Fatalf("running wrapped module: %v", err)
	}
	val.Free()

	// Verify __worker_module__ exists.
	moduleStr, err := evalString(vm, "typeof globalThis.__worker_module__")
	if err != nil {
		t.Fatalf("getting __worker_module__: %v", err)
	}
	if moduleStr == "undefined" || moduleStr == "null" {
		t.Fatal("default export is undefined/null")
	}

	// Verify fetch is callable.
	result, err := evalString(vm, `(function() {
		var req = new Request('http://localhost/');
		return typeof globalThis.__worker_module__.fetch(req, {}, {});
	})()`)
	if err != nil {
		t.Fatalf("invoking fetch: %v", err)
	}

	t.Logf("fetch returned successfully (type=%s)", result)
}

// TestPoolModuleFlow tests the full pool setup path matching the exact
// production flow in getOrCreatePool + Execute.
func TestPoolModuleFlow(t *testing.T) {
	source := `export default {
  fetch(request, env, ctx) {
    return new Response("hello from pool test");
  }
};`

	cfg := EngineConfig{
		PoolSize:         2,
		MemoryLimitMB:    128,
		ExecutionTimeout: 5000,
		MaxFetchRequests: 10,
		FetchTimeoutSec:  5,
		MaxResponseBytes: 1024 * 1024,
	}

	pool, err := newQJSPool(cfg.PoolSize, source, []setupFunc{
		setupWebAPIs,
		setupConsole,
		func(vm *quickjs.VM, el *eventLoop) error {
			return setupFetchWithConfig(vm, cfg, el)
		},
	}, cfg.MemoryLimitMB)
	if err != nil {
		t.Fatalf("newV8Pool: %v", err)
	}
	defer pool.dispose()

	w, err := pool.get()
	if err != nil {
		t.Fatalf("pool.get: %v", err)
	}
	defer pool.put(w)

	// Check __worker_module__ exists (same as Execute does).
	moduleStr, err := evalString(w.vm, "typeof globalThis.__worker_module__")
	if err != nil || moduleStr == "undefined" || moduleStr == "null" {
		t.Fatal("__worker_module__ is undefined/null — default export not captured")
	}

	// Call fetch (same as Execute does).
	fetchType, err := evalString(w.vm, `(function() {
		var req = new Request('http://localhost/test');
		return typeof globalThis.__worker_module__.fetch(req, {}, {});
	})()`)
	if err != nil {
		t.Fatalf("invoking fetch: %v", err)
	}

	t.Logf("pool flow: fetch returned (type=%s)", fetchType)
}

// TestAsyncFetchHandler tests that async fetch handlers (returning Promise<Response>)
// are correctly awaited and converted, matching the exact Execute() flow.
func TestAsyncFetchHandler(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request) {
    const url = new URL(request.url);
    const name = url.searchParams.get("name") || "world";
    return new Response("Hello, " + name + "!");
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/api/hello?name=test"))
	assertOK(t, r)

	want := "Hello, test!"
	if string(r.Response.Body) != want {
		t.Fatalf("body = %q, want %q", r.Response.Body, want)
	}
	t.Logf("response: status=%d body=%q headers=%v", r.Response.StatusCode, string(r.Response.Body), r.Response.Headers)
}

// TestAssetsFetch tests that env.ASSETS.fetch(request) works correctly
// by using a mock AssetsFetcher and running the full worker execution flow.
func TestAssetsFetch(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    return env.ASSETS.fetch(request);
  },
};`

	mockFetcher := &mockAssetsFetcher{
		response: &WorkerResponse{
			StatusCode: 200,
			Headers:    map[string]string{"content-type": "text/html; charset=utf-8"},
			Body:       []byte("<h1>Hello from ASSETS</h1>"),
		},
	}

	env := &Env{
		Vars:    make(map[string]string),
		Secrets: make(map[string]string),
		Assets:  mockFetcher,
	}

	r := execJS(t, e, source, env, getReq("http://localhost/index.html"))
	assertOK(t, r)
	if r.Response.StatusCode != 200 {
		t.Fatalf("expected status 200, got %d", r.Response.StatusCode)
	}
	if string(r.Response.Body) != "<h1>Hello from ASSETS</h1>" {
		t.Fatalf("unexpected body: %q", string(r.Response.Body))
	}
	t.Logf("ASSETS.fetch: status=%d body=%q", r.Response.StatusCode, string(r.Response.Body))
}

// mockAssetsFetcher implements AssetsFetcher for testing.
type mockAssetsFetcher struct {
	response *WorkerResponse
	err      error
}

func (m *mockAssetsFetcher) Fetch(_ *WorkerRequest) (*WorkerResponse, error) {
	return m.response, m.err
}

// ---------------------------------------------------------------------------
// Additional Coverage Tests
// ---------------------------------------------------------------------------

func TestEngine_MaxResponseBytes(t *testing.T) {
	cfg := testCfg()
	cfg.MaxResponseBytes = 12345678
	e := NewEngine(cfg, nilSourceLoader{})
	defer e.Shutdown()

	got := e.MaxResponseBytes()
	if got != 12345678 {
		t.Errorf("MaxResponseBytes() = %d, want 12345678", got)
	}
}

// BuildEnvFromDB tests have been moved to internal/workeradapter/ where the function is now defined.

func TestEngine_EnsureSource_FromCache(t *testing.T) {
	e := newTestEngine(t)

	siteID := "test-source-cache"
	deployKey := "deploy1"

	source := `export default { fetch() { return new Response("ok"); } };`

	srcBytes, err := e.CompileAndCache(siteID, deployKey, source)
	if err != nil {
		t.Fatalf("CompileAndCache: %v", err)
	}
	if len(srcBytes) == 0 {
		t.Fatal("source bytes are empty")
	}

	// Clear the in-memory cache to simulate server restart.
	key := poolKey{SiteID: siteID, DeployKey: deployKey}
	e.sources.Delete(key)

	// Manually store it back.
	e.sources.Store(key, source)

	// EnsureSource should find it in cache and not error.
	err = e.EnsureSource(siteID, deployKey)
	if err != nil {
		t.Errorf("EnsureSource (from cache): %v", err)
	}
}

func TestEngine_EnsureSource_NoStore(t *testing.T) {
	e := newTestEngine(t)

	siteID := "test-no-store"
	deployKey := "deploy1"

	// Clear source cache.
	key := poolKey{SiteID: siteID, DeployKey: deployKey}
	e.sources.Delete(key)

	// EnsureSource should fail when there's no cached source and no store.
	err := e.EnsureSource(siteID, deployKey)
	if err == nil {
		t.Fatal("EnsureSource should fail when store is not set")
	}
	if !strings.Contains(err.Error(), "no source for site") || !strings.Contains(err.Error(), "no source loader configured") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestEngine_CompileAndCache_InvalidSource(t *testing.T) {
	e := newTestEngine(t)

	_, err := e.CompileAndCache("site1", "deploy1", "function {{{invalid")
	if err == nil {
		t.Fatal("CompileAndCache should fail for invalid JS")
	}
}

func TestEngine_CompileAndCache_ReturnBytes(t *testing.T) {
	e := newTestEngine(t)

	source := `export default { fetch() { return new Response("ok"); } };`
	bytes, err := e.CompileAndCache("site1", "deploy1", source)
	if err != nil {
		t.Fatalf("CompileAndCache: %v", err)
	}
	if string(bytes) != source {
		t.Errorf("returned bytes don't match source")
	}
}

func TestEngine_InvalidatePool(t *testing.T) {
	e := newTestEngine(t)

	source := `export default { fetch() { return new Response("ok"); } };`
	siteID := "test-invalidate"
	deployKey := "deploy1"

	if _, err := e.CompileAndCache(siteID, deployKey, source); err != nil {
		t.Fatalf("CompileAndCache: %v", err)
	}

	// Create pool
	pool, err := e.getOrCreatePool(siteID, deployKey)
	if err != nil {
		t.Fatalf("getOrCreatePool: %v", err)
	}
	if pool == nil {
		t.Fatal("pool should not be nil")
	}

	// Invalidate
	e.InvalidatePool(siteID, deployKey)

	// Source should be cleared
	key := poolKey{SiteID: siteID, DeployKey: deployKey}
	if _, ok := e.sources.Load(key); ok {
		t.Error("source should be cleared after InvalidatePool")
	}
}
