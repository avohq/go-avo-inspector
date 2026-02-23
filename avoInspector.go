package avoinspector

import (
	"errors"
	"fmt"
	"log"
	"strings"
)

type AvoInspectorEnv string

const (
	Prod    AvoInspectorEnv = "prod"
	Dev     AvoInspectorEnv = "dev"
	Staging AvoInspectorEnv = "staging"
)

type AvoInspector struct {
	apiKey                 string
	environment            AvoInspectorEnv
	version                string
	avoNetworkCallsHandler *AvoNetworkCallsHandler
	shouldLog              bool
	// publicEncryptionKey is reserved for STORY-13 (Go payload encryption).
	// Currently stored but not yet used for encryption operations.
	publicEncryptionKey string
}

func NewAvoInspector(apiKey string, env AvoInspectorEnv, appVersion string, appName string) (*AvoInspector, error) {
	return NewAvoInspectorWithEncryption(apiKey, env, appVersion, appName, "")
}

func NewAvoInspectorWithEncryption(apiKey string, env AvoInspectorEnv, appVersion string, appName string, publicEncryptionKey string) (*AvoInspector, error) {
	if env == "" {
		env = Dev
		fmt.Println("[Avo Inspector] No environment provided. Defaulting to dev.")
	}

	if apiKey == "" {
		return nil, errors.New("[Avo Inspector] No API key provided. Inspector can't operate without API key.")
	}

	if appVersion == "" {
		return nil, errors.New("[Avo Inspector] No version provided. Some features of Inspector rely on versioning. Please provide comparable string version, i.e. integer or semantic.")
	}

	shouldLog := env == Dev
	libVersion := "1.0.0"
	avoNetworkCallsHandler := newAvoNetworkCallsHandler(apiKey, string(env), appName, appVersion, libVersion, shouldLog)

	return &AvoInspector{
		apiKey:                 apiKey,
		environment:            env,
		version:                appVersion,
		avoNetworkCallsHandler: avoNetworkCallsHandler,
		shouldLog:              shouldLog,
		publicEncryptionKey:    publicEncryptionKey,
	}, nil
}

func (c *AvoInspector) ShouldLog(shouldLog bool) {
	c.shouldLog = shouldLog
}

func (inspector *AvoInspector) TrackSchemaFromEvent(eventName string, eventProperties map[string]interface{}) ([]Property, error) {
	return inspector.TrackSchemaFromEventWithStreamId(eventName, eventProperties, "")
}

func (inspector *AvoInspector) TrackSchemaFromEventWithStreamId(eventName string, eventProperties map[string]interface{}, streamId string) ([]Property, error) {
	if strings.Contains(streamId, ":") {
		log.Printf("[Avo Inspector] Warning: streamId contains ':' which is not supported")
	}

	if inspector.shouldLog {
		fmt.Printf("Avo Inspector: supplied event %s with params %v\n", eventName, eventProperties)
	}

	eventSchema := extractSchema(eventProperties)
	inspectorBatchBody := []any{
		inspector.avoNetworkCallsHandler.bodyForEventSchemaCall(streamId, eventName, eventSchema),
	}

	err := inspector.avoNetworkCallsHandler.callInspectorWithBatchBody(inspectorBatchBody)
	if err != nil {
		if inspector.shouldLog {
			fmt.Printf("Avo Inspector: schema sending failed: %s\n", err)
		}
		return nil, fmt.Errorf("Avo Inspector: schema sending failed: %w", err)
	}

	if inspector.shouldLog {
		fmt.Println("Avo Inspector: schema sent successfully.")
	}

	return eventSchema, nil
}
