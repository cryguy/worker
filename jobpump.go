package worker

import (
	"reflect"
	"unsafe"

	"modernc.org/libc"
	lib "modernc.org/libquickjs"
	"modernc.org/quickjs"
)

// executePendingJobs runs all pending microtasks (Promise callbacks, etc.) in
// the QuickJS runtime. The modernc.org/quickjs Go wrapper never calls
// JS_ExecutePendingJob, so Promise .then() callbacks would otherwise never
// fire. This function uses unsafe reflection to access the unexported runtime
// fields and calls XJS_ExecutePendingJob directly.
//
// Returns the number of jobs executed.
func executePendingJobs(vm *quickjs.VM) int {
	rt, tls, ok := extractRuntime(vm)
	if !ok {
		return 0
	}

	count := 0
	for {
		ret := lib.XJS_ExecutePendingJob(tls, rt, 0)
		if ret <= 0 {
			break
		}
		count++
	}
	return count
}

// extractRuntime uses unsafe reflection to pull the unexported tls and
// cRuntime values out of a *quickjs.VM.
//
// VM struct layout (modernc.org/quickjs@v0.17.1):
//
//	type VM struct {
//	    cContext       uintptr
//	    goFuncs       map[string]int32
//	    int32_16      lib.TJSValue
//	    int32_2       lib.TJSValue
//	    runtime       *runtime
//	    ...
//	}
//
//	type runtime struct {
//	    cRuntime uintptr
//	    tls      *libc.TLS
//	}
func extractRuntime(vm *quickjs.VM) (cRuntime uintptr, tls *libc.TLS, ok bool) {
	vmVal := reflect.ValueOf(vm).Elem() // deref pointer

	// Access the unexported 'runtime' field.
	rtField := vmVal.FieldByName("runtime")
	if !rtField.IsValid() || rtField.IsNil() {
		return 0, nil, false
	}

	// Get the *runtime pointer value using unsafe.
	rtPtr := unsafe.Pointer(rtField.Pointer())

	// runtime struct: first field is cRuntime (uintptr), second is tls (*libc.TLS).
	rtVal := reflect.NewAt(rtField.Type().Elem(), rtPtr).Elem()

	cRuntimeField := rtVal.FieldByName("cRuntime")
	if !cRuntimeField.IsValid() {
		return 0, nil, false
	}
	cRuntime = uintptr(cRuntimeField.Uint())

	tlsField := rtVal.FieldByName("tls")
	if !tlsField.IsValid() || tlsField.IsNil() {
		return 0, nil, false
	}
	tls = (*libc.TLS)(unsafe.Pointer(tlsField.Pointer()))

	return cRuntime, tls, true
}
