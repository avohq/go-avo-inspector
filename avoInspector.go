package avoinspector

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/avohq/go-avo-inspector/internal/testhooks"
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
	defaultMaxQueueSize      = 1000
	defaultFlushTimeout      = 10 * time.Second

	noApiKeyMessage      = "[Avo Inspector] No API key provided. Inspector can't operate without API key."
	apiKeyControlMessage = "[Avo Inspector] API key contains a control character. The API key is sent as a request header and cannot contain CR, LF, or NUL."
	noVersionMessage     = "[Avo Inspector] No version provided. Many features of Inspector rely on versioning. Please provide comparable string version, i.e. integer or semantic."
	internalErrorMessage = "Avo Inspector: something went wrong. Please report to support@avo.app."
	logPrefix            = "[Avo Inspector] "
)

// ErrFlushTimeout is returned by Flush when in-flight sends had not finished within the timeout.
// It is informational: the pending events were still sent, the instance stays usable, and callers
// may ignore it.
var ErrFlushTimeout = errors.New("Avo Inspector: flush timed out before all in-flight sends completed")

// shouldLog is the process-wide logging flag (SPEC.md §4.4).
var shouldLog atomic.Bool

// Options configures an AvoInspector (SPEC.md §4.1, §5). Zero values select the defaults.
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
	// Dev, where every event is sent immediately.
	BatchSize int
	// BatchFlushSeconds is the longest an event waits in the pending batch before the scheduled
	// flush sends it. Default 30.
	BatchFlushSeconds float64
	// MaxQueueSize caps the pending batch; on overflow the oldest events are dropped. Default 1000.
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
	inFlight     map[uint64]chan struct{}
	nextSendID   uint64
	destroyed    bool
	stopTimer    chan struct{}
	ctx          context.Context
	cancel       context.CancelFunc
}

func init() {
	testhooks.SetSamplingRate = func(inspector interface{}, rate float64) {
		inspector.(*AvoInspector).setSamplingRate(rate)
	}
}

// NewAvoInspector creates an inspector with the default batch configuration.
func NewAvoInspector(apiKey string, env AvoInspectorEnv, appVersion string, appName string) (*AvoInspector, error) {
	return NewAvoInspectorWithOptions(Options{ApiKey: apiKey, Env: env, AppVersion: appVersion, AppName: appName})
}

// NewAvoInspectorWithOptions creates an inspector. It returns an error if ApiKey or AppVersion is
// empty or whitespace, or if ApiKey contains CR, LF or NUL.
func NewAvoInspectorWithOptions(options Options) (*AvoInspector, error) {
	if strings.TrimSpace(options.ApiKey) == "" {
		return nil, errors.New(noApiKeyMessage)
	}
	if !isSafeHeaderValue(options.ApiKey) {
		return nil, errors.New(apiKeyControlMessage)
	}
	if strings.TrimSpace(options.AppVersion) == "" {
		return nil, errors.New(noVersionMessage)
	}

	env := options.Env
	if env != Dev && env != Staging && env != Prod {
		logWarning("Invalid env %q, falling back to \"dev\".", string(env))
		env = Dev
	}
	shouldLog.Store(env == Dev)

	batchSize := defaultBatchSize
	if options.BatchSize < 0 {
		logWarning("Invalid batchSize %d; using default %d.", options.BatchSize, defaultBatchSize)
	} else if options.BatchSize > 0 {
		batchSize = options.BatchSize
	}
	if env == Dev {
		batchSize = 1
	}
	batchFlushSeconds := defaultBatchFlushSeconds
	if options.BatchFlushSeconds < 0 {
		logWarning("Invalid batchFlushSeconds %v; using default %v.", options.BatchFlushSeconds, defaultBatchFlushSeconds)
	} else if options.BatchFlushSeconds > 0 {
		batchFlushSeconds = options.BatchFlushSeconds
	}
	maxQueueSize := defaultMaxQueueSize
	if options.MaxQueueSize < 0 {
		logWarning("Invalid maxQueueSize %d; using default %d.", options.MaxQueueSize, defaultMaxQueueSize)
	} else if options.MaxQueueSize > 0 {
		maxQueueSize = options.MaxQueueSize
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
		ctx:                    ctx,
		cancel:                 cancel,
	}
	if batchSize > 1 && !options.DisableBatchTimer {
		inspector.stopTimer = make(chan struct{})
		go inspector.runBatchTimer(time.Duration(batchFlushSeconds*float64(time.Second)), inspector.stopTimer)
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
			logError("extractSchema error: %v", r)
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
// before the event was queued; delivery failures are never returned. In Dev (batch size 1) the
// event is sent before returning, and a non-200 response returns an empty schema. After Destroy it
// returns an empty schema and sends nothing.
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
	destroyed := inspector.destroyed
	inspector.mu.Unlock()
	if destroyed {
		return []Property{}, nil
	}

	defer func() {
		if r := recover(); r != nil {
			logError("internal error: %v", r)
			schema, err = nil, errors.New(internalErrorMessage)
		}
	}()

	schema = safeExtractSchema(eventProperties)
	streamId := options.StreamId
	if strings.Contains(streamId, ":") {
		logWarning("streamId contains ':'; using the value verbatim.")
	}
	logIfEnabled("supplied event %q", eventName)

	inspector.mu.Lock()
	samplingRate := inspector.samplingRate
	inspector.mu.Unlock()
	// SPEC.md §7.7: per-event sampling at enqueue.
	if rand.Float64() > samplingRate {
		logIfEnabled("event %q dropped due to sampling rate.", eventName)
		return schema, nil
	}

	event := inspector.newWireEvent(eventName, streamId, samplingRate, schema, options)

	var batch []wireEvent
	dropped := 0
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
		batch = inspector.pending
		inspector.pending = nil
	}
	inspector.mu.Unlock()

	if dropped > 0 {
		logIfEnabled("maxQueueSize exceeded; dropped %d oldest event(s).", dropped)
	}
	if batch == nil {
		return schema, nil
	}
	result := inspector.dispatch(batch)
	if inspector.batchSize == 1 {
		// Immediate-send mode: the outcome of this call's own send is observable (SPEC.md §7.5).
		if res, ok := <-result; ok && res.status == sendNon200 {
			return []Property{}, nil
		}
	}
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
		EventProperties: schema,
	}
}

// dispatch sends batch in a new goroutine, outside the lock, and tracks it so Flush can wait for
// it. The returned channel yields the result once, or is closed without a value if the instance
// was destroyed first.
func (inspector *AvoInspector) dispatch(batch []wireEvent) <-chan sendResult {
	result := make(chan sendResult, 1)
	inspector.mu.Lock()
	if inspector.destroyed {
		inspector.mu.Unlock()
		close(result)
		return result
	}
	id := inspector.nextSendID
	inspector.nextSendID++
	done := make(chan struct{})
	inspector.inFlight[id] = done
	ctx := inspector.ctx
	inspector.mu.Unlock()

	go func() {
		defer close(done)
		res := inspector.avoNetworkCallsHandler.send(ctx, batch)
		inspector.mu.Lock()
		if res.status == sendOk && res.samplingRate != nil {
			inspector.samplingRate = *res.samplingRate
		}
		delete(inspector.inFlight, id)
		inspector.mu.Unlock()
		// SPEC.md §7.5, §12.5: a failed batch is logged and dropped, never re-queued or retried.
		switch res.status {
		case sendOk:
			logIfEnabled("sent %d event(s).", len(batch))
		case sendNon200:
			logIfEnabled("send of %d event(s) failed (%v); the batch is dropped.", len(batch), res.err)
		default:
			logError("send of %d event(s) failed (%v); the batch is dropped.", len(batch), res.err)
		}
		result <- res
	}()
	return result
}

// Flush sends every pending event and waits until all in-flight sends have completed, or until
// timeout (10 seconds when timeout <= 0) has passed. Flush always completes (SPEC.md §4.6): the
// returned error is informational and callers may ignore it. It is ErrFlushTimeout when the timeout
// passed first and nil otherwise; either way the pending events were sent and the inspector stays
// usable. Delivery failures are not reported. Call Flush before the process or serverless handler
// exits: pending events are otherwise lost.
func (inspector *AvoInspector) Flush(timeout time.Duration) error {
	if timeout <= 0 {
		timeout = defaultFlushTimeout
	}
	inspector.mu.Lock()
	if inspector.destroyed {
		inspector.mu.Unlock()
		return nil
	}
	batch := inspector.pending
	inspector.pending = nil
	inspector.mu.Unlock()

	if len(batch) > 0 {
		inspector.dispatch(batch)
	}

	inspector.mu.Lock()
	waiting := make([]chan struct{}, 0, len(inspector.inFlight))
	for _, done := range inspector.inFlight {
		waiting = append(waiting, done)
	}
	inspector.mu.Unlock()

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for _, done := range waiting {
		select {
		case <-done:
		case <-deadline.C:
			return ErrFlushTimeout
		}
	}
	return nil
}

// Destroy discards pending events unsent, abandons in-flight sends and stops the scheduled
// flush. Afterwards TrackSchemaFromEvent sends nothing. It does not flush.
func (inspector *AvoInspector) Destroy() {
	inspector.mu.Lock()
	if inspector.destroyed {
		inspector.mu.Unlock()
		return
	}
	inspector.destroyed = true
	inspector.pending = nil
	inspector.inFlight = map[uint64]chan struct{}{}
	stopTimer := inspector.stopTimer
	inspector.stopTimer = nil
	inspector.mu.Unlock()

	if stopTimer != nil {
		close(stopTimer)
	}
	inspector.cancel()
}

// runBatchTimer flushes the pending batch every period, so no event waits longer than
// batchFlushSeconds (SPEC.md §12.3). It does not keep the process alive.
func (inspector *AvoInspector) runBatchTimer(period time.Duration, stop <-chan struct{}) {
	if period < time.Millisecond {
		period = time.Millisecond
	}
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			inspector.mu.Lock()
			batch := inspector.pending
			inspector.pending = nil
			inspector.mu.Unlock()
			if len(batch) > 0 {
				inspector.dispatch(batch)
			}
		}
	}
}

// setSamplingRate is a test-only hook reached through internal/testhooks; it is deliberately not
// exported, since a public setter would let callers silently disable reporting.
func (inspector *AvoInspector) setSamplingRate(rate float64) {
	inspector.mu.Lock()
	inspector.samplingRate = rate
	inspector.mu.Unlock()
}

// logOutput receives every log line; tests swap it under logMu.
var (
	logMu     sync.Mutex
	logOutput io.Writer = os.Stderr
)

// Log helpers. None of them may be passed the apiKey (SPEC.md §7.5.1).

func logf(format string, args ...interface{}) {
	logMu.Lock()
	defer logMu.Unlock()
	fmt.Fprintf(logOutput, logPrefix+format+"\n", args...)
}

// logWarning always writes; warnings flag caller mistakes (SPEC.md §4.2, §6.3).
func logWarning(format string, args ...interface{}) {
	logf(format, args...)
}

// logError always writes, whatever the logging flag: it reports network errors, timeouts,
// refused sends and internal errors (SPEC.md §4.2, §7.5).
func logError(format string, args ...interface{}) {
	logf(format, args...)
}

// logIfEnabled writes only when logging is enabled: diagnostics, non-200 responses and
// maxQueueSize drops.
func logIfEnabled(format string, args ...interface{}) {
	if shouldLog.Load() {
		logf(format, args...)
	}
}
