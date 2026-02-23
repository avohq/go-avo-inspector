package avoinspector

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewAvoInspector(t *testing.T) {
	apiKey := "API_KEY"
	env := Dev
	version := "1.0.0"
	appName := "MyApp"

	inspector, err := NewAvoInspector(apiKey, env, version, appName)
	if err != nil {
		t.Errorf("unexpected error: %s", err)
	}

	if inspector.apiKey != apiKey {
		t.Errorf("expected API key to be %s, got %s", apiKey, inspector.apiKey)
	}

	if inspector.environment != env {
		t.Errorf("expected environment to be %s, got %s", env, inspector.environment)
	}

	if inspector.version != version {
		t.Errorf("expected version to be %s, got %s", version, inspector.version)
	}

	if inspector.shouldLog != true {
		t.Errorf("expected shouldLog to be true, got false")
	}

	if inspector.avoNetworkCallsHandler == nil {
		t.Errorf("expected avoNetworkCallsHandler to be initialized")
	}
}

func TestNewAvoInspector_WithEmptyAPIKey(t *testing.T) {
	apiKey := ""
	env := Dev
	version := "1.0.0"
	appName := "MyApp"

	_, err := NewAvoInspector(apiKey, env, version, appName)
	if err == nil {
		t.Error("expected error due to empty API key")
	}

	expectedError := errors.New("[Avo Inspector] No API key provided. Inspector can't operate without API key.")
	if err.Error() != expectedError.Error() {
		t.Errorf("expected error '%s', got '%s'", expectedError.Error(), err.Error())
	}
}

func TestNewAvoInspector_WithEmptyVersion(t *testing.T) {
	apiKey := "API_KEY"
	env := Dev
	version := ""
	appName := "MyApp"

	_, err := NewAvoInspector(apiKey, env, version, appName)
	if err == nil {
		t.Error("expected error due to empty version")
	}

	expectedError := errors.New("[Avo Inspector] No version provided. Some features of Inspector rely on versioning. Please provide comparable string version, i.e. integer or semantic.")
	if err.Error() != expectedError.Error() {
		t.Errorf("expected error '%s', got '%s'", expectedError.Error(), err.Error())
	}
}

func TestAvoInspector_shouldLogMethod(t *testing.T) {
	inspector := &AvoInspector{}

	inspector.ShouldLog(true)
	if inspector.shouldLog != true {
		t.Error("expected shouldLog to be true, got false")
	}

	inspector.ShouldLog(false)
	if inspector.shouldLog != false {
		t.Error("expected shouldLog to be false, got true")
	}
}

func TestAvoInspector_TrackSchemaFromEvent(t *testing.T) {
	apiKey := "API_KEY"
	env := Dev
	version := "1.0.0"
	appName := "MyApp"

	inspector, _ := NewAvoInspector(apiKey, env, version, appName)

	eventName := "TestEvent"
	eventProperties := map[string]interface{}{
		"param1": "value1",
		"param2": 123,
	}

	_, err := inspector.TrackSchemaFromEvent(eventName, eventProperties)
	if fmt.Sprint(err) != "Avo Inspector: schema sending failed: request returned non-200 status code: 400" {
		t.Errorf("unexpected error: %s", err)
	}
}

func TestAvoInspector_TrackSchemaFromEvent_SendsEmptyAnonymousId(t *testing.T) {
	// Integration test: create an AvoInspector instance and verify the payload sent
	// by TrackSchemaFromEvent contains AnonymousId as empty string.
	var capturedPayload []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		capturedPayload, err = ioutil.ReadAll(r.Body)
		if err != nil {
			t.Errorf("failed to read request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"samplingRate":1.0}`))
	}))
	defer server.Close()

	inspector, err := NewAvoInspector("test-api-key", Dev, "1.0.0", "MyApp")
	if err != nil {
		t.Fatalf("unexpected error creating inspector: %v", err)
	}
	// Override the tracking endpoint to point to our test server
	inspector.avoNetworkCallsHandler.trackingEndpoint = server.URL
	inspector.avoNetworkCallsHandler.shouldLog = false

	_, err = inspector.TrackSchemaFromEvent("TestEvent", map[string]interface{}{"param1": "value1"})
	if err != nil {
		t.Fatalf("unexpected error from TrackSchemaFromEvent: %v", err)
	}

	var payload []EventSchemaBody
	if err := json.Unmarshal(capturedPayload, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}

	if len(payload) != 1 {
		t.Fatalf("expected 1 event in payload, got %d", len(payload))
	}

	if payload[0].AnonymousId != "" {
		t.Errorf("expected AnonymousId to be empty string, got '%s'", payload[0].AnonymousId)
	}
}

func TestAvoInspector_TrackSchemaFromEventWithStreamId(t *testing.T) {
	apiKey := "API_KEY"
	env := Dev
	version := "1.0.0"
	appName := "MyApp"

	inspector, _ := NewAvoInspector(apiKey, env, version, appName)

	eventName := "TestEvent"
	eventProperties := map[string]interface{}{
		"param1": "value1",
	}

	// This should work (will fail with 400 from server, but that's expected)
	_, err := inspector.TrackSchemaFromEventWithStreamId(eventName, eventProperties, "user-123")
	if err == nil {
		t.Error("expected error from API call (no valid API key)")
	}
}

func TestAvoInspector_TrackSchemaFromEventWithStreamId_SetsAnonymousId(t *testing.T) {
	// Integration test: create an AvoInspector instance and verify the payload sent
	// by TrackSchemaFromEventWithStreamId contains AnonymousId equal to the streamId.
	var capturedPayload []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		capturedPayload, err = ioutil.ReadAll(r.Body)
		if err != nil {
			t.Errorf("failed to read request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"samplingRate":1.0}`))
	}))
	defer server.Close()

	inspector, err := NewAvoInspector("test-api-key", Dev, "1.0.0", "MyApp")
	if err != nil {
		t.Fatalf("unexpected error creating inspector: %v", err)
	}
	// Override the tracking endpoint to point to our test server
	inspector.avoNetworkCallsHandler.trackingEndpoint = server.URL
	inspector.avoNetworkCallsHandler.shouldLog = false

	_, err = inspector.TrackSchemaFromEventWithStreamId("TestEvent", map[string]interface{}{"param1": "value1"}, "user-123")
	if err != nil {
		t.Fatalf("unexpected error from TrackSchemaFromEventWithStreamId: %v", err)
	}

	var payload []EventSchemaBody
	if err := json.Unmarshal(capturedPayload, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}

	if len(payload) != 1 {
		t.Fatalf("expected 1 event in payload, got %d", len(payload))
	}

	if payload[0].AnonymousId != "user-123" {
		t.Errorf("expected AnonymousId to be 'user-123', got '%s'", payload[0].AnonymousId)
	}
}

func TestAvoInspector_StreamIdWithColonLogsWarning(t *testing.T) {
	apiKey := "API_KEY"
	env := Dev
	version := "1.0.0"
	appName := "MyApp"

	inspector, _ := NewAvoInspector(apiKey, env, version, appName)

	// Capture log output
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)

	eventName := "TestEvent"
	eventProperties := map[string]interface{}{
		"param1": "value1",
	}

	// streamId with colon should log a warning
	inspector.TrackSchemaFromEventWithStreamId(eventName, eventProperties, "user:123")

	logOutput := buf.String()
	if !strings.Contains(logOutput, "[Avo Inspector] Warning: streamId contains ':' which is not supported") {
		t.Errorf("expected warning about streamId containing ':', got log output: '%s'", logOutput)
	}
}

func TestAvoInspector_StreamIdWithoutColonNoWarning(t *testing.T) {
	apiKey := "API_KEY"
	env := Dev
	version := "1.0.0"
	appName := "MyApp"

	inspector, _ := NewAvoInspector(apiKey, env, version, appName)

	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)

	eventName := "TestEvent"
	eventProperties := map[string]interface{}{
		"param1": "value1",
	}

	inspector.TrackSchemaFromEventWithStreamId(eventName, eventProperties, "user-123")

	logOutput := buf.String()
	if strings.Contains(logOutput, "streamId contains ':'") {
		t.Errorf("did not expect warning about streamId containing ':', got: '%s'", logOutput)
	}
}

func TestNewAvoInspectorWithEncryption(t *testing.T) {
	apiKey := "API_KEY"
	env := Dev
	version := "1.0.0"
	appName := "MyApp"
	publicKey := "04abcdef1234567890"

	inspector, err := NewAvoInspectorWithEncryption(apiKey, env, version, appName, publicKey)
	if err != nil {
		t.Errorf("unexpected error: %s", err)
	}

	if inspector.publicEncryptionKey != publicKey {
		t.Errorf("expected publicEncryptionKey to be '%s', got '%s'", publicKey, inspector.publicEncryptionKey)
	}

	// Should still have all the normal fields
	if inspector.apiKey != apiKey {
		t.Errorf("expected API key to be %s, got %s", apiKey, inspector.apiKey)
	}

	if inspector.avoNetworkCallsHandler == nil {
		t.Errorf("expected avoNetworkCallsHandler to be initialized")
	}
}

func TestNewAvoInspectorWithEncryption_EmptyKey(t *testing.T) {
	apiKey := "API_KEY"
	env := Dev
	version := "1.0.0"
	appName := "MyApp"

	// Empty encryption key should still work (no encryption)
	inspector, err := NewAvoInspectorWithEncryption(apiKey, env, version, appName, "")
	if err != nil {
		t.Errorf("unexpected error: %s", err)
	}

	if inspector.publicEncryptionKey != "" {
		t.Errorf("expected publicEncryptionKey to be empty, got '%s'", inspector.publicEncryptionKey)
	}
}

func TestNewAvoInspectorWithEncryption_ValidatesLikeNewAvoInspector(t *testing.T) {
	// Should fail with empty API key, just like NewAvoInspector
	_, err := NewAvoInspectorWithEncryption("", Dev, "1.0.0", "MyApp", "key")
	if err == nil {
		t.Error("expected error due to empty API key")
	}

	// Should fail with empty version
	_, err = NewAvoInspectorWithEncryption("API_KEY", Dev, "", "MyApp", "key")
	if err == nil {
		t.Error("expected error due to empty version")
	}
}

func TestNewAvoInspector_BackwardsCompatible(t *testing.T) {
	// Existing NewAvoInspector should still work and have empty publicEncryptionKey
	inspector, err := NewAvoInspector("API_KEY", Dev, "1.0.0", "MyApp")
	if err != nil {
		t.Errorf("unexpected error: %s", err)
	}

	if inspector.publicEncryptionKey != "" {
		t.Errorf("expected publicEncryptionKey to be empty for standard constructor, got '%s'", inspector.publicEncryptionKey)
	}
}
