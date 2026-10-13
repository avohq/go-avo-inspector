package avoinspector

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestBaseBody_HasAnonymousId_NotSessionIdOrTrackingId(t *testing.T) {
	handler := &AvoNetworkCallsHandler{
		apiKey:       "test-api-key",
		envName:      "test",
		appName:      "test-app",
		appVersion:   "1.0.0",
		libVersion:   "1.0.0",
		samplingRate: 1.0,
		shouldLog:    false,
	}

	newGuid = func() string {
		return "test-message-id"
	}

	body := handler.createBaseCallBody("test-stream-id")

	// AnonymousId should be set to the streamId
	if body.AnonymousId != "test-stream-id" {
		t.Errorf("expected AnonymousId to be 'test-stream-id', got '%s'", body.AnonymousId)
	}

	// Verify JSON output contains anonymousId and NOT sessionId or trackingId
	jsonBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("failed to marshal BaseBody: %v", err)
	}
	jsonStr := string(jsonBytes)

	if !strings.Contains(jsonStr, `"anonymousId"`) {
		t.Error("expected JSON to contain 'anonymousId' field")
	}
	if strings.Contains(jsonStr, `"sessionId"`) {
		t.Error("JSON should NOT contain 'sessionId' field")
	}
	if strings.Contains(jsonStr, `"trackingId"`) {
		t.Error("JSON should NOT contain 'trackingId' field")
	}
}

func TestBaseBody_EmptyStreamId(t *testing.T) {
	handler := &AvoNetworkCallsHandler{
		apiKey:       "test-api-key",
		envName:      "test",
		appName:      "test-app",
		appVersion:   "1.0.0",
		libVersion:   "1.0.0",
		samplingRate: 1.0,
		shouldLog:    false,
	}

	newGuid = func() string {
		return "test-message-id"
	}

	body := handler.createBaseCallBody("")

	if body.AnonymousId != "" {
		t.Errorf("expected AnonymousId to be empty string, got '%s'", body.AnonymousId)
	}
}

func TestAvoNetworkCallsHandler_bodyForEventSchemaCall(t *testing.T) {
	handler := &AvoNetworkCallsHandler{
		apiKey:       "test-api-key",
		envName:      "test",
		appName:      "test-app",
		appVersion:   "1.0.0",
		libVersion:   "1.0.0",
		samplingRate: 1.0,
		shouldLog:    false,
	}

	newGuid = func() string {
		return "test-message-id"
	}

	eventProperties := []Property{
		{PropertyName: "property1", PropertyType: "value1"},
		{PropertyName: "property2", PropertyType: "value2"},
	}

	eventSchemaBody := handler.bodyForEventSchemaCall("test-stream-id", "test-event", eventProperties)

	expectedEventSchemaBody := EventSchemaBody{
		BaseBody: BaseBody{
			ApiKey:       "test-api-key",
			AppName:      "test-app",
			AppVersion:   "1.0.0",
			LibVersion:   "1.0.0",
			Env:          "test",
			LibPlatform:  "go",
			MessageId:    "test-message-id",
			AnonymousId:  "test-stream-id",
			CreatedAt:    eventSchemaBody.CreatedAt,
			SamplingRate: 1.0,
		},
		Type:            "event",
		EventName:       "test-event",
		EventProperties: eventProperties,
	}

	if !reflect.DeepEqual(eventSchemaBody, expectedEventSchemaBody) {
		t.Errorf("unexpected eventSchemaBody, got: %+v, want: %+v", eventSchemaBody, expectedEventSchemaBody)
	}
}

