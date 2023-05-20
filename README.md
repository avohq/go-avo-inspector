# Avo Inspector for Go

## Avo documentation

This is a quick start guide. For more information about the Inspector project please read [Avo Inspector SDK Reference](https://www.avo.app/docs/implementation/avo-inspector-sdk-reference) and the [Avo Inspector Setup Guide](https://www.avo.app/docs/implementation/setup-inspector-sdk).

## Installation


```
    go get github.com/avohq/go-avo-inspector
```

## Initialization

Obtain the API key at [Avo.app](https://www.avo.app/welcome)

```go
import (
	avoinspector "github.com/avohq/go-avo-inspector"
)

avoInspector, err := avoinspector.NewAvoInspector(
    "...", // Your API key obtained in the Avo workspace
    avoinspector.Dev, // or avoinspector.Stafging, avoinspector.Prod
    "1.0", // App version
    "my app" // App name
    )

```

## Enabling logs

Logs are enabled by default in the dev mode and disabled in prod mode.

```go
avoInspector.ShouldLog(true)
```

## Sending event schemas

Whenever you send a tracking event, also call the following method:

Read more in the [Avo documentation](https://www.avo.app/docs/implementation/devs-101#inspecting-events)

### 1.

This method gets actual tracking event parameters, extracts schema automatically and sends it to the Avo Inspector backend.
It is the easiest way to use the library, just call this method at the same place you call your analytics tools' track methods with the same parameters.

```go
result, err := avoInspector.TrackSchemaFromEvent("Test Event", map[string]interface{}{
		"str":  "hello",
		"int":  42,
		"flt":  3.14,
		"bol":  true,
		"nul":  nil,
		"lst":  []interface{}{"foo", "bar", nil, map[string]interface{}{"d": 42}},
		"obj":  map[string]interface{}{"a": 1, "b": "two", "c": []interface{}{true, 3.14}},
		"unk":  complex(1, 2),
		"func": func() {},
	})
```

## Author

Avo (https://www.avo.app), friends@avo.app

## License

AvoInspector is available under the MIT license.
