package gatewayaudit

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

const (
	RedactedValue              = "[redacted]"
	HeaderPreviewMaxBytes      = 64 * 1024
	HeaderPreviewMaxEntries    = 128
	HeaderPreviewMaxInspected  = 256
	HeaderPreviewMaxValues     = 32
	HeaderPreviewMaxNameBytes  = 256
	HeaderPreviewMaxValueBytes = 8192
	MaxURLInputBytes           = 64 * 1024
)

// SanitizeHeaders builds the bounded capture DTO header preview. Raw audit payload
// ownership remains outside this package and may apply a different retention policy.
func SanitizeHeaders(headers map[string][]string) map[string][]string {
	sanitized, _ := SanitizeHeaderPreview(headers)
	return sanitized
}

func SanitizeHeaderPreview(headers map[string][]string) (map[string][]string, bool) {
	if headers == nil {
		return nil, false
	}

	capacity := min(len(headers), HeaderPreviewMaxEntries)
	sanitized := make(map[string][]string, capacity)
	usedBytes := 0
	truncated := false
	inspected := 0
	for name, values := range headers {
		inspected++
		if inspected > HeaderPreviewMaxInspected {
			truncated = true
			break
		}
		if isUncapturedHeader(name) {
			continue
		}
		if len(sanitized) >= HeaderPreviewMaxEntries {
			truncated = true
			break
		}
		boundedName, nameTruncated := BoundUTF8(name, HeaderPreviewMaxNameBytes)
		truncated = truncated || nameTruncated
		entryBytes := int(ResidentItemOverheadBytes) + len(boundedName)
		if usedBytes+entryBytes > HeaderPreviewMaxBytes {
			truncated = true
			break
		}
		valueCount := min(len(values), HeaderPreviewMaxValues)
		if valueCount < len(values) {
			truncated = true
		}
		cloned := make([]string, 0, valueCount)
		if isSensitiveName(name) {
			for range valueCount {
				if usedBytes+entryBytes+len(RedactedValue) > HeaderPreviewMaxBytes {
					truncated = true
					break
				}
				cloned = append(cloned, RedactedValue)
				entryBytes += len(RedactedValue)
			}
		} else {
			for _, value := range values[:valueCount] {
				boundedValue, valueTruncated := BoundUTF8(value, HeaderPreviewMaxValueBytes)
				truncated = truncated || valueTruncated
				if usedBytes+entryBytes+len(boundedValue) > HeaderPreviewMaxBytes {
					truncated = true
					break
				}
				cloned = append(cloned, boundedValue)
				entryBytes += len(boundedValue)
			}
		}
		if _, exists := sanitized[boundedName]; exists {
			truncated = true
			continue
		}
		sanitized[boundedName] = cloned
		usedBytes += entryBytes
	}
	return sanitized, truncated
}

// SanitizeURL removes URL userinfo and redacts credential-like query values.
// It is intended for attempt metadata, not for the raw client query payload.
func SanitizeURL(value string) (string, error) {
	if len(value) > MaxURLInputBytes {
		return "", fmt.Errorf("parse audit URL: input is too large")
	}
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("parse audit URL: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if parsed.Opaque != "" || (scheme != "http" && scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("parse audit URL: absolute HTTP(S) URL is required")
	}

	parsed.Scheme = scheme
	parsed.User = nil
	parsed.Fragment = ""
	parsed.RawFragment = ""
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
		return strings.Clone(value), false
	}

	end := maxBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return strings.Clone(value[:end]), true
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
