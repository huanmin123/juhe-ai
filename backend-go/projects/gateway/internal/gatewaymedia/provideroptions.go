// provideroptions.go 是 L3 扩展通道的解析与合并（媒体设计 §6：请求体
// provider_options: {"<provider_code>": {...}}；契约 §2.1 L3 定义、§2.4
// 规则 4：仅命中 provider 的子对象生效，覆盖同名 L2 值，其余子对象忽略，
// 未知键原样透传由厂商裁决）。只开放这一个通道，不引入 extra_body 等第二
// 通道；媒体域私有，不改对话链请求体语义。
package gatewaymedia

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// ProviderOptionsKey 是请求体中 L3 扩展通道的固定键名（契约 §2.2 表）。
const ProviderOptionsKey = "provider_options"

// ExtractProviderOptions 解析请求体 provider_options 的原始 JSON：顶层必须
// 是对象，键为 provider_code（归一小写去空格，与注册表键一致），值为该
// provider 的参数对象；任何子值不是对象则报错（请求形态错误，链上映射
// 400）。空/null 输入返回空 map（未使用扩展通道是正常形态）。
func ExtractProviderOptions(raw json.RawMessage) (map[string]map[string]any, error) {
	extracted := make(map[string]map[string]any)
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return extracted, nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &top); err != nil {
		return nil, fmt.Errorf("provider_options 必须是 JSON 对象: %w", err)
	}
	for key, value := range top {
		var sub map[string]any
		if err := json.Unmarshal(value, &sub); err != nil {
			return nil, fmt.Errorf("provider_options.%s 必须是对象: %w", key, err)
		}
		extracted[normalizeProviderKey(key)] = sub
	}
	return extracted, nil
}

// MergeProviderOptions 把 opts 中命中 providerCode 的子对象 deep-merge 进
// base 的副本（厂商值覆盖同名键；嵌套对象递归合并；其余 provider 的子对象
// 整体忽略——混合分组多厂商共存不串参数）。opts 是 provider_options 的对象
// 形态（键=provider_code）；base 与 opts 均不被修改。命中子对象内的未知键
// 原样进入合并结果，由上游裁决（契约 §2.1 L3）。
func MergeProviderOptions(base map[string]any, providerCode string, opts map[string]any) map[string]any {
	merged := deepCopyMap(base)
	if len(opts) == 0 {
		return merged
	}
	for key, value := range opts {
		if !providerOptionKeyMatches(providerCode, key) {
			continue
		}
		sub, ok := value.(map[string]any)
		if !ok {
			// 命中 provider 的值不是对象：无效子对象，忽略（形态校验由
			// ExtractProviderOptions 承载，此处不重复报错）。
			continue
		}
		mergeProviderValues(merged, sub)
		break
	}
	return merged
}

// AppliedProviderOptionKeys 返回 opts 中命中 providerCode 的子对象键名摘要
//（字典序稳定，不含值——契约 §2.4 规则 4 的回显面：创建响应/任务面 job
// 对象的 provider_options_applied 来源）。未命中（或命中子对象非对象）返回
// nil。匹配语义与 MergeProviderOptions 同源（providerOptionKeyMatches）——
// 回显的键名集合恒等于实际合并的键名集合。
func AppliedProviderOptionKeys(providerCode string, opts map[string]any) []string {
	if len(opts) == 0 {
		return nil
	}
	for key, value := range opts {
		if !providerOptionKeyMatches(providerCode, key) {
			continue
		}
		sub, ok := value.(map[string]any)
		if !ok {
			continue
		}
		keys := make([]string, 0, len(sub))
		for name := range sub {
			keys = append(keys, name)
		}
		sort.Strings(keys)
		return keys
	}
	return nil
}

// providerOptionKeyMatches 报告 provider_options 的键 key 是否命中
// providerCode（adapter 注册键，如 openai/gemini）。匹配键同时认账户
// provider_code 原键与 adapter 注册键：gpt 账户（openai 的 OAuth 子供应商）
// 既可用 {"gpt":{...}} 也可用 {"openai":{...}} 表达同一 provider 的扩展参数——
// providerFamilyAlias 在归一映射处把子供应商代码折叠到协议族基键，两侧
// 同一归一后比较。gemini（M3 veo adapter 注册键）无子供应商代码，provider_
// code 恒为 "gemini"，恒等映射即命中（{"gemini":{...}}）。M3+ 新增供应商
// 若有子供应商代码，同表扩展。
func providerOptionKeyMatches(providerCode, key string) bool {
	return providerFamilyAlias(normalizeProviderKey(key)) == providerFamilyAlias(normalizeProviderKey(providerCode))
}

// providerFamilyAlias 把 OAuth 子供应商 provider_code 归一到协议族基键
//（与视频 adapter 归一 chainVideoAdapterKeyOfProvider 的 openai/gpt →
// "openai" 同一事实，媒体域内两处镜像）。无别名映射的键原样返回。
func providerFamilyAlias(key string) string {
	switch key {
	case "gpt":
		return "openai"
	default:
		return key
	}
}

// mergeProviderValues 把 src 递归合并进 dst：双方均为对象的键递归合并，
// 其余（含 src 值为非对象）由 src 值覆盖。复制语义只覆盖嵌套 map（merge
// 语义需要），数组等其余值共享引用且本层从不修改它们。
func mergeProviderValues(dst, src map[string]any) {
	for key, value := range src {
		if sub, ok := value.(map[string]any); ok {
			if existing, ok := dst[key].(map[string]any); ok {
				mergeProviderValues(existing, sub)
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
