package avoinspector

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ============================================================
// Event Spec Types Tests
// ============================================================

func TestEventSpecResponse_Parsing(t *testing.T) {
	jsonData := `{
		"eventName": "Purchase",
		"rules": {
			"properties": [
				{
					"propertyName": "amount",
					"propertyType": "float",
					"nameRule": {"type": "exact", "value": "amount"},
					"typeRule": {"type": "exact", "value": "float"}
				}
			]
		},
		"passedEventIds": ["evt-1", "evt-2"],
		"failedEventIds": ["evt-3"]
	}`

	var resp EventSpecResponse
	err := json.Unmarshal([]byte(jsonData), &resp)
	if err != nil {
		t.Fatalf("failed to unmarshal EventSpecResponse: %v", err)
	}
	if resp.EventName != "Purchase" {
		t.Errorf("expected EventName 'Purchase', got '%s'", resp.EventName)
	}
	if len(resp.Rules.Properties) != 1 {
		t.Fatalf("expected 1 property rule, got %d", len(resp.Rules.Properties))
	}
	prop := resp.Rules.Properties[0]
	if prop.PropertyName != "amount" {
		t.Errorf("expected PropertyName 'amount', got '%s'", prop.PropertyName)
	}
	if prop.NameRule.Type != "exact" || prop.NameRule.Value != "amount" {
		t.Errorf("unexpected name rule: %+v", prop.NameRule)
	}
	if prop.TypeRule.Type != "exact" || prop.TypeRule.Value != "float" {
		t.Errorf("unexpected type rule: %+v", prop.TypeRule)
	}
	if len(resp.PassedEventIds) != 2 {
		t.Errorf("expected 2 passedEventIds, got %d", len(resp.PassedEventIds))
	}
	if len(resp.FailedEventIds) != 1 {
		t.Errorf("expected 1 failedEventIds, got %d", len(resp.FailedEventIds))
	}
}

func TestEventSpecResponse_NilResponse(t *testing.T) {
	jsonData := `null`
	var resp *EventSpecResponse
	err := json.Unmarshal([]byte(jsonData), &resp)
	if err != nil {
		t.Fatalf("failed to unmarshal nil response: %v", err)
	}
	if resp != nil {
		t.Errorf("expected nil response, got %+v", resp)
	}
}

// ============================================================
// Event Spec Cache Tests
// ============================================================

func TestEventSpecCache_SetAndGet(t *testing.T) {
	cache := newEventSpecCache()
	spec := &EventSpecResponse{EventName: "Test"}
	cache.set("key1", spec)

	got, ok := cache.get("key1")
	if !ok {
		t.Fatal("expected cache hit for key1")
	}
	if got.EventName != "Test" {
		t.Errorf("expected EventName 'Test', got '%s'", got.EventName)
	}
}

func TestEventSpecCache_NilValueCached(t *testing.T) {
	cache := newEventSpecCache()
	cache.set("key1", nil)

	got, ok := cache.get("key1")
	if !ok {
		t.Fatal("expected cache hit for nil value")
	}
	if got != nil {
		t.Errorf("expected nil value, got %+v", got)
	}
}

func TestEventSpecCache_MissReturnsNotOk(t *testing.T) {
	cache := newEventSpecCache()
	_, ok := cache.get("missing")
	if ok {
		t.Error("expected cache miss for non-existent key")
	}
}

func TestEventSpecCache_TTLEviction(t *testing.T) {
	cache := newEventSpecCache()
	spec := &EventSpecResponse{EventName: "Test"}
	cache.set("key1", spec)

	// Manually backdate the entry to simulate expiry
	cache.mu.Lock()
	if entry, exists := cache.entries["key1"]; exists {
		entry.createdAt = time.Now().Add(-61 * time.Second)
	}
	cache.mu.Unlock()

	// Trigger sweep by doing enough operations
	for i := 0; i < 50; i++ {
		cache.set(fmt.Sprintf("sweep-%d", i), spec)
	}

	_, ok := cache.get("key1")
	if ok {
		t.Error("expected TTL-expired entry to be evicted after sweep")
	}
}

func TestEventSpecCache_PerEntryEviction_50Accesses(t *testing.T) {
	cache := newEventSpecCache()
	spec := &EventSpecResponse{EventName: "Test"}
	cache.set("key1", spec)

	// Access 49 times — should still be there
	for i := 0; i < 49; i++ {
		_, ok := cache.get("key1")
		if !ok {
			t.Fatalf("expected cache hit on access %d", i+1)
		}
	}

	// 50th access should evict
	_, ok := cache.get("key1")
	if ok {
		t.Error("expected entry to be evicted after 50 accesses")
	}
}

func TestEventSpecCache_LRUEviction_MaxCapacity50(t *testing.T) {
	cache := newEventSpecCache()
	spec := &EventSpecResponse{EventName: "Test"}

	// Fill cache to capacity
	for i := 0; i < 50; i++ {
		cache.set(fmt.Sprintf("key-%d", i), spec)
	}

	// All 50 should be present
	for i := 0; i < 50; i++ {
		_, ok := cache.get(fmt.Sprintf("key-%d", i))
		if !ok {
			t.Fatalf("expected key-%d to be in cache", i)
		}
	}

	// Adding 51st should evict the least recently used
	cache.set("key-50", spec)

	// One of the first entries should be evicted
	totalPresent := 0
	for i := 0; i <= 50; i++ {
		if _, ok := cache.get(fmt.Sprintf("key-%d", i)); ok {
			totalPresent++
		}
	}
	if totalPresent > 50 {
		t.Errorf("expected at most 50 entries, found %d", totalPresent)
	}
}

func TestEventSpecCache_GlobalSweep_Every50Operations(t *testing.T) {
	cache := newEventSpecCache()
	spec := &EventSpecResponse{EventName: "Test"}

	// Insert an expired entry
	cache.set("expired", spec)
	cache.mu.Lock()
	if entry, exists := cache.entries["expired"]; exists {
		entry.createdAt = time.Now().Add(-61 * time.Second)
	}
	cache.mu.Unlock()

	// Do 49 operations — sweep should not have run yet
	for i := 0; i < 49; i++ {
		cache.set(fmt.Sprintf("op-%d", i), spec)
	}

	// Check expired entry still present (sweep not triggered yet at 49 ops since initial set)
	// Actually, we inserted "expired" + 49 = 50 ops total, sweep should trigger
	// Let's verify it's gone
	_, ok := cache.get("expired")
	if ok {
		t.Error("expected expired entry to be swept after 50 operations")
	}
}

func TestEventSpecCache_FlushOnBranchIdChange(t *testing.T) {
	cache := newEventSpecCache()
	spec := &EventSpecResponse{EventName: "Test"}
	cache.set("key1", spec)
	cache.set("key2", spec)

	cache.flush()

	_, ok1 := cache.get("key1")
	_, ok2 := cache.get("key2")
	if ok1 || ok2 {
		t.Error("expected all entries to be flushed")
	}
}

func TestEventSpecCache_ConcurrentAccess(t *testing.T) {
	cache := newEventSpecCache()
	spec := &EventSpecResponse{EventName: "Test"}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			key := fmt.Sprintf("key-%d", idx%10)
			cache.set(key, spec)
			cache.get(key)
		}(i)
	}
	wg.Wait()
	// Just verifying no race conditions / panics
}

// ============================================================
// Event Spec Fetcher Tests
// ============================================================

func TestEventSpecFetcher_SuccessfulFetch(t *testing.T) {
	specJSON := `{
		"eventName": "Purchase",
		"rules": {
			"properties": [
				{
					"propertyName": "amount",
					"propertyType": "float",
					"nameRule": {"type": "exact", "value": "amount"},
					"typeRule": {"type": "exact", "value": "float"}
				}
			]
		},
		"passedEventIds": [],
		"failedEventIds": []
	}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(specJSON))
	}))
	defer server.Close()

	fetcher := newEventSpecFetcher(server.URL)

	done := make(chan struct{})
	var result *EventSpecResponse
	var fetchErr error

	fetcher.fetchAsync("test-api-key", "stream-1", "Purchase", func(spec *EventSpecResponse, err error) {
		result = spec
		fetchErr = err
		close(done)
	})

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("fetch timed out")
	}

	if fetchErr != nil {
		t.Fatalf("unexpected error: %v", fetchErr)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.EventName != "Purchase" {
		t.Errorf("expected EventName 'Purchase', got '%s'", result.EventName)
	}
}

func TestEventSpecFetcher_InFlightDedup_SingleHTTPRequest(t *testing.T) {
	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		// Delay to ensure concurrent requests overlap
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"eventName":"Purchase","rules":{"properties":[]},"passedEventIds":[],"failedEventIds":[]}`))
	}))
	defer server.Close()

	fetcher := newEventSpecFetcher(server.URL)

	var wg sync.WaitGroup
	results := make([]*EventSpecResponse, 5)
	errors := make([]error, 5)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			done := make(chan struct{})
			fetcher.fetchAsync("api-key", "stream-1", "Purchase", func(spec *EventSpecResponse, err error) {
				results[idx] = spec
				errors[idx] = err
				close(done)
			})
			<-done
		}(i)
	}
	wg.Wait()

	count := atomic.LoadInt32(&requestCount)
	if count != 1 {
		t.Errorf("expected exactly 1 HTTP request, got %d", count)
	}

	for i, err := range errors {
		if err != nil {
			t.Errorf("callback %d got error: %v", i, err)
		}
	}
	for i, res := range results {
		if res == nil {
			t.Errorf("callback %d got nil result", i)
		}
	}
}

func TestEventSpecFetcher_InFlightFailure_AllCallbacksResolvedWithNil(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	fetcher := newEventSpecFetcher(server.URL)

	var wg sync.WaitGroup
	results := make([]*EventSpecResponse, 3)
	errs := make([]error, 3)

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			done := make(chan struct{})
			fetcher.fetchAsync("api-key", "stream-1", "Purchase", func(spec *EventSpecResponse, err error) {
				results[idx] = spec
				errs[idx] = err
				close(done)
			})
			<-done
		}(i)
	}
	wg.Wait()

	for i, res := range results {
		if res != nil {
			t.Errorf("callback %d expected nil result on failure, got %+v", i, res)
		}
	}
}

func TestEventSpecFetcher_DeferBodyClose(t *testing.T) {
	// We can't directly test defer, but we can verify no resource leaks
	// by making many requests and ensuring server handles them fine
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"eventName":"Test","rules":{"properties":[]},"passedEventIds":[],"failedEventIds":[]}`))
	}))
	defer server.Close()

	fetcher := newEventSpecFetcher(server.URL)
	for i := 0; i < 20; i++ {
		done := make(chan struct{})
		fetcher.fetchAsync("api-key", "stream-1", fmt.Sprintf("Event-%d", i), func(spec *EventSpecResponse, err error) {
			close(done)
		})
		<-done
	}
	// If body wasn't closed properly, we'd see resource exhaustion
}

func TestEventSpecFetcher_UsesGoroutine(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"eventName":"Test","rules":{"properties":[]},"passedEventIds":[],"failedEventIds":[]}`))
	}))
	defer server.Close()

	fetcher := newEventSpecFetcher(server.URL)

	// fetchAsync should return immediately (non-blocking)
	start := time.Now()
	done := make(chan struct{})
	fetcher.fetchAsync("api-key", "stream-1", "Test", func(spec *EventSpecResponse, err error) {
		close(done)
	})
	elapsed := time.Since(start)

	// Should return nearly instantly since it's async
	if elapsed > 20*time.Millisecond {
		t.Errorf("fetchAsync blocked for %v, expected non-blocking", elapsed)
	}

	// But the callback should still complete
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("callback never called")
	}
}

// ============================================================
// Event Validator Tests
// ============================================================

func TestEventValidator_ExactNameRule_Match(t *testing.T) {
	spec := &EventSpecResponse{
		EventName: "Purchase",
		Rules: EventSpecRules{
			Properties: []PropertyRule{
				{
					PropertyName: "amount",
					PropertyType: "float",
					NameRule:     MatchRule{Type: "exact", Value: "amount"},
					TypeRule:     MatchRule{Type: "exact", Value: "float"},
				},
			},
		},
	}

	properties := []Property{
		{PropertyName: "amount", PropertyType: "float"},
	}

	result := validateEventSpec(spec, properties)
	if len(result.Errors) != 0 {
		t.Errorf("expected no errors, got %v", result.Errors)
	}
}

func TestEventValidator_ExactNameRule_Mismatch(t *testing.T) {
	spec := &EventSpecResponse{
		EventName: "Purchase",
		Rules: EventSpecRules{
			Properties: []PropertyRule{
				{
					PropertyName: "amount",
					PropertyType: "float",
					NameRule:     MatchRule{Type: "exact", Value: "amount"},
					TypeRule:     MatchRule{Type: "exact", Value: "float"},
				},
			},
		},
	}

	properties := []Property{
		{PropertyName: "price", PropertyType: "float"},
	}

	result := validateEventSpec(spec, properties)
	if len(result.Errors) == 0 {
		t.Error("expected validation error for missing 'amount' property")
	}
}

func TestEventValidator_RegexNameRule_Match(t *testing.T) {
	spec := &EventSpecResponse{
		EventName: "Purchase",
		Rules: EventSpecRules{
			Properties: []PropertyRule{
				{
					PropertyName: "item_id",
					PropertyType: "string",
					NameRule:     MatchRule{Type: "regex", Value: "^item_.*$"},
					TypeRule:     MatchRule{Type: "exact", Value: "string"},
				},
			},
		},
	}

	properties := []Property{
		{PropertyName: "item_id", PropertyType: "string"},
	}

	result := validateEventSpec(spec, properties)
	if len(result.Errors) != 0 {
		t.Errorf("expected no errors for regex match, got %v", result.Errors)
	}
}

func TestEventValidator_RegexNameRule_NoMatch(t *testing.T) {
	spec := &EventSpecResponse{
		EventName: "Purchase",
		Rules: EventSpecRules{
			Properties: []PropertyRule{
				{
					PropertyName: "item_id",
					PropertyType: "string",
					NameRule:     MatchRule{Type: "regex", Value: "^item_.*$"},
					TypeRule:     MatchRule{Type: "exact", Value: "string"},
				},
			},
		},
	}

	properties := []Property{
		{PropertyName: "product_id", PropertyType: "string"},
	}

	result := validateEventSpec(spec, properties)
	if len(result.Errors) == 0 {
		t.Error("expected validation error for regex name mismatch")
	}
}

func TestEventValidator_TypeMismatch(t *testing.T) {
	spec := &EventSpecResponse{
		EventName: "Purchase",
		Rules: EventSpecRules{
			Properties: []PropertyRule{
				{
					PropertyName: "amount",
					PropertyType: "float",
					NameRule:     MatchRule{Type: "exact", Value: "amount"},
					TypeRule:     MatchRule{Type: "exact", Value: "float"},
				},
			},
		},
	}

	properties := []Property{
		{PropertyName: "amount", PropertyType: "string"},
	}

	result := validateEventSpec(spec, properties)
	if len(result.Errors) == 0 {
		t.Error("expected validation error for type mismatch")
	}
}

func TestEventValidator_RegexTypeRule(t *testing.T) {
	spec := &EventSpecResponse{
		EventName: "Purchase",
		Rules: EventSpecRules{
			Properties: []PropertyRule{
				{
					PropertyName: "amount",
					PropertyType: "number",
					NameRule:     MatchRule{Type: "exact", Value: "amount"},
					TypeRule:     MatchRule{Type: "regex", Value: "^(int|float)$"},
				},
			},
		},
	}

	properties := []Property{
		{PropertyName: "amount", PropertyType: "int"},
	}

	result := validateEventSpec(spec, properties)
	if len(result.Errors) != 0 {
		t.Errorf("expected no errors for regex type match, got %v", result.Errors)
	}
}

func TestEventValidator_NilSpec_NoErrors(t *testing.T) {
	properties := []Property{
		{PropertyName: "amount", PropertyType: "float"},
	}

	result := validateEventSpec(nil, properties)
	if len(result.Errors) != 0 {
		t.Errorf("expected no errors for nil spec, got %v", result.Errors)
	}
}

func TestEventValidator_EmptyProperties(t *testing.T) {
	spec := &EventSpecResponse{
		EventName: "Purchase",
		Rules: EventSpecRules{
			Properties: []PropertyRule{
				{
					PropertyName: "amount",
					PropertyType: "float",
					NameRule:     MatchRule{Type: "exact", Value: "amount"},
					TypeRule:     MatchRule{Type: "exact", Value: "float"},
				},
			},
		},
	}

	result := validateEventSpec(spec, nil)
	if len(result.Errors) == 0 {
		t.Error("expected validation error for missing required property")
	}
}

func TestEventValidator_PassedEventIds_BandwidthOptimization(t *testing.T) {
	spec := &EventSpecResponse{
		EventName:      "Purchase",
		Rules:          EventSpecRules{Properties: []PropertyRule{}},
		PassedEventIds: []string{"evt-1", "evt-2", "evt-3"},
		FailedEventIds: []string{"evt-4", "evt-5", "evt-6", "evt-7"},
	}

	result := validateEventSpec(spec, nil)

	// passedEventIds should be returned only when strictly smaller than failedEventIds
	// 3 passed < 4 failed → return passed
	if result.PassedEventIds == nil {
		t.Error("expected passedEventIds to be returned (smaller set)")
	}
	if len(result.PassedEventIds) != 3 {
		t.Errorf("expected 3 passedEventIds, got %d", len(result.PassedEventIds))
	}
}

func TestEventValidator_PassedEventIds_NotReturnedWhenLarger(t *testing.T) {
	spec := &EventSpecResponse{
		EventName:      "Purchase",
		Rules:          EventSpecRules{Properties: []PropertyRule{}},
		PassedEventIds: []string{"evt-1", "evt-2", "evt-3", "evt-4"},
		FailedEventIds: []string{"evt-5", "evt-6"},
	}

	result := validateEventSpec(spec, nil)

	// 4 passed > 2 failed → don't return passed (bandwidth optimization)
	if result.PassedEventIds != nil {
		t.Errorf("expected passedEventIds to be nil (larger set), got %v", result.PassedEventIds)
	}
}

func TestEventValidator_PassedEventIds_EqualSize_NotReturned(t *testing.T) {
	spec := &EventSpecResponse{
		EventName:      "Purchase",
		Rules:          EventSpecRules{Properties: []PropertyRule{}},
		PassedEventIds: []string{"evt-1", "evt-2"},
		FailedEventIds: []string{"evt-3", "evt-4"},
	}

	result := validateEventSpec(spec, nil)

	// Equal size → don't return (only strictly smaller)
	if result.PassedEventIds != nil {
		t.Errorf("expected passedEventIds to be nil (equal size), got %v", result.PassedEventIds)
	}
}

// ============================================================
// Integration: Validation Active in Dev/Staging Only
// ============================================================

func TestValidation_ActiveInDev(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"samplingRate":1.0}`))
	}))
	defer server.Close()

	inspector, err := NewAvoInspector("test-api-key", Dev, "1.0.0", "MyApp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	inspector.avoNetworkCallsHandler.trackingEndpoint = server.URL

	if !inspector.isValidationEnabled() {
		t.Error("expected validation to be enabled in Dev")
	}
}

func TestValidation_ActiveInStaging(t *testing.T) {
	inspector, err := NewAvoInspector("test-api-key", Staging, "1.0.0", "MyApp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !inspector.isValidationEnabled() {
		t.Error("expected validation to be enabled in Staging")
	}
}

func TestValidation_NotActiveInProd(t *testing.T) {
	inspector, err := NewAvoInspector("test-api-key", Prod, "1.0.0", "MyApp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if inspector.isValidationEnabled() {
		t.Error("expected validation to NOT be enabled in Prod")
	}
}

// ============================================================
// Integration: fetchAndValidateAsync flow
// ============================================================

func TestFetchAndValidateAsync_DevEnvironment(t *testing.T) {
	specJSON := `{
		"eventName": "Purchase",
		"rules": {
			"properties": [
				{
					"propertyName": "amount",
					"propertyType": "float",
					"nameRule": {"type": "exact", "value": "amount"},
					"typeRule": {"type": "exact", "value": "float"}
				}
			]
		},
		"passedEventIds": [],
		"failedEventIds": []
	}`

	specServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(specJSON))
	}))
	defer specServer.Close()

	trackingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"samplingRate":1.0}`))
	}))
	defer trackingServer.Close()

	inspector, err := NewAvoInspector("test-api-key", Dev, "1.0.0", "MyApp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	inspector.avoNetworkCallsHandler.trackingEndpoint = trackingServer.URL
	inspector.specFetcher = newEventSpecFetcher(specServer.URL)

	properties := map[string]interface{}{
		"amount": 42.5,
	}

	_, err = inspector.TrackSchemaFromEvent("Purchase", properties)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Give async validation time to complete
	time.Sleep(200 * time.Millisecond)
}

func TestFetchAndValidateAsync_ProdEnvironment_SkipsValidation(t *testing.T) {
	var specRequested int32
	specServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&specRequested, 1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"eventName":"Test","rules":{"properties":[]},"passedEventIds":[],"failedEventIds":[]}`))
	}))
	defer specServer.Close()

	trackingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"samplingRate":1.0}`))
	}))
	defer trackingServer.Close()

	inspector, err := NewAvoInspector("test-api-key", Prod, "1.0.0", "MyApp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	inspector.avoNetworkCallsHandler.trackingEndpoint = trackingServer.URL
	inspector.specFetcher = newEventSpecFetcher(specServer.URL)

	_, err = inspector.TrackSchemaFromEvent("Test", map[string]interface{}{"key": "val"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	count := atomic.LoadInt32(&specRequested)
	if count != 0 {
		t.Errorf("expected 0 spec requests in prod, got %d", count)
	}
}

func TestFetchAndValidateAsync_UsesCacheOnSecondCall(t *testing.T) {
	var requestCount int32
	specServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"eventName":"Purchase","rules":{"properties":[]},"passedEventIds":[],"failedEventIds":[]}`))
	}))
	defer specServer.Close()

	trackingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"samplingRate":1.0}`))
	}))
	defer trackingServer.Close()

	inspector, err := NewAvoInspector("test-api-key", Dev, "1.0.0", "MyApp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	inspector.avoNetworkCallsHandler.trackingEndpoint = trackingServer.URL
	inspector.specFetcher = newEventSpecFetcher(specServer.URL)

	// First call — triggers fetch
	inspector.TrackSchemaFromEvent("Purchase", map[string]interface{}{"amount": 42.5})
	time.Sleep(300 * time.Millisecond)

	// Second call — should use cache
	inspector.TrackSchemaFromEvent("Purchase", map[string]interface{}{"amount": 42.5})
	time.Sleep(200 * time.Millisecond)

	count := atomic.LoadInt32(&requestCount)
	if count != 1 {
		t.Errorf("expected exactly 1 HTTP request (second should use cache), got %d", count)
	}
}

func TestEventSpecCache_KeyFormat(t *testing.T) {
	// Verify cache key format is apiKey:streamId:eventName
	key := specCacheKey("my-api-key", "stream-1", "Purchase")
	expected := "my-api-key:stream-1:Purchase"
	if key != expected {
		t.Errorf("expected cache key '%s', got '%s'", expected, key)
	}
}

func TestEventSpecCache_KeyFormat_EmptyStreamId(t *testing.T) {
	key := specCacheKey("my-api-key", "", "Purchase")
	expected := "my-api-key::Purchase"
	if key != expected {
		t.Errorf("expected cache key '%s', got '%s'", expected, key)
	}
}
