package gatewaybody

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// body.go small helpers (asciiToLower / IsScannedJSONBody /
// normalizedResponseFormat / jsTruthy / safeNonNegativeInteger / maxInt /
// asciiFoldEqual).
// ---------------------------------------------------------------------------

func TestW9CAsciiToLower(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Application/JSON", "application/json"},
		{"already-lower-123", "already-lower-123"},
		{"", ""},
		{"MIXED ÄÖÜ", "mixed ÄÖÜ"},
	}
	for _, tc := range cases {
		if got := asciiToLower(tc.in); got != tc.want {
			t.Fatalf("asciiToLower(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestW9CIsScannedJSONBody(t *testing.T) {
	if IsScannedJSONBody(nil) {
		t.Fatal("nil request must not be scanned JSON")
	}
	if IsScannedJSONBody(&Request{}) {
		t.Fatal("nil state must not be scanned JSON")
	}
	req := &Request{State: &BodyState{JSONParseStatus: JSONParseStatusScannedJSON}}
	if !IsScannedJSONBody(req) {
		t.Fatal("scanned_json body must report scanned")
	}
	req.State.JSONParseStatus = JSONParseStatusDeferredLargeJSON
	if !IsScannedJSONBody(req) {
		t.Fatal("deferred_large_json body must report scanned")
	}
	req.State.JSONParseStatus = JSONParseStatusParsed
	if IsScannedJSONBody(req) {
		t.Fatal("parsed body must not report scanned")
	}
}

func TestW9CNormalizedResponseFormat(t *testing.T) {
	if _, ok := normalizedResponseFormat(42); ok {
		t.Fatal("non-string must be rejected")
	}
	if _, ok := normalizedResponseFormat("   "); ok {
		t.Fatal("blank string must be rejected")
	}
	if _, ok := normalizedResponseFormat(nil); ok {
		t.Fatal("nil must be rejected")
	}
	got, ok := normalizedResponseFormat("  JSON_Schema  ")
	if !ok || got != "json_schema" {
		t.Fatalf("normalizedResponseFormat = %q,%v want json_schema,true", got, ok)
	}
}

func TestW9CJsTruthy(t *testing.T) {
	cases := []struct {
		value any
		want  bool
	}{
		{nil, false},
		{false, false},
		{true, true},
		{float64(0), false},
		{math.NaN(), false},
		{float64(-1), true},
		{"", false},
		{"x", true},
		{map[string]any{}, true},
		{[]any{}, true},
		{struct{}{}, true},
	}
	for _, tc := range cases {
		if got := jsTruthy(tc.value); got != tc.want {
			t.Fatalf("jsTruthy(%#v) = %v want %v", tc.value, got, tc.want)
		}
	}
}

func TestW9CSafeNonNegativeInteger(t *testing.T) {
	if _, ok := safeNonNegativeInteger("5"); ok {
		t.Fatal("string must be rejected")
	}
	if _, ok := safeNonNegativeInteger(math.NaN()); ok {
		t.Fatal("NaN must be rejected")
	}
	if _, ok := safeNonNegativeInteger(math.Inf(1)); ok {
		t.Fatal("Inf must be rejected")
	}
	if _, ok := safeNonNegativeInteger(-1.0); ok {
		t.Fatal("negative must be rejected")
	}
	if _, ok := safeNonNegativeInteger(maxSafeInteger * 2); ok {
		t.Fatal("beyond max safe integer must be rejected")
	}
	if _, ok := safeNonNegativeInteger(1.5); ok {
		t.Fatal("fraction must be rejected")
	}
	got, ok := safeNonNegativeInteger(float64(9007199254740991))
	if !ok || got != 9007199254740991 {
		t.Fatalf("max safe integer = %d,%v", got, ok)
	}
}

func TestW9CMaxIntAndAsciiFoldEqual(t *testing.T) {
	if maxInt(3, 7) != 7 || maxInt(9, 2) != 9 {
		t.Fatal("maxInt ordering broken")
	}
	if asciiFoldEqual("ABC", "abd") {
		t.Fatal("asciiFoldEqual must detect mismatch")
	}
	if !asciiFoldEqual("HeLLo", "hello") {
		t.Fatal("asciiFoldEqual must fold case")
	}
}

// ---------------------------------------------------------------------------
// request.go gaps (sameSlice / GatewayJSONObjectBody /
// bindGatewayRequestParsedObject / BuildGatewayRequestBodySummary /
// GatewayRequestBodyForcesImageGeneration).
// ---------------------------------------------------------------------------

func TestW9CSameSlice(t *testing.T) {
	if sameSlice([]byte("ab"), []byte("abc")) {
		t.Fatal("different lengths must differ")
	}
	if !sameSlice(nil, nil) {
		t.Fatal("two empty slices are the same")
	}
	a := []byte("xyz")
	if !sameSlice(a, a) {
		t.Fatal("identical backing arrays must be same")
	}
	b := make([]byte, len(a))
	copy(b, a)
	if sameSlice(a, b) {
		t.Fatal("equal content with different backing must differ")
	}
}

func TestW9CGatewayJSONObjectBody(t *testing.T) {
	if GatewayJSONObjectBody(nil) != nil {
		t.Fatal("nil request must yield nil")
	}
	if got := GatewayJSONObjectBody(&Request{Body: "text"}); got != nil {
		t.Fatal("non-object body must yield nil")
	}
	buffer := &Request{Body: []byte(`{}`)}
	if got := GatewayJSONObjectBody(buffer); got != nil {
		t.Fatal("buffer body must fall through to nil")
	}
	req := &Request{Body: map[string]any{"a": 1.0}}
	if got := GatewayJSONObjectBody(req); got == nil || got["a"] != 1.0 {
		t.Fatal("object body must be returned")
	}
	fallback := &Request{Body: []byte("raw")}
	fallback.parsedAvailable = true
	fallback.parsedBody = map[string]any{"b": 2.0}
	if got := GatewayJSONObjectBody(fallback); got == nil || got["b"] != 2.0 {
		t.Fatal("parsed fallback must be returned")
	}
	fallbackNonObject := &Request{parsedAvailable: true, parsedBody: "nope"}
	if got := GatewayJSONObjectBody(fallbackNonObject); got != nil {
		t.Fatal("non-object parsed fallback must yield nil")
	}
}

func TestW9CBindGatewayRequestParsedObject(t *testing.T) {
	req := &Request{}
	bindGatewayRequestParsedObject(req, map[string]any{"m": "x"})
	if req.Serialized != nil {
		t.Fatal("empty raw body must not bind")
	}
	req = &Request{RawBody: []byte(`{"m":"x"}`)}
	bindGatewayRequestParsedObject(req, "text")
	if req.Serialized != nil {
		t.Fatal("non-object parsed value must not bind")
	}
	bindGatewayRequestParsedObject(req, map[string]any{"m": "x"})
	if req.Serialized == nil || !bytes.Equal(req.Serialized.Raw, req.RawBody) {
		t.Fatal("object parsed value must bind the serialized body")
	}
}

func TestW9CBuildGatewayRequestBodySummary(t *testing.T) {
	if BuildGatewayRequestBodySummary(nil) != nil {
		t.Fatal("nil request must yield nil")
	}
	if BuildGatewayRequestBodySummary(&Request{}) != nil {
		t.Fatal("nil state must yield nil")
	}
	small := &Request{State: &BodyState{RawBodyBytes: 10, JSONParseWarningBytes: 100}}
	if BuildGatewayRequestBodySummary(small) != nil {
		t.Fatal("under-warning body must yield nil")
	}
	state := &BodyState{
		RawBodyBytes:          300,
		JSONParseWarningBytes: 100,
		ContentType:           "application/json",
		JSONParseStatus:       JSONParseStatusScannedJSON,
		ImageGeneration:       true,
	}
	summary := BuildGatewayRequestBodySummary(&Request{State: state})
	if summary == nil {
		t.Fatal("over-warning body must produce a summary")
	}
	gatewayBody := summary["_gatewayBody"].(map[string]any)
	if gatewayBody["rawBodyBytes"] != 300 || gatewayBody["contentType"] != "application/json" {
		t.Fatalf("unexpected summary fields: %#v", gatewayBody)
	}

	state.Model = strPtr("gpt-image-1")
	state.Stream = boolPtr(true)
	full := BuildGatewayRequestBodySummary(&Request{State: state})["_gatewayBody"].(map[string]any)
	if full["model"] != "gpt-image-1" || full["stream"] != true {
		t.Fatalf("state-provided model/stream missing: %#v", full)
	}

	state.Model = nil
	state.Stream = nil
	bodyFallback := &Request{
		State: state,
		Body:  map[string]any{"model": "from-body", "stream": false},
	}
	fallback := BuildGatewayRequestBodySummary(bodyFallback)["_gatewayBody"].(map[string]any)
	if fallback["model"] != "from-body" || fallback["stream"] != false {
		t.Fatalf("body-provided model/stream fallback missing: %#v", fallback)
	}
}

func TestW9CGatewayRequestBodyForcesImageGeneration(t *testing.T) {
	if GatewayRequestBodyForcesImageGeneration(nil) {
		t.Fatal("nil request must not force image generation")
	}
	forced := &Request{State: &BodyState{ImageGenerationForced: true}}
	if !GatewayRequestBodyForcesImageGeneration(forced) {
		t.Fatal("forced state must report forced")
	}
	viaBody := &Request{Body: map[string]any{"tool_choice": "image_generation"}}
	if !GatewayRequestBodyForcesImageGeneration(viaBody) {
		t.Fatal("body forcing image generation must be detected")
	}
}

// ---------------------------------------------------------------------------
// middleware.go gaps (DiscardLogger methods / Parser / InFlight accessors /
// requestPathOf / requestContentLength / metadata helpers / recordRejection /
// ReadRawBody error surfaces).
// ---------------------------------------------------------------------------

type w9cRecordingRecorder struct {
	calls int
	input RejectionInput
}

func (r *w9cRecordingRecorder) RecordGatewayBodyRejection(_ *http.Request, _ *Request, input RejectionInput) {
	r.calls++
	r.input = input
}

func TestW9CDiscardLoggerMethods(t *testing.T) {
	logger := DiscardLogger{}
	logger.Debug("d", nil)
	logger.Info("i", nil)
	logger.Warn("w", nil)
	logger.Error("e", nil)
}

func TestW9CMiddlewareAccessorsAndRecordRejection(t *testing.T) {
	recorder := &w9cRecordingRecorder{}
	m := NewMiddleware(Config{Recorder: recorder})
	if m.Parser() == nil {
		t.Fatal("default parser must be created")
	}
	if m.InFlight() == nil {
		t.Fatal("default in-flight limiter must be created")
	}
	if recorder.calls != 0 {
		t.Fatal("no rejection expected yet")
	}
	m.recordRejection(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil), nil, RejectionInput{Reason: RejectReasonGatewayBodySizeLimit})
	if recorder.calls != 1 || recorder.input.Reason != RejectReasonGatewayBodySizeLimit {
		t.Fatalf("recorder must receive the rejection, got %+v", recorder.input)
	}
	shared := NewMiddleware(Config{Parser: m.Parser(), InFlight: m.InFlight()})
	if shared.Parser() != m.Parser() || shared.InFlight() != m.InFlight() {
		t.Fatal("configured parser/limiter must be shared")
	}
}

func TestW9CRequestPathOfFallback(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://gw/v1/chat/completions?x=1", nil)
	if got := requestPathOf(req); got != "/v1/chat/completions" {
		t.Fatalf("requestPathOf = %q", got)
	}
	empty := &http.Request{URL: &url.URL{}}
	if got := requestPathOf(empty); got == "" {
		t.Fatal("empty path must fall back to RequestURI")
	}
	if EndpointPathOf("/v1/messages?a=1") != "/v1/messages" {
		t.Fatal("EndpointPathOf must strip the query")
	}
	if EndpointPathOf("/v1/messages") != "/v1/messages" {
		t.Fatal("EndpointPathOf must keep plain paths")
	}
}

func TestW9CRequestContentLengthParsing(t *testing.T) {
	cases := []struct {
		header string
		want   int
		ok     bool
	}{
		{"", 0, false},
		{"   ", 0, false},
		{"abc", 0, false},
		{"-5", 0, false},
		{"1.5", 0, false},
		{"100", 100, true},
		{" 42 ", 42, true},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		if tc.header != "" || tc.header == "" {
			req.Header.Set("Content-Length", tc.header)
		}
		got, ok := requestContentLengthBytes(req)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("Content-Length %q => %d,%v want %d,%v", tc.header, got, ok, tc.want, tc.ok)
		}
	}
}

func TestW9CMetadataPointerHelpers(t *testing.T) {
	s := "tier"
	b := true
	i := 7
	if metadataString(nil) != nil || metadataBool(nil) != nil || metadataInt(nil) != nil {
		t.Fatal("nil pointers must log as nil")
	}
	if metadataString(&s) != "tier" || metadataBool(&b) != true || metadataInt(&i) != 7 {
		t.Fatal("non-nil pointers must log their values")
	}
}

func TestW9CReadRawBodyParserErrors(t *testing.T) {
	m := NewMiddleware(Config{})
	m.rawBodyLimit = 8

	// Success path.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("hello"))
	raw, perr := m.ReadRawBody(rec, req)
	if perr != nil || string(raw) != "hello" {
		t.Fatalf("simple read failed: %q %v", raw, perr)
	}

	// entity.too.large via MaxBytesReader.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/", strings.NewReader("0123456789"))
	raw, perr = m.ReadRawBody(rec, req)
	if perr == nil || perr.Type != "entity.too.large" || perr.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("over-limit read must surface entity.too.large, got %+v", perr)
	}
	resp := m.HandleParserRejection(rec, req, perr)
	if !resp || rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("HandleParserRejection must answer 413, got %d", rec.Code)
	}

	// request.size.invalid via unexpected EOF.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/", io.NopCloser(io.MultiReader(strings.NewReader("abc"), &w9cErrReader{err: io.ErrUnexpectedEOF})))
	_, perr = m.ReadRawBody(rec, req)
	if perr == nil || perr.Type != "request.size.invalid" {
		t.Fatalf("unexpected EOF must surface request.size.invalid, got %+v", perr)
	}

	// Generic read error copy.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/", io.NopCloser(&w9cErrReader{err: errors.New("boom")}))
	_, perr = m.ReadRawBody(rec, req)
	if perr == nil || perr.Message != "boom" || perr.Type != "" {
		t.Fatalf("generic error must be preserved, got %+v", perr)
	}

	// request.aborted via canceled context plus read error.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/", io.NopCloser(&w9cErrReader{err: errors.New("gone")}))
	abortCtx, abort := context.WithCancel(req.Context())
	req = req.WithContext(abortCtx)
	abort()
	_, perr = m.ReadRawBody(rec, req)
	if perr == nil || perr.Type != "request.aborted" {
		t.Fatalf("canceled read must surface request.aborted, got %+v", perr)
	}

	// nil perr returns false and writes nothing.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/", nil)
	if m.HandleParserRejection(rec, req, nil) {
		t.Fatal("nil parser error must return false")
	}
}

type w9cErrReader struct{ err error }

func (r *w9cErrReader) Read([]byte) (int, error) { return 0, r.err }

// ---------------------------------------------------------------------------
// jsonparser.go gaps (logJobFailure / jobKindName / Stop idempotence /
// slow-queue Warn path).
// ---------------------------------------------------------------------------

type w9cParserLogger struct {
	infos  int
	warns  int
	errors int
}

func (l *w9cParserLogger) Debug(string, map[string]any) {}
func (l *w9cParserLogger) Info(string, map[string]any)  { l.infos++ }
func (l *w9cParserLogger) Warn(string, map[string]any)  { l.warns++ }
func (l *w9cParserLogger) Error(string, map[string]any) { l.errors++ }

func TestW9CJSONParserFailureLoggingBothKinds(t *testing.T) {
	logger := &w9cParserLogger{}
	parser := NewJSONParser(JSONParserOptions{PoolSize: 1, Logger: logger})
	parser.parseFunc = func(context.Context, []byte) (any, error) { return nil, errors.New("parse failed") }
	parser.scanFunc = func([]byte) JSONBodyMetadata { return JSONBodyMetadata{InvalidJSON: true} }
	if _, err := parser.ParseJSONBody(context.Background(), []byte(`{}`), time.Second); err == nil {
		t.Fatal("injected parse failure must surface")
	}
	if _, err := parser.ExtractJSONBodyMetadataAsync(context.Background(), []byte(`{}`), time.Second); err != nil {
		t.Fatalf("metadata hook returning metadata must succeed, got %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && logger.errors < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	if logger.errors == 0 {
		t.Fatal("parse hook failure must be logged via logJobFailure")
	}
	parser.Stop()
	parser.Stop() // idempotent second stop.
	if jobKindName(jsonWorkerJobKindParse) != "parse_json_body" ||
		jobKindName(jsonWorkerJobKindMetadata) != "extract_json_body_metadata" {
		t.Fatal("jobKindName must mirror the Node job names")
	}
}

func TestW9CJSONParserSlowJobWarnPath(t *testing.T) {
	logger := &w9cParserLogger{}
	parser := NewJSONParser(JSONParserOptions{PoolSize: 1, Logger: logger})
	parser.parseFunc = func(context.Context, []byte) (any, error) {
		time.Sleep(1100 * time.Millisecond)
		return map[string]any{}, nil
	}
	if _, err := parser.ParseJSONBody(context.Background(), []byte(`{}`), 5*time.Second); err != nil {
		t.Fatalf("slow job must still succeed, got %v", err)
	}
	if logger.warns == 0 {
		t.Fatal("slow job must log the Warn path of logJobCompletion")
	}
}

// ---------------------------------------------------------------------------
// jsonmetadata.go scanner gaps (truthy evaluation, nested string properties,
// tool choice variants, generation config, invalid documents).
// ---------------------------------------------------------------------------

func TestW9CJSONValueIsTruthy(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{`{}`, true},
		{` [] `, true},
		{` "" `, false},
		{` "auto" `, true},
		{` malformed " `, false},
		{` true `, true},
		{` null `, false},
		{` false `, false},
		{` 0 `, false},
		{` 2.5 `, true},
		{` abc `, false},
		{``, false},
	}
	for _, tc := range cases {
		if got := jsonValueIsTruthy([]byte(tc.raw), 0); got != tc.want {
			t.Fatalf("jsonValueIsTruthy(%q) = %v want %v", tc.raw, got, tc.want)
		}
	}
}

func TestW9CReadJSONObjectNestedStringProperty(t *testing.T) {
	// A valid non-object value is skipped with ok semantics preserved.
	if got := readJSONObjectNestedStringProperty([]byte(` "s"`), 0, []string{"effort"}); !got.ok {
		t.Fatal("valid string value must be skipped with ok=true")
	}
	if got := readJSONObjectNestedStringProperty([]byte(` !`), 0, []string{"effort"}); got.ok {
		t.Fatal("invalid primitive must not parse")
	}
	if got := readJSONObjectNestedStringProperty([]byte(`"x"`), 0, nil); !got.ok {
		t.Fatal("empty path must fall back to value skipping")
	}
	// nested hit
	raw := []byte(`{"reasoning":{"effort":"high"},"other":1}`)
	got := readJSONObjectNestedStringProperty(raw, 0, []string{"reasoning", "effort"})
	if !got.ok || got.value == nil || *got.value != "high" {
		t.Fatalf("nested lookup failed: %+v", got)
	}
	// nested miss but valid
	raw = []byte(`{"reasoning":{"depth":3}}`)
	got = readJSONObjectNestedStringProperty(raw, 0, []string{"reasoning", "effort"})
	if !got.ok || got.value != nil {
		t.Fatalf("nested miss must still parse: %+v", got)
	}
	// non-string target value
	raw = []byte(`{"reasoning":{"effort":42}}`)
	got = readJSONObjectNestedStringProperty(raw, 0, []string{"reasoning", "effort"})
	if !got.ok || got.value != nil {
		t.Fatalf("non-string nested value must parse with nil: %+v", got)
	}
	// broken nested object
	raw = []byte(`{"reasoning":{"effort":"high"}`)
	got = readJSONObjectNestedStringProperty(raw, 0, []string{"reasoning", "effort"})
	if got.ok {
		t.Fatal("truncated nested object must fail")
	}
	// key without colon
	raw = []byte(`{"effort" "high"}`)
	got = readJSONObjectNestedStringProperty(raw, 0, []string{"effort"})
	if got.ok {
		t.Fatal("missing colon must fail")
	}
	// key that is not a string token
	raw = []byte(`{42:"x"}`)
	got = readJSONObjectNestedStringProperty(raw, 0, []string{"effort"})
	if got.ok {
		t.Fatal("non-string key must fail")
	}
}

func TestW9CInspectJSONToolDefinitionsVariants(t *testing.T) {
	// string element
	got := inspectJSONToolDefinitions([]byte(`"image_generation"`), 0, 0)
	if got.inspection.ImageToolCount != 1 {
		t.Fatalf("string tool element: %+v", got.inspection)
	}
	got = inspectJSONToolDefinitions([]byte(`"web_search"`), 0, 0)
	if got.inspection.NonImageToolCount != 1 {
		t.Fatalf("non-image string tool: %+v", got.inspection)
	}
	// depth cutoff
	got = inspectJSONToolDefinitions([]byte(`["image_generation"]`), 0, 5)
	if got.inspection.ImageToolCount != 0 || got.nextIndex != len(`["image_generation"]`) {
		t.Fatalf("depth > 4 must skip the value: %+v", got)
	}
	// malformed string
	got = inspectJSONToolDefinitions([]byte(`"unterminated`), 0, 0)
	if got.nextIndex != len(`"unterminated`) {
		t.Fatalf("malformed tool string: %+v", got)
	}
	// object element
	got = inspectJSONToolDefinitions([]byte(`{"type":"image_generation"}`), 0, 0)
	if got.inspection.ImageToolCount != 1 {
		t.Fatalf("object tool element: %+v", got.inspection)
	}
	// primitive element (skipped)
	got = inspectJSONToolDefinitions([]byte(`null`), 0, 0)
	if got.inspection.ImageToolCount != 0 {
		t.Fatalf("primitive element: %+v", got.inspection)
	}
	// array of mixed
	got = inspectJSONToolDefinitions([]byte(`["image_generation",{"type":"web_search"},7]`), 0, 0)
	if got.inspection.ImageToolCount != 1 || got.inspection.NonImageToolCount != 1 {
		t.Fatalf("mixed array: %+v", got.inspection)
	}
}

func TestW9CInspectJSONToolChoiceVariants(t *testing.T) {
	got := inspectJSONToolChoice([]byte(`"image_generation"`), 0)
	if !got.inspection.ForcedImageGeneration {
		t.Fatal("string image_generation choice must force")
	}
	got = inspectJSONToolChoice([]byte(`"required"`), 0)
	if !got.required || got.inspection.ForcedImageGeneration {
		t.Fatalf("required choice: %+v", got)
	}
	got = inspectJSONToolChoice([]byte(`"auto"`), 0)
	if got.required || got.inspection.ForcedImageGeneration {
		t.Fatalf("auto choice: %+v", got)
	}
	got = inspectJSONToolChoice([]byte(`"unterminated`), 0)
	if got.nextIndex != len(`"unterminated`) {
		t.Fatalf("malformed choice string: %+v", got)
	}
	got = inspectJSONToolChoice([]byte(`42`), 0)
	if got.nextIndex != len(`42`) {
		t.Fatalf("primitive choice: %+v", got)
	}
	got = inspectJSONToolChoice([]byte(`{"type":"image_generation","tools":["image_generation"]}`), 0)
	if !got.inspection.ForcedImageGeneration || got.inspection.ImageToolCount != 1 {
		t.Fatalf("object choice: %+v", got.inspection)
	}
	got = inspectJSONToolChoice([]byte(`{"type":42}`), 0)
	if got.inspection.ForcedImageGeneration {
		t.Fatalf("non-string type: %+v", got.inspection)
	}
	got = inspectJSONToolChoice([]byte(`{"type" 42}`), 0)
	if got.nextIndex != len(`{"type" 42}`) {
		t.Fatalf("missing colon choice: %+v", got)
	}
	got = inspectJSONToolChoice([]byte(`{!}`), 0)
	if got.nextIndex != len(`{!}`) {
		t.Fatalf("garbage key choice: %+v", got)
	}
	got = inspectJSONToolChoice([]byte(`{"tools":["image_generation"],"type":"function"}`), 0)
	if got.inspection.ImageToolCount != 1 || got.inspection.ForcedImageGeneration {
		t.Fatalf("tools-only choice must count but not force: %+v", got.inspection)
	}
}

func TestW9CInspectGenerationConfigVariants(t *testing.T) {
	// non-object value skipped
	got := inspectGenerationConfig([]byte(` 42 `), 0)
	if got.isObject || got.nextIndex != len(` 42 `) {
		t.Fatalf("primitive generation config: %+v", got)
	}
	// camel thinkingConfig effort
	raw := []byte(`{"thinkingConfig":{"thinkingLevel":"high"},"responseModalities":["TEXT","IMAGE"],"responseMimeType":null}`)
	got = inspectGenerationConfig(raw, 0)
	if !got.isObject || got.reasoningEffort == nil || *got.reasoningEffort != "high" {
		t.Fatalf("camel config: %+v", got)
	}
	if !got.imageOutput {
		t.Fatal("IMAGE modality must mark image output")
	}
	// snake thinking_config effort + response_mime_type image
	raw = []byte(`{"thinking_config":{"thinking_level":"low"},"response_modalities":["TEXT"],"response_mime_type":"image/png"}`)
	got = inspectGenerationConfig(raw, 0)
	if got.reasoningEffort == nil || *got.reasoningEffort != "low" {
		t.Fatalf("snake config effort: %+v", got)
	}
	if !got.imageOutput {
		t.Fatal("image mime type must mark image output")
	}
	// invalid effort values
	raw = []byte(`{"thinkingConfig":"not-object"}`)
	got = inspectGenerationConfig(raw, 0)
	if got.reasoningEffort != nil {
		t.Fatalf("non-object thinkingConfig must not yield effort: %+v", got)
	}
	// malformed key
	raw = []byte(`{42:"x"}`)
	got = inspectGenerationConfig(raw, 0)
	if got.nextIndex != len(`{42:"x"}`) {
		t.Fatalf("garbage config: %+v", got)
	}
	// unterminated object: loop drains to the end of raw and still reports
	// the parsed fields with isObject semantics.
	raw = []byte(`{"thinkingConfig":{"thinkingLevel":"high"}`)
	got = inspectGenerationConfig(raw, 0)
	if got.nextIndex != len(raw) || got.reasoningEffort == nil || *got.reasoningEffort != "high" {
		t.Fatalf("unterminated config must keep parsed effort: %+v", got)
	}
}

func TestW9CExtractJSONBodyMetadataCombinations(t *testing.T) {
	// compaction trigger + reasoning precedence + token limit max + strict flags.
	raw := []byte(`{"type":"response","model":"m1","stream":true,` +
		`"reasoning":{"effort":"high"},"reasoning_effort":"low","output_config":{"effort":"minimal"},` +
		`"max_output_tokens":10,"max_tokens":20,` +
		`"response_format":{"type":"json_schema"},"tools":[{"type":"image_generation"}],"tool_choice":"required",` +
		`"service_tier":"flex","type2":"ignored","nested":{"type":"compaction_trigger"}}`)
	metadata := ExtractJSONBodyMetadata(raw)
	if metadata.Model == nil || *metadata.Model != "m1" {
		t.Fatalf("model: %+v", metadata)
	}
	if metadata.Stream == nil || !*metadata.Stream {
		t.Fatalf("stream: %+v", metadata)
	}
	if metadata.ReasoningEffort == nil || *metadata.ReasoningEffort != "high" {
		t.Fatalf("reasoning precedence: %+v", metadata)
	}
	if metadata.MaxOutputTokens == nil || *metadata.MaxOutputTokens != 20 {
		t.Fatalf("token limit max: %+v", metadata)
	}
	if !metadata.StrictOutputRequirement {
		t.Fatal("strict output requirement must be true")
	}
	if !metadata.ImageGeneration || !metadata.ImageGenerationForced {
		t.Fatalf("image generation flags: %+v", metadata)
	}
	if metadata.ServiceTier == nil || *metadata.ServiceTier != "flex" {
		t.Fatalf("service tier: %+v", metadata)
	}

	// tool_choice required with only image tools forces image generation.
	metadata = ExtractJSONBodyMetadata([]byte(`{"tools":["image_generation"],"tool_choice":"required"}`))
	if !metadata.ImageGenerationForced {
		t.Fatalf("required + image-only tools must force: %+v", metadata)
	}

	// invalid JSON keeps pointer fields discovered before failure.
	metadata = ExtractJSONBodyMetadata([]byte(`{"model":"m1","stream":true,`))
	if !metadata.InvalidJSON {
		t.Fatalf("truncated body must be invalid: %+v", metadata)
	}

	// effort fallback to output_config.
	metadata = ExtractJSONBodyMetadata([]byte(`{"output_config":{"effort":"minimal"}}`))
	if metadata.ReasoningEffort == nil || *metadata.ReasoningEffort != "minimal" {
		t.Fatalf("output_config effort fallback: %+v", metadata)
	}

	// non-string model falls back to nil model.
	metadata = ExtractJSONBodyMetadata([]byte(`{"model":42}`))
	if metadata.Model != nil {
		t.Fatalf("non-string model must be nil: %+v", metadata)
	}

	// generation_config snake fallback.
	metadata = ExtractJSONBodyMetadata([]byte(`{"generation_config":{"response_modalities":["IMAGE"]}}`))
	if !metadata.ImageGeneration {
		t.Fatalf("snake generation config image: %+v", metadata)
	}

	// tool_choice object with nested image tool but auto type does not force.
	metadata = ExtractJSONBodyMetadata([]byte(`{"tool_choice":{"type":"auto","tools":[{"type":"image_generation"}]}}`))
	if metadata.ImageGenerationForced {
		t.Fatalf("auto nested image tool must not force: %+v", metadata)
	}
}

func TestW9CJSONDocumentValidationEdges(t *testing.T) {
	invalid := []string{
		``,
		`   `,
		`{"a":}`,
		`{,}`,
		`[1,]`,
		`[1 true]`,
		`{"a" 1}`,
		`{"a":1,}`,
		`"unterminated`,
		`"\x"`,
		`"a\u12"`,
		`01`,
		`1.`,
		`1e`,
		`-`,
		`+1`,
		`.5`,
		`1 2`,
		`tru`,
		`nulll`,
		`{"type":}`,
		`[[`,
		`{"a":1}}`,
	}
	for _, raw := range invalid {
		if isValidJSONDocument([]byte(raw), &jsonDocumentCallbacks{
			onCodexCompactionTrigger: func() {},
			onTopLevelProperty:       func(string, int, int) {},
		}) {
			t.Fatalf("%q must be invalid JSON", raw)
		}
	}
	valid := []string{
		`null`,
		`true`,
		` false `,
		`0`,
		`-0.5e+10`,
		`1E-2`,
		`"a\"b\\c"`,
		`{"k":"é"}`,
		`{"type":"compaction_trigger","a":1}`,
		`[1,[2,{"b":[3]}],null]`,
		`{"nested":{"type":{"deep":"compaction_trigger"}}}`,
		`  { "k" : { } }  `,
	}
	for _, raw := range valid {
		if !isValidJSONDocument([]byte(raw), &jsonDocumentCallbacks{
			onCodexCompactionTrigger: func() {},
			onTopLevelProperty:       func(string, int, int) {},
		}) {
			t.Fatalf("%q must be valid JSON", raw)
		}
	}
}

func TestW9CJSONSmallReaderHelpers(t *testing.T) {
	// readJSONBoolean boundaries.
	if v, ok := readJSONBoolean([]byte("true"), 0); !ok || !v {
		t.Fatal("true must parse")
	}
	if v, ok := readJSONBoolean([]byte("false"), 0); !ok || v {
		t.Fatal("false must parse")
	}
	if _, ok := readJSONBoolean([]byte("tru"), 0); ok {
		t.Fatal("truncated true must fail")
	}
	if _, ok := readJSONBoolean([]byte("fals"), 0); ok {
		t.Fatal("truncated false must fail")
	}
	// readJSONNonNegativeInteger boundaries.
	if v, ok := readJSONNonNegativeInteger([]byte(" 7 "), 0); !ok || v != 7 {
		t.Fatal("whitespace int must parse")
	}
	if _, ok := readJSONNonNegativeInteger([]byte("1.5"), 0); ok {
		t.Fatal("fraction must fail")
	}
	if _, ok := readJSONNonNegativeInteger([]byte("-3"), 0); ok {
		t.Fatal("negative must fail")
	}
	if _, ok := readJSONNonNegativeInteger([]byte("null"), 0); ok {
		t.Fatal("literal must fail")
	}
	// decodeJSONStringToken with escapes.
	raw := []byte(`"a\nb"`)
	token, ok := readJSONStringToken(raw, 0)
	if !ok || token.value != "a\nb" || token.nextIndex != len(raw) {
		t.Fatalf("escaped token: %+v ok=%v", token, ok)
	}
	// plain token avoids unmarshal.
	plain := []byte(`"abc"`)
	token, ok = readJSONStringToken(plain, 0)
	if !ok || token.value != "abc" {
		t.Fatalf("plain token: %+v ok=%v", token, ok)
	}
	// jsonValueIsNull.
	if !jsonValueIsNull([]byte(` null `), 0) {
		t.Fatal("null literal must be detected")
	}
	if jsonValueIsNull([]byte(` nul `), 0) {
		t.Fatal("truncated null must not match")
	}
	if jsonValueIsNull([]byte(`null`), 2) {
		t.Fatal("window past end must not match")
	}
	// compactJSONFrameStack helpers.
	var stack compactJSONFrameStack
	if _, ok := stack.pop(); ok {
		t.Fatal("empty stack pop must fail")
	}
	if stack.replaceTop('x') {
		t.Fatal("empty stack replaceTop must fail")
	}
	if _, ok := stack.peek(); ok {
		t.Fatal("empty stack peek must fail")
	}
	stack.push('{')
	if frame, ok := stack.peek(); !ok || frame != '{' {
		t.Fatal("peek must return the only frame")
	}
	stack.push('[')
	if frame, ok := stack.peek(); !ok || frame != '[' {
		t.Fatal("peek must return the top frame")
	}
	if !stack.replaceTop(']') || stack.frames[stack.length-1] != ']' {
		t.Fatal("replaceTop must swap the top frame")
	}
	if frame, ok := stack.pop(); !ok || frame != ']' {
		t.Fatal("pop must return the replaced frame")
	}
	if _, ok := stack.peek(); !ok || stack.frames[stack.length-1] != '{' {
		t.Fatal("remaining frame must be visible")
	}
}

// ---------------------------------------------------------------------------
// Capture lane resolution and reject paths (resolveLane / limits).
// ---------------------------------------------------------------------------

func TestW9CResolveLaneVariants(t *testing.T) {
	m := NewMiddleware(Config{})
	imageReq := httptest.NewRequest(http.MethodPost, "http://gw/v1/images/generations", nil)
	if m.resolveLane(imageReq, &Request{}) != LaneImage {
		t.Fatal("image endpoint path must resolve to image lane")
	}
	modelReq := httptest.NewRequest(http.MethodPost, "http://gw/v1/chat/completions", nil)
	if m.resolveLane(modelReq, &Request{State: &BodyState{Model: strPtr("Dall-E-3")}}) != LaneImage {
		t.Fatal("image model must resolve to image lane")
	}
	if m.resolveLane(modelReq, &Request{State: &BodyState{Model: strPtr("gpt-4o"), ImageGeneration: true}}) != LaneImage {
		t.Fatal("image generation state must resolve to image lane")
	}
	if m.resolveLane(modelReq, &Request{State: &BodyState{Model: strPtr("gpt-4o")}}) != LaneText {
		t.Fatal("plain model must resolve to text lane")
	}
	custom := NewMiddleware(Config{LaneResolver: func(*http.Request, *Request) Lane { return LaneImage }})
	if custom.resolveLane(modelReq, &Request{}) != LaneImage {
		t.Fatal("custom resolver must win")
	}
}

func TestW9CCaptureRejectsLargeJSONThroughWorkerPath(t *testing.T) {
	recorder := &w9cRecordingRecorder{}
	m := NewMiddleware(Config{Recorder: recorder})
	// Force the deferred scan path with a rawBodyLimit override and a body
	// above the inline metadata scan threshold.
	bigJSON := []byte(`{"model":"m"}` + strings.Repeat(" ", GatewayJSONBodyInlineMetadataScanMaxBytes))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://gw/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	got, err := m.Capture(rec, req, bigJSON)
	if err != nil {
		t.Fatalf("deferred scan must succeed: %v", err)
	}
	if got == nil || got.State.JSONParseStatus != JSONParseStatusScannedJSON {
		t.Fatalf("large JSON must be scanned: %+v", got)
	}
	if got.State.Model == nil || *got.State.Model != "m" {
		t.Fatalf("deferred scan must still extract model: %+v", got.State)
	}
	if recorder.calls != 0 {
		t.Fatalf("accepted request must not be recorded: %+v", recorder.input)
	}
}

func TestW9CCaptureMetadataWorkerBusyAndFailed(t *testing.T) {
	rawBody := []byte(`{"model":"m"}` + strings.Repeat(" ", GatewayJSONBodyInlineMetadataScanMaxBytes))
	newReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "http://gw/v1/chat/completions", nil)
		req.Header.Set("Content-Type", "application/json")
		return req
	}

	// Queue full: one worker busy on a blocking parse job plus a queued job
	// leaves no queue slot for the metadata scan.
	release := make(chan struct{})
	busyParser := NewJSONParser(JSONParserOptions{PoolSize: 1, MaxQueuedJobs: 1})
	busyParser.parseFunc = func(context.Context, []byte) (any, error) {
		<-release
		return map[string]any{}, nil
	}
	occupied := make(chan error, 1)
	go func() {
		_, err := busyParser.ParseJSONBody(context.Background(), []byte(`1`), 10*time.Second)
		occupied <- err
	}()
	waitFor := func(cond func() bool) {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			busyParser.mu.Lock()
			ok := cond()
			busyParser.mu.Unlock()
			if ok {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
		t.Fatal("parser never reached the expected state")
	}
	waitFor(func() bool { return busyParser.busyWorkers == 1 && len(busyParser.queue) == 0 })
	queued := make(chan error, 1)
	go func() {
		_, err := busyParser.ParseJSONBody(context.Background(), []byte(`2`), 10*time.Second)
		queued <- err
	}()
	waitFor(func() bool { return busyParser.busyWorkers == 1 && len(busyParser.queue) == 1 })
	m := NewMiddleware(Config{Parser: busyParser})
	m.metadataScanTimeout = 2 * time.Second
	m.rawBodyLimit = GatewayRawBodyHardLimitBytes
	rec := httptest.NewRecorder()
	got, err := m.Capture(rec, newReq(), rawBody)
	close(release)
	if err != nil || got != nil {
		t.Fatalf("queue-full capture must answer without error: %v %+v", err, got)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("queue-full capture must answer 503, got %d", rec.Code)
	}
	if <-occupied != nil || <-queued != nil {
		t.Fatal("released jobs must complete cleanly")
	}

	// Worker failed: a stopped parser surfaces the generic worker failure.
	failedParser := NewJSONParser(JSONParserOptions{PoolSize: 1})
	failedParser.Stop()
	m2 := NewMiddleware(Config{Parser: failedParser})
	m2.metadataScanTimeout = time.Second
	m2.rawBodyLimit = GatewayRawBodyHardLimitBytes
	rec = httptest.NewRecorder()
	got, err = m2.Capture(rec, newReq(), rawBody)
	if err != nil || got != nil {
		t.Fatalf("stopped parser capture must answer without error: %v %+v", err, got)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("stopped parser capture must answer 503, got %d", rec.Code)
	}
}

func TestW9CCaptureAbortedRequestAfterScan(t *testing.T) {
	m := NewMiddleware(Config{})
	rawBody := []byte(`{"model":"m"}` + strings.Repeat(" ", GatewayJSONBodyInlineMetadataScanMaxBytes))
	req := httptest.NewRequest(http.MethodPost, "http://gw/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	cancel()
	rec := httptest.NewRecorder()
	got, err := m.Capture(rec, req, rawBody)
	if err != nil {
		t.Fatalf("aborted capture must not error: %v", err)
	}
	if got != nil {
		t.Fatalf("aborted capture must return nil request: %+v", got)
	}
}

func TestW9CRejectByContentLengthScopes(t *testing.T) {
	recorder := &w9cRecordingRecorder{}
	m := NewMiddleware(Config{
		Recorder:                  recorder,
		TextRawBodyLimitMegabytes: func() (int, bool) { return 1, true },
	})

	// Over text limit.
	req := httptest.NewRequest(http.MethodPost, "http://gw/v1/chat/completions", nil)
	req.Header.Set("Content-Length", "2097152")
	rec := httptest.NewRecorder()
	if !m.RejectByContentLength(rec, req) {
		t.Fatal("over text limit must reject")
	}
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", rec.Code)
	}

	// Image scope keeps the 64mb limit: 2mb passes.
	req = httptest.NewRequest(http.MethodPost, "http://gw/v1/images/generations", nil)
	req.Header.Set("Content-Length", "2097152")
	if m.RejectByContentLength(httptest.NewRecorder(), req) {
		t.Fatal("2mb image request must pass")
	}

	// Non-applicable path.
	req = httptest.NewRequest(http.MethodPost, "http://gw/models", nil)
	req.Header.Set("Content-Length", "999999999")
	if m.RejectByContentLength(httptest.NewRecorder(), req) {
		t.Fatal("non-body path must not reject")
	}

	// Missing header.
	req = httptest.NewRequest(http.MethodPost, "http://gw/v1/chat/completions", nil)
	if m.RejectByContentLength(httptest.NewRecorder(), req) {
		t.Fatal("missing Content-Length must not reject")
	}
	if recorder.calls == 0 {
		t.Fatal("rejection must be recorded")
	}
}
