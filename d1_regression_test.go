package worker

import (
	"encoding/json"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// D1 + Promise regression tests
//
// Known past bug (from test_plan.md):
//   D1 binding methods (all, first, raw, run) threw synchronously when the
//   underlying SQL failed, instead of returning Promise.reject(). This broke
//   Promise.allSettled because allSettled expects every input to be a
//   thenable — a synchronous throw escapes the allSettled machinery entirely.
// ---------------------------------------------------------------------------

// TestD1Regression_AllSettledWithBadSQL is the critical regression test.
// Promise.allSettled must receive a rejected promise — not a synchronous throw
// — when a D1 method encounters bad SQL. Before the fix this test would panic
// or surface an unhandled exception rather than reporting a "rejected" outcome.
func TestD1Regression_AllSettledWithBadSQL(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    await env.DB.exec("CREATE TABLE reg_users (id INTEGER PRIMARY KEY, name TEXT)");
    await env.DB.exec("INSERT INTO reg_users (name) VALUES ('alice')");

    // Mix of a valid query and one that will fail.
    const results = await Promise.allSettled([
      env.DB.prepare("SELECT * FROM reg_users").all(),
      env.DB.prepare("SELECT * FROM nonexistent_table_xyz").all(),
    ]);

    return Response.json({
      count: results.length,
      statuses: results.map(r => r.status),
      firstOk: results[0].status === 'fulfilled' && results[0].value.results.length > 0,
      secondRejected: results[1].status === 'rejected',
      secondReason: results[1].status === 'rejected' ? String(results[1].reason) : null,
    });
  },
};`

	env := d1Env("d1-reg-allsettled-1")
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Count          int      `json:"count"`
		Statuses       []string `json:"statuses"`
		FirstOk        bool     `json:"firstOk"`
		SecondRejected bool     `json:"secondRejected"`
		SecondReason   string   `json:"secondReason"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Count != 2 {
		t.Errorf("count = %d, want 2", data.Count)
	}
	if data.Statuses[0] != "fulfilled" {
		t.Errorf("statuses[0] = %q, want 'fulfilled'", data.Statuses[0])
	}
	if !data.FirstOk {
		t.Error("first query (valid) should be fulfilled with results")
	}
	if !data.SecondRejected {
		t.Errorf("statuses[1] = %q, want 'rejected' — D1 bad SQL must return Promise.reject, not throw synchronously", data.Statuses[1])
	}
	if data.SecondReason == "" {
		t.Error("rejected reason should be a non-empty error message")
	}
}

// TestD1Regression_AllSettledWithBadSQL_First verifies the same bug for the
// .first() D1 method.
func TestD1Regression_AllSettledWithBadSQL_First(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    await env.DB.exec("CREATE TABLE reg_first (id INTEGER PRIMARY KEY, val TEXT)");
    await env.DB.exec("INSERT INTO reg_first (val) VALUES ('v1')");

    const results = await Promise.allSettled([
      env.DB.prepare("SELECT * FROM reg_first").first(),
      env.DB.prepare("SELECT * FROM no_such_table").first(),
    ]);

    return Response.json({
      statuses: results.map(r => r.status),
      firstFulfilled: results[0].status === 'fulfilled',
      secondRejected: results[1].status === 'rejected',
    });
  },
};`

	env := d1Env("d1-reg-allsettled-first")
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Statuses       []string `json:"statuses"`
		FirstFulfilled bool     `json:"firstFulfilled"`
		SecondRejected bool     `json:"secondRejected"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.FirstFulfilled {
		t.Errorf("first .first() (valid) should be fulfilled, got %q", data.Statuses[0])
	}
	if !data.SecondRejected {
		t.Errorf("second .first() (bad SQL) should be rejected, got %q — synchronous throw bug", data.Statuses[1])
	}
}

// TestD1Regression_AllSettledWithBadSQL_Run verifies the same bug for .run().
func TestD1Regression_AllSettledWithBadSQL_Run(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    await env.DB.exec("CREATE TABLE reg_run (id INTEGER PRIMARY KEY, val TEXT)");

    const results = await Promise.allSettled([
      env.DB.prepare("INSERT INTO reg_run (val) VALUES (?)").bind("ok").run(),
      env.DB.prepare("INSERT INTO no_such_table (val) VALUES (?)").bind("bad").run(),
    ]);

    return Response.json({
      statuses: results.map(r => r.status),
      firstFulfilled: results[0].status === 'fulfilled',
      secondRejected: results[1].status === 'rejected',
    });
  },
};`

	env := d1Env("d1-reg-allsettled-run")
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Statuses       []string `json:"statuses"`
		FirstFulfilled bool     `json:"firstFulfilled"`
		SecondRejected bool     `json:"secondRejected"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.FirstFulfilled {
		t.Errorf("first .run() (valid insert) should be fulfilled, got %q", data.Statuses[0])
	}
	if !data.SecondRejected {
		t.Errorf("second .run() (bad table) should be rejected, got %q — synchronous throw bug", data.Statuses[1])
	}
}

// TestD1Regression_AllSettledWithBadSQL_Raw verifies the same bug for .raw().
func TestD1Regression_AllSettledWithBadSQL_Raw(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    await env.DB.exec("CREATE TABLE reg_raw (id INTEGER PRIMARY KEY, val TEXT)");
    await env.DB.exec("INSERT INTO reg_raw (val) VALUES ('hello')");

    const results = await Promise.allSettled([
      env.DB.prepare("SELECT val FROM reg_raw").raw(),
      env.DB.prepare("SELECT val FROM no_such_table").raw(),
    ]);

    return Response.json({
      statuses: results.map(r => r.status),
      firstFulfilled: results[0].status === 'fulfilled',
      secondRejected: results[1].status === 'rejected',
    });
  },
};`

	env := d1Env("d1-reg-allsettled-raw")
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Statuses       []string `json:"statuses"`
		FirstFulfilled bool     `json:"firstFulfilled"`
		SecondRejected bool     `json:"secondRejected"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.FirstFulfilled {
		t.Errorf("first .raw() (valid) should be fulfilled, got %q", data.Statuses[0])
	}
	if !data.SecondRejected {
		t.Errorf("second .raw() (bad SQL) should be rejected, got %q — synchronous throw bug", data.Statuses[1])
	}
}

// TestD1Regression_ExecReturnsPromise verifies that exec() returns a Promise
// (not a synchronous value) so it is safe to use in Promise combinators.
// Note: exec() always resolves (never rejects) — its result has a count field.
// The critical property is that it is a thenable, enabling use in Promise.all etc.
func TestD1Regression_ExecReturnsPromise(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    await env.DB.exec("CREATE TABLE reg_exec (id INTEGER PRIMARY KEY, val TEXT)");

    // exec() must return a thenable so it works inside Promise combinators.
    const p = env.DB.exec("INSERT INTO reg_exec (val) VALUES ('hello')");
    const isThenable = p !== null && typeof p === 'object' && typeof p.then === 'function';

    const result = await p;
    const hasCount = typeof result === 'object' && result !== null && 'count' in result;

    // Verify it also works inside Promise.all without throwing synchronously.
    const [r1, r2] = await Promise.all([
      env.DB.exec("INSERT INTO reg_exec (val) VALUES ('a')"),
      env.DB.exec("INSERT INTO reg_exec (val) VALUES ('b')"),
    ]);
    const allWork = typeof r1.count === 'number' && typeof r2.count === 'number';

    return Response.json({ isThenable, hasCount, allWork });
  },
};`

	env := d1Env("d1-reg-exec-promise")
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		IsThenable bool `json:"isThenable"`
		HasCount   bool `json:"hasCount"`
		AllWork    bool `json:"allWork"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.IsThenable {
		t.Error("exec() must return a Promise/thenable, not a synchronous value")
	}
	if !data.HasCount {
		t.Error("exec() resolved value should have a 'count' field")
	}
	if !data.AllWork {
		t.Error("exec() should work inside Promise.all without synchronous throws")
	}
}

// TestD1Regression_AllWithMixedBatch verifies that batch() with mixed
// good/bad statements uses Promise.allSettled semantics internally and
// that the outer Promise.allSettled also works correctly.
func TestD1Regression_AllWithMixedBatch(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    await env.DB.exec("CREATE TABLE reg_mixed (id INTEGER PRIMARY KEY, name TEXT)");
    await env.DB.exec("INSERT INTO reg_mixed (name) VALUES ('alpha')");

    // Two independent allSettled operations; neither should throw synchronously.
    const [r1, r2] = await Promise.all([
      Promise.allSettled([
        env.DB.prepare("SELECT * FROM reg_mixed").all(),
      ]),
      Promise.allSettled([
        env.DB.prepare("SELECT * FROM definitely_no_such_table").all(),
      ]),
    ]);

    return Response.json({
      r1Status: r1[0].status,
      r2Status: r2[0].status,
      r1HasResults: r1[0].status === 'fulfilled' && Array.isArray(r1[0].value.results),
      r2IsRejected: r2[0].status === 'rejected',
    });
  },
};`

	env := d1Env("d1-reg-mixed-batch")
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		R1Status     string `json:"r1Status"`
		R2Status     string `json:"r2Status"`
		R1HasResults bool   `json:"r1HasResults"`
		R2IsRejected bool   `json:"r2IsRejected"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.R1Status != "fulfilled" {
		t.Errorf("r1Status = %q, want 'fulfilled'", data.R1Status)
	}
	if !data.R1HasResults {
		t.Error("r1 fulfilled value should have results array")
	}
	if !data.R2IsRejected {
		t.Errorf("r2Status = %q, want 'rejected' — D1 bad SQL must be async rejection", data.R2Status)
	}
}

// TestD1Regression_PromiseRaceWithD1 verifies D1 queries work inside
// Promise.race, which also requires proper thenable promises.
func TestD1Regression_PromiseRaceWithD1(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    await env.DB.exec("CREATE TABLE reg_race (id INTEGER PRIMARY KEY, name TEXT)");
    await env.DB.exec("INSERT INTO reg_race (name) VALUES ('winner')");

    let result = null;
    let errMsg = null;
    try {
      result = await Promise.race([
        env.DB.prepare("SELECT name FROM reg_race").first(),
      ]);
    } catch(e) {
      errMsg = e.message;
    }

    return Response.json({
      result,
      errMsg,
      success: result !== null && result.name === 'winner',
    });
  },
};`

	env := d1Env("d1-reg-race")
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Success bool   `json:"success"`
		ErrMsg  string `json:"errMsg"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.Success {
		t.Errorf("Promise.race with D1 query should succeed, errMsg = %q", data.ErrMsg)
	}
}

// TestD1Regression_SyncThrowDoesNotEscapeAllSettled is the definitive
// regression guard. It verifies that a D1 method error does NOT propagate
// as a synchronous exception that escapes Promise.allSettled entirely.
// Before the fix, calling allSettled with a D1 statement that threw
// synchronously would cause the entire fetch handler to error rather than
// reporting individual rejection statuses.
func TestD1Regression_SyncThrowDoesNotEscapeAllSettled(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    // If D1 throws synchronously, allSettled cannot capture it and
    // the entire handler would throw, causing r.Error != nil in Go.
    // The fix: all D1 methods must return Promise.reject() for errors.
    let allSettledCompleted = false;
    let results;
    try {
      results = await Promise.allSettled([
        env.DB.prepare("SELECT * FROM absolutely_nonexistent_table_abc123").all(),
        env.DB.prepare("SELECT * FROM another_nonexistent_table_xyz789").first(),
        env.DB.prepare("SELECT * FROM yet_another_nonexistent_table_def456").raw(),
      ]);
      allSettledCompleted = true;
    } catch(e) {
      // This catch block should NOT be reached if D1 correctly returns
      // Promise.reject() instead of throwing synchronously.
      return Response.json({
        allSettledCompleted: false,
        syncThrowEscaped: true,
        errorMsg: e.message,
      });
    }

    return Response.json({
      allSettledCompleted,
      syncThrowEscaped: false,
      count: results.length,
      allRejected: results.every(r => r.status === 'rejected'),
    });
  },
};`

	env := d1Env("d1-reg-sync-throw")
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	// The handler must succeed (no Go-level error).
	assertOK(t, r)

	var data struct {
		AllSettledCompleted bool   `json:"allSettledCompleted"`
		SyncThrowEscaped    bool   `json:"syncThrowEscaped"`
		Count               int    `json:"count"`
		AllRejected         bool   `json:"allRejected"`
		ErrorMsg            string `json:"errorMsg"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.SyncThrowEscaped {
		t.Errorf("D1 error escaped Promise.allSettled as a synchronous throw: %s — the bug is not fixed", data.ErrorMsg)
	}
	if !data.AllSettledCompleted {
		t.Error("Promise.allSettled should always complete, even when all D1 queries fail")
	}
	if data.Count != 3 {
		t.Errorf("allSettled count = %d, want 3", data.Count)
	}
	if !data.AllRejected {
		t.Error("all three D1 queries against nonexistent tables should be rejected")
	}
}

// TestD1Regression_PromiseAnyWithD1 verifies that Promise.any works with D1
// promises (requires proper thenable behaviour).
func TestD1Regression_PromiseAnyWithD1(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    await env.DB.exec("CREATE TABLE reg_any (id INTEGER PRIMARY KEY, name TEXT)");
    await env.DB.exec("INSERT INTO reg_any (name) VALUES ('found')");

    let value = null;
    let isAggregate = false;
    try {
      value = await Promise.any([
        env.DB.prepare("SELECT * FROM no_table_1").all(),
        env.DB.prepare("SELECT * FROM reg_any").all(),
      ]);
    } catch(e) {
      isAggregate = e instanceof AggregateError;
    }

    return Response.json({
      success: value !== null && Array.isArray(value.results),
      isAggregate,
    });
  },
};`

	env := d1Env("d1-reg-any")
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Success     bool `json:"success"`
		IsAggregate bool `json:"isAggregate"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Promise.any should resolve with the first success (reg_any.all()).
	// If it throws an AggregateError, the D1 promises aren't proper thenables.
	if !data.Success {
		t.Error("Promise.any should resolve with the first fulfilled D1 query")
	}
	if data.IsAggregate {
		t.Error("Promise.any produced AggregateError — D1 valid query may not be returning a proper Promise")
	}
}

// TestD1Regression_ErrorMessagePreserved verifies that when D1 rejects,
// the rejection reason contains a useful error message (not just "undefined"
// or an empty string).
func TestD1Regression_ErrorMessagePreserved(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    const results = await Promise.allSettled([
      env.DB.prepare("SELECT * FROM no_table").all(),
      env.DB.prepare("NOT VALID SQL AT ALL ***").all(),
    ]);

    return Response.json({
      reasons: results.map(r =>
        r.status === 'rejected' ? String(r.reason) : 'fulfilled'
      ),
    });
  },
};`

	env := d1Env("d1-reg-errmsg")
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Reasons []string `json:"reasons"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(data.Reasons) != 2 {
		t.Fatalf("reasons count = %d, want 2", len(data.Reasons))
	}
	for i, reason := range data.Reasons {
		if reason == "" || reason == "undefined" || reason == "null" {
			t.Errorf("reasons[%d] = %q — rejection reason should be a meaningful error string", i, reason)
		}
		if strings.Contains(reason, "fulfilled") {
			t.Errorf("reasons[%d] = %q — query against nonexistent table should be rejected", i, reason)
		}
	}
}
