package main

import (
	"encoding/json"
	avoinspector "github.com/avohq/go-avo-inspector"
)

func main() {
	data := map[string]interface{}{
		"str":  "hello",
		"int":  42,
		"flt":  3.14,
		"bol":  true,
		"nul":  nil,
		"lst":  []interface{}{"foo", "bar", nil, map[string]interface{}{"d": 42}},
		"obj":  map[string]interface{}{"a": 1, "b": "two", "c": []interface{}{true, 3.14}},
		"unk":  complex(1, 2),
		"func": func() {},
	}

	avoInspector, _ := avoinspector.NewAvoInspector("_", avoinspector.Dev, "1.0", "my app")

	call, _ := avoInspector.TrackSchemaFromEvent("Test Event", data)

	result, _ := json.MarshalIndent(call, "", "  ")
	println(string(result))
}
