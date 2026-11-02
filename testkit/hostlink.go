package testkit

import (
	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/hostlink"
)

// published is the core's count of the Events a Session has published, which a
// recorder compares with how many it has read. The core hands it over through
// internal/hostlink; its type is checked once, when testkit is loaded.
var published = hostlink.Published.(func(*gotato.Session) uint64)
