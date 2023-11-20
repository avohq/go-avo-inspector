package avoinspector

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestAvoNetworkCallsHandler_callInspectorWithBatchBody(t *testing.T) {
	type testCase struct {
		serverCallback func() *httptest.Server
		event          any
		expectedError  string
	}

	testCases := map[string]testCase{
		"should log": {
			serverCallback: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
					w.Write([]byte(`{"samplingRate": 0.001}`))
				}))
			},
			event: map[string]any{
				"number_prop":  1,
				"string_prop":  "test",
				"boolean_prop": true,
			},
		},
		"detect unmarshable event": {
			serverCallback: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
			},
			event:         func() {},
			expectedError: "could not marshal events",
		},
		"detect invalid response status code": {
			serverCallback: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusBadRequest)
				}))
			},
			event: map[string]any{
				"number_prop":  1,
				"string_prop":  "test",
				"boolean_prop": true,
			},
			expectedError: "request returned non-200 status code",
		},
		"detect invalid response body": {
			serverCallback: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
			},
			event: map[string]any{
				"number_prop":  1,
				"string_prop":  "test",
				"boolean_prop": true,
			},
			expectedError: "failed to parse response body",
		},
	}

	for testName, tc := range testCases {
		t.Run(testName, func(t *testing.T) {
			// Setup a test server to mock the communication with Avo Inspector
			srv := tc.serverCallback()
			defer srv.Close()

			// Create an instance of AvoNetworkCallsHandler
			handler := &AvoNetworkCallsHandler{
				apiKey:           "test-api-key",
				envName:          "test",
				appName:          "test-app",
				appVersion:       "1.0.0",
				libVersion:       "1.0.0",
				samplingRate:     1.0,
				shouldLog:        false,
				trackingEndpoint: srv.URL,
			}

			// Verify the result
			err := handler.callInspectorWithBatchBody([]any{tc.event})
			if err == nil && tc.expectedError != "" {
				t.Errorf("expected error '%s' but got none", tc.expectedError)
			} else if err != nil && !strings.Contains(err.Error(), tc.expectedError) {
				t.Errorf("unexpected error, got: %s and expected '%s'", err, tc.expectedError)
			}
		})
	}
}

func TestAvoNetworkCallsHandler_callInspectorWithBatchBodyConcurrent(t *testing.T) {
	// Setup a test server to mock the communication with Avo Inspector
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"samplingRate": 0.001}`))
	}))
	defer srv.Close()

	// Create a single instance of AvoNetworkCallsHandler meant to be used concurrently
	handler := &AvoNetworkCallsHandler{
		apiKey:           "test-api-key",
		envName:          "test",
		appName:          "test-app",
		appVersion:       "1.0.0",
		libVersion:       "1.0.0",
		samplingRate:     1.0,
		shouldLog:        false,
		trackingEndpoint: srv.URL,
	}

	// Define a function to be called concurrently to assert that the sampling rate can be read/written safely
	call := func(wg *sync.WaitGroup) {
		// Signal that we're done with this goroutine
		defer wg.Done()

		// Create a valid event
		event := map[string]any{
			"number_prop":  1,
			"string_prop":  "test",
			"boolean_prop": true,
		}

		// Verify the result
		err := handler.callInspectorWithBatchBody([]any{event})
		if err != nil {
			t.Errorf("unexpected error, got: %s", err)
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go call(&wg)
	}
	wg.Wait()
}

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
