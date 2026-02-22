package worker

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// mockQueueSender is an in-memory implementation of QueueSender for testing.
type mockQueueSender struct {
	mu       sync.Mutex
	messages []QueueMessageInput
}

func (m *mockQueueSender) Send(body, contentType string) (string, error) {
	if contentType == "" {
		contentType = "json"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, QueueMessageInput{Body: body, ContentType: contentType})
	return fmt.Sprintf("qm_%d", len(m.messages)), nil
}

func (m *mockQueueSender) SendBatch(msgs []QueueMessageInput) ([]string, error) {
	var ids []string
	for _, msg := range msgs {
		id, err := m.Send(msg.Body, msg.ContentType)
		if err != nil {
			return ids, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (m *mockQueueSender) Messages() []QueueMessageInput {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]QueueMessageInput, len(m.messages))
	copy(out, m.messages)
	return out
}

// JS-level queue binding tests.

func queueEnv(t *testing.T, sender QueueSender) *Env {
	t.Helper()
	env := &Env{
		Vars:    make(map[string]string),
		Secrets: make(map[string]string),
	}
	if sender != nil {
		env.Queues = map[string]QueueSender{
			"MY_QUEUE": sender,
		}
	}
	return env
}

func TestQueue_JSSend(t *testing.T) {
	e := newTestEngine(t)
	mock := &mockQueueSender{}

	source := `export default {
  async fetch(request, env) {
    await env.MY_QUEUE.send("hello world", { contentType: "text" });
    return Response.json({ ok: true });
  },
};`

	env := queueEnv(t, mock)
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if !data.OK {
		t.Error("expected ok: true")
	}

	// Verify the message was stored.
	msgs := mock.Messages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Body != "hello world" {
		t.Errorf("body = %q, want %q", msgs[0].Body, "hello world")
	}
	if msgs[0].ContentType != "text" {
		t.Errorf("contentType = %q, want %q", msgs[0].ContentType, "text")
	}
}

func TestQueue_JSSendBatch(t *testing.T) {
	e := newTestEngine(t)
	mock := &mockQueueSender{}

	source := `export default {
  async fetch(request, env) {
    await env.MY_QUEUE.sendBatch([
      { body: "first", contentType: "text" },
      { body: "second", contentType: "text" },
    ]);
    return Response.json({ ok: true });
  },
};`

	env := queueEnv(t, mock)
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	msgs := mock.Messages()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
}

func TestQueue_JSDefaultContentType(t *testing.T) {
	e := newTestEngine(t)
	mock := &mockQueueSender{}

	source := `export default {
  async fetch(request, env) {
    await env.MY_QUEUE.send("no-options");
    return Response.json({ ok: true });
  },
};`

	env := queueEnv(t, mock)
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	msgs := mock.Messages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].ContentType != "json" {
		t.Errorf("default contentType = %q, want %q", msgs[0].ContentType, "json")
	}
}

func TestQueue_JSBindingExists(t *testing.T) {
	e := newTestEngine(t)
	mock := &mockQueueSender{}

	source := `export default {
  async fetch(request, env) {
    const hasSend = typeof env.MY_QUEUE.send === 'function';
    const hasSendBatch = typeof env.MY_QUEUE.sendBatch === 'function';
    return Response.json({ hasSend, hasSendBatch });
  },
};`

	env := queueEnv(t, mock)
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		HasSend      bool `json:"hasSend"`
		HasSendBatch bool `json:"hasSendBatch"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if !data.HasSend {
		t.Error("env.MY_QUEUE.send should be a function")
	}
	if !data.HasSendBatch {
		t.Error("env.MY_QUEUE.sendBatch should be a function")
	}
}

// TestQueue_JSSendStringMessage verifies that Queue.send() works with a plain string body.
func TestQueue_JSSendStringMessage(t *testing.T) {
	e := newTestEngine(t)
	mock := &mockQueueSender{}

	source := `export default {
  async fetch(request, env) {
    await env.MY_QUEUE.send("plain string message", { contentType: "text" });
    return Response.json({ ok: true });
  },
};`

	env := queueEnv(t, mock)
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	msgs := mock.Messages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Body != "plain string message" {
		t.Errorf("body = %q, want %q", msgs[0].Body, "plain string message")
	}
	if msgs[0].ContentType != "text" {
		t.Errorf("contentType = %q, want %q", msgs[0].ContentType, "text")
	}
}

// TestQueue_JSSendJSONObject verifies that Queue.send() works with a JSON object body.
func TestQueue_JSSendJSONObject(t *testing.T) {
	e := newTestEngine(t)
	mock := &mockQueueSender{}

	source := `export default {
  async fetch(request, env) {
    await env.MY_QUEUE.send(JSON.stringify({ action: "process", id: 42 }));
    return Response.json({ ok: true });
  },
};`

	env := queueEnv(t, mock)
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	msgs := mock.Messages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	var body struct {
		Action string `json:"action"`
		ID     int    `json:"id"`
	}
	if err := json.Unmarshal([]byte(msgs[0].Body), &body); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if body.Action != "process" {
		t.Errorf("action = %q, want %q", body.Action, "process")
	}
	if body.ID != 42 {
		t.Errorf("id = %d, want 42", body.ID)
	}
}

// TestQueue_JSSendBatchMultipleMessages verifies sendBatch with varied messages.
func TestQueue_JSSendBatchMultipleMessages(t *testing.T) {
	e := newTestEngine(t)
	mock := &mockQueueSender{}

	source := `export default {
  async fetch(request, env) {
    await env.MY_QUEUE.sendBatch([
      { body: "msg-alpha", contentType: "text" },
      { body: "msg-beta", contentType: "text" },
      { body: "msg-gamma", contentType: "json" },
    ]);
    return Response.json({ ok: true });
  },
};`

	env := queueEnv(t, mock)
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	msgs := mock.Messages()
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if msgs[0].Body != "msg-alpha" {
		t.Errorf("msg[0].body = %q, want %q", msgs[0].Body, "msg-alpha")
	}
	if msgs[1].Body != "msg-beta" {
		t.Errorf("msg[1].body = %q, want %q", msgs[1].Body, "msg-beta")
	}
	if msgs[2].Body != "msg-gamma" {
		t.Errorf("msg[2].body = %q, want %q", msgs[2].Body, "msg-gamma")
	}
}

// TestQueue_JSSendNoArgs verifies that Queue.send() with no arguments rejects.
func TestQueue_JSSendNoArgs(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    try {
      await env.MY_QUEUE.send();
      return Response.json({ rejected: false });
    } catch (e) {
      return Response.json({ rejected: true, message: String(e) });
    }
  },
};`

	env := queueEnv(t, nil)
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Rejected bool   `json:"rejected"`
		Message  string `json:"message"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if !data.Rejected {
		t.Error("send() with no args should reject")
	}
}

// TestQueue_JSSendBatchNoArgs verifies that Queue.sendBatch() with no arguments rejects.
func TestQueue_JSSendBatchNoArgs(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    try {
      await env.MY_QUEUE.sendBatch();
      return Response.json({ rejected: false });
    } catch (e) {
      return Response.json({ rejected: true, message: String(e) });
    }
  },
};`

	env := queueEnv(t, nil)
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Rejected bool   `json:"rejected"`
		Message  string `json:"message"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if !data.Rejected {
		t.Error("sendBatch() with no args should reject")
	}
}

// TestQueue_JSSendBatchNonArray verifies that Queue.sendBatch() with a non-array input handles gracefully.
func TestQueue_JSSendBatchNonArray(t *testing.T) {
	e := newTestEngine(t)
	mock := &mockQueueSender{}

	source := `export default {
  async fetch(request, env) {
    await env.MY_QUEUE.sendBatch("not-an-array");
    return Response.json({ ok: true });
  },
};`

	env := queueEnv(t, mock)
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	// Should succeed but with zero messages since the input is not an array
	msgs := mock.Messages()
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages for non-array input, got %d", len(msgs))
	}
}

// TestQueue_AccessibleFromWorkerEnv verifies the queue binding is accessible
// from env and has the correct shape.
func TestQueue_AccessibleFromWorkerEnv(t *testing.T) {
	e := newTestEngine(t)
	mock := &mockQueueSender{}

	source := `export default {
  async fetch(request, env) {
    const exists = env.MY_QUEUE !== undefined;
    const isObj = typeof env.MY_QUEUE === 'object';
    const hasSend = typeof env.MY_QUEUE.send === 'function';
    const hasSendBatch = typeof env.MY_QUEUE.sendBatch === 'function';
    return Response.json({ exists, isObj, hasSend, hasSendBatch });
  },
};`

	env := queueEnv(t, mock)
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Exists       bool `json:"exists"`
		IsObj        bool `json:"isObj"`
		HasSend      bool `json:"hasSend"`
		HasSendBatch bool `json:"hasSendBatch"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatal(err)
	}
	if !data.Exists {
		t.Error("env.MY_QUEUE should exist")
	}
	if !data.IsObj {
		t.Error("env.MY_QUEUE should be an object")
	}
	if !data.HasSend {
		t.Error("env.MY_QUEUE.send should be a function")
	}
	if !data.HasSendBatch {
		t.Error("env.MY_QUEUE.sendBatch should be a function")
	}
}

// failingQueueSender always returns errors.
type failingQueueSender struct{}

func (f *failingQueueSender) Send(body, contentType string) (string, error) {
	return "", fmt.Errorf("queue send failed")
}

func (f *failingQueueSender) SendBatch(msgs []QueueMessageInput) ([]string, error) {
	return nil, fmt.Errorf("queue sendBatch failed")
}

func TestQueue_SendErrorPath(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    try {
      await env.MY_QUEUE.send("hello");
      return Response.json({ error: false });
    } catch(e) {
      return Response.json({ error: true, message: String(e) });
    }
  },
};`

	env := queueEnv(t, &failingQueueSender{})
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Error   bool   `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.Error {
		t.Error("expected queue.send to reject on error")
	}
	if !strings.Contains(data.Message, "queue send failed") {
		t.Errorf("error message = %q, want to contain 'queue send failed'", data.Message)
	}
}

func TestQueue_SendBatchErrorPath(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    try {
      await env.MY_QUEUE.sendBatch([{ body: "hello" }]);
      return Response.json({ error: false });
    } catch(e) {
      return Response.json({ error: true, message: String(e) });
    }
  },
};`

	env := queueEnv(t, &failingQueueSender{})
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Error   bool   `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.Error {
		t.Error("expected queue.sendBatch to reject on error")
	}
	if !strings.Contains(data.Message, "queue sendBatch failed") {
		t.Errorf("error message = %q, want to contain 'queue sendBatch failed'", data.Message)
	}
}

func TestQueue_ContentTypeDefaults(t *testing.T) {
	e := newTestEngine(t)
	mock := &mockQueueSender{}

	// Test that sending without contentType defaults to "json"
	source := `export default {
  async fetch(request, env) {
    await env.MY_QUEUE.send("hello");
    await env.MY_QUEUE.send("world", { contentType: null });
    await env.MY_QUEUE.send("typed", { contentType: "text" });
    return Response.json({ ok: true });
  },
};`

	env := queueEnv(t, mock)
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	msgs := mock.Messages()
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if msgs[0].ContentType != "json" {
		t.Errorf("msg[0] contentType = %q, want 'json' (default)", msgs[0].ContentType)
	}
	if msgs[1].ContentType != "json" {
		t.Errorf("msg[1] contentType = %q, want 'json' (null defaults to json)", msgs[1].ContentType)
	}
	if msgs[2].ContentType != "text" {
		t.Errorf("msg[2] contentType = %q, want 'text'", msgs[2].ContentType)
	}
}

func TestQueue_SendNoArgs(t *testing.T) {
	e := newTestEngine(t)
	mock := &mockQueueSender{}

	source := `export default {
  async fetch(request, env) {
    try {
      await env.MY_QUEUE.send();
      return Response.json({ error: false });
    } catch(e) {
      return Response.json({ error: true, message: String(e) });
    }
  },
};`

	env := queueEnv(t, mock)
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Error   bool   `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.Error {
		t.Error("expected queue.send() with no args to reject")
	}
}

func TestQueue_SendBatchNoArgs(t *testing.T) {
	e := newTestEngine(t)
	mock := &mockQueueSender{}

	source := `export default {
  async fetch(request, env) {
    try {
      await env.MY_QUEUE.sendBatch();
      return Response.json({ error: false });
    } catch(e) {
      return Response.json({ error: true, message: String(e) });
    }
  },
};`

	env := queueEnv(t, mock)
	r := execJS(t, e, source, env, getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Error   bool   `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.Error {
		t.Error("expected queue.sendBatch() with no args to reject")
	}
}
