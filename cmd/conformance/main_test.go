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
