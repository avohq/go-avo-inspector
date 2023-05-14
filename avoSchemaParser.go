package avoinspector

import (
	"strconv"
)

// Property represents a schema of a single event property
type Property struct {
	PropertyName string     `json:"propertyName"`
	PropertyType string     `json:"propertyType"`
	Children     []Property `json:"children,omitempty"`
}

// extractSchema extracts the schema of event properties
func extractSchema(eventProperties map[string]interface{}) []Property {
	var result []Property /* struct {
		PropertyName string     `json:"propertyName"`
		PropertyType string     `json:"propertyType"`
		Children     []Property `json:"children,omitempty"`
	} */

	for key, value := range eventProperties {
		result = append(result, struct {
			PropertyName string     `json:"propertyName"`
			PropertyType string     `json:"propertyType"`
			Children     []Property `json:"children,omitempty"`
		}{
			PropertyName: key,
			PropertyType: getType(value),
			Children:     getChildren(value),
		})
	}

	return result
}

// getType returns the type of a value
func getType(value interface{}) string {
	switch value.(type) {
	case string:
		return "string"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "int"
	case float32, float64:
		return "float"
	case bool:
		return "bool"
	case nil:
		return "null"
	case []interface{}:
		return "list"
	case map[string]interface{}:
		return "object"
	default:
		return "unknown"
	}
}

// getChildren returns the children of a value if it's an object or a list
func getChildren(value interface{}) []Property {
	switch v := value.(type) {
	case []interface{}:
		var children []Property
		for index, element := range v {
			children = append(children, Property{
				PropertyName: strconv.Itoa(index),
				PropertyType: getType(element),
				Children:     getChildren(element),
			})
		}
		return children
	case map[string]interface{}:
		var children []Property
		for key, element := range v {
			children = append(children, Property{
				PropertyName: key,
				PropertyType: getType(element),
				Children:     getChildren(element),
			})
		}
		return children
	default:
		return nil
	}
}
