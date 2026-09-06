package gatewaydispatch

import (
	"compress/flate"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	sharedupstreamhttp "github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

// Upstream HTTP transport, migrated from upstream/request.ts over
// shared/platform/upstreamhttp. SSE / streaming semantics mirror the Node
// implementation: the response body streams chunk-by-chunk to the caller,
// decode-on-read for content-encoding, and the request/response timers only
// cover the phase before the first response header (Node clears
// requestTimeout / firstByteDeadlineTimer on response). Stream idle timeouts
// after headers are enforced by ReadStreamChunkWithIdleTimeout in the
// forwarding loop, exactly like the Node stream pipeline.

// GatewayUpstreamResponse mirrors GatewayUpstreamResponse. The Node
// AsyncIterable body + slot release becomes an io.ReadCloser whose Close
// releases the global concurrency slot (Node releases on end/error/close).
type GatewayUpstreamResponse struct {
	status int
	Header http.Header
	Body   io.ReadCloser
}

// Status mirrors response.status.
func (r *GatewayUpstreamResponse) Status() int { return r.status }

// OK mirrors response.ok.
func (r *GatewayUpstreamResponse) OK() bool { return r.status >= 200 && r.status < 300 }

// ContentType mirrors response.headers.get('content-type').
func (r *GatewayUpstreamResponse) ContentType() string {
	return r.Header.Get("Content-Type")
}

// UpstreamHeaderAccount mirrors UpstreamHeaderAccount.
type UpstreamHeaderAccount struct {
	ID                        string
	APIKey                    string
	Type                      string
	ProviderCode              string
	ProviderProtocolProfileID string
	ProtocolCode              string
	ProtocolVersion           string
	Credentials               map[string]any
}

// UpstreamRequestOptions mirrors UpstreamRequestOptions.
type UpstreamRequestOptions struct {
	Method string
	Header http.Header
	Body   []byte
	// ProxyURL mirrors options.proxyUrl ('' = direct).
	ProxyURL string
	// TimeoutMs mirrors options.timeoutMs. Node's request.setTimeout is a
	// socket idle timeout; the Go transport applies it to the header phase
	// (the stream idle phase is enforced by ReadStreamChunkWithIdleTimeout).
	TimeoutMs *int64
	// RequestTimeoutMs mirrors options.requestTimeoutMs.
	RequestTimeoutMs *int64
	// FirstByteDeadlineMs mirrors options.firstByteDeadlineMs.
	FirstByteDeadlineMs *int64
	// FirstByteDeadlineTransport mirrors options.firstByteDeadlineTransport
	// ('stream' | 'non_stream').
	FirstByteDeadlineTransport string
	// OnFirstByteDeadline mirrors options.onFirstByteDeadline.
	OnFirstByteDeadline FirstByteDeadlineHandler
	// DisableTimeouts mirrors options.disableTimeouts.
	DisableTimeouts bool
	// Signal mirrors options.signal.
	Signal context.Context
	// Transport mirrors options.transport ('fetch' marker); Go routes both
	// branches through the same pooled client, the field stays for contract
	// parity of the anthropic /messages allowlist.
	Transport string
}

// ConcurrencyGovernor mirrors shared/concurrency-governor.ts
// acquireGlobalConcurrencySlot: the release func must be called exactly once
// the response body is consumed.
type ConcurrencyGovernor interface {
	Acquire(ctx context.Context) (release func(), err error)
}

// NopConcurrencyGovernor is the unlimited default.
type NopConcurrencyGovernor struct{}

// Acquire implements ConcurrencyGovernor.
func (NopConcurrencyGovernor) Acquire(context.Context) (func(), error) { return func() {}, nil }

// UpstreamURLPolicy mirrors shared/upstream-url-policy.ts
// prepareSafeUpstreamRequestUrl. The Go default parses and normalizes the
// URL; SSRF guard enforcement belongs to the platform slice that owns DNS
// resolution policy (UnsafeResolvedUpstreamURLError carries the Node error
// contract).
type UpstreamURLPolicy interface {
	PrepareSafeUpstreamRequestURL(ctx context.Context, rawURL string) (*url.URL, error)
}

// PassthroughUpstreamURLPolicy is the default parse-only policy.
type PassthroughUpstreamURLPolicy struct{}

// PrepareSafeUpstreamRequestURL implements UpstreamURLPolicy.
func (PassthroughUpstreamURLPolicy) PrepareSafeUpstreamRequestURL(_ context.Context, rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	return parsed, nil
}

// TransportDeps carries the injected transport collaborators.
type TransportDeps struct {
	// Governor limits in-flight upstream requests; nil = unlimited.
	Governor ConcurrencyGovernor
	// URLPolicy prepares the safe request URL; nil = parse-only.
	URLPolicy UpstreamURLPolicy
	// ClientPool reuses pooled http clients per proxy; nil = shared client.
	ClientPool *sharedupstreamhttp.ClientPool
}

// RequestUpstream mirrors requestUpstream.
func RequestUpstream(ctx context.Context, upstreamURL string, options UpstreamRequestOptions, deps TransportDeps) (*GatewayUpstreamResponse, error) {
	governor := deps.Governor
	if governor == nil {
		governor = NopConcurrencyGovernor{}
	}
	releaseSlot, err := governor.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	urlPolicy := deps.URLPolicy
	if urlPolicy == nil {
		urlPolicy = PassthroughUpstreamURLPolicy{}
	}
	safeURL, err := urlPolicy.PrepareSafeUpstreamRequestURL(ctx, upstreamURL)
	if err != nil {
		releaseSlot()
		return nil, err
	}

	// Node: options.signal. Go defaults to the request ctx when absent.
	signal := options.Signal
	if signal == nil {
		signal = ctx
	}
	if signal.Err() != nil {
		releaseSlot()
		return nil, &UpstreamRequestAbortedError{Message: "请求已取消"}
	}

	// requestCtx mirrors the Node request handle: timers destroy it, the
	// signal aborts it, and response headers stop the timers. The cancel is
	// NOT deferred: on success the response body outlives this function
	// (streaming/large bodies read past the transport buffer), so the cancel
	// transfers to the body closer (Node destroys the request handle only at
	// response end, not at headers).
	requestCtx, requestCancel := context.WithCancel(signal)

	state := &upstreamRequestState{}

	headers := upstreamRequestHeaders(options.Header, options.Body)
	httpRequest, err := http.NewRequestWithContext(requestCtx, options.Method, safeURL.String(), bodyReader(options.Body))
	if err != nil {
		requestCancel()
		releaseSlot()
		return nil, err
	}
	for name, values := range headers {
		for _, value := range values {
			httpRequest.Header.Add(name, value)
		}
	}

	pool := deps.ClientPool
	var client *http.Client
	if pool != nil {
		client, err = pool.Client(options.ProxyURL, sharedupstreamhttp.TransportOptions{})
	} else {
		client, err = sharedupstreamhttp.SharedClient(options.ProxyURL, sharedupstreamhttp.TransportOptions{})
	}
	if err != nil {
		requestCancel()
		releaseSlot()
		return nil, err
	}

	if !options.DisableTimeouts {
		// requestTimeoutMs mirrors the Node timer cleared on response:
		// `上游请求 ${seconds}s 后仍未返回首个响应`.
		if options.RequestTimeoutMs != nil {
			seconds := int64CeilDiv(*options.RequestTimeoutMs, 1000)
			startRequestPhaseTimer(requestCtx, requestCancel, state, *options.RequestTimeoutMs, false, func() error {
				return &UpstreamRequestTimeoutError{
					Message: "上游请求 " + strconv.FormatInt(seconds, 10) + "s 后仍未返回首个响应",
				}
			}, nil)
		}
			// firstByteDeadlineMs mirrors the Node deadline timer: the handler
			// decides; only 'abort' destroys the request. A throwing handler
			// destroys the request with the locally-terminated handler error
			// instead (Node request.ts:266-282 .catch ->
			// normalizeFirstByteDeadlineHandlerError).
			if options.FirstByteDeadlineMs != nil {
				deadlineMs := *options.FirstByteDeadlineMs
				deadlineStartedAtMs := NowMs()
				startRequestPhaseTimer(requestCtx, requestCancel, state, deadlineMs, true, func() error {
					return &GatewayFirstByteTimeoutError{
						Message:   "上游请求 " + strconv.FormatInt(int64CeilDiv(deadlineMs, 1000), 10) + "s 后仍未返回首个响应",
						TimeoutMs: deadlineMs,
						Source:    FirstByteTimeoutSourceConfiguredDeadline,
					}
				}, func() error {
					action, handlerErr := runDeadlineHandler(options.OnFirstByteDeadline, FirstByteDeadlineDecisionInput{
						ElapsedMs: NowMs() - deadlineStartedAtMs,
						TimeoutMs: deadlineMs,
						Transport: firstNonEmpty(options.FirstByteDeadlineTransport, "non_stream"),
					})
					if handlerErr != nil {
						return handlerErr
					}
					if action != FirstByteDeadlineActionAbort {
						return nil // 'continue': the request keeps running
					}
					return &GatewayFirstByteTimeoutError{
						Message:   "上游请求 " + strconv.FormatInt(int64CeilDiv(deadlineMs, 1000), 10) + "s 后仍未返回首个响应",
						TimeoutMs: deadlineMs,
						Source:    FirstByteTimeoutSourceConfiguredDeadline,
					}
				})
			}
		// TimeoutMs applies to the header phase as well.
		if options.TimeoutMs != nil {
			startRequestPhaseTimer(requestCtx, requestCancel, state, *options.TimeoutMs, false, func() error {
				return &UpstreamRequestTimeoutError{Message: "上游请求超时"}
			}, nil)
		}
	}

	// Node sets upstreamRequestStarted = true immediately after
	// request.end(); every transport error afterwards is a "started" error.
	upstreamStarted := true
	response, err := client.Do(httpRequest)
	if err != nil {
		requestCancel()
		releaseSlot()
		if signal.Err() != nil {
			return nil, &UpstreamRequestAbortedError{Message: "请求已取消", UpstreamRequestStarted: true}
		}
		if timerErr := state.timerError(); timerErr != nil {
			if timerErr.locallyTerminated {
				// Locally terminated: never marked started (Node
				// markLocallyTerminatedUpstreamRequestError).
				return nil, timerErr.err
			}
			// A request/socket timer destroyed an in-flight request: Node
			// marks it started (upstreamRequestStarted = true before end()).
			return nil, &StartedTransportError{Err: timerErr.err}
		}
		normalized := normalizeTransportError(err)
		if upstreamStarted {
			return nil, &StartedTransportError{Err: normalized}
		}
		return nil, normalized
	}
	state.markResponseReceived()

	body := response.Body
	if body == nil {
		body = io.NopCloser(strings.NewReader(""))
	}
	decoded, decodeErr := decodeUpstreamResponseBody(body, response.Header.Get("Content-Encoding"))
	if decodeErr != nil {
		_ = body.Close()
		requestCancel()
		releaseSlot()
		return nil, decodeErr
	}
	// Node keeps the request handle alive for the whole response stream and
	// destroys it at response end: the cancel moves into the body closer. The
	// response-body idle watch starts here, mirroring the Node socket timer
	// that stays armed after the response headers arrive.
	bodyIdleWatch := time.Duration(0)
	if !options.DisableTimeouts {
		watchMs := int64(120_000) // Node: options.timeoutMs ?? 120000
		if options.TimeoutMs != nil {
			watchMs = *options.TimeoutMs
		}
		bodyIdleWatch = time.Duration(maxInt64(1, watchMs)) * time.Millisecond
	}
	upstreamBody := newSlotReleasingBodyWithCancel(decoded, releaseSlot, requestCancel, bodyIdleWatch)
	upstreamBody.armIdleWatch()
	return &GatewayUpstreamResponse{
		status: response.StatusCode,
		Header: response.Header,
		Body:   upstreamBody,
	}, nil
}

// upstreamRequestState is the per-request mirror of the Node closure flags
// (settled / responseReceived / firstByteDeadlineTimer).
type upstreamRequestState struct {
	mu               sync.Mutex
	responseReceived bool
	timerErr         *upstreamTimerError
}

// upstreamTimerError carries the phase-timer failure and whether it is a
// local policy termination (first-byte deadline abort) or a transport-class
// destruction (request/socket timeout).
type upstreamTimerError struct {
	err               error
	locallyTerminated bool
}

func (s *upstreamRequestState) markResponseReceived() {
	s.mu.Lock()
	s.responseReceived = true
	s.mu.Unlock()
}

func (s *upstreamRequestState) isResponseReceived() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.responseReceived
}

func (s *upstreamRequestState) setTimerError(err error, locallyTerminated bool) {
	s.mu.Lock()
	if s.timerErr == nil {
		s.timerErr = &upstreamTimerError{err: err, locallyTerminated: locallyTerminated}
	}
	s.mu.Unlock()
}

func (s *upstreamRequestState) timerError() *upstreamTimerError {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.timerErr
}

// startRequestPhaseTimer mirrors the Node setTimeout handlers that destroy
// the request before the first response header. abortOverride, when
// non-nil, lets the first-byte deadline handler keep the request alive on
// 'continue'.
func startRequestPhaseTimer(
	requestCtx context.Context,
	requestCancel func(),
	state *upstreamRequestState,
	delayMs int64,
	locallyTerminated bool,
	createError func() error,
	abortOverride func() error,
) {
	timer := time.AfterFunc(time.Duration(maxInt64(1, delayMs))*time.Millisecond, func() {
		if state.isResponseReceived() {
			return
		}
		failure := createError()
		if abortOverride != nil {
			decided := abortOverride()
			if decided == nil {
				return // handler said 'continue'
			}
			failure = decided
		}
		state.setTimerError(failure, locallyTerminated)
		requestCancel()
	})
	go func() {
		<-requestCtx.Done()
		timer.Stop()
	}()
}

// normalizeTransportError wraps raw dial/read failures the way Node's
// request.destroy(new Error('上游请求超时')) produced them.
func normalizeTransportError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &UpstreamRequestTimeoutError{Message: "上游请求超时"}
	}
	if timeoutLikeText(err.Error()) {
		return &UpstreamRequestTimeoutError{Message: "上游请求超时"}
	}
	return err
}

func bodyReader(body []byte) io.Reader {
	if len(body) == 0 {
		return nil
	}
	return strings.NewReader(string(body))
}

func upstreamRequestHeaders(headers http.Header, body []byte) http.Header {
	output := http.Header{}
	for name, values := range headers {
		for _, value := range values {
			output.Add(name, value)
		}
	}
	if body != nil && output.Get("Content-Length") == "" {
		output.Set("Content-Length", strconv.FormatInt(int64(len(body)), 10))
	}
	return output
}

// decodeUpstreamResponseBody mirrors decodeUpstreamResponseBody: br / gzip /
// x-gzip / deflate / x-deflate / identity; anything else fails the response.
// The archived Node decoder creates a brotli stream for 'br'
// (request.ts createUpstreamResponseDecoder -> createBrotliDecompress).
func decodeUpstreamResponseBody(body io.ReadCloser, contentEncoding string) (io.ReadCloser, error) {
	encodings := parseContentEncodings(contentEncoding)
	if len(encodings) == 0 || allIdentity(encodings) {
		return body, nil
	}
	stream := io.ReadCloser(body)
	for i := len(encodings) - 1; i >= 0; i-- {
		encoding := encodings[i]
		if encoding == "identity" {
			continue
		}
		switch encoding {
		case "br":
			stream = io.NopCloser(brotli.NewReader(stream))
		case "gzip", "x-gzip":
			decoder, err := gzip.NewReader(stream)
			if err != nil {
				return nil, &StartedBodyTransportError{Err: err}
			}
			stream = decoder
		case "deflate", "x-deflate":
			stream = io.NopCloser(flate.NewReader(stream))
		default:
			_ = body.Close()
			return nil, &UnsupportedUpstreamResponseEncodingError{
				Message: "不支持的上游响应压缩编码: " + encoding,
			}
		}
	}
	return stream, nil
}

func parseContentEncodings(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.ToLower(strings.TrimSpace(part))
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func allIdentity(encodings []string) bool {
	for _, encoding := range encodings {
		if encoding != "identity" {
			return false
		}
	}
	return true
}

// slotReleasingBody releases the concurrency slot exactly once when the body
// is closed or fully drained (Node: message.once('end'/'error'/'close')).
//
// It also carries the response-body phase of the Node request.setTimeout
// socket idle watch (request.ts:285-287, `request.setTimeout(options.timeoutMs
// ?? 120000, abort)`): the Node socket timer stays armed after the response
// headers arrive and destroys the request when the upstream goes silent
// mid-body. Go enforces the header phase with the startRequestPhaseTimer above
// and this per-Read idle watch; once the idle budget elapses the upstream
// request context is cancelled, so a pending Read fails instead of blocking
// forever (Node: request.destroy(new Error('上游请求超时'))).
type slotReleasingBody struct {
	reader   io.ReadCloser
	released atomic.Bool
	release  func()
	cancel   func()

	// idleTimeout is the per-Read idle budget (0 disables the watch; the
	// caller derives it from TimeoutMs with the Node 120000 default).
	idleTimeout time.Duration

	mu          sync.Mutex
	idleTimer   *time.Timer
	idleExpired bool
}

// newSlotReleasingBodyWithCancel additionally cancels the upstream request
// context once the body is released (EOF or Close). Node destroys the request
// handle at response end; without this the context would leak for the whole
// body stream lifetime. idleTimeout > 0 arms the response-body idle watch.
func newSlotReleasingBodyWithCancel(reader io.ReadCloser, release func(), cancel func(), idleTimeout time.Duration) *slotReleasingBody {
	return &slotReleasingBody{reader: reader, release: release, cancel: cancel, idleTimeout: idleTimeout}
}

func (b *slotReleasingBody) Read(p []byte) (int, error) {
	if b.isIdleExpired() {
		return 0, &UpstreamRequestTimeoutError{Message: "上游请求超时"}
	}
	b.armIdleWatch()
	n, err := b.reader.Read(p)
	if err == io.EOF {
		// Fully drained: Node destroys the request handle at response end.
		b.stopIdleWatch()
		b.cancelOnce()
		b.releaseOnce()
	}
	return n, err
}

func (b *slotReleasingBody) Close() error {
	b.stopIdleWatch()
	b.cancelOnce()
	b.releaseOnce()
	return b.reader.Close()
}

func (b *slotReleasingBody) releaseOnce() {
	if b.released.CompareAndSwap(false, true) && b.release != nil {
		b.release()
	}
}

func (b *slotReleasingBody) cancelOnce() {
	if b.cancel != nil {
		b.cancel()
	}
}

func (b *slotReleasingBody) isIdleExpired() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.idleExpired
}

// armIdleWatch (re)starts the idle countdown before every Read; the timer
// fires only when no read activity happens for the whole budget.
func (b *slotReleasingBody) armIdleWatch() {
	if b.cancel == nil || b.idleTimeout <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.idleExpired {
		return
	}
	if b.idleTimer == nil {
		b.idleTimer = time.AfterFunc(b.idleTimeout, func() {
			b.mu.Lock()
			b.idleExpired = true
			b.mu.Unlock()
			// Node: abort = () => request.destroy(new Error('上游请求超时')).
			b.cancel()
		})
		return
	}
	b.idleTimer.Reset(b.idleTimeout)
}

func (b *slotReleasingBody) stopIdleWatch() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.idleTimer != nil {
		b.idleTimer.Stop()
	}
}

// ---------------------------------------------------------------------------
// Timeout profile helpers
// ---------------------------------------------------------------------------

// UpstreamSocketTimeoutMs mirrors upstreamSocketTimeoutMs.
func UpstreamSocketTimeoutMs(req *gatewaypreauth.GatewayRequest, profile gatewayrouting.GatewayTimeoutProfile, account *UpstreamHeaderAccount) *int64 {
	if profile.TimeoutsDisabled {
		return nil
	}
	isStreamRequest := IsEffectiveOpenAIStreamRequest(req, account)
	var value int64
	if !isStreamRequest {
		value = maxInt64(profile.FirstResponseTimeoutMs, 30_000)
	} else {
		value = maxInt64(maxInt64(profile.FirstResponseTimeoutMs, profile.IdleTimeoutMs+15_000), 30_000)
	}
	return &value
}

// UpstreamRequestTimeoutMs mirrors upstreamRequestTimeoutMs.
func UpstreamRequestTimeoutMs(profile gatewayrouting.GatewayTimeoutProfile) *int64 {
	if profile.TimeoutsDisabled {
		return nil
	}
	value := profile.FirstResponseTimeoutMs
	return &value
}

// IsEffectiveOpenAIStreamRequest mirrors isEffectiveOpenAIStreamRequest.
func IsEffectiveOpenAIStreamRequest(req *gatewaypreauth.GatewayRequest, account *UpstreamHeaderAccount) bool {
	if usesOpenAIOAuthCompactStreamRules(account) {
		return !IsOpenAIOAuthCodexCompactRequest(req)
	}
	return gatewaypreauth.RequestStream(req)
}

func usesOpenAIOAuthCompactStreamRules(account *UpstreamHeaderAccount) bool {
	if account == nil || account.Type != "oauth" {
		return false
	}
	if account.ProtocolCode == "" && account.ProtocolVersion == "" && account.ProviderProtocolProfileID == "" {
		return true
	}
	return IsGptVendorCode(account.ProviderCode) && isOpenAIProtocolProfileWith(account.ProtocolCode, account.ProtocolVersion)
}

// ---------------------------------------------------------------------------
// Header building
// ---------------------------------------------------------------------------

// BuildUpstreamHeaders mirrors buildUpstreamHeaders.
func BuildUpstreamHeaders(inputHeaders http.Header, account UpstreamHeaderAccount) http.Header {
	headers := CopySafeUpstreamRequestHeaders(inputHeaders, CopySafeUpstreamHeadersOptions{
		PreserveOpenAIOAuthCodexClientHeaders: usesOpenAIOAuthCompactStreamRules(&account) &&
			IsOpenAICodexClientHeaders(inputHeaders),
	})
	headers.Set("Authorization", "Bearer "+account.APIKey)
	if usesOpenAIOAuthCompactStreamRules(&account) {
		ApplyOpenAICodexHeaders(headers, account)
	}
	return headers
}

// CopySafeUpstreamHeadersOptions mirrors the options bag.
type CopySafeUpstreamHeadersOptions struct {
	PreserveOpenAIOAuthCodexClientHeaders bool
}

// CopySafeUpstreamRequestHeaders mirrors copySafeUpstreamRequestHeaders.
func CopySafeUpstreamRequestHeaders(inputHeaders http.Header, options CopySafeUpstreamHeadersOptions) http.Header {
	headers := http.Header{}
	if inputHeaders == nil {
		return headers
	}
	for name, values := range inputHeaders {
		if len(values) == 0 {
			continue
		}
		lowerName := strings.ToLower(name)
		if options.PreserveOpenAIOAuthCodexClientHeaders {
			if _, allowlisted := openAIOAuthCodexAllowlistedHeaders[lowerName]; allowlisted {
				headers[name] = append([]string(nil), values...)
				continue
			}
		}
		if shouldSkipUpstreamRequestHeader(lowerName) {
			continue
		}
		headers[name] = append([]string(nil), values...)
	}
	return headers
}

// gatewayClientProfileHeader mirrors gatewayClientProfileHeader from
// client-profiles/strategy.ts: the internal client profile marker header.
const gatewayClientProfileHeader = "x-juhe-ai-client-profile"

var skippedUpstreamRequestHeaders = map[string]struct{}{
	"host": {}, "authorization": {}, "content-length": {}, "connection": {},
	"keep-alive": {}, "proxy-authenticate": {}, "proxy-authorization": {},
	"te": {}, "trailer": {}, "transfer-encoding": {}, "upgrade": {},
	"expect": {}, "content-encoding": {}, "accept-encoding": {},
	"cookie": {}, "set-cookie": {}, "openai-api-key": {}, "x-api-key": {},
	"anthropic-api-key": {}, "x-goog-api-key": {}, "api-key": {},
	"chatgpt-account-id": {}, "x-oai-attestation": {}, "openai-organization": {},
	"openai-project": {}, gatewayClientProfileHeader: {}, "x-request-id": {},
	"traceparent": {}, "tracestate": {}, "baggage": {}, "x-amzn-trace-id": {},
	"x-cloud-trace-context": {}, "x-forwarded-for": {}, "x-forwarded-host": {},
	"x-forwarded-port": {}, "x-forwarded-proto": {}, "x-forwarded-server": {},
	"x-real-ip": {}, "forwarded": {}, "via": {}, "cf-connecting-ip": {},
}

var skippedUpstreamRequestHeaderPrefixes = []string{
	"x-forwarded-", "x-openai-", "x-stainless-", "x-vercel-",
}

var openAIOAuthCodexAllowlistedHeaders = map[string]struct{}{
	"x-oai-attestation":            {},
	"x-openai-subagent":            {},
	OpenAICodexResponsesLiteHeader: {},
}

func shouldSkipUpstreamRequestHeader(lowerName string) bool {
	if _, skipped := skippedUpstreamRequestHeaders[lowerName]; skipped {
		return true
	}
	for _, prefix := range skippedUpstreamRequestHeaderPrefixes {
		if strings.HasPrefix(lowerName, prefix) {
			return true
		}
	}
	return false
}

// ApplyOpenAICodexHeaders mirrors applyOpenAICodexHeaders.
func ApplyOpenAICodexHeaders(headers http.Header, account UpstreamHeaderAccount) {
	if !IsOpenAICodexClientHeaders(headers) {
		if headers.Get("Accept") == "" {
			headers.Set("Accept", "text/event-stream")
		}
		if headers.Get("Content-Type") == "" {
			headers.Set("Content-Type", "application/json")
		}
		NormalizeOpenAICodexClientHeaders(headers, "")
	}
	accountID := stringCredential(account.Credentials, "account_id")
	if accountID != "" && headers.Get("Chatgpt-Account-Id") == "" {
		headers.Set("Chatgpt-Account-Id", accountID)
	}
}

func stringCredential(credentials map[string]any, key string) string {
	value, ok := credentials[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return trimString(text)
}

// CopyResponseHeaders mirrors copyResponseHeaders: hop-by-hop and gateway
// headers are dropped, connection-token headers are dropped dynamically.
func CopyResponseHeaders(upstreamResponse *GatewayUpstreamResponse, setHeader func(name, value string)) {
	dynamicSkippedHeaders := parseConnectionHeaderTokens(upstreamResponse.Header.Get("Connection"))
	for name, values := range upstreamResponse.Header {
		if len(values) == 0 {
			continue
		}
		if shouldSkipUpstreamResponseHeader(name, dynamicSkippedHeaders) {
			continue
		}
		for _, value := range values {
			setHeader(name, value)
		}
	}
}

var skippedUpstreamResponseHeaders = map[string]struct{}{
	"connection": {}, "content-encoding": {}, "content-length": {}, "keep-alive": {},
	"proxy-authenticate": {}, "proxy-authorization": {}, "set-cookie": {},
	"te": {}, "trailer": {}, "transfer-encoding": {}, "upgrade": {},
}

var skippedUpstreamResponseHeaderPrefixes = []string{
	"cf-aig-", "helicone-", "x-bt-", "x-kong-", "x-litellm-", "x-portkey-",
}

func shouldSkipUpstreamResponseHeader(name string, dynamicSkippedHeaders map[string]struct{}) bool {
	lowerName := strings.ToLower(name)
	if _, skipped := skippedUpstreamResponseHeaders[lowerName]; skipped {
		return true
	}
	if dynamicSkippedHeaders != nil {
		if _, skipped := dynamicSkippedHeaders[lowerName]; skipped {
			return true
		}
	}
	for _, prefix := range skippedUpstreamResponseHeaderPrefixes {
		if strings.HasPrefix(lowerName, prefix) {
			return true
		}
	}
	return false
}

func parseConnectionHeaderTokens(value string) map[string]struct{} {
	if value == "" {
		return nil
	}
	tokens := strings.Split(value, ",")
	set := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		trimmed := strings.ToLower(strings.TrimSpace(token))
		if trimmed != "" {
			set[trimmed] = struct{}{}
		}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

// ---------------------------------------------------------------------------
// Stream chunk reads
// ---------------------------------------------------------------------------

// ReadStreamChunkWithIdleTimeout mirrors readStreamChunkWithIdleTimeout.
func ReadStreamChunkWithIdleTimeout(ctx context.Context, reader io.Reader, buffer []byte, timeoutSeconds int64) (int, error) {
	return readStreamChunkWithTimeout(ctx, reader, buffer, &timeoutSeconds, func() error {
		return &UpstreamRequestTimeoutError{
			Message: "上游流 " + strconv.FormatInt(timeoutSeconds, 10) + "s 无数据，已超时",
		}
	})
}

// ReadStreamChunkWithAbort mirrors readStreamChunkWithAbort.
func ReadStreamChunkWithAbort(ctx context.Context, reader io.Reader, buffer []byte) (int, error) {
	return readStreamChunkWithTimeout(ctx, reader, buffer, nil, func() error { return nil })
}

func readStreamChunkWithTimeout(ctx context.Context, reader io.Reader, buffer []byte, timeoutSeconds *int64, createError func() error) (int, error) {
	type readResult struct {
		n   int
		err error
	}
	signal := ctx
	if signal == nil {
		signal = context.Background()
	}
	if signal.Err() != nil {
		return 0, &UpstreamRequestAbortedError{Message: "请求已取消", UpstreamRequestStarted: true}
	}
	readDone := make(chan readResult, 1)
	go func() {
		n, err := reader.Read(buffer)
		readDone <- readResult{n: n, err: err}
	}()
	var timeout <-chan time.Time
	if timeoutSeconds != nil {
		timer := time.NewTimer(time.Duration(*timeoutSeconds) * time.Second)
		defer timer.Stop()
		timeout = timer.C
	}
	select {
	case result := <-readDone:
		return result.n, result.err
	case <-timeout:
		return 0, createError()
	case <-signal.Done():
		return 0, &UpstreamRequestAbortedError{Message: "请求已取消", UpstreamRequestStarted: true}
	}
}

// IsUpstreamRequestAbortedError mirrors isUpstreamRequestAbortedError.
func IsUpstreamRequestAbortedError(err error) bool {
	var aborted *UpstreamRequestAbortedError
	return errors.As(err, &aborted)
}
