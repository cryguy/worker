package webapi

import "github.com/cryguy/worker/internal/core"

// GetReqIDFromJS reads the __requestID global and parses it to uint64.
func GetReqIDFromJS(rt core.JSRuntime) uint64 {
	s, err := rt.EvalString("String(globalThis.__requestID || '')")
	if err != nil {
		return 0
	}
	return core.ParseReqID(s)
}
