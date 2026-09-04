package gatewayclientip

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// Constants mirror client-ip-error-circuit.service.ts verbatim.
const (
	preAuthMaxEntries               = 20_000
	clientIPErrorCircuitMaxEntries  = 10_000
	preAuthWindowMs                 = 60_000
	preAuthMissingThreshold         = 40
	preAuthInvalidTokenThreshold    = 8
	preAuthInvalidTokenSprayThreshold = 120
	preAuthInitialBlockMs           = 30_000
	preAuthMaxBlockMs               = 5 * 60_000

	clientIPSignatureWindowMs   = 30_000
	clientIPTotalWindowMs       = 60_000
	clientIPSignatureThreshold  = 5
	clientIPTotalThreshold      = 20
	clientIPInitialBlockMs      = 30_000
	clientIPMaxBlockMs          = 10 * 60_000
	maxSignaturesPerScope       = 20

	// circuitStateStoreName mirrors createRuntimeStateStore('gateway-client-ip-error-circuit').
	circuitStateStoreName = "gateway-client-ip-error-circuit"
)

// Pre-auth failure reasons mirror GatewayPreAuthFailureReason plus the spray
// pseudo reason.
const (
	preAuthReasonMissingBearerToken   = "missing_bearer_token"
	preAuthReasonInvalidAPIKey        = "invalid_api_key"
	preAuthReasonInvalidTokenSpray    = "invalid_api_key_spray"
)

// Error circuit reasons mirror GatewayClientIpErrorCircuitReason.
const (
	circuitReasonInvalidJSON              = "invalid_json"
	circuitReasonAdapterRequestValidation = "adapter_request_validation"
)

// preAuthEntry mirrors PreAuthEntry.
type preAuthEntry struct {
	Key            string   `json:"key"`
	Samples        []int64  `json:"samples"`
	BlockCount     int      `json:"blockCount"`
	BlockedUntilMs *int64   `json:"blockedUntilMs,omitempty"`
	LastReason     *string  `json:"lastReason,omitempty"`
}

// signatureSample mirrors the Array<[string, number[]]> signature tuple.
type signatureSample struct {
	Signature string
	Samples   []int64
}

// MarshalJSON emits the Node tuple shape ["signature",[ms...]].
func (s signatureSample) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{s.Signature, s.Samples})
}

// UnmarshalJSON reads the Node tuple shape.
func (s *signatureSample) UnmarshalJSON(raw []byte) error {
	var decoded [2]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	if err := json.Unmarshal(decoded[0], &s.Signature); err != nil {
		return err
	}
	s.Samples = []int64{}
	return json.Unmarshal(decoded[1], &s.Samples)
}

// clientIPErrorEntry mirrors ClientIpErrorEntry.
type clientIPErrorEntry struct {
	Key            string            `json:"key"`
	Samples        []int64           `json:"samples"`
	Signatures     []signatureSample `json:"signatures"`
	BlockCount     int               `json:"blockCount"`
	BlockedUntilMs *int64            `json:"blockedUntilMs,omitempty"`
	LastReason     *string           `json:"lastReason,omitempty"`
}

// ErrorCircuitOptions configures the circuit family.
type ErrorCircuitOptions struct {
	Clock Clock
	// RuntimeStateDriver mirrors runtimeConfig.runtimeStateDriver.
	RuntimeStateDriver string
	// StateRedisURL mirrors runtimeConfig.redis.stateUrl (redis driver).
	StateRedisURL string
	// RedisNamespace mirrors runtimeConfig.redis.namespace (redis driver).
	RedisNamespace string
	// StateStore overrides the constructed Redis store (tests).
	StateStore      RuntimeStateStore
	StateStoreClose func()
}

// ErrorCircuit owns the pre-auth circuit and the client-IP error circuit.
// It satisfies the G05 gatewaypreauth.PreAuthCircuits port.
type ErrorCircuit struct {
	clock    Clock
	useRedis bool
	store    RuntimeStateStore
	closeFn  func()

	preAuth     *orderedExpiryMap[preAuthEntry]
	clientIPErr *orderedExpiryMap[clientIPErrorEntry]
}

// NewErrorCircuit builds the circuit family.
func NewErrorCircuit(opts ErrorCircuitOptions) (*ErrorCircuit, error) {
	clock := opts.Clock
	if clock == nil {
		clock = systemClock()
	}
	useRedis := opts.RuntimeStateDriver == RuntimeStateDriverRedis
	store := opts.StateStore
	closeFn := opts.StateStoreClose
	if useRedis && store == nil {
		constructed, constructedClose, err := NewRedisRuntimeStateStore(opts.StateRedisURL, opts.RedisNamespace, circuitStateStoreName)
		if err != nil {
			return nil, err
		}
		store = constructed
		closeFn = constructedClose
	}
	return &ErrorCircuit{
		clock:       clock,
		useRedis:    useRedis,
		store:       store,
		closeFn:     closeFn,
		preAuth:     newOrderedExpiryMap[preAuthEntry](clock, preAuthMaxEntries),
		clientIPErr: newOrderedExpiryMap[clientIPErrorEntry](clock, clientIPErrorCircuitMaxEntries),
	}, nil
}

// Close disposes the Redis state store when this instance owns one.
func (c *ErrorCircuit) Close() {
	if c.closeFn != nil {
		c.closeFn()
	}
}

// ---------------------------------------------------------------------------
// pre-auth circuit
// ---------------------------------------------------------------------------

// InspectGatewayPreAuthCircuit mirrors inspectGatewayPreAuthCircuit (memory).
func (c *ErrorCircuit) InspectGatewayPreAuthCircuit(input gatewaypreauth.PreAuthCircuitInput) gatewaypreauth.CircuitDecision {
	specificKey, ok := c.preAuthSpecificKey(input)
	if !ok {
		return c.circuitDecisionNotBlocked(nil)
	}
	entry, _ := c.preAuth.Get(specificKey)
	return c.entryDecisionPreAuth(&entry, ok)
}

// InspectPreAuthCircuit mirrors inspectGatewayPreAuthCircuitAsync and is the
// G05 port method.
func (c *ErrorCircuit) InspectPreAuthCircuit(ctx context.Context, input gatewaypreauth.PreAuthCircuitInput) (gatewaypreauth.CircuitDecision, error) {
	if !c.useRedis {
		return c.InspectGatewayPreAuthCircuit(input), nil
	}
	specificKey, ok := c.preAuthSpecificKey(input)
	if !ok {
		return c.circuitDecisionNotBlocked(nil), nil
	}
	entry, found, err := getRuntimeEntry[preAuthEntry](ctx, c.store, specificKey)
	if err != nil {
		return gatewaypreauth.CircuitDecision{}, err
	}
	return c.entryDecisionPreAuth(&entry, found), nil
}

// RecordGatewayPreAuthFailure mirrors recordGatewayPreAuthFailure (memory).
func (c *ErrorCircuit) RecordGatewayPreAuthFailure(input gatewaypreauth.PreAuthFailureInput) gatewaypreauth.CircuitDecision {
	specificKey, ok := c.preAuthSpecificKey(gatewaypreauth.PreAuthCircuitInput{
		ClientIP:      input.ClientIP,
		Authorization: input.Authorization,
	})
	if !ok {
		return c.circuitDecisionNotBlocked(nil)
	}
	clientIP := strings.TrimSpace(input.ClientIP)
	var sprayKey string
	if input.Reason == gatewaypreauth.PreAuthFailureInvalidAPIKey && clientIP != "" {
		sprayKey = preAuthSprayKey(clientIP)
	}
	if sprayKey != "" {
		entry, found := c.preAuth.Get(sprayKey)
		activeSprayDecision := c.entryDecisionPreAuth(&entry, found)
		if activeSprayDecision.Blocked {
			return activeSprayDecision
		}
	}
	now := c.clock.Now().UnixMilli()
	threshold := int64(preAuthMissingThreshold)
	if input.Reason == gatewaypreauth.PreAuthFailureInvalidAPIKey {
		threshold = preAuthInvalidTokenThreshold
	}
	specificDecision := c.recordPreAuthEntry(specificKey, string(input.Reason), threshold, now)
	if specificDecision.Blocked {
		return specificDecision
	}
	if sprayKey == "" {
		return specificDecision
	}
	sprayDecision := c.recordPreAuthEntry(sprayKey, preAuthReasonInvalidTokenSpray, preAuthInvalidTokenSprayThreshold, now)
	if sprayDecision.Blocked {
		return sprayDecision
	}
	return specificDecision
}

// RecordPreAuthFailure mirrors recordGatewayPreAuthFailureAsync and is the
// G05 port method.
func (c *ErrorCircuit) RecordPreAuthFailure(ctx context.Context, input gatewaypreauth.PreAuthFailureInput) (gatewaypreauth.CircuitDecision, error) {
	if !c.useRedis {
		return c.RecordGatewayPreAuthFailure(input), nil
	}
	specificKey, ok := c.preAuthSpecificKey(gatewaypreauth.PreAuthCircuitInput{
		ClientIP:      input.ClientIP,
		Authorization: input.Authorization,
	})
	if !ok {
		return c.circuitDecisionNotBlocked(nil), nil
	}
	clientIP := strings.TrimSpace(input.ClientIP)
	var sprayKey string
	if input.Reason == gatewaypreauth.PreAuthFailureInvalidAPIKey && clientIP != "" {
		sprayKey = preAuthSprayKey(clientIP)
	}
	if sprayKey != "" {
		entry, found, err := getRuntimeEntry[preAuthEntry](ctx, c.store, sprayKey)
		if err != nil {
			return gatewaypreauth.CircuitDecision{}, err
		}
		activeSprayDecision := c.entryDecisionPreAuth(&entry, found)
		if activeSprayDecision.Blocked {
			return activeSprayDecision, nil
		}
	}
	now := c.clock.Now().UnixMilli()
	threshold := int64(preAuthMissingThreshold)
	if input.Reason == gatewaypreauth.PreAuthFailureInvalidAPIKey {
		threshold = preAuthInvalidTokenThreshold
	}
	specificDecision, err := c.recordPreAuthEntryAsync(ctx, specificKey, string(input.Reason), threshold, now)
	if err != nil {
		return gatewaypreauth.CircuitDecision{}, err
	}
	if specificDecision.Blocked {
		return specificDecision, nil
	}
	if sprayKey == "" {
		return specificDecision, nil
	}
	sprayDecision, err := c.recordPreAuthEntryAsync(ctx, sprayKey, preAuthReasonInvalidTokenSpray, preAuthInvalidTokenSprayThreshold, now)
	if err != nil {
		return gatewaypreauth.CircuitDecision{}, err
	}
	if sprayDecision.Blocked {
		return sprayDecision, nil
	}
	return specificDecision, nil
}

// recordPreAuthEntry mirrors recordPreAuthEntry (memory).
func (c *ErrorCircuit) recordPreAuthEntry(key string, reason string, threshold int64, now int64) gatewaypreauth.CircuitDecision {
	entry, found := c.preAuth.Get(key)
	if !found {
		entry = preAuthEntry{Key: key, Samples: []int64{}}
	}
	activeDecision := c.entryDecisionPreAuth(&entry, true)
	if activeDecision.Blocked {
		return activeDecision
	}
	entry.Samples = appendSample(entry.Samples, now, preAuthWindowMs, threshold)
	lastReason := reason
	entry.LastReason = &lastReason
	if int64(len(entry.Samples)) >= threshold {
		openBlockEntry(&entry.BlockCount, &entry.BlockedUntilMs, now, preAuthInitialBlockMs, preAuthMaxBlockMs)
	}
	c.preAuth.Set(key, entry, preAuthMaxBlockMs+preAuthWindowMs)
	return c.entryDecisionPreAuth(&entry, true)
}

// recordPreAuthEntryAsync mirrors recordPreAuthEntryAsync (redis).
func (c *ErrorCircuit) recordPreAuthEntryAsync(ctx context.Context, key string, reason string, threshold int64, now int64) (gatewaypreauth.CircuitDecision, error) {
	entry, found, err := getRuntimeEntry[preAuthEntry](ctx, c.store, key)
	if err != nil {
		return gatewaypreauth.CircuitDecision{}, err
	}
	if !found {
		entry = preAuthEntry{Key: key, Samples: []int64{}}
	}
	activeDecision := c.entryDecisionPreAuth(&entry, true)
	if activeDecision.Blocked {
		return activeDecision, nil
	}
	entry.Samples = appendSample(entry.Samples, now, preAuthWindowMs, threshold)
	lastReason := reason
	entry.LastReason = &lastReason
	if int64(len(entry.Samples)) >= threshold {
		openBlockEntry(&entry.BlockCount, &entry.BlockedUntilMs, now, preAuthInitialBlockMs, preAuthMaxBlockMs)
	}
	if err := setRuntimeEntry(ctx, c.store, key, entry, preAuthMaxBlockMs+preAuthWindowMs); err != nil {
		return gatewaypreauth.CircuitDecision{}, err
	}
	return c.entryDecisionPreAuth(&entry, true), nil
}

// ---------------------------------------------------------------------------
// client-IP error circuit
// ---------------------------------------------------------------------------

// InspectClientIPErrorCircuitSync mirrors inspectClientIpErrorCircuit
// (memory) and reports whether the scope is currently blocked.
func (c *ErrorCircuit) InspectClientIPErrorCircuitSync(input gatewaypreauth.ClientIPErrorCircuitInput) gatewaypreauth.CircuitDecision {
	key, ok := clientIPErrorScopeKey(input)
	if !ok {
		return c.circuitDecisionNotBlocked(nil)
	}
	entry, found := c.clientIPErr.Get(key)
	return c.entryDecisionClientIPError(&entry, found)
}

// InspectClientIPErrorCircuit mirrors inspectClientIpErrorCircuitAsync and
// is the G05 port method.
func (c *ErrorCircuit) InspectClientIPErrorCircuit(ctx context.Context, input gatewaypreauth.ClientIPErrorCircuitInput) (gatewaypreauth.CircuitDecision, error) {
	if !c.useRedis {
		return c.InspectClientIPErrorCircuitSync(input), nil
	}
	key, ok := clientIPErrorScopeKey(input)
	if !ok {
		return c.circuitDecisionNotBlocked(nil), nil
	}
	entry, found, err := getRuntimeEntry[clientIPErrorEntry](ctx, c.store, key)
	if err != nil {
		return gatewaypreauth.CircuitDecision{}, err
	}
	return c.entryDecisionClientIPError(&entry, found), nil
}

// RecordClientIPErrorCircuitSampleSync mirrors recordClientIpErrorCircuitSample
// (memory).
func (c *ErrorCircuit) RecordClientIPErrorCircuitSampleSync(input gatewaypreauth.ClientIPErrorCircuitSampleInput) gatewaypreauth.CircuitDecision {
	key, ok := clientIPErrorScopeKey(sampleScope(input))
	if !ok {
		return c.circuitDecisionNotBlocked(nil)
	}
	entry, found := c.clientIPErr.Get(key)
	if !found {
		entry = clientIPErrorEntry{Key: key, Samples: []int64{}, Signatures: []signatureSample{}}
	}
	activeDecision := c.entryDecisionClientIPError(&entry, true)
	if activeDecision.Blocked {
		return activeDecision
	}
	now := c.clock.Now().UnixMilli()
	entry.Samples = appendSample(entry.Samples, now, clientIPTotalWindowMs, clientIPTotalThreshold)
	signature := sampleSignature(input)
	signatureCount := upsertSignatureSample(&entry, signature, now)
	lastReason := string(input.Reason)
	entry.LastReason = &lastReason

	shouldBlock := signatureCount >= clientIPSignatureThreshold || int64(len(entry.Samples)) >= clientIPTotalThreshold
	if shouldBlock {
		openBlockEntry(&entry.BlockCount, &entry.BlockedUntilMs, now, clientIPInitialBlockMs, clientIPMaxBlockMs)
	}
	c.clientIPErr.Set(key, entry, clientIPMaxBlockMs+clientIPTotalWindowMs)
	return c.entryDecisionClientIPError(&entry, true)
}

// RecordClientIPErrorCircuitSample mirrors recordClientIpErrorCircuitSampleAsync
// and is the G05 port method.
func (c *ErrorCircuit) RecordClientIPErrorCircuitSample(ctx context.Context, input gatewaypreauth.ClientIPErrorCircuitSampleInput) (gatewaypreauth.CircuitDecision, error) {
	if !c.useRedis {
		return c.RecordClientIPErrorCircuitSampleSync(input), nil
	}
	key, ok := clientIPErrorScopeKey(sampleScope(input))
	if !ok {
		return c.circuitDecisionNotBlocked(nil), nil
	}
	entry, found, err := getRuntimeEntry[clientIPErrorEntry](ctx, c.store, key)
	if err != nil {
		return gatewaypreauth.CircuitDecision{}, err
	}
	if !found {
		entry = clientIPErrorEntry{Key: key, Samples: []int64{}, Signatures: []signatureSample{}}
	}
	activeDecision := c.entryDecisionClientIPError(&entry, true)
	if activeDecision.Blocked {
		return activeDecision, nil
	}
	now := c.clock.Now().UnixMilli()
	entry.Samples = appendSample(entry.Samples, now, clientIPTotalWindowMs, clientIPTotalThreshold)
	signature := sampleSignature(input)
	signatureCount := upsertSignatureSample(&entry, signature, now)
	lastReason := string(input.Reason)
	entry.LastReason = &lastReason

	shouldBlock := signatureCount >= clientIPSignatureThreshold || int64(len(entry.Samples)) >= clientIPTotalThreshold
	if shouldBlock {
		openBlockEntry(&entry.BlockCount, &entry.BlockedUntilMs, now, clientIPInitialBlockMs, clientIPMaxBlockMs)
	}
	if err := setRuntimeEntry(ctx, c.store, key, entry, clientIPMaxBlockMs+clientIPTotalWindowMs); err != nil {
		return gatewaypreauth.CircuitDecision{}, err
	}
	return c.entryDecisionClientIPError(&entry, true), nil
}

// RecordClientIPErrorCircuitSuccessSync mirrors recordClientIpErrorCircuitSuccess
// (memory) and reports whether an entry existed.
func (c *ErrorCircuit) RecordClientIPErrorCircuitSuccessSync(input gatewaypreauth.ClientIPErrorCircuitInput) bool {
	key, ok := clientIPErrorScopeKey(input)
	if !ok {
		return false
	}
	_, existed := c.clientIPErr.Get(key)
	c.clientIPErr.Delete(key)
	return existed
}

// RecordClientIPErrorCircuitSuccess mirrors recordClientIpErrorCircuitSuccessAsync
// and is the G05 port method (the Node boolean return is dropped by the
// orchestration).
func (c *ErrorCircuit) RecordClientIPErrorCircuitSuccess(ctx context.Context, input gatewaypreauth.ClientIPErrorCircuitInput) error {
	if !c.useRedis {
		c.RecordClientIPErrorCircuitSuccessSync(input)
		return nil
	}
	key, ok := clientIPErrorScopeKey(input)
	if !ok {
		return nil
	}
	if _, _, err := getRuntimeEntry[clientIPErrorEntry](ctx, c.store, key); err != nil {
		return err
	}
	return c.store.Delete(ctx, runtimeEntryKey(key))
}

// ---------------------------------------------------------------------------
// test snapshots
// ---------------------------------------------------------------------------

// ClearGatewayClientIPErrorCircuitForTest mirrors
// clearGatewayClientIpErrorCircuitForTest.
func (c *ErrorCircuit) ClearGatewayClientIPErrorCircuitForTest() {
	c.preAuth = newOrderedExpiryMap[preAuthEntry](c.clock, preAuthMaxEntries)
	c.clientIPErr = newOrderedExpiryMap[clientIPErrorEntry](c.clock, clientIPErrorCircuitMaxEntries)
}

// CircuitSnapshotRow mirrors one getGatewayClientIpSecuritySnapshotForTest row.
type CircuitSnapshotRow struct {
	Key          string
	FailureCount int
	Blocked      bool
	LastReason   string
}

// SecuritySnapshotForTest mirrors getGatewayClientIpSecuritySnapshotForTest.
func (c *ErrorCircuit) SecuritySnapshotForTest() (preAuth []CircuitSnapshotRow, clientIPErrors []CircuitSnapshotRow) {
	for _, entry := range c.preAuth.Values() {
		decision := c.entryDecisionPreAuth(&entry, true)
		lastReason := ""
		if entry.LastReason != nil {
			lastReason = *entry.LastReason
		}
		preAuth = append(preAuth, CircuitSnapshotRow{
			Key:          entry.Key,
			FailureCount: len(entry.Samples),
			Blocked:      decision.Blocked,
			LastReason:   lastReason,
		})
	}
	for _, entry := range c.clientIPErr.Values() {
		decision := c.entryDecisionClientIPError(&entry, true)
		lastReason := ""
		if entry.LastReason != nil {
			lastReason = *entry.LastReason
		}
		clientIPErrors = append(clientIPErrors, CircuitSnapshotRow{
			Key:          entry.Key,
			FailureCount: len(entry.Samples),
			Blocked:      decision.Blocked,
			LastReason:   lastReason,
		})
	}
	return preAuth, clientIPErrors
}

// ---------------------------------------------------------------------------
// entries + decisions
// ---------------------------------------------------------------------------

// openBlockEntry mirrors openBlock: exponential 30s * 2^min(count,4) capped,
// skipped while the previous block is still active.
func openBlockEntry(blockCount *int, blockedUntilMs **int64, now int64, initialBlockMs int64, maxBlockMs int64) {
	if *blockedUntilMs != nil && **blockedUntilMs > now {
		return
	}
	exponent := *blockCount
	if exponent > 4 {
		exponent = 4
	}
	blockMs := initialBlockMs << uint(exponent)
	if blockMs > maxBlockMs {
		blockMs = maxBlockMs
	}
	*blockCount += 1
	blockedUntil := now + blockMs
	*blockedUntilMs = &blockedUntil
}

// entryDecisionPreAuth mirrors entryDecision for the pre-auth entry.
func (c *ErrorCircuit) entryDecisionPreAuth(entry *preAuthEntry, found bool) gatewaypreauth.CircuitDecision {
	if !found || entry == nil || entry.BlockedUntilMs == nil {
		return c.circuitDecisionNotBlocked(entrySamplesCount(entry, found))
	}
	return c.blockedCircuitDecision(entry.BlockedUntilMs, entry.LastReason, len(entry.Samples))
}

// entryDecisionClientIPError mirrors entryDecision for the error entry.
func (c *ErrorCircuit) entryDecisionClientIPError(entry *clientIPErrorEntry, found bool) gatewaypreauth.CircuitDecision {
	if !found || entry == nil || entry.BlockedUntilMs == nil {
		return c.circuitDecisionNotBlocked(entrySamplesCountClientIP(entry, found))
	}
	return c.blockedCircuitDecision(entry.BlockedUntilMs, entry.LastReason, len(entry.Samples))
}

func entrySamplesCount(entry *preAuthEntry, found bool) *int64 {
	if !found || entry == nil {
		return nil
	}
	count := int64(len(entry.Samples))
	return &count
}

func entrySamplesCountClientIP(entry *clientIPErrorEntry, found bool) *int64 {
	if !found || entry == nil {
		return nil
	}
	count := int64(len(entry.Samples))
	return &count
}

// circuitDecisionNotBlocked mirrors { blocked: false, failureCount? }.
func (c *ErrorCircuit) circuitDecisionNotBlocked(failureCount *int64) gatewaypreauth.CircuitDecision {
	return gatewaypreauth.CircuitDecision{FailureCount: failureCount}
}

// blockedCircuitDecision mirrors the blocked branch of entryDecision.
func (c *ErrorCircuit) blockedCircuitDecision(blockedUntilMs *int64, lastReason *string, sampleCount int) gatewaypreauth.CircuitDecision {
	now := c.clock.Now().UnixMilli()
	if *blockedUntilMs <= now {
		count := int64(sampleCount)
		return gatewaypreauth.CircuitDecision{FailureCount: &count}
	}
	retryAfterSeconds := (*blockedUntilMs - now + 999) / 1000
	if retryAfterSeconds < 1 {
		retryAfterSeconds = 1
	}
	reason := ""
	if lastReason != nil {
		reason = *lastReason
	}
	count := int64(sampleCount)
	return gatewaypreauth.CircuitDecision{
		Blocked:           true,
		Reason:            reason,
		RetryAfterSeconds: &retryAfterSeconds,
		BlockedUntilMs:    blockedUntilMs,
		FailureCount:      &count,
	}
}

// ---------------------------------------------------------------------------
// keys, signatures and samples
// ---------------------------------------------------------------------------

// preAuthSpecificKey mirrors preAuthSpecificKey; ok=false mirrors undefined.
func (c *ErrorCircuit) preAuthSpecificKey(input gatewaypreauth.PreAuthCircuitInput) (string, bool) {
	clientIP := strings.TrimSpace(input.ClientIP)
	if clientIP == "" {
		return "", false
	}
	token := bearerToken(input.Authorization)
	if token == "" {
		return "preauth:" + clientIP + ":missing", true
	}
	return "preauth:" + clientIP + ":token:" + tokenFingerprint(token), true
}

// preAuthSprayKey mirrors preAuthSprayKey.
func preAuthSprayKey(clientIP string) string {
	return "preauth:" + strings.TrimSpace(clientIP) + ":invalid-token-spray"
}

// clientIPErrorScopeKey mirrors clientIpErrorScopeKey; ok=false mirrors
// undefined. The JSON key matches Node JSON.stringify byte for byte so the
// Redis state keys stay shared during coexistence.
func clientIPErrorScopeKey(input gatewaypreauth.ClientIPErrorCircuitInput) (string, bool) {
	clientIP := strings.TrimSpace(input.ClientIP)
	if clientIP == "" {
		return "", false
	}
	apiKeyID := strings.TrimSpace(input.APIKeyID)
	if apiKeyID == "" {
		apiKeyID = "internal"
	}
	return jsonScopeKey(input.SystemAccountID, apiKeyID, clientIP), true
}

func sampleScope(input gatewaypreauth.ClientIPErrorCircuitSampleInput) gatewaypreauth.ClientIPErrorCircuitInput {
	return gatewaypreauth.ClientIPErrorCircuitInput{
		SystemAccountID: input.SystemAccountID,
		GroupID:         input.GroupID,
		APIKeyID:        input.APIKeyID,
		ClientIP:        input.ClientIP,
		Endpoint:        input.Endpoint,
	}
}

// jsonScopeKey serializes {systemAccountId, apiKeyId, clientIp} like
// JSON.stringify (compact, no HTML escaping).
func jsonScopeKey(systemAccountID, apiKeyID, clientIP string) string {
	var buffer strings.Builder
	buffer.WriteString(`{"systemAccountId":`)
	writeJSONStringCompact(&buffer, systemAccountID)
	buffer.WriteString(`,"apiKeyId":`)
	writeJSONStringCompact(&buffer, apiKeyID)
	buffer.WriteString(`,"clientIp":`)
	writeJSONStringCompact(&buffer, clientIP)
	buffer.WriteString(`}`)
	return buffer.String()
}

func writeJSONStringCompact(builder *strings.Builder, value string) {
	encoded, err := json.Marshal(value)
	if err != nil {
		builder.WriteString(`""`)
		return
	}
	// json.Marshal escapes <, > and & as \u003c… which JSON.stringify does
	// not; undo those three to stay byte-compatible.
	encoded = undoHTMLJSONEscapes(encoded)
	builder.Write(encoded)
}

var htmlEscapePairs = []struct{ from, to string }{
	{`\u003c`, `<`},
	{`\u003e`, `>`},
	{`\u0026`, `&`},
	{`\u2028`, `\u2028`},
	{`\u2029`, `\u2029`},
}

func undoHTMLJSONEscapes(value []byte) []byte {
	text := string(value)
	// Go also emits \u003c etc. only for the HTML trio; the line separators
	// are identical in both encoders, so they pass through untouched.
	for _, pair := range htmlEscapePairs[:3] {
		text = strings.ReplaceAll(text, pair.from, pair.to)
	}
	return []byte(text)
}

// sampleSignature mirrors sampleSignature.
func sampleSignature(input gatewaypreauth.ClientIPErrorCircuitSampleInput) string {
	signature := input.Signature
	if signature == "" {
		signature = string(input.Reason)
	}
	return strings.Join([]string{
		normalizeSignaturePart(input.Endpoint),
		string(input.Reason),
		normalizeSignaturePart(signature),
	}, "|")
}

// upsertSignatureSample mirrors upsertSignatureSample.
func upsertSignatureSample(entry *clientIPErrorEntry, signature string, now int64) int64 {
	pruneSignatureSamples(entry, now)
	for i := range entry.Signatures {
		if entry.Signatures[i].Signature == signature {
			entry.Signatures[i].Samples = appendSample(entry.Signatures[i].Samples, now, clientIPSignatureWindowMs, clientIPSignatureThreshold)
			return int64(len(entry.Signatures[i].Samples))
		}
	}
	entry.Signatures = append(entry.Signatures, signatureSample{Signature: signature, Samples: []int64{now}})
	if len(entry.Signatures) > maxSignaturesPerScope {
		entry.Signatures = entry.Signatures[len(entry.Signatures)-maxSignaturesPerScope:]
	}
	return 1
}

// pruneSignatureSamples mirrors pruneSignatureSamples.
func pruneSignatureSamples(entry *clientIPErrorEntry, now int64) {
	kept := entry.Signatures[:0]
	for _, item := range entry.Signatures {
		item.Samples = pruneSamplesInPlace(item.Samples, now, clientIPSignatureWindowMs)
		if len(item.Samples) > 0 {
			kept = append(kept, item)
		}
	}
	entry.Signatures = kept
}

// appendSample mirrors appendSample.
func appendSample(samples []int64, now int64, windowMs int64, maxSamples int64) []int64 {
	samples = pruneSamplesInPlace(samples, now, windowMs)
	samples = append(samples, now)
	if int64(len(samples)) > maxSamples {
		samples = samples[int64(len(samples))-maxSamples:]
	}
	return samples
}

// pruneSamplesInPlace mirrors pruneSamplesInPlace.
func pruneSamplesInPlace(samples []int64, now int64, windowMs int64) []int64 {
	kept := samples[:0]
	for _, sample := range samples {
		if now-sample <= windowMs {
			kept = append(kept, sample)
		}
	}
	return kept
}

// bearerToken mirrors bearerToken.
func bearerToken(authorization string) string {
	if authorization == "" {
		return ""
	}
	match := bearerTokenPattern.FindStringSubmatch(authorization)
	if match == nil {
		return ""
	}
	return strings.TrimSpace(match[1])
}

var bearerTokenPattern = regexp.MustCompile(`(?i)^Bearer\s+(.+)$`)

// tokenFingerprint mirrors tokenFingerprint: sha256 hex, first 16 chars.
func tokenFingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])[:16]
}

// normalizeSignaturePart mirrors normalizeSignaturePart.
func normalizeSignaturePart(value string) string {
	normalized := strings.TrimSpace(value)
	normalized = whitespacePattern.ReplaceAllString(normalized, " ")
	normalized = strings.ToLower(normalized)
	if len(normalized) > 240 {
		normalized = normalized[:240]
	}
	return normalized
}

var whitespacePattern = regexp.MustCompile(`\s+`)

// ---------------------------------------------------------------------------
// redis runtime entries
// ---------------------------------------------------------------------------

func runtimeEntryKey(key string) string {
	return "entry:" + key
}

func getRuntimeEntry[T any](ctx context.Context, store RuntimeStateStore, key string) (T, bool, error) {
	var entry T
	found, err := store.GetJSON(ctx, runtimeEntryKey(key), &entry)
	if err != nil || !found {
		return entry, false, err
	}
	return entry, true, nil
}

func setRuntimeEntry[T any](ctx context.Context, store RuntimeStateStore, key string, entry T, ttlMs int64) error {
	return store.SetJSON(ctx, runtimeEntryKey(key), entry, ttlMs)
}

// Compile-time G05 port assertion for G20 assembly.
var _ gatewaypreauth.PreAuthCircuits = (*ErrorCircuit)(nil)
