package worker

import (
	"encoding/json"
	"testing"
)

func TestScheduler_ObjectExists(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    return Response.json({
      exists: typeof scheduler !== 'undefined',
      hasWait: typeof scheduler.wait === 'function',
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Exists  bool `json:"exists"`
		HasWait bool `json:"hasWait"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !data.Exists {
		t.Error("scheduler should exist on globalThis")
	}
	if !data.HasWait {
		t.Error("scheduler.wait should be a function")
	}
}

func TestScheduler_WaitResolves(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    const before = performance.now();
    await scheduler.wait(50);
    const after = performance.now();
    return Response.json({
      resolved: true,
      elapsed: after - before,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Resolved bool    `json:"resolved"`
		Elapsed  float64 `json:"elapsed"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !data.Resolved {
		t.Error("scheduler.wait() should resolve")
	}
	if data.Elapsed < 30 {
		t.Errorf("elapsed = %f, expected >= 30ms", data.Elapsed)
	}
}

func TestScheduler_WaitZeroMs(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    await scheduler.wait(0);
    return Response.json({ done: true });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Done bool `json:"done"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !data.Done {
		t.Error("scheduler.wait(0) should resolve")
	}
}

func TestScheduler_WaitNegative(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    await scheduler.wait(-10);
    return Response.json({ done: true });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Done bool `json:"done"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.Done {
		t.Error("scheduler.wait(-10) should resolve")
	}
}

// ---------------------------------------------------------------------------
// Scheduler spec compliance tests
// ---------------------------------------------------------------------------

func TestScheduler_PostTask(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    const result = await scheduler.postTask(() => 42);
    return Response.json({ result });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Result int `json:"result"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Result != 42 {
		t.Errorf("result = %d, want 42", data.Result)
	}
}

func TestScheduler_PostTaskWithDelay(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    const before = performance.now();
    const result = await scheduler.postTask(() => 'ok', { delay: 10 });
    const after = performance.now();
    return Response.json({
      result,
      elapsed: after - before,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Result  string  `json:"result"`
		Elapsed float64 `json:"elapsed"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Result != "ok" {
		t.Errorf("result = %q, want 'ok'", data.Result)
	}
	if data.Elapsed < 5 {
		t.Errorf("elapsed = %f, expected >= 5ms (delay: 10ms)", data.Elapsed)
	}
}

func TestScheduler_WaitNoArgs(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    await scheduler.wait();
    return Response.json({ done: true });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Done bool `json:"done"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.Done {
		t.Error("scheduler.wait() with no args should resolve")
	}
}
