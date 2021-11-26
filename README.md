# Avo Inspector for Go

This repo is under development, check out the [issues](https://github.com/avohq/flutter-avo-inspector/issues) to contribute

If you need any help or guidance don't hesitate to contact me on [Twitter](https://twitter.com/TpoM6oH) or our dev team at dev@avo.app

# Intro

At Avo we make sure that your product analytics functions properly. 

One of the tools we have for that is the Avo Inspector. 

It observes the analytics calls of an app, e.g. 
```
MyTracker.track("Login", {
  "userId": 1337,
  "emailAddress": "jane.doe@avo.app",
  "productId": 45,
  "revenue": 15.99,
  "timestamp": 1579263014,
  "deviceId": "2500-11ec-9621"
});
```
You can call Avo Inspector SDK with the same parameters as the tracking calls 
```
AvoInspector.trackSchemaFromEvent("Login", { 
  "userId": 1337, "emailAddress": "jane.doe@avo.app",
  "productId": 45, "revenue": 15.99,
  "timestamp": 1579263014, "deviceId": "2500-11ec-9621"
});
```
and it will extract the event schema and send that schema to Avo servers for inspection.

The resulting payload sent to Avo will look like this:
```
{
  "userId": "int",
  "emailAddress": "string",
  "productId": "int",
  "revenue": "float",
  "timestamp": "int",
  "deviceId": "string"
}
```
Read more about the Inspector SDK [here](https://www.avo.app/docs/implementation/avo-inspector-overview).

# WIP

The SDK doc will be updated alongside the development
