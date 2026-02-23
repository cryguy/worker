package worker

import (
	"testing"
	"time"

	"modernc.org/quickjs"
)

func TestEventLoop_NewEventLoop(t *testing.T) {
	el := newEventLoop()
	if el == nil {
		t.Fatal("newEventLoop returned nil")
	}
	if el.timers == nil {
		t.Error("timers map should be initialized")
	}
	if el.nextID != 0 {
		t.Errorf("nextID = %d, want 0", el.nextID)
	}
	if el.hasPending() {
		t.Error("new event loop should have no pending timers")
	}
}

func TestEventLoop_RegisterTimer_Timeout(t *testing.T) {
	el := newEventLoop()

	id1 := el.registerTimer(100*time.Millisecond, false)
	if id1 != 1 {
		t.Errorf("first timer ID = %d, want 1", id1)
	}
	if !el.hasPending() {
		t.Error("should have pending timers after registerTimer")
	}

	id2 := el.registerTimer(200*time.Millisecond, false)
	if id2 != 2 {
		t.Errorf("second timer ID = %d, want 2", id2)
	}
}

func TestEventLoop_RegisterTimer_Interval(t *testing.T) {
	el := newEventLoop()

	id := el.registerTimer(50*time.Millisecond, true)
	if id != 1 {
		t.Errorf("interval ID = %d, want 1", id)
	}

	el.mu.Lock()
	entry := el.timers[id]
	el.mu.Unlock()

	if entry == nil {
		t.Fatal("timer entry not found")
	}
	if entry.interval < 10*time.Millisecond {
		t.Errorf("interval = %v, should be at least 10ms (minimum enforced)", entry.interval)
	}
}

func TestEventLoop_ClearTimer(t *testing.T) {
	el := newEventLoop()

	id := el.registerTimer(100*time.Millisecond, false)
	if !el.hasPending() {
		t.Error("should have pending timer")
	}

	el.clearTimer(id)
	if el.hasPending() {
		t.Error("should not have pending timers after clear")
	}
}

func TestEventLoop_ClearTimer_NonExistent(t *testing.T) {
	el := newEventLoop()
	// Should not panic when clearing a non-existent timer
	el.clearTimer(999)
	if el.hasPending() {
		t.Error("should not have pending timers")
	}
}

func TestEventLoop_Reset(t *testing.T) {
	el := newEventLoop()

	el.registerTimer(100*time.Millisecond, false)
	el.registerTimer(200*time.Millisecond, true)
	el.registerTimer(300*time.Millisecond, false)

	if !el.hasPending() {
		t.Error("should have pending timers")
	}

	el.reset()

	if el.hasPending() {
		t.Error("should not have pending timers after reset")
	}
	if el.nextID != 0 {
		t.Errorf("nextID after reset = %d, want 0", el.nextID)
	}
}

func TestEventLoop_Drain_Empty(t *testing.T) {
	vm, err := quickjs.NewVM()
	if err != nil {
		t.Fatalf("NewVM: %v", err)
	}
	defer vm.Close()

	el := newEventLoop()
	// Drain on empty event loop should return immediately.
	el.drain(vm, time.Now().Add(time.Second))
}

func TestEventLoop_Drain_Timeout(t *testing.T) {
	vm, err := quickjs.NewVM()
	if err != nil {
		t.Fatalf("NewVM: %v", err)
	}
	defer vm.Close()

	el := newEventLoop()

	// Register the timer callbacks JS infrastructure
	if err := setupTimers(vm, el); err != nil {
		t.Fatalf("setupTimers: %v", err)
	}

	// Set a timer far in the future via JS
	if err := evalDiscard(vm, "setTimeout(function(){}, 10000)"); err != nil {
		t.Fatalf("setTimeout: %v", err)
	}

	// Drain with a deadline in the past
	el.drain(vm, time.Now().Add(-1*time.Second))

	// Timer should still be pending (not fired because deadline passed)
	if !el.hasPending() {
		t.Error("timer should still be pending after expired deadline")
	}
}

func TestEventLoop_Drain_FiresCallback(t *testing.T) {
	vm, err := quickjs.NewVM()
	if err != nil {
		t.Fatalf("NewVM: %v", err)
	}
	defer vm.Close()

	el := newEventLoop()

	if err := setupTimers(vm, el); err != nil {
		t.Fatalf("setupTimers: %v", err)
	}

	// Set a global that the callback will modify
	if err := evalDiscard(vm, "globalThis.__timer_fired = false"); err != nil {
		t.Fatalf("init: %v", err)
	}

	if err := evalDiscard(vm, "setTimeout(function() { globalThis.__timer_fired = true; }, 1)"); err != nil {
		t.Fatalf("setTimeout: %v", err)
	}

	// Drain with sufficient deadline
	el.drain(vm, time.Now().Add(5*time.Second))

	result, err := evalBool(vm, "globalThis.__timer_fired")
	if err != nil {
		t.Fatalf("checking result: %v", err)
	}
	if !result {
		t.Error("timer callback should have fired")
	}
}

func TestEventLoop_Drain_IntervalRepeats(t *testing.T) {
	vm, err := quickjs.NewVM()
	if err != nil {
		t.Fatalf("NewVM: %v", err)
	}
	defer vm.Close()

	el := newEventLoop()

	if err := setupTimers(vm, el); err != nil {
		t.Fatalf("setupTimers: %v", err)
	}

	if err := evalDiscard(vm, "globalThis.__count = 0"); err != nil {
		t.Fatalf("init: %v", err)
	}

	if err := evalDiscard(vm, "setInterval(function() { globalThis.__count++; }, 1)"); err != nil {
		t.Fatalf("setInterval: %v", err)
	}

	// Drain for a short period - interval should fire multiple times
	el.drain(vm, time.Now().Add(100*time.Millisecond))

	count, err := evalInt(vm, "globalThis.__count")
	if err != nil {
		t.Fatalf("checking count: %v", err)
	}
	if count < 2 {
		t.Errorf("interval should have fired at least twice, got %d", count)
	}
}

func TestEventLoop_ClearTimer_DuringDrain(t *testing.T) {
	vm, err := quickjs.NewVM()
	if err != nil {
		t.Fatalf("NewVM: %v", err)
	}
	defer vm.Close()

	el := newEventLoop()

	if err := setupTimers(vm, el); err != nil {
		t.Fatalf("setupTimers: %v", err)
	}

	if err := evalDiscard(vm, "var __intervalId = setInterval(function() {}, 1)"); err != nil {
		t.Fatalf("setInterval: %v", err)
	}

	// Clear the timer from another goroutine during drain
	go func() {
		time.Sleep(10 * time.Millisecond)
		el.mu.Lock()
		for id := range el.timers {
			el.mu.Unlock()
			el.clearTimer(id)
			return
		}
		el.mu.Unlock()
	}()

	el.drain(vm, time.Now().Add(200*time.Millisecond))
	// Should complete without hanging
}

func TestEventLoop_IDsIncrement(t *testing.T) {
	el := newEventLoop()

	id1 := el.registerTimer(time.Second, false)
	id2 := el.registerTimer(time.Second, false)
	id3 := el.registerTimer(time.Second, true)

	if id1 != 1 || id2 != 2 || id3 != 3 {
		t.Errorf("IDs should increment: got %d, %d, %d", id1, id2, id3)
	}
}
