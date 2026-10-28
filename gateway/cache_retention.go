package gateway

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cacheRetention returns the retention every request asks for. Zero means the
// provider's longest.
func cacheRetention(configured time.Duration) (time.Duration, error) {
	if configured < 0 {
		return 0, fmt.Errorf("gateway: CacheRetention cannot be negative")
	}
	return configured, nil
}

// takesPromptCacheOptions reports whether an OpenAI model is GPT-5.6 or
// later, such as gpt-5.6-mini or gpt-6.1-sol.
func takesPromptCacheOptions(model string) bool {
	version, ok := strings.CutPrefix(model, "gpt-")
	if !ok {
		return false
	}
	if end := strings.IndexFunc(version, func(r rune) bool { return r != '.' && (r < '0' || r > '9') }); end >= 0 {
		version = version[:end]
	}
	majorText, minorText, _ := strings.Cut(version, ".")
	major, err := strconv.Atoi(majorText)
	if err != nil {
		return false
	}
	minor := 0
	if minorText != "" {
		if minor, err = strconv.Atoi(minorText); err != nil {
			return false
		}
	}
	return major > 5 || (major == 5 && minor >= 6)
}
