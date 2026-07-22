package gatewayaudit

import (
	"strings"
	"unicode/utf8"
)

func BoundUTF8(value string, maxBytes int) (string, bool) {
	if maxBytes < 0 {
		maxBytes = 0
	}
	if len(value) <= maxBytes {
		return strings.Clone(value), false
	}

	end := maxBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return strings.Clone(value[:end]), true
}
