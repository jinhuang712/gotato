module github.com/jinhuang712/gotato/examples/larkbot

go 1.26.0

require (
	github.com/jinhuang712/gotato v0.0.0
	github.com/jinhuang712/gotato/adapter/bash v0.0.0
	github.com/jinhuang712/gotato/adapter/mcp v0.0.0
	github.com/larksuite/oapi-sdk-go/v3 v3.12.2
	github.com/modelcontextprotocol/go-sdk v1.8.0
)

require (
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/gorilla/websocket v1.5.0 // indirect
	github.com/itchyny/gojq v0.12.19 // indirect
	github.com/itchyny/timefmt-go v0.1.8 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/term v0.45.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	mvdan.cc/sh/v3 v3.14.1 // indirect
)

replace github.com/jinhuang712/gotato => ../..

replace github.com/jinhuang712/gotato/adapter/bash => ../../adapter/bash

replace github.com/jinhuang712/gotato/adapter/mcp => ../../adapter/mcp
