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

	constructor, ok := get(envelope, "constructor")
	constructorMap, isMap := constructor.(avoinspector.OrderedMap)
	if !ok || !isMap {
		writeEnvelope(stdout, &fixtureID, false, nil, "resolve", "missing constructor object")
		return 2
	}
	options, err := constructorOptions(constructorMap)
	if err != nil {
		writeEnvelope(stdout, &fixtureID, false, nil, "resolve", err.Error())
		return 2
	}
	inspector, err := avoinspector.NewAvoInspectorWithOptions(options)
	if err != nil {
		writeEnvelope(stdout, &fixtureID, false, nil, "resolve", "Constructor threw: "+err.Error())
		return 1
	}

	if err := applyPreconditions(inspector, envelope); err != nil {
		writeEnvelope(stdout, &fixtureID, false, nil, "resolve", err.Error())
		return 2
	}

	operation, _ := getString(envelope, "operation")
	if suite, _ := getString(envelope, "suite"); operation == "" && suite == "schema-extraction" {
		operation = "extractSchema"
	}

	// input is required except for sequence; an explicit null is present (fixture-8).
	input, hasInput := get(envelope, "input")
	if !hasInput && (operation == "extractSchema" || operation == "trackSchemaFromEvent") {
		writeEnvelope(stdout, &fixtureID, false, nil, "resolve", "missing input")
		return 2
	}

	var actual interface{}
	outcome := "resolve"
	switch operation {
	case "extractSchema":
		properties, _ := input.(avoinspector.OrderedMap)
		actual = inspector.ExtractOrderedSchema(properties)
	case "trackSchemaFromEvent":
		inputMap, _ := input.(avoinspector.OrderedMap)
		actual, outcome = track(inspector, inputMap)
	case "sequence":
		actual, err = runSequence(inspector, envelope)
		if err != nil {
			writeEnvelope(stdout, &fixtureID, false, nil, "resolve", err.Error())
			return 2
		}
	default:
		writeEnvelope(stdout, &fixtureID, false, nil, "resolve", "unsupported operation: "+operation)
		return 2
	}

	if err := writeEnvelope(stdout, &fixtureID, true, actual, outcome, ""); err != nil {
		fmt.Fprintln(os.Stderr, "output write failed:", err)
		return 1
	}
	return 0
}

// constructorOptions maps the constructor block to Options. A batchSize or maxQueueSize that is
// not an integer is a configError.
func constructorOptions(constructor avoinspector.OrderedMap) (avoinspector.Options, error) {
	options := avoinspector.Options{}
	options.ApiKey, _ = getString(constructor, "apiKey")
	env, _ := getString(constructor, "env")
	options.Env = avoinspector.AvoInspectorEnv(env)
	options.AppVersion, _ = getString(constructor, "version")
	options.AppName, _ = getString(constructor, "appName")
	var err error
	if options.BatchSize, err = getInt(constructor, "batchSize"); err != nil {
		return options, err
	}
	if value, ok := getNumber(constructor, "batchFlushSeconds"); ok {
		options.BatchFlushSeconds = value
	}
	if options.MaxQueueSize, err = getInt(constructor, "maxQueueSize"); err != nil {
		return options, err
	}
	if value, ok := get(constructor, "disableBatchTimer"); ok {
		options.DisableBatchTimer, _ = value.(bool)
	}
	return options, nil
}

func applyPreconditions(inspector *avoinspector.AvoInspector, envelope avoinspector.OrderedMap) error {
	precondition, ok := get(envelope, "precondition")
	if !ok || precondition == nil {
		return nil
	}
	fields, ok := precondition.(avoinspector.OrderedMap)
	if !ok {
		return configError{"precondition must be an object"}
	}
	for _, field := range fields {
		switch field.Key {
		case "samplingRate":
			rate, ok := toFloat(field.Value)
			if !ok {
				return configError{"precondition.samplingRate must be a number"}
			}
			testhooks.SetSamplingRate(inspector, rate)
		default:
			return configError{"unsupported precondition field: " + field.Key}
		}
	}
	return nil
}

// track calls the SDK with a fixture's eventName / eventProperties / streamId / options. The option
// values are passed verbatim; normalizing them is the SDK's job.
func track(inspector *avoinspector.AvoInspector, input avoinspector.OrderedMap) (interface{}, string) {
	eventName, _ := getString(input, "eventName")
	value, _ := get(input, "eventProperties")
	properties, _ := value.(avoinspector.OrderedMap)
	options := avoinspector.TrackOptions{}
	options.StreamId, _ = getString(input, "streamId")
	if value, ok := get(input, "options"); ok {
		if gateway, ok := value.(avoinspector.OrderedMap); ok {
			options.OutputReference, _ = getString(gateway, "outputReference")
			options.OriginHint, _ = getString(gateway, "originHint")
			options.OriginAppVersion, _ = getString(gateway, "originAppVersion")
		}
	}
	schema, err := inspector.TrackOrderedSchemaFromEvent(eventName, properties, options)
	if err != nil {
		return err.Error(), "reject"
	}
	return schema, "resolve"
}

func runSequence(inspector *avoinspector.AvoInspector, envelope avoinspector.OrderedMap) ([]stepRecord, error) {
	value, _ := get(envelope, "steps")
	steps, ok := value.([]interface{})
	if !ok {
		return nil, configError{"sequence operation requires a steps array"}
	}
	records := make([]stepRecord, 0, len(steps))
	for _, rawStep := range steps {
		step, ok := rawStep.(avoinspector.OrderedMap)
		if !ok {
			return nil, configError{"sequence step is not an object"}
		}
		action, _ := getString(step, "action")
		switch action {
		case "track":
			actual, outcome := track(inspector, step)
			records = append(records, stepRecord{"track", outcome, actual})
		case "trackN":
			count, ok := getNumber(step, "count")
			if !ok || count < 1 || count != float64(int(count)) {
				return nil, configError{"trackN requires an integer count >= 1"}
			}
			prefix, _ := getString(step, "eventNamePrefix")
			streamID, _ := getString(step, "streamId")
			var wg sync.WaitGroup
			for i := 0; i < int(count); i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					_, _ = inspector.TrackOrderedSchemaFromEvent(prefix+strconv.Itoa(i), avoinspector.OrderedMap{}, avoinspector.TrackOptions{StreamId: streamID})
				}(i)
			}
			wg.Wait()
			records = append(records, stepRecord{"trackN", "resolve", int(count)})
		case "flush":
			timeout := avoinspector.DefaultFlushTimeout
			if value, _ := get(step, "timeoutMs"); value != nil {
				ms, ok := toFloat(value)
				if !ok || !(ms >= 0 && ms <= maxTimeoutMs) {
					return nil, configError{"flush timeoutMs must be a number from 0 to " + strconv.FormatInt(int64(maxTimeoutMs), 10)}
				}
				timeout = time.Duration(ms * float64(time.Millisecond))
			}
			_ = inspector.Flush(timeout)
			records = append(records, stepRecord{"flush", "resolve", nil})
		case "destroy":
			inspector.Destroy()
			records = append(records, stepRecord{"destroy", "resolve", nil})
		default:
			return nil, configError{"unsupported sequence action: " + action}
		}
	}
	return records, nil
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

// getInt returns an integer field, or 0 when it is absent or null. Any other value that is not a
// whole number within int64 range is a configError.
func getInt(object avoinspector.OrderedMap, key string) (int, error) {
	value, _ := get(object, key)
	if value == nil {
		return 0, nil
	}
	number, ok := toFloat(value)
	if !ok || number != math.Trunc(number) || number < math.MinInt64 || number >= math.MaxInt64 {
		return 0, configError{key + " must be an integer"}
	}
	return int(number), nil
}

func getNumber(object avoinspector.OrderedMap, key string) (float64, bool) {
	value, _ := get(object, key)
	return toFloat(value)
}

func toFloat(value interface{}) (float64, bool) {
	switch v := value.(type) {
	case int64:
		return float64(v), true
	case float64:
		return v, true
	default:
		return 0, false
	}
}
