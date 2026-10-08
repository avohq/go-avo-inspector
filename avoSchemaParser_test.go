package avoinspector

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"
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
			`{"propertyName":"nestedList","propertyType":"list(object)","children":[["int"],["int"]]}]`)
}

// List children keep one entry per object or list element, even when two are identical; only type
// strings are deduplicated. The expected values are the reference Node parser's output for the
// same inputs (SPEC.md §9.3.3).
func TestExtractSchema_DedupMatchesReferenceParser(t *testing.T) {
	testCases := []struct {
		name     string
		input    OrderedMap
		expected string
	}{
		{"nested lists", om{{"v", list{list{1}, list{2}}}},
			`[{"propertyName":"v","propertyType":"list(object)","children":[["int"],["int"]]}]`},
		{"identical objects", om{{"v", list{om{{"a", 1}}, om{{"a", 1}}}}},
			`[{"propertyName":"v","propertyType":"list(object)","children":[[{"propertyName":"a","propertyType":"int"}],[{"propertyName":"a","propertyType":"int"}]]}]`},
		{"mixed", om{{"v", list{list{"x"}, list{"x"}, "s", "s", 1, om{{"b", true}}, om{{"b", true}}}}},
			`[{"propertyName":"v","propertyType":"list(object)","children":[["string"],["string"],"string","int",[{"propertyName":"b","propertyType":"boolean"}],[{"propertyName":"b","propertyType":"boolean"}]]}]`},
		{"deep", om{{"v", list{list{list{1, 1}, list{1}}, list{list{1}}}}},
			`[{"propertyName":"v","propertyType":"list(object)","children":[[["int"],["int"]],[["int"]]]}]`},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assertSchemaJSON(t, extractSchema(tc.input), tc.expected)
		})
	}
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

// Each value is classified once and its keys or elements are copied only when the parser descends
// into it. Typed maps and slices go through reflection, where the older parser copied them two or
// three times: these inputs took 526 and 866 allocations then, and take 276 and 606 now.
func TestExtractSchema_MaterializesEachValueOnce(t *testing.T) {
	typed := map[string]map[string]int{}
	lists := map[string][]map[string]int{}
	for i := 0; i < 10; i++ {
		inner := map[string]int{}
		for j := 0; j < 10; j++ {
			inner[fmt.Sprint(j)] = j
		}
		typed[fmt.Sprint(i)] = inner
		lists[fmt.Sprint(i)] = []map[string]int{inner, inner}
	}
	for _, tc := range []struct {
		name  string
		input interface{}
		limit float64
	}{{"typed maps", typed, 350}, {"lists of typed maps", lists, 700}} {
		if allocs := testing.AllocsPerRun(20, func() { extractSchema(tc.input) }); allocs > tc.limit {
			t.Errorf("%s: %v allocations, want at most %v", tc.name, allocs, tc.limit)
		}
	}
}

// A map, slice or OrderedMap that is its own ancestor is cut like a value past the depth cap: an
// "object" property with no children, or the type string "object" inside a list.
func TestExtractSchema_CutsCyclesByAncestorIdentity(t *testing.T) {
	selfMap := map[string]interface{}{"n": 1}
	selfMap["self"] = selfMap
	assertSchemaJSON(t, extractSchema(selfMap),
		`[{"propertyName":"n","propertyType":"int"},{"propertyName":"self","propertyType":"object","children":[]}]`)

	selfList := []interface{}{nil}
	selfList[0] = selfList
	assertSchemaJSON(t, extractSchema(om{{"list", selfList}}),
		`[{"propertyName":"list","propertyType":"list(object)","children":["object"]}]`)

	ordered := om{{"x", nil}}
	ordered[0].Value = ordered
	assertSchemaJSON(t, extractSchema(ordered),
		`[{"propertyName":"x","propertyType":"object","children":[]}]`)

	// A value shared by siblings is not a cycle and is expanded each time.
	shared := map[string]interface{}{"a": 1}
	assertSchemaJSON(t, extractSchema(om{{"x", shared}, {"y", list{shared}}}),
		`[{"propertyName":"x","propertyType":"object","children":[{"propertyName":"a","propertyType":"int"}]},`+
			`{"propertyName":"y","propertyType":"list(object)","children":[[{"propertyName":"a","propertyType":"int"}]]}]`)
}

// A map holding itself under many keys would otherwise expand to keys^depth entries.
func TestExtractSchema_SelfReferenceUnderManyKeysIsFast(t *testing.T) {
	wide := map[string]interface{}{}
	for i := 0; i < 20; i++ {
		wide[fmt.Sprint(i)] = wide
	}
	done := make(chan []Property, 1)
	go func() { done <- extractSchema(wide) }()
	select {
	case schema := <-done:
		if len(schema) != 20 || len(schema[0].Children) != 0 {
			t.Errorf("expected 20 cut properties, got %d with %d children", len(schema), len(schema[0].Children))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("extracting a self-referencing map did not finish")
	}
}

// The shared cross-SDK reference inputs for the depth rule: top-level properties are at depth 0,
// each descent into a property value or a list element adds 1, and at depth 10 a complex property
// becomes "object" with no children and a complex list element the type string "object".
func TestExtractSchema_DepthReferenceInputs(t *testing.T) {
	testCases := []struct{ input, expected string }{
		{`{"a":[[[[[[[[[[[1]]]]]]]]]]]}`,
			`[{"propertyName":"a","propertyType":"list(object)","children":[[[[[[[[[["object"]]]]]]]]]]}]`},
		{`{"a":[{"b":[{"c":[{"d":[{"e":[{"f":[{"g":1}]}]}]}]}]}]}`,
			`[{"propertyName":"a","propertyType":"list(object)","children":[[{"propertyName":"b","propertyType":"list(object)","children":[[{"propertyName":"c","propertyType":"list(object)","children":[[{"propertyName":"d","propertyType":"list(object)","children":[[{"propertyName":"e","propertyType":"list(object)","children":[[{"propertyName":"f","propertyType":"object","children":[]}]]}]]}]]}]]}]]}]`},
	}
	for _, tc := range testCases {
		var input map[string]interface{}
		if err := json.Unmarshal([]byte(tc.input), &input); err != nil {
			t.Fatal(err)
		}
		assertSchemaJSON(t, extractSchema(input), tc.expected)
	}
}

// Depth counts one level per container, object or list, as in C# and Node: a list of objects
// costs two levels. The expected values are the C# and Node output for the same inputs.
func TestExtractSchema_DepthMatchesReferenceSDKs(t *testing.T) {
	var nested interface{} = 1
	for i := 0; i < 11; i++ {
		nested = list{nested}
	}
	assertSchemaJSON(t, extractSchema(om{{"a", nested}}),
		`[{"propertyName":"a","propertyType":"list(object)","children":[[[[[[[[[["object"]]]]]]]]]]}]`)

	var objects interface{} = om{{"leaf", 1}}
	for i := 6; i >= 1; i-- {
		objects = om{{fmt.Sprintf("l%d", i), list{objects}}}
	}
	assertSchemaJSON(t, extractSchema(objects),
		`[{"propertyName":"l1","propertyType":"list(object)","children":[[{"propertyName":"l2","propertyType":"list(object)","children":[[{"propertyName":"l3","propertyType":"list(object)","children":[[{"propertyName":"l4","propertyType":"list(object)","children":[[{"propertyName":"l5","propertyType":"list(object)","children":[[{"propertyName":"l6","propertyType":"object","children":[]}]]}]]}]]}]]}]]}]`)
}

type namedOrderedMap OrderedMap
type namedKeyValues []KeyValue

// A pointer to an OrderedMap, a named type whose underlying type is []KeyValue, and a bare
// []KeyValue are objects, at the top level and nested.
func TestExtractSchema_OrderedMapVariantsAreObjects(t *testing.T) {
	pointer := &OrderedMap{{"b", 1}, {"a", "x"}}
	expected := `[{"propertyName":"b","propertyType":"int"},{"propertyName":"a","propertyType":"string"}]`
	for name, input := range map[string]interface{}{
		"pointer":       pointer,
		"named":         namedOrderedMap{{"b", 1}, {"a", "x"}},
		"named slice":   namedKeyValues{{"b", 1}, {"a", "x"}},
		"bare":          []KeyValue{{"b", 1}, {"a", "x"}},
		"pointer named": &namedKeyValues{{"b", 1}, {"a", "x"}},
	} {
		t.Run(name, func(t *testing.T) {
			assertSchemaJSON(t, extractSchema(input), expected)
			assertSchemaJSON(t, extractSchema(om{{"nested", input}, {"list", list{input}}}),
				`[{"propertyName":"nested","propertyType":"object","children":`+expected+`},`+
					`{"propertyName":"list","propertyType":"list(object)","children":[`+expected+`]}]`)
		})
	}
	var nilPointer *OrderedMap
	assertSchemaJSON(t, extractSchema(om{{"nil", nilPointer}, {"nilNamed", namedKeyValues(nil)}}),
		`[{"propertyName":"nil","propertyType":"null"},{"propertyName":"nilNamed","propertyType":"null"}]`)
}

// A sub-slice shares its parent's backing array but is a different value, not a cycle.
func TestExtractSchema_SubSliceIsNotACycle(t *testing.T) {
	s := []interface{}{"a", nil}
	s[1] = s[:1]
	assertSchemaJSON(t, extractSchema(om{{"v", s}}),
		`[{"propertyName":"v","propertyType":"list(string)","children":["string",["string"]]}]`)
}

// The wire JSON of a schema decodes back into the same []Property.
func TestProperty_JSONRoundTrip(t *testing.T) {
	testCases := map[string]interface{}{
		"objects":        om{{"user", om{{"name", "a"}, {"address", om{{"zip", 1}}}}}, {"empty", om{}}, {"n", nil}},
		"lists of lists": om{{"v", list{list{1, list{"x"}}, list{2.5}, "s"}}, {"empty", list{}}},
		"list of object": om{{"v", list{om{{"a", 1}}, om{{"b", list{true}}}}}},
		"fixture-9":      om{{"prop7", list{"a", "list", om{{"obj in list", true}, {"int field", 1}}, list{"another", "list"}, list{1, 2}}}},
		"depth cut": om{{"deep", func() interface{} {
			var v interface{} = 1
			for i := 0; i < 12; i++ {
				v = om{{"n", v}}
			}
			return v
		}()}},
	}
	for name, input := range testCases {
		t.Run(name, func(t *testing.T) {
			schema := extractSchema(input)
			encoded, err := json.Marshal(schema)
			if err != nil {
				t.Fatal(err)
			}
			var decoded []Property
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("decode %s: %v", encoded, err)
			}
			if !reflect.DeepEqual(decoded, schema) {
				t.Errorf("round trip changed the schema\n got: %#v\nwant: %#v", decoded, schema)
			}
			reencoded, _ := json.Marshal(decoded)
			if string(reencoded) != string(encoded) {
				t.Errorf("re-encoding differs\n got: %s\nwant: %s", reencoded, encoded)
			}
		})
	}
}

// sharedDAG builds levels nested maps where every key at a level refers to the same next-level
// map: no cycle, but fanOut^levels paths without a budget.
func sharedDAG(fanOut, levels int) OrderedMap {
	next := om{{"leaf", 1}}
	for level := levels; level >= 1; level-- {
		current := om{}
		for i := 0; i < fanOut; i++ {
			current = append(current, kv{fmt.Sprintf("k%d", i), next})
		}
		next = current
	}
	return next
}

// countExpanded counts the objects and lists in a schema that were expanded (non-leaf), plus
// the root, and the complex values cut to "object".
func countExpanded(schema []Property) (expanded, cut int) {
	var walkList func([]interface{})
	var walk func([]Property)
	walk = func(entries []Property) {
		for _, entry := range entries {
			switch {
			case entry.PropertyType == "object" && len(entry.Children) == 0 && entry.ListChildren == nil:
				// Either an empty object or a cut value; the tests below use no empty objects.
				cut++
			case entry.PropertyType == "object":
				expanded++
				walk(entry.Children)
			case entry.ListChildren != nil:
				expanded++
				walkList(entry.ListChildren)
			}
		}
	}
	walkList = func(items []interface{}) {
		for _, item := range items {
			switch v := item.(type) {
			case []Property:
				expanded++
				walk(v)
			case []interface{}:
				expanded++
				walkList(v)
			case string:
				if v == "object" {
					cut++
				}
			}
		}
	}
	walk(schema)
	return expanded + 1, cut
}

// Shared references that are not cycles are bounded by a per-call budget of 10,000 expanded
// objects and lists; past it, complex values are cut like the depth cap.
func TestExtractSchema_SharedReferencesAreBounded(t *testing.T) {
	for _, fanOut := range []int{4, 6} {
		t.Run(fmt.Sprintf("fan-out %d", fanOut), func(t *testing.T) {
			input := sharedDAG(fanOut, 12)
			done := make(chan []Property, 1)
			start := time.Now()
			go func() { done <- extractSchema(input) }()
			select {
			case schema := <-done:
				elapsed := time.Since(start)
				// About 1.5ms, 11ms under -race; without the budget, fan-out 4 took 17s.
				if elapsed > 100*time.Millisecond {
					t.Errorf("took %v", elapsed)
				}
				t.Logf("fan-out %d: %v", fanOut, elapsed)
				// Whichever budget runs out first bounds it: here the 10,000 properties, before
				// 10,000 expanded values.
				expanded, _ := countExpanded(schema)
				if properties := countProperties(schema); properties != maxSchemaProperties || expanded > maxSchemaExpansions {
					t.Errorf("expected %d properties and at most %d expanded values, got %d and %d",
						maxSchemaProperties, maxSchemaExpansions, properties, expanded)
				}
				// Bounded by the budget: at most fanOut entries per expanded value.
				if encoded, _ := json.Marshal(schema); len(encoded) > maxSchemaExpansions*(fanOut+1)*80 {
					t.Errorf("schema JSON is %d bytes", len(encoded))
				}
			case <-time.After(5 * time.Second):
				t.Fatal("extraction did not finish")
			}
		})
	}
}

// The budget counts the root and every list: root + list + 9,998 objects is exactly 10,000, so a
// 9,999th object is the first value cut.
func TestExtractSchema_ExpansionBudgetBoundary(t *testing.T) {
	objects := func(n int) list {
		items := make(list, n)
		for i := range items {
			items[i] = om{{"i", i}}
		}
		return items
	}
	within := extractSchema(om{{"items", objects(9998)}})
	if expanded, cut := countExpanded(within); expanded != 10000 || cut != 0 {
		t.Errorf("9,998 objects: expected 10000 expanded and none cut, got %d and %d", expanded, cut)
	}
	// The two cut elements are both the type string "object", which is deduplicated like any
	// type string, so the list ends in one "object".
	over := extractSchema(om{{"items", objects(10000)}})
	children := over[0].ListChildren
	if expanded, cut := countExpanded(over); expanded != 10000 || cut != 1 {
		t.Errorf("10,000 objects: expected 10000 expanded and 1 cut, got %d and %d", expanded, cut)
	}
	if len(children) != 9999 || children[9997] == "object" || children[9998] != "object" {
		t.Errorf("expected 9998 expanded objects then \"object\", got %d children", len(children))
	}
	// Scalars never count toward the budget.
	scalars := make(list, 20000)
	for i := range scalars {
		scalars[i] = i
	}
	assertSchemaJSON(t, extractSchema(om{{"n", scalars}, {"after", om{{"x", 1}}}}),
		`[{"propertyName":"n","propertyType":"list(int)","children":["int"]},{"propertyName":"after","propertyType":"object","children":[{"propertyName":"x","propertyType":"int"}]}]`)
}

// For shared-reference inputs that hit the budget, the output is byte-identical to the Node SDK's
// parser. The digests are of Node's JSON.stringify output for the same inputs.
func TestExtractSchema_BudgetMatchesNode(t *testing.T) {
	objects := make(list, 10000)
	for i := range objects {
		objects[i] = om{{"i", i}}
	}
	for _, tc := range []struct {
		name   string
		input  OrderedMap
		sha256 string
	}{
		{"10,000 objects", om{{"items", objects}}, "128e49642937e1ca0cc40e5f3c09268a5cde25f3d8f449735176e16f900e2597"},
	} {
		encoded, err := json.Marshal(extractSchema(tc.input))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(encoded)
		if got := hex.EncodeToString(digest[:]); got != tc.sha256 {
			t.Errorf("%s: output differs from Node (sha256 %s, %d bytes)", tc.name, got, len(encoded))
		}
	}
}

type namedString string

// A typed slice of scalars has one child type, whatever its length; []json.Number still types
// each element (SPEC.md §9.3.1.1). A typed slice or array of numbers is typed by its element type
// even when empty; an empty one of strings or booleans is "list(string)" with no children.
func TestExtractSchema_TypedScalarSlices(t *testing.T) {
	array := [3]float32{1, 2, 3}
	floats := []float64{1.5, 2}
	assertSchemaJSON(t, extractSchema(om{
		{"nums", []json.Number{"1.5", "2"}},
		{"named", []namedString{"a"}},
		{"bytes", []byte("hi")},
		{"arr", array},
		{"parr", &array},
		{"pslice", &floats},
		{"empty", []int{}},
		{"emptyFloats", []float64{}},
		{"emptyFloats32", [0]float32{}},
		{"emptyBytes", []byte{}},
		{"emptyUints", &[]uint16{}},
		{"emptyStrings", []string{}},
		{"emptyBools", []bool{}},
		{"emptyNums", []json.Number{}},
		{"iface", list{1, "a", 2, 1.5, "b", nil, om{{"x", 1}}, 3}},
		{"ptrs", []*int{nil}},
		{"u", []uintptr{1}},
		{"b2", []bool{true}},
	}), `[{"propertyName":"nums","propertyType":"list(float)","children":["float","int"]},`+
		`{"propertyName":"named","propertyType":"list(string)","children":["string"]},`+
		`{"propertyName":"bytes","propertyType":"list(int)","children":["int"]},`+
		`{"propertyName":"arr","propertyType":"list(float)","children":["float"]},`+
		`{"propertyName":"parr","propertyType":"list(float)","children":["float"]},`+
		`{"propertyName":"pslice","propertyType":"list(float)","children":["float"]},`+
		`{"propertyName":"empty","propertyType":"list(int)","children":["int"]},`+
		`{"propertyName":"emptyFloats","propertyType":"list(float)","children":["float"]},`+
		`{"propertyName":"emptyFloats32","propertyType":"list(float)","children":["float"]},`+
		`{"propertyName":"emptyBytes","propertyType":"list(int)","children":["int"]},`+
		`{"propertyName":"emptyUints","propertyType":"list(int)","children":["int"]},`+
		`{"propertyName":"emptyStrings","propertyType":"list(string)","children":[]},`+
		`{"propertyName":"emptyBools","propertyType":"list(string)","children":[]},`+
		`{"propertyName":"emptyNums","propertyType":"list(string)","children":[]},`+
		`{"propertyName":"iface","propertyType":"list(int)","children":["int","string","float","null",[{"propertyName":"x","propertyType":"int"}]]},`+
		`{"propertyName":"ptrs","propertyType":"list(string)","children":["null"]},`+
		`{"propertyName":"u","propertyType":"list(int)","children":["int"]},`+
		`{"propertyName":"b2","propertyType":"list(boolean)","children":["boolean"]}]`)
}

// Scalars never count toward the limits, so a large list of them must not cost memory in
// proportion to its length: no boxed copy of a typed slice, no type string per element.
func TestExtractSchema_LargeScalarListsAllocateLittle(t *testing.T) {
	const n = 1 << 20
	floats := make([]float64, n)
	for i := range floats {
		floats[i] = float64(i) + 0.5
	}
	boxed := make(list, n)
	for i := range boxed {
		boxed[i] = floats[i]
	}
	for name, value := range map[string]interface{}{
		"[]float64": floats, "[]byte": make([]byte, n), "[]interface{}": boxed,
	} {
		input := om{{"v", value}}
		if allocated := bytesPerRun(5, func() { extractSchema(input) }); allocated > 1<<20 {
			t.Errorf("%s of %d elements: allocated %d bytes per run, want at most 1 MiB", name, n, allocated)
		}
	}
}

// bytesPerRun is testing.AllocsPerRun for bytes: it averages the bytes allocated over runs calls of
// f, after one warm-up call, with GOMAXPROCS set to 1 so other goroutines barely run meanwhile.
// Bytes, not an allocation count, catch every regression: for a []interface{} the old parser made
// two allocations the size of the list, not one per element.
func bytesPerRun(runs int, f func()) uint64 {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	f()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < runs; i++ {
		f()
	}
	runtime.ReadMemStats(&after)
	return (after.TotalAlloc - before.TotalAlloc) / uint64(runs)
}

// A typed slice or array whose element kind always classifies as "unknown" has one "unknown"
// child, whatever its values. Element types whose classification depends on the value (maps,
// pointers, interfaces) keep typing each element.
func TestExtractSchema_TypedUnknownSlices(t *testing.T) {
	x := 1
	assertSchemaJSON(t, extractSchema(om{
		{"structs", []struct{ A int }{{1}, {2}}},
		{"complex", [2]complex64{}},
		{"funcs", []func(){nil, func() {}}},
		{"chans", []chan int{nil, make(chan int)}},
		{"unsafe", []unsafe.Pointer{nil}},
		{"kvarray", [2]KeyValue{}},
		{"times", []time.Time{{}}},
		{"intmaps", []map[int]int{nil, {1: 1}}},
		{"ptrs", []*int{nil, &x}},
		{"ifaces", []interface{ String() string }{nil, time.Second}},
	}), `[{"propertyName":"structs","propertyType":"list(object)","children":["unknown"]},`+
		`{"propertyName":"complex","propertyType":"list(object)","children":["unknown"]},`+
		`{"propertyName":"funcs","propertyType":"list(object)","children":["unknown"]},`+
		`{"propertyName":"chans","propertyType":"list(object)","children":["unknown"]},`+
		`{"propertyName":"unsafe","propertyType":"list(object)","children":["unknown"]},`+
		`{"propertyName":"kvarray","propertyType":"list(object)","children":["unknown"]},`+
		`{"propertyName":"times","propertyType":"list(object)","children":["unknown"]},`+
		`{"propertyName":"intmaps","propertyType":"list(string)","children":["null","unknown"]},`+
		`{"propertyName":"ptrs","propertyType":"list(string)","children":["null","int"]},`+
		`{"propertyName":"ifaces","propertyType":"list(string)","children":["null","int"]}]`)
}

// A large list of structs must not be copied element by element: every element is "unknown".
func TestExtractSchema_LargeStructListsAllocateLittle(t *testing.T) {
	structs := make([]struct{ A [200]byte }, 1<<16)
	input := om{{"v", structs}}
	if allocated := bytesPerRun(5, func() { extractSchema(input) }); allocated > 1<<20 {
		t.Errorf("[]struct of %d elements: allocated %d bytes per run, want at most 1 MiB", len(structs), allocated)
	}
}

// A list mapped without visiting its elements still counts as one expansion.
func TestExtractSchema_UniformListsCountOneExpansion(t *testing.T) {
	parser := &schemaParser{}
	value := om{{"s", []struct{}{{}}}, {"f", []float64{1}}, {"e", []float64{}}}
	parser.enterObject(value, identity(value), 0)
	if parser.expansions != 4 {
		t.Errorf("expansions = %d, want 4 (the root and three lists)", parser.expansions)
	}
}

// countProperties counts the property entries in a schema at every depth, including those inside
// list children.
func countProperties(schema []Property) int {
	count := 0
	for _, property := range schema {
		count += 1 + countProperties(property.Children) + countListProperties(property.ListChildren)
	}
	return count
}

func countListProperties(items []interface{}) int {
	count := 0
	for _, item := range items {
		switch v := item.(type) {
		case []Property:
			count += countProperties(v)
		case []interface{}:
			count += countListProperties(v)
		}
	}
	return count
}

// One extraction emits at most 10,000 properties. A 1,000,000-key map yields its first 10,000 keys in
// sorted order, an OrderedMap its first 10,000 in the given order, quickly and with a small body.
func TestExtractSchema_PropertyBudgetOnHugeObjects(t *testing.T) {
	const n = 1000000
	plain := make(map[string]interface{}, n)
	ordered := make(OrderedMap, 0, n)
	keys := make([]string, 0, n)
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("k%d", n-i) // inserted in reverse, so sorted and given order differ
		plain[key] = i
		ordered = append(ordered, KeyValue{key, i})
		keys = append(keys, key)
	}
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	for _, tc := range []struct {
		name  string
		input interface{}
		want  []string
	}{
		{"map, sorted order", plain, sorted[:maxSchemaProperties]},
		{"OrderedMap, given order", ordered, keys[:maxSchemaProperties]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			schema := extractSchema(tc.input)
			elapsed := time.Since(start)
			if len(schema) != maxSchemaProperties || countProperties(schema) != maxSchemaProperties {
				t.Fatalf("expected %d properties, got %d", maxSchemaProperties, len(schema))
			}
			for i, property := range schema {
				if property.PropertyName != tc.want[i] {
					t.Fatalf("property %d is %q, want %q", i, property.PropertyName, tc.want[i])
				}
			}
			encoded, _ := json.Marshal(schema)
			if elapsed > 2*time.Second || len(encoded) > 1<<20 {
				t.Errorf("took %v and %d bytes", elapsed, len(encoded))
			}
			t.Logf("%s: %v, %d bytes", tc.name, elapsed, len(encoded))
		})
	}
}

// Nested properties use the same budget, counted depth-first: an object's children are counted
// before its next sibling, and once the budget is spent the rest is omitted.
func TestExtractSchema_PropertyBudgetCountsNestedProperties(t *testing.T) {
	inner := make(OrderedMap, 0, 9999)
	for i := 0; i < 9999; i++ {
		inner = append(inner, KeyValue{fmt.Sprintf("c%d", i), i})
	}
	listed := om{{"x", 1}, {"y", 2}}
	schema := extractSchema(om{{"a", inner}, {"b", 1}, {"c", list{listed}}})
	if len(schema) != 1 || schema[0].PropertyName != "a" || len(schema[0].Children) != 9999 {
		t.Fatalf("expected only a with its 9,999 children (10,000 in all), got %d top-level", len(schema))
	}

	// Objects inside lists count too: 9,998 + c + the two properties of its list element = 10,001.
	almost := make(OrderedMap, 0, 9998)
	for i := 0; i < 9998; i++ {
		almost = append(almost, KeyValue{fmt.Sprintf("p%d", i), i})
	}
	almost = append(almost, KeyValue{"c", list{listed}})
	schema = extractSchema(almost)
	if got := countProperties(schema); got != maxSchemaProperties {
		t.Errorf("expected %d properties counting list elements, got %d", maxSchemaProperties, got)
	}
	element := schema[len(schema)-1].ListChildren[0].([]Property)
	if len(element) != 1 || element[0].PropertyName != "x" {
		t.Errorf("expected the list element cut after x, got %v", element)
	}
}

// Below the budget nothing changes.
func TestExtractSchema_PropertyBudgetLeavesSmallEventsAlone(t *testing.T) {
	input := make(map[string]interface{}, 9999)
	for i := 0; i < 9999; i++ {
		input[fmt.Sprintf("k%05d", i)] = i
	}
	schema := extractSchema(input)
	if len(schema) != 9999 || schema[0].PropertyName != "k00000" || schema[9998].PropertyName != "k09998" {
		t.Errorf("expected all 9,999 properties in sorted order, got %d", len(schema))
	}
}

// canonical writes a schema's wire JSON in the cross-SDK canonical form: {name|type[|children]} for
// an entry, [a,b] for an array, a JSON string for a type string.
func canonical(value interface{}, out *strings.Builder) {
	switch v := value.(type) {
	case []interface{}:
		out.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				out.WriteByte(',')
			}
			canonical(item, out)
		}
		out.WriteByte(']')
	case map[string]interface{}:
		out.WriteString("{" + v["propertyName"].(string) + "|" + v["propertyType"].(string))
		if children, ok := v["children"]; ok {
			out.WriteByte('|')
			canonical(children, out)
		}
		out.WriteByte('}')
	case string:
		quoted, _ := json.Marshal(v)
		out.Write(quoted)
	}
}

// flatOrdered is key0..key(n-1) = int, in insertion order.
func flatOrdered(n int) OrderedMap {
	m := make(OrderedMap, 0, n)
	for i := 0; i < n; i++ {
		m = append(m, KeyValue{"key" + strconv.Itoa(i), i})
	}
	return m
}

// mapsDag is depth levels whose fan keys k0..k(fan-1) all refer to the same next level, ending in
// {"v": 1}: the Java SDK's test input.
func mapsDag(fan, depth int) OrderedMap {
	child := om{{"v", 1}}
	for d := 0; d < depth; d++ {
		level := make(OrderedMap, 0, fan)
		for k := 0; k < fan; k++ {
			level = append(level, KeyValue{"k" + strconv.Itoa(k), child})
		}
		child = level
	}
	return child
}

// The property budget gives the same output as the Java SDK (and Node): the digests and lengths
// are of the canonical form of the reference SDKs' output for the same inputs.
func TestExtractSchema_PropertyBudgetMatchesReferenceSDKs(t *testing.T) {
	nested := make(OrderedMap, 0, 5)
	for _, key := range []string{"a", "b", "c", "d", "e"} {
		nested = append(nested, KeyValue{key, flatOrdered(5000)})
	}
	items := om{{"items", list{flatOrdered(4000), flatOrdered(4000), flatOrdered(4000)}}, {"after", 1}}
	for _, tc := range []struct {
		name   string
		input  OrderedMap
		length int
		sha256 string
	}{
		{"flat 1M keys", flatOrdered(1000000), 138891, "9652d270cd6568d00b73032b56b73782771f634703c89f7fa81df852ae50c835"},
		{"nested a..e of 5,000", nested, 137779, "73549e68066367f3e976a97b299df3bb972e80fe65b487f89b7d7f251397984d"},
		{"items of 3 maps of 4,000, then after", items, 136686, "1f5e38d0b71cdb231568521f0f1ceffc768cd7fcae5e08ab555fc64b8227601f"},
		{"mapsDag(4,12)", mapsDag(4, 12), 147496, "5012bc5b9543195e969f2b10956402ef0306e92a009f409194dffd8bd46083ed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, _ := json.Marshal(extractSchema(tc.input))
			var wire interface{}
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			var out strings.Builder
			canonical(wire, &out)
			digest := sha256.Sum256([]byte(out.String()))
			if got := hex.EncodeToString(digest[:]); got != tc.sha256 || out.Len() != tc.length {
				t.Errorf("differs from the reference SDKs: sha256 %s, length %d, %d entries", got, out.Len(), strings.Count(out.String(), "{"))
			}
		})
	}
}
