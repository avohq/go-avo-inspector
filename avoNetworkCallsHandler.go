package avoinspector

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/avohq/go-avo-inspector/v2/internal/testhooks"
)

// BaseBody is the pre-3.0 wire body.
//
// Deprecated: no longer sent. The SDK now builds its own wire body (SPEC.md §7.3); this type is
// kept only so existing code that references it still compiles.
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

// SessionStartedBody is the pre-3.0 session-started wire body.
//
// Deprecated: no longer sent; server SDKs do not model sessions (SPEC.md §3.3). Kept only so
// existing code that references it still compiles.
type SessionStartedBody struct {
	BaseBody
	Type string `json:"type"`
}

// EventSchemaBody is the pre-3.0 event wire body.
//
// Deprecated: no longer sent. Kept only so existing code that references it still compiles.
type EventSchemaBody struct {
	BaseBody
	Type            string     `json:"type"`
	EventName       string     `json:"eventName"`
	EventProperties []Property `json:"eventProperties"`
	AvoFunction     bool       `json:"avoFunction"`
	EventId         string     `json:"eventId"`
	EventHash       string     `json:"eventHash"`
}

// wireEvent is one element of the request body array (SPEC.md §7.3).
type wireEvent struct {
	ApiKey          string     `json:"apiKey"`
	AppName         string     `json:"appName"`
	AppVersion      *string    `json:"appVersion"`
	LibVersion      string     `json:"libVersion"`
	Env             string     `json:"env"`
	LibPlatform     string     `json:"libPlatform"`
	MessageId       string     `json:"messageId"`
	StreamId        string     `json:"streamId"`
	CreatedAt       string     `json:"createdAt"`
	SamplingRate    float64    `json:"samplingRate"`
	Type            string     `json:"type"`
	EventName       string     `json:"eventName"`
	OutputReference string     `json:"outputReference,omitempty"`
	OriginHint      string     `json:"originHint,omitempty"`
	EventProperties []Property `json:"eventProperties"`
}

// productionEndpoint is where every prod instance sends (SPEC.md §7.1).
const productionEndpoint = "https://api.avo.app/inspector/v2/track"

// trackingEndpoint is the URL used when the mock endpoint does not apply. It is a variable only so
// tests can point it away from the real API: this package's tests set it directly, and the other
// test binaries of this module through internal/testhooks. Nothing outside the module can.
var trackingEndpoint = productionEndpoint

const (
	mockEndpointEnvVar = "AVO_INSPECTOR_MOCK_ENDPOINT"
	gzipThresholdBytes = 1024
	requestTimeout     = 10 * time.Second
	maxResponseBytes   = 64 * 1024
)

type sendStatus int

const (
	sendOk sendStatus = iota
	sendNon200
	sendFailed
)

// sendResult is the outcome of one batch send (SPEC.md §7.5). samplingRate is set only when a 200
// body carried a numeric samplingRate in [0, 1].
type sendResult struct {
	status       sendStatus
	statusCode   int
	samplingRate *float64
	err          error
}

// sendFailure is why a send failed. Its label is fixed text, used both in the log line and as the
// log rate limiter's key, so neither can ever carry text from outside the package.
type sendFailure struct {
	label string
}

func (f *sendFailure) Error() string { return f.label }

// The send failures. They are compared by identity.
var (
	errUnsafeHeader   = &sendFailure{"apiKey contains a control character and cannot be sent as a header"}
	errRequestTimeout = &sendFailure{"Request timed out"}
	errRequestFailed  = &sendFailure{"Request failed"}
	errRequestAborted = &sendFailure{"Request abandoned (destroyed)"}
	// errSerialization replaces the encoder's own message, which quotes the value it rejected.
	errSerialization = &sendFailure{"Request serialization failed"}
)

// AvoNetworkCallsHandler sends batches of events to the Inspector API (SPEC.md §7).
type AvoNetworkCallsHandler struct {
	apiKey string
	env    AvoInspectorEnv
	client *http.Client
}

func newAvoNetworkCallsHandler(apiKey string, env AvoInspectorEnv) *AvoNetworkCallsHandler {
	return &AvoNetworkCallsHandler{
		apiKey: apiKey,
		env:    env,
		client: &http.Client{
			Timeout: requestTimeout,
			// Never follow a redirect: it would carry the api-key header to another host. The
			// 3xx is returned as is and handled as a non-200.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// endpoint resolves the request URL. AVO_INSPECTOR_MOCK_ENDPOINT is honored only for non-prod
// instances, so a prod instance can never be redirected by the environment (SPEC.md §7.1).
func (h *AvoNetworkCallsHandler) endpoint() string {
	if h.env != Prod {
		if override := os.Getenv(mockEndpointEnvVar); override != "" {
			return override
		}
	}
	return trackingEndpoint
}

// isSafeHeaderValue reports whether value can be sent as a header value. SPEC.md §7.2 requires
// refusing CR, LF and NUL; every other Unicode control character (C0, DEL and C1) except tab is
// refused too, the same set the Node and Java SDKs refuse, and so is invalid UTF-8.
func isSafeHeaderValue(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) && r != '\t' {
			return false
		}
	}
	return true
}

// send posts one batch and never retries. ctx is cancelled by Destroy to abandon the request.
func (h *AvoNetworkCallsHandler) send(ctx context.Context, events []wireEvent) sendResult {
	// SPEC.md §7.2: refuse the send rather than rely on the HTTP client to reject (or pass through)
	// a control character in the only caller-supplied header value.
	if !isSafeHeaderValue(h.apiKey) {
		return sendResult{status: sendFailed, err: errUnsafeHeader}
	}

	payload, err := json.Marshal(events)
	if err != nil {
		return sendResult{status: sendFailed, err: errSerialization}
	}
	gzipped := false
	if len(payload) >= gzipThresholdBytes {
		if compressed, err := gzipBytes(payload); err == nil {
			payload = compressed
			gzipped = true
		}
	}

	url := h.endpoint()
	if observe := testhooks.ObserveRequest; observe != nil {
		observe(url)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return sendResult{status: sendFailed, err: errRequestFailed}
	}
	// Content-Length is set by net/http from the fully-buffered body.
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("api-key", h.apiKey)
	req.Header.Set("env", string(h.env))
	req.Header.Set("X-Avo-Client", LibPlatform)
	if gzipped {
		req.Header.Set("Content-Encoding", "gzip")
	}

	res, err := h.client.Do(req)
	if err != nil {
		var netErr net.Error
		if errors.Is(err, context.Canceled) {
			return sendResult{status: sendFailed, err: errRequestAborted}
		}
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
			return sendResult{status: sendFailed, err: errRequestTimeout}
		}
		return sendResult{status: sendFailed, err: errRequestFailed}
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes))

	if res.StatusCode != http.StatusOK {
		return sendResult{status: sendNon200, statusCode: res.StatusCode, err: errors.New("Inspector API returned status " + strconv.Itoa(res.StatusCode))}
	}
	return sendResult{status: sendOk, samplingRate: parseSamplingRate(body)}
}

// parseSamplingRate returns the samplingRate of a 200 body when it is a number in [0, 1], and nil
// otherwise — including for {"success": false}, which must leave the current rate unchanged
// (SPEC.md §7.4, §7.7).
func parseSamplingRate(body []byte) *float64 {
	var response map[string]interface{}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil
	}
	rate, ok := response["samplingRate"].(float64)
	if !ok || rate < 0 || rate > 1 {
		return nil
	}
	return &rate
}

func gzipBytes(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write(data); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
