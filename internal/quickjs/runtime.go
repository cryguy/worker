//go:build !v8

package quickjs

import (
	"fmt"

	"github.com/cryguy/worker/internal/core"
	"modernc.org/quickjs"
)

// qjsRuntime implements core.JSRuntime for the QuickJS engine.
type qjsRuntime struct {
	vm *quickjs.VM
}

var _ core.JSRuntime = (*qjsRuntime)(nil)

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
