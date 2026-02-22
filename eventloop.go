package worker

import (
	"sync"
	"time"

	v8 "github.com/tommie/v8go"
)

// timerEntry represents a pending setTimeout or setInterval callback.
type timerEntry struct {
	callback *v8.Function
	deadline time.Time
	interval time.Duration // 0 for setTimeout, >0 for setInterval
	id       int
	cleared  bool
}

// pendingFetch represents an in-flight HTTP request whose result will be
// delivered to V8 via the event loop when the response arrives.
type pendingFetch struct {
	resultCh <-chan fetchResult
	callback func(fetchResult) // closure that resolves/rejects on V8 thread
}

// eventLoop manages Go-backed timers for setTimeout/setInterval and
// pending fetch requests that need to be resolved on the V8 thread.
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

// setTimeout registers a one-shot timer and returns its ID.
func (el *eventLoop) setTimeout(callback *v8.Function, delay time.Duration) int {
	el.mu.Lock()
	defer el.mu.Unlock()
	el.nextID++
	id := el.nextID
	el.timers[id] = &timerEntry{
		callback: callback,
		deadline: time.Now().Add(delay),
		id:       id,
	}
	return id
}

// setInterval registers a repeating timer and returns its ID.
func (el *eventLoop) setInterval(callback *v8.Function, interval time.Duration) int {
	el.mu.Lock()
	defer el.mu.Unlock()
	el.nextID++
	id := el.nextID
	el.timers[id] = &timerEntry{
		callback: callback,
		deadline: time.Now().Add(interval),
		interval: interval,
		id:       id,
	}
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
// to V8 when the HTTP response arrives.
func (el *eventLoop) addPendingFetch(pf *pendingFetch) {
	el.mu.Lock()
	defer el.mu.Unlock()
	el.pendingFetches = append(el.pendingFetches, pf)
}

// drainPendingFetches does non-blocking reads on all pending fetch channels.
// For each completed fetch, it calls the callback on the V8 thread and removes
// it from the list. Returns true if any fetch was completed.
func (el *eventLoop) drainPendingFetches(ctx *v8.Context) bool {
	el.mu.Lock()
	if len(el.pendingFetches) == 0 {
		el.mu.Unlock()
		return false
	}
	// Snapshot the current list; we'll rebuild it without completed entries.
	pending := el.pendingFetches
	el.mu.Unlock()

	var remaining []*pendingFetch
	didWork := false
	for _, pf := range pending {
		select {
		case result := <-pf.resultCh:
			pf.callback(result)
			ctx.PerformMicrotaskCheckpoint()
			didWork = true
		default:
			remaining = append(remaining, pf)
		}
	}

	el.mu.Lock()
	// Callbacks may have added new pending fetches (via addPendingFetch)
	// during PerformMicrotaskCheckpoint above, so prepend those to remaining.
	el.pendingFetches = append(remaining, el.pendingFetches...)
	el.mu.Unlock()
	return didWork
}

// drain fires all pending timers and resolves pending fetches until none remain
// or the deadline is reached.
// Must be called on the isolate's goroutine (V8 is single-threaded per isolate).
func (el *eventLoop) drain(iso *v8.Isolate, ctx *v8.Context, deadline time.Time) {
	for {
		// Always try to drain pending fetches first.
		if el.drainPendingFetches(ctx) {
			// A fetch completed — loop again to check for new timers/fetches.
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

		// Wait until timer fires or execution deadline, but use short polls
		// if fetches are pending so they can be resolved promptly.
		now := time.Now()
		if next.deadline.After(now) {
			wait := next.deadline.Sub(now)
			if now.Add(wait).After(deadline) {
				// Timer would exceed deadline. If fetches are pending, keep
				// polling until deadline; otherwise return.
				if hasFetches {
					for time.Now().Before(deadline) {
						if el.drainPendingFetches(ctx) {
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
					el.drainPendingFetches(ctx)
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
		if next.interval > 0 {
			next.deadline = time.Now().Add(next.interval)
		} else {
			delete(el.timers, next.id)
		}
		cb := next.callback
		el.mu.Unlock()

		// Call on the isolate's goroutine. Ignore errors from timer callbacks.
		undefinedVal := v8.Undefined(iso)
		_, _ = cb.Call(undefinedVal, undefinedVal)
		ctx.PerformMicrotaskCheckpoint()
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
