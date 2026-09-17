package gatewaybody

// w13c 覆盖率补充测试：补齐 w11b 之后仍 uncovered 的分支。
//
// 不可达语句清单（维持守卫以保持与 Node 语义一致的防御结构，不删除）：
//   - jsonmetadata.go isValidJSONDocument 中的栈守卫失败返回
//     （closeCurrentFrame/replaceTop 的 !ok 分支）：主循环仅在 rootComplete
//     为 false 时进入，此时栈非空，peek 必成功，pop/replaceTop 不会失败。
//   - middleware.go Capture 小 JSON 内联分支的 rejectByRequestLane 命中分支：
//     文本 lane 下限 1MB 大于内联上限 256KB，image lane 为 64MB，均无法触发。
//   - middleware.go writeErrorJSON 的 json.Marshal 失败分支：payload 为纯
//     struct，序列化不可能失败。
//   - jsonparser.go NewJSONParser 的 poolSize < 1 分支：runtime.GOMAXPROCS
//     恒 >= 1。
//   - jsonparser.go executeJob 的 peekResult 提前命中分支：仅在 enqueue 返回
//     与 peek 之间的间隙完成时触发，worker 结果始终通过 done channel 分支
//     返回，属于不可依赖的真实竞态窗口。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestW13CAsciiFoldEqualFoldsSecondOperand(t *testing.T) {
	if !asciiFoldEqual("abc", "ABC") {
		t.Fatal("abc must fold-equal ABC")
	}
	if !asciiFoldEqual("aBc", "AbC") {
		t.Fatal("mixed case must fold-equal")
	}
	if asciiFoldEqual("abc", "ABD") {
		t.Fatal("distinct letters must not fold-equal")
	}
}

func TestW13CCreateBodyStateImageFromParsedTools(t *testing.T) {
	parsed := map[string]any{
		"tools": []any{map[string]any{"type": "image_generation"}},
	}
	state := CreateBodyState(BodyStateInput{
		RawBody:     []byte(`{"tools":[{"type":"image_generation"}]}`),
		ContentType: "application/json",
		ParsedBody:  parsed,
	})
	if !state.ImageGeneration {
		t.Fatal("parsed image tool must set ImageGeneration")
	}
}

func TestW13CExtractMetadataInvalidScalarFields(t *testing.T) {
	raw := []byte(`{"model":5,"service_tier":5,"reasoning_effort":5,"stream":"yes","max_output_tokens":-1,"max_tokens":-2,"type":5}`)
	metadata := ExtractJSONBodyMetadata(raw)
	if metadata.InvalidJSON {
		t.Fatalf("valid JSON document must not be invalid: %+v", metadata)
	}
	if metadata.Model != nil || metadata.ServiceTier != nil || metadata.ReasoningEffort != nil {
		t.Fatalf("non-string scalars must not produce pointers: %+v", metadata)
	}
	if metadata.Stream != nil || metadata.MaxOutputTokens != nil {
		t.Fatalf("invalid stream/limit must produce nil pointers: %+v", metadata)
	}
}

func TestW13CExtractMetadataTokenLimitMerge(t *testing.T) {
	first := ExtractJSONBodyMetadata([]byte(`{"max_output_tokens":9,"max_tokens":5}`))
	if first.MaxOutputTokens == nil || *first.MaxOutputTokens != 9 {
		t.Fatalf("max_output_tokens must participate in the max merge: %+v", first.MaxOutputTokens)
	}
	second := ExtractJSONBodyMetadata([]byte(`{"max_output_tokens":5,"max_tokens":9}`))
	if second.MaxOutputTokens == nil || *second.MaxOutputTokens != 9 {
		t.Fatalf("merge must keep the larger limit: %+v", second.MaxOutputTokens)
	}
}

func TestW13CGenerationConfigNonObjectAndSnakeMime(t *testing.T) {
	nonObject := ExtractJSONBodyMetadata([]byte(`{"generationConfig":5,"generation_config":null}`))
	if nonObject.ImageGeneration {
		t.Fatal("non-object generation config must not set image generation")
	}
	snakeMime := ExtractJSONBodyMetadata([]byte(`{"generation_config":{"response_mime_type":5}}`))
	if snakeMime.ImageGeneration {
		t.Fatal("non-string mime must not set image generation")
	}
}

func TestW13CResponseModalitiesArrayEdges(t *testing.T) {
	withNonString := ExtractJSONBodyMetadata([]byte(`{"generation_config":{"response_modalities":["text",5,"IMAGE"]}}`))
	if !withNonString.ImageGeneration {
		t.Fatal("array containing IMAGE must set image generation")
	}
	if nextIndex, image := inspectJSONStringArrayForImage([]byte(`["image"`), 0); !image || nextIndex != len(`["image"`) {
		t.Fatalf("unterminated array must scan to the end, got %d %v", nextIndex, image)
	}
}

func TestW13CReadJSONObjectNestedStringPropertyEdges(t *testing.T) {
	nestedNotOk := readJSONObjectNestedStringProperty([]byte(`{"reasoning":{"effort":,}}`), 0, []string{"reasoning", "effort"})
	if nestedNotOk.ok {
		t.Fatal("invalid nested value must not be ok")
	}
	skipNotOk := readJSONObjectNestedStringProperty([]byte(`{"reasoning":tru,"effort":"high"}`), 0, []string{"effort"})
	if skipNotOk.ok {
		t.Fatal("invalid skipped value must not be ok")
	}
}

func TestW13CToolDefinitionScanEdges(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"unterminated array", `{"tools":[{"type":"function"}`},
		{"invalid object key", `{"tools":{5:6}}`},
		{"missing colon", `{"tools":{"type" 5}}`},
		{"non-string type", `{"tools":{"type":5}}`},
		{"unterminated object", `{"tools":{"type":"image_generation"}`},
	}
	for _, tc := range cases {
		inspection := ExtractJSONBodyMetadata([]byte(tc.raw))
		if inspection.ImageGeneration {
			t.Fatalf("%s must not set image generation: %+v", tc.name, inspection)
		}
	}
}

func TestW13CToolChoiceScanEdges(t *testing.T) {
	unterminated := ExtractJSONBodyMetadata([]byte(`{"tool_choice":{"type":"image_generation"}`))
	if unterminated.ImageGenerationForced {
		t.Fatal("unterminated tool_choice object must not force image generation")
	}
	nonStringType := ExtractJSONBodyMetadata([]byte(`{"tool_choice":{"type":5}}`))
	if nonStringType.ImageGenerationForced {
		t.Fatal("non-string tool_choice type must not force image generation")
	}
}

func TestW13CStringEscapeScanEdges(t *testing.T) {
	controlChar := ExtractJSONBodyMetadata([]byte("{\"model\":\"a\x01b\"}"))
	if !controlChar.InvalidJSON {
		t.Fatal("raw control char inside string must invalidate the document")
	}
	trailingEscape := ExtractJSONBodyMetadata([]byte(`{"model":"a\`))
	if !trailingEscape.InvalidJSON {
		t.Fatal("trailing backslash must invalidate the document")
	}
	badHex := ExtractJSONBodyMetadata([]byte(`{"model":"\uZZZZ"}`))
	if !badHex.InvalidJSON {
		t.Fatal("bad unicode hex must invalidate the document")
	}
	escapedKey := ExtractJSONBodyMetadata([]byte(`{"\u0061":1}`))
	if escapedKey.InvalidJSON {
		t.Fatal("escaped unknown key must stay valid")
	}
}

func TestW13CDecodeStringTokenInvalidEscape(t *testing.T) {
	metadata := ExtractJSONBodyMetadata([]byte(`{"model":"\uZZ"}`))
	if metadata.Model != nil {
		t.Fatal("invalid unicode escape must not produce a model pointer")
	}
	truthy := ExtractJSONBodyMetadata([]byte(`{"response_format":"\uZZ"}`))
	if truthy.StrictOutputRequirement {
		t.Fatal("unreadable string must not be truthy")
	}
}

func TestW13CReadNonNegativeIntegerInvalidPrimitive(t *testing.T) {
	metadata := ExtractJSONBodyMetadata([]byte(`{"max_output_tokens":tru}`))
	if metadata.MaxOutputTokens != nil {
		t.Fatal("invalid primitive must not produce a limit")
	}
}

func TestW13CJsonTruthyNumberFallback(t *testing.T) {
	truthy := ExtractJSONBodyMetadata([]byte(`{"response_format":1.5}`))
	if !truthy.StrictOutputRequirement {
		t.Fatal("non-zero number must be truthy")
	}
	invalidNumber := ExtractJSONBodyMetadata([]byte(`{"response_format":1e}`))
	if invalidNumber.StrictOutputRequirement {
		t.Fatal("invalid number must not be truthy")
	}
}

func TestW13CSkipJSONValueEdges(t *testing.T) {
	if result := skipJSONValue([]byte(`"unterminated`), 0); result.ok {
		t.Fatal("unterminated string value must not skip ok")
	}
	if result := skipJSONValue([]byte(`{"a":"x`), 0); result.ok {
		t.Fatal("object with unterminated string must not skip ok")
	}
	if result := skipJSONValue([]byte(`[[1]]`), 0); !result.ok {
		t.Fatal("nested arrays must skip ok")
	}
	if result := skipJSONValue([]byte(`{"a":1]}`), 0); result.ok {
		t.Fatal("mismatched brackets must not skip ok")
	}
	if result := skipJSONValue([]byte(`{"a":1`), 0); result.ok {
		t.Fatal("unterminated object must not skip ok")
	}
}

func TestW13CIsValidJSONNumberEdges(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"", false},
		{"-", false},
		{"01", false},
		{"1.", false},
		{"1e+5", true},
		{"1e", false},
	}
	for _, tc := range cases {
		if got := isValidJSONNumber([]byte(tc.text), 0, len(tc.text)); got != tc.want {
			t.Fatalf("isValidJSONNumber(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestW13CJSONParserContextAndDefaultPaths(t *testing.T) {
	parser := NewJSONParser(JSONParserOptions{PoolSize: 1})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := parser.ParseJSONBody(ctx, []byte(`{}`), time.Second); !IsCanceledError(err) {
		t.Fatalf("pre-canceled context must fail canceled, got %v", err)
	}
	if _, err := parser.ParseJSONBody(context.Background(), []byte(`{"a":1}`), 0); err != nil {
		t.Fatalf("timeout <= 0 must use the default, got %v", err)
	}
	parser.Stop()
}

func TestW13CJSONParserRunJobWithCanceledContext(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	parser := NewJSONParser(JSONParserOptions{PoolSize: 1})
	parser.parseFunc = func(ctx context.Context, raw []byte) (any, error) {
		started <- struct{}{}
		<-release
		return map[string]any{}, nil
	}
	blockerDone := make(chan struct{})
	go func() {
		_, _ = parser.ParseJSONBody(context.Background(), []byte(`{}`), 5*time.Second)
		close(blockerDone)
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	targetDone := make(chan struct{})
	var targetErr error
	go func() {
		_, targetErr = parser.ParseJSONBody(ctx, []byte(`{}`), 5*time.Second)
		close(targetDone)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	close(release)
	<-blockerDone
	<-targetDone
	if !IsCanceledError(targetErr) {
		t.Fatalf("queued job with canceled ctx must fail canceled, got %v", targetErr)
	}
	parser.Stop()
}

func TestW13CJSONParserPeekResultAndDoubleStop(t *testing.T) {
	parser := NewJSONParser(JSONParserOptions{PoolSize: 1})
	job := &jsonWorkerJob{done: make(chan struct{})}
	if _, done := parser.peekResult(job); done {
		t.Fatal("fresh job must not peek a result")
	}
	job.result = jsonWorkerResult{value: "v"}
	job.resultSet.Store(true)
	if result, done := parser.peekResult(job); !done || result.value != "v" {
		t.Fatal("result-set job must peek its result")
	}
	parser.Stop()
	parser.Stop()
	if _, err := parser.ParseJSONBody(context.Background(), []byte(`{}`), time.Second); !errors.Is(err, ErrStopped) {
		t.Fatalf("stopped parser must reject with ErrStopped, got %v", err)
	}
}

func TestW13CJSONParserLogFailureWithoutLogger(t *testing.T) {
	parser := NewJSONParser(JSONParserOptions{PoolSize: 1})
	parser.parseFunc = func(ctx context.Context, raw []byte) (any, error) {
		return nil, errors.New("w13c boom")
	}
	if _, err := parser.ParseJSONBody(context.Background(), []byte(`{}`), time.Second); err == nil {
		t.Fatal("failing hook must surface its error")
	}
}

func TestW13CParseJSONValueTrailingContent(t *testing.T) {
	if _, err := ParseJSONValue([]byte(`{"a":1} {}`)); !IsInvalidJSONError(err) {
		t.Fatalf("trailing object must be invalid JSON, got %v", err)
	}
	if _, err := ParseJSONValue([]byte(`{"a":1} x`)); !IsInvalidJSONError(err) {
		t.Fatalf("trailing garbage must be invalid JSON, got %v", err)
	}
}

func TestW13CConvertJSONNumberEdges(t *testing.T) {
	if _, err := convertJSONNumbers(map[string]any{"a": json.Number("1.2.3")}); err == nil {
		t.Fatal("map with invalid number must fail")
	}
	if _, err := convertJSONNumbers([]any{json.Number("1.2.3")}); err == nil {
		t.Fatal("slice with invalid number must fail")
	}
	if _, err := convertJSONNumbers(json.Number("1.2.3")); err == nil {
		t.Fatal("invalid number must fail")
	}
	if value, err := convertJSONNumber("1e999"); err != nil || value == 0 {
		t.Fatalf("out-of-range literal must keep the extreme value, got %v %v", value, err)
	}
	if _, err := convertJSONNumber("1.2.3"); err == nil {
		t.Fatal("syntax-invalid literal must fail")
	}
}

func TestW13CReadRawBodyDefaultLimit(t *testing.T) {
	m := &Middleware{limiter: NewInFlightLimiter(), parser: NewJSONParser(JSONParserOptions{PoolSize: 1}), logger: DiscardLogger{}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"a":1}`))
	raw, perr := m.ReadRawBody(recorder, request)
	if perr != nil || string(raw) != `{"a":1}` {
		t.Fatalf("small body must read cleanly, got %q %v", raw, perr)
	}
}

func TestW13CHandleParserRejectionStatusAndLimitFallbacks(t *testing.T) {
	m := NewMiddleware(Config{})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if !m.HandleParserRejection(recorder, request, &RawBodyParserError{StatusCode: 700}) {
		t.Fatal("parser error must be handled")
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("out-of-range status must clamp to 400, got %d", recorder.Code)
	}
	large := httptest.NewRecorder()
	if !m.HandleParserRejection(large, request, &RawBodyParserError{Type: "entity.too.large", StatusCode: http.StatusRequestEntityTooLarge}) {
		t.Fatal("too-large parser error must be handled")
	}
	if large.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("too-large status must stay 413, got %d", large.Code)
	}
}

func TestW13CCaptureDeferredLargeJSONWarnBranch(t *testing.T) {
	logger := &w13cCaptureLogger{}
	m := NewMiddleware(Config{Logger: logger})
	rawBody := []byte(`{"model":"m"}` + strings.Repeat(" ", GatewayJSONBodyLargeWarningBytes+16))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	request.Header.Set("Content-Type", "application/json")
	req, err := m.Capture(recorder, request, rawBody)
	if err != nil || req == nil {
		t.Fatalf("large JSON body must capture, got %v %v", req, err)
	}
	if logger.warnCount == 0 {
		t.Fatal("body above the warning threshold must log a warning")
	}
	if req.State == nil || req.State.JSONParseStatus != JSONParseStatusScannedJSON {
		t.Fatalf("deferred scan must produce scanned status, got %+v", req.State)
	}
	req.ReleaseInFlight()
}

func TestW13CForEachMultipartPartParseAndTruncationErrors(t *testing.T) {
	if err := forEachMultipartPart(nil, "multipart/mixed; boundary=", 10, nil); err == nil {
		t.Fatal("empty boundary must fail media type parsing")
	}
	truncated := []byte("--w13c\r\nContent-Disposition: form-data; name=\"a\"\r\n\r\nv")
	if err := forEachMultipartPart(truncated, "multipart/mixed; boundary=w13c", 10, nil); err == nil {
		t.Fatal("truncated multipart body must surface an error")
	}
}

func TestW13CParseRequestJSONBodyReusedAndNilContext(t *testing.T) {
	parser := NewJSONParser(JSONParserOptions{PoolSize: 1})
	parsed := map[string]any{"a": 1.0}
	req := &Request{Body: parsed, ctx: context.Background()}
	value, err := parser.ParseRequestJSONBody(nil, req, time.Second)
	if err != nil || value == nil {
		t.Fatalf("nil context with parsed body must reuse it, got %v %v", value, err)
	}

	buffered := &Request{Body: []byte(`{"a":1}`), RawBody: []byte(`{"a":1}`), ctx: context.Background()}
	buffered.parsedAvailable = true
	buffered.parsedBody = map[string]any{"a": 2.0}
	value, err = parser.ParseRequestJSONBody(context.Background(), buffered, time.Second)
	if err != nil || value == nil {
		t.Fatalf("parsedAvailable must short-circuit, got %v %v", value, err)
	}
	parser.Stop()
}

func TestW13CParseRequestJSONBodyRawBodyReplacedMidFlight(t *testing.T) {
	release := make(chan struct{})
	var startedOnce sync.Once
	started := make(chan struct{})
	parser := NewJSONParser(JSONParserOptions{PoolSize: 1})
	parser.parseFunc = func(ctx context.Context, raw []byte) (any, error) {
		startedOnce.Do(func() { close(started) })
		<-release
		return map[string]any{"v": 1.0}, nil
	}

	req := &Request{RawBody: []byte(`{"a":1}`), ctx: context.Background()}
	type callResult struct {
		value any
		err   error
	}
	done := make(chan callResult, 1)
	go func() {
		value, err := parser.ParseRequestJSONBody(context.Background(), req, 5*time.Second)
		done <- callResult{value, err}
	}()
	<-started
	req.RawBody = []byte(`{"a":1,"b":22}`)
	close(release)
	result := <-done
	if result.err != nil || result.value == nil {
		t.Fatalf("replaced raw body must re-materialize, got %v %v", result.value, result.err)
	}
	if string(req.RawBody) != `{"a":1,"b":22}` {
		t.Fatalf("replacement must be preserved, got %q", req.RawBody)
	}
	parser.Stop()
}

func TestW13CAwaitMaterializationPaths(t *testing.T) {
	parser := NewJSONParser(JSONParserOptions{PoolSize: 1})

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	mat := &materialization{raw: []byte(`{}`), wait: make(chan struct{})}
	if _, err := parser.awaitMaterialization(canceled, mat, time.Second); !IsCanceledError(err) {
		t.Fatalf("canceled ctx must fail canceled, got %v", err)
	}

	lateCancel, lateCancelFn := context.WithCancel(context.Background())
	waiting := &materialization{raw: []byte(`{}`), wait: make(chan struct{})}
	go func() {
		time.Sleep(20 * time.Millisecond)
		lateCancelFn()
	}()
	if _, err := parser.awaitMaterialization(lateCancel, waiting, 5*time.Second); !IsCanceledError(err) {
		t.Fatalf("cancel during wait must fail canceled, got %v", err)
	}

	completed := &materialization{raw: []byte(`{}`), wait: make(chan struct{})}
	go func() {
		time.Sleep(20 * time.Millisecond)
		completed.mu.Lock()
		completed.result = jsonWorkerResult{value: map[string]any{"a": 1.0}}
		completed.mu.Unlock()
		close(completed.wait)
	}()
	ctx, ctxCancel := context.WithCancel(context.Background())
	defer ctxCancel()
	value, err := parser.awaitMaterialization(ctx, completed, 5*time.Second)
	if err != nil || value == nil {
		t.Fatalf("completed wait must return the result, got %v %v", value, err)
	}
	parser.Stop()
}

type w13cCaptureLogger struct {
	warnCount int
}

func (l *w13cCaptureLogger) Debug(string, map[string]any) {}
func (l *w13cCaptureLogger) Info(string, map[string]any)  {}
func (l *w13cCaptureLogger) Warn(string, map[string]any)  { l.warnCount++ }
func (l *w13cCaptureLogger) Error(string, map[string]any) {}
