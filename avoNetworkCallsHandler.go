package avoinspector

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"time"
)

type BaseBody struct {
	ApiKey       string  `json:"apiKey"`
	AppName      string  `json:"appName"`
	AppVersion   string  `json:"appVersion"`
	LibVersion   string  `json:"libVersion"`
	Env          string  `json:"env"`
	LibPlatform  string  `json:"libPlatform"`
	MessageId    string  `json:"messageId"`
	TrackingId   string  `json:"trackingId"`
	CreatedAt    string  `json:"createdAt"`
	SessionId    string  `json:"sessionId"`
	SamplingRate float64 `json:"samplingRate"`
}

type SessionStartedBody struct {
	BaseBody
	Type string `json:"type"`
}

type EventSchemaBody struct {
	BaseBody
	Type            string     `json:"type"`
	EventName       string     `json:"eventName"`
	EventProperties []Property `json:"eventProperties"`
	EventId         string     `json:"eventId"`
	EventHash       string     `json:"eventHash"`
}

type AvoNetworkCallsHandler struct {
	apiKey       string
	envName      string
	appName      string
	appVersion   string
	libVersion   string
	samplingRate float64
	shouldLog    bool
}

const trackingEndpoint = "https://api.avo.app/inspector/v1/track"

func newAvoNetworkCallsHandler(apiKey, envName, appName, appVersion, libVersion string, shouldLog bool) *AvoNetworkCallsHandler {
	return &AvoNetworkCallsHandler{
		apiKey:       apiKey,
		envName:      envName,
		appName:      appName,
		appVersion:   appVersion,
		libVersion:   libVersion,
		samplingRate: 1.0,
		shouldLog:    shouldLog,
	}
}

func (h *AvoNetworkCallsHandler) callInspectorWithBatchBody(events []interface{}) error {
	eventsPayload, err := json.Marshal(events)
	if err != nil {
		return fmt.Errorf("could not marshal events: %v", err)
	}

	if len(events) == 0 {
		return nil
	}

	if rand.Float64() > h.samplingRate {
		if h.shouldLog {
			log.Println("Avo Inspector: last event schema dropped due to sampling rate.")
		}
		return nil
	}

	if h.shouldLog {
		for _, event := range events {
			switch e := event.(type) {
			case SessionStartedBody:
				log.Println("Avo Inspector: sending session started event.")
			case EventSchemaBody:
				eventSchemaBody := e
				log.Printf("Avo Inspector: sending event %s with schema %v\n", eventSchemaBody.EventName, eventSchemaBody.EventProperties)
			}
		}
	}

	client := http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodPost, trackingEndpoint, bytes.NewReader(eventsPayload))
	if err != nil {
		return fmt.Errorf("could not create request: %v", err)
	}

	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(eventsPayload)))

	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("request returned non-200 status code: %d", res.StatusCode)
	}

	// Read response body
	responseBody, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %v", err)
	}

	// Parse response body
	var responseData struct {
		SamplingRate float64 `json:"samplingRate"`
	}

	err = json.Unmarshal(responseBody, &responseData)
	if err != nil {
		return fmt.Errorf("failed to parse response body: %v", err)
	}

	h.samplingRate = responseData.SamplingRate

	return nil
}

func (avo *AvoNetworkCallsHandler) bodyForSessionStartedCall(sessionId string) SessionStartedBody {
	sessionBody := SessionStartedBody{
		BaseBody: avo.createBaseCallBody(sessionId),
		Type:     "sessionStarted",
	}
	return sessionBody
}

func (avo *AvoNetworkCallsHandler) bodyForEventSchemaCall(sessionId string, eventName string, eventProperties []Property) EventSchemaBody {
	eventSchemaBody := EventSchemaBody{
		BaseBody:        avo.createBaseCallBody(sessionId),
		Type:            "event",
		EventName:       eventName,
		EventProperties: eventProperties,
	}

	return eventSchemaBody
}

func (avo *AvoNetworkCallsHandler) createBaseCallBody(sessionId string) BaseBody {
	return BaseBody{
		ApiKey:       avo.apiKey,
		AppName:      avo.appName,
		AppVersion:   avo.appVersion,
		LibVersion:   avo.libVersion,
		Env:          avo.envName,
		LibPlatform:  "go",
		MessageId:    newGuid(),
		TrackingId:   "",
		CreatedAt:    time.Now().Format(time.RFC3339),
		SessionId:    sessionId,
		SamplingRate: avo.samplingRate,
	}
}
