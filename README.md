# Avo Inspector for Go

Implements [avohq/spec-first-inspector-server-sdk](https://github.com/avohq/spec-first-inspector-server-sdk) v3.0.1 (`avoinspector.SpecVersion`).
See [CHANGELOG.md](CHANGELOG.md) for what changed in each release.

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

Events are sent in batches from an in-memory buffer, except with a `BatchSize` of 1 (always the
case in `Dev`), where each event is sent during the call. **Events still buffered or in flight when the process exits are lost.** Go gives
libraries no exit hook, so the SDK cannot flush for you: call `Flush` before the process exits.

In `main`, defer it right after creating the inspector:

```go
func main() {
	inspector, err := avoinspector.NewAvoInspector(apiKey, avoinspector.Prod, "1.0", "my app")
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := inspector.Flush(avoinspector.DefaultFlushTimeout); err != nil {
			log.Print("Avo Inspector flush: ", err) // some events may not have been sent
		}
	}()

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
if err := inspector.Flush(avoinspector.DefaultFlushTimeout); err != nil {
	log.Print("Avo Inspector flush: ", err) // some events may not have been sent
}
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
	MaxQueueSize:      1000,  // buffered events beyond this drop the oldest (default 1000)
	DisableBatchTimer: false, // set to true in serverless deployments
})
```

A zero `BatchSize`, `BatchFlushSeconds` or `MaxQueueSize` means "use the default", without a warning.
A negative value is invalid: it logs a warning and the default is used. `BatchFlushSeconds` above
86400 (24 hours) is capped at 86400 with a warning.

A `BatchSize` of 1, in any environment, makes every tracking call send its event and wait for the
response, as in `Dev`. Only a non-200 response makes the call return an empty schema; a network
failure or timeout returns the schema, so a returned schema does not mean the event was delivered.
When earlier batches are still being sent, that wait can take longer than the 10-second request
timeout.

`MaxQueueSize` bounds only the events buffered before a batch is formed. Batches already formed and
waiting to be sent have their own limit (see [High-volume and backfill jobs](#high-volume-and-backfill-jobs)).

### High-volume and backfill jobs

At most 4 requests are sent at once, and batches formed while all 4 are busy wait their turn. Once
1,000 events are waiting, each tracking call waits for room before it returns, for at most about the
10-second request timeout. A job that tracks events faster than the endpoint accepts them, such as
a backfill loop, is therefore paced by the endpoint, as tracking was in v1, which slows the growth of
the backlog. Pacing does not guarantee delivery. `Destroy` releases calls that are waiting.

The wait is bounded, so an endpoint that is slow or down can still fall behind: up to 10,000 events
can wait, and beyond that the oldest waiting events are dropped and the drop is logged, which keeps
the events held for sending bounded. While the backlog is full, a tracking call can take up to
about 10 seconds, so keep that in mind when you track from a latency-sensitive path.

A backfill loop needs no periodic flushes; flush at the end. `Flush` waits only up to its timeout,
so when it returns `ErrFlushTimeout`, events are still waiting or in flight: flush again, bound
those retries, then give up and log:

```go
for _, row := range rows {
	if _, err := avoInspector.TrackSchemaFromEvent(row.Event, row.Properties); err != nil {
		log.Print("Avo Inspector track: ", err)
	}
}
drained := false
for attempt := 0; attempt < 6 && !drained; attempt++ {
	drained = avoInspector.Flush(avoinspector.DefaultFlushTimeout) == nil
}
if !drained {
	log.Print("Avo Inspector did not drain; waiting events may be dropped")
}
```

With a responsive endpoint the first `Flush` normally drains, and an unreachable one fails each
request at once. Against a slow or hung endpoint each request gives up after 10 seconds, so every
attempt shrinks the backlog, but draining a few thousand events can then take minutes, longer than
the attempts allow. Tracking from other goroutines on the same inspector can also keep `Flush` from
ever seeing it drained. That is why the loop is bounded rather than repeated until `Flush` returns
`nil`.

## Enabling logs

Logs are enabled by default in the dev mode and disabled otherwise. The setting is process-wide: it
applies to every inspector in the process. Do not enable logs in production.

```go
avoInspector.EnableLogging(true)
```

`ShouldLog` still works and is deprecated in favour of `EnableLogging`. Logs go to stderr. They never
include the API key or property values: a tracked event is logged with its schema (property names
and types), so values such as email addresses stay out of your logs even when another inspector in
the process turns logging on.

Data loss is always logged, whatever this setting:

| What | Log line |
|---|---|
| Events dropped because the buffer is full (`MaxQueueSize`) | `dropped N event(s) (queue full) in the last Ns.` |
| Events dropped because more than 10,000 wait to be sent | `dropped N event(s) (send backlog full) in the last Ns.` |
| Events tracked with an empty or whitespace-only name, which are sent named `Missing Event Name` | `N event(s) tracked without an event name in the last Ns, sent as "Missing Event Name".` |
| Events in a batch whose send hit an internal error | `dropped N event(s) (internal error) in the last Ns.`, after `send error: <type>` |
| Batches rejected with a non-200 response | `N batch(es) rejected with HTTP <status> in the last Ns.` |
| Failed sends (network error, timeout, refused send) | `schema sending failed: Request failed.` or `Request timed out.` |
| Internal errors | `internal error: <type>`, for example `internal error: *errors.errorString` |

Each kind, and each reason or status within it, prints at most one line per 10 seconds: the first
occurrence prints at once, later ones are counted, and the count is reported with the next line of
that kind, by `Flush` once that kind's 10-second window has passed, or by `Destroy` at any time,
whichever comes first. So a burst followed by quiet is still reported, while an app that calls
`Flush` after every event keeps the rate limit. "In the last Ns" is the real time the count
covers, in whole seconds since that window's first occurrence (at least 1): a count reported long
after the burst says so. The warning for a `StreamId` containing `:` is rate-limited the same way. Response bodies
are never logged, and a caught error or panic is logged by its type only, never its message, which
can carry data from your events. Everything else, such as events dropped by sampling
and per-event debug lines, is logged only when logging is enabled. Sends abandoned by `Destroy` are
not logged.

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
where every event is sent before the call returns, a non-200 response returns an empty schema; a
network failure or timeout still returns the schema.

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
`"android"`, never a user identifier. A non-blank `OriginAppVersion` replaces the inspector's app
version for that event, whether or not `OriginHint` is set. When `OriginHint` is set and
`OriginAppVersion` is blank, the event is sent without an app version.

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

## Schema extraction limits

Schema extraction runs on your goroutine, inside the tracking call. Without limits, a payload such as
an ORM object or a full API response could make that work unbounded, so extraction stops expanding
a value:

- more than 10 levels deep, where each step into an object or into a list element counts as one
  level;
- that contains itself (a cycle);
- once 10,000 objects and lists have been expanded in one call (the event properties object and
  every list count).

A property cut off this way is reported as `"object"` with empty `children`; a list element cut off
this way is reported as the type string `"object"`.

Separately, one call emits at most 10,000 properties, counting the properties of nested objects too
(an object's properties are counted before its next sibling). Once 10,000 have been emitted, the
remaining properties are left out of the schema: for a map, the keys after the first 10,000 in sorted
order; for an `OrderedMap`, those after the first 10,000 in its order. A map with a million keys
therefore costs about as much as one with 10,000.

Neither limit is logged when it applies. Elements of a list that are strings, numbers or booleans
never count toward either limit, and a `[]byte` or other slice of a scalar type is typed from its
element type without visiting its elements, whatever its size.

For example, a map that refers to itself:

```go
order := map[string]interface{}{"id": 7}
order["self"] = order
avoInspector.ExtractSchema(map[string]interface{}{"order": order})
// As JSON:
// [{"propertyName":"order","propertyType":"object","children":[
//   {"propertyName":"id","propertyType":"int"},
//   {"propertyName":"self","propertyType":"object","children":[]}]}]
```

## Flush and Destroy

`Flush` sends the pending events and waits up to the given timeout for in-flight sends. Call it
before the process exits (see [Shutdown](#shutdown)). It always completes, and its error says
whether it drained the inspector: `nil` when, as `Flush` returns, nothing is buffered, waiting or in
flight, and `ErrFlushTimeout` otherwise, such as when the timeout passed first. Either way the
pending events are queued for sending and the inspector stays usable. `ErrFlushTimeout` does not
mean they were sent: some may still be waiting or in flight, so before the process exits, flush
again or accept that they may be lost. Otherwise you may ignore the error. Delivery failures are
never reported. `Flush(0)` starts sending the pending events without waiting for them,
so it returns `nil` only when there was nothing to send and nothing in flight; a negative timeout
waits up to `DefaultFlushTimeout`. `Flush` on a destroyed inspector returns `nil`.

`Destroy` discards pending events without sending them, abandons in-flight sends and stops the
background flush. After `Destroy`, tracking calls send nothing. An idle inspector holds no goroutine
or timer, so one you stop using after a `Flush` is garbage-collected even without `Destroy`.

## Upgrading from v1 to v2

- **The import path changed.** Following Go's module rule for major versions, v2 lives at
  `github.com/avohq/go-avo-inspector/v2`. Run `go get github.com/avohq/go-avo-inspector/v2` and
  change your imports to that path. The package name is still `avoinspector`.

Apart from the import path, every v1.0.0 function and method keeps its signature, and every v1.0.0
type still exists. One type gained a field: `Property` adds `ListChildren`, so a positional
`Property` literal with three values no longer compiles. Use field names in the literal, or add the
fourth value.

These are the behaviour changes you may notice:

- **List children moved.** A list property's element schemas are now in `Property.ListChildren`, in
  the spec's shape (type strings, nested schemas, nested lists). `Property.Children` now holds only
  the children of an `object` property; for lists it is nil, where v1.0.0 put one entry per index,
  named `"0"`, `"1"`, and so on.
- **Type names changed.** Booleans are `"boolean"` instead of `"bool"`, and lists are
  `"list(<element type>)"` (typed by the first element, e.g. `"list(string)"`) instead of `"list"`.
  Typed slices and maps such as `[]string` or `map[string]string` are now read as lists and objects
  instead of `"unknown"`.
- **Schema extraction is bounded.** v1 descended without limit, so a map that contained itself
  recursed until the stack overflowed. Values deeper than 10 levels, cycles, and values past 10,000
  expanded objects and lists per call are now reported as `"object"`, and a call emits at most
  10,000 properties; the rest are left out (see
  [Schema extraction limits](#schema-extraction-limits)).
- **Property order is sorted.** A `map[string]interface{}` is listed sorted by key; in v1.0.0 the
  order was random. Use `OrderedMap` to choose the order.
- **`error` no longer reports HTTP failures.** `TrackSchemaFromEvent` returns an error only for an
  internal failure before the event was queued. A failed send is logged and dropped. In `Dev`, a
  non-200 response returns an empty schema; a network failure or timeout returns the schema.
- **Events are buffered outside `Dev`.** In `Staging` and `Prod`, events are sent in batches in the
  background instead of during the call. Call `Flush` before the process exits, or buffered events
  are lost. `Dev` still sends each event before the call returns. At most 4 batches are sent at
  once. Once 1,000 events wait to be sent, tracking calls wait for room (at most about 10 seconds),
  so a tight loop is paced by the endpoint as it was in v1; past 10,000 waiting events the oldest
  are dropped (see [High-volume and backfill jobs](#high-volume-and-backfill-jobs)).
- **`ShouldLog` is process-wide.** It now sets one flag for every inspector in the process. It gates
  only the debug lines (per-event lines, sampling drops) and the "sent N event(s)." line. Dropped
  events, rejected batches, failed sends and internal errors are always logged, rate-limited (see
  [Enabling logs](#enabling-logs)).
- **Unknown environments fall back to `Dev`.** Any env other than `Dev`, `Staging` or `Prod` now
  becomes `Dev`, with a warning: each event is sent immediately and logging is turned on. In v1.0.0
  only an empty env did this, and any other value was sent as given.
- **Stricter validation.** A whitespace-only API key or app version is rejected, and so is an API
  key containing a control character other than tab (such as a carriage return, line feed or NUL).
  The missing-version error message now reads "Many features of Inspector rely on versioning" (it
  was "Some features").
- **Logs go to stderr.** All logs are written to stderr with an `[Avo Inspector] ` prefix. v1.0.0
  wrote some to stdout and some through the standard `log` package. Dropped events, rejected
  batches, failed sends and internal errors are now logged even when logging is off, at most once
  per 10 seconds per kind. A tracked event is logged with its schema, not
  its property values, which v1.0.0 printed.
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
