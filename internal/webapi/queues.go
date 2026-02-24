package webapi

import (
	"encoding/json"
	"fmt"

	"github.com/cryguy/worker/internal/core"
	"github.com/cryguy/worker/internal/eventloop"
)

// SetupQueues registers global Go functions for Queue operations.
func SetupQueues(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	// __queue_send(reqIDStr, bindingName, body, contentType) -> "" or error
	if err := rt.RegisterFunc("__queue_send", func(reqIDStr, bindingName, body, contentType string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil || state.Env == nil || state.Env.Queues == nil {
			return "", fmt.Errorf("Queues not available")
		}
		sender, ok := state.Env.Queues[bindingName]
		if !ok {
			return "", fmt.Errorf("Queue binding %q not found", bindingName)
		}

		if _, err := sender.Send(body, contentType); err != nil {
			return "", err
		}
		return "", nil
	}); err != nil {
		return fmt.Errorf("registering __queue_send: %w", err)
	}

	// __queue_send_batch(reqIDStr, bindingName, messagesJSON) -> "" or error
	if err := rt.RegisterFunc("__queue_send_batch", func(reqIDStr, bindingName, messagesJSON string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil || state.Env == nil || state.Env.Queues == nil {
			return "", fmt.Errorf("Queues not available")
		}
		sender, ok := state.Env.Queues[bindingName]
		if !ok {
			return "", fmt.Errorf("Queue binding %q not found", bindingName)
		}

		var inputs []core.QueueMessageInput
		if err := json.Unmarshal([]byte(messagesJSON), &inputs); err != nil {
			return "", fmt.Errorf("invalid messages JSON: %w", err)
		}

		if _, err := sender.SendBatch(inputs); err != nil {
			return "", err
		}
		return "", nil
	}); err != nil {
		return fmt.Errorf("registering __queue_send_batch: %w", err)
	}

	// Define the __makeQueue factory function.
	queueFactoryJS := `
globalThis.__makeQueue = function(bindingName) {
	return {
		send: function(body, opts) {
			if (arguments.length === 0) {
				return Promise.reject(new Error("send requires at least 1 argument"));
			}
			var reqID = String(globalThis.__requestID);
			var bodyStr = typeof body === "string" ? body : JSON.stringify(body);
			var contentType = (opts && opts.contentType) || "json";
			return new Promise(function(resolve, reject) {
				try {
					var err = __queue_send(reqID, bindingName, bodyStr, contentType);
					if (err) {
						reject(new Error(err));
					} else {
						resolve();
					}
				} catch(e) {
					reject(e);
				}
			});
		},
		sendBatch: function(messages) {
			if (arguments.length === 0) {
				return Promise.reject(new TypeError("sendBatch requires an array argument"));
			}
			if (!Array.isArray(messages)) {
				return Promise.resolve();
			}
			var reqID = String(globalThis.__requestID);
			var formatted = messages.map(function(m) {
				return {
					body: typeof m.body === "string" ? m.body : JSON.stringify(m.body),
					contentType: m.contentType || "json"
				};
			});
			return new Promise(function(resolve, reject) {
				try {
					var err = __queue_send_batch(reqID, bindingName, JSON.stringify(formatted));
					if (err) {
						reject(new Error(err));
					} else {
						resolve();
					}
				} catch(e) {
					reject(e);
				}
			});
		}
	};
};
`
	if err := rt.Eval(queueFactoryJS); err != nil {
		return fmt.Errorf("evaluating Queue factory JS: %w", err)
	}

	return nil
}
