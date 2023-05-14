package avoinspector

import (
	"errors"
	"fmt"
)

type AvoInspectorEnv string

const (
	Prod    AvoInspectorEnv = "prod"
	Dev     AvoInspectorEnv = "dev"
	Staging AvoInspectorEnv = "staging"
)

/* type AvoInspector struct {
	apiKey    string
	env       AvoInspectorEnv
	version   string
	appName   string
	shouldLog bool
} */

type AvoInspector struct {
	apiKey                 string
	environment            AvoInspectorEnv
	version                string
	avoNetworkCallsHandler *AvoNetworkCallsHandler
	shouldLog              bool
}

func NewAvoInspector(apiKey string, env AvoInspectorEnv, version string, appName string) (*AvoInspector, error) {
	if env == "" {
		env = Dev
		fmt.Println("[Avo Inspector] No environment provided. Defaulting to dev.")
	}

	if apiKey == "" {
		return nil, errors.New("[Avo Inspector] No API key provided. Inspector can't operate without API key.")
	}

	if version == "" {
		return nil, errors.New("[Avo Inspector] No version provided. Some features of Inspector rely on versioning. Please provide comparable string version, i.e. integer or semantic.")
	}

	shouldLog := env == Dev
	libVersion := "0.0.1"
	avoNetworkCallsHandler := newAvoNetworkCallsHandler(apiKey, string(env), appName, version, libVersion, shouldLog)

	return &AvoInspector{
		apiKey:                 apiKey,
		environment:            env,
		version:                version,
		avoNetworkCallsHandler: avoNetworkCallsHandler,
		shouldLog:              shouldLog,
	}, nil
}

func (c *AvoInspector) shouldLogMethod(shouldLog bool) {
	c.shouldLog = shouldLog
}

/* func (c *AvoInspector) TrackSchemaFromEvent(eventName string, eventProperties map[string]interface{}) []Property {
	if c.shouldLog {
		fmt.Printf("Event name: %s\n", eventName)
	}

	result := extractSchema(eventProperties)

	if c.shouldLog {
		fmt.Println(map[string]interface{}{
			"event_name":       eventName,
			"event_properties": eventProperties,
			"schema":           result,
		})
	}

	return result
} */

func (inspector *AvoInspector) TrackSchemaFromEvent(eventName string, eventProperties map[string]interface{}) ([]Property, error) {
	if inspector.shouldLog {
		fmt.Printf("Avo Inspector: supplied event %s with params %v\n", eventName, eventProperties)
	}

	eventSchema := extractSchema(eventProperties)
	sessionID := newGuid()
	inspectorBatchBody := []any{
		inspector.avoNetworkCallsHandler.bodyForSessionStartedCall(sessionID),
		inspector.avoNetworkCallsHandler.bodyForEventSchemaCall(sessionID, eventName, eventSchema),
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
