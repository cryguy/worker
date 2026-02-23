package worker

import (
	"encoding/json"
	"fmt"

	"modernc.org/quickjs"
)

// QueueMessageInput is the Go representation of a batch send item.
type QueueMessageInput struct {
	Body        string
	ContentType string
}

// setupQueues registers global Go functions for Queue operations.
func setupQueues(vm *quickjs.VM, el *eventLoop) error {
	// __queue_send(reqIDStr, bindingName, body, contentType) -> "" or error
	err := registerGoFunc(vm, "__queue_send", func(reqIDStr, bindingName, body, contentType string) (string, error) {
		reqID := parseReqID(reqIDStr)
		state := getRequestState(reqID)
		if state == nil || state.env == nil || state.env.Queues == nil {
			return "", fmt.Errorf("Queues not available")
		}
		sender, ok := state.env.Queues[bindingName]
		if !ok {
			return "", fmt.Errorf("Queue binding %q not found", bindingName)
		}

		if _, err := sender.Send(body, contentType); err != nil {
			return "", err
		}
		return "", nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __queue_send: %w", err)
	}

	// __queue_send_batch(reqIDStr, bindingName, messagesJSON) -> "" or error
	err = registerGoFunc(vm, "__queue_send_batch", func(reqIDStr, bindingName, messagesJSON string) (string, error) {
		reqID := parseReqID(reqIDStr)
		state := getRequestState(reqID)
		if state == nil || state.env == nil || state.env.Queues == nil {
			return "", fmt.Errorf("Queues not available")
		}
		sender, ok := state.env.Queues[bindingName]
		if !ok {
			return "", fmt.Errorf("Queue binding %q not found", bindingName)
		}

		var inputs []QueueMessageInput
		if err := json.Unmarshal([]byte(messagesJSON), &inputs); err != nil {
			return "", fmt.Errorf("invalid messages JSON: %w", err)
		}

		if _, err := sender.SendBatch(inputs); err != nil {
			return "", err
		}
		return "", nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __queue_send_batch: %w", err)
	}

	return nil
}
