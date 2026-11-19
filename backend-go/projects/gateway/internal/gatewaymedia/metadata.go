// metadata.go 是媒体域唯一的请求扩展通道（设计《对外协议面对齐 new-api
// 设计》§5.1）：请求体 metadata 为厂商参数包，规范映射完成后逐键并入厂商
// 报文；不再有 provider_options（按 provider 命名空间）通道。
//
// 与 new-api 的差异只有一处：metadata 的键不得与规范字段同名（冲突 400），
// 避免静默覆盖导致 params_applied 与审计失真；其余（对象/JSON 字符串两种
// 形态、未知键交厂商裁决）与其一致。
package gatewaymedia

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// ErrMetadataReservedField 标记 metadata 试图覆盖规范字段（400
// metadata_conflict）。链上层按 errors.Is 命中映射专用错误码。
var ErrMetadataReservedField = errors.New("metadata 不允许覆盖规范字段")

// MetadataKey 是请求体中扩展通道的固定键名（设计 §5.1）。
const MetadataKey = "metadata"

// VideoMetadataReserved 是视频创建面的规范字段名集合（设计 §4.2.1 参数表）。
// metadata 命中其中任一键即 400：规范字段只能由网关归一，不允许扩展通道
// 覆盖。
var VideoMetadataReserved = reservedFieldSet(
	"model", "prompt", "image", "images", "input_reference",
	"duration", "seconds", "size", "width", "height", "fps", "seed", "n",
	"response_format", "user", "audio", "negative_prompt", "resolution",
	"mode", "metadata",
)

// SpeechMetadataReserved 是语音合成面的规范字段名集合（契约 §2.3）。
var SpeechMetadataReserved = reservedFieldSet(
	"model", "input", "voice", "response_format", "speed", "instructions",
	"language", "stream_format", "metadata",
)

// AudioJobMetadataReserved 是长音频任务面的规范字段名集合（契约 §10.2）。
var AudioJobMetadataReserved = reservedFieldSet(
	"model", "input_url", "language", "metadata",
)

func reservedFieldSet(names ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		set[name] = struct{}{}
	}
	return set
}

// ExtractMetadata 解析请求体的扩展参数包。接受 JSON 对象与 JSON 字符串两种
// 形态（与 new-api 一致）；nil/null 返回 nil（未使用扩展通道是正常形态）。
// reserved 非空时校验键名，命中即报错（400 invalid_request_error）。
func ExtractMetadata(raw any, reserved map[string]struct{}) (map[string]any, error) {
	if raw == nil {
		return nil, nil
	}
	value := raw
	if text, ok := raw.(string); ok {
		if text == "" {
			return nil, nil
		}
		var parsed any
		if err := json.Unmarshal([]byte(text), &parsed); err != nil {
			return nil, fmt.Errorf("metadata 必须是 JSON 对象或 JSON 对象字符串: %w", err)
		}
		if parsed == nil {
			return nil, nil
		}
		value = parsed
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("metadata 必须是 JSON 对象或 JSON 对象字符串，实际为 %v", raw)
	}
	if object == nil {
		return nil, nil
	}
	for key := range object {
		if _, exists := reserved[key]; exists {
			return nil, fmt.Errorf("%w: %s", ErrMetadataReservedField, key)
		}
	}
	return object, nil
}

// MergeMetadata 把 metadata 深合并进 base 的副本（厂商值覆盖同名键，嵌套
// 对象递归合并）。base 与 metadata 均不被修改；未知键原样进入合并结果，
// 由上游裁决。
func MergeMetadata(base map[string]any, metadata map[string]any) map[string]any {
	merged := deepCopyMap(base)
	if len(metadata) == 0 {
		return merged
	}
	mergeMetadataValues(merged, metadata)
	return merged
}

// MergeMetadataJSON 把 metadata 合并进序列化后的厂商 JSON 报文（创建计划
// 在 adapter 构造报文之后统一应用，adapter 无感）。body 必须是 JSON 对象；
// metadata 为空时原样返回（零改写，字节级透传不破）。
func MergeMetadataJSON(body []byte, metadata map[string]any) ([]byte, error) {
	if len(metadata) == 0 {
		return body, nil
	}
	var base map[string]any
	if err := json.Unmarshal(body, &base); err != nil {
		return nil, fmt.Errorf("扩展参数只能并入 JSON 对象报文: %w", err)
	}
	merged := MergeMetadata(base, metadata)
	encoded, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// AppliedMetadataKeys 返回 metadata 的键名摘要（字典序稳定，不含值），
// 供创建响应/任务面的 metadata_applied 回显。空输入返回 nil。
func AppliedMetadataKeys(metadata map[string]any) []string {
	if len(metadata) == 0 {
		return nil
	}
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// mergeMetadataValues 把 src 递归合并进 dst：双方均为对象的键递归合并，
// 其余（含 src 值为非对象）由 src 值覆盖。
func mergeMetadataValues(dst, src map[string]any) {
	for key, value := range src {
		if sub, ok := value.(map[string]any); ok {
			if existing, ok := dst[key].(map[string]any); ok {
				mergeMetadataValues(existing, sub)
				continue
			}
			dst[key] = deepCopyMap(sub)
			continue
		}
		dst[key] = value
	}
}

// deepCopyMap 深拷贝嵌套对象树（map[string]any 递归），数组等其余值共享
// 引用（本包不会原地修改它们）。
func deepCopyMap(source map[string]any) map[string]any {
	if source == nil {
		return map[string]any{}
	}
	copied := make(map[string]any, len(source))
	for key, value := range source {
		if sub, ok := value.(map[string]any); ok {
			copied[key] = deepCopyMap(sub)
			continue
		}
		copied[key] = value
	}
	return copied
}
