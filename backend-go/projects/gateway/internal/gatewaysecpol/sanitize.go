package gatewaysecpol

import (
	"bytes"
	"encoding/json"
)

// 请求侧出站脱敏：字段清单遍历 + 规则替换 + 数字叶子处理（设计 §5 请求侧扫
// 描点 / §9 第 4 条 fail-open）。遍历按文档序（orderedObject），只动白名单字
// 段的文本叶子与数字叶子，键名与清单外字段不动。

// endpointFamily 词表（设计 §5 字段清单按协议族展开）。
const (
	FamilyOpenAIChat      = "openai_chat"
	FamilyOpenAIResponses = "openai_responses"
	FamilyAnthropic       = "anthropic"
	FamilyGemini          = "gemini"
	FamilyMedia           = "media"
	// FamilyTool 是 B7 工具端点族（embeddings/rerank/moderations）：只扫文本
	// 输入叶子，不注入系统提示（工具请求没有 system 位置，响应也不回显输入）。
	FamilyTool = "tool"
)

var knownFamilies = map[string]bool{
	FamilyOpenAIChat:      true,
	FamilyOpenAIResponses: true,
	FamilyAnthropic:       true,
	FamilyGemini:          true,
	FamilyMedia:           true,
	FamilyTool:            true,
}

// SanitizeResult 是单次出站脱敏的产物（设计 §2）。
type SanitizeResult struct {
	// Body 是 sanitized 后的 body 字节；未发生任何替换与注入时为原 body
	// （逐字节一致）；整体 fail-open 时亦为原 body。不会为 nil。
	Body []byte
	// Mapping 是占位符映射表；本请求未命中任何 placeholder 时为 nil。
	Mapping *Mapping
	// Applied 报告是否实际发生替换/注入。
	Applied bool
	// Hits 是命中计数（label → 次数，含 mask 与 placeholder，无原文；§8）。
	Hits map[string]int
	// TotalHits 是原始命中总数（防护次数口径，设计 §4.3.1）：每次实际执行
	// 替换 +1，不去重（同请求同值出现 N 次计 N 次），mask 与 placeholder 都
	// 计入；fail-open 跳过不计。与 Hits 的聚合维度不同（Hits 按 label 分桶，
	// TotalHits 只求总数），两者互不派生。
	TotalHits int
	// Skips 是 fail-open 跳过计数（内层 JSON 解析失败等）。
	Skips int
}

// SanitizeBody 对出站 body 执行规则匹配与替换。endpointFamily 决定字段清单；
// 空策略 / 未知协议族 / 解析失败 / body 非 JSON 对象一律 fail-open 原样返回。
// 含 placeholder 规则的策略在替换完成后注入 §6.3 固定系统提示（注入在扫描之
// 后进行，注入文案自身不被任何规则扫描，保证逐请求逐字节稳定）。
func SanitizeBody(p *CompiledPolicy, body []byte, endpointFamily string) SanitizeResult {
	result := SanitizeResult{Hits: map[string]int{}}
	if p == nil || p.Empty() || !knownFamilies[endpointFamily] {
		result.Body = body
		return result
	}
	root, err := parseOrderedJSON(body)
	if err != nil {
		result.Body = body
		return result
	}
	object, ok := root.(*orderedObject)
	if !ok {
		// body 非 JSON 对象：fail-open 原样（任务契约 §2）。
		result.Body = body
		return result
	}

	ctx := &sanitizeContext{policy: p, hits: result.Hits}
	switch endpointFamily {
	case FamilyOpenAIChat:
		walkOpenAIChat(ctx, object)
	case FamilyOpenAIResponses:
		walkOpenAIResponses(ctx, object)
	case FamilyAnthropic:
		walkAnthropic(ctx, object)
	case FamilyGemini:
		walkGemini(ctx, object)
	case FamilyMedia:
		walkMedia(ctx, object)
	case FamilyTool:
		walkTool(ctx, object)
	}

	injected := false
	if p.HasPlaceholder() {
		injected = injectPlaceholderGuidance(object, endpointFamily)
	}
	if ctx.changed || injected {
		result.Body = serializeOrdered(object)
		result.Applied = true
	} else {
		result.Body = body
	}
	result.Mapping = ctx.mapping
	if ctx.mapping != nil {
		ctx.mapping.hits = ctx.hits
	}
	result.TotalHits = ctx.rawHits
	result.Skips = ctx.skips
	return result
}

// sanitizeContext 承载单次 SanitizeBody 的可变状态（请求级，单 goroutine）。
type sanitizeContext struct {
	policy  *CompiledPolicy
	mapping *Mapping
	hits    map[string]int
	rawHits int
	skips   int
	changed bool
}

// sanitizeText 对一段文本按策略数组顺序应用全部规则（§3.5），占位符保护区间
// 随替换传播。命中时更新计数并标记变更。
func (c *sanitizeContext) sanitizeText(text string) string {
	current := text
	var protected []matchSpan
	for index := range c.policy.rules {
		rule := &c.policy.rules[index]
		var hitCount int
		current, protected, hitCount = applyRuleToText(rule, current, protected, func(original string) string {
			return c.assignPlaceholder(rule.label, original, false)
		})
		if hitCount > 0 {
			c.hits[rule.label] += hitCount
			c.rawHits += hitCount
			c.changed = true
		}
	}
	return current
}

// sanitizeNumberLeaf 处理数字叶子（任务契约 §3）：仅 digit 类规则按解析后十
// 进制文本匹配；命中 → 叶子替换为字符串（mask 为打码文本、placeholder 为
// token，映射记录原始类型 number）；未命中 → 保持 json.Number，字面字节不动。
func (c *sanitizeContext) sanitizeNumberLeaf(number json.Number) any {
	current := number.String()
	var protected []matchSpan
	hit := false
	for index := range c.policy.rules {
		rule := &c.policy.rules[index]
		if !rule.digitOnly {
			continue
		}
		var hitCount int
		current, protected, hitCount = applyRuleToText(rule, current, protected, func(original string) string {
			return c.assignPlaceholder(rule.label, original, true)
		})
		if hitCount > 0 {
			hit = true
			c.hits[rule.label] += hitCount
			c.rawHits += hitCount
			c.changed = true
		}
	}
	if !hit {
		return number
	}
	return current
}

func (c *sanitizeContext) assignPlaceholder(label, original string, isNumber bool) string {
	if c.mapping == nil {
		c.mapping = newMapping()
	}
	return c.mapping.assign(label, original, isNumber)
}

// scanValue 递归扫描内层 JSON 值树（设计 §5：字符串叶子全规则、数字叶子仅
// digit 类规则）。includeNumbers=false 时跳过数字叶子（gemini
// functionResponse.response 仅扫字符串叶子）。
func (c *sanitizeContext) scanValue(value any, includeNumbers bool) any {
	switch typed := value.(type) {
	case string:
		return c.sanitizeText(typed)
	case json.Number:
		if includeNumbers {
			return c.sanitizeNumberLeaf(typed)
		}
		return typed
	case []any:
		for index, item := range typed {
			typed[index] = c.scanValue(item, includeNumbers)
		}
		return typed
	case *orderedObject:
		for _, key := range typed.keys {
			typed.values[key] = c.scanValue(typed.values[key], includeNumbers)
		}
		return typed
	default:
		return value
	}
}

// scanInnerJSON 解析"字符串化 JSON"节点并扫描叶子，返回（新文本, 是否已处
// 理）。解析失败返回 handled=false（调用方决定计 skips 还是退回普通文本）；
// 可解析容器（对象/数组）按叶子扫描后紧凑重序列化（设计 §5「按原紧凑形态重
// 新序列化」），未命中时保留原字节；可解析标量按普通文本扫描。
func (c *sanitizeContext) scanInnerJSON(text string, includeNumbers bool) (string, bool) {
	value, err := parseOrderedJSON([]byte(text))
	if err != nil {
		return text, false
	}
	switch value.(type) {
	case *orderedObject, []any:
	default:
		return c.sanitizeText(text), true
	}
	c.scanValue(value, includeNumbers)
	serialized := serializeOrdered(value)
	if bytes.Equal(serialized, []byte(text)) {
		return text, true
	}
	c.changed = true
	return string(serialized), true
}

// innerJSON 处理工具参数类节点（chat arguments：设计 §5 工具参数类——解析失
// 败该节点跳过并计一次 skips，fail-open）。
func (c *sanitizeContext) innerJSON(text string) string {
	if newText, handled := c.scanInnerJSON(text, true); handled {
		return newText
	}
	c.skips++
	return text
}

// innerJSONOrText 处理 function_call_output.output（设计 §5：字符串内容与内
// 层 JSON 叶子值都属扫描对象）：解析失败按普通文本扫描，不计 skips——字符串
// 内容是合法形态，不算 fail-open。
func (c *sanitizeContext) innerJSONOrText(text string) string {
	if newText, handled := c.scanInnerJSON(text, true); handled {
		return newText
	}
	return c.sanitizeText(text)
}

func asArray(value any) []any {
	items, _ := value.([]any)
	return items
}

func asObject(value any) *orderedObject {
	object, _ := value.(*orderedObject)
	return object
}

// walkOpenAIChat 扫描 messages[].content（字符串 / content[].text）、
// messages[].name（设计 §5「tool_call_id 之外的 name 字段」）、
// messages[].reasoning_content（客户端回传的推理文本，DeepSeek/GLM 链路——设
// 计 §5「客户端回传的推理/思考文本字段同样属扫描对象」）与
// tool_calls[].function.arguments（内层 JSON）。明确不扫：image_url / audio /
// file_id、model、tool 名（tool_calls[].function.name）、metadata、
// tool_call_id。
func walkOpenAIChat(ctx *sanitizeContext, root *orderedObject) {
	for _, item := range asArray(root.get("messages")) {
		message := asObject(item)
		if message == nil {
			continue
		}
		switch content := message.get("content").(type) {
		case string:
			message.set("content", ctx.sanitizeText(content))
		case []any:
			for _, part := range content {
				partObject := asObject(part)
				if partObject == nil {
					continue
				}
				if text, ok := partObject.get("text").(string); ok {
					partObject.set("text", ctx.sanitizeText(text))
				}
			}
		}
		if name, ok := message.get("name").(string); ok {
			message.set("name", ctx.sanitizeText(name))
		}
		if reasoning, ok := message.get("reasoning_content").(string); ok {
			message.set("reasoning_content", ctx.sanitizeText(reasoning))
		}
		for _, call := range asArray(message.get("tool_calls")) {
			callObject := asObject(call)
			if callObject == nil {
				continue
			}
			function := asObject(callObject.get("function"))
			if function == nil {
				continue
			}
			if args, ok := function.get("arguments").(string); ok {
				function.set("arguments", ctx.innerJSON(args))
			}
		}
	}
}

// walkOpenAIResponses 扫描 instructions、input 顶层字符串形态（OpenAI
// Responses API 的 input 允许直接传字符串 prompt）与
// input[].content[].text、function_call_output.output（含内层 JSON）。
func walkOpenAIResponses(ctx *sanitizeContext, root *orderedObject) {
	if instructions, ok := root.get("instructions").(string); ok {
		root.set("instructions", ctx.sanitizeText(instructions))
	}
	if inputText, ok := root.get("input").(string); ok {
		root.set("input", ctx.sanitizeText(inputText))
	}
	for _, item := range asArray(root.get("input")) {
		itemObject := asObject(item)
		if itemObject == nil {
			continue
		}
		for _, part := range asArray(itemObject.get("content")) {
			partObject := asObject(part)
			if partObject == nil {
				continue
			}
			if text, ok := partObject.get("text").(string); ok {
				partObject.set("text", ctx.sanitizeText(text))
			}
		}
		if output, ok := itemObject.get("output").(string); ok {
			itemObject.set("output", ctx.innerJSONOrText(output))
		}
	}
}

// walkAnthropic 扫描 system（字符串或块数组）、messages[].content（字符串或
// 块数组），块级含 text、thinking（客户端回传的思考文本，设计 §5 实现约束
// 「客户端回传的推理/思考文本字段同样属扫描对象」）、tool_use.input、
// tool_result.content（设计 §5）。
func walkAnthropic(ctx *sanitizeContext, root *orderedObject) {
	scanAnthropicTextValue(ctx, root, "system")
	for _, item := range asArray(root.get("messages")) {
		message := asObject(item)
		if message == nil {
			continue
		}
		scanAnthropicTextValue(ctx, message, "content")
	}
}

// scanAnthropicTextValue 扫描 anthropic 的 system / content 形态值：字符串直
// 扫，块数组递归。
func scanAnthropicTextValue(ctx *sanitizeContext, holder *orderedObject, key string) {
	switch value := holder.get(key).(type) {
	case string:
		holder.set(key, ctx.sanitizeText(value))
	case []any:
		scanAnthropicBlocks(ctx, value)
	}
}

func scanAnthropicBlocks(ctx *sanitizeContext, blocks []any) {
	for _, item := range blocks {
		block := asObject(item)
		if block == nil {
			continue
		}
		switch blockType, _ := block.get("type").(string); blockType {
		case "tool_use":
			if input := block.get("input"); input != nil {
				block.set("input", ctx.scanValue(input, true))
			}
		case "tool_result":
			scanAnthropicTextValue(ctx, block, "content")
		default:
			if text, ok := block.get("text").(string); ok {
				block.set("text", ctx.sanitizeText(text))
			}
			// thinking 块回传（{"type":"thinking","thinking":...}）：思考文本
			// 是客户端回传的会话内容，同样属扫描对象（设计 §5 实现约束）。
			// redacted_thinking 的 data 为密文，不含文本叶子，不在此扫描。
			if thinking, ok := block.get("thinking").(string); ok {
				block.set("thinking", ctx.sanitizeText(thinking))
			}
		}
	}
}

// walkGemini 扫描 systemInstruction.parts[].text、contents[].parts[].text、
// functionCall.args、functionResponse.response（设计 §5：args 扫字符串+数字
// 叶子，response 仅字符串叶子）。
func walkGemini(ctx *sanitizeContext, root *orderedObject) {
	if instruction := asObject(root.get("systemInstruction")); instruction != nil {
		scanGeminiParts(ctx, instruction)
	}
	for _, item := range asArray(root.get("contents")) {
		if content := asObject(item); content != nil {
			scanGeminiParts(ctx, content)
		}
	}
}

func scanGeminiParts(ctx *sanitizeContext, holder *orderedObject) {
	for _, item := range asArray(holder.get("parts")) {
		part := asObject(item)
		if part == nil {
			continue
		}
		if text, ok := part.get("text").(string); ok {
			part.set("text", ctx.sanitizeText(text))
			continue
		}
		if call := asObject(part.get("functionCall")); call != nil {
			if args := call.get("args"); args != nil {
				call.set("args", ctx.scanValue(args, true))
			}
			continue
		}
		if response := asObject(part.get("functionResponse")); response != nil {
			switch value := response.get("response").(type) {
			case string:
				response.set("response", ctx.sanitizeText(value))
			case *orderedObject:
				response.set("response", ctx.scanValue(value, false))
			}
		}
	}
}

// walkMedia 扫描媒体域 prompt/text 类字段：顶层 prompt、negative_prompt、
// text、text_prompt、input（字符串或对象内 prompt/negative_prompt）、
// instances[].prompt（任务契约 §2；媒体响应无还原需求，§6.2 不注入）。
func walkMedia(ctx *sanitizeContext, root *orderedObject) {
	for _, key := range []string{"prompt", "negative_prompt", "text", "text_prompt"} {
		if text, ok := root.get(key).(string); ok {
			root.set(key, ctx.sanitizeText(text))
		}
	}
	switch input := root.get("input").(type) {
	case string:
		root.set("input", ctx.sanitizeText(input))
	case *orderedObject:
		for _, key := range []string{"prompt", "negative_prompt"} {
			if text, ok := input.get(key).(string); ok {
				input.set(key, ctx.sanitizeText(text))
			}
		}
	}
	for _, item := range asArray(root.get("instances")) {
		instance := asObject(item)
		if instance == nil {
			continue
		}
		if text, ok := instance.get("prompt").(string); ok {
			instance.set("prompt", ctx.sanitizeText(text))
		}
	}
}

// walkTool 扫描 B7 工具端点的文本输入：
//   - embeddings / moderations 的 input：字符串或字符串数组（token id 数字
//     数组不扫，避免把 token ID 当敏感数字改写）；
//   - rerank 的 query 与 documents：documents 支持字符串数组或 {text} 对象；
//   - moderations 多模态 input[] 的 {type:"text",text} 条目；
//   - 媒体/对话类字段若混入（prompt/text），一并按文本扫描。
func walkTool(ctx *sanitizeContext, root *orderedObject) {
	switch input := root.get("input").(type) {
	case string:
		root.set("input", ctx.sanitizeText(input))
	case []any:
		for index, item := range input {
			switch typed := item.(type) {
			case string:
				input[index] = ctx.sanitizeText(typed)
			case *orderedObject:
				if text, ok := typed.get("text").(string); ok {
					typed.set("text", ctx.sanitizeText(text))
				}
			}
		}
	case *orderedObject:
		if text, ok := input.get("text").(string); ok {
			input.set("text", ctx.sanitizeText(text))
		}
	}
	if query, ok := root.get("query").(string); ok {
		root.set("query", ctx.sanitizeText(query))
	}
	if documents, ok := root.get("documents").([]any); ok {
		for index, item := range documents {
			switch typed := item.(type) {
			case string:
				documents[index] = ctx.sanitizeText(typed)
			case *orderedObject:
				if text, ok := typed.get("text").(string); ok {
					typed.set("text", ctx.sanitizeText(text))
				}
			}
		}
	}
	for _, key := range []string{"prompt", "text", "text_prompt"} {
		if text, ok := root.get(key).(string); ok {
			root.set(key, ctx.sanitizeText(text))
		}
	}
}
