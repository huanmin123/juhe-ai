package openaicompat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// ImageGeneration ports openai-compatible-images/image-generation-executor.ts:
// the executor calls the gateway's own /v1/images/generations endpoint with
// the caller's authorization header and normalizes both JSON and SSE
// responses into the bridge result shape.

// ImageGenerationToolConfig mirrors the tool fields the provider body uses.
type ImageGenerationToolConfig struct {
	Size              string
	Quality           string
	OutputFormat      string
	OutputCompression *float64
	PartialImages     *float64
	Moderation        string
	Background        string
}

// ImageGenerationInput mirrors OpenAIToAnthropicImageGenerationInput.
type ImageGenerationInput struct {
	Prompt string
	Tool   ImageGenerationToolConfig
}

// ImageGenerationResult mirrors OpenAIToAnthropicImageGenerationResult.
type ImageGenerationResult struct {
	ImageBase64   string
	RevisedPrompt string
	OutputFormat  string
}

// ImageGenerationStreamEvent mirrors the partial_image / completed union.
type ImageGenerationStreamEvent struct {
	Type              string // partial_image | completed
	ImageBase64       string
	PartialImageIndex *int
	Result            *ImageGenerationResult
}

// ImageGenerationProviderError mirrors
// OpenAICompatibleImageGenerationProviderError.
type ImageGenerationProviderError struct {
	Message           string
	StatusCode        int
	Type              string
	Code              string
	ModerationDetails map[string]any
}

func (e *ImageGenerationProviderError) Error() string { return e.Message }

// ImageGenerationProviderRuntime mirrors ImageGenerationProviderRuntime.
type ImageGenerationProviderRuntime struct {
	Endpoint      string
	Authorization string
	Model         string
	TimeoutMs     int64
	MaxBodyBytes  int64
}

// HTTPDoer abstracts the transport for mock-first tests.
type HTTPDoer interface {
	Do(request *http.Request) (*http.Response, error)
}

// ImageGenerationExecutor mirrors OpenAIToAnthropicImageGenerationExecutor.
type ImageGenerationExecutor struct {
	Provider ImageGenerationProviderRuntime
	Client   HTTPDoer
}

// NewImageGenerationExecutor mirrors
// openAICompatibleImageGenerationExecutorForGatewayRequest: nil without an
// authorization header; overrides exist only for test seams.
func NewImageGenerationExecutor(config Config, authorization string, client HTTPDoer, overrides *ImageGenerationProviderRuntime) *ImageGenerationExecutor {
	if strings.TrimSpace(authorization) == "" {
		return nil
	}
	config = config.withDefaults()
	provider := ImageGenerationProviderRuntime{
		Endpoint:      fmt.Sprintf("http://127.0.0.1:%d/v1/images/generations", config.Port),
		Authorization: authorization,
		Model:         ImageGenerationProviderModel,
		TimeoutMs:     ImageGenerationProviderTimeoutMs,
		MaxBodyBytes:  ImageGenerationProviderMaxBodyBytes,
	}
	if overrides != nil {
		if overrides.Endpoint != "" {
			provider.Endpoint = overrides.Endpoint
		}
		if overrides.TimeoutMs > 0 {
			provider.TimeoutMs = overrides.TimeoutMs
		}
		if overrides.MaxBodyBytes > 0 {
			provider.MaxBodyBytes = overrides.MaxBodyBytes
		}
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &ImageGenerationExecutor{Provider: provider, Client: client}
}

// ImageGenerationProviderRequestBody mirrors
// imageGenerationImagesProviderRequestBody.
func ImageGenerationProviderRequestBody(provider ImageGenerationProviderRuntime, prompt string, tool ImageGenerationToolConfig, stream bool) map[string]any {
	body := map[string]any{
		"model":  provider.Model,
		"prompt": prompt,
		"n":      1,
	}
	setOptionalString(body, "size", tool.Size)
	setOptionalString(body, "quality", tool.Quality)
	setOptionalString(body, "output_format", tool.OutputFormat)
	if tool.OutputCompression != nil {
		body["output_compression"] = *tool.OutputCompression
	}
	setOptionalString(body, "moderation", tool.Moderation)
	setOptionalString(body, "background", tool.Background)
	if stream {
		body["stream"] = true
		if tool.PartialImages != nil {
			partial := int(*tool.PartialImages)
			if partial < 0 {
				partial = 0
			}
			if partial > 3 {
				partial = 3
			}
			body["partial_images"] = partial
		}
	}
	return body
}

func setOptionalString(target map[string]any, key, value string) {
	if value != "" {
		target[key] = value
	}
}

// Generate mirrors generate: one JSON round-trip.
func (e *ImageGenerationExecutor) Generate(ctx context.Context, input ImageGenerationInput) (*ImageGenerationResult, error) {
	providerBody, err := json.Marshal(ImageGenerationProviderRequestBody(e.Provider, input.Prompt, input.Tool, false))
	if err != nil {
		return nil, e.wrapError(err)
	}
	response, err := e.post(ctx, providerBody, "application/json")
	if err != nil {
		return nil, err
	}
	defer discardResponse(response)
	text, err := readResponseTextWithLimit(response, e.Provider.MaxBodyBytes)
	if err != nil {
		return nil, e.wrapError(err)
	}
	parsed := safeParseJSON(text)
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, imageGenerationProviderErrorFromResponse(response.StatusCode, parsed)
	}
	return imageGenerationResultFromJSON(parsed, input.Tool.OutputFormat)
}

// GenerateStream mirrors generateStream: SSE parsing with partial results and
// a mandatory terminal completed event. Returns a pull iterator; io.EOF ends
// a healthy stream.
func (e *ImageGenerationExecutor) GenerateStream(ctx context.Context, input ImageGenerationInput) (func() (ImageGenerationStreamEvent, error), error) {
	providerBody, err := json.Marshal(ImageGenerationProviderRequestBody(e.Provider, input.Prompt, input.Tool, true))
	if err != nil {
		return nil, e.wrapError(err)
	}
	response, err := e.post(ctx, providerBody, "text/event-stream")
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		defer discardResponse(response)
		text, readErr := readResponseTextWithLimit(response, e.Provider.MaxBodyBytes)
		if readErr != nil {
			return nil, e.wrapError(readErr)
		}
		return nil, imageGenerationProviderErrorFromResponse(response.StatusCode, safeParseJSON(text))
	}
	if !isTextEventStream(response) {
		defer discardResponse(response)
		text, readErr := readResponseTextWithLimit(response, e.Provider.MaxBodyBytes)
		if readErr != nil {
			return nil, e.wrapError(readErr)
		}
		result, resultErr := imageGenerationResultFromJSON(safeParseJSON(text), input.Tool.OutputFormat)
		if resultErr != nil {
			return nil, e.wrapError(resultErr)
		}
		emitted := false
		return func() (ImageGenerationStreamEvent, error) {
			if emitted {
				return ImageGenerationStreamEvent{}, io.EOF
			}
			emitted = true
			return ImageGenerationStreamEvent{Type: "completed", Result: result}, nil
		}, nil
	}
	return e.iterateSSE(ctx, response, input.Tool.OutputFormat), nil
}

// post mirrors requestImageGenerationProvider (timeout + fixed headers).
func (e *ImageGenerationExecutor) post(ctx context.Context, body []byte, accept string) (*http.Response, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(e.Provider.TimeoutMs)*time.Millisecond)
	defer cancel()
	request, err := http.NewRequestWithContext(timeoutCtx, http.MethodPost, e.Provider.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, e.wrapError(err)
	}
	request.Header.Set("accept", accept)
	request.Header.Set("content-type", "application/json")
	request.Header.Set("authorization", e.Provider.Authorization)
	response, err := e.Client.Do(request)
	if err != nil {
		if timeoutCtx.Err() != nil && ctx.Err() == nil {
			return nil, bridgeError("图像生成 provider 请求超时",
				"openai_anthropic_bridge_image_generation_provider_timeout", 504, "upstream_error")
		}
		return nil, e.wrapError(err)
	}
	return response, nil
}

// wrapError mirrors imageGenerationProviderRequestError: contract errors pass
// through, transport failures render the 502 request-failed bridge error.
func (e *ImageGenerationExecutor) wrapError(err error) error {
	switch err.(type) {
	case *BridgeRequestError, *ImageGenerationProviderError:
		return err
	}
	message := "图像生成 provider 请求失败"
	if err != nil {
		message = "图像生成 provider 请求失败：" + err.Error()
	}
	return bridgeError(message,
		"openai_anthropic_bridge_image_generation_provider_request_failed", 502, "upstream_error")
}

// iterateSSE mirrors iterateImageGenerationProviderSse with the same frame
// splitting, byte cap and mandatory completed event.
func (e *ImageGenerationExecutor) iterateSSE(ctx context.Context, response *http.Response, outputFormat string) func() (ImageGenerationStreamEvent, error) {
	reader := bufio.NewReader(response.Body)
	buffer := ""
	total := 0
	completed := false
	finished := false
	var pendingErr error
	return func() (ImageGenerationStreamEvent, error) {
		if pendingErr != nil {
			return ImageGenerationStreamEvent{}, pendingErr
		}
		if finished {
			return ImageGenerationStreamEvent{}, io.EOF
		}
		for {
			if ctx.Err() != nil {
				pendingErr = e.wrapError(ctx.Err())
				return ImageGenerationStreamEvent{}, pendingErr
			}
			chunk := make([]byte, 8192)
			n, readErr := reader.Read(chunk)
			if n > 0 {
				total += n
				if int64(total) > e.Provider.MaxBodyBytes {
					discardResponse(response)
					pendingErr = bridgeError("图像生成 provider 响应体超过读取上限",
						"openai_anthropic_bridge_image_generation_provider_response_too_large", 502, "upstream_error")
					return ImageGenerationStreamEvent{}, pendingErr
				}
				buffer += string(chunk[:n])
				for {
					frame, rest, found := takeNextSSEFrame(buffer)
					if !found {
						break
					}
					buffer = rest
					event, eventErr := imageGenerationProviderStreamEventFromSSEFrame(frame, outputFormat)
					if eventErr != nil {
						discardResponse(response)
						pendingErr = eventErr
						return ImageGenerationStreamEvent{}, pendingErr
					}
					if event == nil {
						continue
					}
					if event.Type == "completed" {
						completed = true
						finished = true
					}
					return *event, nil
				}
			}
			if readErr != nil {
				// Stream ended: process the trailing frame like Node, then
				// enforce the mandatory completed event.
				trailing, trailingErr := imageGenerationProviderStreamEventFromSSEFrame(buffer, outputFormat)
				buffer = ""
				if trailingErr != nil {
					pendingErr = trailingErr
					return ImageGenerationStreamEvent{}, pendingErr
				}
				if trailing != nil {
					if trailing.Type == "completed" {
						completed = true
						finished = true
						return *trailing, nil
					}
					// A trailing partial event is emitted; the completion check
					// runs on the next call with the reader exhausted.
					return *trailing, nil
				}
				if !completed {
					pendingErr = invalidProviderResponse("图像生成 provider streaming 响应缺少最终图片结果")
					return ImageGenerationStreamEvent{}, pendingErr
				}
				finished = true
				return ImageGenerationStreamEvent{}, io.EOF
			}
		}
	}
}

// takeNextSSEFrame mirrors takeNextSseFrame.
func takeNextSSEFrame(buffer string) (frame, rest string, found bool) {
	crlfIndex := strings.Index(buffer, "\r\n\r\n")
	lfIndex := strings.Index(buffer, "\n\n")
	var index, delimiter int
	switch {
	case crlfIndex == -1 && lfIndex == -1:
		return "", "", false
	case crlfIndex == -1:
		index, delimiter = lfIndex, 2
	case lfIndex == -1:
		index, delimiter = crlfIndex, 4
	default:
		if lfIndex < crlfIndex {
			index, delimiter = lfIndex, 2
		} else {
			index, delimiter = crlfIndex, 4
		}
	}
	return buffer[:index], buffer[index+delimiter:], true
}

// imageGenerationProviderStreamEventFromSSEFrame mirrors
// imageGenerationProviderStreamEventFromSseFrame.
func imageGenerationProviderStreamEventFromSSEFrame(frame, outputFormat string) (*ImageGenerationStreamEvent, error) {
	parsedFrame, ok := parseSSEFrame(frame)
	if !ok || parsedFrame.data == "[DONE]" {
		return nil, nil
	}
	parsed := safeParseJSON(parsedFrame.data)
	record, _ := parsed.(map[string]any)
	if record == nil {
		record = map[string]any{}
	}
	eventType := parsedFrame.event
	if eventType == "" {
		if text := stringValue(record["type"]); text != nil {
			eventType = *text
		}
	}
	switch {
	case eventType == "image_generation.partial_image" || eventType == "response.image_generation_call.partial_image":
		imageBase64 := stringValue(record["b64_json"])
		if imageBase64 == nil {
			imageBase64 = stringValue(record["partial_image_b64"])
		}
		if imageBase64 == nil || !looksLikeBase64(*imageBase64) {
			return nil, invalidProviderResponse("图像生成 provider partial image 响应缺少 b64_json")
		}
		return &ImageGenerationStreamEvent{
			Type:              "partial_image",
			ImageBase64:       *imageBase64,
			PartialImageIndex: queryIntegerValue(record["partial_image_index"]),
		}, nil
	case eventType == "image_generation.completed" ||
		eventType == "response.image_generation_call.completed" ||
		eventType == "response.completed":
		result, err := imageGenerationResultFromProviderRecord(record, outputFormat)
		if err != nil {
			return nil, err
		}
		return &ImageGenerationStreamEvent{Type: "completed", Result: result}, nil
	case eventType == "error" || eventType == "response.failed" ||
		eventType == "response.image_generation_call.failed" || isPlainObject(record["error"]):
		responseObject := objectValue(record["response"])
		errPayload := objectValue(record["error"])
		if errPayload == nil {
			errPayload = objectValue(responseObject["error"])
		}
		if errPayload == nil {
			errPayload = record
		}
		return nil, imageGenerationProviderErrorFromResponse(502, errPayload)
	}
	return nil, nil
}

type sseFrame struct {
	event string
	data  string
}

// parseSSEFrame mirrors parseSseFrame: skip blanks/comments, strip one
// leading space, join data lines with \n.
func parseSSEFrame(frame string) (sseFrame, bool) {
	dataLines := []string{}
	event := ""
	for _, rawLine := range strings.Split(strings.ReplaceAll(frame, "\r\n", "\n"), "\n") {
		if rawLine == "" || strings.HasPrefix(rawLine, ":") {
			continue
		}
		field := rawLine
		value := ""
		if colonIndex := strings.Index(rawLine, ":"); colonIndex >= 0 {
			field = rawLine[:colonIndex]
			value = rawLine[colonIndex+1:]
			if strings.HasPrefix(value, " ") {
				value = value[1:]
			}
		}
		if field == "event" {
			event = value
		}
		if field == "data" {
			dataLines = append(dataLines, value)
		}
	}
	if len(dataLines) == 0 {
		return sseFrame{}, false
	}
	return sseFrame{event: event, data: strings.Join(dataLines, "\n")}, true
}

func isTextEventStream(response *http.Response) bool {
	return strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream")
}

// readResponseTextWithLimit mirrors readResponseTextWithLimit.
func readResponseTextWithLimit(response *http.Response, maxBodyBytes int64) (string, error) {
	if response.Body == nil {
		return "", nil
	}
	buffer, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(buffer)) > maxBodyBytes {
		discardResponse(response)
		return "", bridgeError("图像生成 provider 响应体超过读取上限",
			"openai_anthropic_bridge_image_generation_provider_response_too_large", 502, "upstream_error")
	}
	return string(buffer), nil
}

func discardResponse(response *http.Response) {
	if response == nil || response.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	_ = response.Body.Close()
}

// imageGenerationDataItem mirrors imageGenerationDataItem (data[0] object).
func imageGenerationDataItem(value any) map[string]any {
	record, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	list, ok := record["data"].([]any)
	if !ok || len(list) == 0 {
		return nil
	}
	first, _ := list[0].(map[string]any)
	return first
}

// imageGenerationOutputItemFrom mirrors imageGenerationOutputItem: the first
// output entry typed image_generation_call.
func imageGenerationOutputItemFrom(value any) map[string]any {
	record, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	list, ok := record["output"].([]any)
	if !ok {
		return nil
	}
	for _, item := range list {
		if entry, ok := item.(map[string]any); ok && entry["type"] == "image_generation_call" {
			return entry
		}
	}
	return nil
}

// imageGenerationResultFromJSON mirrors imageGenerationResultFromJson.
func imageGenerationResultFromJSON(value any, outputFormat string) (*ImageGenerationResult, error) {
	record, _ := value.(map[string]any)
	if record == nil {
		record = map[string]any{}
	}
	return imageGenerationResultFromProviderRecord(record, outputFormat)
}

// imageGenerationResultFromProviderRecord mirrors
// imageGenerationResultFromProviderRecord with the full b64/revised chains.
func imageGenerationResultFromProviderRecord(record map[string]any, outputFormat string) (*ImageGenerationResult, error) {
	first := imageGenerationDataItem(record)
	response := objectValue(record["response"])
	item := objectValue(record["item"])
	outputItem := imageGenerationOutputItemFrom(record)
	if outputItem == nil {
		outputItem = imageGenerationOutputItemFrom(response)
	}
	if outputItem == nil && item != nil && item["type"] == "image_generation_call" {
		outputItem = item
	}
	lookup := func(source map[string]any, key string) *string {
		if source == nil {
			return nil
		}
		return stringValue(source[key])
	}
	imageBase64 := firstString(
		stringValue(record["b64_json"]),
		stringValue(record["result"]),
		stringValue(record["partial_image_b64"]),
		lookup(item, "result"),
		lookup(outputItem, "result"),
		lookup(first, "b64_json"),
	)
	if imageBase64 == nil || !looksLikeBase64(*imageBase64) {
		return nil, invalidProviderResponse("图像生成 provider 响应缺少 data[0].b64_json")
	}
	result := &ImageGenerationResult{ImageBase64: *imageBase64}
	if revisedPrompt := firstString(
		stringValue(record["revised_prompt"]),
		stringValue(record["prompt"]),
		lookup(item, "revised_prompt"),
		lookup(item, "prompt"),
		lookup(outputItem, "revised_prompt"),
		lookup(outputItem, "prompt"),
		lookup(first, "revised_prompt"),
		lookup(first, "prompt"),
	); revisedPrompt != nil {
		result.RevisedPrompt = *revisedPrompt
	}
	if format := stringValue(outputFormat); format != nil {
		result.OutputFormat = *format
	}
	return result, nil
}

// firstString returns the first non-nil string candidate.
func firstString(candidates ...*string) *string {
	for _, candidate := range candidates {
		if candidate != nil {
			return candidate
		}
	}
	return nil
}

// imageGenerationProviderErrorFromResponse mirrors
// imageGenerationProviderErrorFromResponse.
func imageGenerationProviderErrorFromResponse(statusCode int, parsed any) *ImageGenerationProviderError {
	record, _ := parsed.(map[string]any)
	if record == nil {
		record = map[string]any{}
	}
	errPayload := objectValue(record["error"])
	if errPayload == nil {
		errPayload = record
	}
	message := fmt.Sprintf("图像生成 provider 返回 HTTP %d", statusCode)
	if text := stringValue(errPayload["message"]); text != nil {
		message = *text
	}
	code := "openai_anthropic_bridge_image_generation_provider_error"
	if text := stringValue(errPayload["code"]); text != nil {
		code = *text
	}
	resolvedStatus := 502
	if statusCode >= 400 && statusCode < 500 {
		resolvedStatus = 400
	}
	errType := "upstream_error"
	if text := stringValue(errPayload["type"]); text != nil {
		errType = *text
	}
	providerErr := &ImageGenerationProviderError{
		Message:    message,
		Code:       code,
		StatusCode: resolvedStatus,
		Type:       errType,
	}
	if details := objectValue(errPayload["moderation_details"]); details != nil {
		providerErr.ModerationDetails = details
	}
	return providerErr
}

func invalidProviderResponse(message string) *BridgeRequestError {
	return bridgeError(message,
		"openai_anthropic_bridge_image_generation_provider_invalid_response", 502, "upstream_error")
}

func safeParseJSON(text string) any {
	if text == "" {
		return map[string]any{}
	}
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return map[string]any{}
	}
	return parsed
}

func isPlainObject(value any) bool {
	_, ok := value.(map[string]any)
	return ok
}

var base64Shape = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`)

// looksLikeBase64 mirrors looksLikeBase64 (whitespace removed first).
func looksLikeBase64(value string) bool {
	compact := strings.NewReplacer("\n", "", "\r", "", "\t", "", " ", "").Replace(value)
	return base64Shape.MatchString(compact)
}
