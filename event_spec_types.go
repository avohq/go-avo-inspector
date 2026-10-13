package avoinspector

// EventSpecResponse represents the server response for an event spec query.
type EventSpecResponse struct {
	EventName      string         `json:"eventName"`
	Rules          EventSpecRules `json:"rules"`
	PassedEventIds []string       `json:"passedEventIds"`
	FailedEventIds []string       `json:"failedEventIds"`
}

// EventSpecRules contains the validation rules for an event.
type EventSpecRules struct {
	Properties []PropertyRule `json:"properties"`
}

// PropertyRule defines the expected name and type for a property.
type PropertyRule struct {
	PropertyName string    `json:"propertyName"`
	PropertyType string    `json:"propertyType"`
	NameRule     MatchRule `json:"nameRule"`
	TypeRule     MatchRule `json:"typeRule"`
}

// MatchRule defines a matching strategy: "exact" for literal comparison,
// "regex" for Go stdlib regexp matching.
type MatchRule struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// ValidationResult holds the outcome of validating event properties against a spec.
type ValidationResult struct {
	Errors         []string `json:"errors,omitempty"`
	PassedEventIds []string `json:"passedEventIds,omitempty"`
}
