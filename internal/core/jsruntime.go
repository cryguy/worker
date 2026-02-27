package core

// JSRuntime abstracts the JavaScript engine (V8 or QuickJS) behind a
// common interface used by shared setup functions in internal/webapi
// and the shared event loop in internal/eventloop.
type JSRuntime interface {
	// Eval evaluates JavaScript source and discards the result.
	Eval(js string) error

	// EvalString evaluates JavaScript and returns the result as a Go string.
	EvalString(js string) (string, error)

	// EvalBool evaluates JavaScript and returns the result as a Go bool.
	EvalBool(js string) (bool, error)

	// EvalInt evaluates JavaScript and returns the result as a Go int.
	EvalInt(js string) (int, error)

	// RegisterFunc registers a Go function as a global JavaScript function.
	// The function's Go types are automatically marshaled to/from JS types.
	// On error return, the JS wrapper throws a TypeError instead of
	// returning an array.
	RegisterFunc(name string, fn any) error

	// SetGlobal sets a global variable on the JS context. Basic Go types
	// (string, int, float64, bool) are auto-converted to JS types.
	SetGlobal(name string, value any) error

	// RunMicrotasks pumps the microtask queue (Promise callbacks, etc.).
	// V8: PerformMicrotaskCheckpoint, QuickJS: ExecutePendingJob loop.
	RunMicrotasks()
}

// BinaryTransferer is an optional interface that JSRuntime implementations
// can provide for efficient binary data transfer between Go and JS.
// V8 implements this using SharedArrayBuffer; QuickJS uses direct ArrayBuffer
// access via the libquickjs C API.
type BinaryTransferer interface {
	// ReadBinaryFromJS reads binary data from a JS buffer stored at the
	// given global variable name and returns it as Go bytes.
	ReadBinaryFromJS(globalName string) ([]byte, error)

	// WriteBinaryToJS writes Go bytes into a JS ArrayBuffer at the given
	// global variable name.
	WriteBinaryToJS(globalName string, data []byte) error

	// BinaryMode returns the JS buffer type to use for binary transfer:
	// "sab" for SharedArrayBuffer (V8), "ab" for ArrayBuffer (QuickJS).
	BinaryMode() string
}
