// WPT environment shim for worker-combo's shell runtime.
// Sets up globals that testharness.js expects in a "shell" or "worker" context.

// testharness.js checks for 'document' to decide WindowTestEnvironment.
// We intentionally do NOT set document so it falls through to ShellTestEnvironment.

// Basic globals testharness.js and tests reference.
if (typeof globalThis.self === 'undefined') globalThis.self = globalThis;
if (typeof globalThis.window === 'undefined') globalThis.window = globalThis;

// location stub — used by subset-tests-by-key.js and some tests.
if (typeof globalThis.location === 'undefined') {
  globalThis.location = {
    href: 'http://localhost:8000/',
    origin: 'http://localhost:8000',
    protocol: 'http:',
    host: 'localhost:8000',
    hostname: 'localhost',
    port: '8000',
    pathname: '/',
    search: '',
    hash: ''
  };
}

// GLOBAL scope descriptor — .any.js tests use self.GLOBAL.isWindow() etc.
if (typeof globalThis.GLOBAL === 'undefined') {
  globalThis.GLOBAL = {
    isWindow: function() { return false; },
    isWorker: function() { return true; },
    isShadowRealm: function() { return false; },
    isSharedWorker: function() { return false; },
    isServiceWorker: function() { return false; },
    isDedicatedWorker: function() { return true; },
  };
}

// Wrap fetch() to resolve relative URLs against location.href.
// The runtime's native fetch doesn't handle relative URLs.
(function() {
  var _origFetch = globalThis.fetch;
  if (typeof _origFetch !== 'function') return;
  globalThis.fetch = function(input, init) {
    if (typeof input === 'string' && !/^[a-zA-Z][a-zA-Z0-9+\-.]*:/.test(input)) {
      // Relative URL — resolve against location.href.
      input = new URL(input, globalThis.location.href).href;
    }
    return _origFetch.call(globalThis, input, init);
  };
})();

// addEventListener stub (testharness.js uses it for error handling).
if (typeof globalThis.addEventListener === 'undefined') {
  var __listeners = {};
  globalThis.addEventListener = function(type, fn) {
    if (!__listeners[type]) __listeners[type] = [];
    __listeners[type].push(fn);
  };
  globalThis.removeEventListener = function(type, fn) {
    if (!__listeners[type]) return;
    __listeners[type] = __listeners[type].filter(function(f) { return f !== fn; });
  };
  globalThis.dispatchEvent = function(evt) {
    var fns = __listeners[evt.type || evt] || [];
    for (var i = 0; i < fns.length; i++) fns[i](evt);
  };
}
