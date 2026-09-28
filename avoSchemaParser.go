package avoinspector

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
)

// maxSchemaDepth bounds the recursion of the schema parser (SPEC.md §9.3.2). A complex value found
// deeper than this is reported as an "object" with empty children instead of being descended into.
const maxSchemaDepth = 10

// Property represents a schema of a single event property (SPEC.md §7.3.4).
//
// Children and ListChildren are disjoint: an "object" property carries its nested entries in
// Children, and a list property ("list(string)", "list(int)", ...) carries its element schemas in
// ListChildren. Both are serialized as the single "children" key of the wire format, which is
// present for objects and lists and absent for every scalar type.
type Property struct {
	PropertyName string     `json:"propertyName"`
	PropertyType string     `json:"propertyType"`
	Children     []Property `json:"children,omitempty"`

	// ListChildren holds the deduplicated element schemas of a list property. Each element is a
	// type string (e.g. "string"), a []Property for an element that is an object, or a
	// []interface{} of the same kinds for an element that is itself a list.
	ListChildren []interface{} `json:"-"`
}

// MarshalJSON writes the wire shape of SPEC.md §7.3.4: "children" is present (possibly empty) for
// "object" and list types and absent for scalar types.
func (p Property) MarshalJSON() ([]byte, error) {
	type scalar struct {
		PropertyName string `json:"propertyName"`
		PropertyType string `json:"propertyType"`
	}
	type withChildren struct {
		PropertyName string      `json:"propertyName"`
		PropertyType string      `json:"propertyType"`
		Children     interface{} `json:"children"`
	}
	switch {
	case p.PropertyType == "object":
		children := p.Children
		if children == nil {
			children = []Property{}
		}
		return json.Marshal(withChildren{p.PropertyName, p.PropertyType, children})
	case strings.HasPrefix(p.PropertyType, "list("):
		children := p.ListChildren
		if children == nil {
			children = []interface{}{}
		}
		return json.Marshal(withChildren{p.PropertyName, p.PropertyType, children})
	default:
		return json.Marshal(scalar{p.PropertyName, p.PropertyType})
	}
}

// KeyValue is one entry of an OrderedMap.
type KeyValue struct {
	Key   string
	Value interface{}
}

// OrderedMap is an event-property object whose entries keep the order they were given in. Go maps
// have no order, so a schema extracted from a map[string]interface{} lists its properties sorted by
// key; use an OrderedMap (at the top level or as any nested value) to control the order instead.
// Keys are expected to be unique.
type OrderedMap []KeyValue

// extractSchema extracts the schema of event properties (SPEC.md §9). It accepts nil, a
// map[string]interface{}, an OrderedMap, or any other string-keyed map; anything else yields an
// empty schema. It has no recover of its own: the safe boundary is AvoInspector.ExtractSchema.
func extractSchema(eventProperties interface{}) []Property {
	entries, ok := objectEntries(eventProperties)
	if !ok {
		return []Property{}
	}
	return mapObject(entries, 0)
}

// mapping is the SPEC.md §9.2 mapping function for a value found inside a list: an object maps to
// its []Property, a list to its deduplicated element schemas, and a scalar to its type string.
func mapping(value interface{}, depth int) interface{} {
	if entries, ok := objectEntries(value); ok {
		return mapObject(entries, depth)
	}
	if elements, ok := listElements(value); ok {
		return mapList(elements, depth)
	}
	return getBasicPropType(value)
}

func mapObject(entries []KeyValue, depth int) []Property {
	result := make([]Property, 0, len(entries))
	for _, entry := range entries {
		property := Property{PropertyName: entry.Key, PropertyType: getPropValueType(entry.Value)}
		if nested, ok := objectEntries(entry.Value); ok {
			if depth >= maxSchemaDepth {
				property.Children = []Property{}
			} else {
				property.Children = mapObject(nested, depth+1)
			}
		} else if elements, ok := listElements(entry.Value); ok {
			if depth >= maxSchemaDepth {
				property.PropertyType = "object"
				property.Children = []Property{}
			} else {
				property.ListChildren = mapList(elements, depth+1)
			}
		}
		result = append(result, property)
	}
	return result
}

func mapList(elements []interface{}, depth int) []interface{} {
	mapped := make([]interface{}, 0, len(elements))
	for _, element := range elements {
		if depth >= maxSchemaDepth && isComplex(element) {
			mapped = append(mapped, "object")
		} else {
			mapped = append(mapped, mapping(element, depth+1))
		}
	}
	return removeDuplicates(mapped)
}

// getPropValueType is the SPEC.md §9.2 getPropValueType: a list is typed by its first element, and
// an empty list (or one whose first element is null) defaults to "list(string)".
func getPropValueType(value interface{}) string {
	elements, ok := listElements(value)
	if !ok {
		return getBasicPropType(value)
	}
	if len(elements) == 0 || isNil(elements[0]) {
		return "list(string)"
	}
	elementType := getBasicPropType(elements[0])
	if elementType == "unknown" {
		// "list(unknown)" is not a valid propertyType (SPEC.md §7.3.4).
		return "list(object)"
	}
	return "list(" + elementType + ")"
}

// getBasicPropType classifies a single value by its Go type (SPEC.md §9.2, §9.3). Any floating
// type is "float", including a whole-valued 0.0 (§9.3.1). A nested object or list is "object".
func getBasicPropType(value interface{}) string {
	if isNil(value) {
		return "null"
	}
	if number, ok := value.(json.Number); ok {
		if strings.ContainsAny(string(number), ".eE") {
			return "float"
		}
		return "int"
	}
	if _, ok := objectEntries(value); ok {
		return "object"
	}
	if _, ok := listElements(value); ok {
		return "object"
	}
	v := reflect.Indirect(reflect.ValueOf(value))
	switch v.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return "int"
	case reflect.Float32, reflect.Float64:
		return "float"
	default:
		return "unknown"
	}
}

func isComplex(value interface{}) bool {
	if _, ok := objectEntries(value); ok {
		return true
	}
	_, ok := listElements(value)
	return ok
}

// isNil reports whether value is nil or a nil pointer, map, slice or interface.
func isNil(value interface{}) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Slice, reflect.Interface:
		return v.IsNil()
	default:
		return false
	}
}

// objectEntries returns the entries of an object value: an OrderedMap keeps its order, any other
// string-keyed map is sorted by key. Nil values are not objects.
func objectEntries(value interface{}) ([]KeyValue, bool) {
	switch v := value.(type) {
	case nil:
		return nil, false
	case OrderedMap:
		if v == nil {
			return nil, false
		}
		return v, true
	case map[string]interface{}:
		if v == nil {
			return nil, false
		}
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		entries := make([]KeyValue, 0, len(keys))
		for _, key := range keys {
			entries = append(entries, KeyValue{Key: key, Value: v[key]})
		}
		return entries, true
	}
	rv := reflect.Indirect(reflect.ValueOf(value))
	if rv.Kind() != reflect.Map || rv.IsNil() || rv.Type().Key().Kind() != reflect.String {
		return nil, false
	}
	keys := rv.MapKeys()
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	entries := make([]KeyValue, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, KeyValue{Key: key.String(), Value: rv.MapIndex(key).Interface()})
	}
	return entries, true
}

// listElements returns the elements of a list value (any slice or array other than an
// OrderedMap). Nil slices are not lists.
func listElements(value interface{}) ([]interface{}, bool) {
	switch v := value.(type) {
	case nil, OrderedMap:
		return nil, false
	case []interface{}:
		if v == nil {
			return nil, false
		}
		return v, true
	}
	rv := reflect.Indirect(reflect.ValueOf(value))
	switch rv.Kind() {
	case reflect.Slice:
		if rv.IsNil() {
			return nil, false
		}
	case reflect.Array:
	default:
		return nil, false
	}
	elements := make([]interface{}, rv.Len())
	for i := range elements {
		elements[i] = rv.Index(i).Interface()
	}
	return elements, true
}

// removeDuplicates keeps the first occurrence of each type string. An element that is an object
// or a list is never a duplicate: each keeps its own entry, as in the reference parser, which
// compares those by identity (SPEC.md §9.3.3).
func removeDuplicates(items []interface{}) []interface{} {
	result := make([]interface{}, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		if typeName, ok := item.(string); ok {
			if seen[typeName] {
				continue
			}
			seen[typeName] = true
		}
		result = append(result, item)
	}
	return result
}
