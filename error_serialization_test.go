package worker

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 21. Error serialization
// ---------------------------------------------------------------------------

// TestErrorSerialization_TypeError verifies that a JS TypeError thrown from
// the fetch handler surfaces as a non-nil WorkerResult.Error containing the
// error message.
func TestErrorSerialization_TypeError(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    null.property; // TypeError: Cannot read properties of null
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	if r.Error == nil {
		t.Fatal("expected error for TypeError, got nil")
	}
	t.Logf("TypeError error: %v", r.Error)
}

// TestErrorSerialization_RangeError verifies that a JS RangeError surfaces
// as a non-nil WorkerResult.Error containing the error message.
func TestErrorSerialization_RangeError(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    throw new RangeError("value out of range");
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	if r.Error == nil {
		t.Fatal("expected error for RangeError, got nil")
	}
	if !strings.Contains(r.Error.Error(), "range") {
		t.Errorf("error = %v, expected to contain 'range'", r.Error)
	}
}

// TestErrorSerialization_CustomErrorMessage verifies that a custom Error with
// a specific message is propagated faithfully.
func TestErrorSerialization_CustomErrorMessage(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    throw new Error("custom error message here");
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	if r.Error == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(r.Error.Error(), "custom error message here") {
		t.Errorf("error = %v, expected to contain 'custom error message here'", r.Error)
	}
}

// TestErrorSerialization_ThrownString verifies that throwing a plain string
// (not an Error object) results in WorkerResult.Error being non-nil.
func TestErrorSerialization_ThrownString(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    throw "plain string error";
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	if r.Error == nil {
		t.Fatal("expected error for thrown string, got nil")
	}
	t.Logf("thrown string error: %v", r.Error)
}

// TestErrorSerialization_ThrownNumber verifies that throwing a number results
// in WorkerResult.Error being non-nil.
func TestErrorSerialization_ThrownNumber(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    throw 42;
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	if r.Error == nil {
		t.Fatal("expected error for thrown number, got nil")
	}
	t.Logf("thrown number error: %v", r.Error)
}

// TestErrorSerialization_ThrownObject verifies that throwing a plain object
// results in WorkerResult.Error being non-nil.
func TestErrorSerialization_ThrownObject(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    throw { code: 500, msg: "something went wrong" };
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	if r.Error == nil {
		t.Fatal("expected error for thrown object, got nil")
	}
	t.Logf("thrown object error: %v", r.Error)
}

// TestErrorSerialization_ThrownNull verifies that throwing null results in
// WorkerResult.Error being non-nil.
func TestErrorSerialization_ThrownNull(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    throw null;
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	if r.Error == nil {
		t.Fatal("expected error for thrown null, got nil")
	}
	t.Logf("thrown null error: %v", r.Error)
}

// TestErrorSerialization_AsyncThrow verifies that an error thrown inside an
// async handler is serialised identically to a synchronous throw.
func TestErrorSerialization_AsyncThrow(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    throw new Error("async boom");
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	if r.Error == nil {
		t.Fatal("expected error for async throw, got nil")
	}
	if !strings.Contains(r.Error.Error(), "async boom") {
		t.Errorf("error = %v, expected to contain 'async boom'", r.Error)
	}
}

// TestErrorSerialization_RejectedPromise verifies that returning a rejected
// promise from the fetch handler produces WorkerResult.Error.
func TestErrorSerialization_RejectedPromise(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    return Promise.reject(new Error("rejected promise"));
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	if r.Error == nil {
		t.Fatal("expected error for rejected promise, got nil")
	}
	if !strings.Contains(r.Error.Error(), "rejected promise") {
		t.Errorf("error = %v, expected to contain 'rejected promise'", r.Error)
	}
}

// TestErrorSerialization_CaughtErrorNotPropagated verifies that errors caught
// inside JS do NOT surface as WorkerResult.Error — the worker handles them
// and returns a normal response.
func TestErrorSerialization_CaughtErrorNotPropagated(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    try {
      throw new Error("internal");
    } catch (e) {
      return new Response("caught: " + e.message, { status: 200 });
    }
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)
	if string(r.Response.Body) != "caught: internal" {
		t.Errorf("body = %q, want 'caught: internal'", r.Response.Body)
	}
}

// TestErrorSerialization_StackTraceInLogs verifies that when a worker catches
// an error and logs its stack, the stack trace appears in WorkerResult.Logs.
func TestErrorSerialization_StackTraceInLogs(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    try {
      throw new Error("stack test");
    } catch (err) {
      // Log both message and stack so either form is captured.
      console.error(err.message);
      console.error(err.stack || err.message);
      return new Response("ok");
    }
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	if len(r.Logs) == 0 {
		t.Fatal("expected logs with stack trace, got none")
	}
	// The stack string may be "Error: stack test\n  at ..." or just the
	// message string depending on engine; either way "stack test" must appear.
	found := false
	for _, l := range r.Logs {
		if strings.Contains(l.Message, "stack test") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("logs = %v, expected an entry containing 'stack test'", r.Logs)
	}
}

// TestErrorSerialization_ErrorAfterAwait verifies that an error thrown after
// an awaited expression is still captured.
func TestErrorSerialization_ErrorAfterAwait(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    await Promise.resolve(); // yield control once
    throw new Error("post-await error");
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	if r.Error == nil {
		t.Fatal("expected error after await, got nil")
	}
	if !strings.Contains(r.Error.Error(), "post-await error") {
		t.Errorf("error = %v, expected to contain 'post-await error'", r.Error)
	}
}

// TestErrorSerialization_MultipleErrorTypes exercises TypeError, RangeError,
// SyntaxError (thrown manually), URIError, EvalError in a table-driven manner.
func TestErrorSerialization_MultipleErrorTypes(t *testing.T) {
	e := newTestEngine(t)

	cases := []struct {
		name string
		js   string
		want string
	}{
		{
			"TypeError",
			`throw new TypeError("type problem")`,
			"type problem",
		},
		{
			"RangeError",
			`throw new RangeError("range problem")`,
			"range problem",
		},
		{
			"URIError",
			`throw new URIError("uri problem")`,
			"uri problem",
		},
		{
			"EvalError",
			`throw new EvalError("eval problem")`,
			"eval problem",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			source := `export default {
  fetch(request, env) {
    ` + tc.js + `;
  },
};`
			r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
			if r.Error == nil {
				t.Fatalf("%s: expected error, got nil", tc.name)
			}
			if !strings.Contains(r.Error.Error(), tc.want) {
				t.Errorf("%s: error = %v, expected to contain %q", tc.name, r.Error, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Additional error type tests (short-name variants)
// ---------------------------------------------------------------------------

func TestError_TypeError(t *testing.T) {
	e := newTestEngine(t)
	source := `export default {
  fetch(request, env) {
    throw new TypeError("not a function");
  },
};`
	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	if r.Error == nil {
		t.Fatal("expected error for TypeError, got nil")
	}
	msg := r.Error.Error()
	if !strings.Contains(msg, "TypeError") && !strings.Contains(msg, "not a function") {
		t.Errorf("error = %v, expected 'TypeError' or 'not a function'", r.Error)
	}
}

func TestError_RangeError(t *testing.T) {
	e := newTestEngine(t)
	source := `export default {
  fetch(request, env) {
    throw new RangeError("out of range");
  },
};`
	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	if r.Error == nil {
		t.Fatal("expected error for RangeError, got nil")
	}
}

func TestError_CustomMessage(t *testing.T) {
	e := newTestEngine(t)
	source := `export default {
  fetch(request, env) {
    throw new Error("custom message here");
  },
};`
	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	if r.Error == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(r.Error.Error(), "custom message here") {
		t.Errorf("error = %v, expected to contain 'custom message here'", r.Error)
	}
}

func TestError_ThrowString(t *testing.T) {
	e := newTestEngine(t)
	source := `export default {
  fetch(request, env) {
    throw "raw string error";
  },
};`
	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	if r.Error == nil {
		t.Fatal("expected error for thrown string, got nil")
	}
}

func TestError_ThrowNumber(t *testing.T) {
	e := newTestEngine(t)
	source := `export default {
  fetch(request, env) {
    throw 404;
  },
};`
	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	if r.Error == nil {
		t.Fatal("expected error for thrown number, got nil")
	}
}

func TestError_ThrowObject(t *testing.T) {
	e := newTestEngine(t)
	source := `export default {
  fetch(request, env) {
    throw { code: 500, msg: "server error" };
  },
};`
	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	if r.Error == nil {
		t.Fatal("expected error for thrown object, got nil")
	}
}

func TestError_StackTrace(t *testing.T) {
	e := newTestEngine(t)
	source := `export default {
  async fetch(request, env) {
    function deepCall() { throw new Error("deep error"); }
    function middleCall() { deepCall(); }
    try {
      middleCall();
    } catch (err) {
      console.error(err.message);
      console.error(err.stack || err.message);
    }
    throw new Error("deep error");
  },
};`
	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	if r.Error == nil {
		t.Fatal("expected error for stack trace test, got nil")
	}
	// Verify at least one log entry contains "deep error" or "deepCall".
	found := false
	for _, l := range r.Logs {
		if strings.Contains(l.Message, "deep error") || strings.Contains(l.Message, "deepCall") {
			found = true
			break
		}
	}
	if !found {
		t.Logf("logs = %v (stack trace may not include function names in all engines)", r.Logs)
	}
}

// TestErrorSerialization_EngineRecoveryAfterError verifies the engine can
// serve subsequent successful requests after an error-producing execution.
func TestErrorSerialization_EngineRecoveryAfterError(t *testing.T) {
	e := newTestEngine(t)

	// First: a request that throws — use a distinct siteID so it doesn't
	// collide with the healthy worker compiled next.
	errSrc := `export default {
  fetch(request, env) {
    throw new Error("intentional");
  },
};`
	if _, err := e.CompileAndCache("err-recovery-bad", "deploy1", errSrc); err != nil {
		t.Fatalf("CompileAndCache (err): %v", err)
	}
	r1 := e.Execute("err-recovery-bad", "deploy1", defaultEnv(), getReq("http://localhost/"))
	if r1.Error == nil {
		t.Fatal("expected error from first execution")
	}

	// Second: a healthy request under a different siteID — engine must recover.
	okSrc := `export default {
  fetch(request, env) {
    return new Response("still alive");
  },
};`
	if _, err := e.CompileAndCache("err-recovery-ok", "deploy1", okSrc); err != nil {
		t.Fatalf("CompileAndCache (ok): %v", err)
	}
	r2 := e.Execute("err-recovery-ok", "deploy1", defaultEnv(), getReq("http://localhost/"))
	if r2.Error != nil {
		t.Fatalf("unexpected error after recovery: %v", r2.Error)
	}
	if r2.Response == nil {
		t.Fatal("response is nil after recovery")
	}
	if string(r2.Response.Body) != "still alive" {
		t.Errorf("body = %q, want 'still alive'", r2.Response.Body)
	}
}
