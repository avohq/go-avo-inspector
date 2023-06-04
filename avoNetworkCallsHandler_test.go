package avoinspector

import (
	"reflect"
	"testing"
)

func TestAvoNetworkCallsHandler_bodyForSessionStartedCall(t *testing.T) {
	// Create an instance of AvoNetworkCallsHandler
	handler := &AvoNetworkCallsHandler{
		apiKey:       "test-api-key",
		envName:      "test",
		appName:      "test-app",
		appVersion:   "1.0.0",
		libVersion:   "1.0.0",
		samplingRate: 1.0,
		shouldLog:    false,
	}

	// Mock newGuid function
	newGuid = func() string {
		return "test-message-id"
	}

	// Call the method being tested
	sessionStartedBody := handler.bodyForSessionStartedCall("test-session-id")

	// Verify the result
	expectedSessionStartedBody := SessionStartedBody{
		BaseBody: BaseBody{
			ApiKey:       "test-api-key",
			AppName:      "test-app",
			AppVersion:   "1.0.0",
			LibVersion:   "1.0.0",
			Env:          "test",
			LibPlatform:  "go",
			MessageId:    "test-message-id",
			TrackingId:   "",
			CreatedAt:    sessionStartedBody.CreatedAt,
			SessionId:    "test-session-id",
			SamplingRate: 1.0,
		},
		Type: "sessionStarted",
	}

	if !reflect.DeepEqual(sessionStartedBody, expectedSessionStartedBody) {
		t.Errorf("unexpected sessionStartedBody, got: %+v, want: %+v", sessionStartedBody, expectedSessionStartedBody)
	}
}

func TestAvoNetworkCallsHandler_bodyForEventSchemaCall(t *testing.T) {
	// Create an instance of AvoNetworkCallsHandler
	handler := &AvoNetworkCallsHandler{
		apiKey:       "test-api-key",
		envName:      "test",
		appName:      "test-app",
		appVersion:   "1.0.0",
		libVersion:   "1.0.0",
		samplingRate: 1.0,
		shouldLog:    false,
	}

	// Mock newGuid function
	newGuid = func() string {
		return "test-message-id"
	}

	// Prepare test event properties
	eventProperties := []Property{
		{PropertyName: "property1", PropertyType: "value1"},
		{PropertyName: "property2", PropertyType: "value2"},
	}

	// Call the method being tested
	eventSchemaBody := handler.bodyForEventSchemaCall("test-session-id", "test-event", eventProperties)

	// Verify the result
	expectedEventSchemaBody := EventSchemaBody{
		BaseBody: BaseBody{
			ApiKey:       "test-api-key",
			AppName:      "test-app",
			AppVersion:   "1.0.0",
			LibVersion:   "1.0.0",
			Env:          "test",
			LibPlatform:  "go",
			MessageId:    "test-message-id",
			TrackingId:   "",
			CreatedAt:    eventSchemaBody.CreatedAt,
			SessionId:    "test-session-id",
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
