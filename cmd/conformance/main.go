// Command conformance is the Avo Inspector conformance harness (the avo-inspector-conformance
// binary of the spec's conformance/runner-contract.md). It reads one JSON input envelope from
// stdin, runs the requested operation against a fresh AvoInspector, and writes one JSON output
// envelope to stdout. It contains no assertion logic; the spec's suite runner does the asserting.
// Diagnostics go to stderr.
//
// Run it through scripts/run-conformance.sh.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	avoinspector "github.com/avohq/go-avo-inspector/v2"
	"github.com/avohq/go-avo-inspector/v2/internal/testhooks"
)

// HarnessContractVersion is the version of conformance/runner-contract.md this harness implements.
const HarnessContractVersion = "1.1.0"

type outputEnvelope struct {
	FixtureID *string     `json:"fixture_id"`
	Passed    bool        `json:"passed"`
	Actual    interface{} `json:"actual"`
	Outcome   string      `json:"outcome"`
	Error     *string     `json:"error"`
}

type stepRecord struct {
	Action  string      `json:"action"`
	Outcome string      `json:"outcome"`
	Value   interface{} `json:"value"`
}

// maxTimeoutMs is the largest flush timeoutMs a time.Duration holds.
const maxTimeoutMs = float64(math.MaxInt64 / int64(time.Millisecond))

// configError is an envelope problem: exit code 2.
type configError struct{ message string }

func (e configError) Error() string { return e.message }

func main() {
	os.Exit(run(os.Stdin, os.Stdout))
}

func run(stdin io.Reader, stdout io.Writer) int {
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		writeEnvelope(stdout, nil, false, nil, "resolve", "stdin read failed: "+err.Error())
		return 2
	}
	decoded, err := decodeJSON(line)
	if err != nil {
		writeEnvelope(stdout, nil, false, nil, "resolve", "input JSON parse failed: "+err.Error())
		return 2
	}
	envelope, ok := decoded.(avoinspector.OrderedMap)
	if !ok {
		writeEnvelope(stdout, nil, false, nil, "resolve", "input envelope is not a JSON object")
		return 2
	}
	fixtureID, ok := getString(envelope, "fixture_id")
	if !ok {
		writeEnvelope(stdout, nil, false, nil, "resolve", "missing fixture_id")
		return 2
	}

	// The whole envelope is checked before the SDK is constructed, so a malformed envelope always
	// exits 2 and no step runs before a later one is found malformed.
	req, err := parseRequest(envelope)
	if err != nil {
		writeEnvelope(stdout, &fixtureID, false, nil, "resolve", err.Error())
		return 2
	}
	inspector, err := avoinspector.NewAvoInspectorWithOptions(req.options)
	if err != nil {
		writeEnvelope(stdout, &fixtureID, false, nil, "resolve", "Constructor threw: "+err.Error())
		return 1
	}
	if req.samplingRate != nil {
		testhooks.SetSamplingRate(inspector, *req.samplingRate)
	}

	var actual interface{}
	outcome := "resolve"
	switch req.operation {
	case "extractSchema":
		actual = inspector.ExtractOrderedSchema(req.input)
	case "trackSchemaFromEvent":
		actual, outcome = track(inspector, req.track)
	case "sequence":
		actual = runSequence(inspector, req.steps)
	}

	if err := writeEnvelope(stdout, &fixtureID, true, actual, outcome, ""); err != nil {
		fmt.Fprintln(os.Stderr, "output write failed:", err)
		return 1
	}
	return 0
}

// request is a checked input envelope.
type request struct {
	options      avoinspector.Options
	samplingRate *float64
	operation    string
	input        avoinspector.OrderedMap // extractSchema; nil for an explicit null
	track        trackCall               // trackSchemaFromEvent
	steps        []step                  // sequence
}

type trackCall struct {
	eventName  string
	properties avoinspector.OrderedMap
	options    avoinspector.TrackOptions
}

type step struct {
	action   string
	track    trackCall     // track
	count    int           // trackN
	prefix   string        // trackN
	streamID string        // trackN
	timeout  time.Duration // flush
}

var (
	suites       = []string{"schema-extraction", "wire-protocol", "error-handling", "batching"}
	environments = []string{"dev", "staging", "prod"}
)

// parseRequest checks the envelope against the runner contract (input envelope). A required field
// must be present with its type and an optional one has its type when present; null is accepted
// only for an extractSchema input. Inside the constructor, a track input, its options and a step,
// a key the harness would ignore is an error. Top-level keys the harness does not read
// (expected_*, mock_response, description, ...) belong to the suite runner.
func parseRequest(envelope avoinspector.OrderedMap) (request, error) {
	var req request
	suite, err := requireString(envelope, "", "suite")
	if err != nil {
		return req, err
	}
	if !contains(suites, suite) {
		return req, configError{"unsupported suite: " + suite}
	}
	constructor, err := requireObject(envelope, "", "constructor")
	if err != nil {
		return req, err
	}
	if req.options, err = constructorOptions(constructor); err != nil {
		return req, err
	}
	if req.samplingRate, err = precondition(envelope); err != nil {
		return req, err
	}

	operation, present, err := optionalString(envelope, "", "operation")
	switch {
	case err != nil:
		return req, err
	case !present && suite == "schema-extraction":
		operation = "extractSchema"
	case !present:
		return req, configError{"operation is required"}
	case suite == "schema-extraction" && operation != "extractSchema":
		return req, configError{"the schema-extraction suite runs only extractSchema"}
	}
	req.operation = operation

	switch operation {
	case "extractSchema":
		input, present := get(envelope, "input")
		if !present {
			return req, configError{"input is required"}
		}
		if input != nil {
			if req.input, present = input.(avoinspector.OrderedMap); !present {
				return req, configError{"input must be an object or null"}
			}
		}
	case "trackSchemaFromEvent":
		input, err := requireObject(envelope, "", "input")
		if err != nil {
			return req, err
		}
		if req.track, err = parseTrack(input, "input."); err != nil {
			return req, err
		}
	case "sequence":
		if req.steps, err = parseSteps(envelope); err != nil {
			return req, err
		}
	default:
		return req, configError{"unsupported operation: " + operation}
	}
	return req, nil
}

// constructorOptions maps the constructor block to Options. apiKey, env and version are required
// strings, and env is dev, staging or prod; a blank string is left to the SDK to reject. batchSize
// and maxQueueSize are integers, batchFlushSeconds a number and disableBatchTimer a boolean; their
// range is the SDK's to check.
func constructorOptions(constructor avoinspector.OrderedMap) (avoinspector.Options, error) {
	options := avoinspector.Options{}
	if err := checkKeys(constructor, "constructor.", "apiKey", "env", "version", "appName",
		"batchSize", "batchFlushSeconds", "maxQueueSize", "disableBatchTimer"); err != nil {
		return options, err
	}
	var err error
	if options.ApiKey, err = requireString(constructor, "constructor.", "apiKey"); err != nil {
		return options, err
	}
	env, err := requireString(constructor, "constructor.", "env")
	if err != nil {
		return options, err
	}
	if !contains(environments, env) {
		return options, configError{"constructor.env must be dev, staging or prod"}
	}
	options.Env = avoinspector.AvoInspectorEnv(env)
	if options.AppVersion, err = requireString(constructor, "constructor.", "version"); err != nil {
		return options, err
	}
	if options.AppName, _, err = optionalString(constructor, "constructor.", "appName"); err != nil {
		return options, err
	}
	if options.BatchSize, _, err = optionalInt(constructor, "constructor.", "batchSize"); err != nil {
		return options, err
	}
	if options.BatchFlushSeconds, _, err = optionalNumber(constructor, "constructor.", "batchFlushSeconds"); err != nil {
		return options, err
	}
	if options.MaxQueueSize, _, err = optionalInt(constructor, "constructor.", "maxQueueSize"); err != nil {
		return options, err
	}
	if value, present := get(constructor, "disableBatchTimer"); present {
		disable, ok := value.(bool)
		if !ok {
			return options, configError{"constructor.disableBatchTimer must be a boolean"}
		}
		options.DisableBatchTimer = disable
	}
	return options, nil
}

// precondition returns the samplingRate precondition, if any. Any other precondition field is
// unsupported, which the runner contract makes an exit-2 error.
func precondition(envelope avoinspector.OrderedMap) (*float64, error) {
	if _, present := get(envelope, "precondition"); !present {
		return nil, nil
	}
	fields, err := requireObject(envelope, "", "precondition")
	if err != nil {
		return nil, err
	}
	if err := checkKeys(fields, "precondition.", "samplingRate"); err != nil {
		return nil, err
	}
	rate, present, err := optionalNumber(fields, "precondition.", "samplingRate")
	if err != nil || !present {
		return nil, err
	}
	return &rate, nil
}

// parseTrack checks a track input or a track step: eventName is a required string,
// eventProperties a required object, streamId an optional string, and options an optional object
// of optional strings, passed through verbatim (normalizing them is the SDK's job).
func parseTrack(object avoinspector.OrderedMap, where string, extraKeys ...string) (trackCall, error) {
	var call trackCall
	if err := checkKeys(object, where, append([]string{"eventName", "eventProperties", "streamId", "options"}, extraKeys...)...); err != nil {
		return call, err
	}
	var err error
	if call.eventName, err = requireString(object, where, "eventName"); err != nil {
		return call, err
	}
	if call.properties, err = requireObject(object, where, "eventProperties"); err != nil {
		return call, err
	}
	if call.options.StreamId, _, err = optionalString(object, where, "streamId"); err != nil {
		return call, err
	}
	if _, present := get(object, "options"); !present {
		return call, nil
	}
	options, err := requireObject(object, where, "options")
	if err != nil {
		return call, err
	}
	where += "options."
	if err := checkKeys(options, where, "outputReference", "originHint", "originAppVersion"); err != nil {
		return call, err
	}
	if call.options.OutputReference, _, err = optionalString(options, where, "outputReference"); err != nil {
		return call, err
	}
	if call.options.OriginHint, _, err = optionalString(options, where, "originHint"); err != nil {
		return call, err
	}
	call.options.OriginAppVersion, _, err = optionalString(options, where, "originAppVersion")
	return call, err
}

// parseSteps checks every step of a sequence before any of them runs.
func parseSteps(envelope avoinspector.OrderedMap) ([]step, error) {
	value, _ := get(envelope, "steps")
	rawSteps, ok := value.([]interface{})
	if !ok {
		return nil, configError{"sequence operation requires a steps array"}
	}
	steps := make([]step, 0, len(rawSteps))
	for i, rawStep := range rawSteps {
		where := "steps[" + strconv.Itoa(i) + "]."
		object, ok := rawStep.(avoinspector.OrderedMap)
		if !ok {
			return nil, configError{"steps[" + strconv.Itoa(i) + "] must be an object"}
		}
		action, err := requireString(object, where, "action")
		if err != nil {
			return nil, err
		}
		s := step{action: action, timeout: avoinspector.DefaultFlushTimeout}
		switch action {
		case "track":
			s.track, err = parseTrack(object, where, "action")
		case "trackN":
			s, err = parseTrackN(object, where, s)
		case "flush":
			if err = checkKeys(object, where, "action", "timeoutMs"); err != nil {
				break
			}
			ms, present, numErr := optionalNumber(object, where, "timeoutMs")
			switch {
			case numErr != nil:
				err = numErr
			case present && !(ms >= 0 && ms <= maxTimeoutMs):
				err = configError{where + "timeoutMs must be a number from 0 to " + strconv.FormatInt(int64(maxTimeoutMs), 10)}
			case present:
				s.timeout = time.Duration(ms * float64(time.Millisecond))
			}
		case "destroy":
			err = checkKeys(object, where, "action")
		default:
			err = configError{"unsupported sequence action: " + action}
		}
		if err != nil {
			return nil, err
		}
		steps = append(steps, s)
	}
	return steps, nil
}

// parseTrackN checks a trackN step: count is a required integer >= 1, eventNamePrefix a required
// string and streamId an optional string.
func parseTrackN(object avoinspector.OrderedMap, where string, s step) (step, error) {
	if err := checkKeys(object, where, "action", "count", "eventNamePrefix", "streamId"); err != nil {
		return s, err
	}
	count, present, err := optionalInt(object, where, "count")
	if err != nil || !present || count < 1 {
		return s, configError{where + "count must be an integer >= 1"}
	}
	s.count = count
	if s.prefix, err = requireString(object, where, "eventNamePrefix"); err != nil {
		return s, err
	}
	s.streamID, _, err = optionalString(object, where, "streamId")
	return s, err
}

// track calls the SDK with a checked track input. The option values are passed verbatim.
func track(inspector *avoinspector.AvoInspector, call trackCall) (interface{}, string) {
	schema, err := inspector.TrackOrderedSchemaFromEvent(call.eventName, call.properties, call.options)
	if err != nil {
		return err.Error(), "reject"
	}
	return schema, "resolve"
}

func runSequence(inspector *avoinspector.AvoInspector, steps []step) []stepRecord {
	records := make([]stepRecord, 0, len(steps))
	for _, s := range steps {
		switch s.action {
		case "track":
			actual, outcome := track(inspector, s.track)
			records = append(records, stepRecord{"track", outcome, actual})
		case "trackN":
			var wg sync.WaitGroup
			for i := 0; i < s.count; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					_, _ = inspector.TrackOrderedSchemaFromEvent(s.prefix+strconv.Itoa(i), avoinspector.OrderedMap{}, avoinspector.TrackOptions{StreamId: s.streamID})
				}(i)
			}
			wg.Wait()
			records = append(records, stepRecord{"trackN", "resolve", s.count})
		case "flush":
			_ = inspector.Flush(s.timeout)
			records = append(records, stepRecord{"flush", "resolve", nil})
		case "destroy":
			inspector.Destroy()
			records = append(records, stepRecord{"destroy", "resolve", nil})
		}
	}
	return records
}

// writeEnvelope writes the output envelope line and returns the write error. The error paths in
// run ignore it: they already exit nonzero.
func writeEnvelope(stdout io.Writer, fixtureID *string, passed bool, actual interface{}, outcome string, errorMessage string) error {
	envelope := outputEnvelope{FixtureID: fixtureID, Passed: passed, Actual: actual, Outcome: outcome}
	if errorMessage != "" {
		envelope.Error = &errorMessage
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		fmt.Fprintln(os.Stderr, "output encoding failed:", err)
		encoded = []byte(`{"fixture_id":null,"passed":false,"actual":null,"outcome":"resolve","error":"output encoding failed"}`)
	}
	_, err = fmt.Fprintf(stdout, "%s\n", encoded)
	return err
}

// decodeJSON decodes a JSON document keeping what a map[string]interface{} would lose: objects
// become avoinspector.OrderedMap in source order, and a number literal written as an integer
// becomes an int64 (or stays a json.Number outside the int64 range) and any other becomes a
// float64, so 3 is "int" and 3.0 is "float" (SPEC.md §9.3.1.1).
func decodeJSON(document string) (interface{}, error) {
	decoder := json.NewDecoder(strings.NewReader(document))
	decoder.UseNumber()
	value, err := decodeValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after the JSON document")
	}
	return value, nil
}

func decodeValue(decoder *json.Decoder) (interface{}, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch t := token.(type) {
	case json.Delim:
		switch t {
		case '{':
			object := avoinspector.OrderedMap{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, _ := keyToken.(string)
				value, err := decodeValue(decoder)
				if err != nil {
					return nil, err
				}
				object = append(object, avoinspector.KeyValue{Key: key, Value: value})
			}
			_, err := decoder.Token()
			return object, err
		case '[':
			array := []interface{}{}
			for decoder.More() {
				value, err := decodeValue(decoder)
				if err != nil {
					return nil, err
				}
				array = append(array, value)
			}
			_, err := decoder.Token()
			return array, err
		}
		return nil, fmt.Errorf("unexpected delimiter %v", t)
	case json.Number:
		if strings.ContainsAny(string(t), ".eE") {
			return t.Float64()
		}
		if integer, err := t.Int64(); err == nil {
			return integer, nil
		}
		// Outside the int64 range: pass the literal through, which the SDK classifies as "int".
		return t, nil
	default:
		return t, nil
	}
}

func get(object avoinspector.OrderedMap, key string) (interface{}, bool) {
	for _, entry := range object {
		if entry.Key == key {
			return entry.Value, true
		}
	}
	return nil, false
}

func getString(object avoinspector.OrderedMap, key string) (string, bool) {
	value, _ := get(object, key)
	s, ok := value.(string)
	return s, ok
}

// checkKeys returns a configError for the first key of object that is not one of allowed.
func checkKeys(object avoinspector.OrderedMap, where string, allowed ...string) error {
	for _, entry := range object {
		if !contains(allowed, entry.Key) {
			return configError{"unsupported field: " + where + entry.Key}
		}
	}
	return nil
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

// requireString returns a required string field.
func requireString(object avoinspector.OrderedMap, where, key string) (string, error) {
	s, present, err := optionalString(object, where, key)
	if err == nil && !present {
		err = configError{where + key + " is required"}
	}
	return s, err
}

// optionalString returns a string field and whether it is present. A present value that is not a
// string, null included, is a configError.
func optionalString(object avoinspector.OrderedMap, where, key string) (string, bool, error) {
	value, present := get(object, key)
	if !present {
		return "", false, nil
	}
	s, ok := value.(string)
	if !ok {
		return "", true, configError{where + key + " must be a string"}
	}
	return s, true, nil
}

// requireObject returns a required object field; null is not an object.
func requireObject(object avoinspector.OrderedMap, where, key string) (avoinspector.OrderedMap, error) {
	value, present := get(object, key)
	if !present {
		return nil, configError{where + key + " is required"}
	}
	m, ok := value.(avoinspector.OrderedMap)
	if !ok {
		return nil, configError{where + key + " must be an object"}
	}
	return m, nil
}

// optionalNumber returns a number field and whether it is present. A present value that is not a
// number, null included, is a configError.
func optionalNumber(object avoinspector.OrderedMap, where, key string) (float64, bool, error) {
	value, present := get(object, key)
	if !present {
		return 0, false, nil
	}
	number, ok := toFloat(value)
	if !ok {
		return 0, true, configError{where + key + " must be a number"}
	}
	return number, true, nil
}

// optionalInt returns an integer field and whether it is present. A present value that is not a
// whole number within int64 range is a configError.
func optionalInt(object avoinspector.OrderedMap, where, key string) (int, bool, error) {
	number, present, err := optionalNumber(object, where, key)
	if err != nil || present && (number != math.Trunc(number) || number < math.MinInt64 || number >= math.MaxInt64) {
		return 0, true, configError{where + key + " must be an integer"}
	}
	return int(number), present, nil
}

func toFloat(value interface{}) (float64, bool) {
	switch v := value.(type) {
	case int64:
		return float64(v), true
	case float64:
		return v, true
	case json.Number:
		// An integer literal outside the int64 range (see decodeJSON).
		f, err := strconv.ParseFloat(string(v), 64)
		return f, err == nil
	default:
		return 0, false
	}
}
