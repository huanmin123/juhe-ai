package gatewayresponse

import (
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// Codex Remote Compaction V2 契约（codex-compaction-contract.ts 的契约部分：
// 计数、失配帧与请求判定）。codex 桥（G18）通过本包导出的函数消费。

// CodexCompactionContractMismatchErrorCode 对齐
// codexCompactionContractMismatchErrorCode。
const CodexCompactionContractMismatchErrorCode = "codex_compaction_contract_mismatch"

// CodexCompactionContractCounts 对齐 CodexCompactionContractCounts。
type CodexCompactionContractCounts struct {
	OutputItemCount     int
	CompactionItemCount int
}

// CodexCompactionContractMismatchInput 对齐 CodexCompactionContractMismatchInput。
type CodexCompactionContractMismatchInput struct {
	OutputItemCount     int
	CompactionItemCount int
	Transport           string // 'json' | 'sse'
	EventType           string
	Force               bool
	Message             string
}

// 请求侧压缩触发判定唯一实现在 gatewaycodex.CodexCompactionExpectedForRequest
// （调度内核通用化设计 5.2 三轨合一：preflight 单点消费）。本包原有的第四份
// 同形拷贝与配套触发判定辅助（requestPathHasCompactionTrigger /
// jsonValueHasCompactionTrigger 等）已随批次 2 及其遗留清理删除；边界行为
// （深度/广度上限、raw 扫描窗口）由 gatewaycodex 包自身测试锁定。

// CodexCompactionContractMismatchFrame 对齐
// codexCompactionContractMismatchFrame。
func CodexCompactionContractMismatchFrame(input CodexCompactionContractMismatchInput) *gatewayproto.SemanticFrame {
	if !input.Force && input.CompactionItemCount == 1 {
		return nil
	}
	rawText := ""
	if input.Transport == "sse" {
		rawText = input.EventType
	}
	message := input.Message
	if message == "" {
		message = "Codex Remote Compaction V2 响应结构无效：期望恰好 1 个 compaction output item，实际 " +
			itoa(int64(input.CompactionItemCount)) + " 个，output item 总数 " +
			itoa(int64(input.OutputItemCount)) + " 个"
	}
	return &gatewayproto.SemanticFrame{
		// Node 以 provenance === 'gateway_protocol_contract' 承载契约帧；Go 的
		// SemanticFrame 用 FrameType raw_json_path 表示（与本包
		// MatchRuntimeResponseInspectionPolicy 的契约帧匹配一致）。
		FrameType:      gatewayproto.FrameTypeRawJSONPath,
		Protocol:       "openai_v1",
		EndpointFamily: gatewayproto.EndpointFamilyResponses,
		Transport:      gatewayproto.ResponseTransport(input.Transport),
		ErrorCode:      CodexCompactionContractMismatchErrorCode,
		ErrorType:      "invalid_response_contract",
		ErrorMessage:   message,
		RawJSONPaths:   []string{"output"},
		RawText:        rawText,
		EventType:      input.EventType,
	}
}

// CountCodexCompactionOutputItemsFromJSON 对齐
// countCodexCompactionOutputItemsFromJson。
func CountCodexCompactionOutputItemsFromJSON(value any) *CodexCompactionContractCounts {
	root, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	output, ok := root["output"].([]any)
	if !ok {
		return nil
	}
	return countCodexCompactionOutputItems(output)
}

// CountCodexCompactionOutputItemsFromStreamEvent 对齐
// countCodexCompactionOutputItemsFromStreamEvent。
func CountCodexCompactionOutputItemsFromStreamEvent(event gatewayopenai.ParsedStreamEvent) *CodexCompactionContractCounts {
	if event.EventType != "response.output_item.done" && event.EventName != "response.output_item.done" {
		return nil
	}
	item, _ := event.Data["item"].(map[string]any)
	counts := &CodexCompactionContractCounts{OutputItemCount: 1}
	if isCodexDeserializableCompactionItem(item) {
		counts.CompactionItemCount = 1
	}
	return counts
}

func countCodexCompactionOutputItems(output []any) *CodexCompactionContractCounts {
	compactionItemCount := 0
	for _, item := range output {
		object, _ := item.(map[string]any)
		if isCodexDeserializableCompactionItem(object) {
			compactionItemCount++
		}
	}
	return &CodexCompactionContractCounts{
		OutputItemCount:     len(output),
		CompactionItemCount: compactionItemCount,
	}
}

func isCodexDeserializableCompactionItem(item map[string]any) bool {
	if item == nil {
		return false
	}
	if item["type"] != "compaction" && item["type"] != "compaction_summary" {
		return false
	}
	_, isString := item["encrypted_content"].(string)
	return isString
}
