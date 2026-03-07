//go:build !v8

package quickjs

import (
	"encoding/base64"
	"fmt"
	"reflect"
	"strconv"
	"strings"
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
//
// When the C API is available (r.tls != nil) and the function has string
// parameters, a NUL-safe bridge is used: the JS wrapper stores string args
// in globals, and Go reads them via XJS_ToCStringLen2 which preserves NUL
// bytes. This fixes the issue where modernc.org/quickjs's RegisterFunc
// truncates strings at NUL due to C-string conversion.
func (r *qjsRuntime) RegisterFunc(name string, fn any) error {
	fnType := reflect.TypeOf(fn)

	// Use NUL-safe path if C API is available and function has string params.
	if r.tls != nil && fnType != nil && fnType.Kind() == reflect.Func {
		hasStringParam := false
		for i := 0; i < fnType.NumIn(); i++ {
			if fnType.In(i).Kind() == reflect.String {
				hasStringParam = true
				break
			}
		}
		if hasStringParam {
			return r.registerFuncNulSafe(name, fn)
		}
	}

	return r.registerFuncLegacy(name, fn)
}

// registerFuncLegacy is the original RegisterFunc implementation used when
// no string parameters are involved or the C API is unavailable.
func (r *qjsRuntime) registerFuncLegacy(name string, fn any) error {
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

// registerFuncNulSafe registers a Go function using a bridge that preserves
// NUL bytes in string arguments. The JS wrapper stores string args in
// globalThis.__nsarg_N, then calls a bridge Go function that reads them
// via the C API (XJS_ToCStringLen2).
func (r *qjsRuntime) registerFuncNulSafe(name string, fn any) error {
	fnVal := reflect.ValueOf(fn)
	fnType := fnVal.Type()
	nArgs := fnType.NumIn()

	// Track which params are strings vs other types.
	isStringParam := make([]bool, nArgs)
	for i := 0; i < nArgs; i++ {
		isStringParam[i] = fnType.In(i).Kind() == reflect.String
	}

	// Build the bridge function. It takes an int (non-string arg count signal)
	// and reads string args from globals via C API.
	// We support return signatures: (), (T), (T, error).
	numOut := fnType.NumOut()

	callOriginal := func() ([]reflect.Value, error) {
		goArgs := make([]reflect.Value, nArgs)
		for i := 0; i < nArgs; i++ {
			s, err := r.cReadJSGlobalString(fmt.Sprintf("__nsarg_%d", i))
			if err != nil {
				return nil, fmt.Errorf("reading arg %d: %w", i, err)
			}
			if isStringParam[i] {
				goArgs[i] = reflect.ValueOf(s)
			} else {
				// Parse non-string args from their JS string representation.
				v, err := parseNonStringArg(s, fnType.In(i))
				if err != nil {
					return nil, fmt.Errorf("parsing arg %d: %w", i, err)
				}
				goArgs[i] = v
			}
		}
		return fnVal.Call(goArgs), nil
	}

	// Build bridge function that preserves the original return type.
	// The bridge is registered via registerFuncLegacy which correctly
	// converts Go types (bool, int, string, etc.) to JS types.
	var bridgeFn any
	switch {
	case numOut == 0:
		bridgeFn = func() error {
			_, err := callOriginal()
			return err
		}
	case numOut == 1:
		bridgeFn = nulSafeBridge1(fnType.Out(0), callOriginal)
	case numOut == 2:
		bridgeFn = nulSafeBridge2(fnType.Out(0), callOriginal)
	default:
		return r.registerFuncLegacy(name, fn)
	}

	// Register the bridge function (no string params, so legacy path is fine).
	bridgeName := "__nsb_" + name
	if err := r.registerFuncLegacy(bridgeName, bridgeFn); err != nil {
		return err
	}

	// Build JS wrapper: store all args in globals, call bridge, cleanup.
	var storeArgs strings.Builder
	for i := 0; i < nArgs; i++ {
		fmt.Fprintf(&storeArgs, "\t\t\tglobalThis.__nsarg_%d = arguments[%d];\n", i, i)
	}
	var cleanupArgs strings.Builder
	for i := 0; i < nArgs; i++ {
		fmt.Fprintf(&cleanupArgs, "\t\t\t\tdelete globalThis.__nsarg_%d;\n", i)
	}

	wrapJS := fmt.Sprintf(`(function() {
		var bridge = globalThis[%q];
		globalThis[%q] = function() {
%s			try {
				return bridge();
			} finally {
%s			}
		};
		delete globalThis[%q];
	})()`, bridgeName, name, storeArgs.String(), cleanupArgs.String(), bridgeName)
	return r.Eval(wrapJS)
}

// cReadJSGlobalString reads a JS global variable as a Go string using the
// QuickJS C API (XJS_ToCStringLen2), which preserves NUL bytes.
func (r *qjsRuntime) cReadJSGlobalString(globalName string) (string, error) {
	cName, err := libc.CString(globalName)
	if err != nil {
		return "", fmt.Errorf("allocating property name: %w", err)
	}

	glob := lib.XJS_GetGlobalObject(r.tls, r.ctx)
	jsVal := lib.XJS_GetPropertyStr(r.tls, r.ctx, glob, cName)
	lib.XFreeValue(r.tls, r.ctx, glob)
	libc.Xfree(r.tls, cName)

	// Read the string with length — NUL-safe.
	var strLen lib.Tsize_t
	cStr := lib.XJS_ToCStringLen2(r.tls, r.ctx, uintptr(unsafe.Pointer(&strLen)), jsVal, 0)
	lib.XFreeValue(r.tls, r.ctx, jsVal)

	if cStr == 0 {
		return "", nil
	}

	// Build Go string from pointer + length (preserves NUL bytes).
	raw := unsafe.Slice((*byte)(unsafe.Pointer(cStr)), strLen)

	// QuickJS encodes lone surrogates as CESU-8 (ED [A0-BF] [80-BF]),
	// which is invalid UTF-8. Replace each lone surrogate with U+FFFD
	// so Go receives valid UTF-8.
	result := sanitizeCESU8(raw)
	lib.XJS_FreeCString(r.tls, r.ctx, cStr)

	return result, nil
}

// sanitizeCESU8 replaces CESU-8 encoded lone surrogates (U+D800-U+DFFF)
// with U+FFFD. These appear as 3-byte sequences: ED [A0-BF] [80-BF].
// QuickJS already converts proper surrogate pairs to 4-byte UTF-8, so any
// remaining CESU-8 surrogate sequences are lone surrogates.
func sanitizeCESU8(b []byte) string {
	// Quick check: if no 0xED byte, no surrogates possible.
	hasSurrogate := false
	for _, v := range b {
		if v == 0xED {
			hasSurrogate = true
			break
		}
	}
	if !hasSurrogate {
		return string(b)
	}

	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		if b[i] == 0xED && i+2 < len(b) && b[i+1] >= 0xA0 && b[i+1] <= 0xBF && b[i+2] >= 0x80 && b[i+2] <= 0xBF {
			// CESU-8 lone surrogate → U+FFFD (EF BF BD)
			out = append(out, 0xEF, 0xBF, 0xBD)
			i += 3
		} else {
			out = append(out, b[i])
			i++
		}
	}
	return string(out)
}

type callOriginalFn = func() ([]reflect.Value, error)

// nulSafeBridge1 creates a bridge function for single-return-value functions,
// preserving the original return type so QuickJS converts it correctly.
func nulSafeBridge1(retType reflect.Type, callOrig callOriginalFn) any {
	switch retType.Kind() {
	case reflect.Bool:
		return func() (bool, error) {
			results, err := callOrig()
			if err != nil {
				return false, err
			}
			return results[0].Bool(), nil
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return func() (int, error) {
			results, err := callOrig()
			if err != nil {
				return 0, err
			}
			return int(results[0].Int()), nil
		}
	case reflect.Float32, reflect.Float64:
		return func() (float64, error) {
			results, err := callOrig()
			if err != nil {
				return 0, err
			}
			return results[0].Float(), nil
		}
	default: // string and others
		return func() (string, error) {
			results, err := callOrig()
			if err != nil {
				return "", err
			}
			return fmt.Sprint(results[0].Interface()), nil
		}
	}
}

// nulSafeBridge2 creates a bridge function for (T, error) return signatures.
func nulSafeBridge2(retType reflect.Type, callOrig callOriginalFn) any {
	extractErr := func(results []reflect.Value) error {
		errVal := results[1]
		if !errVal.IsNil() {
			return errVal.Interface().(error)
		}
		return nil
	}
	switch retType.Kind() {
	case reflect.Bool:
		return func() (bool, error) {
			results, err := callOrig()
			if err != nil {
				return false, err
			}
			if e := extractErr(results); e != nil {
				return false, e
			}
			return results[0].Bool(), nil
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return func() (int, error) {
			results, err := callOrig()
			if err != nil {
				return 0, err
			}
			if e := extractErr(results); e != nil {
				return 0, e
			}
			return int(results[0].Int()), nil
		}
	case reflect.Float32, reflect.Float64:
		return func() (float64, error) {
			results, err := callOrig()
			if err != nil {
				return 0, err
			}
			if e := extractErr(results); e != nil {
				return 0, e
			}
			return results[0].Float(), nil
		}
	default: // string and others
		return func() (string, error) {
			results, err := callOrig()
			if err != nil {
				return "", err
			}
			if e := extractErr(results); e != nil {
				return "", e
			}
			return fmt.Sprint(results[0].Interface()), nil
		}
	}
}

// parseNonStringArg converts a JS string representation to the expected Go type.
func parseNonStringArg(s string, t reflect.Type) (reflect.Value, error) {
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			// Try parsing as float first (JS numbers are floats)
			f, err2 := strconv.ParseFloat(s, 64)
			if err2 != nil {
				return reflect.Zero(t), err
			}
			n = int64(f)
		}
		return reflect.ValueOf(n).Convert(t), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			f, err2 := strconv.ParseFloat(s, 64)
			if err2 != nil {
				return reflect.Zero(t), err
			}
			n = uint64(f)
		}
		return reflect.ValueOf(n).Convert(t), nil
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return reflect.Zero(t), err
		}
		return reflect.ValueOf(f).Convert(t), nil
	case reflect.Bool:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return reflect.Zero(t), err
		}
		return reflect.ValueOf(b), nil
	default:
		return reflect.Zero(t), nil
	}
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
