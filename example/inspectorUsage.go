package main

import (
	"encoding/json"
	avoinspector "github.com/avohq/go-avo-inspector/v2"
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
	// Send anything still buffered before main returns; buffered events are lost at exit.
	defer func() {
		// ErrFlushTimeout means sends were still in flight at the timeout; they may not arrive
		// once main returns. This example logs it and exits anyway.
		if err := avoInspector.Flush(avoinspector.DefaultFlushTimeout); err != nil {
			println("Avo Inspector flush:", err.Error())
		}
	}()

	call, _ := avoInspector.TrackSchemaFromEvent("Test Event", data)

	result, _ := json.MarshalIndent(call, "", "  ")
	println(string(result))
}
