package main

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/avohq/go-avo-inspector/v2/internal/testhooks"
)

// closedPort is where every request from this package's tests goes: nothing listens there, so a
// send fails at once and never reaches the real API.
const closedPort = "http://127.0.0.1:1"

var observed struct {
	sync.Mutex
	urls []string
}

// TestMain keeps every test off the real API before any test runs. Non-prod instances send to
// AVO_INSPECTOR_MOCK_ENDPOINT, which points at the closed port unless a test sets its own; prod
// instances ignore that variable by design, so the production endpoint points there too. Every
// request's URL is recorded so a test can check where it went.
func TestMain(m *testing.M) {
	os.Setenv("AVO_INSPECTOR_MOCK_ENDPOINT", closedPort)
	testhooks.SetProductionEndpoint(closedPort)
	testhooks.ObserveRequest = func(url string) {
		observed.Lock()
		observed.urls = append(observed.urls, url)
		observed.Unlock()
	}
	os.Exit(m.Run())
}

// takeObserved returns the URLs requested since the last call and forgets them.
func takeObserved() []string {
	observed.Lock()
	defer observed.Unlock()
	urls := observed.urls
	observed.urls = nil
	return urls
}

// A dev track, sent to the mock endpoint, and a prod track, sent to the production endpoint, both go
// to the closed port when the test sets no endpoint of its own.
func TestHarness_TestsSendOnlyToTheClosedPort(t *testing.T) {
	takeObserved()
	for _, envelope := range []string{
		`{"suite":"wire-protocol","fixture_id":"dev","operation":"trackSchemaFromEvent",` +
			`"constructor":{"apiKey":"k","env":"dev","version":"1"},"input":{"eventName":"e","eventProperties":{}}}`,
		`{"suite":"batching","fixture_id":"prod","operation":"sequence","constructor":{"apiKey":"k","env":"prod","version":"1"},` +
			`"steps":[{"action":"track","eventName":"e","eventProperties":{}},{"action":"flush"}]}`,
	} {
		var stdout bytes.Buffer
		if code := run(strings.NewReader(envelope+"\n"), &stdout); code != 0 {
			t.Fatalf("exit code %d: %s", code, stdout.String())
		}
	}
	if got, want := takeObserved(), []string{closedPort, closedPort}; !reflect.DeepEqual(got, want) {
		t.Errorf("requests went to %q, want %q (one dev, one prod)", got, want)
	}
}
