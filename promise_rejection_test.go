package worker

import (
	"encoding/json"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Promise rejection semantics
// ---------------------------------------------------------------------------

// TestPromiseRejection_CaughtWithCatch verifies that a rejected promise caught
// via .catch() does not surface as an error on the WorkerResult.
func TestPromiseRejection_CaughtWithCatch(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    let caught = null;
    await Promise.reject(new Error("deliberate")).catch(err => {
      caught = err.message;
    });
    return Response.json({ caught });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Caught string `json:"caught"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Caught != "deliberate" {
		t.Errorf("caught = %q, want 'deliberate'", data.Caught)
	}
}

// TestPromiseRejection_CaughtWithThenErrorHandler verifies that a rejection
// handled by the second argument of .then() is treated as handled.
func TestPromiseRejection_CaughtWithThenErrorHandler(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    let reason = null;
    await Promise.reject("then-caught").then(null, r => { reason = r; });
    return Response.json({ reason });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Reason != "then-caught" {
		t.Errorf("reason = %q, want 'then-caught'", data.Reason)
	}
}

// TestPromiseRejection_CaughtWithTryCatch verifies that awaiting a rejected
// promise inside a try/catch surfaces the error to the catch block and does
// not bubble further.
func TestPromiseRejection_CaughtWithTryCatch(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    let message = null;
    try {
      await Promise.reject(new Error("try-caught"));
    } catch (e) {
      message = e.message;
    }
    return Response.json({ message });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Message != "try-caught" {
		t.Errorf("message = %q, want 'try-caught'", data.Message)
	}
}

// TestPromiseRejection_UnhandledBubblesAsError verifies that an unhandled
// promise rejection inside an async fetch handler propagates as an error.
func TestPromiseRejection_UnhandledBubblesAsError(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    await Promise.reject(new Error("unhandled boom"));
    return new Response("unreachable");
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	if r.Error == nil {
		t.Fatal("expected error for unhandled promise rejection, got nil")
	}
	if !strings.Contains(r.Error.Error(), "unhandled boom") {
		t.Errorf("error = %v, expected to contain 'unhandled boom'", r.Error)
	}
}

// TestPromiseRejection_ChainedCatch verifies that a .catch() later in a
// promise chain catches rejections from earlier in the chain.
func TestPromiseRejection_ChainedCatch(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    let result = await Promise.resolve(1)
      .then(() => { throw new Error("chain error"); })
      .catch(e => e.message);
    return Response.json({ result });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Result != "chain error" {
		t.Errorf("result = %q, want 'chain error'", data.Result)
	}
}

// TestPromiseRejection_PromiseAllRejectsOnFirst verifies that Promise.all
// rejects as soon as any member promise rejects, and the rejection reason
// matches the first failing promise.
func TestPromiseRejection_PromiseAllRejectsOnFirst(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    let caught = null;
    try {
      await Promise.all([
        Promise.resolve("ok"),
        Promise.reject(new Error("first fail")),
        Promise.reject(new Error("second fail")),
      ]);
    } catch (e) {
      caught = e.message;
    }
    return Response.json({ caught });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Caught string `json:"caught"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Promise.all rejects with the first rejection reason encountered.
	if data.Caught != "first fail" && data.Caught != "second fail" {
		t.Errorf("caught = %q, expected one of the rejection reasons", data.Caught)
	}
}

// TestPromiseRejection_PromiseAllSettled verifies that Promise.allSettled
// never rejects — all outcomes are reported regardless of individual failures.
func TestPromiseRejection_PromiseAllSettled(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    const results = await Promise.allSettled([
      Promise.resolve("good"),
      Promise.reject(new Error("bad")),
      Promise.resolve("also good"),
    ]);
    return Response.json({
      count: results.length,
      statuses: results.map(r => r.status),
      values: results.map(r => r.status === 'fulfilled' ? r.value : r.reason.message),
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Count    int      `json:"count"`
		Statuses []string `json:"statuses"`
		Values   []string `json:"values"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Count != 3 {
		t.Errorf("count = %d, want 3", data.Count)
	}
	if data.Statuses[0] != "fulfilled" {
		t.Errorf("statuses[0] = %q, want 'fulfilled'", data.Statuses[0])
	}
	if data.Statuses[1] != "rejected" {
		t.Errorf("statuses[1] = %q, want 'rejected'", data.Statuses[1])
	}
	if data.Statuses[2] != "fulfilled" {
		t.Errorf("statuses[2] = %q, want 'fulfilled'", data.Statuses[2])
	}
	if data.Values[1] != "bad" {
		t.Errorf("values[1] = %q, want 'bad'", data.Values[1])
	}
}

// TestPromiseRejection_PromiseRaceRejectsFirst verifies that Promise.race
// rejects when the fastest-settling promise rejects.
func TestPromiseRejection_PromiseRaceRejectsFirst(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    let caught = null;
    try {
      await Promise.race([
        Promise.reject(new Error("race lost")),
        new Promise(resolve => setTimeout(resolve, 100)),
      ]);
    } catch (e) {
      caught = e.message;
    }
    return Response.json({ caught });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Caught string `json:"caught"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Caught != "race lost" {
		t.Errorf("caught = %q, want 'race lost'", data.Caught)
	}
}

// TestPromiseRejection_RejectWithNonError verifies that a promise can be
// rejected with a non-Error value (string, number, object) and that value is
// preserved through the catch handler.
func TestPromiseRejection_RejectWithNonError(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    const results = {};

    await Promise.reject("string reason").catch(r => { results.str = r; });
    await Promise.reject(42).catch(r => { results.num = r; });
    await Promise.reject({ code: 404 }).catch(r => { results.obj = r.code; });
    await Promise.reject(null).catch(r => { results.nul = r; });

    return Response.json(results);
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Str string      `json:"str"`
		Num float64     `json:"num"`
		Obj float64     `json:"obj"`
		Nul interface{} `json:"nul"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Str != "string reason" {
		t.Errorf("str = %q, want 'string reason'", data.Str)
	}
	if data.Num != 42 {
		t.Errorf("num = %v, want 42", data.Num)
	}
	if data.Obj != 404 {
		t.Errorf("obj = %v, want 404", data.Obj)
	}
	if data.Nul != nil {
		t.Errorf("nul = %v, want null", data.Nul)
	}
}

// TestPromiseRejection_AsyncThrowIsRejection verifies that throwing inside an
// async function produces a rejected promise equivalent to an explicit reject.
func TestPromiseRejection_AsyncThrowIsRejection(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    async function boom() {
      throw new Error("async throw");
    }
    let caught = null;
    await boom().catch(e => { caught = e.message; });
    return Response.json({ caught });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Caught string `json:"caught"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Caught != "async throw" {
		t.Errorf("caught = %q, want 'async throw'", data.Caught)
	}
}

// TestPromiseRejection_FinallyRunsOnRejection verifies that .finally() runs
// even when the promise chain is rejected.
func TestPromiseRejection_FinallyRunsOnRejection(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    let finallyRan = false;
    let caught = null;
    await Promise.reject(new Error("finally test"))
      .finally(() => { finallyRan = true; })
      .catch(e => { caught = e.message; });
    return Response.json({ finallyRan, caught });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		FinallyRan bool   `json:"finallyRan"`
		Caught     string `json:"caught"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.FinallyRan {
		t.Error("finally should run even when promise rejects")
	}
	if data.Caught != "finally test" {
		t.Errorf("caught = %q, want 'finally test'", data.Caught)
	}
}

// TestPromiseRejection_NestedAsync verifies that rejection propagates correctly
// through nested async calls.
func TestPromiseRejection_NestedAsync(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    async function inner() {
      throw new Error("from inner");
    }
    async function middle() {
      await inner();
    }
    async function outer() {
      await middle();
    }
    let caught = null;
    try {
      await outer();
    } catch (e) {
      caught = e.message;
    }
    return Response.json({ caught });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Caught string `json:"caught"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Caught != "from inner" {
		t.Errorf("caught = %q, want 'from inner'", data.Caught)
	}
}

// TestPromiseRejection_CatchReturnValue verifies that returning a value from a
// .catch() handler converts the rejected promise back to a fulfilled one.
func TestPromiseRejection_CatchReturnValue(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    const value = await Promise.reject(new Error("recoverable"))
      .catch(e => "recovered: " + e.message);
    return Response.json({ value });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Value != "recovered: recoverable" {
		t.Errorf("value = %q, want 'recovered: recoverable'", data.Value)
	}
}

// TestPromiseRejection_CatchRethrow verifies that rethrowing inside a .catch()
// keeps the promise in a rejected state.
func TestPromiseRejection_CatchRethrow(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    let finalReason = null;
    await Promise.reject(new Error("original"))
      .catch(e => { throw new Error("rethrown: " + e.message); })
      .catch(e => { finalReason = e.message; });
    return Response.json({ finalReason });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		FinalReason string `json:"finalReason"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.FinalReason != "rethrown: original" {
		t.Errorf("finalReason = %q, want 'rethrown: original'", data.FinalReason)
	}
}

// TestPromiseRejection_PromiseAnyResolvesOnFirst verifies that Promise.any
// resolves with the first fulfilled promise, ignoring prior rejections.
func TestPromiseRejection_PromiseAnyResolvesOnFirst(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    let value = null;
    try {
      value = await Promise.any([
        Promise.reject(new Error("fail 1")),
        Promise.resolve("winner"),
        Promise.reject(new Error("fail 2")),
      ]);
    } catch (e) {
      value = "AggregateError: " + e.message;
    }
    return Response.json({ value });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Value != "winner" {
		t.Errorf("value = %q, want 'winner'", data.Value)
	}
}

// TestPromiseRejection_PromiseAnyAllRejected verifies that Promise.any
// rejects with an AggregateError when all promises reject.
func TestPromiseRejection_PromiseAnyAllRejected(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    let isAggregate = false;
    let errors = [];
    try {
      await Promise.any([
        Promise.reject(new Error("e1")),
        Promise.reject(new Error("e2")),
      ]);
    } catch (e) {
      isAggregate = e instanceof AggregateError;
      errors = e.errors ? e.errors.map(err => err.message) : [];
    }
    return Response.json({ isAggregate, errors });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		IsAggregate bool     `json:"isAggregate"`
		Errors      []string `json:"errors"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.IsAggregate {
		t.Error("Promise.any with all rejections should throw AggregateError")
	}
	if len(data.Errors) != 2 {
		t.Errorf("errors count = %d, want 2", len(data.Errors))
	}
}

// TestPromiseRejection_FetchHandlerThrowSyncError verifies that a synchronous
// throw inside a non-async fetch handler is reported as an error.
func TestPromiseRejection_FetchHandlerThrowSyncError(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    throw new TypeError("sync type error");
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	if r.Error == nil {
		t.Fatal("expected error for sync throw, got nil")
	}
	if !strings.Contains(r.Error.Error(), "sync type error") {
		t.Errorf("error = %v, expected to contain 'sync type error'", r.Error)
	}
}

// TestPromiseRejection_MultipleAwaitedRejections verifies that catching
// multiple sequentially awaited rejections all succeed independently.
func TestPromiseRejection_MultipleAwaitedRejections(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    const results = [];
    for (let i = 0; i < 3; i++) {
      try {
        await Promise.reject(new Error("err-" + i));
      } catch (e) {
        results.push(e.message);
      }
    }
    return Response.json({ results });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Results []string `json:"results"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(data.Results) != 3 {
		t.Fatalf("results count = %d, want 3", len(data.Results))
	}
	for i, msg := range data.Results {
		want := strings.Repeat("err-", 1) + string(rune('0'+i))
		if msg != want {
			t.Errorf("results[%d] = %q, want %q", i, msg, want)
		}
	}
}
