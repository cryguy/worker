package worker

import (
	"fmt"
	"sync"
	"time"

	"modernc.org/quickjs"
)

// timerEntry represents a pending setTimeout or setInterval callback.
// Unlike the v8go version, the callback is NOT stored in Go — it lives
// in globalThis.__timerCallbacks[id] on the JS side. Go only tracks
// scheduling metadata.
type timerEntry struct {
	deadline time.Time
	interval time.Duration // 0 for setTimeout, >0 for setInterval
	id       int
	cleared  bool
}

// fetchResult holds the pre-serialized outcome of an in-flight HTTP fetch.
// The fetch goroutine reads the response body, serializes headers, and encodes
// the body as base64 before sending — so the event loop only passes strings to JS.
type fetchResult struct {
	status      int
	statusText  string
	headersJSON string
	bodyB64     string
	redirected  bool
	finalURL    string
	err         error
}

// pendingFetch represents an in-flight HTTP request whose result will be
// delivered to JS via the event loop when the response arrives.
type pendingFetch struct {
	resultCh <-chan fetchResult
	fetchID  string
}

// eventLoop manages Go-backed timers for setTimeout/setInterval and
// pending fetch requests that need to be resolved on the JS thread.
// Provides real wall-clock delays backed by Go timers.
type eventLoop struct {
	mu             sync.Mutex
	timers         map[int]*timerEntry
	nextID         int
	pendingFetches []*pendingFetch
}

func newEventLoop() *eventLoop {
	return &eventLoop{
		timers: make(map[int]*timerEntry),
	}
}

// registerTimer creates a timer entry and returns its ID.
// The actual JS callback is stored in globalThis.__timerCallbacks[id].
func (el *eventLoop) registerTimer(delay time.Duration, isInterval bool) int {
	el.mu.Lock()
	defer el.mu.Unlock()
	el.nextID++
	id := el.nextID
	entry := &timerEntry{
		deadline: time.Now().Add(delay),
		id:       id,
	}
	if isInterval {
		if delay < 10*time.Millisecond {
			delay = 10 * time.Millisecond // minimum interval
		}
		entry.interval = delay
	}
	el.timers[id] = entry
	return id
}

// clearTimer cancels a timer by ID.
func (el *eventLoop) clearTimer(id int) {
	el.mu.Lock()
	defer el.mu.Unlock()
	if t, ok := el.timers[id]; ok {
		t.cleared = true
		delete(el.timers, id)
	}
}

// addPendingFetch registers a pending fetch whose result will be delivered
// to JS when the HTTP response arrives.
func (el *eventLoop) addPendingFetch(pf *pendingFetch) {
	el.mu.Lock()
	defer el.mu.Unlock()
	el.pendingFetches = append(el.pendingFetches, pf)
}

// drainPendingFetches does non-blocking reads on all pending fetch channels.
// For each completed fetch, it resolves/rejects via JS globals and removes
// it from the list. Returns true if any fetch was completed.
func (el *eventLoop) drainPendingFetches(vm *quickjs.VM) bool {
	el.mu.Lock()
	if len(el.pendingFetches) == 0 {
		el.mu.Unlock()
		return false
	}
	// Snapshot the current list; we'll rebuild it without completed entries.
	pending := el.pendingFetches
	el.pendingFetches = nil
	el.mu.Unlock()

	var remaining []*pendingFetch
	didWork := false
	for _, pf := range pending {
		select {
		case result := <-pf.resultCh:
			if result.err != nil {
				js := fmt.Sprintf(`globalThis.__fetchReject(%q, %q)`,
					pf.fetchID, result.err.Error())
				v, err := vm.EvalValue(js, quickjs.EvalGlobal)
				if err == nil {
					v.Free()
				}
			} else {
				js := fmt.Sprintf(`globalThis.__fetchResolve(%q, %d, %q, %q, %q, %v, %q)`,
					pf.fetchID, result.status, result.statusText,
					result.headersJSON, result.bodyB64,
					result.redirected, result.finalURL)
				v, err := vm.EvalValue(js, quickjs.EvalGlobal)
				if err == nil {
					v.Free()
				}
			}
			// Microtask checkpoint after each fetch resolution.
			executePendingJobs(vm)
			didWork = true
		default:
			remaining = append(remaining, pf)
		}
	}

	el.mu.Lock()
	// Callbacks may have added new pending fetches during resolution,
	// so prepend those to remaining.
	el.pendingFetches = append(remaining, el.pendingFetches...)
	el.mu.Unlock()
	return didWork
}

// fireTimer fires a timer callback by invoking the JS-side callback map.
func (el *eventLoop) fireTimer(vm *quickjs.VM, id int) {
	js := fmt.Sprintf(`(function() {
		var entry = globalThis.__timerCallbacks[%d];
		if (!entry) return;
		if (!entry.interval) delete globalThis.__timerCallbacks[%d];
		entry.fn.apply(null, entry.args || []);
	})()`, id, id)
	v, err := vm.EvalValue(js, quickjs.EvalGlobal)
	if err == nil {
		v.Free()
	}
}

// drain fires all pending timers and resolves pending fetches until none remain
// or the deadline is reached.
// Must be called on the VM's goroutine (QuickJS is single-threaded per VM).
func (el *eventLoop) drain(vm *quickjs.VM, deadline time.Time) {
	for {
		// Always try to drain pending fetches first.
		if el.drainPendingFetches(vm) {
			continue
		}

		el.mu.Lock()
		hasTimers := len(el.timers) > 0
		hasFetches := len(el.pendingFetches) > 0
		el.mu.Unlock()

		if !hasTimers && !hasFetches {
			return
		}

		// Find the next timer to fire.
		el.mu.Lock()
		var next *timerEntry
		for _, t := range el.timers {
			if t.cleared {
				continue
			}
			if next == nil || t.deadline.Before(next.deadline) {
				next = t
			}
		}
		el.mu.Unlock()

		if next == nil && !hasFetches {
			return
		}

		if next == nil && hasFetches {
			// No timers, but fetches are pending — poll with short sleep.
			if time.Now().After(deadline) {
				return
			}
			time.Sleep(1 * time.Millisecond)
			continue
		}

		// Wait until timer fires or execution deadline.
		now := time.Now()
		if next.deadline.After(now) {
			wait := next.deadline.Sub(now)
			if now.Add(wait).After(deadline) {
				// Timer would exceed deadline. If fetches are pending, keep
				// polling until deadline; otherwise return.
				if hasFetches {
					for time.Now().Before(deadline) {
						if el.drainPendingFetches(vm) {
							break
						}
						time.Sleep(1 * time.Millisecond)
					}
				}
				return
			}
			if hasFetches {
				// Poll in short intervals until the timer fires, draining
				// fetches as they complete.
				timerDeadline := now.Add(wait)
				for time.Now().Before(timerDeadline) {
					el.drainPendingFetches(vm)
					remaining := time.Until(timerDeadline)
					if remaining <= 0 {
						break
					}
					if remaining > 1*time.Millisecond {
						remaining = 1 * time.Millisecond
					}
					time.Sleep(remaining)
				}
			} else {
				time.Sleep(wait)
			}
		}

		if time.Now().After(deadline) {
			return
		}

		// Fire the callback.
		el.mu.Lock()
		if next.cleared {
			el.mu.Unlock()
			continue
		}
		timerID := next.id
		if next.interval > 0 {
			next.deadline = time.Now().Add(next.interval)
		} else {
			delete(el.timers, next.id)
		}
		el.mu.Unlock()

		el.fireTimer(vm, timerID)
		// Microtask checkpoint after timer callback.
		executePendingJobs(vm)
	}
}

// hasPending returns true if there are any active timers or pending fetches.
func (el *eventLoop) hasPending() bool {
	el.mu.Lock()
	defer el.mu.Unlock()
	return len(el.timers) > 0 || len(el.pendingFetches) > 0
}

// reset clears all timers and pending fetches. Called when a worker is returned to the pool.
func (el *eventLoop) reset() {
	el.mu.Lock()
	defer el.mu.Unlock()
	el.timers = make(map[int]*timerEntry)
	el.nextID = 0
	el.pendingFetches = nil
}
