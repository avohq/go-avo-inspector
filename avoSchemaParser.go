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
// Keys are expected to be unique. A pointer to an OrderedMap, a named type based on it, and a bare
// []KeyValue are read the same way.
type OrderedMap []KeyValue

// extractSchema extracts the schema of event properties (SPEC.md §9). It accepts nil, a
// map[string]interface{}, an OrderedMap, or any other string-keyed map; anything else yields an
// empty schema. It has no recover of its own: the safe boundary is AvoInspector.ExtractSchema.
func extractSchema(eventProperties interface{}) []Property {
	if classify(eventProperties) != kindObject {
		return []Property{}
	}
	return (&schemaParser{}).enterObject(eventProperties, 0)
}

// schemaParser holds the identities of the maps and slices on the path from the root to the value
// being mapped. A value that is its own ancestor (a cycle) is cut like a value past the depth cap,
// so a map holding itself under several keys cannot expand exponentially.
type schemaParser struct {
	ancestors []nodeIdentity
}

// nodeIdentity identifies a map or slice value. A slice is identified by its backing array, its
// length and its type, so a sub-slice that shares its parent's array is a different value.
type nodeIdentity struct {
	pointer uintptr
	length  int
	typ     reflect.Type
}

// valueKind is the schema category of a value. Every value is classified once, by its Go type or
// reflect.Kind, without copying its keys or elements; those are materialized only when the parser
// descends into the value.
type valueKind int

const (
	kindUnknown valueKind = iota
	kindNull
	kindString
	kindInt
	kindFloat
	kindBool
	kindObject
	kindList
)

// classify returns the kind of value. A nil value, or a nil pointer, map or slice, is null. Any
// floating type is "float", including a whole-valued 0.0 (SPEC.md §9.3.1).
func classify(value interface{}) valueKind {
	switch v := value.(type) {
	case nil:
		return kindNull
	case string:
		return kindString
	case bool:
		return kindBool
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, uintptr:
		return kindInt
	case float32, float64:
		return kindFloat
	case json.Number:
		if strings.ContainsAny(string(v), ".eE") {
			return kindFloat
		}
		return kindInt
	case OrderedMap:
		if v == nil {
			return kindNull
		}
		return kindObject
	case map[string]interface{}:
		if v == nil {
			return kindNull
		}
		return kindObject
	case []interface{}:
		if v == nil {
			return kindNull
		}
		return kindList
	}
	rv := reflect.ValueOf(value)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return kindNull
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.String:
		return kindString
	case reflect.Bool:
		return kindBool
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return kindInt
	case reflect.Float32, reflect.Float64:
		return kindFloat
	case reflect.Map:
		if rv.IsNil() {
			return kindNull
		}
		if rv.Type().Key().Kind() == reflect.String {
			return kindObject
		}
	case reflect.Slice:
		if rv.IsNil() {
			return kindNull
		}
		if rv.Type().Elem() == keyValueType {
			// A *OrderedMap, a named type based on OrderedMap or []KeyValue, or a bare []KeyValue.
			return kindObject
		}
		return kindList
	case reflect.Array:
		return kindList
	}
	return kindUnknown
}

var (
	keyValueType   = reflect.TypeOf(KeyValue{})
	orderedMapType = reflect.TypeOf(OrderedMap(nil))
)

// basicTypeName is the SPEC.md §9.2 getBasicPropType: a nested object or list is "object".
func basicTypeName(kind valueKind) string {
	switch kind {
	case kindNull:
		return "null"
	case kindString:
		return "string"
	case kindInt:
		return "int"
	case kindFloat:
		return "float"
	case kindBool:
		return "boolean"
	case kindObject, kindList:
		return "object"
	default:
		return "unknown"
	}
}

// propValueType is the SPEC.md §9.2 getPropValueType: a list is typed by its first element, and
// an empty list (or one whose first element is null) defaults to "list(string)".
func propValueType(value interface{}, kind valueKind) string {
	if kind != kindList {
		return basicTypeName(kind)
	}
	first, ok := firstElement(value)
	if !ok {
		return "list(string)"
	}
	switch firstKind := classify(first); firstKind {
	case kindNull:
		return "list(string)"
	case kindUnknown:
		// "list(unknown)" is not a valid propertyType (SPEC.md §7.3.4).
		return "list(object)"
	default:
		return "list(" + basicTypeName(firstKind) + ")"
	}
}

// isLeaf reports whether a complex value is reported without descending into it: past the
// depth cap (SPEC.md §9.3.2), or when it is one of its own ancestors.
func (p *schemaParser) isLeaf(value interface{}, kind valueKind, depth int) bool {
	if kind != kindObject && kind != kindList {
		return false
	}
	if depth >= maxSchemaDepth {
		return true
	}
	id, ok := identity(value)
	if !ok {
		return false
	}
	for _, ancestor := range p.ancestors {
		if ancestor == id {
			return true
		}
	}
	return false
}

// enterObject maps an object value with that value on the ancestor path.
func (p *schemaParser) enterObject(value interface{}, depth int) []Property {
	pushed := p.push(value)
	result := p.mapObject(objectEntries(value), depth)
	p.pop(pushed)
	return result
}

// enterList maps a list value with that value on the ancestor path.
func (p *schemaParser) enterList(value interface{}, depth int) []interface{} {
	pushed := p.push(value)
	result := p.mapList(listElements(value), depth)
	p.pop(pushed)
	return result
}

func (p *schemaParser) push(value interface{}) bool {
	id, ok := identity(value)
	if ok {
		p.ancestors = append(p.ancestors, id)
	}
	return ok
}

func (p *schemaParser) pop(pushed bool) {
	if pushed {
		p.ancestors = p.ancestors[:len(p.ancestors)-1]
	}
}

func (p *schemaParser) mapObject(entries []KeyValue, depth int) []Property {
	result := make([]Property, 0, len(entries))
	for _, entry := range entries {
		kind := classify(entry.Value)
		property := Property{PropertyName: entry.Key, PropertyType: propValueType(entry.Value, kind)}
		switch {
		case p.isLeaf(entry.Value, kind, depth):
			property.PropertyType = "object"
			property.Children = []Property{}
		case kind == kindObject:
			property.Children = p.enterObject(entry.Value, depth+1)
		case kind == kindList:
			property.ListChildren = p.enterList(entry.Value, depth+1)
		}
		result = append(result, property)
	}
	return result
}

// mapList is the SPEC.md §9.2 mapping function applied to each element of a list: an object maps
// to its []Property, a list to its deduplicated element schemas, and a scalar to its type string.
func (p *schemaParser) mapList(elements []interface{}, depth int) []interface{} {
	mapped := make([]interface{}, 0, len(elements))
	for _, element := range elements {
		kind := classify(element)
		switch {
		case p.isLeaf(element, kind, depth):
			mapped = append(mapped, "object")
		case kind == kindObject:
			mapped = append(mapped, p.enterObject(element, depth+1))
		case kind == kindList:
			mapped = append(mapped, p.enterList(element, depth+1))
		default:
			mapped = append(mapped, basicTypeName(kind))
		}
	}
	return removeDuplicates(mapped)
}

// identity returns what identifies a map, slice or OrderedMap for cycle detection, or false for
// values that cannot contain themselves (arrays held by value, empty slices).
func identity(value interface{}) (nodeIdentity, bool) {
	rv := reflect.ValueOf(value)
	for rv.Kind() == reflect.Ptr {
		if rv.Elem().Kind() == reflect.Array {
			return nodeIdentity{pointer: rv.Pointer(), typ: rv.Type()}, true
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Map:
		return nodeIdentity{pointer: rv.Pointer(), typ: rv.Type()}, true
	case reflect.Slice:
		if rv.Len() == 0 {
			return nodeIdentity{}, false
		}
		return nodeIdentity{pointer: rv.Pointer(), length: rv.Len(), typ: rv.Type()}, true
	default:
		return nodeIdentity{}, false
	}
}

// objectEntries returns the entries of a value classified as an object: an OrderedMap keeps its
// order, any other string-keyed map is sorted by key.
func objectEntries(value interface{}) []KeyValue {
	switch v := value.(type) {
	case OrderedMap:
		return v
	case map[string]interface{}:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		entries := make([]KeyValue, len(keys))
		for i, key := range keys {
			entries[i] = KeyValue{Key: key, Value: v[key]}
		}
		return entries
	}
	rv := indirect(reflect.ValueOf(value))
	if rv.Kind() == reflect.Slice {
		return rv.Convert(orderedMapType).Interface().(OrderedMap)
	}
	keys := rv.MapKeys()
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	entries := make([]KeyValue, len(keys))
	for i, key := range keys {
		entries[i] = KeyValue{Key: key.String(), Value: rv.MapIndex(key).Interface()}
	}
	return entries
}

// listElements returns the elements of a value classified as a list.
func listElements(value interface{}) []interface{} {
	if v, ok := value.([]interface{}); ok {
		return v
	}
	rv := indirect(reflect.ValueOf(value))
	elements := make([]interface{}, rv.Len())
	for i := range elements {
		elements[i] = rv.Index(i).Interface()
	}
	return elements
}

// firstElement returns the first element of a value classified as a list, if it has one.
func firstElement(value interface{}) (interface{}, bool) {
	if v, ok := value.([]interface{}); ok {
		if len(v) == 0 {
			return nil, false
		}
		return v[0], true
	}
	rv := indirect(reflect.ValueOf(value))
	if rv.Len() == 0 {
		return nil, false
	}
	return rv.Index(0).Interface(), true
}

// indirect follows pointers to the value they point at. classify has already ruled out nil ones.
func indirect(rv reflect.Value) reflect.Value {
	for rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	return rv
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
