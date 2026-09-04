package gatewayusage

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// Audit payload summary mirroring
// backend/src/modules/audit-logs/audit-payload-summary.ts: the head/tail
// base64 window + text preview contract applied when bodies exceed the
// full-capture limits.

// AuditBodySummaryEdgeBytes mirrors auditBodySummaryEdgeBytes.
const AuditBodySummaryEdgeBytes = 256 * 1024

// AuditPayloadSummaryContentType mirrors auditPayloadSummaryContentType.
const AuditPayloadSummaryContentType = "application/json; audit=payload-summary"

// auditBodySummaryTextPreviewBytes mirrors auditBodySummaryTextPreviewBytes.
const auditBodySummaryTextPreviewBytes = 4 * 1024

// AuditPayloadSummaryReason mirrors AuditPayloadSummaryReason.
type AuditPayloadSummaryReason = string

// Summary reason values.
const (
	SummaryReasonBodyExceededLimit AuditPayloadSummaryReason = "body_exceeded_full_capture_limit"
	SummaryReasonTransportBudget   AuditPayloadSummaryReason = "transport_message_budget"
)

// SummarizeAuditPayloadOptions mirrors the options bag.
type SummarizeAuditPayloadOptions struct {
	Force                  bool
	IncludeGatewayMetadata bool
	Reason                 AuditPayloadSummaryReason
}

// SummarizeAuditPayloadForLimit mirrors summarizeAuditPayloadForLimit: true
// when the body was replaced by a summary or hash.
func SummarizeAuditPayloadForLimit(payload *AuditLogPayloadInput, fullBodyLimitBytes int, options SummarizeAuditPayloadOptions) bool {
	if (!options.IncludeGatewayMetadata && payload.PartType == AuditPartGatewayMetadata) || !payload.HasBody {
		return false
	}
	if payload.CaptureStatus != "" && payload.CaptureStatus != AuditCaptureComplete {
		updateExistingPayloadSummaryLimit(payload, fullBodyLimitBytes)
		return false
	}
	body := payload.Body
	originalBodySizeBytes := len(body)
	if payload.RawBodySizeBytes != nil {
		originalBodySizeBytes = *payload.RawBodySizeBytes
	}
	if fullBodyLimitBytes == 0 {
		if payload.BodySha256 == "" {
			payload.BodySha256 = sha256Hex(body)
		}
		size := originalBodySizeBytes
		payload.RawBodySizeBytes = &size
		payload.CaptureStatus = AuditCaptureHashOnly
		payload.Body = nil
		payload.HasBody = false
		payload.ContentEncoding = ""
		return true
	}
	if !options.Force && originalBodySizeBytes <= fullBodyLimitBytes {
		return false
	}
	originalContentType := payload.ContentType
	originalContentEncoding := payload.ContentEncoding
	originalSha256 := payload.BodySha256
	if originalSha256 == "" {
		originalSha256 = sha256Hex(body)
	}
	summary := buildAuditPayloadSummary(auditPayloadSummaryBuildInput{
		body:                   body,
		originalSha256:         originalSha256,
		originalBodySizeBytes:  originalBodySizeBytes,
		originalContentType:    originalContentType,
		originalContentEncoding: originalContentEncoding,
		fullBodyLimitBytes:     fullBodyLimitBytes,
		reason:                 summaryReasonOrDefault(options.Reason),
	})
	encoded, err := json.Marshal(summary)
	if err != nil {
		return false
	}
	payload.Body = encoded
	payload.HasBody = true
	payload.BodySha256 = originalSha256
	size := originalBodySizeBytes
	payload.RawBodySizeBytes = &size
	payload.CaptureStatus = AuditCaptureSummaryOnly
	payload.ContentType = AuditPayloadSummaryContentType
	payload.ContentEncoding = ""
	return true
}

func summaryReasonOrDefault(reason AuditPayloadSummaryReason) AuditPayloadSummaryReason {
	if reason != "" {
		return reason
	}
	return SummaryReasonBodyExceededLimit
}

// updateExistingPayloadSummaryLimit mirrors updateExistingPayloadSummaryLimit.
func updateExistingPayloadSummaryLimit(payload *AuditLogPayloadInput, fullBodyLimitBytes int) {
	if fullBodyLimitBytes == 0 && payload.CaptureStatus == AuditCaptureSummaryOnly {
		payload.CaptureStatus = AuditCaptureHashOnly
		payload.Body = nil
		payload.HasBody = false
		payload.ContentEncoding = ""
		return
	}
	if payload.CaptureStatus != AuditCaptureSummaryOnly || !payload.HasBody {
		return
	}
	var summary map[string]any
	if err := json.Unmarshal(payload.Body, &summary); err != nil {
		return
	}
	if summaryType, _ := summary["type"].(string); summaryType != "audit_payload_summary" {
		return
	}
	shrinkExistingPayloadSummary(summary, fullBodyLimitBytes)
	summary["fullBodyLimitBytes"] = fullBodyLimitBytes
	encoded, err := json.Marshal(summary)
	if err != nil {
		return
	}
	payload.Body = encoded
	payload.HasBody = true
}

// shrinkExistingPayloadSummary mirrors shrinkExistingPayloadSummary.
func shrinkExistingPayloadSummary(summary map[string]any, fullBodyLimitBytes int) {
	head := decodedSummaryWindow(summary["headBase64"])
	tail := decodedSummaryWindow(summary["tailBase64"])
	if head == nil || tail == nil {
		return
	}
	retainedBytes := head.length + tail.length
	if retainedBytes > fullBodyLimitBytes {
		retainedBytes = fullBodyLimitBytes
	}
	if retainedBytes < 0 {
		retainedBytes = 0
	}
	headBytes := head.length
	if headBytes > (retainedBytes+1)/2 {
		headBytes = (retainedBytes + 1) / 2
	}
	tailBytes := retainedBytes / 2
	if tailBytes > tail.length {
		tailBytes = tail.length
	}
	nextHead := head.data[:headBytes]
	nextTail := tail.data[tail.length-tailBytes:]
	originalSizeBytes := numericSummaryValue(summary["originalSizeBytes"])
	summary["retainedHeadBytes"] = len(nextHead)
	summary["retainedTailBytes"] = len(nextTail)
	omitted := originalSizeBytes - len(nextHead) - len(nextTail)
	if omitted < 0 {
		omitted = 0
	}
	summary["omittedMiddleBytes"] = omitted
	summary["headBase64"] = base64.StdEncoding.EncodeToString(nextHead)
	summary["tailBase64"] = base64.StdEncoding.EncodeToString(nextTail)
	if _, ok := summary["textPreview"].(map[string]any); ok {
		summary["textPreview"] = map[string]any{
			"head": textPreview(nextHead),
			"tail": textPreview(nextTail),
		}
	}
}

type summaryWindow struct {
	data   []byte
	length int
}

func decodedSummaryWindow(value any) *summaryWindow {
	text, ok := value.(string)
	if !ok {
		return nil
	}
	data, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return nil
	}
	return &summaryWindow{data: data, length: len(data)}
}

func numericSummaryValue(value any) int {
	number, ok := value.(float64)
	if !ok || number != number || number < 0 {
		return 0
	}
	return int(number)
}

type auditPayloadSummaryBuildInput struct {
	body                   []byte
	originalSha256         string
	originalBodySizeBytes  int
	originalContentType    string
	originalContentEncoding string
	fullBodyLimitBytes     int
	reason                 AuditPayloadSummaryReason
}

// buildAuditPayloadSummary mirrors buildAuditPayloadSummary. Returned as an
// OrderedObject so the persisted key order matches the Node literal.
func buildAuditPayloadSummary(input auditPayloadSummaryBuildInput) *OrderedObject {
	retainedBodyBytes := len(input.body)
	if retainedBodyBytes > AuditBodySummaryEdgeBytes*2 {
		retainedBodyBytes = AuditBodySummaryEdgeBytes * 2
	}
	if retainedBodyBytes > input.fullBodyLimitBytes {
		retainedBodyBytes = input.fullBodyLimitBytes
	}
	if retainedBodyBytes < 0 {
		retainedBodyBytes = 0
	}
	headBytes := (retainedBodyBytes + 1) / 2
	tailBytes := retainedBodyBytes / 2
	if tailBytes > len(input.body)-headBytes {
		tailBytes = len(input.body) - headBytes
	}
	if tailBytes < 0 {
		tailBytes = 0
	}
	head := input.body[:headBytes]
	tail := input.body[len(input.body)-tailBytes:]
	omitted := input.originalBodySizeBytes - retainedBodyBytes
	if omitted < 0 {
		omitted = 0
	}
	summary := NewOrderedObject()
	summary.Set("type", "audit_payload_summary")
	summary.Set("captureStatus", "summary_only")
	summary.Set("reason", string(input.reason))
	summary.Set("fullBodyLimitBytes", input.fullBodyLimitBytes)
	summary.Set("originalSha256", input.originalSha256)
	summary.Set("originalSizeBytes", input.originalBodySizeBytes)
	if input.originalContentType != "" {
		summary.Set("originalContentType", input.originalContentType)
	}
	if input.originalContentEncoding != "" {
		summary.Set("originalContentEncoding", input.originalContentEncoding)
	}
	summary.Set("retainedHeadBytes", len(head))
	summary.Set("retainedTailBytes", len(tail))
	summary.Set("omittedMiddleBytes", omitted)
	summary.Set("headBase64", base64.StdEncoding.EncodeToString(head))
	summary.Set("tailBase64", base64.StdEncoding.EncodeToString(tail))
	if isTextLikePayload(input.originalContentType, input.originalContentEncoding) {
		summary.Set("textPreview", map[string]any{
			"head": textPreview(head),
			"tail": textPreview(tail),
		})
	}
	return summary
}

// isTextLikePayload mirrors isTextLikePayload.
func isTextLikePayload(contentType string, contentEncoding string) bool {
	encoding := strings.ToLower(strings.TrimSpace(contentEncoding))
	if encoding != "" && encoding != "identity" {
		return false
	}
	normalizedType := strings.ToLower(contentType)
	return strings.Contains(normalizedType, "json") ||
		strings.Contains(normalizedType, "text") ||
		strings.Contains(normalizedType, "xml") ||
		strings.Contains(normalizedType, "event-stream") ||
		strings.Contains(normalizedType, "javascript") ||
		strings.Contains(normalizedType, "x-www-form-urlencoded")
}

// textPreview mirrors textPreview: up to 4 KiB of UTF-8 text.
func textPreview(data []byte) string {
	limit := len(data)
	if limit > auditBodySummaryTextPreviewBytes {
		limit = auditBodySummaryTextPreviewBytes
	}
	return string(data[:limit])
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
