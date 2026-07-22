package gateway

import (
	"mime"
	"strconv"
	"strings"
)

func acceptsEventStream(value string) bool {
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
