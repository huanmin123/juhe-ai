package gatewaydispatch

import (
	"net/url"
	"strings"
)

// UpstreamAttempt mirrors upstream/attempt.ts UpstreamAttempt: the
// per-attempt failure record carried through the dispatch engine.
type UpstreamAttempt struct {
	AccountID                 string
	AccountName               string
	ProviderCode              string
	ProviderProtocolProfileID string
	ProtocolCode              string
	ProtocolVersion           string
	UpstreamURL               string
	Status                    int
	HasStatus                 bool
	Message                   string
	ErrorCode                 string
	TransportFailureKind      string // 'timeout' | 'connection' | 'read_incomplete' | ''
	ResponseHeaders           map[string]string
	ResponseBodyText          string
	ParsedResponseBody        map[string]any
}

// TransportFailureKind values mirror the Node union.
const (
	TransportFailureKindTimeout        = "timeout"
	TransportFailureKindConnection     = "connection"
	TransportFailureKindReadIncomplete = "read_incomplete"
)

// IsRealUpstreamAttempt mirrors isRealUpstreamAttempt: only http(s) URLs
// count as real upstream attempts.
func IsRealUpstreamAttempt(attempt UpstreamAttempt) bool {
	if attempt.UpstreamURL == "" {
		return false
	}
	parsed, err := url.Parse(attempt.UpstreamURL)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	return scheme == "http" || scheme == "https"
}

// IsCompletedRealUpstreamAttempt mirrors isCompletedRealUpstreamAttempt.
func IsCompletedRealUpstreamAttempt(attempt UpstreamAttempt) bool {
	return IsRealUpstreamAttempt(attempt) && attempt.HasStatus && attempt.Status > 0
}
