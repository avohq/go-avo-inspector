package avoinspector

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
)

// AC4: publicEncryptionKey in base body when non-empty
func TestBaseBody_PublicEncryptionKeyIncluded(t *testing.T) {
	handler := &AvoNetworkCallsHandler{
		apiKey:              "test-api-key",
		envName:             "dev",
		appName:             "test-app",
		appVersion:          "1.0.0",
		libVersion:          "1.0.0",
		samplingRate:        1.0,
		shouldLog:           false,
		publicEncryptionKey: "some-base64-key",
	}

	newGuid = func() string { return "test-message-id" }

	body := handler.createBaseCallBody("stream")
	jsonBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &m); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	val, ok := m["publicEncryptionKey"]
	if !ok {
		t.Fatal("expected publicEncryptionKey in JSON output")
	}
	if val != "some-base64-key" {
		t.Errorf("publicEncryptionKey = %v, want 'some-base64-key'", val)
	}
}

// AC4: publicEncryptionKey omitted from base body when empty
func TestBaseBody_PublicEncryptionKeyOmittedWhenEmpty(t *testing.T) {
	handler := &AvoNetworkCallsHandler{
		apiKey:              "test-api-key",
		envName:             "dev",
		appName:             "test-app",
		appVersion:          "1.0.0",
		libVersion:          "1.0.0",
		samplingRate:        1.0,
		shouldLog:           false,
		publicEncryptionKey: "",
	}

	newGuid = func() string { return "test-message-id" }

	body := handler.createBaseCallBody("stream")
	jsonBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &m); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if _, ok := m["publicEncryptionKey"]; ok {
		t.Error("publicEncryptionKey should be omitted when empty")
	}
}

// AC5: List-type property values omitted entirely
func TestEncryptEventProperties_ListTypeOmitted(t *testing.T) {
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubKeyBase64 := base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes())

	properties := []Property{
		{PropertyName: "name", PropertyType: "string"},
		{PropertyName: "tags", PropertyType: "list"},
		{PropertyName: "count", PropertyType: "int"},
	}

	encrypted := encryptEventProperties(properties, pubKeyBase64)

	// Should have 2 properties (list omitted)
	if len(encrypted) != 2 {
		t.Errorf("expected 2 encrypted properties, got %d", len(encrypted))
	}

	for _, ep := range encrypted {
		if ep.PropertyName == "tags" {
			t.Error("list-type property 'tags' should have been omitted")
		}
	}
}

// AC6: Encryption failure logs warning, omits property, continues
func TestEncryptEventProperties_FailureContinues(t *testing.T) {
	properties := []Property{
		{PropertyName: "name", PropertyType: "string"},
		{PropertyName: "age", PropertyType: "int"},
	}

	// Use an invalid key so encryption fails
	encrypted := encryptEventProperties(properties, "invalid-key")

	// All properties should be omitted on failure (each encryption fails)
	if len(encrypted) != 0 {
		t.Errorf("expected 0 properties when encryption fails, got %d", len(encrypted))
	}
}

// AC9: Prod negative test — no encryptedPropertyValue in prod env payload
func TestProdPayload_NoEncryptedPropertyValue(t *testing.T) {
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubKeyBase64 := base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes())

	handler := &AvoNetworkCallsHandler{
		apiKey:              "test-api-key",
		envName:             "prod",
		appName:             "test-app",
		appVersion:          "1.0.0",
		libVersion:          "1.0.0",
		samplingRate:        1.0,
		shouldLog:           false,
		publicEncryptionKey: pubKeyBase64,
	}

	newGuid = func() string { return "test-message-id" }

	properties := []Property{
		{PropertyName: "email", PropertyType: "string"},
	}

	body := handler.bodyForEventSchemaCall("stream", "test-event", properties)

	jsonBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	jsonStr := string(jsonBytes)
	if contains(jsonStr, "encryptedPropertyValue") {
		t.Error("prod payload should NOT contain encryptedPropertyValue")
	}
}

// AC3+encryption: Dev env with key should encrypt properties
func TestDevPayload_HasEncryptedPropertyValue(t *testing.T) {
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubKeyBase64 := base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes())

	handler := &AvoNetworkCallsHandler{
		apiKey:              "test-api-key",
		envName:             "dev",
		appName:             "test-app",
		appVersion:          "1.0.0",
		libVersion:          "1.0.0",
		samplingRate:        1.0,
		shouldLog:           false,
		publicEncryptionKey: pubKeyBase64,
	}

	newGuid = func() string { return "test-message-id" }

	properties := []Property{
		{PropertyName: "email", PropertyType: "string"},
	}

	body := handler.bodyForEventSchemaCall("stream", "test-event", properties)

	jsonBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &m); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	// Should have eventProperties with encryptedPropertyValue
	props, ok := m["eventProperties"].([]interface{})
	if !ok || len(props) == 0 {
		t.Fatal("expected eventProperties in output")
	}

	prop := props[0].(map[string]interface{})
	if _, ok := prop["encryptedPropertyValue"]; !ok {
		t.Error("dev payload with encryption key should contain encryptedPropertyValue")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
