// Package testhooks exposes test-only hooks of the avoinspector package to the conformance
// harness. Being internal, it cannot be imported from outside this module, so the hooks never
// become part of the SDK's public API.
package testhooks

// SetSamplingRate overrides an *avoinspector.AvoInspector's sampling rate. It is installed by the
// avoinspector package's init.
var SetSamplingRate func(inspector interface{}, rate float64)
