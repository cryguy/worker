package wpt

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cryguy/worker/v2/internal/core"
	"github.com/cryguy/worker/v2/internal/webapi"
)

// skipTests lists test files that require infrastructure we don't support
// (e.g. WebIDL harness, service workers).
var skipTests = map[string]string{
	"idlharness.any.js":          "requires WebIDL harness infrastructure",
	"streams/decode-utf8.any.js": "requires SharedArrayBuffer (common/sab.js)",
}

func init() {
	// Allow fetch to 127.0.0.1 so the WPT resource server is reachable.
	webapi.FetchSSRFEnabled = false
	webapi.FetchTransport = http.DefaultTransport
}

// wptSuiteDir is the path to the cloned WPT suite.
// Clone with: git clone --depth 1 https://github.com/web-platform-tests/wpt tests/wpt/suite
var wptSuiteDir string

// runnerDir holds shim.js, report.js, and expectations.
var runnerDir string

// cached JS sources loaded once per test run.
var (
	shimJS        string
	reportJS      string
	testharnessJS string
	loadOnce      sync.Once
	loadErr       error
)

func init() {
	// Resolve paths relative to this test file.
	here, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	// tests/wpt/ is the working directory when running `go test ./tests/wpt/`
	runnerDir = filepath.Join(here, "runner")
	wptSuiteDir = filepath.Join(here, "suite")
}

func loadHarness(t *testing.T) {
	t.Helper()
	loadOnce.Do(func() {
		b, err := os.ReadFile(filepath.Join(runnerDir, "shim.js"))
		if err != nil {
			loadErr = fmt.Errorf("reading shim.js: %w", err)
			return
		}
		shimJS = string(b)

		b, err = os.ReadFile(filepath.Join(runnerDir, "report.js"))
		if err != nil {
			loadErr = fmt.Errorf("reading report.js: %w", err)
			return
		}
		reportJS = string(b)

		b, err = os.ReadFile(filepath.Join(wptSuiteDir, "resources", "testharness.js"))
		if err != nil {
			loadErr = fmt.Errorf("reading testharness.js: %w", err)
			return
		}
		testharnessJS = string(b)
	})
	if loadErr != nil {
		t.Fatal(loadErr)
	}
}

// testCaseResult mirrors the JSON emitted by report.js.
type testCaseResult struct {
	Name    string  `json:"name"`
	Status  int     `json:"status"`
	Message *string `json:"message"`
	Stack   *string `json:"stack"`
}

// harnessStatus mirrors the harness status from report.js.
type harnessStatus struct {
	Status  int     `json:"status"`
	Message *string `json:"message"`
}

// wptStatusName converts status int to human string.
func wptStatusName(s int) string {
	switch s {
	case 0:
		return "PASS"
	case 1:
		return "FAIL"
	case 2:
		return "TIMEOUT"
	case 3:
		return "NOTRUN"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", s)
	}
}

// expectations holds per-test expected failures.
type expectations struct {
	ExpectedFailures []string `json:"expectedFailures"`
}

// loadExpectations reads expectations/<area>.json if it exists.
// Returns a map from test file name to expectations.
func loadExpectations(area string) map[string]*expectations {
	path := filepath.Join(runnerDir, "expectations", area+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	// The JSON format matches Deno's: key is filename, value is true (all pass)
	// or {"expectedFailures": ["test name", ...]}.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil
	}
	result := make(map[string]*expectations)
	for name, v := range raw {
		s := strings.TrimSpace(string(v))
		if s == "true" {
			result[name] = &expectations{}
			continue
		}
		var exp expectations
		if err := json.Unmarshal(v, &exp); err != nil {
			continue
		}
		result[name] = &exp
	}
	return result
}

// metaScriptRe parses // META: script=/path/to/script.js directives.
var metaScriptRe = regexp.MustCompile(`^//\s*META:\s*script=(.+)$`)

// metaTimeoutRe parses // META: timeout=long directives.
var metaTimeoutRe = regexp.MustCompile(`^//\s*META:\s*timeout=long\s*$`)

// parseMeta extracts // META: directives from a test file.
func parseMeta(source string) (scripts []string, long bool) {
	for _, line := range strings.Split(source, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "// META:") && !strings.HasPrefix(line, "//META:") {
			if !strings.HasPrefix(line, "//") && line != "" {
				break // META directives must be at the top
			}
			continue
		}
		if m := metaScriptRe.FindStringSubmatch(line); m != nil {
			scripts = append(scripts, m[1])
		}
		if metaTimeoutRe.MatchString(line) {
			long = true
		}
	}
	return
}

// resolveScript reads a WPT resource script.
// Absolute paths (starting with /) are resolved from wptSuiteDir.
// Relative paths are resolved from the test file's directory.
func resolveScript(path, testFileDir string) (string, error) {
	var clean string
	if strings.HasPrefix(path, "/") {
		clean = filepath.Join(wptSuiteDir, filepath.FromSlash(strings.TrimPrefix(path, "/")))
	} else {
		clean = filepath.Join(testFileDir, filepath.FromSlash(path))
	}
	b, err := os.ReadFile(clean)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", path, err)
	}
	return string(b), nil
}

// runWPTTest executes a single .any.js test file and returns results.
// baseURL, if non-empty, sets globalThis.location.href so relative fetch()
// calls resolve against the WPT resource server.
func runWPTTest(t *testing.T, testPath, baseURL string) ([]testCaseResult, *harnessStatus) {
	t.Helper()
	loadHarness(t)

	// Read the test file.
	testSource, err := os.ReadFile(testPath)
	if err != nil {
		t.Fatalf("reading test file: %v", err)
	}
	source := string(testSource)

	// Parse META directives.
	metaScripts, longTimeout := parseMeta(source)

	// Create a standalone runtime.
	cfg := core.EngineConfig{
		MemoryLimitMB:    256,
		MaxFetchRequests: 200,
		ExecutionTimeout: 30000,
		FetchTimeoutSec:  30,
	}
	rt, el, cleanup, err := newStandaloneRuntime(cfg)
	if err != nil {
		t.Fatalf("creating runtime: %v", err)
	}
	defer cleanup()

	// Set up request state for APIs that need it.
	env := &core.Env{Vars: map[string]string{}}
	reqID := core.NewRequestState(cfg.MaxFetchRequests, env)
	defer core.ClearRequestState(reqID)
	_ = rt.SetGlobal("__requestID", fmt.Sprintf("%d", reqID))

	// Evaluate scripts in order: shim → testharness.js → report.js → META scripts → test
	evalOrFail := func(name, js string) {
		if err := rt.Eval(js); err != nil {
			t.Fatalf("eval %s: %v", name, err)
		}
	}

	evalOrFail("shim.js", shimJS)

	// Override location if a base URL is provided (for relative fetch).
	if baseURL != "" {
		locJS := fmt.Sprintf(`(function(){
			var u = new URL(%q);
			globalThis.location = {
				href: u.href, origin: u.origin, protocol: u.protocol,
				host: u.host, hostname: u.hostname, port: u.port,
				pathname: u.pathname, search: u.search, hash: u.hash
			};
		})()`, baseURL)
		evalOrFail("location-override", locJS)
	}

	evalOrFail("testharness.js", testharnessJS)
	evalOrFail("report.js", reportJS)

	// Load META: script= dependencies.
	testFileDir := filepath.Dir(testPath)
	for _, script := range metaScripts {
		js, err := resolveScript(script, testFileDir)
		if err != nil {
			t.Fatalf("loading META script %s: %v", script, err)
		}
		evalOrFail(script, js)
	}

	// Evaluate the test itself.
	evalOrFail(filepath.Base(testPath), source)

	// Signal that loading is done (triggers ShellTestEnvironment completion).
	_ = rt.Eval("if (typeof done === 'function' && !globalThis.__wpt_done) { /* some tests call done() themselves */ }")

	// Drain microtasks and event loop.
	rt.RunMicrotasks()

	timeout := 10 * time.Second
	if longTimeout {
		timeout = 60 * time.Second
	}
	deadline := time.Now().Add(timeout)

	// Poll until tests complete or timeout.
	for !isWPTDone(rt) && time.Now().Before(deadline) {
		if el.HasPending() {
			el.Drain(rt, time.Now().Add(100*time.Millisecond))
		}
		rt.RunMicrotasks()
		time.Sleep(5 * time.Millisecond)
	}

	// Collect results.
	resultsJSON, err := rt.EvalString("JSON.stringify(globalThis.__wpt_results)")
	if err != nil {
		t.Fatalf("reading results: %v", err)
	}

	var results []testCaseResult
	if err := json.Unmarshal([]byte(resultsJSON), &results); err != nil {
		t.Fatalf("parsing results JSON: %v", err)
	}

	statusJSON, err := rt.EvalString("JSON.stringify(globalThis.__wpt_harness_status)")
	if err != nil {
		t.Fatalf("reading harness status: %v", err)
	}

	var hs harnessStatus
	if statusJSON != "null" && statusJSON != "" {
		if err := json.Unmarshal([]byte(statusJSON), &hs); err != nil {
			t.Fatalf("parsing harness status: %v", err)
		}
	}

	return results, &hs
}

func isWPTDone(rt core.JSRuntime) bool {
	done, err := rt.EvalBool("!!globalThis.__wpt_done")
	if err != nil {
		return false
	}
	return done
}

// RunWPTTestArea runs all .any.js tests in a WPT directory.
func RunWPTTestArea(t *testing.T, area string) {
	t.Helper()

	suiteArea := filepath.Join(wptSuiteDir, area)
	if _, err := os.Stat(suiteArea); os.IsNotExist(err) {
		t.Skipf("WPT suite not found at %s — clone with: git clone --depth 1 https://github.com/web-platform-tests/wpt tests/wpt/suite", suiteArea)
	}

	// Start a static file server so fetch() in tests can load resources.
	serverURL := ensureWPTServer(t)

	exp := loadExpectations(area)

	// Find all .any.js files recursively in this area.
	var testFiles []string
	err := filepath.Walk(suiteArea, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".any.js") {
			testFiles = append(testFiles, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", suiteArea, err)
	}

	for _, testPath := range testFiles {
		// Compute a relative name for the test (e.g. "piping/abort.any.js").
		relPath, _ := filepath.Rel(suiteArea, testPath)
		testName := filepath.ToSlash(relPath)
		baseName := filepath.Base(testPath)

		if reason, ok := skipTests[baseName]; ok {
			t.Run(testName, func(t *testing.T) { t.Skipf("skipped: %s", reason) })
			continue
		}
		if reason, ok := skipTests[testName]; ok {
			t.Run(testName, func(t *testing.T) { t.Skipf("skipped: %s", reason) })
			continue
		}

		// Base URL points to the test's location on the WPT server.
		baseURL := serverURL + "/" + area + "/" + testName

		t.Run(testName, func(t *testing.T) {
			results, hs := runWPTTest(t, testPath, baseURL)

			// Determine expected failures for this test file.
			var expectedFails map[string]bool
			var expectAllFail bool
			if exp != nil {
				if e, ok := exp[testName]; ok {
					expectedFails = make(map[string]bool)
					for _, name := range e.ExpectedFailures {
						if name == "*" {
							expectAllFail = true
						} else {
							expectedFails[name] = true
						}
					}
				}
			}

			if hs.Status != 0 {
				msg := "unknown"
				if hs.Message != nil {
					msg = *hs.Message
				}
				t.Errorf("harness error (status %d): %s", hs.Status, msg)
			}

			passed := 0
			failed := 0
			expectedFailed := 0
			for _, r := range results {
				if r.Status == 0 {
					if expectedFails[r.Name] || expectAllFail {
						t.Logf("UNEXPECTED PASS: %s (was expected to fail)", r.Name)
					}
					passed++
				} else {
					if expectedFails[r.Name] || expectAllFail {
						expectedFailed++
						continue
					}
					failed++
					msg := ""
					if r.Message != nil {
						msg = *r.Message
					}
					t.Errorf("FAIL [%s]: %s — %s", wptStatusName(r.Status), r.Name, msg)
				}
			}

			t.Logf("Results: %d passed, %d failed, %d expected failures, %d total",
				passed, failed, expectedFailed, len(results))
		})
	}
}

// --- WPT Server helpers ---

var (
	wptServerOnce sync.Once
	wptServerURL  string
)

// ensureWPTServer returns a URL for WPT resources. It prefers the full Python
// WPT server (python wpt serve) if running, since it can execute .py handlers
// like inspect-headers.py. Falls back to a minimal Go static file server.
func ensureWPTServer(t *testing.T) string {
	t.Helper()
	wptServerOnce.Do(func() {
		// Prefer the Python WPT server if running.
		resp, err := http.Get("http://web-platform.test:8000/")
		if err == nil {
			resp.Body.Close()
			wptServerURL = "http://web-platform.test:8000"
			return
		}

		// Fall back to a simple Go static file server with .py handler shims.
		mux := http.NewServeMux()
		// Replicate fetch/api/resources/inspect-headers.py: echo request
		// headers back as x-request-<name> response headers.
		mux.HandleFunc("/fetch/api/resources/inspect-headers.py", func(w http.ResponseWriter, r *http.Request) {
			headersParam := r.URL.Query().Get("headers")
			var checkedHeaders []string
			if headersParam != "" {
				checkedHeaders = strings.Split(headersParam, "|")
			}
			for _, h := range checkedHeaders {
				canonKey := http.CanonicalHeaderKey(h)
				if vals, ok := r.Header[canonKey]; ok {
					w.Header().Set("x-request-"+h, strings.Join(vals, ", "))
				}
			}
			if r.URL.Query().Get("cors") != "" {
				origin := r.Header.Get("Origin")
				if origin != "" {
					w.Header().Set("Access-Control-Allow-Origin", origin)
				} else {
					w.Header().Set("Access-Control-Allow-Origin", "*")
				}
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, HEAD")
				var exposed []string
				for _, h2 := range checkedHeaders {
					exposed = append(exposed, "x-request-"+h2)
				}
				w.Header().Set("Access-Control-Expose-Headers", strings.Join(exposed, ", "))
				if ah := r.URL.Query().Get("allow_headers"); ah != "" {
					w.Header().Set("Access-Control-Allow-Headers", ah)
				} else {
					var reqHdrs []string
					for k := range r.Header {
						reqHdrs = append(reqHdrs, k)
					}
					w.Header().Set("Access-Control-Allow-Headers", strings.Join(reqHdrs, ", "))
				}
			}
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(200)
		})
		// Replicate fetch/api/resources/trickle.py: write "TEST_TRICKLE\n"
		// count times with optional delay between chunks.
		mux.HandleFunc("/fetch/api/resources/trickle.py", func(w http.ResponseWriter, r *http.Request) {
			count := 50
			if c := r.URL.Query().Get("count"); c != "" {
				if n, err := strconv.Atoi(c); err == nil {
					count = n
				}
			}
			if r.URL.Query().Get("notype") == "" {
				w.Header().Set("Content-Type", "text/plain")
			}
			w.WriteHeader(200)
			for i := 0; i < count; i++ {
				_, _ = w.Write([]byte("TEST_TRICKLE\n"))
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
		})
		mux.Handle("/", http.FileServer(http.Dir(wptSuiteDir)))
		server := &http.Server{Handler: mux}

		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err == nil {
			wptServerURL = fmt.Sprintf("http://%s", ln.Addr().String())
			go func() { _ = server.Serve(ln) }()
		}
	})
	if wptServerURL == "" {
		t.Skip("could not start WPT resource server")
	}
	return wptServerURL
}

// --- Concrete test functions for each WPT area ---

// passing for both quickjs+v8
func TestWPT_URL(t *testing.T) {
	RunWPTTestArea(t, "url")
}

func TestWPT_Encoding(t *testing.T) {
	RunWPTTestArea(t, "encoding")
}

func TestWPT_Streams(t *testing.T) {
	RunWPTTestArea(t, "streams")
}

func TestWPT_Compression(t *testing.T) {
	RunWPTTestArea(t, "compression")
}

func TestWPT_WebCryptoAPI(t *testing.T) {
	RunWPTTestArea(t, "WebCryptoAPI")
}

func TestWPT_FileAPI(t *testing.T) {
	RunWPTTestArea(t, "FileAPI")
}

func TestWPT_FormData(t *testing.T) {
	RunWPTTestArea(t, "xhr/formdata")
}

func TestWPT_FetchHeaders(t *testing.T) {
	RunWPTTestArea(t, "fetch/api/headers")
}

func TestWPT_FetchRequest(t *testing.T) {
	RunWPTTestArea(t, "fetch/api/request")
}

func TestWPT_FetchResponse(t *testing.T) {
	RunWPTTestArea(t, "fetch/api/response")
}

func TestWPT_FetchBody(t *testing.T) {
	RunWPTTestArea(t, "fetch/api/body")
}

// passing for both quickjs+v8
func TestWPT_Timers(t *testing.T) {
	RunWPTTestArea(t, "html/webappapis/timers")
}

// passing for both quickjs+v8
func TestWPT_Console(t *testing.T) {
	RunWPTTestArea(t, "console")
}
