// Package vendorinfer 按模型名推断九家真实厂商与连接 preset。
//
// 定位：纯函数包，不依赖数据库、HTTP 或任何运行时状态。调用方（如旧业务导入链）
// 在来源账户没有可信 vendor 时，用来源模型列表推断唯一厂商；推断不出或互斥时
// 上层必须跳过该来源并写报告，不得回落到通用连接器或空 vendor。
//
// 推断顺序（每个模型名独立执行，全部命中必须收敛到同一厂商）：
//  1. 目录精确匹配：模型名等于 Entry.PublicModel 或 Entry.UpstreamModel；
//  2. 目录前缀匹配：取目录中与模型名构成前缀关系的最长名称（PublicModel 或
//     UpstreamModel），同一最长长度出现多个厂商视为该模型冲突；
//  3. 静态前缀：目录未命中时按业界通用模型名前缀表判定，同样取最长匹配前缀。
//
// 匹配大小写不敏感（模型名先折叠为小写再比较），返回的报告保留调用方传入的原始
// 模型名。Unmatched 与 Conflicts 去重后按 ASCII 升序输出，便于稳定落报告。
package vendorinfer

import (
	"sort"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// Entry 是推断目录的一行：一个已知模型在厂商内的公开名与上游名。
// PublicModel 与 UpstreamModel 至少一个非空；两者都参与精确与前缀匹配。
type Entry struct {
	Vendor        string
	PublicModel   string
	UpstreamModel string
}

// Result 是一次推断的结论。
type Result struct {
	// Vendor 是唯一推断出的厂商；OK 为 false 时为空字符串。
	Vendor string
	// OK 表示全部输入模型都命中且收敛到同一厂商，且不存在冲突。
	OK bool
	// Unmatched 是无法判定厂商的模型名（目录与静态前缀都没有命中）。
	Unmatched []string
	// Conflicts 是厂商冲突涉及的模型名：单个模型命中多个厂商，或不同模型分别
	// 命中不同厂商时，冲突涉及的全部模型名。
	Conflicts []string
}

// staticPrefix 是目录未命中时使用的通用模型名前缀表。
// 只登记九家真实厂商可明确归属的前缀，未登记的名字一律视为无法判定。
type staticPrefix struct {
	Prefix string
	Vendor string
}

// staticPrefixes 是静态前缀表的唯一来源（顺序不参与语义，匹配按最长前缀）。
var staticPrefixes = []staticPrefix{
	{"gpt-", contracts.VendorGPT},
	{"o1", contracts.VendorGPT},
	{"o3", contracts.VendorGPT},
	{"o4", contracts.VendorGPT},
	{"chatgpt", contracts.VendorGPT},
	{"codex", contracts.VendorGPT},
	{"claude", contracts.VendorAnthropic},
	{"gemini", contracts.VendorGemini},
	{"grok", contracts.VendorXAI},
	{"deepseek", contracts.VendorDeepSeek},
	{"glm", contracts.VendorGLM},
	{"chatglm", contracts.VendorGLM},
	{"qwen", contracts.VendorQwen},
	{"qwq", contracts.VendorQwen},
	{"wan", contracts.VendorQwen},
	{"doubao", contracts.VendorVolcengine},
	{"seedance", contracts.VendorVolcengine},
	{"seedream", contracts.VendorVolcengine},
	{"volc", contracts.VendorVolcengine},
	{"minimax", contracts.VendorMiniMax},
	{"hailuo", contracts.VendorMiniMax},
	{"abab", contracts.VendorMiniMax},
	{"speech-2", contracts.VendorMiniMax},
}

// Catalog 是只读的推断目录，由 NewCatalog 一次性构建。
type Catalog struct {
	// exact 是折叠为小写的模型名到厂商集合的索引。
	exact map[string][]string
	// names 是参与前缀匹配的（小写名称, 厂商）对。
	names []catalogName
}

type catalogName struct {
	Name   string
	Vendor string
}

// NewCatalog 用目录行构建推断目录；空行（Vendor 为空、或两个模型名都为空）被忽略。
func NewCatalog(entries []Entry) *Catalog {
	catalog := &Catalog{exact: make(map[string][]string, len(entries)*2)}
	for _, entry := range entries {
		vendor := strings.TrimSpace(entry.Vendor)
		if vendor == "" {
			continue
		}
		for _, name := range []string{entry.PublicModel, entry.UpstreamModel} {
			name = strings.ToLower(strings.TrimSpace(name))
			if name == "" {
				continue
			}
			if !containsString(catalog.exact[name], vendor) {
				catalog.exact[name] = append(catalog.exact[name], vendor)
			}
			if !catalog.hasName(name, vendor) {
				catalog.names = append(catalog.names, catalogName{Name: name, Vendor: vendor})
			}
		}
	}
	return catalog
}

func (c *Catalog) hasName(name, vendor string) bool {
	for _, item := range c.names {
		if item.Name == name && item.Vendor == vendor {
			return true
		}
	}
	return false
}

// Infer 按 docs 中的推断顺序判定 vendor。
//
// 任一模型无法判定（Unmatched）或出现厂商冲突（Conflicts）时 OK 为 false；
// 即使全部模型都能判定，只要命中的厂商不唯一，OK 同样为 false 且冲突模型记入
// Conflicts。空输入返回 OK=false、Vendor 为空，且两个报告切片均为空。
func (c *Catalog) Infer(models []string) Result {
	result := Result{Unmatched: []string{}, Conflicts: []string{}}
	conflictSet := make(map[string]struct{})
	unmatchedSet := make(map[string]struct{})
	resolved := make(map[string]string) // 原始模型名 → vendor
	resolvedOrder := make([]string, 0, len(models))

	for _, model := range models {
		vendor, conflict := c.resolve(model)
		switch {
		case vendor != "":
			resolved[model] = vendor
			resolvedOrder = append(resolvedOrder, model)
		case conflict:
			conflictSet[model] = struct{}{}
		default:
			unmatchedSet[model] = struct{}{}
		}
	}

	vendors := make(map[string]struct{}, len(resolved))
	for _, vendor := range resolved {
		vendors[vendor] = struct{}{}
	}
	if len(vendors) > 1 {
		// 不同模型分别命中不同厂商：整体无法收敛到唯一 vendor，全部已解析模型
		// 都是冲突上下文，便于报告直接定位来源模型列表。
		for _, model := range resolvedOrder {
			conflictSet[model] = struct{}{}
		}
	}

	if len(vendors) == 1 && len(conflictSet) == 0 && len(unmatchedSet) == 0 {
		for vendor := range vendors {
			result.Vendor = vendor
		}
		result.OK = true
	}
	result.Unmatched = sortedKeys(unmatchedSet)
	result.Conflicts = sortedKeys(conflictSet)
	return result
}

// resolve 判定单个模型名的厂商；返回空 vendor 时 conflict 表示是否为厂商冲突，
// false 表示完全未命中。
func (c *Catalog) resolve(model string) (vendor string, conflict bool) {
	name := strings.ToLower(strings.TrimSpace(model))
	if name == "" {
		return "", false
	}
	if vendors, ok := c.exact[name]; ok && len(vendors) > 0 {
		if len(vendors) == 1 {
			return vendors[0], false
		}
		return "", true
	}
	if vendors := c.prefixVendors(name); len(vendors) > 0 {
		if len(vendors) == 1 {
			return vendors[0], false
		}
		return "", true
	}
	if vendors := staticVendors(name); len(vendors) > 0 {
		if len(vendors) == 1 {
			return vendors[0], false
		}
		return "", true
	}
	return "", false
}

// prefixVendors 返回目录中与 name 构成前缀关系的最长名称对应的厂商集合。
func (c *Catalog) prefixVendors(name string) []string {
	longest := 0
	var vendors []string
	for _, item := range c.names {
		if !strings.HasPrefix(name, item.Name) {
			continue
		}
		switch {
		case len(item.Name) > longest:
			longest = len(item.Name)
			vendors = []string{item.Vendor}
		case len(item.Name) == longest:
			if !containsString(vendors, item.Vendor) {
				vendors = append(vendors, item.Vendor)
			}
		}
	}
	return vendors
}

// staticVendors 返回静态前缀表中与 name 构成前缀关系的最长前缀对应的厂商集合。
func staticVendors(name string) []string {
	longest := 0
	var vendors []string
	for _, item := range staticPrefixes {
		if !strings.HasPrefix(name, item.Prefix) {
			continue
		}
		switch {
		case len(item.Prefix) > longest:
			longest = len(item.Prefix)
			vendors = []string{item.Vendor}
		case len(item.Prefix) == longest:
			if !containsString(vendors, item.Vendor) {
				vendors = append(vendors, item.Vendor)
			}
		}
	}
	return vendors
}

// ConnectionPreset 返回厂商在给定协议面下的连接 preset。
//
// 协议面取 contracts.SurfaceOpenAIV1 / SurfaceAnthropicV1 / SurfaceGeminiV1Beta；
// 厂商没有该协议面连接、厂商或协议面未知、或候选 preset 未在 registry 注册时返回
// ok=false（调用方据此跳过来源，不得写入臆造 preset）。
func ConnectionPreset(vendor, protocolSurface string) (preset string, ok bool) {
	var candidate string
	switch protocolSurface {
	case contracts.SurfaceOpenAIV1:
		switch vendor {
		case contracts.VendorGPT:
			candidate = contracts.PresetGPTOpenAIV1
		case contracts.VendorXAI:
			candidate = contracts.PresetXAIOpenAIV1
		case contracts.VendorDeepSeek:
			candidate = contracts.PresetDeepSeekOpenAIV1
		case contracts.VendorGLM:
			candidate = contracts.PresetGLMGeneralOpenAIV1
		case contracts.VendorGemini:
			candidate = contracts.PresetGeminiOpenAIChatV1Beta
		case contracts.VendorQwen:
			candidate = contracts.PresetQwenDashScope
		case contracts.VendorVolcengine:
			candidate = contracts.PresetVolcengineArk
		case contracts.VendorMiniMax:
			candidate = contracts.PresetMiniMaxMedia
		}
	case contracts.SurfaceAnthropicV1:
		switch vendor {
		case contracts.VendorAnthropic:
			candidate = contracts.PresetAnthropicV1
		case contracts.VendorDeepSeek:
			candidate = contracts.PresetDeepSeekAnthropicV1
		case contracts.VendorGLM:
			candidate = contracts.PresetGLMCodingAnthropicV1
		}
	case contracts.SurfaceGeminiV1Beta:
		if vendor == contracts.VendorGemini {
			candidate = contracts.PresetGeminiNativeV1Beta
		}
	}
	if candidate == "" || !contracts.HasConnectionPreset(candidate) {
		return "", false
	}
	return candidate, true
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func sortedKeys(set map[string]struct{}) []string {
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}
