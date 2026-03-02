package worker

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// mockCacheStore — in-memory CacheStore for V8 integration tests.
// ---------------------------------------------------------------------------

type mockCacheStore struct {
	mu    sync.Mutex
	items map[string]map[string]*CacheEntry // cacheName -> url -> entry
}

func newMockCacheStore() *mockCacheStore {
	return &mockCacheStore{items: make(map[string]map[string]*CacheEntry)}
}

func (m *mockCacheStore) Match(cacheName, url string) (*CacheEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.items[cacheName]; ok {
		if e, ok := c[url]; ok {
			if e.ExpiresAt != nil && e.ExpiresAt.Before(time.Now()) {
				delete(c, url)
				return nil, nil
			}
			return e, nil
		}
	}
	return nil, nil
}

func (m *mockCacheStore) Put(cacheName, url string, status int, headers string, body []byte, ttl *int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.items[cacheName] == nil {
		m.items[cacheName] = make(map[string]*CacheEntry)
	}
	var expiresAt *time.Time
	if ttl != nil && *ttl > 0 {
		t := time.Now().Add(time.Duration(*ttl) * time.Second)
		expiresAt = &t
	}
	m.items[cacheName][url] = &CacheEntry{
		Status:    status,
		Headers:   headers,
		Body:      body,
		ExpiresAt: expiresAt,
	}
	return nil
}

func (m *mockCacheStore) Delete(cacheName, url string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.items[cacheName]; ok {
		if _, ok := c[url]; ok {
			delete(c, url)
			return true, nil
		}
	}
	return false, nil
}

// count returns the number of entries for a given cacheName and url (0 or 1).
func (m *mockCacheStore) count(cacheName, url string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.items[cacheName]; ok {
		if _, ok := c[url]; ok {
			return 1
		}
	}
	return 0
}

// forceExpire sets a cache entry's expiry into the past (test helper).
func (m *mockCacheStore) forceExpire(cacheName, url string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.items[cacheName]; ok {
		if e, ok := c[url]; ok {
			t := time.Now().Add(-1 * time.Second)
			e.ExpiresAt = &t
		}
	}
}

// cacheEnv returns an Env with an in-memory CacheStore for V8 cache tests.
func cacheEnv() *Env {
	return &Env{
		Vars:    make(map[string]string),
		Secrets: make(map[string]string),
		Cache:   newMockCacheStore(),
	}
}

func TestCacheBridge_PutAndMatch(t *testing.T) {
	bridge := newMockCacheStore()

	err := bridge.Put("default", "https://example.com/test", 200, `{"content-type":"text/html"}`, []byte("hello"), nil)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	entry, err := bridge.Match("default", "https://example.com/test")
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if entry == nil {
		t.Fatal("Match returned nil, expected entry")
	}
	if entry.Status != 200 {
		t.Errorf("Status = %d, want 200", entry.Status)
	}
	if string(entry.Body) != "hello" {
		t.Errorf("Body = %q, want 'hello'", string(entry.Body))
	}
}

func TestCacheBridge_MatchNotFound(t *testing.T) {
	bridge := newMockCacheStore()

	entry, err := bridge.Match("default", "https://example.com/missing")
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if entry != nil {
		t.Errorf("expected nil for missing entry, got %+v", entry)
	}
}

func TestCacheBridge_PutReplaces(t *testing.T) {
	bridge := newMockCacheStore()

	_ = bridge.Put("default", "https://example.com/page", 200, "{}", []byte("first"), nil)
	_ = bridge.Put("default", "https://example.com/page", 201, "{}", []byte("second"), nil)

	entry, _ := bridge.Match("default", "https://example.com/page")
	if entry == nil {
		t.Fatal("Match returned nil")
	}
	if entry.Status != 201 {
		t.Errorf("Status = %d, want 201 (replaced)", entry.Status)
	}
	if string(entry.Body) != "second" {
		t.Errorf("Body = %q, want 'second'", string(entry.Body))
	}

	// Verify only one entry exists.
	count := bridge.count("default", "https://example.com/page")
	if count != 1 {
		t.Errorf("expected 1 entry, got %d", count)
	}
}

func TestCacheBridge_Delete(t *testing.T) {
	bridge := newMockCacheStore()

	_ = bridge.Put("default", "https://example.com/del", 200, "{}", []byte("data"), nil)
	deleted, err := bridge.Delete("default", "https://example.com/del")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !deleted {
		t.Error("Delete should return true for existing entry")
	}

	entry, _ := bridge.Match("default", "https://example.com/del")
	if entry != nil {
		t.Error("entry should be deleted")
	}
}

func TestCacheBridge_DeleteNotFound(t *testing.T) {
	bridge := newMockCacheStore()

	deleted, err := bridge.Delete("default", "https://example.com/nope")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if deleted {
		t.Error("Delete should return false for non-existent entry")
	}
}

func TestCacheBridge_TTLExpiration(t *testing.T) {
	bridge := newMockCacheStore()

	// Put an entry then force-expire it.
	ttl := 3600
	_ = bridge.Put("default", "https://example.com/ttl", 200, "{}", []byte("expired"), &ttl)

	// Force expiration into the past.
	bridge.forceExpire("default", "https://example.com/ttl")

	entry, _ := bridge.Match("default", "https://example.com/ttl")
	if entry != nil {
		t.Error("expired entry should not be returned by Match")
	}
}

func TestCacheBridge_SeparateCacheNames(t *testing.T) {
	bridge := newMockCacheStore()

	_ = bridge.Put("cache-a", "https://example.com/url", 200, "{}", []byte("from-a"), nil)
	_ = bridge.Put("cache-b", "https://example.com/url", 200, "{}", []byte("from-b"), nil)

	entryA, _ := bridge.Match("cache-a", "https://example.com/url")
	entryB, _ := bridge.Match("cache-b", "https://example.com/url")

	if entryA == nil || string(entryA.Body) != "from-a" {
		t.Errorf("cache-a entry body = %v", entryA)
	}
	if entryB == nil || string(entryB.Body) != "from-b" {
		t.Errorf("cache-b entry body = %v", entryB)
	}
}

// ---------------------------------------------------------------------------
// Integration tests (V8)
// ---------------------------------------------------------------------------

func TestCache_DefaultPutAndMatch(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    // Put a response into the default cache.
    var url = 'https://example.com/cached-page';
    var resp = new Response('cached body', {
      status: 200,
      headers: { 'Content-Type': 'text/html', 'Cache-Control': 'max-age=3600' },
    });
    await caches.default.put(url, resp);

    // Match it back.
    var matched = await caches.default.match(url);
    if (!matched) {
      return new Response('MISS', { status: 500 });
    }
    var body = matched._body;
    return Response.json({
      status: matched.status,
      body: body,
      ct: matched.headers.get('content-type'),
    });
  },
};`

	r := execJS(t, e, source, cacheEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
		CT     string `json:"ct"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Status != 200 {
		t.Errorf("status = %d, want 200", data.Status)
	}
	if data.Body != "cached body" {
		t.Errorf("body = %q, want 'cached body'", data.Body)
	}
	if !strings.Contains(data.CT, "text/html") {
		t.Errorf("content-type = %q, want text/html", data.CT)
	}
}

func TestCache_MatchMiss(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    var matched = await caches.default.match('https://example.com/not-cached');
    return Response.json({ hit: matched !== undefined });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Hit bool `json:"hit"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Hit {
		t.Error("should be a cache miss")
	}
}

func TestCache_Delete(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    var url = 'https://example.com/to-delete';
    await caches.default.put(url, new Response('data'));

    // Verify it exists.
    var before = await caches.default.match(url);
    if (!before) return new Response('put failed', { status: 500 });

    // Delete it.
    var deleted = await caches.default.delete(url);

    // Verify it's gone.
    var after = await caches.default.match(url);

    return Response.json({
      deleted: deleted,
      gone: after === undefined,
    });
  },
};`

	r := execJS(t, e, source, cacheEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Deleted bool `json:"deleted"`
		Gone    bool `json:"gone"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if !data.Deleted {
		t.Error("delete should return true")
	}
	if !data.Gone {
		t.Error("entry should be gone after delete")
	}
}

func TestCache_OpenNamedCache(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    var myCache = await caches.open('my-cache');
    var url = 'https://example.com/named';
    await myCache.put(url, new Response('named-data'));

    // Should NOT appear in default cache.
    var inDefault = await caches.default.match(url);

    // Should appear in named cache.
    var inNamed = await myCache.match(url);

    return Response.json({
      inDefault: inDefault !== undefined,
      inNamed: inNamed !== undefined,
      body: inNamed ? inNamed._body : null,
    });
  },
};`

	r := execJS(t, e, source, cacheEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		InDefault bool    `json:"inDefault"`
		InNamed   bool    `json:"inNamed"`
		Body      *string `json:"body"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.InDefault {
		t.Error("entry should not be in default cache")
	}
	if !data.InNamed {
		t.Error("entry should be in named cache")
	}
	if data.Body == nil || *data.Body != "named-data" {
		t.Errorf("body = %v, want 'named-data'", data.Body)
	}
}

func TestCache_PutWithRequest(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    var req = new Request('https://example.com/req-cache');
    await caches.default.put(req, new Response('from-request'));

    var matched = await caches.default.match(req);
    return Response.json({
      hit: matched !== undefined,
      body: matched ? matched._body : null,
    });
  },
};`

	r := execJS(t, e, source, cacheEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Hit  bool    `json:"hit"`
		Body *string `json:"body"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if !data.Hit {
		t.Error("should match using Request object")
	}
	if data.Body == nil || *data.Body != "from-request" {
		t.Errorf("body = %v, want 'from-request'", data.Body)
	}
}

func TestCache_MatchWithStringURL(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    await caches.default.put('https://example.com/str', new Response('string-url'));
    var matched = await caches.default.match('https://example.com/str');
    return Response.json({
      hit: matched !== undefined,
      body: matched ? matched._body : null,
    });
  },
};`

	r := execJS(t, e, source, cacheEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Hit  bool    `json:"hit"`
		Body *string `json:"body"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if !data.Hit {
		t.Error("should match with string URL")
	}
}

func TestCache_CachesOpenReturnsSameInstance(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    var c1 = await caches.open('test');
    var c2 = await caches.open('test');
    return Response.json({ same: c1 === c2 });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Same bool `json:"same"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if !data.Same {
		t.Error("caches.open should return same instance for same name")
	}
}

func TestCache_DeleteNonExistent(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    var deleted = await caches.default.delete('https://example.com/nonexistent');
    return Response.json({ deleted: deleted });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Deleted bool `json:"deleted"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Deleted {
		t.Error("deleting non-existent entry should return false")
	}
}

func TestCacheBridge_SiteIsolation(t *testing.T) {
	bridgeA := newMockCacheStore()
	bridgeB := newMockCacheStore()

	url := "https://example.com/shared-path"

	_ = bridgeA.Put("default", url, 200, `{"x":"a"}`, []byte("site-a-data"), nil)
	_ = bridgeB.Put("default", url, 200, `{"x":"b"}`, []byte("site-b-data"), nil)

	entryA, _ := bridgeA.Match("default", url)
	if entryA == nil || string(entryA.Body) != "site-a-data" {
		t.Errorf("Site A should see its own data, got %v", entryA)
	}

	entryB, _ := bridgeB.Match("default", url)
	if entryB == nil || string(entryB.Body) != "site-b-data" {
		t.Errorf("Site B should see its own data, got %v", entryB)
	}
}

func TestCacheBridge_DeleteScopedToSite(t *testing.T) {
	bridgeA := newMockCacheStore()
	bridgeB := newMockCacheStore()

	url := "https://example.com/delete-test"
	_ = bridgeA.Put("default", url, 200, "{}", []byte("a"), nil)
	_ = bridgeB.Put("default", url, 200, "{}", []byte("b"), nil)

	deleted, _ := bridgeA.Delete("default", url)
	if !deleted {
		t.Error("Delete A should have deleted an entry")
	}

	entryB, _ := bridgeB.Match("default", url)
	if entryB == nil {
		t.Error("Site B cache entry should still exist after Site A deletion")
	}
}

func TestCacheBridge_BinaryBody(t *testing.T) {
	bridge := newMockCacheStore()

	body := []byte{0x00, 0xFF, 0x01, 0xFE, 0x80, 0x7F, 0xAB, 0xCD}
	err := bridge.Put("default", "https://example.com/binary", 200, "{}", body, nil)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	entry, err := bridge.Match("default", "https://example.com/binary")
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if entry == nil {
		t.Fatal("Match returned nil, expected entry")
	}
	if len(entry.Body) != len(body) {
		t.Fatalf("body length = %d, want %d", len(entry.Body), len(body))
	}
	for i, b := range body {
		if entry.Body[i] != b {
			t.Errorf("body[%d] = 0x%02X, want 0x%02X", i, entry.Body[i], b)
		}
	}
}

func TestCache_PutNoResponse(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    var rejected = false;
    var msg = '';
    try {
      await caches.default.put('https://example.com/no-resp', undefined);
    } catch(e) {
      rejected = true;
      msg = e.message;
    }
    return Response.json({ rejected: rejected, msg: msg });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Rejected bool   `json:"rejected"`
		Msg      string `json:"msg"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.Rejected {
		t.Error("cache.put(url, undefined) should reject")
	}
	if !strings.Contains(data.Msg, "Cache.put requires a response") {
		t.Errorf("error message = %q, want 'Cache.put requires a response'", data.Msg)
	}
}

func TestCacheBridge_TTLZeroNoExpiry(t *testing.T) {
	bridge := newMockCacheStore()

	ttl := 0
	err := bridge.Put("default", "https://example.com/ttl-zero", 200, "{}", []byte("data"), &ttl)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	entry, err := bridge.Match("default", "https://example.com/ttl-zero")
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if entry == nil {
		t.Fatal("Match returned nil, expected entry")
	}
	if entry.ExpiresAt != nil {
		t.Errorf("ExpiresAt = %v, want nil for ttl=0", entry.ExpiresAt)
	}
}

// ---------------------------------------------------------------------------
// Cache spec compliance tests
// ---------------------------------------------------------------------------

func TestCache_MatchAll(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    var url1 = 'https://example.com/page1';
    var url2 = 'https://example.com/page2';
    await caches.default.put(url1, new Response('body1'));
    await caches.default.put(url2, new Response('body2'));

    // matchAll with a specific URL returns only that entry.
    var specific = await caches.default.matchAll(url1);
    // matchAll with no arg returns empty (our implementation).
    var all = await caches.default.matchAll();

    return Response.json({
      specificLen: specific.length,
      specificBody: specific.length > 0 ? specific[0]._body : null,
      allLen: all.length,
    });
  },
};`

	r := execJS(t, e, source, cacheEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		SpecificLen  int     `json:"specificLen"`
		SpecificBody *string `json:"specificBody"`
		AllLen       int     `json:"allLen"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.SpecificLen != 1 {
		t.Errorf("matchAll(url) length = %d, want 1", data.SpecificLen)
	}
	if data.SpecificBody == nil || *data.SpecificBody != "body1" {
		t.Errorf("matchAll(url) body = %v, want 'body1'", data.SpecificBody)
	}
	if data.AllLen != 0 {
		t.Errorf("matchAll() length = %d, want 0 (no-arg returns empty)", data.AllLen)
	}
}

func TestCache_Keys(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    var url = 'https://example.com/keyed';
    await caches.default.put(url, new Response('data'));

    var keys = await caches.default.keys(url);
    return Response.json({
      length: keys.length,
      isRequest: keys.length > 0 ? keys[0] instanceof Request : false,
      keyUrl: keys.length > 0 ? keys[0].url : null,
    });
  },
};`

	r := execJS(t, e, source, cacheEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Length    int     `json:"length"`
		IsReq    bool    `json:"isRequest"`
		KeyURL   *string `json:"keyUrl"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Length != 1 {
		t.Errorf("keys(url) length = %d, want 1", data.Length)
	}
	if !data.IsReq {
		t.Error("keys() should return Request objects")
	}
	if data.KeyURL == nil || *data.KeyURL != "https://example.com/keyed" {
		t.Errorf("key url = %v, want 'https://example.com/keyed'", data.KeyURL)
	}
}

func TestCacheStorage_Has(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    var beforeOpen = await caches.has('test-has');
    await caches.open('test-has');
    var afterOpen = await caches.has('test-has');
    return Response.json({ beforeOpen, afterOpen });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		BeforeOpen bool `json:"beforeOpen"`
		AfterOpen  bool `json:"afterOpen"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.BeforeOpen {
		t.Error("caches.has should return false before open")
	}
	if !data.AfterOpen {
		t.Error("caches.has should return true after open")
	}
}

func TestCacheStorage_Delete(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    await caches.open('test-del');
    var hasBefore = await caches.has('test-del');
    var deleted = await caches.delete('test-del');
    var hasAfter = await caches.has('test-del');
    var deletedAgain = await caches.delete('test-del');
    return Response.json({ hasBefore, deleted, hasAfter, deletedAgain });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		HasBefore    bool `json:"hasBefore"`
		Deleted      bool `json:"deleted"`
		HasAfter     bool `json:"hasAfter"`
		DeletedAgain bool `json:"deletedAgain"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if !data.HasBefore {
		t.Error("cache should exist before delete")
	}
	if !data.Deleted {
		t.Error("caches.delete should return true for existing cache")
	}
	if data.HasAfter {
		t.Error("cache should not exist after delete")
	}
	if data.DeletedAgain {
		t.Error("caches.delete should return false for non-existent cache")
	}
}

func TestCacheStorage_Keys(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    await caches.open('alpha');
    await caches.open('beta');
    var keys = await caches.keys();
    return Response.json({
      isArray: Array.isArray(keys),
      length: keys.length,
      hasAlpha: keys.includes('alpha'),
      hasBeta: keys.includes('beta'),
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		IsArray  bool `json:"isArray"`
		Length   int  `json:"length"`
		HasAlpha bool `json:"hasAlpha"`
		HasBeta  bool `json:"hasBeta"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if !data.IsArray {
		t.Error("caches.keys() should return an array")
	}
	if data.Length < 2 {
		t.Errorf("caches.keys() length = %d, want >= 2", data.Length)
	}
	if !data.HasAlpha {
		t.Error("caches.keys() should include 'alpha'")
	}
	if !data.HasBeta {
		t.Error("caches.keys() should include 'beta'")
	}
}

func TestCacheStorage_Match(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    var myCache = await caches.open('search-cache');
    var url = 'https://example.com/found';
    await myCache.put(url, new Response('found-data'));

    // caches.match searches across all named caches.
    var result = await caches.match(url);
    var miss = await caches.match('https://example.com/not-found');
    return Response.json({
      found: result !== undefined,
      body: result ? result._body : null,
      miss: miss === undefined,
    });
  },
};`

	r := execJS(t, e, source, cacheEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Found bool    `json:"found"`
		Body  *string `json:"body"`
		Miss  bool    `json:"miss"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if !data.Found {
		t.Error("caches.match should find entry across caches")
	}
	if data.Body == nil || *data.Body != "found-data" {
		t.Errorf("body = %v, want 'found-data'", data.Body)
	}
	if !data.Miss {
		t.Error("caches.match should return undefined for miss")
	}
}

func TestCache_MatchNullRequest(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    var matched = await caches.default.match(null);
    return Response.json({ hit: matched !== undefined });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Hit bool `json:"hit"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Hit {
		t.Error("match(null) should return undefined, not a hit")
	}
}
