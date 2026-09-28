package avoinspector

import (
	"encoding/json"
	"reflect"
	"testing"
)

type om = OrderedMap
type kv = KeyValue
type list = []interface{}

// assertSchemaJSON compares the wire JSON of a schema against the expected JSON document.
func assertSchemaJSON(t *testing.T, actual []Property, expected string) {
	t.Helper()
	actualBytes, err := json.Marshal(actual)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got, want interface{}
	if err := json.Unmarshal(actualBytes, &got); err != nil {
		t.Fatalf("unmarshal actual: %v", err)
	}
	if err := json.Unmarshal([]byte(expected), &want); err != nil {
		t.Fatalf("unmarshal expected: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("schema mismatch\n got: %s\nwant: %s", actualBytes, expected)
	}
}

// The 13 golden fixtures of SPEC.md §10, with the JSON inputs written as native Go values.
func TestExtractSchema_GoldenFixtures(t *testing.T) {
	testCases := []struct {
		name     string
		input    interface{}
		expected string
	}{
		{"fixture-1 basic primitives", om{{"a", true}, {"b", 1}, {"c", "hello"}, {"d", 3.14}},
			`[{"propertyName":"a","propertyType":"boolean"},{"propertyName":"b","propertyType":"int"},{"propertyName":"c","propertyType":"string"},{"propertyName":"d","propertyType":"float"}]`},
		{"fixture-2 null", om{{"a", nil}, {"b", nil}},
			`[{"propertyName":"a","propertyType":"null"},{"propertyName":"b","propertyType":"null"}]`},
		{"fixture-3 empty and falsy", om{{"a", false}, {"b", 0}, {"c", ""}, {"e", nil}, {"f", om{}}, {"g", list{}}},
			`[{"propertyName":"a","propertyType":"boolean"},{"propertyName":"b","propertyType":"int"},{"propertyName":"c","propertyType":"string"},{"propertyName":"e","propertyType":"null"},{"propertyName":"f","propertyType":"object","children":[]},{"propertyName":"g","propertyType":"list(string)","children":[]}]`},
		{"fixture-4 nested object", om{{"user", om{{"name", "Alice"}, {"age", 30}}}},
			`[{"propertyName":"user","propertyType":"object","children":[{"propertyName":"name","propertyType":"string"},{"propertyName":"age","propertyType":"int"}]}]`},
		{"fixture-5 list of strings", om{{"tags", list{"a", "b", "c"}}},
			`[{"propertyName":"tags","propertyType":"list(string)","children":["string"]}]`},
		{"fixture-6 empty array", om{{"items", list{}}},
			`[{"propertyName":"items","propertyType":"list(string)","children":[]}]`},
		{"fixture-7 heterogeneous array", om{{"mixed", list{1.2, "two", om{{"three", 3}}}}},
			`[{"propertyName":"mixed","propertyType":"list(float)","children":["float","string",[{"propertyName":"three","propertyType":"int"}]]}]`},
		{"fixture-8 null input", nil, `[]`},
		{"fixture-9 complex mixed array", om{{"prop7", list{"a", "list", om{{"obj in list", true}, {"int field", 1}}, list{"another", "list"}, list{1, 2}}}},
			`[{"propertyName":"prop7","propertyType":"list(string)","children":["string",[{"propertyName":"obj in list","propertyType":"boolean"},{"propertyName":"int field","propertyType":"int"}],["string"],["int"]]}]`},
		{"fixture-10 list deduplication", om{{"vals", list{"true", "false", true, 10, "true", true, 11, 10, 0.1, 0.1}}},
			`[{"propertyName":"vals","propertyType":"list(string)","children":["string","boolean","int","float"]}]`},
		{"fixture-11 object with nested list", om{{"event", om{{"tags", list{"promo", "sale"}}, {"count", 2}}}},
			`[{"propertyName":"event","propertyType":"object","children":[{"propertyName":"tags","propertyType":"list(string)","children":["string"]},{"propertyName":"count","propertyType":"int"}]}]`},
		{"fixture-12 all types", om{{"str", "hello"}, {"int", 42}, {"float", 3.14}, {"bool", true}, {"null_val", nil}, {"obj", om{{"key", "val"}}}, {"list_str", list{"a"}}, {"list_int", list{1, 2}}, {"list_float", list{1.1}}, {"list_bool", list{true, false}}},
			`[{"propertyName":"str","propertyType":"string"},{"propertyName":"int","propertyType":"int"},{"propertyName":"float","propertyType":"float"},{"propertyName":"bool","propertyType":"boolean"},{"propertyName":"null_val","propertyType":"null"},{"propertyName":"obj","propertyType":"object","children":[{"propertyName":"key","propertyType":"string"}]},{"propertyName":"list_str","propertyType":"list(string)","children":["string"]},{"propertyName":"list_int","propertyType":"list(int)","children":["int"]},{"propertyName":"list_float","propertyType":"list(float)","children":["float"]},{"propertyName":"list_bool","propertyType":"list(boolean)","children":["boolean"]}]`},
		{"fixture-13 3-level nesting", om{{"a", om{{"b", om{{"c", 42}}}}}},
			`[{"propertyName":"a","propertyType":"object","children":[{"propertyName":"b","propertyType":"object","children":[{"propertyName":"c","propertyType":"int"}]}]}]`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assertSchemaJSON(t, extractSchema(tc.input), tc.expected)
		})
	}
}

// SPEC.md §9.3.1: in a statically-typed language the declared type is authoritative, so a
// whole-valued float is "float" and an integer zero is "int".
func TestExtractSchema_FloatZeroIsFloat(t *testing.T) {
	assertSchemaJSON(t, extractSchema(om{{"f64", 0.0}, {"f32", float32(0)}, {"one", 1.0}, {"i", 0}, {"u8", uint8(0)}}),
		`[{"propertyName":"f64","propertyType":"float"},{"propertyName":"f32","propertyType":"float"},{"propertyName":"one","propertyType":"float"},{"propertyName":"i","propertyType":"int"},{"propertyName":"u8","propertyType":"int"}]`)
}

func TestExtractSchema_JSONNumber(t *testing.T) {
	assertSchemaJSON(t, extractSchema(om{{"i", json.Number("3")}, {"f", json.Number("0.0")}, {"e", json.Number("1e3")}}),
		`[{"propertyName":"i","propertyType":"int"},{"propertyName":"f","propertyType":"float"},{"propertyName":"e","propertyType":"float"}]`)
}

// A plain Go map has no order, so its properties are listed sorted by key, at every level.
func TestExtractSchema_PlainMapIsSortedByKey(t *testing.T) {
	input := map[string]interface{}{
		"zeta":  "z",
		"alpha": map[string]interface{}{"y": 1, "b": 2.5},
		"mid":   []interface{}{map[string]interface{}{"k2": true, "k1": nil}},
	}
	for i := 0; i < 20; i++ {
		assertSchemaJSON(t, extractSchema(input),
			`[{"propertyName":"alpha","propertyType":"object","children":[{"propertyName":"b","propertyType":"float"},{"propertyName":"y","propertyType":"int"}]},{"propertyName":"mid","propertyType":"list(object)","children":[[{"propertyName":"k1","propertyType":"null"},{"propertyName":"k2","propertyType":"boolean"}]]},{"propertyName":"zeta","propertyType":"string"}]`)
	}
}

func TestExtractSchema_TypedGoValues(t *testing.T) {
	type custom struct{ A int }
	var nilPtr *int
	answer := 42
	assertSchemaJSON(t, extractSchema(om{
		{"strs", []string{"a", "b"}},
		{"ints", [2]int64{1, 2}},
		{"typedMap", map[string]string{"k": "v"}},
		{"ptr", &answer},
		{"nilPtr", nilPtr},
		{"nilSlice", []string(nil)},
		{"struct", custom{A: 1}},
		{"func", func() {}},
		{"complex", complex(1, 2)},
		{"funcs", []interface{}{func() {}}},
		{"nullFirst", []interface{}{nil, 1}},
		{"nestedList", []interface{}{[]interface{}{1}, []interface{}{2}}},
	}),
		`[{"propertyName":"strs","propertyType":"list(string)","children":["string"]},`+
			`{"propertyName":"ints","propertyType":"list(int)","children":["int"]},`+
			`{"propertyName":"typedMap","propertyType":"object","children":[{"propertyName":"k","propertyType":"string"}]},`+
			`{"propertyName":"ptr","propertyType":"int"},`+
			`{"propertyName":"nilPtr","propertyType":"null"},`+
			`{"propertyName":"nilSlice","propertyType":"null"},`+
			`{"propertyName":"struct","propertyType":"unknown"},`+
			`{"propertyName":"func","propertyType":"unknown"},`+
			`{"propertyName":"complex","propertyType":"unknown"},`+
			`{"propertyName":"funcs","propertyType":"list(object)","children":["unknown"]},`+
			`{"propertyName":"nullFirst","propertyType":"list(string)","children":["null","int"]},`+
			`{"propertyName":"nestedList","propertyType":"list(object)","children":[["int"]]}]`)
}

// SPEC.md §9.3.2: past the depth limit a complex value is truncated to an empty "object".
func TestExtractSchema_DepthTruncation(t *testing.T) {
	var value interface{} = "leaf"
	for i := 0; i < 15; i++ {
		value = om{{"n", value}}
	}
	schema := extractSchema(value)
	depth := 0
	current := schema
	for len(current) > 0 {
		depth++
		if current[0].PropertyType != "object" {
			t.Fatalf("level %d: expected object, got %s", depth, current[0].PropertyType)
		}
		current = current[0].Children
	}
	if depth != maxSchemaDepth+1 {
		t.Errorf("expected truncation after %d levels, got %d", maxSchemaDepth+1, depth)
	}
}

func TestExtractSchema_NonObjectInputIsEmpty(t *testing.T) {
	for _, input := range []interface{}{nil, "str", 3, []interface{}{1}, map[string]interface{}(nil), OrderedMap(nil)} {
		if schema := extractSchema(input); schema == nil || len(schema) != 0 {
			t.Errorf("input %#v: expected empty non-nil schema, got %#v", input, schema)
		}
	}
}
