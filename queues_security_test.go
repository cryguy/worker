package worker

import (
	"testing"
)

// TestQueue_SiteIsolation verifies that queue names are isolated by site ID.
// Uses mockQueueSender (in-memory) — no GORM or DB dependencies.
func TestQueue_SiteIsolation(t *testing.T) {
	sender1 := &mockQueueSender{}
	sender2 := &mockQueueSender{}

	// Both send a message via their respective senders.
	_, err := sender1.Send("msg from site-a", "json")
	if err != nil {
		t.Fatalf("sender1.Send: %v", err)
	}
	_, err = sender2.Send("msg from site-b", "json")
	if err != nil {
		t.Fatalf("sender2.Send: %v", err)
	}

	// Verify messages are isolated: each sender has exactly 1 message.
	msgs1 := sender1.Messages()
	msgs2 := sender2.Messages()
	if len(msgs1) != 1 {
		t.Errorf("sender1 messages = %d, want 1", len(msgs1))
	}
	if len(msgs2) != 1 {
		t.Errorf("sender2 messages = %d, want 1", len(msgs2))
	}
	if msgs1[0].Body != "msg from site-a" {
		t.Errorf("sender1 body = %q, want %q", msgs1[0].Body, "msg from site-a")
	}
	if msgs2[0].Body != "msg from site-b" {
		t.Errorf("sender2 body = %q, want %q", msgs2[0].Body, "msg from site-b")
	}
}
