# Changelog

## 2.0.0

Implements [avohq/spec-first-inspector-server-sdk](https://github.com/avohq/spec-first-inspector-server-sdk) v3.0.1: the `/inspector/v2/track` endpoint with the `api-key`, `env` and `X-Avo-Client` headers, gzip for bodies of 1024 bytes or more, batching with `Flush` and `Destroy`, stream ids and gateway options (`TrackOptions`). This is a breaking release: the module path is now `github.com/avohq/go-avo-inspector/v2`; see [Upgrading from v1 to v2](README.md#upgrading-from-v1-to-v2) in the README. Every v1.0.0 function and method keeps its signature, and every v1.0.0 type still exists.

New API:

- **`Options` and `NewAvoInspectorWithOptions(Options)`**, for the batching options (`BatchSize`, `BatchFlushSeconds`, `MaxQueueSize`, `DisableBatchTimer`). A zero value means the default; a negative one prints a warning and uses the default. `NewAvoInspector` is unchanged.
- **`TrackSchemaFromEventWithOptions(eventName, properties, TrackOptions)`**, where `TrackOptions` carries `StreamId`, `OutputReference`, `OriginHint` and `OriginAppVersion`. `TrackSchemaFromEvent` is the same call without options.
- **`OrderedMap` and `KeyValue`**, to keep property order, with `TrackOrderedSchemaFromEvent` and `ExtractOrderedSchema`. A pointer to an `OrderedMap`, a named type based on it, and a bare `[]KeyValue` are read the same way, at the top level or nested.
- **`ExtractSchema`** returns the schema without sending anything.
- **`Flush(timeout)` and `Destroy()`.** `Flush` sends pending events and waits for in-flight sends. It returns `nil` only when, as it returns, nothing is buffered, waiting or in flight, and `ErrFlushTimeout` otherwise; the error is informational. `Flush(0)` starts the sends without waiting, so it returns `nil` only when nothing was pending; a negative timeout means `DefaultFlushTimeout` (10 seconds).
- **`EnableLogging`**, process-wide. `ShouldLog` still works and is deprecated in favour of it.
- **`Property.ListChildren`** holds a list property's element schemas. `Property.Children` now holds only an `object` property's children. Adding the field means a positional `Property` literal with three values no longer compiles; use field names.
- **`Property` decodes as well as encodes.** `UnmarshalJSON` is the inverse of `MarshalJSON`, so the schema JSON reads back into `[]Property`.
- **`Version`, `SpecVersion`, `LibPlatform` and `MissingEventName` constants.** `libVersion` on the wire comes from `Version`.
- `BaseBody`, `SessionStartedBody` and `EventSchemaBody` are deprecated and no longer sent; they remain so existing code compiles.

Wire and schema:

- **One event object per tracked event.** The `sessionStarted` element and the `sessionId`, `trackingId`, `avoFunction`, `eventId` and `eventHash` fields are gone; every event carries `streamId`. `createdAt` has millisecond precision.
- **Gateway options are sent per event.** `OutputReference` and `OriginHint` are trimmed and omitted when blank. A non-blank `OriginAppVersion` replaces the app version for that event; with `OriginHint` set and no `OriginAppVersion`, the event is sent without an app version.
- **Lists are typed from their first element** (`list(int)`), with the element types listed as `children`; booleans are `boolean`. Typed slices, arrays and string-keyed maps such as `[]string` or `map[string]string` are read as lists and objects instead of `unknown`. Any floating type is `float`, including `0.0`, and a `json.Number` is `int` or `float` by its text.
- **Property order is sorted by key** for Go maps (random in v1.0.0), or kept as given with `OrderedMap`.
- **Schema extraction is bounded.** It stops at 10 levels of nesting, at a map or slice that contains itself, and after 10,000 expanded maps and lists per call (a value shared by many properties would otherwise expand exponentially), the same limits as the Node SDK. Such a value is reported as `"object"` with no children (as a list element, the type string `"object"`). In v1.0.0 a cyclic payload overflowed the stack. Separately, one call emits at most 10,000 properties, counting nested properties: the rest are left out, keys after the first 10,000 in sorted order for a map and in the given order for an `OrderedMap`, so a map with a million keys costs about as much as one with 10,000. Neither limit is logged. A typed slice or array of scalars, structs or other kinds whose element type alone decides the result is typed without visiting its elements.
- **An event without a name is sent as `Missing Event Name`.** An empty or whitespace-only event name is replaced by that placeholder; the event is sent like any other, its schema is returned, and it never returns an error, in `Dev` either.

Delivery and robustness:

- **Events are buffered outside `Dev`** and sent when `BatchSize` events are queued, when the oldest is `BatchFlushSeconds` old (capped at 24 hours), or on `Flush`. A `BatchSize` of 1, always the case in `Dev`, sends every event during the call. When more than `MaxQueueSize` events are buffered, the oldest are dropped. A `BatchSize` larger than `MaxQueueSize` prints a warning.
- **`TrackSchemaFromEvent`'s `error` no longer reports HTTP failures.** It reports only an internal failure before the event was queued. With a `BatchSize` of 1, a non-200 response returns an empty schema; a network failure or timeout still returns the schema.
- **Each inspector sends at most 4 batches at once.** Batches waiting their turn may hold up to 10,000 events, separately from `MaxQueueSize`; past that the oldest waiting events are dropped. Failed requests are never retried. See "High-volume and backfill jobs" in the README.
- **Tracking waits for room under load (backpressure).** Once 1,000 events wait for a sender, a tracking call waits until fewer do before it returns, for at most about the 10-second request timeout, and `Destroy` releases it. A loop that tracks faster than the endpoint accepts is paced by the endpoint, as v1's synchronous sends paced it, which slows the growth of the backlog; an endpoint slow for longer than the wait can still overflow it, so pacing does not guarantee delivery.
- **A send that panics is recovered**: it is logged as an internal error and its events count as dropped, and the sender goes on. No batch can be left pending: one registered for sending is always either sent or finished as dropped, so `Flush` never waits on it.
- **An idle inspector holds no goroutine or timer.** The batch timer is a one-shot timer armed only while events are pending, so an inspector you stop using after a `Flush` is garbage-collected without `Destroy`.
- **Redirects are not followed.** A 3xx counts as a non-200 response, so the `api-key` header is never forwarded to another host.
- **The API key is validated** in the constructor and again before each send: it must not be empty or whitespace, must be valid UTF-8, and must not contain a control character other than tab. A refused send is dropped; the key is never rewritten.
- **Unknown environments fall back to `Dev`** with a warning (in v1.0.0 only an empty env did). The missing-version error message now reads "Many features of Inspector rely on versioning".
- **The sampling rate changes only on a 200 that carries a numeric `samplingRate` in [0, 1].** A `{"success":false}` response no longer sets it to 0 and drops every later event, and the rate is read and written under a lock. Sampling is decided per event, before it is queued.

Logging:

- **Logs go to stderr** with an `[Avo Inspector] ` prefix. v1.0.0 wrote some to stdout and some through the standard `log` package.
- **Logs never contain property values or the API key.** A tracked event is logged with its schema (property names and types). Because the logging flag is process-wide, the v1.0.0 line with raw parameters could print values such as emails from a prod inspector.
- **Dropped events, rejected batches, failed sends and internal errors are always logged**, whatever `EnableLogging`. Each kind, and each reason or status within it, prints at most one line every 10 seconds. The count of what was suppressed is carried into the next line of that kind, or printed by `Flush` once its 10 seconds have passed and right away by `Destroy`, with the real number of seconds it covers (the same rule as the Node and Java SDKs, with no timer). Events tracked without a name and the `:`-in-stream-id warning are limited the same way.
- **A caught error or panic is logged by its type only**, never its message, which can quote data from your events. Failed sends use fixed labels (`Request failed`, `Request timed out`, and so on), never the error text. Response bodies are never logged. A send abandoned by `Destroy` is not logged.
- `EnableLogging` gates only the debug lines (per-event lines, sampling drops, "sent N event(s).").

Build and module:

- **The module path is `github.com/avohq/go-avo-inspector/v2`**, following Go's rule for major versions; the package name is still `avoinspector`. It still targets Go 1.20 and uses only the standard library.
- `scripts/run-conformance.sh` builds the conformance harness (`cmd/conformance`) and runs the spec's conformance suite against it.
- The example in `example/` imports the `/v2` module.
