package gateway

import (
	"mime"
	"strconv"
	"strings"
)

// AcceptsEventStream reports whether a parsed Accept header permits SSE. It is
// exported for listener-independent request-facts adapters; it performs no
// HTTP I/O and does not mutate the header value.
func AcceptsEventStream(value string) bool {
	for _, item := range splitHeaderList(value) {
		mediaType, parameters, err := mime.ParseMediaType(strings.TrimSpace(item))
		if err != nil || !strings.EqualFold(mediaType, "text/event-stream") {
			continue
		}
		if quality, ok := parameters["q"]; ok {
			parsed, err := strconv.ParseFloat(quality, 64)
			if err != nil || !(parsed > 0 && parsed <= 1) {
				continue
			}
		}
		return true
	}
	return false
}

func acceptsEventStream(value string) bool { return AcceptsEventStream(value) }

func splitHeaderList(value string) []string {
	items := make([]string, 0, 4)
	start := 0
	quoted := false
	escaped := false
	for index := 0; index < len(value); index++ {
		switch current := value[index]; {
		case escaped:
			escaped = false
		case quoted && current == '\\':
			escaped = true
		case current == '"':
			quoted = !quoted
		case current == ',' && !quoted:
			items = append(items, value[start:index])
			start = index + 1
		}
	}
	return append(items, value[start:])
}
