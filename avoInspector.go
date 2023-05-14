package avoinspector

import (
	"fmt"
)

type AvoInspector struct {
	apiKey    string
	env       string
	version   string
	appName   string
	shouldLog bool
}

func NewAvoInspector(apiKey string, env string, version string, appName string) *AvoInspector {
	return &AvoInspector{
		apiKey:    apiKey,
		env:       env,
		version:   version,
		appName:   appName,
		shouldLog: true,
	}
}

func (c *AvoInspector) shouldLogMethod(shouldLog bool) {
	c.shouldLog = shouldLog
}

func (c *AvoInspector) TrackSchemaFromEvent(eventName string, eventProperties map[string]interface{}) []Property {
	if c.shouldLog {
		fmt.Printf("Event name: %s\n", eventName)
	}

	result := extractSchema(eventProperties)

	if c.shouldLog {
		//jsonResult, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(map[string]interface{}{
			"event_name":       eventName,
			"event_properties": eventProperties,
			"schema":           result,
		})
	}

	return result
}
