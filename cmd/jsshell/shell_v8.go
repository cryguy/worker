//go:build v8

package main

import (
	"github.com/cryguy/worker/v2/internal/core"
	"github.com/cryguy/worker/v2/internal/eventloop"
	"github.com/cryguy/worker/v2/internal/v8engine"
)

const engineName = "V8"

func newRuntime(cfg core.EngineConfig) (core.JSRuntime, *eventloop.EventLoop, func(), error) {
	return v8engine.NewStandaloneRuntime(cfg)
}
