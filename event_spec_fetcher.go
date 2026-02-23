package avoinspector

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	fetchSocketTimeout = 5 * time.Second
	fetchWallTimeout   = 10 * time.Second
)

// fetchCallback is the signature for async fetch completion.
type fetchCallback func(spec *EventSpecResponse, err error)

// inFlightEntry tracks pending callbacks for a single in-flight request.
type inFlightEntry struct {
	callbacks []fetchCallback
}

// eventSpecFetcher performs HTTP GET requests to retrieve event specs,
// with goroutine-based async execution and sync.Map for in-flight dedup.
type eventSpecFetcher struct {
	baseURL   string
	client    *http.Client
	inFlight  sync.Map // key -> *inFlightEntry
	inFlightMu sync.Mutex // protects the check-and-set on inFlight
}

// newEventSpecFetcher creates a new fetcher with connection reuse via custom Transport.
func newEventSpecFetcher(baseURL string) *eventSpecFetcher {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: fetchSocketTimeout,
		}).DialContext,
		MaxIdleConns:        10,
		IdleConnTimeout:     90 * time.Second,
		MaxIdleConnsPerHost: 5,
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   fetchWallTimeout,
	}

	return &eventSpecFetcher{
		baseURL: baseURL,
		client:  client,
	}
}

// fetchAsync fetches the event spec asynchronously using a goroutine.
// Concurrent requests for the same key are de-duplicated: only one HTTP
// request is made, and all queued callbacks are resolved with the result.
// On failure, all queued callbacks receive nil spec.
func (f *eventSpecFetcher) fetchAsync(apiKey, streamId, eventName string, cb fetchCallback) {
	key := specCacheKey(apiKey, streamId, eventName)

	f.inFlightMu.Lock()
	if val, loaded := f.inFlight.Load(key); loaded {
		// Already in-flight — append callback
		entry := val.(*inFlightEntry)
		entry.callbacks = append(entry.callbacks, cb)
		f.inFlightMu.Unlock()
		return
	}

	// New request — register in-flight entry with this callback
	entry := &inFlightEntry{
		callbacks: []fetchCallback{cb},
	}
	f.inFlight.Store(key, entry)
	f.inFlightMu.Unlock()

	// Launch goroutine for async fetch
	go func() {
		spec, err := f.doFetch(apiKey, streamId, eventName)

		// Collect all queued callbacks under lock, then resolve outside lock
		f.inFlightMu.Lock()
		val, _ := f.inFlight.LoadAndDelete(key)
		var callbacks []fetchCallback
		if val != nil {
			e := val.(*inFlightEntry)
			callbacks = make([]fetchCallback, len(e.callbacks))
			copy(callbacks, e.callbacks)
		}
		f.inFlightMu.Unlock()

		for _, callback := range callbacks {
			if err != nil {
				callback(nil, err)
			} else {
				callback(spec, nil)
			}
		}
	}()
}

// doFetch performs the actual HTTP GET request.
func (f *eventSpecFetcher) doFetch(apiKey, streamId, eventName string) (*EventSpecResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fetchWallTimeout)
	defer cancel()

	url := fmt.Sprintf("%s/api/event-spec?apiKey=%s&streamId=%s&eventName=%s",
		f.baseURL, apiKey, streamId, eventName)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create spec request: %v", err)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("spec request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("spec request returned status %d", resp.StatusCode)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read spec response body: %v", err)
	}

	var spec EventSpecResponse
	if err := json.Unmarshal(body, &spec); err != nil {
		return nil, fmt.Errorf("failed to parse spec response: %v", err)
	}

	return &spec, nil
}
