package main

import (
	"encoding/json"
	"log"

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

	avoInspector, err := avoinspector.NewAvoInspector("_", avoinspector.Dev, "1.0", "my app")
	if err != nil {
		log.Fatal(err)
	}
	// Send anything still buffered before main returns; buffered events are lost at exit.
	defer func() {
		// ErrFlushTimeout means Flush returned before all events were sent; those may not arrive
		// once main returns. This example logs it and exits anyway.
		if err := avoInspector.Flush(avoinspector.DefaultFlushTimeout); err != nil {
			log.Print("Avo Inspector flush: ", err)
		}
	}()

	call, err := avoInspector.TrackSchemaFromEvent(avoinspector.InspectorEvent{
		EventName:       "Test Event",
		EventProperties: data,
	})
	if err != nil {
		log.Print("Avo Inspector track: ", err)
		return
	}

	result, err := json.MarshalIndent(call, "", "  ")
	if err != nil {
		log.Print(err)
		return
	}
	println(string(result))
}
