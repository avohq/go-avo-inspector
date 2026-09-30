package avoinspector

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
)

// TestMain points the mock endpoint at a closed local port, so a test that forgets to start a
// test server can never reach the real Inspector API. Tests that need a server override it.
func TestMain(m *testing.M) {
	os.Setenv(mockEndpointEnvVar, "http://127.0.0.1:1")
	os.Exit(m.Run())
}

type capturedRequest struct {
	header  http.Header
	rawBody []byte
	events  []map[string]interface{}
}

// testServer is a mock Inspector endpoint that records every request. respond, when set, writes
// the response for the n-th request (0-based); by default it answers 200 {"samplingRate":1}.
type testServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []capturedRequest
	respond  func(n int, w http.ResponseWriter, r *http.Request)
}

// newTestServer starts a mock endpoint and points AVO_INSPECTOR_MOCK_ENDPOINT at it.
func newTestServer(t *testing.T, respond func(n int, w http.ResponseWriter, r *http.Request)) *testServer {
	t.Helper()
	server := &testServer{respond: respond}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := raw
		if r.Header.Get("Content-Encoding") == "gzip" {
			reader, err := gzip.NewReader(bytes.NewReader(raw))
			if err != nil {
				t.Errorf("gzip body does not gunzip: %v", err)
				return
			}
			body, _ = io.ReadAll(reader)
		}
		var events []map[string]interface{}
		if err := json.Unmarshal(body, &events); err != nil {
			t.Errorf("request body is not a JSON array: %v", err)
		}
		server.mu.Lock()
		n := len(server.requests)
		server.requests = append(server.requests, capturedRequest{header: r.Header.Clone(), rawBody: raw, events: events})
		server.mu.Unlock()
		if server.respond != nil {
			server.respond(n, w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"samplingRate":1}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv(mockEndpointEnvVar, server.URL)
	return server
}

func (s *testServer) captured() []capturedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]capturedRequest(nil), s.requests...)
}

func respondWith(status int, body string) func(int, http.ResponseWriter, *http.Request) {
	return func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func eventNames(request capturedRequest) []string {
	names := []string{}
	for _, event := range request.events {
		names = append(names, event["eventName"].(string))
	}
	return names
}

func mustInspector(t *testing.T, options Options) *AvoInspector {
	t.Helper()
	if options.ApiKey == "" {
		options.ApiKey = "test-key"
	}
	if options.AppVersion == "" {
		options.AppVersion = "1.0.0"
	}
	inspector, err := NewAvoInspectorWithOptions(options)
	if err != nil {
		t.Fatalf("constructor failed: %v", err)
	}
	t.Cleanup(inspector.Destroy)
	return inspector
}
