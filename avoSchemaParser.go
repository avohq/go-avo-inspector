package avoinspector

import (
	"bytes"
	"container/heap"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// maxSchemaDepth bounds the recursion of the schema parser (SPEC.md §9.3.2). A complex value found
// deeper than this is reported as an "object" with empty children instead of being descended into.
const maxSchemaDepth = 10

// maxSchemaExpansions bounds the objects and lists expanded in one extraction. Shared references
// that are not cycles can otherwise expand exponentially; past the budget, complex values are
// reported as "object" like the depth cap.
const maxSchemaExpansions = 10000

// maxSchemaProperties bounds the property entries emitted in one extraction, at every depth. Past it,
// further properties are omitted, in the order they are visited: sorted keys for a map, the given
// order for an OrderedMap. It is independent of maxSchemaExpansions.
const maxSchemaProperties = 10000

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

// UnmarshalJSON is the inverse of MarshalJSON: "children" goes to Children for an "object"
// property and to ListChildren for a list property. Inside ListChildren, an array of schema
// entries decodes as []Property and any other array as []interface{}; an empty array, which the
// JSON does not tell apart, decodes as an empty []interface{}.
func (p *Property) UnmarshalJSON(data []byte) error {
	var raw struct {
		PropertyName string          `json:"propertyName"`
		PropertyType string          `json:"propertyType"`
		Children     json.RawMessage `json:"children"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*p = Property{PropertyName: raw.PropertyName, PropertyType: raw.PropertyType}
	if len(raw.Children) == 0 || string(raw.Children) == "null" {
		return nil
	}
	if strings.HasPrefix(raw.PropertyType, "list(") {
		children, err := decodeListChildren(raw.Children)
		p.ListChildren = children
		return err
	}
	p.Children = []Property{}
	return json.Unmarshal(raw.Children, &p.Children)
}

// decodeListChildren decodes the "children" array of a list property (SPEC.md §7.3.4).
func decodeListChildren(data json.RawMessage) ([]interface{}, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	result := make([]interface{}, 0, len(items))
	for _, item := range items {
		switch firstJSONByte(item) {
		case '"':
			var typeName string
			if err := json.Unmarshal(item, &typeName); err != nil {
				return nil, err
			}
			result = append(result, typeName)
		case '[':
			var elements []json.RawMessage
			if err := json.Unmarshal(item, &elements); err != nil {
				return nil, err
			}
			if len(elements) > 0 && firstJSONByte(elements[0]) == '{' {
				entries := []Property{}
				if err := json.Unmarshal(item, &entries); err != nil {
					return nil, err
				}
				result = append(result, entries)
				continue
			}
			nested, err := decodeListChildren(item)
			if err != nil {
				return nil, err
			}
			result = append(result, nested)
		default:
			return nil, fmt.Errorf("avoinspector: unexpected list child %s", item)
		}
	}
	return result, nil
}

func firstJSONByte(data []byte) byte {
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if len(trimmed) == 0 {
		return 0
	}
	return trimmed[0]
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
	return (&schemaParser{}).enterObject(eventProperties, identity(eventProperties), 0)
}

// schemaParser holds the identities of the maps and slices on the path from the root to the value
// being mapped. A value that is its own ancestor (a cycle) is cut like a value past the depth cap,
// so a map holding itself under several keys cannot expand exponentially.
type schemaParser struct {
	ancestors []nodeIdentity
	// expansions counts the objects and lists mapped so far, the root included.
	expansions int
	// properties counts the property entries emitted so far, at every depth.
	properties int
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
	if kind := scalarKind(rv.Kind()); kind != kindUnknown {
		return kind
	}
	switch rv.Kind() {
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

// scalarKind returns the kind of a scalar reflect.Kind, or kindUnknown for any other.
func scalarKind(kind reflect.Kind) valueKind {
	switch kind {
	case reflect.String:
		return kindString
	case reflect.Bool:
		return kindBool
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return kindInt
	case reflect.Float32, reflect.Float64:
		return kindFloat
	}
	return kindUnknown
}

var (
	keyValueType   = reflect.TypeOf(KeyValue{})
	orderedMapType = reflect.TypeOf(OrderedMap(nil))
	jsonNumberType = reflect.TypeOf(json.Number(""))
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
// an empty list (or one whose first element is null) defaults to "list(string)". A typed slice or
// array of numbers is typed by its element type instead, so an empty []float64 is "list(float)".
func propValueType(value interface{}, kind valueKind) string {
	if kind != kindList {
		return basicTypeName(kind)
	}
	if elementKind, ok := numericElementKind(value); ok {
		return "list(" + basicTypeName(elementKind) + ")"
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

// visit decides how a complex value is mapped. It is a leaf, reported without descending into it,
// when it is past the depth cap (SPEC.md §9.3.2), when the expansion budget is spent, or when it is
// one of its own ancestors. Otherwise it
// returns the value's identity, computed once, to put on the ancestor path while descending.
func (p *schemaParser) visit(value interface{}, kind valueKind, depth int) (leaf bool, id nodeIdentity) {
	if kind != kindObject && kind != kindList {
		return false, nodeIdentity{}
	}
	if depth >= maxSchemaDepth || p.expansions >= maxSchemaExpansions {
		return true, nodeIdentity{}
	}
	id = identity(value)
	if id.typ == nil {
		return false, id
	}
	for _, ancestor := range p.ancestors {
		if ancestor == id {
			return true, id
		}
	}
	return false, id
}

// enterObject maps an object value with its identity on the ancestor path.
func (p *schemaParser) enterObject(value interface{}, id nodeIdentity, depth int) []Property {
	pushed := p.push(id)
	// At most the remaining property budget can be emitted, so take only that many entries.
	result := p.mapObject(objectEntries(value, maxSchemaProperties-p.properties), depth)
	p.pop(pushed)
	return result
}

// enterList maps a list value with its identity on the ancestor path. A typed slice or array whose
// element type alone decides its elements' kind maps to that one type without visiting its
// elements, so its size costs nothing; it still counts as one expansion.
func (p *schemaParser) enterList(value interface{}, id nodeIdentity, depth int) []interface{} {
	pushed := p.push(id)
	defer p.pop(pushed)
	if kind, ok := uniformElementKind(value); ok {
		return []interface{}{basicTypeName(kind)}
	}
	return p.mapList(listElements(value), depth)
}

// uniformElementKind returns the kind every element of a typed slice or array has when its
// element type alone decides it: a scalar, or a kind classify always reports as unknown (struct,
// complex, func, chan, unsafe pointer). Element types whose kind depends on the value (interfaces,
// pointers, maps, slices, arrays, and json.Number, typed by its text) report false. An empty one
// reports false too, unless its elements are numbers (see numericElementKind).
func uniformElementKind(value interface{}) (valueKind, bool) {
	if kind, ok := numericElementKind(value); ok {
		return kind, true
	}
	if _, ok := value.([]interface{}); ok {
		return kindUnknown, false
	}
	rv := indirect(reflect.ValueOf(value))
	elem := rv.Type().Elem()
	if rv.Len() == 0 || elem == jsonNumberType {
		return kindUnknown, false
	}
	if kind := scalarKind(elem.Kind()); kind != kindUnknown {
		return kind, true
	}
	switch elem.Kind() {
	case reflect.Struct, reflect.Complex64, reflect.Complex128, reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return kindUnknown, true
	}
	return kindUnknown, false
}

// numericElementKind returns kindInt or kindFloat for a typed slice or array of integers or
// floats, []byte included, whatever its length: such a list is typed by its element type, like a
// binary value or a primitive array in the other SDKs, so an empty one has the same type and
// children as a non-empty one. Any other list reports false.
func numericElementKind(value interface{}) (valueKind, bool) {
	if _, ok := value.([]interface{}); ok {
		return kindUnknown, false
	}
	rv := indirect(reflect.ValueOf(value))
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return kindUnknown, false
	}
	switch kind := scalarKind(rv.Type().Elem().Kind()); kind {
	case kindInt, kindFloat:
		return kind, true
	}
	return kindUnknown, false
}

// push counts an expansion and puts id on the ancestor path.
func (p *schemaParser) push(id nodeIdentity) bool {
	p.expansions++
	if id.typ == nil {
		return false
	}
	p.ancestors = append(p.ancestors, id)
	return true
}

func (p *schemaParser) pop(pushed bool) {
	if pushed {
		p.ancestors = p.ancestors[:len(p.ancestors)-1]
	}
}

func (p *schemaParser) mapObject(entries []KeyValue, depth int) []Property {
	result := make([]Property, 0, len(entries))
	for _, entry := range entries {
		if p.properties >= maxSchemaProperties {
			break
		}
		p.properties++
		kind := classify(entry.Value)
		property := Property{PropertyName: entry.Key, PropertyType: propValueType(entry.Value, kind)}
		leaf, id := p.visit(entry.Value, kind, depth)
		switch {
		case leaf:
			property.PropertyType = "object"
			property.Children = []Property{}
		case kind == kindObject:
			property.Children = p.enterObject(entry.Value, id, depth+1)
		case kind == kindList:
			property.ListChildren = p.enterList(entry.Value, id, depth+1)
		}
		result = append(result, property)
	}
	return result
}

// mapList is the SPEC.md §9.2 mapping function applied to each element of a list: an object maps
// to its []Property, a list to its deduplicated element schemas, and a scalar to its type string.
// Type strings are deduplicated as they are mapped, keeping the first occurrence. An element that
// is an object or a list is never a duplicate: each keeps its own entry, as in the reference
// parser, which compares those by identity (SPEC.md §9.3.3).
func (p *schemaParser) mapList(elements []interface{}, depth int) []interface{} {
	mapped := []interface{}{}
	seen := map[string]bool{}
	addType := func(typeName string) {
		if !seen[typeName] {
			seen[typeName] = true
			mapped = append(mapped, typeName)
		}
	}
	for _, element := range elements {
		kind := classify(element)
		leaf, id := p.visit(element, kind, depth)
		switch {
		case leaf:
			addType("object")
		case kind == kindObject:
			mapped = append(mapped, p.enterObject(element, id, depth+1))
		case kind == kindList:
			mapped = append(mapped, p.enterList(element, id, depth+1))
		default:
			addType(basicTypeName(kind))
		}
	}
	return mapped
}

// identity returns what identifies a map, slice or OrderedMap for cycle detection. It returns the
// zero nodeIdentity (typ == nil) for values that cannot contain themselves: arrays held by value,
// empty slices, and scalars.
func identity(value interface{}) nodeIdentity {
	rv := reflect.ValueOf(value)
	for rv.Kind() == reflect.Ptr {
		if rv.Elem().Kind() == reflect.Array {
			return nodeIdentity{pointer: rv.Pointer(), typ: rv.Type()}
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Map:
		return nodeIdentity{pointer: rv.Pointer(), typ: rv.Type()}
	case reflect.Slice:
		if rv.Len() == 0 {
			return nodeIdentity{}
		}
		return nodeIdentity{pointer: rv.Pointer(), length: rv.Len(), typ: rv.Type()}
	default:
		return nodeIdentity{}
	}
}

// objectEntries returns at most limit entries of a value classified as an object: the first ones in
// the given order for an OrderedMap, the first ones in sorted key order for any other string-keyed
// map. Only those limit keys are sorted, so a huge map costs O(keys * log limit).
func objectEntries(value interface{}, limit int) []KeyValue {
	if limit <= 0 {
		return nil
	}
	switch v := value.(type) {
	case OrderedMap:
		if len(v) > limit {
			return v[:limit]
		}
		return v
	case map[string]interface{}:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		keys = firstSortedKeys(keys, limit)
		entries := make([]KeyValue, len(keys))
		for i, key := range keys {
			entries[i] = KeyValue{Key: key, Value: v[key]}
		}
		return entries
	}
	rv := indirect(reflect.ValueOf(value))
	if rv.Kind() == reflect.Slice {
		ordered := rv.Convert(orderedMapType).Interface().(OrderedMap)
		if len(ordered) > limit {
			return ordered[:limit]
		}
		return ordered
	}
	mapKeys := rv.MapKeys()
	if len(mapKeys) <= limit {
		sort.Slice(mapKeys, func(i, j int) bool { return mapKeys[i].String() < mapKeys[j].String() })
		entries := make([]KeyValue, len(mapKeys))
		for i, key := range mapKeys {
			entries[i] = KeyValue{Key: key.String(), Value: rv.MapIndex(key).Interface()}
		}
		return entries
	}
	keys := make([]string, len(mapKeys))
	for i, key := range mapKeys {
		keys[i] = key.String()
	}
	keys = firstSortedKeys(keys, limit)
	keyType := rv.Type().Key()
	entries := make([]KeyValue, len(keys))
	for i, key := range keys {
		entries[i] = KeyValue{Key: key, Value: rv.MapIndex(reflect.ValueOf(key).Convert(keyType)).Interface()}
	}
	return entries
}

// firstSortedKeys returns the n smallest keys in sorted order. When there are more than n keys it
// keeps the n smallest in a max-heap instead of sorting them all.
func firstSortedKeys(keys []string, n int) []string {
	if len(keys) <= n {
		sort.Strings(keys)
		return keys
	}
	smallest := keyMaxHeap(append(make([]string, 0, n), keys[:n]...))
	heap.Init(&smallest)
	for _, key := range keys[n:] {
		if key < smallest[0] {
			smallest[0] = key
			heap.Fix(&smallest, 0)
		}
	}
	sort.Strings(smallest)
	return smallest
}

// keyMaxHeap is a container/heap of strings with the largest at the root.
type keyMaxHeap []string

func (h keyMaxHeap) Len() int            { return len(h) }
func (h keyMaxHeap) Less(i, j int) bool  { return h[i] > h[j] }
func (h keyMaxHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *keyMaxHeap) Push(x interface{}) { *h = append(*h, x.(string)) }
func (h *keyMaxHeap) Pop() interface{} {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
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

// copySchema returns a deep copy of schema, so the copy shares no slice with it at any depth.
func copySchema(schema []Property) []Property {
	if schema == nil {
		return nil
	}
	copied := make([]Property, len(schema))
	for i, property := range schema {
		copied[i] = Property{
			PropertyName: property.PropertyName,
			PropertyType: property.PropertyType,
			Children:     copySchema(property.Children),
			ListChildren: copyListChildren(property.ListChildren),
		}
	}
	return copied
}

func copyListChildren(children []interface{}) []interface{} {
	if children == nil {
		return nil
	}
	copied := make([]interface{}, len(children))
	for i, child := range children {
		switch c := child.(type) {
		case []Property:
			copied[i] = copySchema(c)
		case []interface{}:
			copied[i] = copyListChildren(c)
		default:
			copied[i] = c
		}
	}
	return copied
}
