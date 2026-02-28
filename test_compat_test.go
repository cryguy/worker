package worker

// test_compat_test.go provides thin compatibility shims for test files that
// reference unexported symbols that moved into internal subpackages during
// the refactor to internal/*.

import (
	"context"
	"crypto"
	"crypto/elliptic"
	"fmt"
	"hash"
	"mime"
	"net"
	"path/filepath"
	"strings"

	gohtml "golang.org/x/net/html"

	"github.com/cryguy/worker/v2/internal/core"
	"github.com/cryguy/worker/v2/internal/webapi"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

// maxDecompressedSize mirrors the constant in internal/webapi/compression.go.
const maxDecompressedSize = 128 * 1024 * 1024 // 128 MB

// maxLogEntries mirrors the constant in internal/core/reqstate.go.
const maxLogEntries = 1000

// maxLogMessageSize mirrors the constant in internal/core/reqstate.go.
const maxLogMessageSize = 4096

// maxKVValueSize mirrors core.MaxKVValueSize for tests.
const maxKVValueSize = core.MaxKVValueSize

// maxPolyfillDownloadSize mirrors webapi.MaxPolyfillDownloadSize for tests.
const maxPolyfillDownloadSize = webapi.MaxPolyfillDownloadSize

// ---------------------------------------------------------------------------
// Variables
// ---------------------------------------------------------------------------

// forbiddenFetchHeaders proxies webapi.ForbiddenFetchHeaders.
var forbiddenFetchHeaders = webapi.ForbiddenFetchHeaders

// polyfillHashes is the same map as webapi.PolyfillHashes so tests can mutate it.
var polyfillHashes = webapi.PolyfillHashes

// Combinator constant proxies.
var (
	combinatorChild           = webapi.CombinatorChild
	combinatorDescendant      = webapi.CombinatorDescendant
	combinatorAdjacentSibling = webapi.CombinatorAdjacentSibling
	combinatorGeneralSibling  = webapi.CombinatorGeneralSibling
)

// ---------------------------------------------------------------------------
// Type aliases / shim types
// ---------------------------------------------------------------------------

// D1Bridge type alias for tests.
type D1Bridge = webapi.D1Bridge

// cryptoKeyEntry is a test-local struct with lowercase field names.
// Tests access entry.data, entry.hashAlgo, entry.algoName etc.
type cryptoKeyEntry struct {
	data        []byte
	hashAlgo    string
	algoName    string
	keyType     string
	namedCurve  string
	ecKey       any
	extractable bool
}

// toCoreKeyEntry converts a local cryptoKeyEntry to core.CryptoKeyEntry.
func toCoreKeyEntry(e *cryptoKeyEntry) *core.CryptoKeyEntry {
	if e == nil {
		return nil
	}
	return &core.CryptoKeyEntry{
		Data:        e.data,
		HashAlgo:    e.hashAlgo,
		AlgoName:    e.algoName,
		KeyType:     e.keyType,
		NamedCurve:  e.namedCurve,
		EcKey:       e.ecKey,
		Extractable: e.extractable,
	}
}

// fromCoreKeyEntry converts a core.CryptoKeyEntry to a local cryptoKeyEntry.
func fromCoreKeyEntry(e *core.CryptoKeyEntry) *cryptoKeyEntry {
	if e == nil {
		return nil
	}
	return &cryptoKeyEntry{
		data:        e.Data,
		hashAlgo:    e.HashAlgo,
		algoName:    e.AlgoName,
		keyType:     e.KeyType,
		namedCurve:  e.NamedCurve,
		ecKey:       e.EcKey,
		extractable: e.Extractable,
	}
}

// requestState is a test-local struct with lowercase field names mirroring
// the fields of core.RequestState that tests access.
type requestState struct {
	logs         []core.LogEntry
	maxFetches   int
	env          *core.Env
	fetchCancels map[string]context.CancelFunc
}

// fromCoreRequestState converts a *core.RequestState to a local *requestState.
func fromCoreRequestState(s *core.RequestState) *requestState {
	if s == nil {
		return nil
	}
	return &requestState{
		logs:         s.Logs,
		maxFetches:   s.MaxFetches,
		env:          s.Env,
		fetchCancels: s.FetchCancels,
	}
}

// attrMatcherShim mirrors webapi.AttrMatcher with the same field names.
// Tests access .Name, .Op, .Value on elements of sel.Attributes.
type attrMatcherShim struct {
	Name  string
	Op    string
	Value string
}

// cssSelector is a test-local struct with a matches method.
// Tests call sel.matches(tagName, attrs) and access sel.Tag, sel.ID, sel.Classes, sel.Attributes.
type cssSelector struct {
	inner      *webapi.CSSSelector
	Tag        string
	ID         string
	Classes    []string
	Attributes []attrMatcherShim
}

// matches calls the underlying webapi.CSSSelector.Matches.
func (sel *cssSelector) matches(tagName string, attrs map[string]string) bool {
	return sel.inner.Matches(tagName, attrs)
}

// fromWebAPISelector wraps a webapi.CSSSelector into the local cssSelector shim.
func fromWebAPISelector(s *webapi.CSSSelector) *cssSelector {
	if s == nil {
		return nil
	}
	attrs := make([]attrMatcherShim, len(s.Attributes))
	for i, a := range s.Attributes {
		attrs[i] = attrMatcherShim{Name: a.Name, Op: a.Op, Value: a.Value}
	}
	return &cssSelector{
		inner:      s,
		Tag:        s.Tag,
		ID:         s.ID,
		Classes:    s.Classes,
		Attributes: attrs,
	}
}

// elementInfo is a test-local struct with lowercase field names.
// Tests construct literals like: elementInfo{tagName: "div", attrs: nil, depth: 1}
type elementInfo struct {
	tagName string
	attrs   map[string]string
	depth   int
}

// toWebAPIElementInfo converts a slice of local elementInfo to []webapi.ElementInfo.
func toWebAPIElementInfo(in []elementInfo) []webapi.ElementInfo {
	if in == nil {
		return nil
	}
	out := make([]webapi.ElementInfo, len(in))
	for i, e := range in {
		out[i] = webapi.ElementInfo{TagName: e.tagName, Attrs: e.attrs, Depth: e.depth}
	}
	return out
}

// selectorPartShim mirrors webapi.SelectorPart with lowercase field names.
// Tests access cs.parts[i].sel and cs.parts[i].combinator.
type selectorPartShim struct {
	sel        *cssSelector
	combinator webapi.CombinatorType
}

// compoundSelectorShim wraps *webapi.CompoundSelector and exposes
// lowercase methods and fields for tests.
type compoundSelectorShim struct {
	inner *webapi.CompoundSelector
	parts []selectorPartShim
}

func (cs *compoundSelectorShim) matchesWithContext(tagName string, attrs map[string]string, ancestors []elementInfo, siblings []elementInfo) bool {
	return cs.inner.MatchesWithContext(tagName, attrs, toWebAPIElementInfo(ancestors), toWebAPIElementInfo(siblings))
}

func (cs *compoundSelectorShim) isSimple() bool { return cs.inner.IsSimple() }

func (cs *compoundSelectorShim) subject() *cssSelector {
	return fromWebAPISelector(cs.inner.Subject())
}

// matchedElement is a test-local struct with lowercase field names that mirrors
// webapi.MatchedElement. Tests construct literals like &matchedElement{handlerIdx: 0, ...}
type matchedElement struct {
	handlerIdx  int
	depth       int
	skipContent bool
}

// ---------------------------------------------------------------------------
// Constructor / factory shims
// ---------------------------------------------------------------------------

// newRequestState wraps core.NewRequestState, returning a local uint64 ID.
func newRequestState(maxFetches int, env *core.Env) uint64 {
	return core.NewRequestState(maxFetches, env)
}

// clearRequestState wraps core.ClearRequestState, returning a local *requestState.
func clearRequestState(id uint64) *requestState {
	return fromCoreRequestState(core.ClearRequestState(id))
}

// getRequestState wraps core.GetRequestState, returning a local *requestState.
func getRequestState(id uint64) *requestState {
	return fromCoreRequestState(core.GetRequestState(id))
}

// addLog wraps core.AddLog.
func addLog(id uint64, level, message string) { core.AddLog(id, level, message) }

// getCryptoKey wraps core.GetCryptoKey, returning a local *cryptoKeyEntry.
func getCryptoKey(reqID uint64, keyID int) *cryptoKeyEntry {
	return fromCoreKeyEntry(core.GetCryptoKey(reqID, keyID))
}

// importCryptoKey wraps core.ImportCryptoKey.
func importCryptoKey(reqID uint64, hashAlgo string, data []byte) int {
	return core.ImportCryptoKey(reqID, hashAlgo, data)
}

// importCryptoKeyFull wraps core.ImportCryptoKeyFull, accepting a local *cryptoKeyEntry.
func importCryptoKeyFull(reqID uint64, entry *cryptoKeyEntry) int {
	return core.ImportCryptoKeyFull(reqID, toCoreKeyEntry(entry))
}

// registerFetchCancel wraps core.RegisterFetchCancel.
func registerFetchCancel(reqID uint64, cancel context.CancelFunc) string {
	return core.RegisterFetchCancel(reqID, cancel)
}

// removeFetchCancel wraps core.RemoveFetchCancel.
func removeFetchCancel(reqID uint64, fetchID string) context.CancelFunc {
	return core.RemoveFetchCancel(reqID, fetchID)
}

// callFetchCancel wraps core.CallFetchCancel.
func callFetchCancel(reqID uint64, fetchID string) { core.CallFetchCancel(reqID, fetchID) }

// ---------------------------------------------------------------------------
// KV shims
// ---------------------------------------------------------------------------

// encodeCursor wraps core.EncodeCursor for tests.
func encodeCursor(offset int) string { return core.EncodeCursor(offset) }

// decodeCursor wraps core.DecodeCursor for tests.
func decodeCursor(cursor string) int { return core.DecodeCursor(cursor) }

// ---------------------------------------------------------------------------
// Webapi: D1 shims
// ---------------------------------------------------------------------------

// NewD1BridgeMemory wraps webapi.NewD1BridgeMemory for tests.
func NewD1BridgeMemory(databaseID string) (*webapi.D1Bridge, error) {
	return webapi.NewD1BridgeMemory(databaseID)
}

// OpenD1Database wraps webapi.OpenD1Database for tests.
func OpenD1Database(dataDir, databaseID string) (*webapi.D1Bridge, error) {
	return webapi.OpenD1Database(dataDir, databaseID)
}

// ValidateDatabaseID wraps webapi.ValidateDatabaseID for tests.
func ValidateDatabaseID(id string) error {
	return webapi.ValidateDatabaseID(id)
}

// ---------------------------------------------------------------------------
// Webapi: fetch / network shims
// ---------------------------------------------------------------------------

// isPrivateIP wraps webapi.IsPrivateIP for tests.
func isPrivateIP(ip net.IP) bool { return webapi.IsPrivateIP(ip) }

// isPrivateHostname wraps webapi.IsPrivateHostname for tests.
func isPrivateHostname(rawURL string) bool { return webapi.IsPrivateHostname(rawURL) }

// ---------------------------------------------------------------------------
// Webapi: HTML rewriter shims
// ---------------------------------------------------------------------------

// voidElement wraps webapi.VoidElement for tests.
func voidElement(tag string) bool { return webapi.VoidElement(tag) }

// htmlAttrsToMap wraps webapi.HtmlAttrsToMap for tests.
func htmlAttrsToMap(attrs []gohtml.Attribute) map[string]string {
	return webapi.HtmlAttrsToMap(attrs)
}

// shouldSkipContent converts the test-local stack to webapi.MatchedElement and delegates.
func shouldSkipContent(stack []*matchedElement, depth int) bool {
	converted := make([]*webapi.MatchedElement, len(stack))
	for i, m := range stack {
		converted[i] = &webapi.MatchedElement{
			HandlerIdx:  m.handlerIdx,
			Depth:       m.depth,
			SkipContent: m.skipContent,
		}
	}
	return webapi.ShouldSkipContent(converted, depth)
}

// ---------------------------------------------------------------------------
// Webapi: crypto shims
// ---------------------------------------------------------------------------

// aesKeyWrap wraps webapi.AesKeyWrap for tests.
func aesKeyWrap(kek, plaintext []byte) ([]byte, error) { return webapi.AesKeyWrap(kek, plaintext) }

// aesKeyUnwrap wraps webapi.AesKeyUnwrap for tests.
func aesKeyUnwrap(kek, ciphertext []byte) ([]byte, error) {
	return webapi.AesKeyUnwrap(kek, ciphertext)
}

func normalizeAlgo(name string) string              { return webapi.NormalizeAlgo(name) }
func hashFuncFromAlgo(algo string) func() hash.Hash { return webapi.HashFuncFromAlgo(algo) }
func cryptoHashFromAlgo(algo string) crypto.Hash    { return webapi.CryptoHashFromAlgo(algo) }
func rsaJWKAlg(algoName, hashAlgo string) string    { return webapi.RsaJWKAlg(algoName, hashAlgo) }
func padBytes(b []byte, length int) []byte           { return webapi.PadBytes(b, length) }
func curveFromName(name string) elliptic.Curve       { return webapi.CurveFromName(name) }

// ---------------------------------------------------------------------------
// Webapi: selector shims
// ---------------------------------------------------------------------------

// parseSelector wraps webapi.ParseSelector returning a local *cssSelector shim.
func parseSelector(s string) *cssSelector { return fromWebAPISelector(webapi.ParseSelector(s)) }

// parseCompoundSelector wraps webapi.ParseCompoundSelector returning a shim with
// lowercase methods and fields.
func parseCompoundSelector(s string) *compoundSelectorShim {
	inner := webapi.ParseCompoundSelector(s)
	parts := make([]selectorPartShim, len(inner.Parts))
	for i, p := range inner.Parts {
		parts[i] = selectorPartShim{
			sel:        fromWebAPISelector(p.Sel),
			combinator: p.Combinator,
		}
	}
	return &compoundSelectorShim{inner: inner, parts: parts}
}

// parseURL wraps webapi.ParseURL for tests.
func parseURL(rawURL, base string) (*webapi.URLParsed, error) {
	return webapi.ParseURL(rawURL, base)
}

// ---------------------------------------------------------------------------
// Webapi: polyfill shims
// ---------------------------------------------------------------------------

// downloadAndExtract wraps webapi.DownloadAndExtract for tests.
func downloadAndExtract(url, destDir string) error { return webapi.DownloadAndExtract(url, destDir) }

// EnsureUnenv wraps webapi.EnsureUnenv for tests.
func EnsureUnenv(dataDir string) (string, error) { return webapi.EnsureUnenv(dataDir) }

// ---------------------------------------------------------------------------
// Misc helpers
// ---------------------------------------------------------------------------

// errMissingArg returns a formatted error for missing arguments.
func errMissingArg(name string, n int) error {
	return fmt.Errorf("%s requires at least %d argument(s)", name, n)
}

// errInvalidArg returns a formatted error for invalid arguments.
func errInvalidArg(name, reason string) error {
	return fmt.Errorf("%s: %s", name, reason)
}

// contentType returns the MIME type for a file path based on its extension.
// This mirrors the function that was in the old flat assets.go.
func contentType(path string) string {
	ext := filepath.Ext(path)
	if ext == "" {
		return "application/octet-stream"
	}
	ct := mime.TypeByExtension(strings.ToLower(ext))
	if ct == "" {
		return "application/octet-stream"
	}
	return ct
}
