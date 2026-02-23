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
	ApiKey              string  `json:"apiKey"`
	AppName             string  `json:"appName"`
	AppVersion          string  `json:"appVersion"`
	LibVersion          string  `json:"libVersion"`
	Env                 string  `json:"env"`
	LibPlatform         string  `json:"libPlatform"`
	MessageId           string  `json:"messageId"`
	AnonymousId         string  `json:"anonymousId"`
	CreatedAt           string  `json:"createdAt"`
	SamplingRate        float64 `json:"samplingRate"`
	PublicEncryptionKey string  `json:"publicEncryptionKey,omitempty"`
}

type EventSchemaBody struct {
	BaseBody
	Type            string      `json:"type"`
	EventName       string      `json:"eventName"`
	EventProperties interface{} `json:"eventProperties"`
	AvoFunction     bool        `json:"avoFunction"`
	EventId         string      `json:"eventId"`
	EventHash       string      `json:"eventHash"`
}

type AvoNetworkCallsHandler struct {
	apiKey              string
	envName             string
	appName             string
	appVersion          string
	libVersion          string
	samplingRate        float64
	shouldLog           bool
	trackingEndpoint    string
	publicEncryptionKey string
}

const defaultTrackingEndpoint = "https://api.avo.app/inspector/v1/track"

func newAvoNetworkCallsHandler(apiKey, envName, appName, appVersion, libVersion string, shouldLog bool, publicEncryptionKey string) *AvoNetworkCallsHandler {
	return &AvoNetworkCallsHandler{
		apiKey:              apiKey,
		envName:             envName,
		appName:             appName,
		appVersion:          appVersion,
		libVersion:          libVersion,
		samplingRate:        1.0,
		shouldLog:           shouldLog,
		trackingEndpoint:    defaultTrackingEndpoint,
		publicEncryptionKey: publicEncryptionKey,
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
			case EventSchemaBody:
				eventSchemaBody := e
				log.Printf("Avo Inspector: sending event %s with schema %v\n", eventSchemaBody.EventName, eventSchemaBody.EventProperties)
			}
		}
	}

	client := http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodPost, h.trackingEndpoint, bytes.NewReader(eventsPayload))
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

func (avo *AvoNetworkCallsHandler) bodyForEventSchemaCall(streamId string, eventName string, eventProperties []Property) EventSchemaBody {
	var props interface{}
	if shouldEncrypt(avo.envName, avo.publicEncryptionKey) {
		props = encryptEventProperties(eventProperties, avo.publicEncryptionKey)
	} else {
		props = eventProperties
	}

	eventSchemaBody := EventSchemaBody{
		BaseBody:        avo.createBaseCallBody(streamId),
		Type:            "event",
		EventName:       eventName,
		EventProperties: props,
	}

	return eventSchemaBody
}

func (avo *AvoNetworkCallsHandler) createBaseCallBody(streamId string) BaseBody {
	return BaseBody{
		ApiKey:              avo.apiKey,
		AppName:             avo.appName,
		AppVersion:          avo.appVersion,
		LibVersion:          avo.libVersion,
		Env:                 avo.envName,
		LibPlatform:         "go",
		MessageId:           newGuid(),
		AnonymousId:         streamId,
		CreatedAt:           time.Now().Format(time.RFC3339),
		SamplingRate:        avo.samplingRate,
		PublicEncryptionKey: avo.publicEncryptionKey,
	}
}
