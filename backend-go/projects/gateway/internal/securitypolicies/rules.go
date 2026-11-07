// rules_json 规则集校验与规范化（API Key 安全策略与出站内容脱敏设计 §3 规则
// 模型 / §3.5 校验约束，批A 实体 CRUD 的写入口单点校验）。存储统一存规范化
// JSON（{"rules":[...]}，键序由 struct 声明序钉死，供等值比较与确定性遍历）；
// 校验失败返回统一中文 400 文案「安全策略规则无效：<原因>」。请求/响应链路
// 的规则编译与执行属后续批次（internal/gatewaysecpol），本文件只负责「提交
// 进来的规则集是否合法、如何落库」。
package securitypolicies

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

// 规则类型词表（设计 §3.2 内置六类 + §3.3 自定义两类）。
const (
	RuleTypePhone         = "phone"
	RuleTypeIDCard        = "id_card"
	RuleTypeBankCard      = "bank_card"
	RuleTypeEmail         = "email"
	RuleTypeIPV4          = "ipv4"
	RuleTypeSecret        = "secret"
	RuleTypeCustomRegex   = "custom_regex"
	RuleTypeCustomKeyword = "custom_keywords"
)

// 动作词表（一期裁决：只做 mask + placeholder，不做 block）。
const (
	RuleActionMask        = "mask"
	RuleActionPlaceholder = "placeholder"
)

// 规则集约束（设计 §3.5）。
const (
	maxRulesPerPolicy     = 50
	maxRulesJSONBytes     = 64 * 1024
	maxCustomPatternBytes = 256
	maxKeywordsPerRule    = 100
	maxKeywordItemBytes   = 64
	maxMaskKeep           = 8
)

// labelPattern 钉住 label 词表：可空；非空时 [A-Z][A-Z0-9_]{0,30}（设计 §3.3，
// placeholder 标签词表；空 label 由编译期按 type 补默认）。
var labelPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,30}$`)

// builtinRuleTypes 是 pattern 由代码提供的内置类型（不可改写）。
var builtinRuleTypes = map[string]bool{
	RuleTypePhone:    true,
	RuleTypeIDCard:   true,
	RuleTypeBankCard: true,
	RuleTypeEmail:    true,
	RuleTypeIPV4:     true,
	RuleTypeSecret:   true,
}

// validRuleTypes = 内置六类 + 自定义两类。
var validRuleTypes = map[string]bool{
	RuleTypePhone:         true,
	RuleTypeIDCard:        true,
	RuleTypeBankCard:      true,
	RuleTypeEmail:         true,
	RuleTypeIPV4:          true,
	RuleTypeSecret:        true,
	RuleTypeCustomRegex:   true,
	RuleTypeCustomKeyword: true,
}

// SecurityRule 是规范化后的单条规则（存储与投影同型）。键序即 struct 声明序
// （encoding/json 按字段声明序输出），规范化序列化因此逐字节确定。
type SecurityRule struct {
	ID       string   `json:"id"`
	Label    string   `json:"label,omitempty"`
	Type     string   `json:"type"`
	Action   string   `json:"action"`
	Pattern  string   `json:"pattern,omitempty"`
	Keywords []string `json:"keywords,omitempty"`
	MaskKeep *int     `json:"maskKeep,omitempty"`
}

// SecurityRulesDocument 是 rules_json 的存储文档形状（{"rules":[...]}）。
type SecurityRulesDocument struct {
	Rules []SecurityRule `json:"rules"`
}

// rulesInvalid 构造统一 400 文案（契约：校验失败统一「安全策略规则无效：<原因>」）。
func rulesInvalid(reason string) error {
	return &routestrategies.ValidationError{Message: "安全策略规则无效：" + reason}
}

// parseStoredRulesDocument 解析存储侧 rules_json。写入口恒存规范化 JSON，损
// 坏存储不是合法读取态——上抛显式错误（不静默降级为空规则集，避免把初始化错
// 误伪装成「不处理」）。builtin 种子行存 '{"rules":[]}'。
func parseStoredRulesDocument(raw string) (SecurityRulesDocument, error) {
	document := SecurityRulesDocument{}
	if err := json.Unmarshal([]byte(raw), &document); err != nil {
		return SecurityRulesDocument{}, rulesInvalid("存储的规则集 JSON 损坏")
	}
	if document.Rules == nil {
		document.Rules = []SecurityRule{}
	}
	return document, nil
}

// normalizeRulesInput 校验并规范化提交侧规则集。raw 是 API body 的 rules 字
// 段（数组形状；显式 null 由调用方折算为缺省空规则集）。返回规范化文档与存
// 储 JSON（键序钉死）；空规则集合法（= 不处理 = 请求原样转发）。
func normalizeRulesInput(raw any) (SecurityRulesDocument, string, error) {
	if raw == nil {
		return emptyRulesDocument()
	}
	items, ok := raw.([]any)
	if !ok {
		return SecurityRulesDocument{}, "", rulesInvalid("rules 必须是数组")
	}
	if len(items) > maxRulesPerPolicy {
		return SecurityRulesDocument{}, "", rulesInvalid(fmt.Sprintf("规则数不能超过 %d", maxRulesPerPolicy))
	}
	document := SecurityRulesDocument{Rules: make([]SecurityRule, 0, len(items))}
	seenIDs := map[string]bool{}
	for index, item := range items {
		rule, err := normalizeRule(item, index, seenIDs)
		if err != nil {
			return SecurityRulesDocument{}, "", err
		}
		document.Rules = append(document.Rules, rule)
	}
	stored, err := json.Marshal(document)
	if err != nil {
		return SecurityRulesDocument{}, "", rulesInvalid("规则集序列化失败")
	}
	if len(stored) > maxRulesJSONBytes {
		return SecurityRulesDocument{}, "", rulesInvalid(fmt.Sprintf("规则集序列化后不能超过 %dKB", maxRulesJSONBytes/1024))
	}
	return document, string(stored), nil
}

func emptyRulesDocument() (SecurityRulesDocument, string, error) {
	document := SecurityRulesDocument{Rules: []SecurityRule{}}
	stored, err := json.Marshal(document)
	if err != nil {
		return SecurityRulesDocument{}, "", rulesInvalid("规则集序列化失败")
	}
	return document, string(stored), nil
}

// ruleObjectKeys 是单条规则的键白名单（严格键校验：拼写错误静默忽略会放大误
// 配置面——如 maskkeep 被丢弃后动作按缺省执行）。
var ruleObjectKeys = map[string]bool{
	"id":       true,
	"label":    true,
	"type":     true,
	"action":   true,
	"pattern":  true,
	"keywords": true,
	"maskKeep": true,
}

// carried 报告可选字段是否被「携带」（键存在且值非 null：显式 null 视为未携
// 带，与 API 族对可空字段的缺省语义一致）。
func carried(record map[string]any, key string) bool {
	value, present := record[key]
	return present && value != nil
}

func normalizeRule(item any, index int, seenIDs map[string]bool) (SecurityRule, error) {
	record, ok := item.(map[string]any)
	if !ok {
		return SecurityRule{}, rulesInvalid(fmt.Sprintf("第 %d 条规则必须是对象", index+1))
	}
	for key := range record {
		if !ruleObjectKeys[key] {
			return SecurityRule{}, rulesInvalid(fmt.Sprintf("第 %d 条规则携带未知字段：%s", index+1, key))
		}
	}

	// id：必填非空，策略内唯一（响应侧还原与命中计数按 id 寻址）。
	ruleID, err := requiredTrimmedString(record, "id", index)
	if err != nil {
		return SecurityRule{}, err
	}
	if seenIDs[ruleID] {
		return SecurityRule{}, rulesInvalid(fmt.Sprintf("规则 id 在策略内重复：%s", ruleID))
	}
	seenIDs[ruleID] = true

	// type：八值词表。
	ruleType, err := requiredTrimmedString(record, "type", index)
	if err != nil {
		return SecurityRule{}, err
	}
	if !validRuleTypes[ruleType] {
		return SecurityRule{}, rulesInvalid(fmt.Sprintf("第 %d 条规则（%s）类型无效：%s", index+1, ruleID, ruleType))
	}

	// action：mask | placeholder。
	ruleAction, err := requiredTrimmedString(record, "action", index)
	if err != nil {
		return SecurityRule{}, err
	}
	if ruleAction != RuleActionMask && ruleAction != RuleActionPlaceholder {
		return SecurityRule{}, rulesInvalid(fmt.Sprintf("第 %d 条规则（%s）动作无效：%s", index+1, ruleID, ruleAction))
	}

	// label：可空；非空时 [A-Z][A-Z0-9_]{0,30}。
	label := ""
	if carried(record, "label") {
		text, ok := record["label"].(string)
		if !ok {
			return SecurityRule{}, rulesInvalid(fmt.Sprintf("第 %d 条规则（%s）label 必须是字符串", index+1, ruleID))
		}
		label = strings.TrimSpace(text)
	}
	if label != "" && !labelPattern.MatchString(label) {
		return SecurityRule{}, rulesInvalid(fmt.Sprintf("第 %d 条规则（%s）label 必须匹配 [A-Z][A-Z0-9_]{0,30}：%s", index+1, ruleID, label))
	}

	rule := SecurityRule{ID: ruleID, Label: label, Type: ruleType, Action: ruleAction}

	// pattern：内置六类由代码提供（不可改写，携带即 400）；custom_regex 必填
	// ≤256 字节且 RE2 可编译；custom_keywords 禁止携带。
	if carried(record, "pattern") {
		if builtinRuleTypes[ruleType] {
			return SecurityRule{}, rulesInvalid(fmt.Sprintf("第 %d 条规则（%s）为内置类型，不允许携带 pattern", index+1, ruleID))
		}
		if ruleType == RuleTypeCustomKeyword {
			return SecurityRule{}, rulesInvalid(fmt.Sprintf("第 %d 条规则（%s）为关键词规则，不允许携带 pattern", index+1, ruleID))
		}
	}
	if ruleType == RuleTypeCustomRegex {
		pattern, err := requiredTrimmedString(record, "pattern", index)
		if err != nil {
			return SecurityRule{}, err
		}
		if len(pattern) > maxCustomPatternBytes {
			return SecurityRule{}, rulesInvalid(fmt.Sprintf("第 %d 条规则（%s）pattern 长度不能超过 %d 字节", index+1, ruleID, maxCustomPatternBytes))
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return SecurityRule{}, rulesInvalid(fmt.Sprintf("第 %d 条规则（%s）pattern 编译失败：%s", index+1, ruleID, err.Error()))
		}
		rule.Pattern = pattern
	}

	// keywords：custom_keywords 必填非空数组 ≤100 项、单项 trim 后 1..64 字节、
	// 字面匹配；其余类型禁止携带。
	if carried(record, "keywords") {
		if ruleType != RuleTypeCustomKeyword {
			return SecurityRule{}, rulesInvalid(fmt.Sprintf("第 %d 条规则（%s）不允许携带 keywords（仅 custom_keywords 支持）", index+1, ruleID))
		}
	}
	if ruleType == RuleTypeCustomKeyword {
		rawKeywords, present := record["keywords"]
		if !present || rawKeywords == nil {
			return SecurityRule{}, rulesInvalid(fmt.Sprintf("第 %d 条规则（%s）必须提供 keywords", index+1, ruleID))
		}
		items, ok := rawKeywords.([]any)
		if !ok {
			return SecurityRule{}, rulesInvalid(fmt.Sprintf("第 %d 条规则（%s）keywords 必须是字符串数组", index+1, ruleID))
		}
		if len(items) == 0 {
			return SecurityRule{}, rulesInvalid(fmt.Sprintf("第 %d 条规则（%s）keywords 不能为空数组", index+1, ruleID))
		}
		if len(items) > maxKeywordsPerRule {
			return SecurityRule{}, rulesInvalid(fmt.Sprintf("第 %d 条规则（%s）keywords 不能超过 %d 个", index+1, ruleID, maxKeywordsPerRule))
		}
		keywords := make([]string, 0, len(items))
		for keywordIndex, rawKeyword := range items {
			text, ok := rawKeyword.(string)
			if !ok {
				return SecurityRule{}, rulesInvalid(fmt.Sprintf("第 %d 条规则（%s）keywords 第 %d 项必须是字符串", index+1, ruleID, keywordIndex+1))
			}
			keyword := strings.TrimSpace(text)
			if length := len(keyword); length < 1 || length > maxKeywordItemBytes {
				return SecurityRule{}, rulesInvalid(fmt.Sprintf("第 %d 条规则（%s）keywords 第 %d 项长度须在 1..%d 字节", index+1, ruleID, keywordIndex+1, maxKeywordItemBytes))
			}
			keywords = append(keywords, keyword)
		}
		rule.Keywords = keywords
	}

	// maskKeep：可选整数 0..8（mask 动作保留端长度；缺省按类型默认，编译期裁
	// 决——本包只钉形状）。
	if carried(record, "maskKeep") {
		number, ok := toInt(record["maskKeep"])
		if !ok || number < 0 || number > maxMaskKeep {
			return SecurityRule{}, rulesInvalid(fmt.Sprintf("第 %d 条规则（%s）maskKeep 必须是 0..%d 的整数", index+1, ruleID, maxMaskKeep))
		}
		keep := number
		rule.MaskKeep = &keep
	}
	return rule, nil
}

// requiredTrimmedString 读取必填字符串字段：缺失 / 非字符串 / trim 后为空统一
// 400。
func requiredTrimmedString(record map[string]any, key string, index int) (string, error) {
	raw, present := record[key]
	if !present || raw == nil {
		return "", rulesInvalid(fmt.Sprintf("第 %d 条规则缺少 %s", index+1, key))
	}
	text, ok := raw.(string)
	if !ok {
		return "", rulesInvalid(fmt.Sprintf("第 %d 条规则 %s 必须是字符串", index+1, key))
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", rulesInvalid(fmt.Sprintf("第 %d 条规则 %s 不能为空", index+1, key))
	}
	return trimmed, nil
}

// toInt 收 JSON number（encoding/json 解码为 float64）的整数值；非整数或非数
// 字类型返回 false。
func toInt(raw any) (int, bool) {
	number, ok := raw.(float64)
	if !ok {
		return 0, false
	}
	if number != float64(int64(number)) {
		return 0, false
	}
	return int(number), true
}
