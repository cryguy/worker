//go:build !v8

package main

import (
	"github.com/cryguy/worker/v2/internal/core"
	"github.com/cryguy/worker/v2/internal/eventloop"
	"github.com/cryguy/worker/v2/internal/quickjs"
)

const engineName = "QuickJS"

func newRuntime(cfg core.EngineConfig) (core.JSRuntime, *eventloop.EventLoop, func(), error) {
	return quickjs.NewStandaloneRuntime(cfg)
}
