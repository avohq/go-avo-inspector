package avoinspector

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWire_BodyAndHeaders(t *testing.T) {
	server := newTestServer(t, nil)
	inspector := mustInspector(t, Options{Env: Dev, AppName: "TestApp"})

	_, err := inspector.TrackSchemaFromEventWithOptions("User Signed Up",
		map[string]interface{}{"plan": "pro", "seats": 3}, TrackOptions{StreamId: "stream-abc"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	requests := server.captured()
	if len(requests) != 1 || len(requests[0].events) != 1 {
		t.Fatalf("expected one request with one event (no sessionStarted element), got %v", requests)
	}
	request := requests[0]
	for header, expected := range map[string]string{
		"api-key":      "test-key",
		"env":          "dev",
		"X-Avo-Client": "go",
		"Content-Type": "application/json",
		"Accept":       "application/json",
	} {
		if got := request.header.Get(header); got != expected {
			t.Errorf("header %s: expected %q, got %q", header, expected, got)
		}
	}
	if got := request.header.Get("Content-Length"); got != strconv.Itoa(len(request.rawBody)) {
		t.Errorf("Content-Length %q does not match body length %d", got, len(request.rawBody))
	}
	if request.header.Get("Content-Encoding") != "" {
		t.Errorf("small body must not be compressed")
	}

	event := request.events[0]
	expected := map[string]interface{}{
		"apiKey":       "test-key",
		"appName":      "TestApp",
		"appVersion":   "1.0.0",
		"libVersion":   Version,
		"env":          "dev",
		"libPlatform":  "go",
		"streamId":     "stream-abc",
		"samplingRate": 1.0,
		"type":         "event",
		"eventName":    "User Signed Up",
	}
	for key, value := range expected {
		if event[key] != value {
			t.Errorf("field %s: expected %v, got %v", key, value, event[key])
		}
	}
	for _, forbidden := range []string{"sessionId", "trackingId", "visitorId", "userId", "outputReference", "originHint", "avoFunction", "eventId", "eventHash"} {
		if _, ok := event[forbidden]; ok {
			t.Errorf("field %s must not be sent", forbidden)
		}
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(event["messageId"].(string)) {
		t.Errorf("messageId is not a lowercase UUID v4: %v", event["messageId"])
	}
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`).MatchString(event["createdAt"].(string)) {
		t.Errorf("createdAt lacks millisecond precision: %v", event["createdAt"])
	}
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(Version) {
		t.Errorf("Version is not plain SemVer: %q", Version)
	}
}

func TestWire_GzipAtThreshold(t *testing.T) {
	server := newTestServer(t, nil)
	inspector := mustInspector(t, Options{Env: Dev})
	properties := map[string]interface{}{}
	for i := 0; i < 40; i++ {
		properties["attribute_"+strconv.Itoa(i)] = "value"
	}
	_, _ = inspector.TrackSchemaFromEvent("Large", properties)
	requests := server.captured()
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	request := requests[0]
	if request.header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("expected a gzip body for a large payload")
	}
	if got := request.header.Get("Content-Length"); got != strconv.Itoa(len(request.rawBody)) {
		t.Errorf("Content-Length %q must be the compressed length %d", got, len(request.rawBody))
	}
	if request.header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type must stay application/json when compressed")
	}
}

// The threshold is on the serialized byte length: 1023 bytes stays plain, 1024 is compressed.
func TestWire_GzipThresholdBoundary(t *testing.T) {
	handler := newAvoNetworkCallsHandler("k", Dev)
	for _, size := range []int{1023, 1024} {
		server := newTestServer(t, nil)
		event := wireEvent{EventProperties: []Property{}}
		base, _ := json.Marshal([]wireEvent{event})
		event.EventName = strings.Repeat("x", size-len(base))
		if body, _ := json.Marshal([]wireEvent{event}); len(body) != size {
			t.Fatalf("test setup: body is %d bytes, wanted %d", len(body), size)
		}
		handler.send(context.Background(), []wireEvent{event})
		encoding := server.captured()[0].header.Get("Content-Encoding")
		if (size >= gzipThresholdBytes) != (encoding == "gzip") {
			t.Errorf("%d-byte body: Content-Encoding %q", size, encoding)
		}
	}
}

func TestWire_GzipIsRFC1952(t *testing.T) {
	compressed, err := gzipBytes([]byte(`[{"a":1}]`))
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("not a gzip stream: %v", err)
	}
	plain, _ := io.ReadAll(reader)
	if string(plain) != `[{"a":1}]` {
		t.Errorf("round trip changed the body: %s", plain)
	}
}

// SPEC.md §7.2: the SDK itself refuses a header value carrying CR, LF or NUL, rather than
// relying on net/http, and sends nothing.
func TestWire_HeaderControlCharacterGuard(t *testing.T) {
	server := newTestServer(t, nil)
	for _, apiKey := range []string{"key\r\nX-Injected: 1", "key\n", "ke\x00y"} {
		handler := newAvoNetworkCallsHandler(apiKey, Dev)
		result := handler.send(context.Background(), []wireEvent{{EventProperties: []Property{}}})
		if result.status != sendFailed || !errors.Is(result.err, errUnsafeHeader) {
			t.Errorf("apiKey %q: expected errUnsafeHeader, got %v (%v)", apiKey, result.status, result.err)
		}
	}
	if n := len(server.captured()); n != 0 {
		t.Errorf("expected no requests, got %d", n)
	}
}

func TestWire_GatewayOptions(t *testing.T) {
	testCases := []struct {
		name            string
		options         TrackOptions
		appVersion      interface{}
		outputReference interface{}
		originHint      interface{}
	}{
		{"all set, trimmed", TrackOptions{OutputReference: "  meta-x7k2q\nb  ", OriginHint: "\tandroid\rtv \n", OriginAppVersion: "  4.2.0 "}, "4.2.0", "meta-x7k2q\nb", "android\rtv"},
		{"hint without version", TrackOptions{OriginHint: " android "}, nil, "<absent>", "android"},
		{"version without hint", TrackOptions{OutputReference: " meta ", OriginAppVersion: " 4.2.0 "}, "4.2.0", "meta", "<absent>"},
		{"blank values are absent", TrackOptions{OutputReference: "   ", OriginHint: "", OriginAppVersion: "  "}, "1.0.0", "<absent>", "<absent>"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t, nil)
			inspector := mustInspector(t, Options{Env: Dev})
			_, _ = inspector.TrackSchemaFromEventWithOptions("purchase", map[string]interface{}{"appVersion": true}, tc.options)
			event := server.captured()[0].events[0]
			if value, ok := event["appVersion"]; !ok || value != tc.appVersion {
				t.Errorf("appVersion: expected %v, got %v (present %v)", tc.appVersion, value, ok)
			}
			for key, expected := range map[string]interface{}{"outputReference": tc.outputReference, "originHint": tc.originHint} {
				value, ok := event[key]
				if expected == "<absent>" {
					if ok {
						t.Errorf("%s must be omitted, got %v", key, value)
					}
				} else if value != expected {
					t.Errorf("%s: expected %q, got %v", key, expected, value)
				}
			}
			properties := event["eventProperties"].([]interface{})
			if len(properties) != 1 || properties[0].(map[string]interface{})["propertyName"] != "appVersion" {
				t.Errorf("options must not touch the schema: %v", properties)
			}
		})
	}
}

func TestWire_MockEndpointIgnoredInProd(t *testing.T) {
	t.Setenv(mockEndpointEnvVar, "http://attacker.example")
	if productionEndpoint != "https://api.avo.app/inspector/v2/track" {
		t.Errorf("unexpected production endpoint %q", productionEndpoint)
	}
	if got := newAvoNetworkCallsHandler("k", Prod).endpoint(); got != trackingEndpoint {
		t.Errorf("prod must ignore the mock endpoint, got %q", got)
	}
	if got := newAvoNetworkCallsHandler("k", Staging).endpoint(); got != "http://attacker.example" {
		t.Errorf("staging must honor the mock endpoint, got %q", got)
	}
	t.Setenv(mockEndpointEnvVar, "")
	if got := newAvoNetworkCallsHandler("k", Dev).endpoint(); got != trackingEndpoint {
		t.Errorf("expected the v2 endpoint by default, got %q", got)
	}
}

func TestWire_RequestTimeout(t *testing.T) {
	if requestTimeout != 10*time.Second {
		t.Errorf("request timeout must be 10s, got %v", requestTimeout)
	}
	release := make(chan struct{})
	newTestServer(t, func(int, http.ResponseWriter, *http.Request) { <-release })
	defer close(release)
	handler := newAvoNetworkCallsHandler("k", Dev)
	handler.client.Timeout = 50 * time.Millisecond
	result := handler.send(context.Background(), []wireEvent{{EventProperties: []Property{}}})
	if result.status != sendFailed || !errors.Is(result.err, errRequestTimeout) {
		t.Errorf("expected %q, got %v", errRequestTimeout, result.err)
	}
}

// A redirect is never followed: it would carry the api-key header to another host. The 3xx is
// handled as an ordinary non-200.
func TestWire_RedirectIsNotFollowed(t *testing.T) {
	var leaked []string
	var mu sync.Mutex
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		leaked = append(leaked, r.Header.Get("api-key"))
		mu.Unlock()
	}))
	defer other.Close()
	for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect} {
		newTestServer(t, func(_ int, w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, other.URL, status)
		})
		result := newAvoNetworkCallsHandler("secret-key", Dev).send(context.Background(), []wireEvent{{EventProperties: []Property{}}})
		if result.status != sendNon200 {
			t.Errorf("%d: expected a non-200 result, got %v (%v)", status, result.status, result.err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(leaked) != 0 {
		t.Errorf("the redirect was followed and sent api-key %q to another host", leaked)
	}
}

// Under test, even a prod instance, which ignores the mock endpoint, cannot reach the real API:
// TestMain points the production endpoint at a closed local port.
func TestWire_TestsCannotReachTheRealAPI(t *testing.T) {
	logs := captureLogs(t)
	inspector := mustInspector(t, Options{Env: Prod, BatchSize: 1, DisableBatchTimer: true})
	inspector.EnableLogging(false)
	start := time.Now()
	_, _ = inspector.TrackSchemaFromEvent("E", nil)
	if !strings.Contains(logs(), "schema sending failed: Request failed.") || time.Since(start) > 2*time.Second {
		t.Errorf("a prod send under test must fail fast against the closed port, got %q after %v", logs(), time.Since(start))
	}
}

// Up to maxConcurrentSends requests run at once, and the connection of each is kept for the next
// round: rounds of concurrent sends reuse the same connections instead of reconnecting.
func TestWire_ConcurrentSendsReuseTheirConnections(t *testing.T) {
	var newConns atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		time.Sleep(30 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"samplingRate":1}`))
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			newConns.Add(1)
		}
	}
	server.Start()
	defer server.Close()
	t.Setenv(mockEndpointEnvVar, server.URL)
	handler := newAvoNetworkCallsHandler("k", Dev)
	for round := 0; round < 3; round++ {
		var wg sync.WaitGroup
		for i := 0; i < maxConcurrentSends; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if result := handler.send(context.Background(), []wireEvent{{EventProperties: []Property{}}}); result.status != sendOk {
					t.Errorf("send failed: %+v", result)
				}
			}()
		}
		wg.Wait()
	}
	if got := newConns.Load(); got != maxConcurrentSends {
		t.Errorf("opened %d connections for 3 rounds of %d concurrent sends, want %d", got, maxConcurrentSends, maxConcurrentSends)
	}
}

type wrappingTransport struct{ http.RoundTripper }

// A default transport replaced by a wrapper (as APM and tracing libraries do) is used as is: the
// package must never panic at init over it. A plain *http.Transport is cloned with an idle
// connection per concurrent sender, leaving the original untouched.
func TestWire_TransportToleratesAReplacedDefaultTransport(t *testing.T) {
	wrapper := wrappingTransport{http.DefaultTransport}
	if got := newTransport(wrapper); got != (http.RoundTripper)(wrapper) {
		t.Errorf("a wrapped default transport was not used as is: %T", got)
	}
	base := &http.Transport{MaxIdleConnsPerHost: 1}
	cloned, ok := newTransport(base).(*http.Transport)
	if !ok || cloned == base || cloned.MaxIdleConnsPerHost != maxConcurrentSends || base.MaxIdleConnsPerHost != 1 {
		t.Errorf("expected a clone with MaxIdleConnsPerHost %d and the original unchanged, got %+v", maxConcurrentSends, cloned)
	}
}
