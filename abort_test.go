package worker

import (
	"encoding/json"
	"testing"
)

func TestAbort_ControllerAbortSetsSignal(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const controller = new AbortController();
    const before = controller.signal.aborted;
    controller.abort();
    const after = controller.signal.aborted;
    return Response.json({ before, after });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Before bool `json:"before"`
		After  bool `json:"after"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if data.Before {
		t.Error("signal.aborted should be false before abort()")
	}
	if !data.After {
		t.Error("signal.aborted should be true after abort()")
	}
}

func TestAbort_ListenerFires(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const controller = new AbortController();
    let fired = false;
    controller.signal.addEventListener('abort', () => { fired = true; });
    controller.abort();
    return Response.json({ fired });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Fired bool `json:"fired"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !data.Fired {
		t.Error("abort event listener should have fired")
	}
}

func TestAbort_AbortReason(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const controller = new AbortController();
    controller.abort("custom reason");
    return Response.json({
      reason: controller.signal.reason,
      aborted: controller.signal.aborted,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Reason  string `json:"reason"`
		Aborted bool   `json:"aborted"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if data.Reason != "custom reason" {
		t.Errorf("reason = %q, want 'custom reason'", data.Reason)
	}
	if !data.Aborted {
		t.Error("signal should be aborted")
	}
}

func TestAbort_SignalAbortStatic(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const signal = AbortSignal.abort("pre-aborted");
    return Response.json({
      aborted: signal.aborted,
      reason: signal.reason,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Aborted bool   `json:"aborted"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !data.Aborted {
		t.Error("AbortSignal.abort() should return aborted signal")
	}
	if data.Reason != "pre-aborted" {
		t.Errorf("reason = %q", data.Reason)
	}
}

func TestAbort_ThrowIfAborted(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const controller = new AbortController();
    controller.abort("stopped");
    try {
      controller.signal.throwIfAborted();
      return new Response("should not reach");
    } catch(e) {
      return Response.json({ caught: true, reason: String(e) });
    }
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Caught bool `json:"caught"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !data.Caught {
		t.Error("throwIfAborted should throw when signal is aborted")
	}
}

func TestAbort_EventTargetBasics(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const target = new EventTarget();
    let count = 0;
    const handler = () => { count++; };
    target.addEventListener('test', handler);
    target.dispatchEvent(new Event('test'));
    target.dispatchEvent(new Event('test'));
    target.removeEventListener('test', handler);
    target.dispatchEvent(new Event('test'));
    return Response.json({ count });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if data.Count != 2 {
		t.Errorf("count = %d, want 2 (listener removed before 3rd dispatch)", data.Count)
	}
}

func TestAbort_DOMException(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const err = new DOMException("test message", "TestError");
    return Response.json({
      message: err.message,
      name: err.name,
      isError: err instanceof Error,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Message string `json:"message"`
		Name    string `json:"name"`
		IsError bool   `json:"isError"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if data.Message != "test message" {
		t.Errorf("message = %q", data.Message)
	}
	if data.Name != "TestError" {
		t.Errorf("name = %q", data.Name)
	}
	if !data.IsError {
		t.Error("DOMException should extend Error")
	}
}

func TestAbort_EventOnceListener(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const target = new EventTarget();
    let count = 0;
    target.addEventListener('test', () => { count++; }, { once: true });
    target.dispatchEvent(new Event('test'));
    target.dispatchEvent(new Event('test'));
    target.dispatchEvent(new Event('test'));
    return Response.json({ count });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Count != 1 {
		t.Errorf("once listener count = %d, want 1", data.Count)
	}
}

func TestAbort_EventPreventDefault(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const event = new Event('test', { cancelable: true });
    const beforePD = event.defaultPrevented;
    event.preventDefault();
    const afterPD = event.defaultPrevented;

    const nonCancelable = new Event('test2');
    nonCancelable.preventDefault();
    const ncPD = nonCancelable.defaultPrevented;

    return Response.json({ beforePD, afterPD, ncPD });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		BeforePD bool `json:"beforePD"`
		AfterPD  bool `json:"afterPD"`
		NcPD     bool `json:"ncPD"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.BeforePD {
		t.Error("defaultPrevented should be false before preventDefault()")
	}
	if !data.AfterPD {
		t.Error("defaultPrevented should be true after preventDefault() on cancelable event")
	}
	if data.NcPD {
		t.Error("defaultPrevented should stay false on non-cancelable event")
	}
}

func TestAbort_SignalAbortDefaultReason(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const signal = AbortSignal.abort();
    return Response.json({
      aborted: signal.aborted,
      isDOMException: signal.reason instanceof DOMException,
      reasonName: signal.reason ? signal.reason.name : "",
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Aborted        bool   `json:"aborted"`
		IsDOMException bool   `json:"isDOMException"`
		ReasonName     string `json:"reasonName"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if !data.Aborted {
		t.Error("AbortSignal.abort() should be aborted")
	}
	if !data.IsDOMException {
		t.Error("default reason should be a DOMException")
	}
	if data.ReasonName != "AbortError" {
		t.Errorf("reason name = %q, want AbortError", data.ReasonName)
	}
}

func TestAbort_DoubleAbortIsNoop(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const controller = new AbortController();
    let count = 0;
    controller.signal.addEventListener('abort', () => { count++; });
    controller.abort("first");
    controller.abort("second");
    return Response.json({
      count,
      reason: controller.signal.reason,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Count  int    `json:"count"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Count != 1 {
		t.Errorf("abort event should only fire once, count = %d", data.Count)
	}
	if data.Reason != "first" {
		t.Errorf("reason should be first abort reason, got %q", data.Reason)
	}
}

func TestAbort_DispatchEventReturnValue(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const target = new EventTarget();
    const event1 = new Event('test');
    const result1 = target.dispatchEvent(event1);

    const target2 = new EventTarget();
    const event2 = new Event('test', { cancelable: true });
    target2.addEventListener('test', (e) => { e.preventDefault(); });
    const result2 = target2.dispatchEvent(event2);

    return Response.json({ result1, result2 });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Result1 bool `json:"result1"`
		Result2 bool `json:"result2"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if !data.Result1 {
		t.Error("dispatchEvent should return true when not prevented")
	}
	if data.Result2 {
		t.Error("dispatchEvent should return false when preventDefault called")
	}
}

func TestAbort_SignalTimeout(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    const signal = AbortSignal.timeout(10);
    const beforeAborted = signal.aborted;
    // Wait long enough for the timeout to fire.
    await new Promise(r => setTimeout(r, 50));
    return Response.json({
      beforeAborted,
      afterAborted: signal.aborted,
      reasonName: signal.reason ? signal.reason.name : "",
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		BeforeAborted bool   `json:"beforeAborted"`
		AfterAborted  bool   `json:"afterAborted"`
		ReasonName    string `json:"reasonName"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.BeforeAborted {
		t.Error("signal should not be aborted before timeout fires")
	}
	if !data.AfterAborted {
		t.Error("signal should be aborted after timeout fires")
	}
	if data.ReasonName != "TimeoutError" {
		t.Errorf("reason name = %q, want TimeoutError", data.ReasonName)
	}
}

func TestAbort_SignalTimeoutListenerFires(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    const signal = AbortSignal.timeout(5);
    let fired = false;
    signal.addEventListener('abort', () => { fired = true; });
    await new Promise(r => setTimeout(r, 50));
    return Response.json({ fired });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Fired bool `json:"fired"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if !data.Fired {
		t.Error("abort listener should fire after timeout")
	}
}

// ---------------------------------------------------------------------------
// Event spec compliance tests
// ---------------------------------------------------------------------------

func TestEvent_Composed(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const composedTrue = new Event('test', { composed: true }).composed;
    const composedDefault = new Event('test').composed;
    return Response.json({ composedTrue, composedDefault });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		ComposedTrue    bool `json:"composedTrue"`
		ComposedDefault bool `json:"composedDefault"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if !data.ComposedTrue {
		t.Error("new Event('test', {composed: true}).composed should be true")
	}
	if data.ComposedDefault {
		t.Error("new Event('test').composed should default to false")
	}
}

func TestEvent_EventPhase(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const phase = new Event('test').eventPhase;
    return Response.json({ phase });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Phase int `json:"phase"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Phase != 0 {
		t.Errorf("eventPhase = %d, want 0 (NONE)", data.Phase)
	}
}

func TestEvent_IsTrusted(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const trusted = new Event('test').isTrusted;
    return Response.json({ trusted });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Trusted bool `json:"trusted"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Trusted {
		t.Error("new Event('test').isTrusted should be false")
	}
}

func TestEvent_StaticConstants(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    return Response.json({
      none: Event.NONE,
      capturing: Event.CAPTURING_PHASE,
      atTarget: Event.AT_TARGET,
      bubbling: Event.BUBBLING_PHASE,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		None      int `json:"none"`
		Capturing int `json:"capturing"`
		AtTarget  int `json:"atTarget"`
		Bubbling  int `json:"bubbling"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.None != 0 {
		t.Errorf("Event.NONE = %d, want 0", data.None)
	}
	if data.Capturing != 1 {
		t.Errorf("Event.CAPTURING_PHASE = %d, want 1", data.Capturing)
	}
	if data.AtTarget != 2 {
		t.Errorf("Event.AT_TARGET = %d, want 2", data.AtTarget)
	}
	if data.Bubbling != 3 {
		t.Errorf("Event.BUBBLING_PHASE = %d, want 3", data.Bubbling)
	}
}

func TestEvent_ComposedPath(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const target = new EventTarget();
    let path = null;
    target.addEventListener('test', (e) => {
      path = e.composedPath();
    });
    target.dispatchEvent(new Event('test'));
    return Response.json({
      isArray: Array.isArray(path),
      length: path ? path.length : 0,
      containsTarget: path ? path[0] === target : false,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		IsArray        bool `json:"isArray"`
		Length         int  `json:"length"`
		ContainsTarget bool `json:"containsTarget"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if !data.IsArray {
		t.Error("composedPath() should return an array")
	}
	if data.Length != 1 {
		t.Errorf("composedPath() length = %d, want 1", data.Length)
	}
	if !data.ContainsTarget {
		t.Error("composedPath() should contain the target")
	}
}

func TestEvent_SymbolToStringTag(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const tag = Object.prototype.toString.call(new Event('test'));
    return Response.json({ tag });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Tag string `json:"tag"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Tag != "[object Event]" {
		t.Errorf("tag = %q, want '[object Event]'", data.Tag)
	}
}

// ---------------------------------------------------------------------------
// EventTarget spec compliance tests
// ---------------------------------------------------------------------------

func TestEventTarget_AddEventListenerWithSignal(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const et = new EventTarget();
    const ac = new AbortController();
    let called = 0;
    et.addEventListener('test', () => called++, { signal: ac.signal });
    et.dispatchEvent(new Event('test'));
    ac.abort();
    et.dispatchEvent(new Event('test'));
    return Response.json({ called });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Called int `json:"called"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Called != 1 {
		t.Errorf("called = %d, want 1 (listener should be removed after signal aborts)", data.Called)
	}
}

func TestEventTarget_SymbolToStringTag(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const tag = Object.prototype.toString.call(new EventTarget());
    return Response.json({ tag });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Tag string `json:"tag"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Tag != "[object EventTarget]" {
		t.Errorf("tag = %q, want '[object EventTarget]'", data.Tag)
	}
}

// ---------------------------------------------------------------------------
// AbortSignal spec compliance tests
// ---------------------------------------------------------------------------

func TestAbortSignal_ReadonlyAborted(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const ac = new AbortController();
    const before = ac.signal.aborted;
    ac.abort();
    const after = ac.signal.aborted;
    return Response.json({ before, after });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Before bool `json:"before"`
		After  bool `json:"after"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Before {
		t.Error("signal.aborted should be false before abort()")
	}
	if !data.After {
		t.Error("signal.aborted should be true after abort()")
	}
}

func TestAbortSignal_SymbolToStringTag(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const ac = new AbortController();
    const tag = Object.prototype.toString.call(ac.signal);
    return Response.json({ tag });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Tag string `json:"tag"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Tag != "[object AbortSignal]" {
		t.Errorf("tag = %q, want '[object AbortSignal]'", data.Tag)
	}
}

func TestAbortController_SymbolToStringTag(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const tag = Object.prototype.toString.call(new AbortController());
    return Response.json({ tag });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Tag string `json:"tag"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Tag != "[object AbortController]" {
		t.Errorf("tag = %q, want '[object AbortController]'", data.Tag)
	}
}

// ---------------------------------------------------------------------------
// DOMException spec compliance tests
// ---------------------------------------------------------------------------

func TestDOMException_ErrorCodes(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const abort = new DOMException('msg', 'AbortError');
    const timeout = new DOMException('msg', 'TimeoutError');
    const notFound = new DOMException('msg', 'NotFoundError');
    const unknown = new DOMException('msg', 'CustomError');
    return Response.json({
      abortCode: abort.code,
      timeoutCode: timeout.code,
      notFoundCode: notFound.code,
      unknownCode: unknown.code,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		AbortCode    int `json:"abortCode"`
		TimeoutCode  int `json:"timeoutCode"`
		NotFoundCode int `json:"notFoundCode"`
		UnknownCode  int `json:"unknownCode"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.AbortCode != 20 {
		t.Errorf("AbortError.code = %d, want 20", data.AbortCode)
	}
	if data.TimeoutCode != 23 {
		t.Errorf("TimeoutError.code = %d, want 23", data.TimeoutCode)
	}
	if data.NotFoundCode != 8 {
		t.Errorf("NotFoundError.code = %d, want 8", data.NotFoundCode)
	}
	if data.UnknownCode != 0 {
		t.Errorf("CustomError.code = %d, want 0", data.UnknownCode)
	}
}

func TestDOMException_SymbolToStringTag(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const tag = Object.prototype.toString.call(new DOMException('msg', 'TestError'));
    return Response.json({ tag });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Tag string `json:"tag"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Tag != "[object DOMException]" {
		t.Errorf("tag = %q, want '[object DOMException]'", data.Tag)
	}
}

func TestCustomEvent_DefaultDetail(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const event = new CustomEvent("test");
    return Response.json({ detail: event.detail, type: event.type });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Detail interface{} `json:"detail"`
		Type   string      `json:"type"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Detail != nil {
		t.Errorf("detail should default to null, got %v", data.Detail)
	}
	if data.Type != "test" {
		t.Errorf("type = %q, want 'test'", data.Type)
	}
}

func TestCustomEvent_StringDetail(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const event = new CustomEvent("test", { detail: "hello" });
    return Response.json({ detail: event.detail });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Detail != "hello" {
		t.Errorf("detail = %q, want 'hello'", data.Detail)
	}
}

func TestCustomEvent_ObjectDetail(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const event = new CustomEvent("test", { detail: { foo: 42 } });
    return Response.json({ foo: event.detail.foo });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Foo int `json:"foo"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Foo != 42 {
		t.Errorf("detail.foo = %d, want 42", data.Foo)
	}
}

func TestCustomEvent_ExplicitNullDetail(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const event = new CustomEvent("test", { detail: null });
    return Response.json({ detail: event.detail, isNull: event.detail === null });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Detail interface{} `json:"detail"`
		IsNull bool        `json:"isNull"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.IsNull {
		t.Error("explicit null detail should remain null")
	}
}

func TestCustomEvent_FalsyZeroDetail(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const event = new CustomEvent("test", { detail: 0 });
    return Response.json({ detail: event.detail });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Detail float64 `json:"detail"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Detail != 0 {
		t.Errorf("detail = %v, want 0", data.Detail)
	}
}

func TestCustomEvent_InheritsEventOptions(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const event = new CustomEvent("test", { bubbles: true, detail: "x" });
    return Response.json({ bubbles: event.bubbles, detail: event.detail, type: event.type });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Bubbles bool   `json:"bubbles"`
		Detail  string `json:"detail"`
		Type    string `json:"type"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.Bubbles {
		t.Error("bubbles should be true")
	}
	if data.Detail != "x" {
		t.Errorf("detail = %q, want 'x'", data.Detail)
	}
	if data.Type != "test" {
		t.Errorf("type = %q, want 'test'", data.Type)
	}
}

func TestCustomEvent_InstanceOf(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const event = new CustomEvent("test", { detail: "y" });
    return Response.json({
      isEvent: event instanceof Event,
      isCustomEvent: event instanceof CustomEvent,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		IsEvent       bool `json:"isEvent"`
		IsCustomEvent bool `json:"isCustomEvent"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.IsEvent {
		t.Error("CustomEvent instance should satisfy instanceof Event")
	}
	if !data.IsCustomEvent {
		t.Error("CustomEvent instance should satisfy instanceof CustomEvent")
	}
}

func TestCustomEvent_DispatchAndDetailAccess(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const target = new EventTarget();
    let capturedDetail = undefined;
    target.addEventListener("custom", (e) => { capturedDetail = e.detail; });
    target.dispatchEvent(new CustomEvent("custom", { detail: { value: 99 } }));
    return Response.json({ capturedDetail });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		CapturedDetail struct {
			Value int `json:"value"`
		} `json:"capturedDetail"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.CapturedDetail.Value != 99 {
		t.Errorf("capturedDetail.value = %d, want 99", data.CapturedDetail.Value)
	}
}
