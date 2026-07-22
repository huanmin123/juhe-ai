package gatewayaudit

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

const RedactedValue = "[redacted]"

// SanitizeHeaders builds the bounded capture DTO header view. Raw audit payload
// ownership remains outside this package and may apply a different retention policy.
func SanitizeHeaders(headers map[string][]string) map[string][]string {
	if headers == nil {
		return nil
	}

	sanitized := make(map[string][]string, len(headers))
	for name, values := range headers {
		if isUncapturedHeader(name) {
			continue
		}
		cloned := make([]string, len(values))
		if isSensitiveName(name) {
			for index := range cloned {
				cloned[index] = RedactedValue
			}
		} else {
			copy(cloned, values)
		}
		sanitized[name] = cloned
	}
	return sanitized
}

// SanitizeURL removes URL userinfo and redacts credential-like query values.
// It is intended for attempt metadata, not for the raw client query payload.
func SanitizeURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("parse audit URL: %w", err)
	}
	if parsed.Scheme == "" && parsed.Host == "" {
		return "", fmt.Errorf("parse audit URL: absolute URL is required")
	}

	parsed.User = nil
	query := parsed.Query()
	for name, values := range query {
		if !isSensitiveName(name) {
			continue
		}
		for index := range values {
			values[index] = RedactedValue
		}
		query[name] = values
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func BoundUTF8(value string, maxBytes int) (string, bool) {
	if maxBytes < 0 {
		maxBytes = 0
	}
	if len(value) <= maxBytes {
		return value, false
	}

	end := maxBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end], true
}

func isUncapturedHeader(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), "x-oai-attestation")
}

func isSensitiveName(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	switch normalized {
	case "authorization", "proxy-authorization", "cookie", "set-cookie",
		"x-api-key", "api-key", "api_key", "openai-api-key", "x-goog-api-key",
		"x-google-api-key", "anthropic-api-key", "x-anthropic-api-key",
		"x-openai-api-key", "access_token", "refresh_token":
		return true
	}
	return strings.HasSuffix(normalized, "-api-key") ||
		strings.HasSuffix(normalized, "_api_key") ||
		strings.Contains(normalized, "secret") ||
		strings.Contains(normalized, "token") ||
		strings.Contains(normalized, "credential")
}
