package avoinspector

import (
	"fmt"
	"regexp"
)

// validateEventSpec validates event properties against the spec rules.
// Returns a ValidationResult with any errors and optionally passedEventIds
// (only when strictly smaller than failedEventIds for bandwidth optimization).
func validateEventSpec(spec *EventSpecResponse, properties []Property) ValidationResult {
	result := ValidationResult{}

	if spec == nil {
		return result
	}

	// Bandwidth optimization: return passedEventIds only when strictly smaller
	if len(spec.PassedEventIds) > 0 && len(spec.PassedEventIds) < len(spec.FailedEventIds) {
		result.PassedEventIds = spec.PassedEventIds
	}

	// Validate each rule against provided properties
	for _, rule := range spec.Rules.Properties {
		matched := false
		for _, prop := range properties {
			if matchesRule(prop.PropertyName, rule.NameRule) {
				matched = true
				// Check type
				if !matchesRule(prop.PropertyType, rule.TypeRule) {
					result.Errors = append(result.Errors,
						fmt.Sprintf("property '%s': expected type matching %s '%s', got '%s'",
							prop.PropertyName, rule.TypeRule.Type, rule.TypeRule.Value, prop.PropertyType))
				}
				break
			}
		}
		if !matched {
			result.Errors = append(result.Errors,
				fmt.Sprintf("missing property matching %s '%s'",
					rule.NameRule.Type, rule.NameRule.Value))
		}
	}

	return result
}

// matchesRule checks if a value matches a MatchRule (exact or regex).
// Uses Go stdlib regexp which is RE2-based (guaranteed linear-time).
func matchesRule(value string, rule MatchRule) bool {
	switch rule.Type {
	case "exact":
		return value == rule.Value
	case "regex":
		re, err := regexp.Compile(rule.Value)
		if err != nil {
			// Invalid regex — no match
			return false
		}
		return re.MatchString(value)
	default:
		return false
	}
}
