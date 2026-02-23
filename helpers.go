package worker

import (
	"fmt"
	"strconv"

	"modernc.org/quickjs"
)

// boolToInt converts a bool to 1 (true) or 0 (false) for quickjs interop,
// since quickjs RegisterFunc cannot marshal Go bool return values.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// evalDiscard evaluates JavaScript and discards the result (frees the Value).
// Use for fire-and-forget JS execution where the return value is not needed.
func evalDiscard(vm *quickjs.VM, js string) error {
	v, err := vm.EvalValue(js, quickjs.EvalGlobal)
	if err != nil {
		return err
	}
	v.Free()
	return nil
}

// evalString evaluates JavaScript and returns the result as a Go string.
// Uses vm.Eval which auto-converts to Go types (no manual Free needed).
func evalString(vm *quickjs.VM, js string) (string, error) {
	r, err := vm.Eval(js, quickjs.EvalGlobal)
	if err != nil {
		return "", err
	}
	if r == nil {
		return "", nil
	}
	return fmt.Sprint(r), nil
}

// evalInt evaluates JavaScript and returns the result as a Go int.
func evalInt(vm *quickjs.VM, js string) (int, error) {
	r, err := vm.Eval(js, quickjs.EvalGlobal)
	if err != nil {
		return 0, err
	}
	switch v := r.(type) {
	case int:
		return v, nil
	case float64:
		return int(v), nil
	default:
		return 0, fmt.Errorf("expected int, got %T", r)
	}
}

// evalBool evaluates JavaScript and returns the result as a Go bool.
func evalBool(vm *quickjs.VM, js string) (bool, error) {
	r, err := vm.Eval(js, quickjs.EvalGlobal)
	if err != nil {
		return false, err
	}
	b, ok := r.(bool)
	if !ok {
		return false, fmt.Errorf("expected bool, got %T", r)
	}
	return b, nil
}

// setGlobal sets a global property on the VM's global object.
// The value is auto-converted from Go types to JS types.
func setGlobal(vm *quickjs.VM, name string, value any) error {
	atom, err := vm.NewAtom(name)
	if err != nil {
		return fmt.Errorf("creating atom %q: %w", name, err)
	}
	glob := vm.GlobalObject()
	defer glob.Free()
	return glob.SetProperty(atom, value)
}

// getGlobalString reads a global property as a string.
func getGlobalString(vm *quickjs.VM, name string) (string, error) {
	return evalString(vm, fmt.Sprintf("String(globalThis[%q])", name))
}

// getReqIDFromJS reads the __requestID global and parses it to uint64.
func getReqIDFromJS(vm *quickjs.VM) uint64 {
	s, err := getGlobalString(vm, "__requestID")
	if err != nil {
		return 0
	}
	id, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// newJSObject creates a new empty JavaScript object via EvalValue.
// The caller owns the returned Value and must Free() it when done.
func newJSObject(vm *quickjs.VM) (quickjs.Value, error) {
	return vm.EvalValue("({})", quickjs.EvalGlobal)
}

// NewJSObject creates a new empty JavaScript object. This is the exported
// version of newJSObject, intended for use by downstream users building
// custom env bindings via EnvBindingFunc.
func NewJSObject(vm *quickjs.VM) (quickjs.Value, error) {
	return newJSObject(vm)
}

// jsEscape escapes a string for safe embedding in JavaScript source code.
// Uses %q formatting which produces a Go-quoted string that is also valid JS.
func jsEscape(s string) string {
	return strconv.Quote(s)
}

// registerGoFunc registers a Go function that returns (T, error) and wraps it
// in JS so that:
//   - On success (error == nil), returns T directly (not [T, null])
//   - On error (error != nil), throws a TypeError with the error message
//
// This is needed because modernc.org/quickjs's RegisterFunc returns multi-value
// Go results as JS arrays [value, error] instead of throwing on error.
func registerGoFunc(vm *quickjs.VM, name string, f any, wantThis bool) error {
	rawName := "__raw_" + name
	if err := vm.RegisterFunc(rawName, f, wantThis); err != nil {
		return err
	}
	// Build JS wrapper with proper argument forwarding.
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
	return evalDiscard(vm, wrapJS)
}
