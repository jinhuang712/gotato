module github.com/jinhuang712/gotato/adapter/bash

go 1.26.0

require (
	github.com/itchyny/gojq v0.12.19
	mvdan.cc/sh/v3 v3.14.1
)

require (
	github.com/itchyny/timefmt-go v0.1.8 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/term v0.45.0 // indirect
)

require github.com/jinhuang712/gotato v0.0.0

replace github.com/jinhuang712/gotato => ../..
