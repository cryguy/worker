package webapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cryguy/worker/internal/core"
	"github.com/cryguy/worker/internal/eventloop"
)

// FetchSSRFEnabled controls whether the SSRF-safe dialer is used for fetch.
// Tests set this to false so httptest servers on 127.0.0.1 are reachable.
var FetchSSRFEnabled = true

// ForbiddenFetchHeaders is the blocklist of headers that workers cannot set.
var ForbiddenFetchHeaders = map[string]bool{
	"host":                true,
	"transfer-encoding":   true,
	"connection":          true,
	"keep-alive":          true,
	"upgrade":             true,
	"proxy-authorization": true,
	"proxy-connection":    true,
	"te":                  true,
	"trailer":             true,
	"x-forwarded-for":     true,
	"x-forwarded-host":    true,
	"x-forwarded-proto":   true,
	"x-real-ip":           true,
}

// FetchTransport is the http.RoundTripper used by fetch. Tests can override it.
var FetchTransport http.RoundTripper = &http.Transport{
	DialContext: ssrfSafeDialContext,
}

// fetchJS defines the global fetch() function and resolve/reject handlers.
const fetchJS = `
(function() {
globalThis.__fetchPromises = {};

globalThis.fetch = function(input, init) {
	var reqID = String(globalThis.__requestID || '');
	var url = '', method = 'GET', headers = {}, body = '', bodyIsBase64 = false;
	var redirect = 'follow', signalAborted = false, signal = null;

	function extractBody(b) {
		if (b == null) return;
		if (b instanceof ArrayBuffer || ArrayBuffer.isView(b)) {
			body = __bufferSourceToB64(b);
			bodyIsBase64 = true;
		} else if (b instanceof ReadableStream && b._queue) {
			var chunks = [];
			for (var i = 0; i < b._queue.length; i++) {
				var c = b._queue[i];
				if (typeof c === 'string') {
					var enc = new TextEncoder();
					var bytes = enc.encode(c);
					for (var j = 0; j < bytes.length; j++) chunks.push(bytes[j]);
				} else if (c instanceof Uint8Array || ArrayBuffer.isView(c)) {
					var arr = new Uint8Array(c.buffer || c, c.byteOffset || 0, c.byteLength || c.length);
					for (var j2 = 0; j2 < arr.length; j2++) chunks.push(arr[j2]);
				} else if (c instanceof ArrayBuffer) {
					var arr2 = new Uint8Array(c);
					for (var j3 = 0; j3 < arr2.length; j3++) chunks.push(arr2[j3]);
				} else {
					var s = String(c);
					for (var j4 = 0; j4 < s.length; j4++) chunks.push(s.charCodeAt(j4) & 0xFF);
				}
			}
			b._queue = [];
			if (chunks.length > 0) {
				body = __bufferSourceToB64(new Uint8Array(chunks));
				bodyIsBase64 = true;
			}
		} else {
			body = String(b);
		}
	}

	if (typeof input === 'string') {
		url = input;
	} else if (input instanceof URL) {
		url = input.toString();
	} else if (input && typeof input === 'object') {
		url = input.url || '';
		method = input.method || 'GET';
		if (input.headers) {
			if (input.headers._map) {
				var m = input.headers._map;
				for (var k in m) { if (m.hasOwnProperty(k)) headers[k] = String(m[k]); }
			} else if (typeof input.headers.forEach === 'function') {
				input.headers.forEach(function(v, k) { headers[k] = v; });
			}
		}
		if (input._body != null) extractBody(input._body);
		if (input.redirect !== undefined) redirect = String(input.redirect);
		if (input.signal) { signal = input.signal; if (input.signal.aborted) signalAborted = true; }
	}

	if (init && typeof init === 'object') {
		if (init.method !== undefined) method = String(init.method).toUpperCase();
		if (init.headers) {
			var src;
			if (init.headers instanceof Headers) {
				src = {};
				init.headers.forEach(function(v, k) { src[k] = v; });
			} else if (init.headers._map) {
				src = init.headers._map;
			} else {
				src = init.headers;
			}
			if (typeof src === 'object') {
				for (var k2 in src) { if (src.hasOwnProperty(k2)) headers[k2.toLowerCase()] = String(src[k2]); }
			}
		}
		if (init.body != null) extractBody(init.body);
		if (init.redirect !== undefined) redirect = String(init.redirect);
		if (init.signal) { signal = init.signal; if (init.signal.aborted) signalAborted = true; }
	}

	if (!method) method = 'GET';

	if (signalAborted) {
		return Promise.reject(new DOMException('The operation was aborted.', 'AbortError'));
	}

	var headersJSON = JSON.stringify(headers);
	var argsJSON = JSON.stringify({
		url: url, method: method, headersJSON: headersJSON,
		body: body || '', bodyIsBase64: bodyIsBase64,
		redirect: redirect
	});

	return new Promise(function(resolve, reject) {
		try {
			var fetchID = __fetchStart(reqID, argsJSON);
			globalThis.__fetchPromises[fetchID] = { resolve: resolve, reject: reject };

			if (signal && !signal.aborted) {
				signal.addEventListener('abort', function onAbort() {
					signal.removeEventListener('abort', onAbort);
					__fetchAbort(reqID, fetchID);
					var p = globalThis.__fetchPromises[fetchID];
					if (p) {
						delete globalThis.__fetchPromises[fetchID];
						p.reject(new DOMException('The operation was aborted.', 'AbortError'));
					}
				});
			}
		} catch(e) { reject(e); }
	});
};

globalThis.__fetchResolve = function(fetchID, status, statusText, headersJSON, bodyB64, redirected, finalURL) {
	var p = globalThis.__fetchPromises[fetchID];
	delete globalThis.__fetchPromises[fetchID];
	if (!p) return;
	try {
		var hdrs = JSON.parse(headersJSON);
		var body = null;
		if (bodyB64 && bodyB64.length > 0) {
			var buf = __b64ToBuffer(bodyB64);
			var ct = (hdrs['content-type'] || '').toLowerCase();
			if (ct.indexOf('text/') === 0 || ct.indexOf('application/json') !== -1 ||
			    ct.indexOf('application/xml') !== -1 || ct.indexOf('application/javascript') !== -1 ||
			    ct.indexOf('application/x-www-form-urlencoded') !== -1) {
				body = new TextDecoder().decode(buf);
			} else {
				body = buf;
			}
		}
		var r = new Response(body, {status: status, statusText: statusText, headers: hdrs});
		if (redirected) {
			Object.defineProperty(r, 'redirected', {value: true, writable: false});
		}
		Object.defineProperty(r, 'url', {value: finalURL || '', writable: false});
		p.resolve(r);
	} catch(e) { p.reject(e); }
};

globalThis.__fetchReject = function(fetchID, errMsg) {
	var p = globalThis.__fetchPromises[fetchID];
	delete globalThis.__fetchPromises[fetchID];
	if (p) p.reject(new TypeError(errMsg));
};
})();
`

// SetupFetch registers Go-backed fetch helpers and evaluates the JS polyfill.
func SetupFetch(rt core.JSRuntime, cfg core.EngineConfig, el *eventloop.EventLoop) error {
	timeout := time.Duration(cfg.FetchTimeoutSec) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	maxBytes := int64(cfg.MaxResponseBytes)
	if maxBytes == 0 {
		maxBytes = 10 * 1024 * 1024
	}

	// __fetchStart(reqIDStr, argsJSON) -> fetchID
	if err := rt.RegisterFunc("__fetchStart", func(reqIDStr, argsJSON string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state != nil && state.FetchCount >= state.MaxFetches {
			return "", fmt.Errorf("exceeded maximum fetch requests (%d)", state.MaxFetches)
		}
		if state != nil {
			state.FetchCount++
		}

		var args struct {
			URL          string `json:"url"`
			Method       string `json:"method"`
			HeadersJSON  string `json:"headersJSON"`
			Body         string `json:"body"`
			BodyIsBase64 bool   `json:"bodyIsBase64"`
			Redirect     string `json:"redirect"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("fetch: parsing arguments: %s", err.Error())
		}

		if args.URL == "" {
			return "", fmt.Errorf("fetch requires at least 1 argument")
		}

		if FetchSSRFEnabled && IsPrivateHostname(args.URL) {
			return "", fmt.Errorf("fetch to private IP addresses is not allowed")
		}

		var headers map[string]string
		if args.HeadersJSON != "" && args.HeadersJSON != "{}" {
			if err := json.Unmarshal([]byte(args.HeadersJSON), &headers); err != nil {
				return "", fmt.Errorf("fetch: parsing headers: %s", err.Error())
			}
		}

		var bodyReader io.Reader
		if args.Body != "" {
			if args.BodyIsBase64 {
				decoded, err := base64.StdEncoding.DecodeString(args.Body)
				if err != nil {
					return "", fmt.Errorf("fetch: decoding binary body: %s", err.Error())
				}
				bodyReader = strings.NewReader(string(decoded))
			} else {
				bodyReader = strings.NewReader(args.Body)
			}
		}

		fetchCtx, fetchCancel := context.WithCancel(context.Background())
		fetchID := core.RegisterFetchCancel(reqID, fetchCancel)

		httpReq, err := http.NewRequestWithContext(fetchCtx, args.Method, args.URL, bodyReader)
		if err != nil {
			fetchCancel()
			core.RemoveFetchCancel(reqID, fetchID)
			return "", fmt.Errorf("fetch: %s", err.Error())
		}
		for k, v := range headers {
			if ForbiddenFetchHeaders[strings.ToLower(k)] {
				continue
			}
			httpReq.Header.Set(k, v)
		}

		redirectMode := args.Redirect
		if redirectMode == "" {
			redirectMode = "follow"
		}
		var checkRedirect func(req *http.Request, via []*http.Request) error
		switch redirectMode {
		case "manual":
			checkRedirect = func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			}
		case "error":
			checkRedirect = func(req *http.Request, via []*http.Request) error {
				return fmt.Errorf("fetch failed: redirect mode is 'error'")
			}
		default:
			checkRedirect = func(req *http.Request, via []*http.Request) error {
				if len(via) >= 20 {
					return fmt.Errorf("too many redirects")
				}
				if FetchSSRFEnabled && IsPrivateHostname(req.URL.String()) {
					return fmt.Errorf("redirect to private IP address is not allowed")
				}
				return nil
			}
		}

		client := &http.Client{
			Timeout:       timeout,
			Transport:     FetchTransport,
			CheckRedirect: checkRedirect,
		}

		capturedRedirectMode := redirectMode
		capturedURL := args.URL
		capturedFetchCtx := fetchCtx
		capturedFetchCancel := fetchCancel

		resultCh := make(chan eventloop.FetchResult, 1)
		go func() {
			defer capturedFetchCancel()
			resp, httpErr := client.Do(httpReq)
			if httpErr != nil {
				abortedBySignal := capturedFetchCtx.Err() != nil
				core.RemoveFetchCancel(reqID, fetchID)
				if capturedRedirectMode == "error" {
					resultCh <- eventloop.FetchResult{Err: fmt.Errorf("fetch failed: redirect mode is 'error'")}
					return
				}
				if abortedBySignal {
					resultCh <- eventloop.FetchResult{Err: fmt.Errorf("The operation was aborted.")}
					return
				}
				resultCh <- eventloop.FetchResult{Err: fmt.Errorf("fetch: %s", httpErr.Error())}
				return
			}
			defer func() { _ = resp.Body.Close() }()
			core.RemoveFetchCancel(reqID, fetchID)

			limitedReader := io.LimitReader(resp.Body, maxBytes+1)
			respBody, readErr := io.ReadAll(limitedReader)
			if readErr != nil {
				resultCh <- eventloop.FetchResult{Err: fmt.Errorf("fetch: reading body: %s", readErr.Error())}
				return
			}
			if int64(len(respBody)) > maxBytes {
				respBody = respBody[:maxBytes]
			}

			respHeaders := make(map[string]string)
			for k, vals := range resp.Header {
				respHeaders[strings.ToLower(k)] = strings.Join(vals, ", ")
			}
			hdrsJSON, _ := json.Marshal(respHeaders)

			finalURL := capturedURL
			if resp.Request != nil && resp.Request.URL != nil {
				finalURL = resp.Request.URL.String()
			}
			redirected := finalURL != capturedURL

			resultCh <- eventloop.FetchResult{
				Status:      resp.StatusCode,
				StatusText:  resp.Status,
				HeadersJSON: string(hdrsJSON),
				BodyB64:     base64.StdEncoding.EncodeToString(respBody),
				Redirected:  redirected,
				FinalURL:    finalURL,
			}
		}()

		el.AddPendingFetch(&eventloop.PendingFetch{ResultCh: resultCh, FetchID: fetchID})
		return fetchID, nil
	}); err != nil {
		return err
	}

	// __fetchAbort(reqID, fetchID)
	if err := rt.RegisterFunc("__fetchAbort", func(reqIDStr, fetchID string) {
		reqID := core.ParseReqID(reqIDStr)
		core.CallFetchCancel(reqID, fetchID)
	}); err != nil {
		return err
	}

	return rt.Eval(fetchJS)
}

// --- SSRF Protection ---

// IsPrivateHostname performs a fast, non-resolving pre-check for obviously
// private hostnames and literal IP addresses.
func IsPrivateHostname(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return true
	}
	hostname := u.Hostname()
	if hostname == "" {
		return true
	}
	lower := strings.ToLower(hostname)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return true
	}
	if ip := net.ParseIP(hostname); ip != nil {
		return IsPrivateIP(ip)
	}
	return false
}

// ssrfSafeDialContext resolves DNS and validates the resolved IP against
// private ranges at connect time, preventing DNS rebinding / TOCTOU attacks.
func ssrfSafeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid address %q: %w", addr, err)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("DNS lookup failed for %s: %w", host, err)
	}
	var safeIP net.IPAddr
	found := false
	for _, ip := range ips {
		if !IsPrivateIP(ip.IP) {
			safeIP = ip
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("fetch to private IP addresses is not allowed")
	}
	dialer := &net.Dialer{}
	return dialer.DialContext(ctx, network, net.JoinHostPort(safeIP.IP.String(), port))
}

// privateRanges is parsed once at init time.
var privateRanges []*net.IPNet

func init() {
	for _, cidr := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
		"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
		"192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24",
		"240.0.0.0/4",
		"::1/128", "fc00::/7", "fe80::/10",
	} {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			panic("invalid CIDR: " + cidr)
		}
		privateRanges = append(privateRanges, n)
	}
}

// IsPrivateIP returns true if the IP is in a private, loopback, or link-local range.
func IsPrivateIP(ip net.IP) bool {
	for _, n := range privateRanges {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
