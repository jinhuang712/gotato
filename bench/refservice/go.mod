module github.com/jinhuang712/gotato/bench/refservice

go 1.24

require (
	github.com/jinhuang712/gotato v0.2.0
	github.com/jinhuang712/gotato/adapter/http v0.0.0
)

replace github.com/jinhuang712/gotato => ../..

replace github.com/jinhuang712/gotato/adapter/http => ../../adapter/http
