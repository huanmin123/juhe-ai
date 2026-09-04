package gatewaybody

import "sync"

// Body pipeline limits, mirroring the exported constants of
// request/body.ts byte for byte.
const (
	GatewayJSONBodyLargeWarningBytes          = 2 * 1024 * 1024
	GatewayJSONBodyInlineParseMaxBytes        = 256 * 1024
	GatewayJSONBodyInlineMetadataScanMaxBytes = 256 * 1024
	DefaultGatewayTextRawBodyLimitMegabytes   = 16
	GatewayTextRawBodyLimitMegabytesMin       = 1
	GatewayTextRawBodyLimitMegabytesMax       = 64
	DefaultGatewayTextRawBodyLimitBytes       = DefaultGatewayTextRawBodyLimitMegabytes * 1024 * 1024
	GatewayTextRawBodyHardLimitBytes          = DefaultGatewayTextRawBodyLimitBytes
	GatewayTextRawBodyHardLimit               = "16mb"
	GatewayImageRawBodyHardLimitBytes         = 64 * 1024 * 1024
	GatewayImageRawBodyHardLimit              = "64mb"
	GatewayRawBodyHardLimitBytes              = GatewayImageRawBodyHardLimitBytes
	GatewayRawBodyHardLimit                   = GatewayImageRawBodyHardLimit
	DefaultGatewayBodyInFlightMaxBytes        = 256 * 1024 * 1024
)

// JSONParseStatus mirrors GatewayJsonBodyParseStatus. The values carry the
// exact Node wire spelling because they surface in log fields and usage
// snapshots.
type JSONParseStatus string

const (
	JSONParseStatusEmpty             JSONParseStatus = "empty"
	JSONParseStatusNotJSON           JSONParseStatus = "not_json"
	JSONParseStatusParsed            JSONParseStatus = "parsed"
	JSONParseStatusScannedJSON       JSONParseStatus = "scanned_json"
	JSONParseStatusDeferredLargeJSON JSONParseStatus = "deferred_large_json"
	JSONParseStatusInvalidJSON       JSONParseStatus = "invalid_json"
)

// BodyState mirrors GatewayRequestBodyState. Optional Node fields keep
// pointer types so the nullish fallback chain (state value ?? parsed body
// value) survives the migration byte for byte; ServiceTier is the one Node
// field that can never be undefined (normalizeUsageServiceTier defaults to
// 'default'), so it stays a plain string.
type BodyState struct {
	RawBodyBytes            int
	ContentType             string
	IsJSON                  bool
	JSONParseStatus         JSONParseStatus
	JSONParseWarningBytes   int
	Model                   *string
	Stream                  *bool
	ServiceTier             string
	ReasoningEffort         *string
	MaxOutputTokens         *int
	ResponseFormat          *string
	ImageGeneration         bool
	ImageGenerationForced   bool
	StrictOutputRequirement bool
	CodexCompactionTrigger  bool
}

// IsJSONContentType mirrors isGatewayJsonContentType: a content type is JSON
// when its lowercased text contains "json".
func IsJSONContentType(contentType string) bool {
	return containsFold(contentType, "json")
}

func containsFold(value, needle string) bool {
	n := len(value) - len(needle)
	for i := 0; i <= n; i++ {
		if asciiFoldEqual(value[i:i+len(needle)], needle) {
			return true
		}
	}
	return false
}

func asciiFoldEqual(a, b string) bool {
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// BodyStateInput mirrors the createGatewayRequestBodyState input. Pointer
// fields represent "provided", so nil keeps the Node `?? parsed fallback`
// semantics.
type BodyStateInput struct {
	RawBody                 []byte
	ContentType             string
	JSONParseStatus         JSONParseStatus
	ParsedBody              map[string]any
	Model                   *string
	Stream                  *bool
	ServiceTier             *string
	ReasoningEffort         *string
	MaxOutputTokens         *int
	ResponseFormat          *string
	ImageGeneration         *bool
	ImageGenerationForced   *bool
	StrictOutputRequirement *bool
	CodexCompactionTrigger  *bool
}

// CreateBodyState mirrors createGatewayRequestBodyState.
func CreateBodyState(input BodyStateInput) *BodyState {
	parsed := input.ParsedBody
	var imageInspection *ImageGenerationToolInspection
	if parsed != nil {
		inspection := InspectImageGenerationTools(parsed)
		imageInspection = &inspection
	}

	state := &BodyState{
		RawBodyBytes:          len(input.RawBody),
		ContentType:           input.ContentType,
		IsJSON:                IsJSONContentType(input.ContentType),
		JSONParseStatus:       input.JSONParseStatus,
		JSONParseWarningBytes: GatewayJSONBodyLargeWarningBytes,
	}
	if input.Model != nil {
		state.Model = input.Model
	} else if value, ok := parsedString(parsed, "model"); ok {
		state.Model = &value
	}
	if input.Stream != nil {
		state.Stream = input.Stream
	} else if value, ok := parsedBool(parsed, "stream"); ok {
		state.Stream = &value
	}
	if input.ServiceTier != nil {
		state.ServiceTier = *input.ServiceTier
	} else {
		state.ServiceTier = NormalizeUsageServiceTierValue(parsedValue(parsed, "service_tier"))
	}
	if input.ReasoningEffort != nil {
		state.ReasoningEffort = input.ReasoningEffort
	} else {
		state.ReasoningEffort = parsedReasoningEffort(parsed)
	}
	if input.MaxOutputTokens != nil {
		state.MaxOutputTokens = input.MaxOutputTokens
	} else {
		state.MaxOutputTokens = parsedMaxOutputTokens(parsed)
	}
	if input.ResponseFormat != nil {
		state.ResponseFormat = input.ResponseFormat
	} else if value, ok := normalizedResponseFormat(parsedValue(parsed, "response_format")); ok {
		state.ResponseFormat = &value
	}
	if input.ImageGeneration != nil {
		state.ImageGeneration = *input.ImageGeneration
	} else if imageInspection != nil {
		state.ImageGeneration = imageInspection.ImageToolCount > 0 || imageInspection.ForcedImageGeneration
	}
	if input.ImageGenerationForced != nil {
		state.ImageGenerationForced = *input.ImageGenerationForced
	} else if imageInspection != nil {
		state.ImageGenerationForced = imageInspection.ForcedImageGeneration
	}
	if input.StrictOutputRequirement != nil {
		state.StrictOutputRequirement = *input.StrictOutputRequirement
	} else {
		// Boolean(parsedBody?.response_format || parsedBody?.tools ||
		// parsedBody?.tool_choice): JavaScript truthiness of the values, not
		// mere presence (e.g. {"tools": null} stays false).
		state.StrictOutputRequirement = jsTruthy(parsedValue(parsed, "response_format")) ||
			jsTruthy(parsedValue(parsed, "tools")) ||
			jsTruthy(parsedValue(parsed, "tool_choice"))
	}
	if input.CodexCompactionTrigger != nil {
		state.CodexCompactionTrigger = *input.CodexCompactionTrigger
	}
	return state
}

func parsedValue(parsed map[string]any, key string) any {
	if parsed == nil {
		return nil
	}
	return parsed[key]
}

func parsedString(parsed map[string]any, key string) (value string, ok bool) {
	value, ok = parsed[key].(string)
	return value, ok
}

func parsedBool(parsed map[string]any, key string) (value bool, ok bool) {
	value, ok = parsed[key].(bool)
	return value, ok
}

// normalizedResponseFormat mirrors normalizedResponseFormat: trimmed strings
// are lowercased, everything else is undefined.
func normalizedResponseFormat(value any) (string, bool) {
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	trimmed := trimJSSpace(text)
	if trimmed == "" {
		return "", false
	}
	return asciiToLower(trimmed), true
}

// parsedReasoningEffort mirrors parsedReasoningEffort: reasoning.effort,
// then the reasoning_effort field, then output_config.effort.
func parsedReasoningEffort(parsed map[string]any) *string {
	var nested any
	if reasoning, ok := parsedObjectValue(parsed, "reasoning"); ok {
		nested = reasoning["effort"]
	}
	var outputConfig any
	if config, ok := parsedObjectValue(parsed, "output_config"); ok {
		outputConfig = config["effort"]
	}
	if effort, ok := NormalizeUsageReasoningEffortValue(nested); ok {
		return &effort
	}
	if effort, ok := NormalizeUsageReasoningEffortValue(parsedValue(parsed, "reasoning_effort")); ok {
		return &effort
	}
	if effort, ok := NormalizeUsageReasoningEffortValue(outputConfig); ok {
		return &effort
	}
	return nil
}

func parsedObjectValue(parsed map[string]any, key string) (map[string]any, bool) {
	object, ok := parsed[key].(map[string]any)
	return object, ok
}

// jsTruthy mirrors JavaScript Boolean() coercion for decoded JSON values.
func jsTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case float64:
		return typed != 0 && typed == typed
	case string:
		return typed != ""
	case map[string]any:
		return true
	case []any:
		return true
	default:
		return true
	}
}

// parsedMaxOutputTokens mirrors parsedMaxOutputTokens: the largest safe
// non-negative integer among max_output_tokens and max_tokens.
func parsedMaxOutputTokens(parsed map[string]any) *int {
	if parsed == nil {
		return nil
	}
	var best *int
	for _, key := range []string{"max_output_tokens", "max_tokens"} {
		if value, ok := safeNonNegativeInteger(parsed[key]); ok {
			if best == nil || value > *best {
				copied := value
				best = &copied
			}
		}
	}
	return best
}

func safeNonNegativeInteger(value any) (int, bool) {
	number, ok := value.(float64)
	if !ok {
		return 0, false
	}
	if number != number || number < 0 || number > maxSafeInteger || number != float64(int64(number)) {
		return 0, false
	}
	return int(number), true
}

const maxSafeInteger = float64(9007199254740991)

func asciiToLower(value string) string {
	out := []byte(value)
	for i, b := range out {
		if 'A' <= b && b <= 'Z' {
			out[i] = b + ('a' - 'A')
		}
	}
	return string(out)
}

// IsScannedJSONBody mirrors isGatewayScannedJsonBody.
func IsScannedJSONBody(req *Request) bool {
	if req == nil || req.State == nil {
		return false
	}
	return req.State.JSONParseStatus == JSONParseStatusScannedJSON ||
		req.State.JSONParseStatus == JSONParseStatusDeferredLargeJSON
}

// TextRawBodyLimitMegabytes mirrors gatewayRuntime settings resolution: a
// missing or out-of-range configured value falls back to the 16mb default.
type TextRawBodyLimitProvider func() (megabytes int, configured bool)

// GatewayTextRawBodyLimitBytes mirrors gatewayTextRawBodyLimitBytes.
func GatewayTextRawBodyLimitBytes(configuredMegabytes int, configured bool) int {
	if !configured {
		return DefaultGatewayTextRawBodyLimitBytes
	}
	if configuredMegabytes < GatewayTextRawBodyLimitMegabytesMin ||
		configuredMegabytes > GatewayTextRawBodyLimitMegabytesMax {
		return DefaultGatewayTextRawBodyLimitBytes
	}
	return configuredMegabytes * 1024 * 1024
}

// RawBodyLimitScope mirrors GatewayRawBodyLimitScope.
type RawBodyLimitScope string

const (
	RawBodyLimitScopeGateway RawBodyLimitScope = "gateway"
	RawBodyLimitScopeText    RawBodyLimitScope = "text"
	RawBodyLimitScopeImage   RawBodyLimitScope = "image"
)

// InFlightState mirrors GatewayBodyInFlightState.
type InFlightState struct {
	CurrentBytes  int
	RequestCount  int
	MaxBytes      int
	RejectedCount int
}

// InFlightLease mirrors GatewayBodyInFlightLease. Release is idempotent.
type InFlightLease struct {
	limiter  *InFlightLimiter
	bytes    int
	released bool
}

// Release mirrors lease.release.
func (lease *InFlightLease) Release() {
	if lease == nil || lease.released {
		return
	}
	lease.released = true
	lease.limiter.release(lease.bytes)
}

// InFlightLimiter holds the process-global in-flight body byte budget. Node
// keeps the counters in body.ts module scope; the Go gateway creates one
// limiter per process and shares it across middleware instances.
type InFlightLimiter struct {
	mu                 sync.Mutex
	currentBytes       int
	requestCount       int
	rejectedCount      int
	maxBytesForTest    int
	hasMaxBytesForTest bool
}

// NewInFlightLimiter creates the shared in-flight budget.
func NewInFlightLimiter() *InFlightLimiter {
	return &InFlightLimiter{}
}

// TryAcquire mirrors tryAcquireGatewayRequestBodyInFlightBytes. Zero-byte
// requests pass without a lease; a rejected request returns (nil, false).
func (l *InFlightLimiter) TryAcquire(rawBodyBytes int, configuredMaxBytes int) (*InFlightLease, bool) {
	bytes := normalizeInFlightBytes(rawBodyBytes)
	if bytes <= 0 {
		return nil, true
	}
	maxBytes := l.maxBytes(configuredMaxBytes)
	l.mu.Lock()
	defer l.mu.Unlock()
	if bytes > maxBytes || l.currentBytes+bytes > maxBytes {
		l.rejectedCount++
		return nil, false
	}
	l.currentBytes += bytes
	l.requestCount++
	return &InFlightLease{limiter: l, bytes: bytes}, true
}

func (l *InFlightLimiter) release(bytes int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.currentBytes = maxInt(0, l.currentBytes-bytes)
	l.requestCount = maxInt(0, l.requestCount-1)
}

// State mirrors getGatewayRequestBodyInFlightState.
func (l *InFlightLimiter) State(configuredMaxBytes int) InFlightState {
	l.mu.Lock()
	defer l.mu.Unlock()
	return InFlightState{
		CurrentBytes:  l.currentBytes,
		RequestCount:  l.requestCount,
		MaxBytes:      l.maxBytes(configuredMaxBytes),
		RejectedCount: l.rejectedCount,
	}
}

// SetMaxBytesForTest mirrors setGatewayRequestBodyInFlightMaxBytesForTest.
func (l *InFlightLimiter) SetMaxBytesForTest(value int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.maxBytesForTest = maxInt(1, value)
	l.hasMaxBytesForTest = true
}

// ClearMaxBytesForTest mirrors clearGatewayRequestBodyInFlightForTest.
func (l *InFlightLimiter) ClearMaxBytesForTest() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.maxBytesForTest = 0
	l.hasMaxBytesForTest = false
}

func (l *InFlightLimiter) maxBytes(configuredMaxBytes int) int {
	if l.hasMaxBytesForTest {
		return l.maxBytesForTest
	}
	normalized := normalizeInFlightBytes(configuredMaxBytes)
	if normalized > 0 {
		return normalized
	}
	return DefaultGatewayBodyInFlightMaxBytes
}

func normalizeInFlightBytes(value int) int {
	if value <= 0 {
		return 0
	}
	return value
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
