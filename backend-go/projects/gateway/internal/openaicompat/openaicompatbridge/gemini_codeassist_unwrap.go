package openaicompatbridge

// 2026-11-07 跨协议 Responses↔Chat 桥退役：本包原 bridge request/response/
// stream/dispatch 文件族整体删除。保留面只剩 Gemini Code Assist
// （google_oauth code_assist/google_one）上游 {response:...} envelope 的原生
// 解包（原 bridge_gemini_response.go 尾段 + bridge_stream.go 的 SSE 泵 +
// bridgejson.go 的 JSON 助手），消费方是组合根
// chain_bridge_response.go transformGeminiCodeAssistIfApplicable。

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// ---------------------------------------------------------------------------
// SSE 边界与泵（原 bridge_stream.go）
// ---------------------------------------------------------------------------

// IndexBridgeSseBoundary finds the first SSE empty-line boundary and returns
// the index of the boundary's leading byte together with the full boundary
// length. 词表：\n\n、\n\r\n、\r\n\r\n（经 \n\r\n 后缀命中，index 指向前一
// 终止符的 \n）与 \r\r（DD-C1 补齐，此前纯 CR 空行不识别、整流粘连到 EOF）；
// 边界判定只认空行序列，事件内容行尾 \r\n 不触发。
func IndexBridgeSseBoundary(value string) (int, int) {
	for i := 0; i < len(value); i++ {
		if value[i] == '\r' {
			if i+1 < len(value) && value[i+1] == '\r' {
				return i, 2
			}
			continue
		}
		if value[i] != '\n' {
			continue
		}
		length := 1
		j := i + 1
		if j < len(value) && value[j] == '\r' {
			j++
			length++
		}
		if j >= len(value) || value[j] != '\n' {
			continue
		}
		return i, length + 1
	}
	return -1, 0
}

// PumpBridgeSseTransform consumes src until EOF, splits complete SSE events
// (keeping the boundary inside the event text, mirroring
// takeCompleteSseEvents), feeds every event through process, writes the
// outputs to dst, then flushes the trailing buffer through process and the
// finish() outputs. process/finish may be nil (pass-through).
func PumpBridgeSseTransform(src io.Reader, dst io.Writer, process func(eventText string) []string, finish func() []string) error {
	if process == nil {
		process = func(string) []string { return nil }
	}
	var pending strings.Builder
	buf := make([]byte, 32*1024)
	writeOutputs := func(outputs []string) error {
		for _, output := range outputs {
			if _, err := io.WriteString(dst, output); err != nil {
				return err
			}
		}
		return nil
	}
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			pending.Write(buf[:n])
			for {
				text := pending.String()
				index, length := IndexBridgeSseBoundary(text)
				if index < 0 {
					break
				}
				event := text[:index+length]
				pending.Reset()
				pending.WriteString(text[index+length:])
				if err := writeOutputs(process(event)); err != nil {
					return err
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return readErr
		}
	}
	if rest := strings.TrimSpace(pending.String()); rest != "" {
		if err := writeOutputs(process(rest)); err != nil {
			return err
		}
	}
	if finish != nil {
		if err := writeOutputs(finish()); err != nil {
			return err
		}
	}
	return nil
}

// bridgeSplitCompleteSseEvents 以空行边界拆分完整 SSE 事件（原
// codexBridgeSplitCompleteSseEvents，Code Assist 非流式收集的唯一消费方）。
func bridgeSplitCompleteSseEvents(input string) []string {
	events := []string{}
	rest := input
	for {
		index, length := IndexBridgeSseBoundary(rest)
		if index < 0 {
			break
		}
		events = append(events, rest[:index+length])
		rest = rest[index+length:]
	}
	if strings.TrimSpace(rest) != "" {
		events = append(events, rest)
	}
	return events
}

// ---------------------------------------------------------------------------
// JSON 助手（原 bridgejson.go 保留子集）
// ---------------------------------------------------------------------------

func bridgeObjectValue(value any) map[string]any {
	if object, ok := value.(map[string]any); ok {
		return object
	}
	return nil
}

func bridgeIsArray(value any) ([]any, bool) {
	items, ok := value.([]any)
	return items, ok
}

// bridgeCloneJSON deep-clones a decoded JSON value (Node jsonRecordClone).
func bridgeCloneJSON(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		cloned := make(map[string]any, len(typed))
		for key, item := range typed {
			cloned[key] = bridgeCloneJSON(item)
		}
		return cloned
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = bridgeCloneJSON(item)
		}
		return cloned
	default:
		return value
	}
}

// bridgeJSONStringify marshals compact JSON, mirroring JSON.stringify.
func bridgeJSONStringify(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// ---------------------------------------------------------------------------
// Gemini Code Assist {response:...} unwrap (code-assist-runtime.ts)
// ---------------------------------------------------------------------------

// UnwrapGeminiCodeAssistPayload mirrors unwrapGeminiCodeAssistPayload: parse
// the SSE data payload and, when it is the Code Assist {response:...} wrapper,
// re-serialize the inner response; otherwise return the payload unchanged.
func UnwrapGeminiCodeAssistPayload(payload string) string {
	var parsed any
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return payload
	}
	record, ok := parsed.(map[string]any)
	if !ok {
		return payload
	}
	response, has := record["response"]
	if !has {
		return payload
	}
	switch response.(type) {
	case map[string]any, []any:
		return bridgeJSONStringify(response)
	}
	return payload
}

// UnwrapGeminiCodeAssistSseBuffer mirrors unwrapGeminiCodeAssistSse: every SSE
// data payload is unwrapped in place, the event framing kept.
func UnwrapGeminiCodeAssistSseBuffer(body []byte) []byte {
	var output bytes.Buffer
	_ = PumpUnwrapGeminiCodeAssistSse(bytes.NewReader(body), &output)
	return output.Bytes()
}

// PumpUnwrapGeminiCodeAssistSse 是 UnwrapGeminiCodeAssistSseBuffer 的逐事件
// 增量变体：每个事件的 {response:...} 包装在事件边界到达时即时解开写入 dst，
// 事件改写规则与 buffer 版本逐行一致。
func PumpUnwrapGeminiCodeAssistSse(src io.Reader, dst io.Writer) error {
	return PumpBridgeSseTransform(src, dst, func(eventText string) []string {
		payload := geminiCodeAssistSseDataPayload(eventText)
		if payload == "" || payload == "[DONE]" {
			return []string{strings.TrimRight(eventText, "\n") + "\n\n"}
		}
		return []string{"data: " + UnwrapGeminiCodeAssistPayload(payload) + "\n\n"}
	}, nil)
}

// CollectGeminiCodeAssistSseBuffer mirrors collectGeminiCodeAssistSse:
// non-stream consumption — unwrap every event, keep the last response with
// parts and merge the collected text into its first text part.
func CollectGeminiCodeAssistSseBuffer(body []byte) []byte {
	var last map[string]any
	var lastWithParts map[string]any
	collectedTextParts := []string{}
	for _, eventText := range bridgeSplitCompleteSseEvents(string(body)) {
		payload := geminiCodeAssistSseDataPayload(eventText)
		if payload == "" || payload == "[DONE]" {
			continue
		}
		unwrappedPayload := UnwrapGeminiCodeAssistPayload(payload)
		var unwrapped any
		if err := json.Unmarshal([]byte(unwrappedPayload), &unwrapped); err != nil {
			continue
		}
		record, ok := unwrapped.(map[string]any)
		if !ok {
			continue
		}
		last = record
		parts := geminiCodeAssistExtractParts(record)
		if len(parts) == 0 {
			continue
		}
		lastWithParts = record
		for _, part := range parts {
			if text, ok := part["text"].(string); ok && text != "" {
				collectedTextParts = append(collectedTextParts, text)
			}
		}
	}
	response := lastWithParts
	if response == nil {
		response = last
	}
	if response == nil {
		response = map[string]any{}
	}
	return []byte(bridgeJSONStringify(geminiCodeAssistMergeCollectedTextParts(response, collectedTextParts)))
}

// geminiCodeAssistSseDataPayload mirrors sseDataPayload: the joined data line
// text of one SSE event.
func geminiCodeAssistSseDataPayload(eventText string) string {
	var dataLines []string
	for _, line := range strings.Split(strings.ReplaceAll(eventText, "\r\n", "\n"), "\n") {
		switch {
		case line == "data":
			dataLines = append(dataLines, "")
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimLeft(line[len("data:"):], " "))
		}
	}
	if len(dataLines) == 0 {
		return ""
	}
	return strings.TrimSpace(strings.Join(dataLines, "\n"))
}

func geminiCodeAssistExtractParts(response map[string]any) []map[string]any {
	candidates, _ := bridgeIsArray(response["candidates"])
	if len(candidates) == 0 {
		return nil
	}
	firstCandidate := bridgeObjectValue(candidates[0])
	if firstCandidate == nil {
		return nil
	}
	content := bridgeObjectValue(firstCandidate["content"])
	if content == nil {
		return nil
	}
	parts, _ := bridgeIsArray(content["parts"])
	output := []map[string]any{}
	for _, part := range parts {
		if partMap := bridgeObjectValue(part); partMap != nil {
			output = append(output, partMap)
		}
	}
	return output
}

// geminiCodeAssistMergeCollectedTextParts mirrors mergeCollectedTextParts:
// the collected text replaces the first text part (or is prepended when the
// response carries no text part).
func geminiCodeAssistMergeCollectedTextParts(response map[string]any, textParts []string) map[string]any {
	if len(textParts) == 0 {
		return response
	}
	mergedText := strings.Join(textParts, "")
	result := map[string]any{}
	for key, value := range response {
		result[key] = bridgeCloneJSON(value)
	}
	var candidates []any
	if existing, ok := bridgeIsArray(result["candidates"]); ok && len(existing) > 0 {
		candidates = existing
	} else {
		candidates = []any{map[string]any{}}
	}
	candidate, _ := candidates[0].(map[string]any)
	if candidate == nil {
		candidate = map[string]any{}
	}
	candidates[0] = candidate
	content, ok := candidate["content"].(map[string]any)
	if !ok {
		content = map[string]any{"role": "model"}
	}
	candidate["content"] = content
	existingParts, _ := bridgeIsArray(content["parts"])
	newParts := []any{}
	textUpdated := false
	for _, part := range existingParts {
		partMap := bridgeObjectValue(part)
		if partMap != nil {
			if _, hasText := partMap["text"]; hasText && !textUpdated {
				next := map[string]any{}
				for key, value := range partMap {
					next[key] = value
				}
				next["text"] = mergedText
				newParts = append(newParts, next)
				textUpdated = true
				continue
			}
		}
		newParts = append(newParts, part)
	}
	if !textUpdated {
		newParts = append([]any{map[string]any{"text": mergedText}}, newParts...)
	}
	content["parts"] = newParts
	result["candidates"] = candidates
	return result
}
