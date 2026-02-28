//go:build !v8

package quickjs

import (
	"encoding/base64"
	"fmt"
	"reflect"
	"unsafe"

	"github.com/cryguy/worker/v2/internal/core"
	"modernc.org/libc"
	lib "modernc.org/libquickjs"
	"modernc.org/quickjs"
)

// qjsRuntime implements core.JSRuntime for the QuickJS engine.
type qjsRuntime struct {
	vm  *quickjs.VM
	tls *libc.TLS // cached from VM internals for direct C API access
	ctx uintptr   // cached JSContext pointer for direct C API access

	// fallback fields: used only when direct C API extraction fails
	// (e.g. if modernc.org/quickjs changes its unexported struct layout).
	useFallback   bool
	pendingBinary []byte // temp: data being written to JS
	pendingResult []byte // temp: data being read from JS
}

// btChunkSize is the raw byte chunk size for the fallback base64 transfer path.
const btChunkSize = 196608 // 192 KB raw → 256 KB base64

var _ core.JSRuntime = (*qjsRuntime)(nil)
var _ core.BinaryTransferer = (*qjsRuntime)(nil)

// Eval evaluates JavaScript and discards the result.
func (r *qjsRuntime) Eval(js string) error {
	v, err := r.vm.EvalValue(js, quickjs.EvalGlobal)
	if err != nil {
		return err
	}
	v.Free()
	return nil
}

// EvalString evaluates JavaScript and returns the result as a Go string.
func (r *qjsRuntime) EvalString(js string) (string, error) {
	result, err := r.vm.Eval(js, quickjs.EvalGlobal)
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", nil
	}
	return fmt.Sprint(result), nil
}

// EvalBool evaluates JavaScript and returns the result as a Go bool.
func (r *qjsRuntime) EvalBool(js string) (bool, error) {
	result, err := r.vm.Eval(js, quickjs.EvalGlobal)
	if err != nil {
		return false, err
	}
	b, ok := result.(bool)
	if !ok {
		return false, fmt.Errorf("expected bool, got %T", result)
	}
	return b, nil
}

// EvalInt evaluates JavaScript and returns the result as a Go int.
func (r *qjsRuntime) EvalInt(js string) (int, error) {
	result, err := r.vm.Eval(js, quickjs.EvalGlobal)
	if err != nil {
		return 0, err
	}
	switch v := result.(type) {
	case int:
		return v, nil
	case float64:
		return int(v), nil
	default:
		return 0, fmt.Errorf("expected int, got %T", result)
	}
}

// RegisterFunc registers a Go function as a global JavaScript function.
// Multi-value Go returns (T, error) are automatically unwrapped: on success
// returns T, on error throws a TypeError. This is necessary because the
// QuickJS Go wrapper returns multi-value results as JS arrays.
func (r *qjsRuntime) RegisterFunc(name string, fn any) error {
	rawName := "__raw_" + name
	if err := r.vm.RegisterFunc(rawName, fn, false); err != nil {
		return err
	}
	wrapJS := fmt.Sprintf(`(function() {
		var raw = globalThis[%q];
		globalThis[%q] = function() {
			var r = raw.apply(this, arguments);
			if (Array.isArray(r)) {
				if (r[1] !== null && r[1] !== undefined) throw new TypeError("calling %s: " + r[1]);
				return r[0];
			}
			return r;
		};
		delete globalThis[%q];
	})()`, rawName, name, name, rawName)
	return r.Eval(wrapJS)
}

// SetGlobal sets a global property on the VM's global object.
func (r *qjsRuntime) SetGlobal(name string, value any) error {
	atom, err := r.vm.NewAtom(name)
	if err != nil {
		return fmt.Errorf("creating atom %q: %w", name, err)
	}
	glob := r.vm.GlobalObject()
	defer glob.Free()
	return glob.SetProperty(atom, value)
}

// RunMicrotasks pumps the QuickJS microtask queue.
func (r *qjsRuntime) RunMicrotasks() {
	executePendingJobs(r.vm)
}

// VM returns the underlying QuickJS VM for engine-specific operations.
func (r *qjsRuntime) VM() *quickjs.VM {
	return r.vm
}

// BinaryMode returns "ab" — QuickJS uses plain ArrayBuffer for binary transfer.
func (r *qjsRuntime) BinaryMode() string { return "ab" }

// initBinaryTransfer extracts the VM's internal tls and cContext pointers
// for direct C API access. If extraction fails (e.g. struct layout changed
// in a new quickjs version), falls back to chunked base64 transfer which
// is slower but doesn't depend on internal layout.
func (r *qjsRuntime) initBinaryTransfer() error {
	if err := r.tryExtractVMInternals(); err != nil {
		r.useFallback = true
		return r.initFallbackTransfer()
	}

	// Smoke-test: try a trivial C API call to verify pointers are valid.
	glob := lib.XJS_GetGlobalObject(r.tls, r.ctx)
	lib.XFreeValue(r.tls, r.ctx, glob)

	return nil
}

// tryExtractVMInternals uses reflect+unsafe to cache the VM's tls and ctx.
func (r *qjsRuntime) tryExtractVMInternals() (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic extracting VM internals: %v", p)
		}
	}()

	vmType := reflect.TypeOf(r.vm).Elem()
	vmPtr := uintptr(unsafe.Pointer(r.vm))

	// cContext is the first field of VM (offset 0).
	r.ctx = *(*uintptr)(unsafe.Pointer(vmPtr))
	if r.ctx == 0 {
		return fmt.Errorf("JSContext is nil")
	}

	// Get runtime pointer via its reflected field offset.
	rtField, ok := vmType.FieldByName("runtime")
	if !ok {
		return fmt.Errorf("quickjs.VM missing 'runtime' field")
	}
	rtPtr := *(*uintptr)(unsafe.Pointer(vmPtr + rtField.Offset))
	if rtPtr == 0 {
		return fmt.Errorf("runtime pointer is nil")
	}

	// tls is the second field in runtime (after cRuntime uintptr).
	r.tls = *(**libc.TLS)(unsafe.Pointer(rtPtr + unsafe.Sizeof(uintptr(0))))
	if r.tls == nil {
		return fmt.Errorf("TLS is nil")
	}

	return nil
}

// WriteBinaryToJS writes Go bytes into a JS ArrayBuffer at the given global
// variable name. Uses the QuickJS C API (JS_NewArrayBufferCopy) for a single
// memcpy — matching V8's SharedArrayBuffer performance. Falls back to chunked
// base64 if the C API pointers could not be extracted.
func (r *qjsRuntime) WriteBinaryToJS(globalName string, data []byte) error {
	if len(data) == 0 {
		return r.Eval(fmt.Sprintf("globalThis[%q] = new ArrayBuffer(0);", globalName))
	}
	if r.useFallback {
		return r.writeBinaryFallback(globalName, data)
	}

	// Create ArrayBuffer with copy of data via C API — single memcpy.
	bufPtr := uintptr(unsafe.Pointer(&data[0]))
	jsVal := lib.XJS_NewArrayBufferCopy(r.tls, r.ctx, bufPtr, lib.Tsize_t(len(data)))

	// Set as globalThis[globalName].
	cName, err := libc.CString(globalName)
	if err != nil {
		lib.XFreeValue(r.tls, r.ctx, jsVal)
		return fmt.Errorf("allocating property name: %w", err)
	}

	glob := lib.XJS_GetGlobalObject(r.tls, r.ctx)
	// JS_SetPropertyStr consumes the val reference — do not free jsVal after.
	ret := lib.XJS_SetPropertyStr(r.tls, r.ctx, glob, cName, jsVal)
	lib.XFreeValue(r.tls, r.ctx, glob)
	libc.Xfree(r.tls, cName)

	if ret < 0 {
		return fmt.Errorf("setting global %q", globalName)
	}
	return nil
}

// ReadBinaryFromJS reads binary data from a JS ArrayBuffer at the given
// global variable name and returns it as Go bytes. Uses the QuickJS C API
// (JS_GetArrayBuffer) for a single memcpy — matching V8's performance.
// Falls back to chunked base64 if the C API pointers could not be extracted.
func (r *qjsRuntime) ReadBinaryFromJS(globalName string) ([]byte, error) {
	if r.useFallback {
		return r.readBinaryFallback(globalName)
	}

	cName, err := libc.CString(globalName)
	if err != nil {
		return nil, fmt.Errorf("allocating property name: %w", err)
	}

	glob := lib.XJS_GetGlobalObject(r.tls, r.ctx)
	jsVal := lib.XJS_GetPropertyStr(r.tls, r.ctx, glob, cName)
	lib.XFreeValue(r.tls, r.ctx, glob)
	libc.Xfree(r.tls, cName)

	// Get ArrayBuffer data pointer and size.
	var size lib.Tsize_t
	dataPtr := lib.XJS_GetArrayBuffer(r.tls, r.ctx, uintptr(unsafe.Pointer(&size)), jsVal)

	if dataPtr == 0 || size == 0 {
		lib.XFreeValue(r.tls, r.ctx, jsVal)
		_ = r.Eval(fmt.Sprintf("delete globalThis[%q];", globalName))
		return nil, nil
	}

	// Copy data to Go bytes — single memcpy.
	result := make([]byte, size)
	copy(result, unsafe.Slice((*byte)(unsafe.Pointer(dataPtr)), size))

	// Clean up: free our reference, then delete the global property.
	lib.XFreeValue(r.tls, r.ctx, jsVal)
	_ = r.Eval(fmt.Sprintf("delete globalThis[%q];", globalName))

	return result, nil
}

// --- Fallback: chunked base64 transfer (used if C API extraction fails) ---

// initFallbackTransfer registers Go callback functions for chunked base64 transfer.
func (r *qjsRuntime) initFallbackTransfer() error {
	if err := r.RegisterFunc("__qjs_bt_chunk", func(offset int) (string, error) {
		if r.pendingBinary == nil {
			return "", fmt.Errorf("no pending binary data")
		}
		end := offset + btChunkSize
		if end > len(r.pendingBinary) {
			end = len(r.pendingBinary)
		}
		return base64.StdEncoding.EncodeToString(r.pendingBinary[offset:end]), nil
	}); err != nil {
		return fmt.Errorf("registering __qjs_bt_chunk: %w", err)
	}

	if err := r.RegisterFunc("__qjs_bt_recv", func(b64 string) (string, error) {
		decoded, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return "", fmt.Errorf("decoding binary chunk: %w", err)
		}
		r.pendingResult = append(r.pendingResult, decoded...)
		return "", nil
	}); err != nil {
		return fmt.Errorf("registering __qjs_bt_recv: %w", err)
	}

	return nil
}

func (r *qjsRuntime) writeBinaryFallback(globalName string, data []byte) error {
	r.pendingBinary = data
	defer func() { r.pendingBinary = nil }()

	return r.Eval(fmt.Sprintf(`(function() {
		var sz = %d;
		var buf = new ArrayBuffer(sz);
		var view = new Uint8Array(buf);
		var off = 0;
		while (off < sz) {
			var b64 = __qjs_bt_chunk(off);
			var raw = atob(b64);
			for (var i = 0; i < raw.length; i++) {
				view[off + i] = raw.charCodeAt(i);
			}
			off += raw.length;
		}
		globalThis[%q] = buf;
	})()`, len(data), globalName))
}

func (r *qjsRuntime) readBinaryFallback(globalName string) ([]byte, error) {
	size, err := r.EvalInt(fmt.Sprintf(
		"(function(){var b=globalThis[%q];return b?b.byteLength:0;})()", globalName))
	if err != nil {
		return nil, fmt.Errorf("reading %s byte length: %w", globalName, err)
	}
	if size == 0 {
		_ = r.Eval(fmt.Sprintf("delete globalThis[%q];", globalName))
		return nil, nil
	}

	r.pendingResult = make([]byte, 0, size)
	defer func() { r.pendingResult = nil }()

	if err := r.Eval(fmt.Sprintf(`(function() {
		var buf = globalThis[%q];
		delete globalThis[%q];
		var view = new Uint8Array(buf);
		var cs = %d;
		for (var off = 0; off < view.length; off += cs) {
			var end = Math.min(off + cs, view.length);
			var chunk = view.subarray(off, end);
			var parts = [];
			for (var i = 0; i < chunk.length; i += 8192) {
				parts.push(String.fromCharCode.apply(null, chunk.subarray(i, Math.min(i + 8192, chunk.length))));
			}
			__qjs_bt_recv(btoa(parts.join('')));
		}
	})()`, globalName, globalName, btChunkSize)); err != nil {
		return nil, fmt.Errorf("reading binary from JS: %w", err)
	}

	return r.pendingResult, nil
}
