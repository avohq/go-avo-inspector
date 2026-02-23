package avoinspector

import (
	"errors"
	"fmt"
	"log"
	"strings"
)

const defaultSpecEndpoint = "https://api.avo.app/inspector/v1"

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
	specFetcher         *eventSpecFetcher
	specCache           *eventSpecCache
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
	avoNetworkCallsHandler := newAvoNetworkCallsHandler(apiKey, string(env), appName, appVersion, libVersion, shouldLog, publicEncryptionKey)

	return &AvoInspector{
		apiKey:                 apiKey,
		environment:            env,
		version:                appVersion,
		avoNetworkCallsHandler: avoNetworkCallsHandler,
		shouldLog:              shouldLog,
		publicEncryptionKey:    publicEncryptionKey,
		specCache:              newEventSpecCache(),
	}, nil
}

func (c *AvoInspector) ShouldLog(shouldLog bool) {
	c.shouldLog = shouldLog
}

// EnableValidation enables async event spec validation for non-prod environments.
// In production, this is a no-op. Call this after creating an inspector to activate
// spec fetching and validation in dev/staging.
func (inspector *AvoInspector) EnableValidation() {
	if inspector.environment != Prod && inspector.specFetcher == nil {
		inspector.specFetcher = newEventSpecFetcher(defaultSpecEndpoint)
	}
}

// isValidationEnabled returns true if event spec validation is active.
// Validation is active in dev and staging only, NOT in prod.
func (inspector *AvoInspector) isValidationEnabled() bool {
	return inspector.environment == Dev || inspector.environment == Staging
}

// fetchAndValidateAsync fetches the event spec and validates properties asynchronously.
// Does nothing if validation is not enabled (prod environment) or if specFetcher is not set.
func (inspector *AvoInspector) fetchAndValidateAsync(eventName string, streamId string, eventSchema []Property) {
	if !inspector.isValidationEnabled() || inspector.specFetcher == nil {
		return
	}

	cacheKey := specCacheKey(inspector.apiKey, streamId, eventName)

	// Check cache first
	if spec, ok := inspector.specCache.get(cacheKey); ok {
		if spec != nil {
			result := validateEventSpec(spec, eventSchema)
			if len(result.Errors) > 0 && inspector.shouldLog {
				for _, e := range result.Errors {
					log.Printf("[Avo Inspector] Validation error for '%s': %s", eventName, e)
				}
			}
		}
		return
	}

	// Fetch async
	inspector.specFetcher.fetchAsync(inspector.apiKey, streamId, eventName, func(spec *EventSpecResponse, err error) {
		if err != nil {
			if inspector.shouldLog {
				log.Printf("[Avo Inspector] Failed to fetch spec for '%s': %v", eventName, err)
			}
			// Cache nil to avoid re-fetching on failure
			inspector.specCache.set(cacheKey, nil)
			return
		}

		// Cache the result (including nil)
		inspector.specCache.set(cacheKey, spec)

		if spec != nil {
			result := validateEventSpec(spec, eventSchema)
			if len(result.Errors) > 0 && inspector.shouldLog {
				for _, e := range result.Errors {
					log.Printf("[Avo Inspector] Validation error for '%s': %s", eventName, e)
				}
			}
		}
	})
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

	// Trigger async spec validation (dev/staging only)
	inspector.fetchAndValidateAsync(eventName, streamId, eventSchema)

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
