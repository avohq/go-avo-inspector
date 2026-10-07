package avoinspector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/avohq/go-avo-inspector/v2/internal/testhooks"
)

type AvoInspectorEnv string

const (
	Prod    AvoInspectorEnv = "prod"
	Dev     AvoInspectorEnv = "dev"
	Staging AvoInspectorEnv = "staging"
)

const (
	defaultBatchSize         = 30
	defaultBatchFlushSeconds = 30.0
	maxBatchFlushSeconds     = 24 * 60 * 60.0
	defaultMaxQueueSize      = 1000

	// maxConcurrentSends is the number of batch sends in progress at once per inspector. Batches
	// swapped out beyond this wait for a free sender, so a fast producer or a slow endpoint cannot
	// open an unbounded number of requests.
	maxConcurrentSends = 4
	// maxWaitingEvents bounds the events in batches waiting for a sender. It is separate from
	// MaxQueueSize, which bounds only the unsent buffer; past it the oldest waiting events are
	// dropped.
	maxWaitingEvents = 10000
	// backpressureEvents is the number of events waiting for a sender at which a tracking call
	// waits for room before returning, so a tight loop is paced by the endpoint, as in v1.
	backpressureEvents = 1000

	noApiKeyMessage      = "[Avo Inspector] No API key provided. Inspector can't operate without API key."
	apiKeyControlMessage = "[Avo Inspector] API key contains a control character. The API key is sent as a request header and cannot contain CR, LF, or NUL."
	apiKeyUTF8Message    = "Avo Inspector: apiKey must be valid UTF-8"
	// apiKeyOtherControlMessage is for control characters other than CR, LF and NUL, which keep
	// the spec's apiKeyControlMessage.
	apiKeyOtherControlMessage = "Avo Inspector: apiKey must not contain control characters"
	noVersionMessage          = "[Avo Inspector] No version provided. Many features of Inspector rely on versioning. Please provide comparable string version, i.e. integer or semantic."
	internalErrorMessage      = "Avo Inspector: something went wrong. Please report to support@avo.app."
	logPrefix                 = "[Avo Inspector] "
)

// MissingEventName is the name an event is sent with when it is tracked with an empty or
// whitespace-only name.
const MissingEventName = "Missing Event Name"

// DefaultFlushTimeout is how long Flush waits for in-flight sends when given a negative timeout,
// and a sensible value to pass before the process exits.
const DefaultFlushTimeout = 10 * time.Second

// ErrFlushTimeout is returned by Flush when it did not drain the inspector: something was still
// buffered, waiting or in flight as it returned, because the timeout passed or, for Flush(0),
// because it does not wait. The pending events are still queued for sending and the instance stays
// usable, but some may not have been sent yet: before the process exits, flush again or accept that
// they may be lost. Otherwise callers may ignore it.
var ErrFlushTimeout = errors.New("Avo Inspector: flush returned before all events were sent")

// shouldLog is the process-wide logging flag (SPEC.md §4.4).
var shouldLog atomic.Bool

// Options configures an AvoInspector (SPEC.md §4.1, §5).
//
// A zero BatchSize, BatchFlushSeconds or MaxQueueSize means "use the default", silently. A
// negative value is invalid: it logs a warning and the default is used.
type Options struct {
	// ApiKey is the Inspector API key. Required.
	ApiKey string
	// Env is Dev, Staging or Prod. An empty or unknown value falls back to Dev with a warning.
	Env AvoInspectorEnv
	// AppVersion is the application version sent as appVersion. Required.
	AppVersion string
	// AppName is the application name sent as appName.
	AppName string
	// BatchSize flushes the pending batch when it holds this many events. Default 30; always 1 in
	// Dev. With a batch size of 1, in any env, each event is sent during the tracking call, which
	// waits for the response (SPEC.md §7.5). When earlier batches are still being sent, that wait
	// can be longer than the request timeout.
	BatchSize int
	// BatchFlushSeconds is the longest an event waits in the pending batch before the scheduled
	// flush sends it. Default 30; values above 86400 (24 hours) are capped with a warning.
	BatchFlushSeconds float64
	// MaxQueueSize caps the pending batch; on overflow the oldest events are dropped. Default 1000.
	// A BatchSize above it is kept but logs a warning, since such a batch never fills.
	MaxQueueSize int
	// DisableBatchTimer turns off the scheduled flush, leaving the size trigger and Flush. Set it
	// in serverless deployments.
	DisableBatchTimer bool
}

// TrackOptions are the optional per-call inputs of TrackSchemaFromEventWithOptions.
type TrackOptions struct {
	// StreamId is sent verbatim as streamId, or "" when empty.
	StreamId string
	// OutputReference is the gateway output this observation was bound for (e.g. "meta-x7k2q").
	// Empty means the observation was taken at the gateway checkpoint.
	OutputReference string
	// OriginHint is a low-cardinality label of the source the event came from (e.g. "web",
	// "ios"). It MUST NOT be a user identifier or any other high-cardinality value.
	OriginHint string
	// OriginAppVersion is the app version of the source that produced this event. It overrides
	// the instance's AppVersion for this event; when OriginHint is set and this is empty, the
	// event is sent with a null appVersion.
	OriginAppVersion string
}

// AvoInspector extracts event schemas and reports them to the Avo Inspector API. It is safe for
// concurrent use. Callers must call Flush before the process (or serverless handler) exits.
type AvoInspector struct {
	apiKey                 string
	environment            AvoInspectorEnv
	version                string
	appName                string
	avoNetworkCallsHandler *AvoNetworkCallsHandler

	batchSize         int
	batchFlushSeconds float64
	maxQueueSize      int
	disableBatchTimer bool

	// mu guards every field below.
	mu           sync.Mutex
	samplingRate float64
	pending      []wireEvent
	// inFlight holds a done channel for every batch that has left the buffer and is not finished:
	// being sent, or waiting in waiting for a sender. Flush waits on these.
	inFlight      map[uint64]chan struct{}
	nextSendID    uint64
	waiting       []*queuedBatch
	waitingEvents int
	// room is closed and replaced whenever waitingEvents falls or the inspector is destroyed, to
	// wake tracking calls waiting for room (awaitRoom).
	room          chan struct{}
	activeSenders int
	destroyed     bool
	// flushTimer is armed while the pending batch holds events and the batch timer is enabled.
	// timerGeneration changes whenever the batch is swapped out, so a timer that fires after its
	// batch was already taken does nothing.
	flushTimer      *time.Timer
	timerGeneration uint64
	ctx             context.Context
	cancel          context.CancelFunc
}

func init() {
	testhooks.SetSamplingRate = func(inspector interface{}, rate float64) {
		inspector.(*AvoInspector).setSamplingRate(rate)
	}
	testhooks.SetProductionEndpoint = func(url string) {
		trackingEndpoint = url
	}
}

// NewAvoInspector creates an inspector with the default batch configuration.
func NewAvoInspector(apiKey string, env AvoInspectorEnv, appVersion string, appName string) (*AvoInspector, error) {
	return NewAvoInspectorWithOptions(Options{ApiKey: apiKey, Env: env, AppVersion: appVersion, AppName: appName})
}

// NewAvoInspectorWithOptions creates an inspector. It returns an error if ApiKey or AppVersion is
// empty or whitespace, or if ApiKey is not valid UTF-8 or contains a control character other
// than tab.
func NewAvoInspectorWithOptions(options Options) (*AvoInspector, error) {
	if strings.TrimSpace(options.ApiKey) == "" {
		return nil, errors.New(noApiKeyMessage)
	}
	if !utf8.ValidString(options.ApiKey) {
		return nil, errors.New(apiKeyUTF8Message)
	}
	if strings.ContainsAny(options.ApiKey, "\r\n\x00") {
		return nil, errors.New(apiKeyControlMessage)
	}
	if !isSafeHeaderValue(options.ApiKey) {
		return nil, errors.New(apiKeyOtherControlMessage)
	}
	if strings.TrimSpace(options.AppVersion) == "" {
		return nil, errors.New(noVersionMessage)
	}

	env := options.Env
	if env != Dev && env != Staging && env != Prod {
		logAlways("Invalid env %q, falling back to \"dev\".", string(env))
		env = Dev
	}
	shouldLog.Store(env == Dev)

	batchSize := defaultBatchSize
	if options.BatchSize < 0 {
		logAlways("Invalid batchSize %d; using default %d.", options.BatchSize, defaultBatchSize)
	} else if options.BatchSize > 0 {
		batchSize = options.BatchSize
	}
	if env == Dev {
		batchSize = 1
	}
	batchFlushSeconds := defaultBatchFlushSeconds
	switch {
	case options.BatchFlushSeconds < 0 || math.IsNaN(options.BatchFlushSeconds):
		logAlways("Invalid batchFlushSeconds %v; using default %v.", options.BatchFlushSeconds, defaultBatchFlushSeconds)
	case options.BatchFlushSeconds > maxBatchFlushSeconds:
		// Larger values overflow the timer's time.Duration.
		logAlways("batchFlushSeconds %v is above the maximum; using %v (24 hours).", options.BatchFlushSeconds, maxBatchFlushSeconds)
		batchFlushSeconds = maxBatchFlushSeconds
	case options.BatchFlushSeconds > 0:
		batchFlushSeconds = options.BatchFlushSeconds
	}
	maxQueueSize := defaultMaxQueueSize
	if options.MaxQueueSize < 0 {
		logAlways("Invalid maxQueueSize %d; using default %d.", options.MaxQueueSize, defaultMaxQueueSize)
	} else if options.MaxQueueSize > 0 {
		maxQueueSize = options.MaxQueueSize
	}
	// Not clamped: conformance fixture batch-4 requires FIFO overflow in this case.
	if batchSize > maxQueueSize {
		logAlways("batchSize %d is larger than maxQueueSize %d, so a batch never fills: events are sent "+
			"only by the scheduled flush or Flush, and the oldest are dropped once %d are buffered. "+
			"Set BatchSize to at most MaxQueueSize.", batchSize, maxQueueSize, maxQueueSize)
	}

	ctx, cancel := context.WithCancel(context.Background())
	inspector := &AvoInspector{
		apiKey:                 options.ApiKey,
		environment:            env,
		version:                options.AppVersion,
		appName:                options.AppName,
		avoNetworkCallsHandler: newAvoNetworkCallsHandler(options.ApiKey, env),
		batchSize:              batchSize,
		batchFlushSeconds:      batchFlushSeconds,
		maxQueueSize:           maxQueueSize,
		disableBatchTimer:      options.DisableBatchTimer,
		samplingRate:           1.0,
		inFlight:               map[uint64]chan struct{}{},
		room:                   make(chan struct{}),
		ctx:                    ctx,
		cancel:                 cancel,
	}
	return inspector, nil
}

// EnableLogging turns diagnostic logging on or off for every inspector in the process. It is on
// by default in Dev and off otherwise. Do not enable it in production.
func (inspector *AvoInspector) EnableLogging(enable bool) {
	shouldLog.Store(enable)
}

// ShouldLog turns diagnostic logging on or off for every inspector in the process.
//
// Deprecated: use EnableLogging.
func (inspector *AvoInspector) ShouldLog(enable bool) {
	inspector.EnableLogging(enable)
}

// ExtractSchema returns the schema of eventProperties without sending anything. Properties are
// listed sorted by key; use ExtractOrderedSchema to keep a specific order. It never panics and
// returns an empty schema for nil input.
func (inspector *AvoInspector) ExtractSchema(eventProperties map[string]interface{}) []Property {
	return safeExtractSchema(eventProperties)
}

// ExtractOrderedSchema is ExtractSchema for properties given as an OrderedMap, whose order the
// schema keeps.
func (inspector *AvoInspector) ExtractOrderedSchema(eventProperties OrderedMap) []Property {
	return safeExtractSchema(eventProperties)
}

func safeExtractSchema(eventProperties interface{}) (schema []Property) {
	defer func() {
		if r := recover(); r != nil {
			logInternalError("extractSchema error", r)
			schema = []Property{}
		}
	}()
	return extractSchema(eventProperties)
}

// TrackSchemaFromEvent extracts the schema of eventProperties and queues it for sending. It is
// TrackSchemaFromEventWithOptions with no options.
func (inspector *AvoInspector) TrackSchemaFromEvent(eventName string, eventProperties map[string]interface{}) ([]Property, error) {
	return inspector.TrackSchemaFromEventWithOptions(eventName, eventProperties, TrackOptions{})
}

// TrackSchemaFromEventWithOptions extracts the schema of eventProperties, samples the event, and
// adds it to the pending batch, which is sent when it reaches the batch size, when the scheduled
// flush runs, or on Flush.
//
// It returns the extracted schema. The returned error is non-nil only for an internal failure
// before the event was queued; delivery failures are never returned. With a batch size of 1
// (always in Dev) the event is sent before returning; a non-200 response returns an empty schema,
// while a network failure or timeout still returns the schema. After Destroy it returns an empty schema and sends nothing. An event whose name is empty
// or whitespace is sent as MissingEventName, and a rate-limited line reports it. While 1,000 or
// more events wait to be sent, the call waits for room before returning, for at most about the
// 10-second request timeout (backpressure); Destroy releases it.
func (inspector *AvoInspector) TrackSchemaFromEventWithOptions(eventName string, eventProperties map[string]interface{}, options TrackOptions) ([]Property, error) {
	return inspector.track(eventName, eventProperties, options)
}

// TrackOrderedSchemaFromEvent is TrackSchemaFromEventWithOptions for properties given as an
// OrderedMap, whose order the schema keeps.
func (inspector *AvoInspector) TrackOrderedSchemaFromEvent(eventName string, eventProperties OrderedMap, options TrackOptions) ([]Property, error) {
	return inspector.track(eventName, eventProperties, options)
}

func (inspector *AvoInspector) track(eventName string, eventProperties interface{}, options TrackOptions) (schema []Property, err error) {
	inspector.mu.Lock()
	destroyed, samplingRate := inspector.destroyed, inspector.samplingRate
	inspector.mu.Unlock()
	if destroyed {
		return []Property{}, nil
	}

	defer func() {
		if r := recover(); r != nil {
			logInternalError("internal error", r)
			schema, err = nil, errors.New(internalErrorMessage)
		}
	}()

	if strings.TrimSpace(eventName) == "" {
		eventName = MissingEventName
		logLimited("missing-event-name", 1, func(total, _ int, seconds int64) string {
			return fmt.Sprintf("%d event(s) tracked without an event name in the last %ds, sent as %q.", total, seconds, MissingEventName)
		})
	}
	schema = safeExtractSchema(eventProperties)
	streamId := options.StreamId
	if strings.Contains(streamId, ":") {
		logLimited("streamid-colon", 1, func(_, suppressed int, seconds int64) string {
			return "streamId contains ':'; using the value verbatim." + suppressedSuffix(suppressed, seconds)
		})
	}
	if shouldLog.Load() {
		logf("supplied event %q with schema %s", eventName, schemaForLog(schema))
	}

	// SPEC.md §7.7: per-event sampling at enqueue.
	if rand.Float64() > samplingRate {
		logIfEnabled("event %q dropped due to sampling rate.", eventName)
		return schema, nil
	}

	event := inspector.newWireEvent(eventName, streamId, samplingRate, schema, options)

	var (
		batch                   *queuedBatch
		startSender             bool
		dropped, backlogDropped int
	)
	inspector.mu.Lock()
	if inspector.destroyed {
		inspector.mu.Unlock()
		return []Property{}, nil
	}
	inspector.pending = append(inspector.pending, event)
	for len(inspector.pending) > inspector.maxQueueSize {
		inspector.pending = inspector.pending[1:]
		dropped++
	}
	if len(inspector.pending) >= inspector.batchSize {
		batch, startSender, backlogDropped = inspector.takeBatch()
	} else {
		inspector.armFlushTimer()
	}
	inspector.mu.Unlock()

	if dropped > 0 {
		logDropped(dropped, "queue full")
	}
	if batch == nil {
		inspector.awaitRoom()
		return schema, nil
	}
	launched := false
	if startSender {
		// The batch is registered in flight with a sender slot reserved. If anything below panics
		// before launch starts that sender, finish the batch instead of stranding it.
		defer func() {
			if !launched {
				inspector.abandonReserved(batch)
			}
		}()
	}
	if testHookBeforeSend != nil {
		testHookBeforeSend()
	}
	inspector.launch(batch, startSender, backlogDropped)
	launched = true
	if inspector.batchSize == 1 {
		// Immediate-send mode: the outcome of this call's own send is observable (SPEC.md §7.5).
		if res, ok := <-batch.result; ok && res.status == sendNon200 {
			return []Property{}, nil
		}
	}
	inspector.awaitRoom()
	return schema, nil
}

// newWireEvent builds the wire body of one event, resolving the gateway options (SPEC.md §7.3.6).
func (inspector *AvoInspector) newWireEvent(eventName, streamId string, samplingRate float64, schema []Property, options TrackOptions) wireEvent {
	outputReference := strings.TrimSpace(options.OutputReference)
	originHint := strings.TrimSpace(options.OriginHint)
	var appVersion *string
	if originAppVersion := strings.TrimSpace(options.OriginAppVersion); originAppVersion != "" {
		appVersion = &originAppVersion
	} else if originHint == "" {
		version := inspector.version
		appVersion = &version
	}
	return wireEvent{
		ApiKey:          inspector.apiKey,
		AppName:         inspector.appName,
		AppVersion:      appVersion,
		LibVersion:      Version,
		Env:             string(inspector.environment),
		LibPlatform:     LibPlatform,
		MessageId:       newGuid(),
		StreamId:        streamId,
		CreatedAt:       time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		SamplingRate:    samplingRate,
		Type:            "event",
		EventName:       eventName,
		OutputReference: outputReference,
		OriginHint:      originHint,
		// A copy: the caller gets schema back and may change it before the event is sent.
		EventProperties: copySchema(schema),
	}
}

// queuedBatch is a batch that has left the buffer. It is registered in inFlight from that moment,
// so Flush waits for it whether it is being sent or still waiting for a sender.
type queuedBatch struct {
	id     uint64
	events []wireEvent
	// done is closed once the batch is sent or discarded.
	done chan struct{}
	// result yields the send result once, or is closed without a value if the batch is discarded.
	result chan sendResult
}

// takeBatch swaps out the pending batch and registers it as in flight, then hands it to a new
// sender if fewer than maxConcurrentSends are running, or queues it to wait for one. It does all of
// this in one critical section, so a concurrent Flush can never miss a batch. It returns the batch
// (nil when the buffer was empty), whether the caller must start a sender for it, and how many
// waiting events were dropped because the backlog passed maxWaitingEvents. Call it with mu held,
// then pass the results to launch after releasing it.
func (inspector *AvoInspector) takeBatch() (batch *queuedBatch, startSender bool, dropped int) {
	events := inspector.takePending()
	if len(events) == 0 {
		return nil, false, 0
	}
	batch = &queuedBatch{
		id:     inspector.nextSendID,
		events: events,
		done:   make(chan struct{}),
		result: make(chan sendResult, 1),
	}
	inspector.nextSendID++
	inspector.inFlight[batch.id] = batch.done
	if inspector.activeSenders < maxConcurrentSends {
		inspector.activeSenders++
		return batch, true, 0
	}
	inspector.waiting = append(inspector.waiting, batch)
	inspector.waitingEvents += len(events)
	return batch, false, inspector.dropOldestWaiting()
}

// dropOldestWaiting drops the oldest waiting events (FIFO) until at most maxWaitingEvents wait, and
// returns how many it dropped. A batch left empty is discarded. Call it with mu held.
func (inspector *AvoInspector) dropOldestWaiting() int {
	dropped := 0
	for inspector.waitingEvents > maxWaitingEvents && len(inspector.waiting) > 0 {
		oldest := inspector.waiting[0]
		n := inspector.waitingEvents - maxWaitingEvents
		if n > len(oldest.events) {
			n = len(oldest.events)
		}
		oldest.events = oldest.events[n:]
		inspector.waitingEvents -= n
		inspector.signalRoom()
		dropped += n
		if len(oldest.events) == 0 {
			inspector.waiting[0] = nil
			inspector.waiting = inspector.waiting[1:]
			inspector.discard(oldest)
		}
	}
	return dropped
}

// discard finishes a batch without sending it. Call it with mu held.
func (inspector *AvoInspector) discard(batch *queuedBatch) {
	delete(inspector.inFlight, batch.id)
	close(batch.result)
	close(batch.done)
}

// launch acts on what takeBatch returned, outside the lock: it logs the backlog drop and starts a
// sender for the batch when takeBatch reserved one.
func (inspector *AvoInspector) launch(batch *queuedBatch, startSender bool, dropped int) {
	if dropped > 0 {
		logDropped(dropped, "send backlog full")
	}
	if startSender {
		go inspector.runSender(batch)
	}
}

// signalRoom wakes every tracking call waiting in awaitRoom. Call it with mu held whenever
// waitingEvents falls or the inspector is destroyed.
func (inspector *AvoInspector) signalRoom() {
	close(inspector.room)
	inspector.room = make(chan struct{})
}

// awaitRoom is the backpressure: while backpressureThreshold or more events wait for a sender, the
// tracking call waits for room, so a loop that tracks faster than the endpoint accepts is paced by
// it instead of overflowing the backlog. The wait ends when the backlog falls below the
// threshold, when the inspector is destroyed, or after backpressureWait at most.
func (inspector *AvoInspector) awaitRoom() {
	deadline := time.Now().Add(backpressureWait)
	for {
		inspector.mu.Lock()
		if inspector.destroyed || inspector.waitingEvents < backpressureThreshold {
			inspector.mu.Unlock()
			return
		}
		room := inspector.room
		inspector.mu.Unlock()
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return
		}
		timer := time.NewTimer(remaining)
		select {
		case <-room:
			timer.Stop()
		case <-timer.C:
			return
		}
	}
}

// abandonReserved finishes a batch whose reserved sender was never started: it is removed from
// inFlight, its channels are closed and it counts as dropped. The slot passes to the next waiting
// batch, if any, so waiting batches are not stranded either; otherwise it is freed.
func (inspector *AvoInspector) abandonReserved(batch *queuedBatch) {
	inspector.mu.Lock()
	delete(inspector.inFlight, batch.id)
	var next *queuedBatch
	if !inspector.destroyed && len(inspector.waiting) > 0 {
		next = inspector.waiting[0]
		inspector.waiting[0] = nil
		inspector.waiting = inspector.waiting[1:]
		inspector.waitingEvents -= len(next.events)
		inspector.signalRoom()
	} else {
		inspector.activeSenders--
	}
	inspector.mu.Unlock()
	close(batch.result)
	close(batch.done)
	logDropped(len(batch.events), "internal error")
	if next != nil {
		go inspector.runSender(next)
	}
}

// runSender sends batch, then keeps sending waiting batches in order until none are left, and
// exits. At most maxConcurrentSends run at once, and none runs while nothing is waiting.
func (inspector *AvoInspector) runSender(batch *queuedBatch) {
	for {
		inspector.send(batch)
		inspector.mu.Lock()
		if inspector.destroyed || len(inspector.waiting) == 0 {
			inspector.activeSenders--
			inspector.mu.Unlock()
			return
		}
		batch = inspector.waiting[0]
		inspector.waiting[0] = nil
		inspector.waiting = inspector.waiting[1:]
		inspector.waitingEvents -= len(batch.events)
		inspector.signalRoom()
		inspector.mu.Unlock()
	}
}

// post sends one batch. A panic inside it is recovered and logged as an internal error by type,
// and posted is false, so the sender goes on and its slot is freed.
func (inspector *AvoInspector) post(batch *queuedBatch) (res sendResult, posted bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logInternalError("send error", recovered)
			posted = false
		}
	}()
	if testHookInSend != nil {
		testHookInSend()
	}
	return inspector.avoNetworkCallsHandler.send(inspector.ctx, batch.events), true
}

// send posts one batch outside the lock and finishes it. A batch whose post panicked counts as
// dropped.
func (inspector *AvoInspector) send(batch *queuedBatch) {
	res, posted := inspector.post(batch)
	inspector.mu.Lock()
	if posted && res.status == sendOk && res.samplingRate != nil {
		inspector.samplingRate = *res.samplingRate
	}
	delete(inspector.inFlight, batch.id)
	inspector.mu.Unlock()
	if !posted {
		logDropped(len(batch.events), "internal error")
		close(batch.result)
		close(batch.done)
		return
	}
	// SPEC.md §7.5, §12.5: a failed batch is logged and dropped, never re-queued or retried.
	switch res.status {
	case sendOk:
		logIfEnabled("sent %d event(s).", len(batch.events))
	case sendNon200:
		logRejected(res.statusCode)
	case sendFailed:
		// A send abandoned by Destroy is not a failure.
		failure, ok := res.err.(*sendFailure)
		if !ok {
			failure = errRequestFailed
		}
		if failure != errRequestAborted {
			logFailedSend(failure)
		}
	}
	batch.result <- res
	close(batch.done)
}

// Flush sends every pending event and waits until all in-flight sends have completed, or until
// timeout has passed. Flush(0) starts the sends without waiting; a negative timeout means
// DefaultFlushTimeout (10 seconds). Flush always completes (SPEC.md §4.6), and its error reports
// whether it drained the inspector: nil when, as Flush returns, nothing is buffered, waiting or in
// flight, and ErrFlushTimeout otherwise, such as when Flush(0) leaves its sends in flight or the
// timeout passes first. Either way the pending events are queued for sending and the inspector
// stays usable; ErrFlushTimeout means some may not have been sent yet, and callers may otherwise
// ignore it. Delivery failures are not reported. On a destroyed inspector Flush
// returns nil. Call Flush before the process or serverless handler exits: pending events are
// otherwise lost.
func (inspector *AvoInspector) Flush(timeout time.Duration) error {
	// Report held log counts whose window has expired, after the sends this call waited for.
	defer flushLogCounts(true)
	if timeout < 0 {
		timeout = DefaultFlushTimeout
	}
	inspector.mu.Lock()
	if inspector.destroyed {
		inspector.mu.Unlock()
		return nil
	}
	batch, startSender, dropped := inspector.takeBatch()
	waiting := make([]chan struct{}, 0, len(inspector.inFlight))
	for _, done := range inspector.inFlight {
		waiting = append(waiting, done)
	}
	inspector.mu.Unlock()

	if batch != nil {
		inspector.launch(batch, startSender, dropped)
	}

	deadline := time.Now().Add(timeout)
	for _, done := range waiting {
		select {
		case <-done:
			continue
		default:
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return ErrFlushTimeout
		}
		timer := time.NewTimer(remaining)
		select {
		case <-done:
			timer.Stop()
		case <-timer.C:
			return ErrFlushTimeout
		}
	}
	// Drained only if nothing is left now, including events queued while Flush waited.
	inspector.mu.Lock()
	drained := len(inspector.pending) == 0 && len(inspector.inFlight) == 0
	inspector.mu.Unlock()
	if !drained {
		return ErrFlushTimeout
	}
	return nil
}

// Destroy discards pending events and batches waiting to be sent, abandons in-flight sends and
// stops the scheduled flush. Afterwards TrackSchemaFromEvent sends nothing. It does not flush.
func (inspector *AvoInspector) Destroy() {
	inspector.mu.Lock()
	if inspector.destroyed {
		inspector.mu.Unlock()
		return
	}
	inspector.destroyed = true
	inspector.takePending()
	for _, batch := range inspector.waiting {
		inspector.discard(batch)
	}
	inspector.waiting = nil
	inspector.waitingEvents = 0
	inspector.signalRoom()
	inspector.inFlight = map[uint64]chan struct{}{}
	inspector.mu.Unlock()

	inspector.cancel()
	flushLogCounts(false)
}

// takePending swaps out the pending batch and disarms the flush timer. Call it with mu held.
func (inspector *AvoInspector) takePending() []wireEvent {
	batch := inspector.pending
	inspector.pending = nil
	if inspector.flushTimer != nil {
		inspector.flushTimer.Stop()
		inspector.flushTimer = nil
	}
	inspector.timerGeneration++
	return batch
}

// armFlushTimer starts the one-shot flush timer when the first event enters an empty batch, so no
// event waits longer than batchFlushSeconds (SPEC.md §12.3). Nothing runs while the batch is
// empty, so an idle inspector holds no goroutine or timer. Call it with mu held.
func (inspector *AvoInspector) armFlushTimer() {
	if inspector.disableBatchTimer || inspector.batchSize <= 1 || inspector.flushTimer != nil || len(inspector.pending) == 0 {
		return
	}
	delay := time.Duration(inspector.batchFlushSeconds * float64(time.Second))
	if delay < time.Millisecond {
		delay = time.Millisecond
	}
	generation := inspector.timerGeneration
	inspector.flushTimer = time.AfterFunc(delay, func() { inspector.onFlushTimer(generation) })
}

func (inspector *AvoInspector) onFlushTimer(generation uint64) {
	inspector.mu.Lock()
	if inspector.destroyed || generation != inspector.timerGeneration {
		inspector.mu.Unlock()
		return
	}
	batch, startSender, dropped := inspector.takeBatch()
	inspector.mu.Unlock()
	if batch != nil {
		inspector.launch(batch, startSender, dropped)
	}
}

// setSamplingRate is a test-only hook reached through internal/testhooks; it is deliberately not
// exported, since a public setter would let callers silently disable reporting.
func (inspector *AvoInspector) setSamplingRate(rate float64) {
	inspector.mu.Lock()
	inspector.samplingRate = rate
	inspector.mu.Unlock()
}

// backpressureThreshold and backpressureWait are backpressureEvents and the request timeout; they
// are variables only so tests can change them.
var (
	backpressureThreshold = backpressureEvents
	backpressureWait      = requestTimeout
)

// testHookInSend, when set by a test, runs inside a sender just before a batch is posted. It is
// always nil outside tests.
var testHookInSend func()

// testHookBeforeSend, when set by a test, runs after a size-triggered batch has left the buffer
// and before its send starts. It is always nil outside tests.
var testHookBeforeSend func()

// logRateWindow is how long an always-on log line of one kind stays quiet after it is printed.
const logRateWindow = 10 * time.Second

// logLimiter rate-limits the always-on log lines. For each key (a kind, plus the reason or status
// within it) it keeps when the current window began, how many occurrences were suppressed since,
// and how to word that key's line. It has no timer: suppressed counts are reported on the next
// occurrence after the window, by Flush once the window has expired, or by Destroy
// (flushLogCounts).
var logLimiter = struct {
	sync.Mutex
	entries map[string]*limitedLog
	now     func() time.Time
}{entries: map[string]*limitedLog{}, now: time.Now}

type limitedLog struct {
	windowStart time.Time
	suppressed  int
	line        logLine
}

// logLine words a limited line: total occurrences to report, how many of them were suppressed,
// and the whole seconds they span.
type logLine func(total, suppressed int, seconds int64) string

// logLimited prints an always-on line for n occurrences under key, at most once per
// logRateWindow. The line reports n plus the occurrences suppressed since the window began, over
// the real time since then. time.Now carries a monotonic reading, so the span is monotonic.
func logLimited(key string, n int, line logLine) {
	logLimiter.Lock()
	now := logLimiter.now()
	entry, seen := logLimiter.entries[key]
	if seen && now.Sub(entry.windowStart) < logRateWindow {
		entry.suppressed += n
		entry.line = line
		logLimiter.Unlock()
		return
	}
	suppressed, seconds := 0, int64(1)
	if seen {
		suppressed, seconds = entry.suppressed, wholeSeconds(now.Sub(entry.windowStart))
	}
	logLimiter.entries[key] = &limitedLog{windowStart: now, line: line}
	logLimiter.Unlock()
	logf("%s", line(n+suppressed, suppressed, seconds))
}

// flushLogCounts prints the line of every key holding suppressed occurrences and resets it, so a
// burst followed by quiet is still reported. Flush passes expiredOnly, which skips keys whose window
// is still open: an app that flushes after every event keeps the rate limit. Destroy reports
// everything.
func flushLogCounts(expiredOnly bool) {
	logLimiter.Lock()
	now := logLimiter.now()
	keys := make([]string, 0, len(logLimiter.entries))
	for key, entry := range logLimiter.entries {
		if entry.suppressed > 0 && (!expiredOnly || now.Sub(entry.windowStart) >= logRateWindow) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		entry := logLimiter.entries[key]
		lines = append(lines, entry.line(entry.suppressed, entry.suppressed, wholeSeconds(now.Sub(entry.windowStart))))
		delete(logLimiter.entries, key)
	}
	logLimiter.Unlock()
	for _, line := range lines {
		logf("%s", line)
	}
}

// wholeSeconds is the whole seconds in d, at least 1.
func wholeSeconds(d time.Duration) int64 {
	if seconds := int64(d / time.Second); seconds > 1 {
		return seconds
	}
	return 1
}

// logDropped reports events lost before sending: reason is "queue full" (maxQueueSize),
// "send backlog full" (maxWaitingEvents) or "internal error" (a send that panicked).
func logDropped(n int, reason string) {
	logLimited("dropped:"+reason, n, func(total, _ int, seconds int64) string {
		return fmt.Sprintf("dropped %d event(s) (%s) in the last %ds.", total, reason, seconds)
	})
}

// logRejected reports a batch answered with a non-200 status. Only the status is logged, never
// the response body.
func logRejected(status int) {
	logLimited("non200:"+strconv.Itoa(status), 1, func(total, _ int, seconds int64) string {
		return fmt.Sprintf("%d batch(es) rejected with HTTP %d in the last %ds.", total, status, seconds)
	})
}

// logFailedSend reports a batch lost to a network error, a timeout or a refused send. Its text and
// its limiter key come from the failure's fixed label, never from an error message.
func logFailedSend(failure *sendFailure) {
	logLimited("failed:"+failure.label, 1, func(_, suppressed int, seconds int64) string {
		return "schema sending failed: " + failure.label + "." + suppressedSuffix(suppressed, seconds)
	})
}

// logInternalError reports a recovered internal error by its type only (for example
// "*errors.errorString"): a panic value or error message can carry user data.
func logInternalError(context string, recovered interface{}) {
	logLimited("internal:"+context, 1, func(_, suppressed int, seconds int64) string {
		return fmt.Sprintf("%s: %T%s", context, recovered, suppressedSuffix(suppressed, seconds))
	})
}

func suppressedSuffix(suppressed int, seconds int64) string {
	if suppressed == 0 {
		return ""
	}
	return fmt.Sprintf(" (%d more in the last %ds)", suppressed, seconds)
}

// logOutput receives every log line; tests swap it under logMu.
var (
	logMu     sync.Mutex
	logOutput io.Writer = os.Stderr
)

// Log helpers. None of them may be passed the apiKey (SPEC.md §7.5.1) or raw event property
// values; log a schema with schemaForLog instead.

func logf(format string, args ...interface{}) {
	logMu.Lock()
	defer logMu.Unlock()
	fmt.Fprintf(logOutput, logPrefix+format+"\n", args...)
}

// schemaForLog renders a schema in its wire shape (property names, types and children) for a log
// line. It is the only form of event properties that may be logged: logging is process-wide, so a
// dev instance can turn it on for a prod one, and raw values can hold personal data.
func schemaForLog(schema []Property) string {
	encoded, err := json.Marshal(schema)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

// logAlways writes whatever the logging flag: warnings about caller mistakes (SPEC.md §4.2,
// §6.3) and reports of network errors, timeouts, refused sends and internal errors (§4.2, §7.5).
func logAlways(format string, args ...interface{}) {
	logf(format, args...)
}

// logIfEnabled writes only when logging is enabled: diagnostics such as per-event lines,
// successful sends and sampling drops. Data loss and internal errors are always logged, through
// logLimited.
func logIfEnabled(format string, args ...interface{}) {
	if shouldLog.Load() {
		logf(format, args...)
	}
}
