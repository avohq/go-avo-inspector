package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// Integer literals stay "int" whatever their size, and anything with a fraction or exponent is
// "float" (SPEC.md §9.3.1.1).
func TestHarness_KeepsIntegerLiteralsOutsideInt64AsInt(t *testing.T) {
	input := `{"suite":"schema-extraction","fixture_id":"big","constructor":{"apiKey":"k","env":"dev","version":"1"},` +
		`"input":{"small":3,"uint64":18446744073709551615,"huge":123456789012345678901234567890,"negative":-9223372036854775809,"zero":0.0,"exp":1e3}}` + "\n"
	var stdout bytes.Buffer
	if code := run(strings.NewReader(input), &stdout); code != 0 {
		t.Fatalf("exit code %d: %s", code, stdout.String())
	}
	expected := `"actual":[{"propertyName":"small","propertyType":"int"},{"propertyName":"uint64","propertyType":"int"},` +
		`{"propertyName":"huge","propertyType":"int"},{"propertyName":"negative","propertyType":"int"},` +
		`{"propertyName":"zero","propertyType":"float"},{"propertyName":"exp","propertyType":"float"}]`
	if !strings.Contains(stdout.String(), expected) {
		t.Errorf("unexpected output %s", stdout.String())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("stdout closed") }

// Exit code 0 means the output envelope was written, so a failed write is a harness failure
// (runner contract, "Exit codes").
func TestHarness_ExitsOneWhenTheEnvelopeWriteFails(t *testing.T) {
	input := `{"suite":"schema-extraction","fixture_id":"w","constructor":{"apiKey":"k","env":"dev","version":"1"},"input":{"a":1}}` + "\n"
	if code := run(strings.NewReader(input), failingWriter{}); code != 1 {
		t.Fatalf("exit code %d, want 1", code)
	}
}

// Every field of the input envelope is checked against the runner contract before the SDK is
// constructed: a required field must be present with its type, an optional one has its type when
// present, and null is accepted only where the contract allows it (an extractSchema input). Any
// malformed envelope exits 2. A well-formed envelope whose values the SDK rejects (a blank apiKey)
// exits 1, and a valid one exits 0. Keys the harness does not read are ignored wherever they are
// (see TestHarness_IgnoresUnknownFields).
func TestHarness_ValidatesTheInputEnvelope(t *testing.T) {
	// Valid track cases send from a dev instance: keep them off the real API.
	t.Setenv("AVO_INSPECTOR_MOCK_ENDPOINT", "http://127.0.0.1:1/inspector/v2/track")
	const ctor = `"constructor":{"apiKey":"k","env":"dev","version":"1"}`
	ctorWith := func(fields string) string {
		return `"constructor":{"apiKey":"k","env":"dev","version":"1",` + fields + `}`
	}
	extract := func(constructor, input string) string {
		return `{"suite":"schema-extraction","fixture_id":"t",` + constructor + `,"input":` + input + `}`
	}
	track := func(input string) string {
		return `{"suite":"wire-protocol","fixture_id":"t","operation":"trackSchemaFromEvent",` + ctor + `,"input":` + input + `}`
	}
	trackWith := func(fields string) string {
		return track(`{"eventName":"e","eventProperties":{"a":1},` + fields + `}`)
	}
	sequence := func(steps string) string {
		return `{"suite":"batching","fixture_id":"t","operation":"sequence",` +
			`"constructor":{"apiKey":"k","env":"staging","version":"1"},"steps":` + steps + `}`
	}
	const validTrack = `{"action":"track","eventName":"e","eventProperties":{"a":1}}`

	for _, tc := range []struct {
		name, envelope string
		want           int
	}{
		// The envelope and its top-level fields.
		{"not JSON", `{`, 2},
		{"not an object", `[]`, 2},
		{"fixture_id missing", `{"suite":"schema-extraction",` + ctor + `,"input":{}}`, 2},
		{"fixture_id not a string", `{"suite":"schema-extraction","fixture_id":3,` + ctor + `,"input":{}}`, 2},
		{"fixture_id null", `{"suite":"schema-extraction","fixture_id":null,` + ctor + `,"input":{}}`, 2},
		{"suite missing", `{"fixture_id":"t",` + ctor + `,"input":{}}`, 2},
		{"suite not a string", `{"suite":3,"fixture_id":"t",` + ctor + `,"input":{}}`, 2},
		{"suite null", `{"suite":null,"fixture_id":"t",` + ctor + `,"input":{}}`, 2},
		{"suite unknown", `{"suite":"other","fixture_id":"t",` + ctor + `,"input":{}}`, 2},
		{"constructor missing", `{"suite":"schema-extraction","fixture_id":"t","input":{}}`, 2},
		{"constructor null", `{"suite":"schema-extraction","fixture_id":"t","constructor":null,"input":{}}`, 2},
		{"constructor not an object", `{"suite":"schema-extraction","fixture_id":"t","constructor":[],"input":{}}`, 2},
		{"operation missing outside schema-extraction", `{"suite":"wire-protocol","fixture_id":"t",` + ctor + `,"input":{}}`, 2},
		{"operation not a string", `{"suite":"wire-protocol","fixture_id":"t","operation":3,` + ctor + `,"input":{}}`, 2},
		{"operation null", `{"suite":"wire-protocol","fixture_id":"t","operation":null,` + ctor + `,"input":{}}`, 2},
		{"operation unsupported", `{"suite":"wire-protocol","fixture_id":"t","operation":"other",` + ctor + `,"input":{}}`, 2},
		{"schema-extraction with another operation", `{"suite":"schema-extraction","fixture_id":"t","operation":"trackSchemaFromEvent",` + ctor + `,"input":{}}`, 2},
		{"schema-extraction with operation extractSchema", `{"suite":"schema-extraction","fixture_id":"t","operation":"extractSchema",` + ctor + `,"input":{}}`, 0},
		{"precondition null", `{"suite":"schema-extraction","fixture_id":"t",` + ctor + `,"input":{},"precondition":null}`, 2},
		{"precondition not an object", `{"suite":"schema-extraction","fixture_id":"t",` + ctor + `,"input":{},"precondition":[]}`, 2},
		{"precondition samplingRate not a number", `{"suite":"schema-extraction","fixture_id":"t",` + ctor + `,"input":{},"precondition":{"samplingRate":"1"}}`, 2},
		{"precondition samplingRate null", `{"suite":"schema-extraction","fixture_id":"t",` + ctor + `,"input":{},"precondition":{"samplingRate":null}}`, 2},
		{"precondition unsupported field", `{"suite":"schema-extraction","fixture_id":"t",` + ctor + `,"input":{},"precondition":{"other":1}}`, 2},
		{"precondition samplingRate", `{"suite":"schema-extraction","fixture_id":"t",` + ctor + `,"input":{},"precondition":{"samplingRate":0.5}}`, 0},
		{"runner-owned top-level keys", `{"suite":"schema-extraction","fixture_id":"t",` + ctor + `,"input":{},"expected":[],"mock_response":null,"description":"d"}`, 0},

		// constructor.
		{"apiKey missing", extract(`"constructor":{"env":"dev","version":"1"}`, `{}`), 2},
		{"apiKey not a string", extract(`"constructor":{"apiKey":3,"env":"dev","version":"1"}`, `{}`), 2},
		{"apiKey null", extract(`"constructor":{"apiKey":null,"env":"dev","version":"1"}`, `{}`), 2},
		{"env missing", extract(`"constructor":{"apiKey":"k","version":"1"}`, `{}`), 2},
		{"env not a string", extract(`"constructor":{"apiKey":"k","env":1,"version":"1"}`, `{}`), 2},
		{"env not dev, staging or prod", extract(`"constructor":{"apiKey":"k","env":"test","version":"1"}`, `{}`), 2},
		{"version missing", extract(`"constructor":{"apiKey":"k","env":"dev"}`, `{}`), 2},
		{"version null", extract(`"constructor":{"apiKey":"k","env":"dev","version":null}`, `{}`), 2},
		{"env staging", extract(`"constructor":{"apiKey":"k","env":"staging","version":"1"}`, `{}`), 0},
		{"env prod", extract(`"constructor":{"apiKey":"k","env":"prod","version":"1"}`, `{}`), 0},
		{"blank apiKey is the SDK's to reject", extract(`"constructor":{"apiKey":"","env":"dev","version":"1"}`, `{}`), 1},
		{"blank version is the SDK's to reject", extract(`"constructor":{"apiKey":"k","env":"dev","version":" "}`, `{}`), 1},
		{"a malformed envelope exits 2 even when the SDK would reject it", `{"suite":"wire-protocol","fixture_id":"t","operation":"other","constructor":{"apiKey":"","env":"dev","version":"1"},"input":{}}`, 2},
		{"appName not a string", extract(ctorWith(`"appName":3`), `{}`), 2},
		{"appName null", extract(ctorWith(`"appName":null`), `{}`), 2},
		{"appName", extract(ctorWith(`"appName":"a"`), `{}`), 0},
		{"batchSize fractional", extract(ctorWith(`"batchSize":2.5`), `{}`), 2},
		{"batchSize out of range", extract(ctorWith(`"batchSize":1e30`), `{}`), 2},
		{"batchSize integer literal beyond int64", extract(ctorWith(`"batchSize":99999999999999999999`), `{}`), 2},
		{"batchSize not a number", extract(ctorWith(`"batchSize":"3"`), `{}`), 2},
		{"batchSize null", extract(ctorWith(`"batchSize":null`), `{}`), 2},
		{"batchSize", extract(ctorWith(`"batchSize":3`), `{}`), 0},
		{"batchSize whole float", extract(ctorWith(`"batchSize":3.0`), `{}`), 0},
		{"batchSize below 1 is the SDK's to correct", extract(ctorWith(`"batchSize":-1`), `{}`), 0},
		{"batchSize 0", extract(ctorWith(`"batchSize":0`), `{}`), 0},
		{"maxQueueSize fractional", extract(ctorWith(`"maxQueueSize":2.5`), `{}`), 2},
		{"maxQueueSize out of range", extract(ctorWith(`"maxQueueSize":1e30`), `{}`), 2},
		{"maxQueueSize integer literal beyond int64", extract(ctorWith(`"maxQueueSize":99999999999999999999`), `{}`), 2},
		{"maxQueueSize not a number", extract(ctorWith(`"maxQueueSize":"3"`), `{}`), 2},
		{"maxQueueSize null", extract(ctorWith(`"maxQueueSize":null`), `{}`), 2},
		{"maxQueueSize", extract(ctorWith(`"maxQueueSize":10`), `{}`), 0},
		{"maxQueueSize below 1 is the SDK's to correct", extract(ctorWith(`"maxQueueSize":-1`), `{}`), 0},
		{"batchFlushSeconds not a number", extract(ctorWith(`"batchFlushSeconds":"2"`), `{}`), 2},
		{"batchFlushSeconds boolean", extract(ctorWith(`"batchFlushSeconds":true`), `{}`), 2},
		{"batchFlushSeconds object", extract(ctorWith(`"batchFlushSeconds":{}`), `{}`), 2},
		{"batchFlushSeconds null", extract(ctorWith(`"batchFlushSeconds":null`), `{}`), 2},
		{"batchFlushSeconds", extract(ctorWith(`"batchFlushSeconds":2`), `{}`), 0},
		{"batchFlushSeconds fractional", extract(ctorWith(`"batchFlushSeconds":2.5`), `{}`), 0},
		{"batchFlushSeconds 0", extract(ctorWith(`"batchFlushSeconds":0`), `{}`), 0},
		{"batchFlushSeconds negative is the SDK's to correct", extract(ctorWith(`"batchFlushSeconds":-1`), `{}`), 0},
		{"batchFlushSeconds huge is the SDK's to cap", extract(ctorWith(`"batchFlushSeconds":99999999999999999999`), `{}`), 0},
		{"disableBatchTimer string", extract(ctorWith(`"disableBatchTimer":"true"`), `{}`), 2},
		{"disableBatchTimer number", extract(ctorWith(`"disableBatchTimer":1`), `{}`), 2},
		{"disableBatchTimer array", extract(ctorWith(`"disableBatchTimer":[]`), `{}`), 2},
		{"disableBatchTimer null", extract(ctorWith(`"disableBatchTimer":null`), `{}`), 2},
		{"disableBatchTimer true", extract(ctorWith(`"disableBatchTimer":true`), `{}`), 0},
		{"disableBatchTimer false", extract(ctorWith(`"disableBatchTimer":false`), `{}`), 0},

		// extractSchema input.
		{"extractSchema input missing", `{"suite":"schema-extraction","fixture_id":"t",` + ctor + `}`, 2},
		{"extractSchema input number", extract(ctor, `3`), 2},
		{"extractSchema input string", extract(ctor, `"x"`), 2},
		{"extractSchema input array", extract(ctor, `[]`), 2},
		{"extractSchema input null", extract(ctor, `null`), 0},
		{"extractSchema input", extract(ctor, `{"a":1}`), 0},

		// trackSchemaFromEvent input.
		{"track input missing", `{"suite":"wire-protocol","fixture_id":"t","operation":"trackSchemaFromEvent",` + ctor + `}`, 2},
		{"track input null", track(`null`), 2},
		{"track input array", track(`[]`), 2},
		{"track input number", track(`3`), 2},
		{"eventName missing", track(`{"eventProperties":{}}`), 2},
		{"eventName not a string", track(`{"eventName":3,"eventProperties":{}}`), 2},
		{"eventName null", track(`{"eventName":null,"eventProperties":{}}`), 2},
		{"blank eventName is the SDK's to handle", track(`{"eventName":"","eventProperties":{}}`), 0},
		{"eventProperties missing", track(`{"eventName":"e"}`), 2},
		{"eventProperties null", track(`{"eventName":"e","eventProperties":null}`), 2},
		{"eventProperties array", track(`{"eventName":"e","eventProperties":[]}`), 2},
		{"eventProperties string", track(`{"eventName":"e","eventProperties":"x"}`), 2},
		{"streamId not a string", trackWith(`"streamId":3`), 2},
		{"streamId null", trackWith(`"streamId":null`), 2},
		{"streamId", trackWith(`"streamId":"s"`), 0},
		{"options null", trackWith(`"options":null`), 2},
		{"options not an object", trackWith(`"options":[]`), 2},
		{"option value not a string", trackWith(`"options":{"originHint":3}`), 2},
		{"option value null", trackWith(`"options":{"originAppVersion":null}`), 2},
		{"options", trackWith(`"options":{"outputReference":" o ","originHint":"web","originAppVersion":""}`), 0},
		{"empty options", trackWith(`"options":{}`), 0},
		{"track", trackWith(`"streamId":"s","options":{"originHint":"web"}`), 0},

		// sequence steps.
		{"steps missing", `{"suite":"batching","fixture_id":"t","operation":"sequence",` + ctor + `}`, 2},
		{"steps null", sequence(`null`), 2},
		{"steps not an array", sequence(`{}`), 2},
		{"step not an object", sequence(`[3]`), 2},
		{"action missing", sequence(`[{}]`), 2},
		{"action not a string", sequence(`[{"action":3}]`), 2},
		{"action unsupported", sequence(`[{"action":"other"}]`), 2},
		{"track step eventName missing", sequence(`[{"action":"track","eventProperties":{}}]`), 2},
		{"track step eventProperties null", sequence(`[{"action":"track","eventName":"e","eventProperties":null}]`), 2},
		{"track step option not a string", sequence(`[{"action":"track","eventName":"e","eventProperties":{},"options":{"originHint":3}}]`), 2},
		{"track step", sequence(`[` + validTrack + `]`), 0},
		{"a malformed later step exits 2 before any step runs", sequence(`[` + validTrack + `,{"action":"flush","timeoutMs":-1}]`), 2},
		{"trackN count missing", sequence(`[{"action":"trackN","eventNamePrefix":"E"}]`), 2},
		{"trackN count 0", sequence(`[{"action":"trackN","count":0,"eventNamePrefix":"E"}]`), 2},
		{"trackN count fractional", sequence(`[{"action":"trackN","count":2.5,"eventNamePrefix":"E"}]`), 2},
		{"trackN count not a number", sequence(`[{"action":"trackN","count":"3","eventNamePrefix":"E"}]`), 2},
		{"trackN count null", sequence(`[{"action":"trackN","count":null,"eventNamePrefix":"E"}]`), 2},
		{"trackN count out of range", sequence(`[{"action":"trackN","count":1e30,"eventNamePrefix":"E"}]`), 2},
		{"trackN eventNamePrefix missing", sequence(`[{"action":"trackN","count":2}]`), 2},
		{"trackN eventNamePrefix not a string", sequence(`[{"action":"trackN","count":2,"eventNamePrefix":3}]`), 2},
		{"trackN streamId not a string", sequence(`[{"action":"trackN","count":2,"eventNamePrefix":"E","streamId":3}]`), 2},
		{"trackN", sequence(`[{"action":"trackN","count":2,"eventNamePrefix":"E","streamId":"s"}]`), 0},
		{"flush timeoutMs negative", sequence(`[{"action":"flush","timeoutMs":-1}]`), 2},
		{"flush timeoutMs beyond time.Duration", sequence(`[{"action":"flush","timeoutMs":1e13}]`), 2},
		{"flush timeoutMs out of range", sequence(`[{"action":"flush","timeoutMs":1e30}]`), 2},
		{"flush timeoutMs integer literal beyond int64", sequence(`[{"action":"flush","timeoutMs":99999999999999999999}]`), 2},
		{"flush timeoutMs not a number", sequence(`[{"action":"flush","timeoutMs":"5"}]`), 2},
		{"flush timeoutMs null", sequence(`[{"action":"flush","timeoutMs":null}]`), 2},
		{"flush", sequence(`[{"action":"flush"}]`), 0},
		{"flush timeoutMs 0", sequence(`[{"action":"flush","timeoutMs":0}]`), 0},
		{"flush timeoutMs", sequence(`[{"action":"flush","timeoutMs":5}]`), 0},
		{"flush timeoutMs fractional", sequence(`[{"action":"flush","timeoutMs":2.5}]`), 0},
		{"destroy", sequence(`[{"action":"destroy"}]`), 0},
	} {
		var stdout bytes.Buffer
		if code := run(strings.NewReader(tc.envelope+"\n"), &stdout); code != tc.want {
			t.Errorf("%s: exit code %d, want %d: %s", tc.name, code, tc.want, strings.TrimSpace(stdout.String()))
		}
	}
}

// Unknown fields are ignored wherever they appear: in the envelope, the constructor, a track input
// and its options, every kind of step, and mock_response, which belongs to the suite runner. Only
// an unknown precondition field exits 2, since the contract requires every precondition applied.
func TestHarness_IgnoresUnknownFields(t *testing.T) {
	for _, tc := range []struct {
		name, envelope string
		want           int
	}{
		{"envelope, constructor and mock_response", `{"suite":"schema-extraction","fixture_id":"t","future":{"x":1},` +
			`"constructor":{"apiKey":"k","env":"dev","version":"1","future":true},"input":{"a":1},` +
			`"mock_response":{"status":200,"body":{},"future":"x","delayMs":5}}`, 0},
		{"track input and options", `{"suite":"wire-protocol","fixture_id":"t","operation":"trackSchemaFromEvent",` +
			`"constructor":{"apiKey":"k","env":"dev","version":"1"},` +
			`"input":{"eventName":"e","eventProperties":{},"future":1,"options":{"originHint":"web","future":"x"}},` +
			`"mock_response":{"status":200,"future":[1]}}`, 0},
		{"every step", `{"suite":"batching","fixture_id":"t","operation":"sequence",` +
			`"constructor":{"apiKey":"k","env":"staging","version":"1","batchSize":100},"steps":[` +
			`{"action":"track","eventName":"e","eventProperties":{},"future":1,"options":{"future":"x"}},` +
			`{"action":"trackN","count":2,"eventNamePrefix":"E","future":1},` +
			`{"action":"flush","timeoutMs":0,"future":1},` +
			`{"action":"destroy","future":1}],"mock_response":{"future":true}}`, 0},
		{"precondition", `{"suite":"schema-extraction","fixture_id":"t",` +
			`"constructor":{"apiKey":"k","env":"dev","version":"1"},"input":{},"precondition":{"future":1}}`, 2},
	} {
		var stdout bytes.Buffer
		if code := run(strings.NewReader(tc.envelope+"\n"), &stdout); code != tc.want {
			t.Errorf("%s: exit code %d, want %d: %s", tc.name, code, tc.want, strings.TrimSpace(stdout.String()))
		}
	}
}

// A flush step's value is whether the flush drained: true when Flush returns nil, false when it
// returns ErrFlushTimeout. The other steps' values are unchanged.
func TestHarness_FlushStepReportsWhetherItDrained(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"samplingRate":1}`))
	}))
	defer ok.Close()
	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer hung.Close()
	defer close(release)

	const track = `{"action":"track","eventName":"e","eventProperties":{}}`
	for _, tc := range []struct {
		name, endpoint, steps string
		want                  []interface{}
	}{
		{"nothing pending", ok.URL, `[{"action":"flush"}]`, []interface{}{true}},
		{"sent", ok.URL, `[` + track + `,{"action":"flush"}]`, []interface{}{nil, true}},
		{"timed out", hung.URL, `[` + track + `,{"action":"flush","timeoutMs":50},{"action":"destroy"}]`, []interface{}{nil, false, nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AVO_INSPECTOR_MOCK_ENDPOINT", tc.endpoint+"/inspector/v2/track")
			envelope := `{"suite":"batching","fixture_id":"t","operation":"sequence",` +
				`"constructor":{"apiKey":"k","env":"staging","version":"1"},"steps":` + tc.steps + "}\n"
			var stdout bytes.Buffer
			if code := run(strings.NewReader(envelope), &stdout); code != 0 {
				t.Fatalf("exit code %d: %s", code, stdout.String())
			}
			var output struct {
				Actual []struct {
					Action string      `json:"action"`
					Value  interface{} `json:"value"`
				} `json:"actual"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
				t.Fatal(err)
			}
			var got []interface{}
			for _, record := range output.Actual {
				if record.Action == "track" {
					got = append(got, nil) // a track step's value is its schema
					continue
				}
				got = append(got, record.Value)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("step values %v, want %v: %s", got, tc.want, stdout.String())
			}
		})
	}
}
