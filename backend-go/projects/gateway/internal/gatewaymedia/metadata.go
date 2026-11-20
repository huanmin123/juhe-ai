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
	"strings"
)

// ErrMetadataReservedField 标记 metadata 试图覆盖规范字段（400
// metadata_conflict）。链上层按 errors.Is 命中映射专用错误码。
var ErrMetadataReservedField = errors.New("metadata 不允许覆盖规范字段")

// MetadataKey 是请求体中扩展通道的固定键名（设计 §5.1）。
const MetadataKey = "metadata"

// RetiredProviderOptionsKey 是已退役的旧扩展通道键名（设计 §5.1：不保留
// 别名、不做双写）。媒体创建/语音直连的通用透传面必须剥除该键，旧客户端
// 携带时按未知字段忽略且不产生任何回显。
const RetiredProviderOptionsKey = "provider_options"

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

// MergeMetadata 把 metadata 逐层合并进 base 的副本，但只允许新增路径：与
// base 中任一既有路径同名的值一律拒绝（ErrMetadataReservedField，链上层
// 映射 400 metadata_conflict），不做覆盖。这是计费安全边界：出站报文的
// 规范字段（如 parameters.size/duration、contents、text）决定上游实际
// 工作量，若允许 metadata 覆盖，创建快照与计费档会与真实工作量脱钩。
// 双方均为对象且该键不存在于 base 时递归新增；数组、标量与 base 已有键
// 均不覆盖。base 与 metadata 均不被修改；未知键原样进入合并结果，由上游
// 裁决。
func MergeMetadata(base map[string]any, metadata map[string]any) (map[string]any, error) {
	return MergeMetadataReserved(base, metadata, nil)
}

// MergeMetadataReserved 在 MergeMetadata 的“只新增”语义之上，增加 adapter
// 声明的规范路径校验：reservedPaths 中的点号路径即使当前 base 尚未出现，
// metadata 也不得提供（例如请求未传 size 时，metadata 仍不得注入
// parameters.size）。这是计费安全边界：规范映射字段决定上游实际工作量，
// 不能因为本次请求恰好省略该字段就允许扩展通道补写。
func MergeMetadataReserved(base map[string]any, metadata map[string]any, reservedPaths []string) (map[string]any, error) {
	if len(metadata) == 0 {
		return deepCopyMap(base), nil
	}
	if err := validateMetadataReservedPaths(metadata, reservedPaths); err != nil {
		return nil, err
	}
	merged := deepCopyMap(base)
	if err := mergeMetadataValues(merged, metadata, ""); err != nil {
		return nil, err
	}
	return merged, nil
}

// MergeMetadataJSON 把 metadata 合并进序列化后的厂商 JSON 报文（创建计划
// 在 adapter 构造报文之后统一应用，adapter 无感）。body 必须是 JSON 对象；
// metadata 为空时原样返回（零改写，字节级透传不破）。合并语义同
// MergeMetadata：只新增、不覆盖任何既有路径。
func MergeMetadataJSON(body []byte, metadata map[string]any) ([]byte, error) {
	return MergeMetadataJSONReserved(body, metadata, nil)
}

// MergeMetadataJSONReserved 是 MergeMetadataJSON 的 reservedPaths 版本
// （语义同 MergeMetadataReserved）；供序列化后统一合并的 adapter 使用。
func MergeMetadataJSONReserved(body []byte, metadata map[string]any, reservedPaths []string) ([]byte, error) {
	if len(metadata) == 0 {
		return body, nil
	}
	var base map[string]any
	if err := json.Unmarshal(body, &base); err != nil {
		return nil, fmt.Errorf("扩展参数只能并入 JSON 对象报文: %w", err)
	}
	merged, err := MergeMetadataReserved(base, metadata, reservedPaths)
	if err != nil {
		return nil, err
	}
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

// validateMetadataReservedPaths 递归校验 metadata 是否触碰 adapter 声明的
// 规范路径。规则：
//   - 路径与 reserved 精确相等，或位于 reserved 子树内 → 冲突；
//   - 非对象值占了 reserved 路径的祖先（如把 parameters 写成标量，而
//     parameters.size 是规范路径）→ 冲突，避免整棵容器被替换；
//   - 对象形式的祖先容器允许通过，继续递归检查子键（因此
//     parameters.prompt_extend 这类新增叶子仍可用）。
func validateMetadataReservedPaths(metadata map[string]any, reservedPaths []string) error {
	if len(metadata) == 0 || len(reservedPaths) == 0 {
		return nil
	}
	reserved := make(map[string]struct{}, len(reservedPaths))
	for _, path := range reservedPaths {
		path = strings.TrimSpace(path)
		if path != "" {
			reserved[path] = struct{}{}
		}
	}
	if len(reserved) == 0 {
		return nil
	}
	return validateMetadataReservedValues(metadata, reserved, "")
}

func validateMetadataReservedValues(values map[string]any, reserved map[string]struct{}, prefix string) error {
	for key, value := range values {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if metadataPathReserved(path, reserved) {
			return fmt.Errorf("%w: %s", ErrMetadataReservedField, path)
		}
		if object, ok := value.(map[string]any); ok {
			if err := validateMetadataReservedValues(object, reserved, path); err != nil {
				return err
			}
			continue
		}
		if metadataPathAncestorOfReserved(path, reserved) {
			return fmt.Errorf("%w: %s", ErrMetadataReservedField, path)
		}
	}
	return nil
}

// metadataPathReserved 判断 path 是否命中 reserved 路径或其子树。
func metadataPathReserved(path string, reserved map[string]struct{}) bool {
	if _, exists := reserved[path]; exists {
		return true
	}
	for candidate := range reserved {
		if strings.HasPrefix(path, candidate+".") {
			return true
		}
	}
	return false
}

// metadataPathAncestorOfReserved 判断 path 是否是某个 reserved 路径的祖先
// （用于拒绝“以标量/数组占据规范容器”）。
func metadataPathAncestorOfReserved(path string, reserved map[string]struct{}) bool {
	prefix := path + "."
	for candidate := range reserved {
		if strings.HasPrefix(candidate, prefix) {
			return true
		}
	}
	return false
}

// mergeMetadataValues 把 src 递归新增进 dst：双方均为对象的键继续递归；
// dst 已有任意键（对象/数组/标量）时拒绝覆盖并返回冲突路径。路径用点号
// 连接，顶层键即键名，嵌套键如 parameters.size。
func mergeMetadataValues(dst, src map[string]any, prefix string) error {
	for key, value := range src {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		existing, exists := dst[key]
		if !exists {
			if sub, ok := value.(map[string]any); ok {
				dst[key] = deepCopyMap(sub)
				continue
			}
			dst[key] = value
			continue
		}
		existingObject, existingIsObject := existing.(map[string]any)
		valueObject, valueIsObject := value.(map[string]any)
		if existingIsObject && valueIsObject {
			if err := mergeMetadataValues(existingObject, valueObject, path); err != nil {
				return err
			}
			continue
		}
		return fmt.Errorf("%w: %s", ErrMetadataReservedField, path)
	}
	return nil
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
