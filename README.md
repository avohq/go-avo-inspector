# Avo Inspector for Go

Implements [avohq/spec-first-inspector-server-sdk](https://github.com/avohq/spec-first-inspector-server-sdk) v3.0.1 (`avoinspector.SpecVersion`).

## Avo documentation

This is a quick start guide. For more information about the Inspector project please read [Avo Inspector SDK Reference](https://www.avo.app/docs/implementation/avo-inspector-sdk-reference) and the [Avo Inspector Setup Guide](https://www.avo.app/docs/implementation/setup-inspector-sdk).

## Installation

```
go get github.com/avohq/go-avo-inspector
```

## Initialization

Obtain the API key at [Avo.app](https://www.avo.app/welcome)

```go
import (
	avoinspector "github.com/avohq/go-avo-inspector"
)

avoInspector, err := avoinspector.NewAvoInspector(
    "...", // Your API key obtained in the Avo workspace
    avoinspector.Dev, // or avoinspector.Staging, avoinspector.Prod
    "1.0", // App version
    "my app", // App name
    )
```

`NewAvoInspector` returns an error if the API key or the app version is empty or whitespace, or if
the API key contains a carriage return, line feed or NUL character. An empty or unknown environment
falls back to `Dev` with a warning.

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
A negative value is invalid: it logs a warning and the default is used.

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

`TrackSchemaFromEvent` returns the extracted schema. Since 1.1.0 its `error` only reports an
internal failure before the event was queued; it is never an HTTP or network failure. Delivery
failures are logged (when logging is enabled) and the event is dropped, without retry. In `Dev`,
where every event is sent before the call returns, a non-200 response returns an empty schema.

Before 1.1.0 every call sent the event synchronously and returned the HTTP failure as `error`. Now
events are batched (except in `Dev`) and sent in the background, so you must call `Flush` before
the process exits (see below).

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

## Flush before exit

Events are held in memory until a batch is sent. Pending and in-flight events are lost if the process
exits first, so call `Flush` before the process exits, and in serverless functions before the
handler returns:

```go
avoInspector.Flush(avoinspector.DefaultFlushTimeout) // sends pending events and waits up to 10 seconds
```

`Flush` always completes: it sends the pending events and waits for in-flight sends. Its error is
informational and you may ignore it. It is `ErrFlushTimeout` when in-flight sends were still running
when the timeout passed, and `nil` otherwise; in both cases the pending events were sent and the
inspector stays usable. Delivery failures are never reported. `Flush(0)` sends the pending events
without waiting for them; a negative timeout waits up to `DefaultFlushTimeout`.

`Destroy` discards pending events without sending them, abandons in-flight sends and stops the
background flush. After `Destroy`, tracking calls send nothing. An idle inspector holds no goroutine
or timer, so one you stop using after a `Flush` is garbage-collected even without `Destroy`.

## Conformance

`scripts/run-conformance.sh` builds the conformance harness (`cmd/conformance`, runner contract
1.1.0) and runs the spec's conformance suite against it. It needs `go` and `node` (>= 18). The
script fetches the spec repository at the pinned commit into `.spec-repo/`; set `SPEC_DIR` to use a
local checkout instead.

## Releasing

Update the `Version` constant in `version.go` on every release; it is sent as `libVersion`.

## Author

Avo (https://www.avo.app), friends@avo.app

## License

AvoInspector is available under the MIT license.
