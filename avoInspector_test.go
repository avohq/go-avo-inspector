package avoinspector

import (
	"fmt"
	"net/http"
	"reflect"
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
			<-inspector.dispatch([]wireEvent{event})
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
	if inspector.stopTimer != nil {
		t.Errorf("expected no timer goroutine")
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
	if inspector.samplingRate != 0.75 || len(inspector.pending) != 0 || len(inspector.inFlight) != 0 || inspector.stopTimer != nil {
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
