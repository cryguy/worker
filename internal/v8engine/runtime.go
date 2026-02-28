//go:build v8

package v8engine

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"

	"github.com/cryguy/worker/v2/internal/core"
	v8 "github.com/tommie/v8go"
)

// v8Runtime implements core.JSRuntime for the V8 engine.
type v8Runtime struct {
	iso *v8.Isolate
	ctx *v8.Context
}

var _ core.JSRuntime = (*v8Runtime)(nil)
var _ core.BinaryTransferer = (*v8Runtime)(nil)

// Eval evaluates JavaScript and discards the result.
func (r *v8Runtime) Eval(js string) error {
	_, err := r.ctx.RunScript(js, "eval.js")
	return err
}

// EvalString evaluates JavaScript and returns the result as a Go string.
func (r *v8Runtime) EvalString(js string) (string, error) {
	val, err := r.ctx.RunScript(js, "eval_string.js")
	if err != nil {
		return "", err
	}
	if val == nil {
		return "", nil
	}
	return val.String(), nil
}

// EvalBool evaluates JavaScript and returns the result as a Go bool.
func (r *v8Runtime) EvalBool(js string) (bool, error) {
	val, err := r.ctx.RunScript(js, "eval_bool.js")
	if err != nil {
		return false, err
	}
	if val == nil {
		return false, nil
	}
	return val.Boolean(), nil
}

// EvalInt evaluates JavaScript and returns the result as a Go int.
func (r *v8Runtime) EvalInt(js string) (int, error) {
	val, err := r.ctx.RunScript(js, "eval_int.js")
	if err != nil {
		return 0, err
	}
	if val == nil {
		return 0, nil
	}
	return int(val.Integer()), nil
}

// RegisterFunc registers a Go function as a global JavaScript function.
// Uses reflection to inspect the Go function's signature and creates a
// V8 FunctionTemplate that marshals arguments and return values.
//
// Supported Go function signatures:
//   - func(args...) — no return, JS function returns undefined
//   - func(args...) T — single return, JS function returns T
//   - func(args...) (T, error) — on success returns T, on error throws TypeError
//
// Supported argument types: string, int, float64, bool
// Supported return types: string, int, float64, bool
func (r *v8Runtime) RegisterFunc(name string, fn any) error {
	fnVal := reflect.ValueOf(fn)
	fnType := fnVal.Type()

	if fnType.Kind() != reflect.Func {
		return fmt.Errorf("RegisterFunc: expected function, got %T", fn)
	}

	tmpl := v8.NewFunctionTemplate(r.iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
		args := info.Args()

		// Validate argument count: throw TypeError if fewer args than required.
		if len(args) < fnType.NumIn() {
			msg := fmt.Sprintf("%s requires at least %d argument(s), got %d", name, fnType.NumIn(), len(args))
			jsMsg, _ := v8.NewValue(r.iso, msg)
			r.iso.ThrowException(jsMsg)
			return nil
		}

		// Build Go arguments from JS values.
		goArgs := make([]reflect.Value, fnType.NumIn())
		for i := 0; i < fnType.NumIn(); i++ {
			goArgs[i] = jsToGoArg(args[i], fnType.In(i))
		}

		results := fnVal.Call(goArgs)

		// Handle return values.
		switch fnType.NumOut() {
		case 0:
			return nil
		case 1:
			return goToJSValue(r.iso, results[0])
		case 2:
			// (T, error) pattern: throw on error, return T on success.
			errVal := results[1]
			if !errVal.IsNil() {
				errMsg := errVal.Interface().(error).Error()
				msg := fmt.Sprintf("calling %s: %s", name, errMsg)
				jsMsg, _ := v8.NewValue(r.iso, msg)
				r.iso.ThrowException(jsMsg)
				return nil
			}
			return goToJSValue(r.iso, results[0])
		default:
			return nil
		}
	})

	fnObj := tmpl.GetFunction(r.ctx)

	return r.ctx.Global().Set(name, fnObj)
}

// SetGlobal sets a global variable on the JS context.
func (r *v8Runtime) SetGlobal(name string, value any) error {
	jsVal, err := goAnyToJSValue(r.iso, r.ctx, value)
	if err != nil {
		return fmt.Errorf("converting value for %q: %w", name, err)
	}
	return r.ctx.Global().Set(name, jsVal)
}

// RunMicrotasks pumps the V8 microtask queue.
func (r *v8Runtime) RunMicrotasks() {
	r.ctx.PerformMicrotaskCheckpoint()
}

// BinaryMode returns "sab" — V8 uses SharedArrayBuffer for binary transfer.
func (r *v8Runtime) BinaryMode() string { return "sab" }

// ReadBinaryFromJS reads a SharedArrayBuffer from a JS global and returns its contents as Go bytes.
func (r *v8Runtime) ReadBinaryFromJS(globalName string) ([]byte, error) {
	sabVal, err := r.ctx.Global().Get(globalName)
	if err != nil {
		return nil, fmt.Errorf("retrieving %s: %w", globalName, err)
	}

	data, release, err := sabVal.SharedArrayBufferGetContents()
	if err != nil {
		return nil, fmt.Errorf("reading SharedArrayBuffer %s: %w", globalName, err)
	}
	result := make([]byte, len(data))
	copy(result, data)
	release()

	// Clean up the global.
	_, _ = r.ctx.RunScript(fmt.Sprintf("delete globalThis[%q];", globalName), "sab_read_cleanup.js")

	return result, nil
}

// WriteBinaryToJS writes Go bytes into a JS ArrayBuffer via SharedArrayBuffer bridge.
func (r *v8Runtime) WriteBinaryToJS(globalName string, data []byte) error {
	// Allocate a SharedArrayBuffer in JS.
	allocScript := fmt.Sprintf("globalThis.__tmp_write_sab = new SharedArrayBuffer(%d);", len(data))
	if _, err := r.ctx.RunScript(allocScript, "sab_alloc.js"); err != nil {
		return fmt.Errorf("allocating SharedArrayBuffer: %w", err)
	}

	if len(data) > 0 {
		sabVal, err := r.ctx.Global().Get("__tmp_write_sab")
		if err != nil {
			_, _ = r.ctx.RunScript("delete globalThis.__tmp_write_sab;", "sab_cleanup.js")
			return fmt.Errorf("retrieving SharedArrayBuffer: %w", err)
		}

		sabBytes, release, err := sabVal.SharedArrayBufferGetContents()
		if err != nil {
			_, _ = r.ctx.RunScript("delete globalThis.__tmp_write_sab;", "sab_cleanup.js")
			return fmt.Errorf("getting SharedArrayBuffer contents: %w", err)
		}
		copy(sabBytes, data)
		release()
	}

	// Copy SAB to regular ArrayBuffer and store at globalName.
	copyScript := fmt.Sprintf(`(function() {
		var sab = globalThis.__tmp_write_sab;
		delete globalThis.__tmp_write_sab;
		var buf = new ArrayBuffer(sab.byteLength);
		new Uint8Array(buf).set(new Uint8Array(sab));
		globalThis[%q] = buf;
	})()`, globalName)
	if _, err := r.ctx.RunScript(copyScript, "sab_copy.js"); err != nil {
		return fmt.Errorf("copying SharedArrayBuffer to ArrayBuffer: %w", err)
	}

	return nil
}

// Iso returns the underlying V8 isolate for engine-specific operations.
func (r *v8Runtime) Iso() *v8.Isolate {
	return r.iso
}

// Ctx returns the underlying V8 context for engine-specific operations.
func (r *v8Runtime) Ctx() *v8.Context {
	return r.ctx
}

// jsToGoArg converts a V8 value to a Go reflect.Value of the expected type.
func jsToGoArg(val *v8.Value, targetType reflect.Type) reflect.Value {
	switch targetType.Kind() {
	case reflect.String:
		return reflect.ValueOf(val.String())
	case reflect.Int:
		return reflect.ValueOf(int(val.Integer()))
	case reflect.Int64:
		return reflect.ValueOf(val.Integer())
	case reflect.Float64:
		return reflect.ValueOf(val.Number())
	case reflect.Bool:
		return reflect.ValueOf(val.Boolean())
	default:
		return reflect.Zero(targetType)
	}
}

// goToJSValue converts a Go reflect.Value to a V8 value.
func goToJSValue(iso *v8.Isolate, val reflect.Value) *v8.Value {
	if !val.IsValid() {
		return nil
	}
	switch val.Kind() {
	case reflect.String:
		v, _ := v8.NewValue(iso, val.String())
		return v
	case reflect.Int, reflect.Int64, reflect.Int32:
		v, _ := v8.NewValue(iso, int32(val.Int()))
		return v
	case reflect.Float64, reflect.Float32:
		v, _ := v8.NewValue(iso, val.Float())
		return v
	case reflect.Bool:
		v, _ := v8.NewValue(iso, val.Bool())
		return v
	default:
		return nil
	}
}

// goAnyToJSValue converts a Go any value to a V8 value.
func goAnyToJSValue(iso *v8.Isolate, ctx *v8.Context, value any) (*v8.Value, error) {
	if value == nil {
		return v8.Undefined(iso), nil
	}

	switch v := value.(type) {
	case string:
		return v8.NewValue(iso, v)
	case int:
		return v8.NewValue(iso, int32(v))
	case int32:
		return v8.NewValue(iso, v)
	case int64:
		return v8.NewValue(iso, int32(v))
	case float64:
		return v8.NewValue(iso, v)
	case bool:
		return v8.NewValue(iso, v)
	case *v8.Value:
		return v, nil
	case *v8.Object:
		return v.Value, nil
	default:
		// For complex types, serialize to JSON and parse in JS.
		data, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("marshaling value: %w", err)
		}
		script := fmt.Sprintf("JSON.parse(%s)", strconv.Quote(string(data)))
		return ctx.RunScript(script, "set_global.js")
	}
}
