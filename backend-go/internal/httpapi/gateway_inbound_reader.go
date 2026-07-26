package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"unicode/utf8"

	"juhe-ai/backend-go/internal/modules/gatewayrequestprep"
)

const (
	// gatewayResponsesInboundDefaultRawBodyHardLimitBytes matches the Node raw
	// ingress hard cap, rather than its later text-lane limit. /v1/responses may
	// be upgraded to an image/tool lane only after bounded body facts exist, so
	// applying the 16 MiB text limit at this raw boundary would reject valid
	// requests before that later owner can make the lane decision.
	gatewayResponsesInboundDefaultRawBodyHardLimitBytes int64 = 64 << 20
	gatewayInboundJSONMaxDepth                                = 64
	gatewayInboundJSONMaxTokens                               = 1 << 20
	// Keep duplicate-key tracking well below the raw-body ceiling. Unknown
	// object keys must be retained only while that one object is scanned, but
	// accepting 65K bounded keys would still turn a 64 MiB raw request into a
	// materially larger transient allocation. 4K fields is far beyond the
	// supported request shape and makes the scanner's peak auxiliary memory
	// predictably small under hostile input.
	gatewayInboundJSONMaxObjectFields      = 1 << 12
	gatewayInboundModelMaxBytes            = 512
	gatewayInboundJSONMaxKeyBytes          = 512
	gatewayInboundJSONMaxKeyLiteralBytes   = 4 << 10
	gatewayInboundJSONMaxModelLiteralBytes = 4 << 10
	gatewayInboundCodexMetadataMaxBytes    = 4 << 10
)

var (
	ErrGatewayResponsesInboundRequestRequired = errors.New("gateway responses inbound request is required")
	ErrGatewayResponsesInboundMethod          = errors.New("gateway responses inbound method must be POST")
	ErrGatewayResponsesInboundPath            = errors.New("gateway responses inbound path must be canonical POST /v1/responses")
	ErrGatewayResponsesInboundBodyTooLarge    = errors.New("gateway responses inbound body exceeds limit")
	ErrGatewayResponsesInboundJSON            = errors.New("gateway responses inbound JSON is invalid")
)

// GatewayResponsesInboundMetadata is the small, value-only portion of a
// request that the listener needs before authentication and route planning.
// Model and Stream retain Node-compatible optional-field semantics: Model is
// empty and Stream is false when their JSON properties are absent. If present,
// they must respectively be a bounded non-blank string and a boolean.
type GatewayResponsesInboundMetadata struct {
	Model                  string
	Stream                 bool
	CodexTurnMetadataValid bool
}

// GatewayResponsesInboundReader configures an unregistered raw ingress
// boundary. RawBodyHardLimitBytes may lower the raw hard cap for a deliberately
// isolated caller or test; zero, negative, and values above the production
// hard cap resolve to the Node-compatible 64 MiB default. This boundary does
// not apply a text-lane limit: model/image/tool and route decisions belong to
// later bounded metadata, preflight, and admission owners.
type GatewayResponsesInboundReader struct {
	RawBodyHardLimitBytes int64
}

// GatewayResponsesInbound is a copy-safe result of one bounded request-body
// read. It owns its byte slice; RawBody always returns a new copy, and its
// preparation result cannot be synthesized outside gatewayrequestprep.
//
// This is deliberately not an http.Handler and does not authenticate,
// authorize, select an account, contact storage, dispatch upstream, or write
// an HTTP response. In particular, constructing this value is not gateway
// owner evidence.
type GatewayResponsesInbound struct {
	rawBody     []byte
	metadata    GatewayResponsesInboundMetadata
	preparation gatewayrequestprep.Result
}

func (v GatewayResponsesInbound) RawBody() []byte {
	return bytes.Clone(v.rawBody)
}

func (v GatewayResponsesInbound) Metadata() GatewayResponsesInboundMetadata {
	return v.metadata
}

func (v GatewayResponsesInbound) Preparation() gatewayrequestprep.Result {
	return v.preparation
}

// ReadGatewayResponsesInbound is the unregistered HTTP boundary for the
// future exact POST /v1/responses listener. It performs a Content-Length
// preflight and independently enforces the same bound while consuming a
// chunked or dishonest body. It closes the original request body exactly once
// before returning and never replaces it with a replayable reader.
func ReadGatewayResponsesInbound(request *http.Request) (result GatewayResponsesInbound, err error) {
	return (GatewayResponsesInboundReader{}).Read(request)
}

// Read applies the configured raw ingress hard cap. It is intentionally not
// registered as an HTTP handler.
func (reader GatewayResponsesInboundReader) Read(request *http.Request) (result GatewayResponsesInbound, err error) {
	if request == nil {
		return GatewayResponsesInbound{}, ErrGatewayResponsesInboundRequestRequired
	}
	var closeBody func() error
	var stopCloseOnCancel func() bool
	if request.Body != nil {
		var closeOnce sync.Once
		var closeErr error
		closeBody = func() error {
			closeOnce.Do(func() { closeErr = request.Body.Close() })
			return closeErr
		}
		stopCloseOnCancel = context.AfterFunc(request.Context(), func() { _ = closeBody() })
		defer func() {
			if stopCloseOnCancel != nil {
				stopCloseOnCancel()
			}
			if closeErr := closeBody(); closeErr != nil && err == nil {
				err = fmt.Errorf("close gateway responses inbound body: %w", closeErr)
			}
		}()
	}
	if request.Method != http.MethodPost {
		return GatewayResponsesInbound{}, ErrGatewayResponsesInboundMethod
	}
	if !isCanonicalGatewayResponsesPath(request) {
		return GatewayResponsesInbound{}, ErrGatewayResponsesInboundPath
	}
	if contextErr := request.Context().Err(); contextErr != nil {
		return GatewayResponsesInbound{}, fmt.Errorf("gateway responses inbound context: %w", contextErr)
	}
	hardLimit := reader.rawBodyHardLimitBytes()
	if request.ContentLength > hardLimit {
		return GatewayResponsesInbound{}, ErrGatewayResponsesInboundBodyTooLarge
	}
	if request.ContentLength < -1 {
		return GatewayResponsesInbound{}, fmt.Errorf("%w: invalid Content-Length", ErrGatewayResponsesInboundJSON)
	}

	rawBody, err := readGatewayResponsesInboundBody(request.Context(), request.Body, hardLimit)
	if err != nil {
		return GatewayResponsesInbound{}, err
	}
	metadata, err := parseGatewayResponsesInboundMetadata(request.Context(), rawBody)
	if err != nil {
		return GatewayResponsesInbound{}, err
	}
	metadata.CodexTurnMetadataValid = parseGatewayCodexTurnMetadata(request.Context(), request.Header.Values("X-Codex-Turn-Metadata"))
	if contextErr := request.Context().Err(); contextErr != nil {
		return GatewayResponsesInbound{}, fmt.Errorf("gateway responses inbound context: %w", contextErr)
	}
	preparation, err := gatewayrequestprep.PrepareHTTPRequest(request, gatewayrequestprep.HTTPFacts{
		StreamRequested:        metadata.Stream,
		CodexTurnMetadataValid: metadata.CodexTurnMetadataValid,
	})
	if err != nil {
		return GatewayResponsesInbound{}, fmt.Errorf("prepare gateway responses inbound request: %w", err)
	}
	return GatewayResponsesInbound{
		// io.ReadAll allocated this slice for this reader invocation. The result
		// takes ownership directly; only RawBody copies on egress.
		rawBody:     rawBody,
		metadata:    metadata,
		preparation: preparation,
	}, nil
}

func (reader GatewayResponsesInboundReader) rawBodyHardLimitBytes() int64 {
	if reader.RawBodyHardLimitBytes > 0 && reader.RawBodyHardLimitBytes <= gatewayResponsesInboundDefaultRawBodyHardLimitBytes {
		return reader.RawBodyHardLimitBytes
	}
	return gatewayResponsesInboundDefaultRawBodyHardLimitBytes
}

func isCanonicalGatewayResponsesPath(request *http.Request) bool {
	if request == nil || request.RequestURI != "/v1/responses" {
		return false
	}
	url := request.URL
	if url == nil || url.Scheme != "" || url.Host != "" || url.User != nil || url.Opaque != "" || url.Path != "/v1/responses" || url.RawQuery != "" || url.ForceQuery || url.Fragment != "" || url.RawFragment != "" {
		return false
	}
	// A non-empty RawPath is accepted only when it is the exact canonical
	// spelling. Percent-encoded, repeated, and alternate slash paths must not
	// enter a future listener as equivalent routes.
	return (url.RawPath == "" || url.RawPath == "/v1/responses") && url.EscapedPath() == "/v1/responses"
}

func readGatewayResponsesInboundBody(ctx context.Context, body io.Reader, hardLimit int64) ([]byte, error) {
	if body == nil {
		return nil, fmt.Errorf("%w: missing body", ErrGatewayResponsesInboundJSON)
	}
	limited := io.LimitReader(contextBoundReader{ctx: ctx, reader: body}, hardLimit+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, fmt.Errorf("gateway responses inbound context: %w", contextErr)
		}
		return nil, fmt.Errorf("read gateway responses inbound body: %w", err)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, fmt.Errorf("gateway responses inbound context: %w", contextErr)
	}
	if int64(len(raw)) > hardLimit {
		return nil, ErrGatewayResponsesInboundBodyTooLarge
	}
	return raw, nil
}

type contextBoundReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextBoundReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if contextErr := r.ctx.Err(); contextErr != nil {
		return n, contextErr
	}
	return n, err
}

func parseGatewayResponsesInboundMetadata(ctx context.Context, raw []byte) (GatewayResponsesInboundMetadata, error) {
	metadata := GatewayResponsesInboundMetadata{}
	err := scanBoundedJSONObject(ctx, raw, func(key string, scanner *boundedJSONScanner, depth int) (bool, error) {
		switch key {
		case "model":
			model, err := scanner.boundedString(gatewayInboundModelMaxBytes, gatewayInboundJSONMaxModelLiteralBytes)
			if err != nil || strings.TrimSpace(model) == "" || !utf8.ValidString(model) {
				return true, errors.New("model must be a bounded non-blank string")
			}
			metadata.Model = model
			return true, nil
		case "stream":
			stream, err := scanner.boolean()
			if err != nil {
				return true, errors.New("stream must be a boolean")
			}
			metadata.Stream = stream
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return GatewayResponsesInboundMetadata{}, fmt.Errorf("gateway responses inbound context: %w", contextErr)
		}
		return GatewayResponsesInboundMetadata{}, fmt.Errorf("%w: %v", ErrGatewayResponsesInboundJSON, err)
	}
	return metadata, nil
}

// parseGatewayCodexTurnMetadata exposes no header content. It recognizes only
// one bounded, duplicate-free object containing a non-blank turn_id. Multiple
// header values are deliberately rejected rather than silently selecting one.
func parseGatewayCodexTurnMetadata(ctx context.Context, values []string) bool {
	if len(values) != 1 || len(values[0]) == 0 || len(values[0]) > gatewayInboundCodexMetadataMaxBytes {
		return false
	}
	valid := false
	if scanBoundedJSONObject(ctx, []byte(values[0]), func(key string, scanner *boundedJSONScanner, depth int) (bool, error) {
		if key != "turn_id" {
			return false, nil
		}
		turnID, err := scanner.boundedString(gatewayInboundModelMaxBytes, gatewayInboundJSONMaxModelLiteralBytes)
		if err != nil || strings.TrimSpace(turnID) == "" || !utf8.ValidString(turnID) {
			return true, errors.New("turn_id must be a bounded non-blank string")
		}
		valid = true
		return true, nil
	}) != nil {
		return false
	}
	return valid
}

type boundedJSONScanner struct {
	raw    []byte
	ctx    context.Context
	pos    int
	tokens int
}

type boundedJSONRootField func(string, *boundedJSONScanner, int) (handled bool, err error)

// scanBoundedJSONObject is intentionally byte-oriented. Unknown scalar values
// and unknown subtree strings/numbers are syntactically validated in place;
// only bounded object keys and the explicitly needed model/turn_id strings are
// decoded. This prevents a valid-but-huge unknown JSON token from becoming a
// second large allocation before later owners decide whether it is relevant.
func scanBoundedJSONObject(ctx context.Context, raw []byte, visitRootField boundedJSONRootField) error {
	if ctx == nil {
		ctx = context.Background()
	}
	scanner := boundedJSONScanner{raw: raw, ctx: ctx}
	if err := scanner.skipSpace(); err != nil {
		return err
	}
	if err := scanner.takeToken(); err != nil {
		return err
	}
	if !scanner.consumeByte('{') {
		return errors.New("JSON body must be one object")
	}
	if err := scanner.scanObject(1, visitRootField); err != nil {
		return err
	}
	if err := scanner.skipSpace(); err != nil {
		return err
	}
	if scanner.pos != len(scanner.raw) {
		return errors.New("JSON body must contain one value")
	}
	return nil
}

func (s *boundedJSONScanner) takeToken() error {
	if err := s.contextErr(); err != nil {
		return err
	}
	if s.tokens >= gatewayInboundJSONMaxTokens {
		return errors.New("JSON token limit exceeded")
	}
	s.tokens++
	return nil
}

func (s *boundedJSONScanner) scanObject(depth int, visitRootField boundedJSONRootField) error {
	if depth > gatewayInboundJSONMaxDepth {
		return errors.New("JSON nesting limit exceeded")
	}
	seen := make(map[string]struct{})
	fields := 0
	if err := s.skipSpace(); err != nil {
		return err
	}
	if s.consumeByte('}') {
		return nil
	}
	for {
		fields++
		if fields > gatewayInboundJSONMaxObjectFields {
			return errors.New("JSON object field limit exceeded")
		}
		if err := s.takeToken(); err != nil {
			return err
		}
		key, err := s.objectKey()
		if err != nil {
			return err
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate JSON key %q", key)
		}
		seen[key] = struct{}{}
		if err := s.skipSpace(); err != nil {
			return err
		}
		if !s.consumeByte(':') {
			return errors.New("JSON object key missing value separator")
		}
		if err := s.skipSpace(); err != nil {
			return err
		}
		handled := false
		if visitRootField != nil {
			handled, err = visitRootField(key, s, depth)
			if err != nil {
				return err
			}
		}
		if !handled {
			if err := s.scanValue(depth); err != nil {
				return err
			}
		}
		if err := s.skipSpace(); err != nil {
			return err
		}
		if s.consumeByte('}') {
			return nil
		}
		if !s.consumeByte(',') {
			return errors.New("JSON object fields must be comma-separated")
		}
		if err := s.skipSpace(); err != nil {
			return err
		}
		if s.peekByte('}') || s.peekByte(',') {
			return errors.New("JSON object has an empty field")
		}
	}
}

func (s *boundedJSONScanner) scanArray(depth int) error {
	if depth > gatewayInboundJSONMaxDepth {
		return errors.New("JSON nesting limit exceeded")
	}
	if err := s.skipSpace(); err != nil {
		return err
	}
	if s.consumeByte(']') {
		return nil
	}
	for {
		if err := s.scanValue(depth); err != nil {
			return err
		}
		if err := s.skipSpace(); err != nil {
			return err
		}
		if s.consumeByte(']') {
			return nil
		}
		if !s.consumeByte(',') {
			return errors.New("JSON array values must be comma-separated")
		}
		if err := s.skipSpace(); err != nil {
			return err
		}
		if s.peekByte(']') || s.peekByte(',') {
			return errors.New("JSON array has an empty value")
		}
	}
}

func (s *boundedJSONScanner) scanValue(depth int) error {
	if err := s.skipSpace(); err != nil {
		return err
	}
	if err := s.takeToken(); err != nil {
		return err
	}
	if s.pos >= len(s.raw) {
		return errors.New("JSON value is missing")
	}
	switch s.raw[s.pos] {
	case '{':
		s.pos++
		return s.scanObject(depth+1, nil)
	case '[':
		s.pos++
		return s.scanArray(depth + 1)
	case '"':
		_, err := s.jsonString(0)
		return err
	case 't':
		return s.literal("true")
	case 'f':
		return s.literal("false")
	case 'n':
		return s.literal("null")
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return s.number()
	default:
		return errors.New("invalid JSON value")
	}
}

func (s *boundedJSONScanner) objectKey() (string, error) {
	literal, err := s.jsonString(gatewayInboundJSONMaxKeyLiteralBytes)
	if err != nil {
		return "", err
	}
	var key string
	if err := json.Unmarshal(literal, &key); err != nil || len(key) > gatewayInboundJSONMaxKeyBytes || !utf8.ValidString(key) {
		return "", errors.New("JSON object key exceeds bounded string limit")
	}
	return key, nil
}

func (s *boundedJSONScanner) boundedString(maxBytes, maxLiteralBytes int) (string, error) {
	if err := s.takeToken(); err != nil {
		return "", err
	}
	literal, err := s.jsonString(maxLiteralBytes)
	if err != nil {
		return "", err
	}
	var value string
	if err := json.Unmarshal(literal, &value); err != nil || len(value) > maxBytes || !utf8.ValidString(value) {
		return "", errors.New("JSON string exceeds bounded string limit")
	}
	return value, nil
}

func (s *boundedJSONScanner) boolean() (bool, error) {
	if err := s.takeToken(); err != nil {
		return false, err
	}
	if s.literal("true") == nil {
		return true, nil
	}
	if s.literal("false") == nil {
		return false, nil
	}
	return false, errors.New("JSON value is not a boolean")
}

func (s *boundedJSONScanner) jsonString(maxLiteralBytes int) ([]byte, error) {
	if !s.consumeByte('"') {
		return nil, errors.New("JSON string is missing opening quote")
	}
	start := s.pos - 1
	for s.pos < len(s.raw) {
		if err := s.contextErr(); err != nil {
			return nil, err
		}
		if maxLiteralBytes > 0 && s.pos-start > maxLiteralBytes {
			return nil, errors.New("JSON string literal exceeds bounded limit")
		}
		current := s.raw[s.pos]
		switch {
		case current == '"':
			s.pos++
			if maxLiteralBytes > 0 && s.pos-start > maxLiteralBytes {
				return nil, errors.New("JSON string literal exceeds bounded limit")
			}
			return s.raw[start:s.pos], nil
		case current < 0x20:
			return nil, errors.New("JSON string contains an unescaped control byte")
		case current == '\\':
			s.pos++
			if s.pos >= len(s.raw) {
				return nil, errors.New("JSON string has an incomplete escape")
			}
			escape := s.raw[s.pos]
			s.pos++
			switch escape {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			case 'u':
				if len(s.raw)-s.pos < 4 || !isJSONHex(s.raw[s.pos]) || !isJSONHex(s.raw[s.pos+1]) || !isJSONHex(s.raw[s.pos+2]) || !isJSONHex(s.raw[s.pos+3]) {
					return nil, errors.New("JSON string has an invalid unicode escape")
				}
				s.pos += 4
			default:
				return nil, errors.New("JSON string has an invalid escape")
			}
		default:
			if current < utf8.RuneSelf {
				s.pos++
				continue
			}
			_, size := utf8.DecodeRune(s.raw[s.pos:])
			if size == 1 {
				return nil, errors.New("JSON string contains invalid UTF-8")
			}
			s.pos += size
		}
	}
	return nil, errors.New("JSON string is not closed")
}

func (s *boundedJSONScanner) number() error {
	start := s.pos
	if s.consumeByte('-') && s.pos >= len(s.raw) {
		return errors.New("JSON number is incomplete")
	}
	if s.consumeByte('0') {
		if s.pos < len(s.raw) && isJSONDigit(s.raw[s.pos]) {
			return errors.New("JSON number has a leading zero")
		}
	} else {
		if s.pos >= len(s.raw) || !isJSONNonZeroDigit(s.raw[s.pos]) {
			return errors.New("JSON number is invalid")
		}
		for s.pos < len(s.raw) && isJSONDigit(s.raw[s.pos]) {
			if err := s.periodicContextErr(start); err != nil {
				return err
			}
			s.pos++
		}
	}
	if s.consumeByte('.') {
		fractionStart := s.pos
		for s.pos < len(s.raw) && isJSONDigit(s.raw[s.pos]) {
			if err := s.periodicContextErr(fractionStart); err != nil {
				return err
			}
			s.pos++
		}
		if s.pos == fractionStart {
			return errors.New("JSON number has an empty fraction")
		}
	}
	if s.peekByte('e') || s.peekByte('E') {
		s.pos++
		if s.peekByte('+') || s.peekByte('-') {
			s.pos++
		}
		exponentStart := s.pos
		for s.pos < len(s.raw) && isJSONDigit(s.raw[s.pos]) {
			if err := s.periodicContextErr(exponentStart); err != nil {
				return err
			}
			s.pos++
		}
		if s.pos == exponentStart {
			return errors.New("JSON number has an empty exponent")
		}
	}
	return nil
}

func (s *boundedJSONScanner) literal(value string) error {
	if len(s.raw)-s.pos < len(value) || string(s.raw[s.pos:s.pos+len(value)]) != value {
		return errors.New("invalid JSON literal")
	}
	s.pos += len(value)
	return nil
}

func (s *boundedJSONScanner) skipSpace() error {
	start := s.pos
	for s.pos < len(s.raw) {
		switch s.raw[s.pos] {
		case ' ', '\n', '\r', '\t':
			if err := s.periodicContextErr(start); err != nil {
				return err
			}
			s.pos++
		default:
			return nil
		}
	}
	return s.contextErr()
}

func (s *boundedJSONScanner) consumeByte(value byte) bool {
	if s.pos >= len(s.raw) || s.raw[s.pos] != value {
		return false
	}
	s.pos++
	return true
}

func (s *boundedJSONScanner) peekByte(value byte) bool {
	return s.pos < len(s.raw) && s.raw[s.pos] == value
}

func (s *boundedJSONScanner) contextErr() error {
	if s.ctx == nil {
		return nil
	}
	return s.ctx.Err()
}

func (s *boundedJSONScanner) periodicContextErr(start int) error {
	if (s.pos-start)&0x3fff != 0 {
		return nil
	}
	return s.contextErr()
}

func isJSONDigit(value byte) bool        { return value >= '0' && value <= '9' }
func isJSONNonZeroDigit(value byte) bool { return value >= '1' && value <= '9' }
func isJSONHex(value byte) bool {
	return isJSONDigit(value) || value >= 'a' && value <= 'f' || value >= 'A' && value <= 'F'
}
