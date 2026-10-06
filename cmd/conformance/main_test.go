package main

import (
	"bytes"
	"errors"
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

// input is required for every operation except sequence, and a missing required field is a
// configuration error (runner contract, envelope fields and "Exit codes").
func TestHarness_ExitsTwoWhenInputIsMissing(t *testing.T) {
	constructor := `"constructor":{"apiKey":"k","env":"dev","version":"1"}`
	for name, input := range map[string]string{
		"schema-extraction":    `{"suite":"schema-extraction","fixture_id":"m",` + constructor + `}`,
		"trackSchemaFromEvent": `{"suite":"s","fixture_id":"m","operation":"trackSchemaFromEvent",` + constructor + `}`,
	} {
		var stdout bytes.Buffer
		if code := run(strings.NewReader(input+"\n"), &stdout); code != 2 {
			t.Errorf("%s: exit code %d, want 2: %s", name, code, stdout.String())
		}
	}
}

// extractSchema passes an explicit null input through (runner contract, fixture-8).
func TestHarness_AcceptsNullInputForExtractSchema(t *testing.T) {
	input := `{"suite":"schema-extraction","fixture_id":"n","constructor":{"apiKey":"k","env":"dev","version":"1"},"input":null}` + "\n"
	var stdout bytes.Buffer
	if code := run(strings.NewReader(input), &stdout); code != 0 {
		t.Fatalf("exit code %d: %s", code, stdout.String())
	}
}

// A flush timeoutMs that is not a non-negative number within time.Duration's range is a
// configuration error, not a silently different timeout.
func TestHarness_ValidatesFlushTimeoutMs(t *testing.T) {
	for timeoutMs, want := range map[string]int{
		`-1`: 2, `1e13`: 2, `1e30`: 2, `99999999999999999999`: 2, `"5"`: 2,
		`0`: 0, `5`: 0, `2.5`: 0, `null`: 0,
	} {
		input := `{"suite":"batching","fixture_id":"t","operation":"sequence",` +
			`"constructor":{"apiKey":"k","env":"dev","version":"1"},` +
			`"steps":[{"action":"flush","timeoutMs":` + timeoutMs + `}]}` + "\n"
		var stdout bytes.Buffer
		if code := run(strings.NewReader(input), &stdout); code != want {
			t.Errorf("timeoutMs %s: exit code %d, want %d: %s", timeoutMs, code, want, stdout.String())
		}
	}
}

// batchSize and maxQueueSize are integers (runner contract, constructor fields): a fractional,
// out-of-range or non-numeric value is a configuration error, not a silently truncated option.
// An integer below 1 still reaches the SDK, which falls back to the default with a warning.
func TestHarness_ValidatesIntegerConstructorOptions(t *testing.T) {
	for _, key := range []string{"batchSize", "maxQueueSize"} {
		for value, want := range map[string]int{
			`2.5`: 2, `1e30`: 2, `99999999999999999999`: 2, `"3"`: 2,
			`3`: 0, `3.0`: 0, `-1`: 0, `0`: 0, `null`: 0,
		} {
			input := `{"suite":"schema-extraction","fixture_id":"c","constructor":{"apiKey":"k","env":"dev","version":"1","` +
				key + `":` + value + `},"input":{"a":1}}` + "\n"
			var stdout bytes.Buffer
			if code := run(strings.NewReader(input), &stdout); code != want {
				t.Errorf("%s %s: exit code %d, want %d: %s", key, value, code, want, stdout.String())
			}
		}
	}
}

// batchFlushSeconds is a number and disableBatchTimer a boolean (runner contract, constructor
// fields): any other present, non-null value is a configuration error, not a silent default.
func TestHarness_ValidatesConstructorOptionTypes(t *testing.T) {
	for _, tc := range []struct {
		key, value string
		want       int
	}{
		{"batchFlushSeconds", `"2"`, 2}, {"batchFlushSeconds", `true`, 2}, {"batchFlushSeconds", `{}`, 2},
		{"batchFlushSeconds", `2`, 0}, {"batchFlushSeconds", `2.5`, 0}, {"batchFlushSeconds", `0`, 0},
		{"batchFlushSeconds", `-1`, 0}, {"batchFlushSeconds", `99999999999999999999`, 0}, {"batchFlushSeconds", `null`, 0},
		{"disableBatchTimer", `"true"`, 2}, {"disableBatchTimer", `1`, 2}, {"disableBatchTimer", `[]`, 2},
		{"disableBatchTimer", `true`, 0}, {"disableBatchTimer", `false`, 0}, {"disableBatchTimer", `null`, 0},
	} {
		input := `{"suite":"schema-extraction","fixture_id":"c","constructor":{"apiKey":"k","env":"dev","version":"1","` +
			tc.key + `":` + tc.value + `},"input":{"a":1}}` + "\n"
		var stdout bytes.Buffer
		if code := run(strings.NewReader(input), &stdout); code != tc.want {
			t.Errorf("%s %s: exit code %d, want %d: %s", tc.key, tc.value, code, tc.want, stdout.String())
		}
	}
}
