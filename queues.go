package worker

import (
	"encoding/json"
	"fmt"

	v8 "github.com/tommie/v8go"
)

// QueueMessageInput is the Go representation of a batch send item.
type QueueMessageInput struct {
	Body        string
	ContentType string
}

// buildQueueBinding creates a JS object with async send/sendBatch methods
// backed by the given QueueSender.
func buildQueueBinding(iso *v8.Isolate, ctx *v8.Context, sender QueueSender) (*v8.Value, error) {
	qObj, err := newJSObject(iso, ctx)
	if err != nil {
		return nil, fmt.Errorf("creating Queue object: %w", err)
	}

	// send(body, options?) -> Promise<void>
	_ = qObj.Set("send", v8.NewFunctionTemplate(iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
		resolver, _ := v8.NewPromiseResolver(ctx)
		args := info.Args()
		if len(args) == 0 {
			errVal, _ := v8.NewValue(iso, "Queue.send requires a body argument")
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}

		body := args[0].String()
		contentType := "json"

		if len(args) > 1 && args[1].IsObject() {
			_ = ctx.Global().Set("__tmp_queue_opts", args[1])
			optsResult, err := ctx.RunScript(`(function() {
				var o = globalThis.__tmp_queue_opts;
				delete globalThis.__tmp_queue_opts;
				return o.contentType !== undefined && o.contentType !== null ? String(o.contentType) : "json";
			})()`, "queue_opts.js")
			if err == nil {
				contentType = optsResult.String()
			}
		}

		if _, err := sender.Send(body, contentType); err != nil {
			errVal, _ := v8.NewValue(iso, err.Error())
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}
		resolver.Resolve(v8.Undefined(iso))
		return resolver.GetPromise().Value
	}).GetFunction(ctx))

	// sendBatch(messages) -> Promise<void>
	_ = qObj.Set("sendBatch", v8.NewFunctionTemplate(iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
		resolver, _ := v8.NewPromiseResolver(ctx)
		args := info.Args()
		if len(args) == 0 {
			errVal, _ := v8.NewValue(iso, "Queue.sendBatch requires a messages argument")
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}

		// Extract messages array via JS JSON serialization.
		_ = ctx.Global().Set("__tmp_queue_batch", args[0])
		batchResult, err := ctx.RunScript(`(function() {
			var msgs = globalThis.__tmp_queue_batch;
			delete globalThis.__tmp_queue_batch;
			if (!Array.isArray(msgs)) return JSON.stringify([]);
			return JSON.stringify(msgs.map(function(m) {
				return {
					body: typeof m.body === 'string' ? m.body : JSON.stringify(m.body),
					contentType: m.contentType || "json"
				};
			}));
		})()`, "queue_batch.js")
		if err != nil {
			errVal, _ := v8.NewValue(iso, "failed to parse batch messages: "+err.Error())
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}

		var inputs []QueueMessageInput
		if err := json.Unmarshal([]byte(batchResult.String()), &inputs); err != nil {
			errVal, _ := v8.NewValue(iso, "failed to unmarshal batch messages: "+err.Error())
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}

		if _, err := sender.SendBatch(inputs); err != nil {
			errVal, _ := v8.NewValue(iso, err.Error())
			resolver.Reject(errVal)
			return resolver.GetPromise().Value
		}
		resolver.Resolve(v8.Undefined(iso))
		return resolver.GetPromise().Value
	}).GetFunction(ctx))

	return qObj.Value, nil
}
