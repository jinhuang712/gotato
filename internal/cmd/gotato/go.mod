module github.com/jinhuang712/gotato/internal/cmd/gotato

go 1.24

require (
	github.com/jinhuang712/gotato v0.2.0
	github.com/jinhuang712/gotato/adapter/http v0.0.0
)

require gopkg.in/yaml.v3 v3.0.1 // indirect

replace github.com/jinhuang712/gotato => ../../..

replace github.com/jinhuang712/gotato/adapter/http => ../../../adapter/http
