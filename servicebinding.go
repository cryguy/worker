package worker

import (
	"encoding/json"
	"fmt"

	"modernc.org/quickjs"
)

// ServiceBindingConfig identifies the target worker for a service binding.
type ServiceBindingConfig struct {
	TargetSiteID    string
	TargetDeployKey string
}

// ServiceBindingBridge provides Go methods that back the service binding JS bindings.
type ServiceBindingBridge struct {
	Dispatcher WorkerDispatcher
	Env        *Env
}

// Fetch calls the target worker's fetch handler with the given request.
// The target worker receives its own environment (not the caller's).
func (sb *ServiceBindingBridge) Fetch(config ServiceBindingConfig, req *WorkerRequest) (*WorkerResponse, error) {
	// Provide a minimal env for the target worker. The target must never
	// receive the caller's env (secret isolation), but Execute requires
	// a non-nil Env.
	targetEnv := &Env{
		Vars:    make(map[string]string),
		Secrets: make(map[string]string),
	}
	result := sb.Dispatcher.Execute(config.TargetSiteID, config.TargetDeployKey, targetEnv, req)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.Response == nil {
		return nil, fmt.Errorf("target worker returned no response")
	}
	return result.Response, nil
}

// setupServiceBindings registers global Go functions for service binding operations.
func setupServiceBindings(vm *quickjs.VM, el *eventLoop) error {
	// __sb_fetch(reqIDStr, bindingName, reqJSON) -> JSON response or error
	err := registerGoFunc(vm, "__sb_fetch", func(reqIDStr, bindingName, reqJSON string) (string, error) {
		reqID := parseReqID(reqIDStr)
		state := getRequestState(reqID)
		if state == nil || state.env == nil || state.env.ServiceBindings == nil || state.env.Dispatcher == nil {
			return "", fmt.Errorf("ServiceBindings not available")
		}
		config, ok := state.env.ServiceBindings[bindingName]
		if !ok {
			return "", fmt.Errorf("ServiceBinding %q not found", bindingName)
		}

		var reqData struct {
			URL     string            `json:"url"`
			Method  string            `json:"method"`
			Headers map[string]string `json:"headers"`
			Body    *string           `json:"body"`
		}
		if err := json.Unmarshal([]byte(reqJSON), &reqData); err != nil {
			return "", fmt.Errorf("invalid request JSON: %w", err)
		}

		workerReq := &WorkerRequest{
			Method:  reqData.Method,
			URL:     reqData.URL,
			Headers: reqData.Headers,
		}
		if reqData.Body != nil {
			workerReq.Body = []byte(*reqData.Body)
		}

		bridge := &ServiceBindingBridge{
			Dispatcher: state.env.Dispatcher,
			Env:        state.env,
		}

		resp, err := bridge.Fetch(config, workerReq)
		if err != nil {
			return "", err
		}

		// Build response JSON
		respJSON := map[string]interface{}{
			"status":  resp.StatusCode,
			"headers": resp.Headers,
			"body":    string(resp.Body),
		}
		data, _ := json.Marshal(respJSON)
		return string(data), nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __sb_fetch: %w", err)
	}

	return nil
}
