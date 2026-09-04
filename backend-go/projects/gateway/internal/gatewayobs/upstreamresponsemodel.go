package gatewayobs

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// 上游响应模型审计，逐行为对齐
// backend/src/modules/gateway/observability/upstream-response-model.ts。
// 解析仅作观测：任何解析失败都不影响转发（Node 的 try/catch 语义在此即
// 「错误只导致拿不到模型」）。

// UpstreamResponseModelProtocol mirrors UpstreamResponseModelProtocol.
type UpstreamResponseModelProtocol string

const (
	UpstreamResponseModelProtocolOpenAI    UpstreamResponseModelProtocol = "openai"
	UpstreamResponseModelProtocolAnthropic UpstreamResponseModelProtocol = "anthropic"
	UpstreamResponseModelProtocolGemini    UpstreamResponseModelProtocol = "gemini"
)

const maxObservedModelLength = 200
const maxSseEventBytes = 256 * 1024
const maxJsonResponseBytes = 1024 * 1024

// UpstreamResponseModelObserverOptions mirrors the private options shape.
type UpstreamResponseModelObserverOptions struct {
	Protocol UpstreamResponseModelProtocol
	SSE      bool
}

// UpstreamResponseModelObservation mirrors UpstreamResponseModelObservation.
// Node 的 model getter 缺省为 undefined，这里以空字符串表达。
type UpstreamResponseModelObservation struct {
	protocol UpstreamResponseModelProtocol
	sse      bool
	decoder  utf8StringDecoder

	firstModel     string
	terminalModel  string
	completed      bool
	pendingLine    string
	eventName      string
	dataLines      []string
	dataBytes      int
	eventOversized bool

	jsonChunks    [][]byte
	jsonBytes     int
	jsonOversized bool
	conflict      bool
}

// CreateUpstreamResponseModelObservation mirrors
// createUpstreamResponseModelObservation.
func CreateUpstreamResponseModelObservation(options UpstreamResponseModelObserverOptions) *UpstreamResponseModelObservation {
	return &UpstreamResponseModelObservation{protocol: options.Protocol, sse: options.SSE}
}

// Protocol mirrors the protocol field.
func (observation *UpstreamResponseModelObservation) Protocol() UpstreamResponseModelProtocol {
	return observation.protocol
}

// Model mirrors the model getter: terminalModel ?? firstModel.
func (observation *UpstreamResponseModelObservation) Model() string {
	if observation.terminalModel != "" {
		return observation.terminalModel
	}
	return observation.firstModel
}

// Conflict mirrors the conflict field.
func (observation *UpstreamResponseModelObservation) Conflict() bool {
	return observation.conflict
}

// Observe mirrors observe: completed 后与空 chunk 直接忽略。
func (observation *UpstreamResponseModelObservation) Observe(chunk []byte) {
	if observation.completed || len(chunk) == 0 {
		return
	}
	if observation.sse {
		observation.observeSseText(observation.decoder.write(chunk))
		return
	}
	observation.observeJSONChunk(chunk)
}

// Finish mirrors finish.
func (observation *UpstreamResponseModelObservation) Finish() {
	if observation.completed {
		return
	}
	observation.completed = true
	if observation.sse {
		if trailing := observation.decoder.end(); trailing != "" {
			observation.observeSseText(trailing)
		}
		if observation.pendingLine != "" {
			line := observation.pendingLine
			observation.pendingLine = ""
			observation.consumeSseLine(line)
		}
		observation.consumeSseEvent()
	} else {
		observation.finishJSONObservation()
	}
	observation.jsonChunks = nil
}

func (observation *UpstreamResponseModelObservation) observeJSONChunk(chunk []byte) {
	if observation.jsonOversized {
		return
	}
	observation.jsonBytes += len(chunk)
	if observation.jsonBytes > maxJsonResponseBytes {
		observation.jsonOversized = true
		observation.jsonChunks = nil
		return
	}
	buffer := make([]byte, len(chunk))
	copy(buffer, chunk)
	observation.jsonChunks = append(observation.jsonChunks, buffer)
}

func (observation *UpstreamResponseModelObservation) finishJSONObservation() {
	if observation.jsonOversized || len(observation.jsonChunks) == 0 {
		return
	}
	observation.observeJSONPayload(bytes.Join(observation.jsonChunks, nil), "")
}

func (observation *UpstreamResponseModelObservation) observeSseText(text string) {
	if text == "" {
		return
	}
	observation.pendingLine += text
	for {
		lineBreak := strings.IndexByte(observation.pendingLine, '\n')
		if lineBreak < 0 {
			if len(observation.pendingLine) > maxSseEventBytes {
				observation.pendingLine = ""
				observation.resetSseEvent(true)
			}
			return
		}
		line := observation.pendingLine[:lineBreak]
		line = strings.TrimSuffix(line, "\r")
		observation.pendingLine = observation.pendingLine[lineBreak+1:]
		observation.consumeSseLine(line)
	}
}

func (observation *UpstreamResponseModelObservation) consumeSseLine(line string) {
	if len(line) == 0 {
		observation.consumeSseEvent()
		return
	}
	if strings.HasPrefix(line, "event:") {
		observation.eventName = trimJSText(line[len("event:"):])
		return
	}
	if !strings.HasPrefix(line, "data:") || observation.eventOversized {
		return
	}
	data := line[len("data:"):]
	data = stripOneLeadingWhitespace(data)
	observation.dataBytes += len(data)
	if observation.dataBytes > maxSseEventBytes {
		observation.resetSseEvent(true)
		return
	}
	observation.dataLines = append(observation.dataLines, data)
}

func (observation *UpstreamResponseModelObservation) consumeSseEvent() {
	if !observation.eventOversized && len(observation.dataLines) > 0 {
		observation.observeJSONPayload([]byte(strings.Join(observation.dataLines, "\n")), observation.eventName)
	}
	observation.resetSseEvent(false)
}

func (observation *UpstreamResponseModelObservation) resetSseEvent(oversized bool) {
	observation.eventName = ""
	observation.dataLines = nil
	observation.dataBytes = 0
	observation.eventOversized = oversized
}

func (observation *UpstreamResponseModelObservation) observeJSONPayload(payload []byte, eventName string) {
	var value interface{}
	if err := json.Unmarshal(payload, &value); err != nil {
		return
	}
	record, ok := value.(map[string]interface{})
	if !ok {
		return
	}
	model := observation.modelFromPayload(record)
	if model == "" {
		return
	}
	observation.observeModel(model, observation.isTerminalEvent(record, eventName))
}

func (observation *UpstreamResponseModelObservation) modelFromPayload(value map[string]interface{}) string {
	if observation.protocol == UpstreamResponseModelProtocolAnthropic {
		if model := modelText(recordValue(value["message"])["model"]); model != "" {
			return model
		}
		return modelText(value["model"])
	}
	if observation.protocol == UpstreamResponseModelProtocolGemini {
		if model := modelText(value["modelVersion"]); model != "" {
			return model
		}
		return modelText(recordValue(value["response"])["modelVersion"])
	}
	if model := modelText(recordValue(value["response"])["model"]); model != "" {
		return model
	}
	return modelText(value["model"])
}

func (observation *UpstreamResponseModelObservation) isTerminalEvent(value map[string]interface{}, eventName string) bool {
	if observation.protocol == UpstreamResponseModelProtocolGemini {
		return true
	}
	if observation.protocol != UpstreamResponseModelProtocolOpenAI {
		return false
	}
	eventType := trimJSText(eventName)
	if eventType == "" {
		eventType = modelText(value["type"])
	}
	switch eventType {
	case "response.completed", "response.done", "response.failed", "response.incomplete", "response.cancelled", "response.canceled":
		return true
	}
	return false
}

func (observation *UpstreamResponseModelObservation) observeModel(model string, terminal bool) {
	current := observation.Model()
	if current != "" && current != model {
		observation.conflict = true
	}
	if terminal {
		observation.terminalModel = model
		return
	}
	if observation.firstModel == "" {
		observation.firstModel = model
	}
}

// stripOneLeadingWhitespace mirrors replace(/^\s/, ”)：只剥一个空白字符。
func stripOneLeadingWhitespace(value string) string {
	if value == "" {
		return value
	}
	r, size := utf8.DecodeRuneInString(value)
	if isJSWhitespaceRune(r) {
		return value[size:]
	}
	return value
}

// modelText mirrors modelText：字符串、trim 后非空且码点数 <= 200。
func modelText(value interface{}) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	model := strings.TrimSpace(text)
	if model == "" || utf8.RuneCountInString(model) > maxObservedModelLength {
		return ""
	}
	return model
}

func recordValue(value interface{}) map[string]interface{} {
	record, _ := value.(map[string]interface{})
	return record
}

// UpstreamResponseModelRequestInfo mirrors
// upstreamResponseModelProtocolForRequest 的入参对象。Headers 使用
// http.Header（键为 canonical 形式；has 语义按存在性判断）。
type UpstreamResponseModelRequestInfo struct {
	Headers      http.Header
	UpstreamURL  string
	ProviderCode string
	ProtocolCode string
}

// UpstreamResponseModelProtocolForRequest mirrors
// upstreamResponseModelProtocolForRequest.
func UpstreamResponseModelProtocolForRequest(input UpstreamResponseModelRequestInfo) UpstreamResponseModelProtocol {
	protocolCode := strings.ToLower(strings.TrimSpace(input.ProtocolCode))
	if strings.Contains(protocolCode, "anthropic") {
		return UpstreamResponseModelProtocolAnthropic
	}
	if strings.Contains(protocolCode, "gemini") {
		return UpstreamResponseModelProtocolGemini
	}
	if strings.Contains(protocolCode, "openai") {
		return UpstreamResponseModelProtocolOpenAI
	}
	if hasUpstreamHeader(input.Headers, "anthropic-version") {
		return UpstreamResponseModelProtocolAnthropic
	}
	if isGoogleOpenAICompatibleUpstreamURL(input.UpstreamURL) {
		return UpstreamResponseModelProtocolOpenAI
	}
	if hasUpstreamHeader(input.Headers, "x-goog-api-key") || hasUpstreamHeader(input.Headers, "x-goog-user-project") || isGeminiNativeUpstreamURL(input.UpstreamURL) {
		return UpstreamResponseModelProtocolGemini
	}
	providerCode := strings.ToLower(strings.TrimSpace(input.ProviderCode))
	if providerCode == "anthropic" {
		return UpstreamResponseModelProtocolAnthropic
	}
	if providerCode == "gemini" {
		return UpstreamResponseModelProtocolGemini
	}
	return UpstreamResponseModelProtocolOpenAI
}

func hasUpstreamHeader(headers http.Header, name string) bool {
	if headers == nil {
		return false
	}
	_, exists := headers[textproto.CanonicalMIMEHeaderKey(name)]
	return exists
}

var googleOpenAIPathPattern = regexp.MustCompile(`(?i)(^|/)openai(/|$)`)

func isGoogleOpenAICompatibleUpstreamURL(value string) bool {
	hostname, path, ok := parseUpstreamURLParts(value)
	if !ok {
		return false
	}
	if hostname != "generativelanguage.googleapis.com" && !strings.HasSuffix(hostname, ".googleapis.com") {
		return false
	}
	return googleOpenAIPathPattern.MatchString(path)
}

func isGeminiNativeUpstreamURL(value string) bool {
	hostname, _, ok := parseUpstreamURLParts(value)
	if !ok {
		return false
	}
	return hostname == "generativelanguage.googleapis.com" ||
		(strings.HasSuffix(hostname, ".googleapis.com") && strings.Contains(hostname, "cloudcode"))
}

// parseUpstreamURLParts mirrors new URL(value)：解析失败或无主机名视为非法。
func parseUpstreamURLParts(value string) (string, string, bool) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", "", false
	}
	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "" {
		return "", "", false
	}
	return hostname, parsed.Path, true
}

// ObserveUpstreamResponseModelBody mirrors observeUpstreamResponseModelBody:
// Node 的异步迭代包装在此成为 io.Reader 包装——每个底层 chunk 先观察再透传，
// EOF 时调用 Finish；观察器绝不改写字节。
func ObserveUpstreamResponseModelBody(body io.Reader, observation *UpstreamResponseModelObservation) io.Reader {
	return &observingUpstreamBodyReader{body: body, observation: observation}
}

type observingUpstreamBodyReader struct {
	body        io.Reader
	observation *UpstreamResponseModelObservation
	finished    bool
}

func (reader *observingUpstreamBodyReader) Read(p []byte) (int, error) {
	n, err := reader.body.Read(p)
	if n > 0 {
		reader.observation.Observe(p[:n])
	}
	if err == io.EOF && !reader.finished {
		reader.finished = true
		reader.observation.Finish()
	}
	return n, err
}

// ---------------------------------------------------------------------------
// Node StringDecoder('utf8') 的增量 UTF-8 解码镜像
// ---------------------------------------------------------------------------

// utf8StringDecoder mirrors StringDecoder('utf8')：write 缓冲可能跨 chunk 的
// 不完整多字节序列，end() 以一个 U+FFFD 结尾（Node lastNeed 语义）。
type utf8StringDecoder struct {
	pending []byte
}

func (decoder *utf8StringDecoder) write(chunk []byte) string {
	buffer := decoder.pending
	decoder.pending = nil
	buffer = append(buffer, chunk...)
	var out strings.Builder
	index := 0
	for index < len(buffer) {
		r, size := utf8.DecodeRune(buffer[index:])
		if r == utf8.RuneError && size == 1 {
			if incomplete := incompleteUTF8SequenceLength(buffer[index:]); incomplete > 0 {
				decoder.pending = append(decoder.pending[:0], buffer[index:]...)
				break
			}
			out.WriteRune(utf8.RuneError)
			index += 1
			continue
		}
		out.Write(buffer[index : index+size])
		index += size
	}
	return out.String()
}

func (decoder *utf8StringDecoder) end() string {
	if len(decoder.pending) == 0 {
		return ""
	}
	decoder.pending = nil
	return "\uFFFD"
}

// incompleteUTF8SequenceLength returns the buffered length when the bytes form
// a valid but incomplete multi-byte sequence prefix, otherwise 0.
func incompleteUTF8SequenceLength(remaining []byte) int {
	if len(remaining) == 0 {
		return 0
	}
	lead := remaining[0]
	var expected int
	switch {
	case lead >= 0xC2 && lead <= 0xDF:
		expected = 2
	case lead >= 0xE0 && lead <= 0xEF:
		expected = 3
	case lead >= 0xF0 && lead <= 0xF4:
		expected = 4
	default:
		return 0
	}
	if len(remaining) >= expected {
		return 0
	}
	for index := 1; index < len(remaining); index += 1 {
		if remaining[index]&0xC0 != 0x80 {
			return 0
		}
	}
	return len(remaining)
}
