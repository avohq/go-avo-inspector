package avoinspector

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewAvoInspector(t *testing.T) {
	inspector, err := NewAvoInspector("API_KEY", Dev, "1.0.0", "MyApp")
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	defer inspector.Destroy()

	if inspector.apiKey != "API_KEY" || inspector.environment != Dev || inspector.version != "1.0.0" || inspector.appName != "MyApp" {
		t.Errorf("constructor options not stored: %+v", inspector)
	}
	if !shouldLog.Load() {
		t.Errorf("expected logging to be enabled in dev")
	}
	if inspector.batchSize != 1 {
		t.Errorf("expected dev to force batchSize 1, got %d", inspector.batchSize)
	}
}

func TestNewAvoInspector_ValidationErrors(t *testing.T) {
	testCases := []struct {
		name     string
		options  Options
		expected string
	}{
		{"empty api key", Options{ApiKey: "", AppVersion: "1.0.0"}, noApiKeyMessage},
		{"whitespace api key", Options{ApiKey: " \t\n", AppVersion: "1.0.0"}, noApiKeyMessage},
		{"api key with CR", Options{ApiKey: "key\rX-Injected: 1", AppVersion: "1.0.0"}, apiKeyControlMessage},
		{"api key with LF", Options{ApiKey: "key\nX-Injected: 1", AppVersion: "1.0.0"}, apiKeyControlMessage},
		{"api key with trailing LF", Options{ApiKey: "key\n", AppVersion: "1.0.0"}, apiKeyControlMessage},
		{"api key with NUL", Options{ApiKey: "key\x00", AppVersion: "1.0.0"}, apiKeyControlMessage},
		{"empty version", Options{ApiKey: "key", AppVersion: ""}, noVersionMessage},
		{"whitespace version", Options{ApiKey: "key", AppVersion: "   "}, noVersionMessage},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			inspector, err := NewAvoInspectorWithOptions(tc.options)
			if err == nil || inspector != nil {
				t.Fatalf("expected an error, got inspector %v", inspector)
			}
			if err.Error() != tc.expected {
				t.Errorf("expected error %q, got %q", tc.expected, err.Error())
			}
		})
	}

	if noVersionMessage != "[Avo Inspector] No version provided. Many features of Inspector rely on versioning. Please provide comparable string version, i.e. integer or semantic." {
		t.Errorf("version message drifted from SPEC.md §4.1: %q", noVersionMessage)
	}
}

func TestNewAvoInspector_EnvFallsBackToDev(t *testing.T) {
	for _, env := range []AvoInspectorEnv{"", "production", "DEV"} {
		inspector := mustInspector(t, Options{Env: env, BatchSize: 30})
		if inspector.environment != Dev || inspector.batchSize != 1 {
			t.Errorf("env %q: expected dev with batchSize 1, got %s with %d", env, inspector.environment, inspector.batchSize)
		}
	}
}

func TestNewAvoInspector_BatchDefaults(t *testing.T) {
	inspector := mustInspector(t, Options{Env: Staging})
	if inspector.batchSize != 30 || inspector.batchFlushSeconds != 30 || inspector.maxQueueSize != 1000 {
		t.Errorf("unexpected defaults: batchSize %d, batchFlushSeconds %v, maxQueueSize %d",
			inspector.batchSize, inspector.batchFlushSeconds, inspector.maxQueueSize)
	}
	invalid := mustInspector(t, Options{Env: Prod, BatchSize: -1, BatchFlushSeconds: -2, MaxQueueSize: -3})
	if invalid.batchSize != 30 || invalid.batchFlushSeconds != 30 || invalid.maxQueueSize != 1000 {
		t.Errorf("invalid values should fall back to defaults, got %d %v %d",
			invalid.batchSize, invalid.batchFlushSeconds, invalid.maxQueueSize)
	}
}

func TestLoggingIsProcessWide(t *testing.T) {
	first := mustInspector(t, Options{Env: Prod})
	second := mustInspector(t, Options{Env: Prod})
	if shouldLog.Load() {
		t.Fatalf("expected logging off after constructing a prod instance")
	}
	first.EnableLogging(true)
	if !shouldLog.Load() {
		t.Errorf("EnableLogging(true) on one instance must enable logging for all")
	}
	second.ShouldLog(false)
	if shouldLog.Load() {
		t.Errorf("deprecated ShouldLog(false) must disable the process-wide flag")
	}
}

func TestExtractSchema_PublicMethodsNeverPanic(t *testing.T) {
	inspector := mustInspector(t, Options{Env: Prod})
	if schema := inspector.ExtractSchema(nil); schema == nil || len(schema) != 0 {
		t.Errorf("expected empty schema for nil, got %#v", schema)
	}
	if schema := inspector.ExtractOrderedSchema(nil); schema == nil || len(schema) != 0 {
		t.Errorf("expected empty schema for nil, got %#v", schema)
	}
	schema := inspector.ExtractSchema(map[string]interface{}{"b": 1, "a": "x"})
	if len(schema) != 2 || schema[0].PropertyName != "a" || schema[1].PropertyType != "int" {
		t.Errorf("unexpected schema %#v", schema)
	}
}

func TestTrack_DevSendsImmediatelyAndReturnsSchema(t *testing.T) {
	server := newTestServer(t, nil)
	inspector := mustInspector(t, Options{Env: Dev, AppName: "MyApp"})

	schema, err := inspector.TrackSchemaFromEvent("TestEvent", map[string]interface{}{"param1": "value1", "param2": 123})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(schema) != 2 || schema[0].PropertyName != "param1" || schema[1].PropertyType != "int" {
		t.Errorf("unexpected schema %#v", schema)
	}
	if n := len(server.captured()); n != 1 {
		t.Fatalf("expected the send to complete within the dev-mode call, got %d requests", n)
	}
}

// A non-200 is not an error of TrackSchemaFromEvent: in immediate-send mode it resolves with an
// empty schema, and a network failure resolves with the extracted schema (SPEC.md §7.5).
func TestTrack_HTTPFailuresAreNotReturnedAsErrors(t *testing.T) {
	newTestServer(t, respondWith(400, `{"ok":false,"error":"bad key"}`))
	inspector := mustInspector(t, Options{Env: Dev})
	schema, err := inspector.TrackSchemaFromEvent("Event", map[string]interface{}{"form_id": "signup"})
	if err != nil || schema == nil || len(schema) != 0 {
		t.Errorf("non-200: expected ([], nil), got (%#v, %v)", schema, err)
	}

	t.Setenv(mockEndpointEnvVar, "http://127.0.0.1:1")
	schema, err = inspector.TrackSchemaFromEvent("Event", map[string]interface{}{"form_id": "signup"})
	if err != nil || len(schema) != 1 {
		t.Errorf("network error: expected (schema, nil), got (%#v, %v)", schema, err)
	}
}

func TestTrack_SamplingRateZeroDropsWithoutSending(t *testing.T) {
	server := newTestServer(t, nil)
	inspector := mustInspector(t, Options{Env: Dev})
	inspector.setSamplingRate(0)
	for i := 0; i < 50; i++ {
		schema, err := inspector.TrackSchemaFromEvent("Dropped", map[string]interface{}{"x": 1})
		if err != nil || len(schema) != 1 {
			t.Fatalf("sampled-out event must still return its schema, got (%#v, %v)", schema, err)
		}
	}
	_ = inspector.Flush(time.Second)
	if n := len(server.captured()); n != 0 {
		t.Errorf("expected no requests at samplingRate 0, got %d", n)
	}
}

// {"success":false} carries no samplingRate and must leave the rate unchanged; before 1.1.0 it
// unmarshalled to 0 and silently dropped every later event.
func TestSamplingRate_UpdatedOnlyFromValid200Bodies(t *testing.T) {
	testCases := []struct {
		name     string
		status   int
		body     string
		expected float64
	}{
		{"success false", 200, `{"success":false}`, 0.5},
		{"ok false", 200, `{"ok":false}`, 0.5},
		{"empty body", 200, ``, 0.5},
		{"numeric rate", 200, `{"samplingRate":0.25,"success":true}`, 0.25},
		{"zero rate", 200, `{"samplingRate":0}`, 0},
		{"rate above range", 200, `{"samplingRate":1.5}`, 0.5},
		{"rate below range", 200, `{"samplingRate":-0.1}`, 0.5},
		{"string rate", 200, `{"samplingRate":"0.2"}`, 0.5},
		{"non-200 with rate", 500, `{"samplingRate":0.1}`, 0.5},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			newTestServer(t, respondWith(tc.status, tc.body))
			inspector := mustInspector(t, Options{Env: Dev})
			inspector.setSamplingRate(0.5)
			// Send directly, bypassing the per-event sampling decision.
			event := inspector.newWireEvent("E", "", 0.5, []Property{}, TrackOptions{})
			inspector.mu.Lock()
			send := inspector.registerSend([]wireEvent{event})
			inspector.mu.Unlock()
			<-inspector.startSend(send)
			inspector.mu.Lock()
			rate := inspector.samplingRate
			inspector.mu.Unlock()
			if rate != tc.expected {
				t.Errorf("expected samplingRate %v, got %v", tc.expected, rate)
			}
		})
	}
}

func TestTrack_BodyCarriesSamplingRateAtEnqueue(t *testing.T) {
	server := newTestServer(t, respondWith(200, `{"samplingRate":1}`))
	inspector := mustInspector(t, Options{Env: Staging, BatchSize: 30, DisableBatchTimer: true})
	inspector.setSamplingRate(1)
	_, _ = inspector.TrackSchemaFromEvent("E1", nil)
	inspector.setSamplingRate(0.999999)
	for i := 0; ; i++ {
		// Retry the (very likely) sampled-in second event until it is enqueued.
		inspector.mu.Lock()
		n := len(inspector.pending)
		inspector.mu.Unlock()
		if n == 2 || i > 100 {
			break
		}
		_, _ = inspector.TrackSchemaFromEvent("E2", nil)
	}
	_ = inspector.Flush(time.Second)
	requests := server.captured()
	if len(requests) != 1 || len(requests[0].events) != 2 {
		t.Fatalf("expected one batch of two events, got %d requests", len(requests))
	}
	if requests[0].events[0]["samplingRate"] != 1.0 || requests[0].events[1]["samplingRate"] != 0.999999 {
		t.Errorf("expected per-event snapshots 1 and 0.999999, got %v and %v",
			requests[0].events[0]["samplingRate"], requests[0].events[1]["samplingRate"])
	}
}

func TestBatching_SizeTriggerAndFlush(t *testing.T) {
	server := newTestServer(t, nil)
	inspector := mustInspector(t, Options{Env: Staging, BatchSize: 3, DisableBatchTimer: true})
	for i := 1; i <= 4; i++ {
		schema, err := inspector.TrackSchemaFromEvent(fmt.Sprintf("E%d", i), map[string]interface{}{"a": i})
		if err != nil || len(schema) != 1 {
			t.Fatalf("track %d: unexpected (%#v, %v)", i, schema, err)
		}
	}
	if err := inspector.Flush(time.Second); err != nil {
		t.Fatalf("flush: %v", err)
	}
	requests := server.captured()
	if len(requests) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(requests))
	}
	sizes := map[int]bool{len(requests[0].events): true, len(requests[1].events): true}
	if !sizes[3] || !sizes[1] {
		t.Errorf("expected batches of 3 and 1, got %d and %d", len(requests[0].events), len(requests[1].events))
	}
}

func TestBatching_MaxQueueSizeDropsOldest(t *testing.T) {
	server := newTestServer(t, nil)
	inspector := mustInspector(t, Options{Env: Staging, BatchSize: 30, MaxQueueSize: 2, DisableBatchTimer: true})
	for _, name := range []string{"E1", "E2", "E3"} {
		_, _ = inspector.TrackSchemaFromEvent(name, nil)
	}
	_ = inspector.Flush(time.Second)
	requests := server.captured()
	if len(requests) != 1 || !reflect.DeepEqual(eventNames(requests[0]), []string{"E2", "E3"}) {
		t.Errorf("expected one batch [E2 E3], got %v", requests)
	}
}

// SPEC.md §12.3: a partial batch is sent by the scheduled flush without any further calls.
func TestBatching_TimerFlushesIdleBatch(t *testing.T) {
	server := newTestServer(t, nil)
	inspector := mustInspector(t, Options{Env: Staging, BatchSize: 30, BatchFlushSeconds: 0.05})
	_, _ = inspector.TrackSchemaFromEvent("Idle", nil)
	deadline := time.Now().Add(2 * time.Second)
	for len(server.captured()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := len(server.captured()); n != 1 {
		t.Fatalf("expected the scheduled flush to send the idle batch, got %d requests", n)
	}
}

func TestBatching_DisableBatchTimer(t *testing.T) {
	server := newTestServer(t, nil)
	inspector := mustInspector(t, Options{Env: Staging, BatchSize: 30, BatchFlushSeconds: 0.02, DisableBatchTimer: true})
	_, _ = inspector.TrackSchemaFromEvent("Idle", nil)
	time.Sleep(200 * time.Millisecond)
	if n := len(server.captured()); n != 0 {
		t.Errorf("expected no scheduled flush with DisableBatchTimer, got %d requests", n)
	}
	if inspector.flushTimer != nil {
		t.Errorf("expected no flush timer")
	}
}

// SPEC.md §12.5: a batch that fails in transit (here, the connection is dropped) is not re-queued.
func TestBatching_TransientFailureDropsBatch(t *testing.T) {
	server := newTestServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		if n == 0 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		_, _ = w.Write([]byte(`{"samplingRate":1}`))
	})
	inspector := mustInspector(t, Options{Env: Staging, BatchSize: 2, DisableBatchTimer: true})
	for _, name := range []string{"E1", "E2"} {
		_, _ = inspector.TrackSchemaFromEvent(name, nil)
	}
	_ = inspector.Flush(time.Second)
	for _, name := range []string{"E3", "E4"} {
		_, _ = inspector.TrackSchemaFromEvent(name, nil)
	}
	_ = inspector.Flush(time.Second)
	_ = inspector.Flush(time.Second)

	requests := server.captured()
	if len(requests) != 2 {
		t.Fatalf("expected 2 requests (the dropped one and the next), got %d", len(requests))
	}
	if !reflect.DeepEqual(eventNames(requests[1]), []string{"E3", "E4"}) {
		t.Errorf("failed batch must not be re-queued; second batch was %v", eventNames(requests[1]))
	}
}

func TestDestroy_DiscardsPendingAndStopsTracking(t *testing.T) {
	server := newTestServer(t, nil)
	inspector := mustInspector(t, Options{Env: Staging, BatchSize: 30, BatchFlushSeconds: 0.02})
	inspector.setSamplingRate(0.75)
	_, _ = inspector.TrackSchemaFromEvent("E1", nil)
	inspector.Destroy()

	schema, err := inspector.TrackSchemaFromEvent("E2", map[string]interface{}{"a": 1})
	if err != nil || schema == nil || len(schema) != 0 {
		t.Errorf("after Destroy expected ([], nil), got (%#v, %v)", schema, err)
	}
	if err := inspector.Flush(time.Second); err != nil {
		t.Errorf("flush after destroy: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(server.captured()); n != 0 {
		t.Errorf("expected no requests after Destroy, got %d", n)
	}
	inspector.mu.Lock()
	defer inspector.mu.Unlock()
	if inspector.samplingRate != 0.75 || len(inspector.pending) != 0 || len(inspector.inFlight) != 0 || inspector.flushTimer != nil {
		t.Errorf("unexpected post-destroy state: rate %v pending %d inFlight %d", inspector.samplingRate, len(inspector.pending), len(inspector.inFlight))
	}
	if inspector.apiKey != "test-key" || inspector.version != "1.0.0" {
		t.Errorf("constructor options must persist after Destroy")
	}
}

func TestFlush_TimesOutOnHungSend(t *testing.T) {
	release := make(chan struct{})
	newTestServer(t, func(int, http.ResponseWriter, *http.Request) { <-release })
	defer close(release)
	inspector := mustInspector(t, Options{Env: Staging, BatchSize: 30, DisableBatchTimer: true})
	_, _ = inspector.TrackSchemaFromEvent("E1", nil)
	start := time.Now()
	if err := inspector.Flush(50 * time.Millisecond); err != ErrFlushTimeout {
		t.Errorf("expected ErrFlushTimeout, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("flush did not honor its timeout: %v", elapsed)
	}
	// The instance stays usable after a flush.
	inspector.Destroy()
}

// SPEC.md §3.1, §12.4: concurrent enqueue and flush lose and duplicate nothing.
func TestBatching_ConcurrentTracksAreSentExactlyOnce(t *testing.T) {
	server := newTestServer(t, nil)
	inspector := mustInspector(t, Options{Env: Staging, BatchSize: 10, DisableBatchTimer: true})
	const count = 300
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = inspector.TrackSchemaFromEvent(fmt.Sprintf("C%d", i), nil)
			if i%50 == 0 {
				_ = inspector.Flush(time.Second)
			}
		}(i)
	}
	wg.Wait()
	_ = inspector.Flush(5 * time.Second)

	seen := map[string]bool{}
	total := 0
	for _, request := range server.captured() {
		for _, event := range request.events {
			total++
			seen[event["messageId"].(string)] = true
		}
	}
	if total != count || len(seen) != count {
		t.Errorf("expected %d unique events, got %d events with %d unique messageIds", count, total, len(seen))
	}
}

// captureLogs redirects log output for the rest of the test and returns a reader of it.
func captureLogs(t *testing.T) func() string {
	t.Helper()
	resetLogLimiter(t)
	buffer := &strings.Builder{}
	logMu.Lock()
	previous := logOutput
	logOutput = buffer
	logMu.Unlock()
	t.Cleanup(func() {
		logMu.Lock()
		logOutput = previous
		logMu.Unlock()
	})
	return func() string {
		logMu.Lock()
		defer logMu.Unlock()
		return buffer.String()
	}
}

// Send failures and internal errors are logged even with logging off, and never include the
// apiKey (SPEC.md §4.2, §7.5, §7.5.1).
func TestLogging_FailuresAreLoggedWhenLoggingIsOff(t *testing.T) {
	const apiKey = "secret-key-123"
	newQuietInspector := func(t *testing.T) *AvoInspector {
		inspector := mustInspector(t, Options{ApiKey: apiKey, Env: Staging, BatchSize: 30, DisableBatchTimer: true})
		inspector.EnableLogging(false)
		return inspector
	}
	testCases := []struct {
		name     string
		setup    func(t *testing.T, inspector *AvoInspector)
		expected string
	}{
		{"network error", func(t *testing.T, _ *AvoInspector) {
			t.Setenv(mockEndpointEnvVar, "http://127.0.0.1:1")
		}, "Request failed"},
		{"timeout", func(t *testing.T, inspector *AvoInspector) {
			release := make(chan struct{})
			newTestServer(t, func(int, http.ResponseWriter, *http.Request) { <-release })
			t.Cleanup(func() { close(release) })
			inspector.avoNetworkCallsHandler.client.Timeout = 50 * time.Millisecond
		}, "Request timed out"},
		{"header guard", func(t *testing.T, inspector *AvoInspector) {
			newTestServer(t, nil)
			inspector.avoNetworkCallsHandler.apiKey = apiKey + "\n"
		}, errUnsafeHeader.Error()},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			inspector := newQuietInspector(t)
			tc.setup(t, inspector)
			_, _ = inspector.TrackSchemaFromEvent("E", nil)
			_ = inspector.Flush(2 * time.Second)
			output := logs()
			if !strings.Contains(output, tc.expected) {
				t.Errorf("expected a log containing %q, got %q", tc.expected, output)
			}
			if strings.Contains(output, apiKey) {
				t.Errorf("log must not contain the apiKey: %q", output)
			}
		})
	}

	t.Run("internal error", func(t *testing.T) {
		logs := captureLogs(t)
		inspector := newQuietInspector(t)
		previous := newGuid
		newGuid = func() string { panic("boom") }
		t.Cleanup(func() { newGuid = previous })
		schema, err := inspector.TrackSchemaFromEvent("E", nil)
		if err == nil || err.Error() != internalErrorMessage || schema != nil {
			t.Fatalf("expected the internal error, got (%#v, %v)", schema, err)
		}
		if output := logs(); !strings.Contains(output, "internal error: boom") || strings.Contains(output, apiKey) {
			t.Errorf("expected an internal error log without the apiKey, got %q", output)
		}
	})
}

// Flush(0) sends the pending events without waiting for them, as Node and Java do; a negative
// timeout waits up to DefaultFlushTimeout.
func TestFlush_ZeroSendsWithoutWaiting(t *testing.T) {
	release := make(chan struct{})
	server := newTestServer(t, func(int, http.ResponseWriter, *http.Request) { <-release })
	defer close(release)
	inspector := mustInspector(t, Options{Env: Staging, BatchSize: 30, DisableBatchTimer: true})
	_, _ = inspector.TrackSchemaFromEvent("E1", nil)
	start := time.Now()
	// Not waiting was asked for, so it is not a timeout.
	if err := inspector.Flush(0); err != nil {
		t.Errorf("expected nil from Flush(0) while the send is still in flight, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("Flush(0) waited %v", elapsed)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(server.captured()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := len(server.captured()); n != 1 {
		t.Errorf("Flush(0) must still send the pending batch, got %d requests", n)
	}
}

func TestFlush_NegativeWaitsForInFlightSends(t *testing.T) {
	server := newTestServer(t, nil)
	inspector := mustInspector(t, Options{Env: Staging, BatchSize: 30, DisableBatchTimer: true})
	_, _ = inspector.TrackSchemaFromEvent("E1", nil)
	if err := inspector.Flush(-1); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
	if n := len(server.captured()); n != 1 {
		t.Errorf("expected the send to have completed, got %d requests", n)
	}
}

// An idle inspector runs nothing: the flush timer is armed only while events are pending, so
// tracking starts no goroutine and a flushed inspector is back to the baseline.
func TestBatchTimer_IdleInspectorHoldsNoGoroutine(t *testing.T) {
	t.Setenv(mockEndpointEnvVar, "http://127.0.0.1:1")
	baseline := runtime.NumGoroutine()
	inspector := mustInspector(t, Options{Env: Staging, BatchSize: 30, BatchFlushSeconds: 3600})
	inspector.EnableLogging(false)
	_, _ = inspector.TrackSchemaFromEvent("E1", nil)
	if n := runtime.NumGoroutine(); n > baseline {
		t.Errorf("an armed flush timer must not hold a goroutine: %d goroutines, baseline %d", n, baseline)
	}
	_ = inspector.Flush(2 * time.Second)
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > baseline {
		t.Errorf("after Flush, %d goroutines remain, baseline %d", n, baseline)
	}
}

// An inspector dropped without Destroy is garbage-collected once its batch is flushed.
func TestBatchTimer_DroppedInspectorIsCollected(t *testing.T) {
	t.Setenv(mockEndpointEnvVar, "http://127.0.0.1:1")
	collected := make(chan struct{})
	func() {
		inspector, err := NewAvoInspectorWithOptions(Options{ApiKey: "k", Env: Staging, AppVersion: "1", BatchSize: 30, BatchFlushSeconds: 3600})
		if err != nil {
			t.Fatal(err)
		}
		inspector.EnableLogging(false)
		_, _ = inspector.TrackSchemaFromEvent("E1", nil)
		_ = inspector.Flush(2 * time.Second)
		runtime.SetFinalizer(inspector, func(*AvoInspector) { close(collected) })
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		select {
		case <-collected:
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Errorf("an idle inspector dropped without Destroy was never collected")
}

// The timer fires once per batch, BatchFlushSeconds after the first event entered it, and is
// disarmed whenever the batch is swapped out.
func TestBatchTimer_ArmedOnlyWhilePending(t *testing.T) {
	server := newTestServer(t, nil)
	inspector := mustInspector(t, Options{Env: Staging, BatchSize: 2, BatchFlushSeconds: 3600})
	armed := func() bool {
		inspector.mu.Lock()
		defer inspector.mu.Unlock()
		return inspector.flushTimer != nil
	}
	if armed() {
		t.Fatalf("timer armed before any event")
	}
	_, _ = inspector.TrackSchemaFromEvent("E1", nil)
	if !armed() {
		t.Fatalf("timer not armed by the first event")
	}
	_, _ = inspector.TrackSchemaFromEvent("E2", nil) // size trigger swaps the batch out
	if armed() {
		t.Errorf("timer still armed after the size trigger")
	}
	_, _ = inspector.TrackSchemaFromEvent("E3", nil)
	_ = inspector.Flush(2 * time.Second)
	if armed() {
		t.Errorf("timer still armed after Flush")
	}
	if n := len(server.captured()); n != 2 {
		t.Errorf("expected 2 requests, got %d", n)
	}
}

// A batch that has left the buffer but whose send has not started yet must already count as in
// flight: Flush called in that window has to wait for it.
func TestFlush_WaitsForBatchTakenButNotYetSent(t *testing.T) {
	server := newTestServer(t, nil)
	inspector := mustInspector(t, Options{Env: Staging, BatchSize: 1, DisableBatchTimer: true})
	flushed := make(chan int, 1)
	testHookBeforeSend = func() {
		testHookBeforeSend = nil
		go func() {
			_ = inspector.Flush(5 * time.Second)
			flushed <- len(server.captured())
		}()
		// Hold the window open long enough for that Flush to run inside it.
		time.Sleep(200 * time.Millisecond)
	}
	t.Cleanup(func() { testHookBeforeSend = nil })
	_, _ = inspector.TrackSchemaFromEvent("E1", nil)
	if n := <-flushed; n != 1 {
		t.Errorf("Flush returned before the taken batch was sent: %d requests captured", n)
	}
}

// A batch size above the queue cap can never trigger a send, so the constructor always warns. The
// size is not clamped, as in Node: batch-4 requires FIFO overflow into a single flushed batch.
func TestNewAvoInspector_WarnsWhenBatchSizeExceedsMaxQueueSize(t *testing.T) {
	logs := captureLogs(t)
	inspector := mustInspector(t, Options{Env: Staging, BatchSize: 30, MaxQueueSize: 2, DisableBatchTimer: true})
	inspector.EnableLogging(false)
	if !strings.Contains(logs(), "batchSize 30 is larger than maxQueueSize 2") {
		t.Errorf("expected a warning, got %q", logs())
	}
	if inspector.batchSize != 30 {
		t.Errorf("batchSize must not be clamped, got %d", inspector.batchSize)
	}

	quiet := captureLogs(t)
	mustInspector(t, Options{Env: Staging, BatchSize: 2, MaxQueueSize: 2})
	mustInspector(t, Options{Env: Dev, BatchSize: 30, MaxQueueSize: 2})
	if strings.Contains(quiet(), "larger than maxQueueSize") {
		t.Errorf("no warning expected when the batch fits or in dev, got %q", quiet())
	}
}

// A huge or infinite BatchFlushSeconds is capped at 24 hours with a warning, instead of
// overflowing the timer duration into an immediate flush. NaN is invalid and uses the default.
func TestNewAvoInspector_BatchFlushSecondsIsCapped(t *testing.T) {
	testCases := []struct {
		value    float64
		expected float64
	}{
		{math.Inf(1), maxBatchFlushSeconds},
		{1e300, maxBatchFlushSeconds},
		{1e10, maxBatchFlushSeconds},
		{math.NaN(), defaultBatchFlushSeconds},
	}
	for _, tc := range testCases {
		t.Run(fmt.Sprint(tc.value), func(t *testing.T) {
			server := newTestServer(t, nil)
			logs := captureLogs(t)
			inspector := mustInspector(t, Options{Env: Staging, BatchSize: 30, BatchFlushSeconds: tc.value})
			inspector.EnableLogging(false)
			if inspector.batchFlushSeconds != tc.expected {
				t.Errorf("expected batchFlushSeconds %v, got %v", tc.expected, inspector.batchFlushSeconds)
			}
			if !strings.Contains(logs(), "batchFlushSeconds") {
				t.Errorf("expected a warning, got %q", logs())
			}
			_, _ = inspector.TrackSchemaFromEvent("E1", nil)
			time.Sleep(200 * time.Millisecond)
			if n := len(server.captured()); n != 0 {
				t.Errorf("the timer fired early: %d requests", n)
			}
		})
	}
}

// Every Unicode control character (Cc: C0, DEL and C1) except tab is rejected by the constructor
// and refused by the send-time guard. CR, LF and NUL get the spec's message, the others their own.
func TestNewAvoInspector_RejectsEveryControlCharacterButTab(t *testing.T) {
	server := newTestServer(t, nil)
	for _, apiKey := range []string{"key\x01", "key\x1bx", "key\x7f", "k\x0bey", "k\x0cey", "key\u0080", "key\u0085", "key\u009f"} {
		inspector, err := NewAvoInspectorWithOptions(Options{ApiKey: apiKey, AppVersion: "1.0.0"})
		if err == nil || err.Error() != apiKeyOtherControlMessage || inspector != nil {
			t.Errorf("apiKey %q: expected %q, got (%v, %v)", apiKey, apiKeyOtherControlMessage, inspector, err)
		}
		result := newAvoNetworkCallsHandler(apiKey, Dev).send(context.Background(), []wireEvent{{EventProperties: []Property{}}})
		if !errors.Is(result.err, errUnsafeHeader) {
			t.Errorf("apiKey %q: send-time guard did not refuse it: %v", apiKey, result.err)
		}
	}
	// CR, LF and NUL keep the spec's exact message, even alongside another control character.
	for _, apiKey := range []string{"key\r", "key\n", "key\x00", "key\x01\n"} {
		_, err := NewAvoInspectorWithOptions(Options{ApiKey: apiKey, AppVersion: "1.0.0"})
		if err == nil || err.Error() != apiKeyControlMessage {
			t.Errorf("apiKey %q: expected the spec message, got %v", apiKey, err)
		}
	}
	if n := len(server.captured()); n != 0 {
		t.Fatalf("a control character reached the wire: %d requests", n)
	}
	for _, apiKey := range []string{"key\twith-tab", "key\u00a0nbsp", "clé"} {
		if inspector := mustInspector(t, Options{ApiKey: apiKey}); inspector.apiKey != apiKey {
			t.Errorf("apiKey %q is not a control character and must be kept", apiKey)
		}
	}
}

// A send that Destroy abandons is not a failure and is not logged, as in Node and Java.
func TestDestroy_AbandonedSendIsNotLogged(t *testing.T) {
	release := make(chan struct{})
	newTestServer(t, func(int, http.ResponseWriter, *http.Request) { <-release })
	defer close(release)
	logs := captureLogs(t)
	inspector := mustInspector(t, Options{Env: Staging, BatchSize: 30, DisableBatchTimer: true})
	inspector.EnableLogging(false)
	_, _ = inspector.TrackSchemaFromEvent("E1", nil)
	_ = inspector.Flush(0)
	inspector.mu.Lock()
	var sends []chan struct{}
	for _, done := range inspector.inFlight {
		sends = append(sends, done)
	}
	inspector.mu.Unlock()
	if len(sends) != 1 {
		t.Fatalf("expected one send in flight, got %d", len(sends))
	}
	inspector.Destroy()
	select {
	case <-sends[0]:
	case <-time.After(2 * time.Second):
		t.Fatalf("Destroy did not abandon the send")
	}
	if output := logs(); output != "" {
		t.Errorf("expected no log for an abandoned send, got %q", output)
	}
}

// An apiKey that is not valid UTF-8 is rejected at construction and refused at send. Valid
// multibyte characters, whose continuation bytes fall in 0x80-0xBF, are accepted.
func TestNewAvoInspector_RejectsInvalidUTF8(t *testing.T) {
	server := newTestServer(t, nil)
	for _, apiKey := range []string{"key\x85", "key\xff", "\xc4"} {
		inspector, err := NewAvoInspectorWithOptions(Options{ApiKey: apiKey, AppVersion: "1.0.0"})
		if err == nil || err.Error() != apiKeyUTF8Message || inspector != nil {
			t.Errorf("apiKey %q: expected %q, got (%v, %v)", apiKey, apiKeyUTF8Message, inspector, err)
		}
		result := newAvoNetworkCallsHandler(apiKey, Dev).send(context.Background(), []wireEvent{{EventProperties: []Property{}}})
		if !errors.Is(result.err, errUnsafeHeader) {
			t.Errorf("apiKey %q: send-time guard did not refuse it: %v", apiKey, result.err)
		}
	}
	if n := len(server.captured()); n != 0 {
		t.Fatalf("an invalid apiKey reached the wire: %d requests", n)
	}
	inspector := mustInspector(t, Options{ApiKey: "klucz-ą", Env: Dev})
	_, _ = inspector.TrackSchemaFromEvent("E", nil)
	if requests := server.captured(); len(requests) != 1 || requests[0].header.Get("api-key") != "klucz-ą" {
		t.Errorf("a valid multibyte apiKey must be accepted and sent, got %d requests", len(requests))
	}
}

// Logs show the event's schema (property names and types), never raw property values or the
// apiKey, even with logging on and across tracking, send failures and Flush. Logging is
// process-wide, so a dev instance can turn it on for a prod one.
func TestLogging_NeverShowsPropertyValues(t *testing.T) {
	const marker = "PII-MARKER-123@example.com"
	const apiKey = "secret-key-456"
	properties := map[string]interface{}{
		"email":  marker,
		"nested": map[string]interface{}{"note": marker},
		"tags":   []interface{}{marker},
	}
	logs := captureLogs(t)

	server := newTestServer(t, respondWith(500, `{}`))
	dev := mustInspector(t, Options{ApiKey: apiKey, Env: Dev})
	_, _ = dev.TrackSchemaFromEvent("Signed Up", properties)

	staging := mustInspector(t, Options{ApiKey: apiKey, Env: Staging, BatchSize: 30, MaxQueueSize: 1, DisableBatchTimer: true})
	staging.EnableLogging(true)
	_, _ = staging.TrackSchemaFromEventWithOptions("Signed Up", properties, TrackOptions{StreamId: "s:1", OriginHint: "web"})
	_, _ = staging.TrackSchemaFromEvent("Signed Up", properties)
	_ = staging.Flush(2 * time.Second)
	server.Close()
	_, _ = staging.TrackSchemaFromEvent("Signed Up", properties)
	_ = staging.Flush(2 * time.Second)

	output := logs()
	for _, secret := range []string{marker, apiKey} {
		if strings.Contains(output, secret) {
			t.Errorf("logs contain %q:\n%s", secret, output)
		}
	}
	for _, expected := range []string{
		`"propertyName":"email","propertyType":"string"`,
		`"propertyName":"note","propertyType":"string"`,
		`"propertyName":"tags","propertyType":"list(string)"`,
		"rejected with HTTP 500",
		"schema sending failed: Request failed.",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("logs lack %q:\n%s", expected, output)
		}
	}
}

// resetLogLimiter clears the always-on log rate limit, so earlier tests cannot suppress this one's
// lines, and restores the real clock afterwards.
func resetLogLimiter(t *testing.T) {
	logLimiter.Lock()
	logLimiter.entries = map[string]*limitedLog{}
	logLimiter.now = time.Now
	logLimiter.Unlock()
	t.Cleanup(func() {
		logLimiter.Lock()
		logLimiter.entries = map[string]*limitedLog{}
		logLimiter.now = time.Now
		logLimiter.Unlock()
	})
}

// fakeLogClock makes the log rate limit read a clock the test advances.
func fakeLogClock(t *testing.T) (advance func(time.Duration)) {
	var mu sync.Mutex
	current := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	logLimiter.Lock()
	logLimiter.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return current
	}
	logLimiter.Unlock()
	return func(d time.Duration) {
		mu.Lock()
		current = current.Add(d)
		mu.Unlock()
	}
}

func countLines(output, fragment string) int {
	n := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, fragment) {
			n++
		}
	}
	return n
}

// Events dropped because the buffer is full are data loss: always logged, once per 10s window, and
// the suppressed count is reported with the next line.
func TestLogging_DroppedEventsAreRateLimited(t *testing.T) {
	logs := captureLogs(t)
	advance := fakeLogClock(t)
	inspector := mustInspector(t, Options{ApiKey: "secret-key-789", Env: Staging, BatchSize: 30, MaxQueueSize: 2, DisableBatchTimer: true})
	inspector.EnableLogging(false)
	for i := 0; i < 10; i++ {
		_, _ = inspector.TrackSchemaFromEvent("E", map[string]interface{}{"email": "PII-MARKER-123@example.com"})
	}
	if n := countLines(logs(), "(queue full)"); n != 1 {
		t.Fatalf("expected one dropped line in the window, got %d:\n%s", n, logs())
	}
	if !strings.Contains(logs(), "[Avo Inspector] dropped 1 event(s) (queue full) in the last 10s.") {
		t.Errorf("unexpected first line:\n%s", logs())
	}
	advance(logRateWindow)
	_, _ = inspector.TrackSchemaFromEvent("E", nil)
	if !strings.Contains(logs(), "[Avo Inspector] dropped 8 event(s) (queue full) in the last 10s.") {
		t.Errorf("expected the 7 suppressed drops plus this one to be reported:\n%s", logs())
	}
	for _, secret := range []string{"secret-key-789", "PII-MARKER-123"} {
		if strings.Contains(logs(), secret) {
			t.Errorf("logs contain %q", secret)
		}
	}
}

// Rejected batches are data loss: always logged, one line per status per 10s window.
func TestLogging_Non200IsRateLimitedPerStatus(t *testing.T) {
	status := 500
	var mu sync.Mutex
	newTestServer(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		code := status
		mu.Unlock()
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"ok":false,"error":"secret-body"}`))
	})
	logs := captureLogs(t)
	advance := fakeLogClock(t)
	inspector := mustInspector(t, Options{ApiKey: "secret-key-789", Env: Staging, BatchSize: 1, DisableBatchTimer: true})
	inspector.EnableLogging(false)
	for i := 0; i < 5; i++ {
		_, _ = inspector.TrackSchemaFromEvent("E", nil)
	}
	mu.Lock()
	status = 400
	mu.Unlock()
	for i := 0; i < 3; i++ {
		_, _ = inspector.TrackSchemaFromEvent("E", nil)
	}
	output := logs()
	if countLines(output, "rejected with HTTP 500") != 1 || countLines(output, "rejected with HTTP 400") != 1 {
		t.Fatalf("expected one line per status:\n%s", output)
	}
	if !strings.Contains(output, "[Avo Inspector] 1 batch(es) rejected with HTTP 500 in the last 10s.") {
		t.Errorf("unexpected 500 line:\n%s", output)
	}
	advance(logRateWindow)
	mu.Lock()
	status = 500
	mu.Unlock()
	_, _ = inspector.TrackSchemaFromEvent("E", nil)
	if !strings.Contains(logs(), "[Avo Inspector] 5 batch(es) rejected with HTTP 500 in the last 10s.") {
		t.Errorf("expected the 4 suppressed rejections plus this one:\n%s", logs())
	}
	for _, secret := range []string{"secret-key-789", "secret-body"} {
		if strings.Contains(logs(), secret) {
			t.Errorf("logs contain %q", secret)
		}
	}
}

// A storm of failed sends prints at most one line per window, then reports how many were
// suppressed.
func TestLogging_FailureStormIsRateLimited(t *testing.T) {
	logs := captureLogs(t)
	advance := fakeLogClock(t)
	t.Setenv(mockEndpointEnvVar, "http://127.0.0.1:1")
	inspector := mustInspector(t, Options{ApiKey: "secret-key-789", Env: Staging, BatchSize: 1, DisableBatchTimer: true})
	inspector.EnableLogging(false)
	for i := 0; i < 5; i++ {
		_, _ = inspector.TrackSchemaFromEvent("E", nil)
	}
	if n := countLines(logs(), "schema sending failed"); n != 1 {
		t.Fatalf("expected one failed line in the window, got %d:\n%s", n, logs())
	}
	if !strings.Contains(logs(), "[Avo Inspector] schema sending failed: Request failed.\n") {
		t.Errorf("unexpected failed line:\n%s", logs())
	}
	advance(logRateWindow)
	_, _ = inspector.TrackSchemaFromEvent("E", nil)
	if !strings.Contains(logs(), "[Avo Inspector] schema sending failed: Request failed. (4 more in the last 10s)") {
		t.Errorf("expected the suppressed count:\n%s", logs())
	}
	if strings.Contains(logs(), "secret-key-789") {
		t.Errorf("logs contain the apiKey")
	}
}

// Internal errors are always logged, rate-limited like the other kinds.
func TestLogging_InternalErrorsAreRateLimited(t *testing.T) {
	logs := captureLogs(t)
	fakeLogClock(t)
	inspector := mustInspector(t, Options{Env: Staging, DisableBatchTimer: true})
	inspector.EnableLogging(false)
	previous := newGuid
	newGuid = func() string { panic("boom") }
	t.Cleanup(func() { newGuid = previous })
	for i := 0; i < 3; i++ {
		if _, err := inspector.TrackSchemaFromEvent("E", nil); err == nil {
			t.Fatal("expected the internal error")
		}
	}
	if n := countLines(logs(), "internal error"); n != 1 {
		t.Errorf("expected one internal-error line in the window, got %d:\n%s", n, logs())
	}
}

// Sampling drops are not data loss the caller can act on: they stay behind the logging flag.
func TestLogging_SamplingDropsAreSilentWhenLoggingIsOff(t *testing.T) {
	logs := captureLogs(t)
	inspector := mustInspector(t, Options{Env: Staging, DisableBatchTimer: true})
	inspector.EnableLogging(false)
	inspector.setSamplingRate(0)
	for i := 0; i < 5; i++ {
		_, _ = inspector.TrackSchemaFromEvent("E", nil)
	}
	if output := logs(); output != "" {
		t.Errorf("expected no output, got %q", output)
	}
}

// The rate limit is safe under concurrent use, loses no count, and starts no goroutine or timer.
func TestLogging_RateLimitIsConcurrencySafe(t *testing.T) {
	logs := captureLogs(t)
	advance := fakeLogClock(t)
	baseline := runtime.NumGoroutine()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			logDropped(1, "queue full")
		}()
	}
	wg.Wait()
	// The callers finish just after wg.Done; wait for them to exit before counting.
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > baseline {
		t.Errorf("the rate limit left %d goroutines running, baseline %d", n, baseline)
	}
	if n := countLines(logs(), "(queue full)"); n != 1 {
		t.Fatalf("expected one line, got %d:\n%s", n, logs())
	}
	advance(logRateWindow)
	logDropped(1, "queue full")
	if !strings.Contains(logs(), "dropped 100 event(s) (queue full) in the last 10s.") {
		t.Errorf("expected the 99 suppressed drops plus this one:\n%s", logs())
	}
}
