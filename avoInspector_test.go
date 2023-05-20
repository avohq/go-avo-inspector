package avoinspector

import (
	"errors"
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

	inspector.shouldLogMethod(true)
	if inspector.shouldLog != true {
		t.Error("expected shouldLog to be true, got false")
	}

	inspector.shouldLogMethod(false)
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
	if err != nil {
		t.Errorf("unexpected error: %s", err)
	}
}
