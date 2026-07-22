package gatewayopenai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

const (
	DefaultMaxNonStreamJSONBytes = 16 << 20
	MaxNonStreamJSONBytes        = 64 << 20
)

var (
	ErrPayloadTooLarge       = errors.New("openai non-stream payload too large")
	ErrInvalidMaxBytes       = errors.New("invalid openai non-stream payload limit")
	ErrInvalidJSON           = errors.New("invalid openai non-stream JSON")
	ErrInvalidInterpretation = errors.New("invalid openai response interpretation")
	ErrInvalidEndpointFamily = errors.New("invalid openai endpoint family")
)

var serviceTierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type Interpretation string

const (
	InterpretOpaque Interpretation = ""
	InterpretOpenAI Interpretation = "openai"
)

type EndpointFamily string

const (
	EndpointUnknown         EndpointFamily = ""
	EndpointChatCompletions EndpointFamily = "chat_completions"
	EndpointResponses       EndpointFamily = "responses"
)

type FrameKind string

const (
	FrameOutputTextDone FrameKind = "output_text_done"
	FrameError          FrameKind = "error"
	FrameCompleted      FrameKind = "completed"
	FrameUsage          FrameKind = "usage"
	FrameRawJSONPath    FrameKind = "raw_json_path"
)

type ParseOptions struct {
	Interpretation Interpretation
	EndpointFamily EndpointFamily
	MaxBytes       int
}

type Result struct {
	Opaque   bool
	Usage    Usage
	Frames   []Frame
	Document *JSONDocument
}

// JSONDocument retains an exact, bounded OpenAI JSON value for response-policy
// path checks without exposing a mutable generic map to callers.
type JSONDocument struct {
	root map[string]any
}

// PathExists implements the response-policy meaning of existence: null, false,
// blank strings, empty arrays, and empty objects are not meaningful values.
func (document *JSONDocument) PathExists(path string) bool {
	if document == nil {
		return false
	}
	parts := strings.Split(path, ".")
	normalized := parts[:0]
	for _, part := range parts {
		if part = trimECMAScriptWhitespace(part); part != "" {
			normalized = append(normalized, part)
		}
	}
	if len(normalized) == 0 {
		return false
	}
	var current any = document.root
	for _, part := range normalized {
		switch value := current.(type) {
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(value) {
				return false
			}
			current = value[index]
		case map[string]any:
			var exists bool
			current, exists = value[part]
			if !exists {
				return false
			}
		default:
			return false
		}
	}
	return meaningfulJSONValue(current)
}

type Usage struct {
	ServiceTier        string
	InputTokens        *int64
	OutputTokens       *int64
	CacheReadTokens    *int64
	CacheWriteTokens   *int64
	CacheWrite1hTokens *int64
	ThinkingTokens     *int64
	InputImageTokens   *int64
	OutputImageTokens  *int64
	InputAudioTokens   *int64
	OutputAudioTokens  *int64
	OutputImageCount   *int64
}

func (usage Usage) Empty() bool {
	return usage.ServiceTier == "" && usage.InputTokens == nil && usage.OutputTokens == nil &&
		usage.CacheReadTokens == nil && usage.CacheWriteTokens == nil && usage.CacheWrite1hTokens == nil &&
		usage.ThinkingTokens == nil && usage.InputImageTokens == nil && usage.OutputImageTokens == nil &&
		usage.InputAudioTokens == nil && usage.OutputAudioTokens == nil && usage.OutputImageCount == nil
}

type Frame struct {
	Kind           FrameKind
	EndpointFamily EndpointFamily
	Text           string
	ErrorCode      string
	ErrorType      string
	ErrorMessage   string
	FinishReason   string
	Status         string
	Usage          *Usage
	JSONPaths      []string
	ChoiceIndex    *int
	OutputIndex    *int
	ContentIndex   *int
	VisibleOutput  bool
	Document       *JSONDocument
}

type PayloadTooLargeError struct {
	Limit  int
	Actual int
}

func (err *PayloadTooLargeError) Error() string {
	return fmt.Sprintf("openai non-stream payload is %d bytes, limit is %d: %v", err.Actual, err.Limit, ErrPayloadTooLarge)
}

func (err *PayloadTooLargeError) Unwrap() error { return ErrPayloadTooLarge }

// ParseNonStreamJSON extracts only bounded OpenAI response facts. The zero-value
// interpretation is deliberately opaque so generic clients cannot be parsed by
// accidental default configuration.
func ParseNonStreamJSON(body []byte, options ParseOptions) (Result, error) {
	maxBytes, err := parseLimit(options.MaxBytes)
	if err != nil {
		return Result{}, err
	}
	if len(body) > maxBytes {
		return Result{}, &PayloadTooLargeError{Limit: maxBytes, Actual: len(body)}
	}
	if err := validateEndpointFamily(options.EndpointFamily); err != nil {
		return Result{}, err
	}
	switch options.Interpretation {
	case InterpretOpaque:
		return Result{Opaque: true}, nil
	case InterpretOpenAI:
	default:
		return Result{}, fmt.Errorf("%w: %q", ErrInvalidInterpretation, options.Interpretation)
	}

	root, err := decodeJSONObject(body)
	if err != nil {
		return Result{}, err
	}
	usage := extractUsage(root)
	document := &JSONDocument{root: root}
	frames := make([]Frame, 0, 6)
	if rootError := objectValue(root["error"]); rootError != nil {
		frames = append(frames, errorFrame(rootError, options.EndpointFamily, "error"))
	}
	if response := objectValue(root["response"]); response != nil {
		if responseError := objectValue(response["error"]); responseError != nil {
			frames = append(frames, errorFrame(responseError, options.EndpointFamily, "response.error"))
		}
	}
	switch options.EndpointFamily {
	case EndpointChatCompletions:
		frames = append(frames, extractChatFrames(root)...)
	case EndpointResponses:
		frames = append(frames, extractResponsesFrames(root)...)
	case EndpointUnknown:
		frames = append(frames, extractChatFrames(root)...)
		frames = append(frames, extractResponsesFrames(root)...)
	}
	if objectValue(root["usage"]) != nil && usage.hasMeteredValue() {
		usageCopy := cloneUsage(usage)
		frames = append(frames, Frame{
			Kind:           FrameUsage,
			EndpointFamily: options.EndpointFamily,
			Usage:          &usageCopy,
			JSONPaths:      []string{"usage"},
		})
	}
	frames = append(frames, Frame{
		Kind:           FrameRawJSONPath,
		EndpointFamily: options.EndpointFamily,
	})
	for index := range frames {
		frames[index].Document = document
	}
	return Result{Usage: usage, Frames: frames, Document: document}, nil
}

func parseLimit(configured int) (int, error) {
	if configured == 0 {
		return DefaultMaxNonStreamJSONBytes, nil
	}
	if configured < 0 || configured > MaxNonStreamJSONBytes {
		return 0, fmt.Errorf("%w: %d (allowed 1..%d)", ErrInvalidMaxBytes, configured, MaxNonStreamJSONBytes)
	}
	return configured, nil
}

func validateEndpointFamily(family EndpointFamily) error {
	switch family {
	case EndpointUnknown, EndpointChatCompletions, EndpointResponses:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidEndpointFamily, family)
	}
}

func decodeJSONObject(body []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidJSON, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("%w: multiple JSON values", ErrInvalidJSON)
		}
		return nil, fmt.Errorf("%w: trailing data: %v", ErrInvalidJSON, err)
	}
	root := objectValue(value)
	if root == nil {
		return nil, fmt.Errorf("%w: top-level value must be an object", ErrInvalidJSON)
	}
	return root, nil
}

func extractUsage(root map[string]any) Usage {
	usage := objectValue(root["usage"])
	if usage == nil {
		return Usage{ServiceTier: serviceTierValue(root["service_tier"])}
	}
	responsesInputDetails := objectValue(usage["input_tokens_details"])
	chatInputDetails := objectValue(usage["prompt_tokens_details"])
	outputDetails := objectValue(usage["output_tokens_details"])
	if outputDetails == nil {
		outputDetails = objectValue(usage["completion_tokens_details"])
	}
	responsesCacheCreation := nestedObject(responsesInputDetails, "cache_creation")
	chatCacheCreation := nestedObject(chatInputDetails, "cache_creation")
	rootCacheCreation := objectValue(usage["cache_creation"])

	cacheWrite5mTokens := firstCount(
		field(responsesInputDetails, "cache_write_5m_tokens"),
		field(responsesInputDetails, "cache_write_5m_input_tokens"),
		field(responsesInputDetails, "cache_creation_5m_tokens"),
		field(responsesInputDetails, "cache_creation_5m_input_tokens"),
		field(responsesCacheCreation, "ephemeral_5m_input_tokens"),
		field(chatInputDetails, "cache_write_5m_tokens"),
		field(chatInputDetails, "cache_write_5m_input_tokens"),
		field(chatInputDetails, "cache_creation_5m_tokens"),
		field(chatInputDetails, "cache_creation_5m_input_tokens"),
		field(chatCacheCreation, "ephemeral_5m_input_tokens"),
		usage["cache_write_5m_tokens"],
		usage["cache_write_5m_input_tokens"],
		usage["cache_creation_5m_tokens"],
		usage["cache_creation_5m_input_tokens"],
		usage["cache_creation_5_m_tokens"],
		usage["claude_cache_creation_5m_tokens"],
		usage["claude_cache_creation_5_m_tokens"],
		field(rootCacheCreation, "ephemeral_5m_input_tokens"),
	)
	cacheWrite1hTokens := firstCount(
		field(responsesInputDetails, "cache_write_1h_tokens"),
		field(responsesInputDetails, "cache_write_1h_input_tokens"),
		field(responsesInputDetails, "cache_creation_1h_tokens"),
		field(responsesInputDetails, "cache_creation_1h_input_tokens"),
		field(responsesCacheCreation, "ephemeral_1h_input_tokens"),
		field(chatInputDetails, "cache_write_1h_tokens"),
		field(chatInputDetails, "cache_write_1h_input_tokens"),
		field(chatInputDetails, "cache_creation_1h_tokens"),
		field(chatInputDetails, "cache_creation_1h_input_tokens"),
		field(chatCacheCreation, "ephemeral_1h_input_tokens"),
		usage["cache_write_1h_tokens"],
		usage["cache_write_1h_input_tokens"],
		usage["cache_creation_1h_tokens"],
		usage["cache_creation_1h_input_tokens"],
		usage["cache_creation_1_h_tokens"],
		usage["claude_cache_creation_1h_tokens"],
		usage["claude_cache_creation_1_h_tokens"],
		field(rootCacheCreation, "ephemeral_1h_input_tokens"),
	)
	cacheWriteDetailTokens := sumCounts(cacheWrite5mTokens, cacheWrite1hTokens)
	cacheWriteTokens := firstCount(
		field(responsesInputDetails, "cache_write_tokens"),
		field(responsesInputDetails, "cache_write_input_tokens"),
		field(responsesInputDetails, "cache_creation_tokens"),
		field(responsesInputDetails, "cache_creation_input_tokens"),
		field(chatInputDetails, "cache_write_tokens"),
		field(chatInputDetails, "cache_write_input_tokens"),
		field(chatInputDetails, "cache_creation_tokens"),
		field(chatInputDetails, "cache_creation_input_tokens"),
		usage["cache_write_tokens"],
		usage["cache_write_input_tokens"],
		usage["cache_creation_tokens"],
		usage["cache_creation_input_tokens"],
		cacheWriteDetailTokens,
	)

	outputImageCount := firstCount(usage["output_image_count"], usage["output_images"], usage["image_count"])
	if outputImageCount != nil && *outputImageCount <= 0 {
		outputImageCount = nil
	}
	return Usage{
		ServiceTier:        serviceTierValue(root["service_tier"]),
		InputTokens:        firstCount(usage["input_tokens"], usage["prompt_tokens"]),
		OutputTokens:       firstCount(usage["output_tokens"], usage["completion_tokens"]),
		CacheReadTokens:    firstCount(field(responsesInputDetails, "cached_tokens"), field(chatInputDetails, "cached_tokens"), usage["prompt_cache_hit_tokens"]),
		CacheWriteTokens:   cacheWriteTokens,
		CacheWrite1hTokens: cacheWrite1hTokens,
		ThinkingTokens:     firstCount(field(outputDetails, "reasoning_tokens")),
		InputImageTokens:   firstCount(field(responsesInputDetails, "image_tokens"), field(chatInputDetails, "image_tokens")),
		OutputImageTokens:  firstCount(field(outputDetails, "image_tokens")),
		InputAudioTokens:   firstCount(field(responsesInputDetails, "audio_tokens"), field(chatInputDetails, "audio_tokens")),
		OutputAudioTokens:  firstCount(field(outputDetails, "audio_tokens")),
		OutputImageCount:   outputImageCount,
	}
}

func extractChatFrames(root map[string]any) []Frame {
	choices, _ := root["choices"].([]any)
	frames := make([]Frame, 0, len(choices)*3)
	for choiceIndex, choiceValue := range choices {
		choice := objectValue(choiceValue)
		if choice == nil {
			continue
		}
		message := objectValue(choice["message"])
		finishReason := stringValue(choice["finish_reason"])
		if content := openAITextValue(field(message, "content")); content != "" {
			frames = append(frames, outputFrame(EndpointChatCompletions, content, finishReason, "choices."+strconv.Itoa(choiceIndex)+".message.content", &choiceIndex, nil, nil))
		}
		if reasoning := openAITextValue(field(message, "reasoning_content")); reasoning != "" {
			frames = append(frames, outputFrame(EndpointChatCompletions, reasoning, finishReason, "choices."+strconv.Itoa(choiceIndex)+".message.reasoning_content", &choiceIndex, nil, nil))
		}
		if refusal := openAITextValue(field(message, "refusal")); refusal != "" {
			frames = append(frames, outputFrame(EndpointChatCompletions, refusal, finishReason, "choices."+strconv.Itoa(choiceIndex)+".message.refusal", &choiceIndex, nil, nil))
		}
		if finishReason != "" {
			index := choiceIndex
			frames = append(frames, Frame{
				Kind:           FrameCompleted,
				EndpointFamily: EndpointChatCompletions,
				FinishReason:   finishReason,
				Status:         finishReason,
				ChoiceIndex:    &index,
			})
		}
	}
	return frames
}

func extractResponsesFrames(root map[string]any) []Frame {
	status := stringValue(root["status"])
	frames := make([]Frame, 0, 4)
	if outputText, ok := root["output_text"].(string); ok && outputText != "" {
		frames = append(frames, outputFrame(EndpointResponses, outputText, status, "output_text", nil, nil, nil))
	}
	output, _ := root["output"].([]any)
	for outputIndex, outputValue := range output {
		item := objectValue(outputValue)
		content, _ := field(item, "content").([]any)
		for contentIndex, contentValue := range content {
			entry := objectValue(contentValue)
			outputIndexCopy := outputIndex
			contentIndexCopy := contentIndex
			pathPrefix := "output." + strconv.Itoa(outputIndex) + ".content." + strconv.Itoa(contentIndex)
			if text := openAITextValue(field(entry, "text")); text != "" {
				frames = append(frames, outputFrame(
					EndpointResponses, text, status, pathPrefix+".text", nil, &outputIndexCopy, &contentIndexCopy,
				))
			}
			if refusal := openAITextValue(field(entry, "refusal")); refusal != "" {
				frames = append(frames, outputFrame(
					EndpointResponses, refusal, status, pathPrefix+".refusal", nil, &outputIndexCopy, &contentIndexCopy,
				))
			}
		}
	}
	if status != "" {
		frames = append(frames, Frame{
			Kind:           FrameCompleted,
			EndpointFamily: EndpointResponses,
			FinishReason:   status,
			Status:         status,
		})
	}
	return frames
}

func outputFrame(family EndpointFamily, text, status, path string, choiceIndex, outputIndex, contentIndex *int) Frame {
	return Frame{
		Kind:           FrameOutputTextDone,
		EndpointFamily: family,
		Text:           text,
		FinishReason:   status,
		Status:         status,
		JSONPaths:      []string{path},
		ChoiceIndex:    choiceIndex,
		OutputIndex:    outputIndex,
		ContentIndex:   contentIndex,
		VisibleOutput:  true,
	}
}

func errorFrame(value map[string]any, family EndpointFamily, path string) Frame {
	return Frame{
		Kind:           FrameError,
		EndpointFamily: family,
		ErrorCode:      stringValue(value["code"]),
		ErrorType:      stringValue(value["type"]),
		ErrorMessage:   stringValue(value["message"]),
		JSONPaths:      []string{path},
	}
}

func serviceTierValue(value any) string {
	text, ok := value.(string)
	if !ok || text != strings.TrimSpace(text) || !serviceTierPattern.MatchString(text) {
		return ""
	}
	return text
}

func openAITextValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	items, ok := value.([]any)
	if !ok {
		return ""
	}
	var builder strings.Builder
	for _, item := range items {
		entry := objectValue(item)
		if text, ok := field(entry, "text").(string); ok && text != "" {
			builder.WriteString(text)
		}
	}
	return builder.String()
}

func firstCount(values ...any) *int64 {
	for _, value := range values {
		if count := countValue(value); count != nil {
			return count
		}
	}
	return nil
}

func countValue(value any) *int64 {
	var text string
	switch typed := value.(type) {
	case json.Number:
		text = typed.String()
	case string:
		text = strings.TrimSpace(typed)
		if text == "" {
			return nil
		}
	case *int64:
		if typed == nil {
			return nil
		}
		copy := *typed
		return &copy
	default:
		return nil
	}
	parsed, _, err := big.ParseFloat(text, 10, 256, big.ToZero)
	if err != nil || parsed.Sign() < 0 || parsed.IsInf() {
		return nil
	}
	integer, _ := parsed.Int(nil)
	if !integer.IsInt64() {
		return nil
	}
	count := integer.Int64()
	return &count
}

func (usage Usage) hasMeteredValue() bool {
	return usage.InputTokens != nil || usage.OutputTokens != nil || usage.CacheReadTokens != nil ||
		usage.CacheWriteTokens != nil || usage.CacheWrite1hTokens != nil || usage.ThinkingTokens != nil ||
		usage.InputImageTokens != nil || usage.OutputImageTokens != nil || usage.InputAudioTokens != nil ||
		usage.OutputAudioTokens != nil || usage.OutputImageCount != nil
}

func meaningfulJSONValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return trimECMAScriptWhitespace(typed) != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

func trimECMAScriptWhitespace(value string) string {
	return strings.TrimFunc(value, func(character rune) bool {
		switch character {
		case '\u0009', '\u000A', '\u000B', '\u000C', '\u000D', '\u0020', '\u00A0',
			'\u1680', '\u2028', '\u2029', '\u202F', '\u205F', '\u3000', '\uFEFF':
			return true
		default:
			return character >= '\u2000' && character <= '\u200A'
		}
	})
}

func sumCounts(values ...*int64) *int64 {
	var sum int64
	found := false
	for _, value := range values {
		if value == nil {
			continue
		}
		if *value > math.MaxInt64-sum {
			return nil
		}
		sum += *value
		found = true
	}
	if !found {
		return nil
	}
	return &sum
}

func cloneUsage(value Usage) Usage {
	value.InputTokens = cloneInt64(value.InputTokens)
	value.OutputTokens = cloneInt64(value.OutputTokens)
	value.CacheReadTokens = cloneInt64(value.CacheReadTokens)
	value.CacheWriteTokens = cloneInt64(value.CacheWriteTokens)
	value.CacheWrite1hTokens = cloneInt64(value.CacheWrite1hTokens)
	value.ThinkingTokens = cloneInt64(value.ThinkingTokens)
	value.InputImageTokens = cloneInt64(value.InputImageTokens)
	value.OutputImageTokens = cloneInt64(value.OutputImageTokens)
	value.InputAudioTokens = cloneInt64(value.InputAudioTokens)
	value.OutputAudioTokens = cloneInt64(value.OutputAudioTokens)
	value.OutputImageCount = cloneInt64(value.OutputImageCount)
	return value
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func nestedObject(value map[string]any, key string) map[string]any {
	return objectValue(field(value, key))
}

func field(value map[string]any, key string) any {
	if value == nil {
		return nil
	}
	return value[key]
}

func objectValue(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
