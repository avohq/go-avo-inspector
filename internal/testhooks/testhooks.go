// Package testhooks exposes test-only hooks of the avoinspector package to the conformance
// harness. Being internal, it cannot be imported from outside this module, so the hooks never
// become part of the SDK's public API.
package testhooks

// SetSamplingRate overrides an *avoinspector.AvoInspector's sampling rate. It is installed by the
// avoinspector package's init.
var SetSamplingRate func(inspector interface{}, rate float64)

// SetProductionEndpoint replaces the URL that prod instances, and instances without a mock
// endpoint, send to, so a test binary can keep every request off the real API. It is installed by
// the avoinspector package's init; production code never calls it.
var SetProductionEndpoint func(url string)

// ObserveRequest, when set, is called with the URL of every request just before it is sent. It is
// read without synchronization: set it before any inspector sends, as a TestMain does.
var ObserveRequest func(url string)
