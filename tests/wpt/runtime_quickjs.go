//go:build !v8

package wpt

import (
	"github.com/cryguy/worker/v2/internal/core"
	"github.com/cryguy/worker/v2/internal/eventloop"
	"github.com/cryguy/worker/v2/internal/quickjs"
)

func newStandaloneRuntime(cfg core.EngineConfig) (core.JSRuntime, *eventloop.EventLoop, func(), error) {
	return quickjs.NewStandaloneRuntime(cfg)
}
