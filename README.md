# Avo Inspector for Go

Implements [avohq/spec-first-inspector-server-sdk](https://github.com/avohq/spec-first-inspector-server-sdk) v3.0.1 (`avoinspector.SpecVersion`).

> **Flush before your process exits.** Outside `Dev`, events are buffered in memory and sent in
> batches. Events still buffered when the process exits are lost. Go has no exit hook the SDK could
> use, so call `Flush` yourself: see [Shutdown](#shutdown).

## Avo documentation

This is a quick start guide. For more information about the Inspector project please read [Avo Inspector SDK Reference](https://www.avo.app/docs/implementation/avo-inspector-sdk-reference) and the [Avo Inspector Setup Guide](https://www.avo.app/docs/implementation/setup-inspector-sdk).

## Installation

```
go get github.com/avohq/go-avo-inspector/v2
```

## Shutdown

Events are sent in batches from an in-memory buffer (except in `Dev`, where each event is sent
during the call). **Events still buffered or in flight when the process exits are lost.** Go gives
libraries no exit hook, so the SDK cannot flush for you: call `Flush` before the process exits.

In `main`, defer it right after creating the inspector:

```go
func main() {
	inspector, err := avoinspector.NewAvoInspector(apiKey, avoinspector.Prod, "1.0", "my app")
	if err != nil {
		log.Fatal(err)
	}
	defer inspector.Flush(avoinspector.DefaultFlushTimeout)

	// ... run your program ...
}
```

Deferred calls do not run when the process ends through `os.Exit` (including `log.Fatal`), an
unrecovered panic in another goroutine, or a signal it does not handle. A `SIGTERM` from your
orchestrator ends a Go program without running deferred calls unless you handle it, so handle it
and flush:

```go
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()

go func() { _ = server.ListenAndServe() }()
<-ctx.Done()                              // SIGTERM or Ctrl-C
_ = server.Shutdown(context.Background()) // let in-flight requests finish tracking
inspector.Flush(avoinspector.DefaultFlushTimeout)
```

In a serverless function, call `Flush` before the handler returns, and set `DisableBatchTimer` in
`Options`: the runtime may freeze or discard the process between invocations.

## Initialization

Obtain the API key at [Avo.app](https://www.avo.app/welcome)

```go
import (
	avoinspector "github.com/avohq/go-avo-inspector/v2"
)

avoInspector, err := avoinspector.NewAvoInspector(
    "...", // Your API key obtained in the Avo workspace
    avoinspector.Dev, // or avoinspector.Staging, avoinspector.Prod
    "1.0", // App version
    "my app", // App name
    )
```

`NewAvoInspector` returns an error if the API key or the app version is empty or whitespace, or if
the API key is not valid UTF-8 or contains a control character other than tab (such as a carriage
return, line feed or NUL). An empty or unknown environment falls back to `Dev` with a warning.

To configure batching, use `NewAvoInspectorWithOptions`:

```go
avoInspector, err := avoinspector.NewAvoInspectorWithOptions(avoinspector.Options{
	ApiKey:            "...",
	Env:               avoinspector.Prod,
	AppVersion:        "1.0",
	AppName:           "my app",
	BatchSize:         30,    // send when this many events are pending (default 30; always 1 in Dev)
	BatchFlushSeconds: 30,    // send pending events at least this often (default 30)
	MaxQueueSize:      1000,  // pending events beyond this drop the oldest (default 1000)
	DisableBatchTimer: false, // set to true in serverless deployments
})
```

A zero `BatchSize`, `BatchFlushSeconds` or `MaxQueueSize` means "use the default", without a warning.
A negative value is invalid: it logs a warning and the default is used. `BatchFlushSeconds` above
86400 (24 hours) is capped at 86400 with a warning.

## Enabling logs

Logs are enabled by default in the dev mode and disabled otherwise. The setting is process-wide: it
applies to every inspector in the process. Do not enable logs in production.

```go
avoInspector.EnableLogging(true)
```

`ShouldLog` still works and is deprecated in favour of `EnableLogging`. Logs go to stderr and never
include the API key. Failed sends (network errors, timeouts, a refused send) and internal errors are
always logged, whatever this setting; everything else, including non-200 responses, is logged only
when logging is enabled.

## Sending event schemas

Whenever you send a tracking event, also call the following method:

Read more in the [Avo documentation](https://www.avo.app/docs/implementation/devs-101#inspecting-events)

This method gets actual tracking event parameters, extracts schema automatically and sends it to the Avo Inspector backend.
It is the easiest way to use the library, just call this method at the same place you call your analytics tools' track methods with the same parameters.

```go
result, err := avoInspector.TrackSchemaFromEvent("Test Event", map[string]interface{}{
	"str":  "hello",
	"int":  42,
	"flt":  3.14,
	"bol":  true,
	"nul":  nil,
	"lst":  []interface{}{"foo", "bar", nil, map[string]interface{}{"d": 42}},
	"obj":  map[string]interface{}{"a": 1, "b": "two", "c": []interface{}{true, 3.14}},
	"unk":  complex(1, 2),
	"func": func() {},
})
```

`TrackSchemaFromEvent` returns the extracted schema. Since v2 its `error` only reports an
internal failure before the event was queued; it is never an HTTP or network failure. A failed
send is logged and its events are dropped, without retry. In `Dev`,
where every event is sent before the call returns, a non-200 response returns an empty schema.

In v1 every call sent the event synchronously and returned the HTTP failure as `error`. Now
events are batched (except in `Dev`) and sent in the background, so you must call `Flush` before
the process exits (see [Shutdown](#shutdown)).

### Stream id and gateway options

Go has no named arguments, so the optional per-call inputs are grouped in one `TrackOptions` struct:

```go
result, err := avoInspector.TrackSchemaFromEventWithOptions("Purchase", properties, avoinspector.TrackOptions{
	StreamId:         "stream-123", // correlation id, sent verbatim
	OutputReference:  "meta-x7k2q", // the gateway output this observation was bound for
	OriginHint:       "android",    // the source the event came from
	OriginAppVersion: "4.2.0",      // that source's app version
})
```

All fields are optional. `OriginHint` must be a low-cardinality label such as `"web"`, `"ios"` or
`"android"`, never a user identifier. When `OriginHint` is set, the event is sent with
`OriginAppVersion` as its app version (or none, if that is empty) instead of the inspector's app
version.

### Property order

Go maps have no order, so a schema extracted from a `map[string]interface{}` lists properties sorted
by key. To keep a specific order, pass an `OrderedMap`, at the top level or as any nested value:

```go
result, err := avoInspector.TrackOrderedSchemaFromEvent("Signup", avoinspector.OrderedMap{
	{Key: "plan", Value: "pro"},
	{Key: "seats", Value: 3},
}, avoinspector.TrackOptions{})
```

`ExtractSchema` and `ExtractOrderedSchema` return the schema without sending anything.

## Flush and Destroy

`Flush` sends the pending events and waits up to the given timeout for in-flight sends. Call it
before the process exits (see [Shutdown](#shutdown)). It always completes, and its error is
informational: you may ignore it. It is `ErrFlushTimeout` when in-flight sends were still running
when the timeout passed, and `nil` otherwise; in both cases the pending events were sent and the
inspector stays usable. Delivery failures are never reported. `Flush(0)` sends the pending events
without waiting for them and returns `nil`; a negative timeout waits up to `DefaultFlushTimeout`.

`Destroy` discards pending events without sending them, abandons in-flight sends and stops the
background flush. After `Destroy`, tracking calls send nothing. An idle inspector holds no goroutine
or timer, so one you stop using after a `Flush` is garbage-collected even without `Destroy`.

## Upgrading from v1 to v2

- **The import path changed.** Following Go's module rule for major versions, v2 lives at
  `github.com/avohq/go-avo-inspector/v2`. Run `go get github.com/avohq/go-avo-inspector/v2` and
  change your imports to that path. The package name is still `avoinspector`.

Apart from the import path, every v1.0.0 function and type still exists with the same signature.
These are the behaviour changes you may notice:

- **List children moved.** A list property's element schemas are now in `Property.ListChildren`, in
  the spec's shape (type strings, nested schemas, nested lists). `Property.Children` now holds only
  the children of an `object` property; for lists it is nil, where v1.0.0 put one entry per index,
  named `"0"`, `"1"`, and so on.
- **Type names changed.** Booleans are `"boolean"` instead of `"bool"`, and lists are
  `"list(<element type>)"` (typed by the first element, e.g. `"list(string)"`) instead of `"list"`.
  Typed slices and maps such as `[]string` or `map[string]string` are now read as lists and objects
  instead of `"unknown"`.
- **Property order is sorted.** A `map[string]interface{}` is listed sorted by key; in v1.0.0 the
  order was random. Use `OrderedMap` to choose the order.
- **`error` no longer reports HTTP failures.** `TrackSchemaFromEvent` returns an error only for an
  internal failure before the event was queued. A failed send is logged and dropped. In `Dev`, a
  non-200 response returns an empty schema.
- **Events are buffered outside `Dev`.** In `Staging` and `Prod`, events are sent in batches in the
  background instead of during the call. Call `Flush` before the process exits, or buffered events
  are lost. `Dev` still sends each event before the call returns.
- **`ShouldLog` is process-wide.** It now sets one flag for every inspector in the process, and it
  now also controls the network logs, which in v1.0.0 it never reached.
- **Unknown environments fall back to `Dev`.** Any env other than `Dev`, `Staging` or `Prod` now
  becomes `Dev`, with a warning: each event is sent immediately and logging is turned on. In v1.0.0
  only an empty env did this, and any other value was sent as given.
- **Stricter validation.** A whitespace-only API key or app version is rejected, and so is an API
  key containing a control character other than tab (such as a carriage return, line feed or NUL).
  The missing-version error message now reads "Many features of Inspector rely on versioning" (it
  was "Some features").
- **Logs go to stderr.** All logs are written to stderr with an `[Avo Inspector] ` prefix. v1.0.0
  wrote some to stdout and some through the standard `log` package. Failed sends and internal
  errors are now logged even when logging is off.
- **New endpoint and wire body.** Events go to `https://api.avo.app/inspector/v2/track` with
  `api-key`, `env` and `X-Avo-Client` headers and `Content-Type: application/json`, gzipped when
  1024 bytes or larger. Each event is one element of the body. The separate `sessionStarted`
  element and the `sessionId`, `trackingId`, `avoFunction`, `eventId` and `eventHash` fields are no
  longer sent. `createdAt` has millisecond precision and `libVersion` is `2.0.0`.

  The Inspector API accepts events without `sessionId`; the C# SDK 1.1.0 sends the same shape.

## Conformance

`scripts/run-conformance.sh` builds the conformance harness (`cmd/conformance`, runner contract
1.1.0) and runs the spec's conformance suite against it. It needs `go` and `node` (>= 18). The
script fetches the spec repository at the pinned commit into `.spec-repo/`; set `SPEC_DIR` to use a
local checkout instead.

## Releasing

Update the `Version` constant in `version.go` on every release; it is sent as `libVersion`. Release
tags must match the module's major version: v2 releases are tagged `v2.x.y`, starting with `v2.0.0`.

## Author

Avo (https://www.avo.app), friends@avo.app

## License

AvoInspector is available under the MIT license.
