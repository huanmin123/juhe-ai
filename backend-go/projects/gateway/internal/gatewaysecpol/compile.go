package gatewaysecpol

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// 规则编译器：rules_json -> CompiledPolicy（设计 §3 规则模型 / §10 编译缓存：
// 编译产物只存进程内，regexp.Regexp 不可序列化，运行时快照不携带编译产物）。
//
// 规则类型与动作词表、SecurityRule 形状镜像 internal/securitypolicies/rules.go
// 的存储契约（设计 §3.1）；本包不 import securitypolicies——实体层携带存储
// 依赖，纯函数库只依赖 rules_json 文本形状。存储侧已做提交校验，Compile 仍复
// 核形状与可编译性：损坏的存储不是合法读取态，返回显式错误而非静默不处理。

// 规则类型词表（设计 §3.2 内置六类 + §3.3 自定义两类）。
const (
	ruleTypePhone         = "phone"
	ruleTypeIDCard        = "id_card"
	ruleTypeBankCard      = "bank_card"
	ruleTypeEmail         = "email"
	ruleTypeIPV4          = "ipv4"
	ruleTypeSecret        = "secret"
	ruleTypeCustomRegex   = "custom_regex"
	ruleTypeCustomKeyword = "custom_keywords"
)

// 动作词表（一期裁决：只做 mask + placeholder，不做 block）。
const (
	actionMask        = "mask"
	actionPlaceholder = "placeholder"
)

// maxMaskKeep 镜像存储侧 maskKeep 上限（rules.go：0..8）。
const maxMaskKeep = 8

// digitRuleTypes 是数字叶子可适用的 digit 类规则（任务契约 §3：数字叶子仅按
// phone/id_card/bank_card/ipv4/secret 的匹配形态扫描；email 与自定义规则不适
// 用）。ipv4 / secret 在纯十进制文本上不会命中，列入仅为词表完备。
var digitRuleTypes = map[string]bool{
	ruleTypePhone:    true,
	ruleTypeIDCard:   true,
	ruleTypeBankCard: true,
	ruleTypeIPV4:     true,
	ruleTypeSecret:   true,
}

// defaultLabels 是 label 缺省词表（设计 §3.3：按 type 默认）。
var defaultLabels = map[string]string{
	ruleTypePhone:         "PHONE",
	ruleTypeIDCard:        "ID_CARD",
	ruleTypeBankCard:      "BANK_CARD",
	ruleTypeEmail:         "EMAIL",
	ruleTypeIPV4:          "IPV4",
	ruleTypeSecret:        "SECRET",
	ruleTypeCustomRegex:   "CUSTOM",
	ruleTypeCustomKeyword: "CUSTOM",
}

// compiledRule 是编译后的单条规则，字段在 Compile 后只读。
type compiledRule struct {
	id        string
	label     string
	action    string // actionMask | actionPlaceholder
	digitOnly bool   // 仅作用于数字叶子（digitRuleTypes）
	matcher   ruleMatcher
	maskFn    func(match string) string // action=mask 时的打码函数
}

// CompiledPolicy 是编译后的安全策略，Compile 后字段只读，可跨请求并发复用
// （regexp.Regexp 并发安全；设计 §5 性能预算：编译一次、每请求只做匹配）。
type CompiledPolicy struct {
	rules          []compiledRule
	hasPlaceholder bool
}

// Empty 报告是否为空规则集（= 不处理 = 请求原样转发，设计 §1.1 目标 4）。
func (p *CompiledPolicy) Empty() bool {
	return p == nil || len(p.rules) == 0
}

// HasPlaceholder 报告策略是否含任意 placeholder 规则（设计 §6.2：静态判断即
// 注入依据，不按单请求命中动态判断）。
func (p *CompiledPolicy) HasPlaceholder() bool {
	return p != nil && p.hasPlaceholder
}

// compileDocument 镜像 securitypolicies.SecurityRulesDocument 的存储形状
// （{"rules":[...]}，设计 §3.1）。
type compileDocument struct {
	Rules []compileRuleInput `json:"rules"`
}

type compileRuleInput struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Type     string   `json:"type"`
	Action   string   `json:"action"`
	Pattern  string   `json:"pattern"`
	Keywords []string `json:"keywords"`
	MaskKeep *int     `json:"maskKeep"`
}

// Compile 编译 rules_json。空规则集合法（Empty()==true）。未知类型、非法动
// 作、custom_regex 编译失败、custom_keywords 空数组、maskKeep 越界均返回显
// 式错误。
func Compile(rulesJSON string) (*CompiledPolicy, error) {
	var document compileDocument
	if err := json.Unmarshal([]byte(rulesJSON), &document); err != nil {
		return nil, fmt.Errorf("gatewaysecpol: 规则集 JSON 解析失败: %w", err)
	}
	policy := &CompiledPolicy{}
	for index := range document.Rules {
		rule, err := compileRuleEntry(&document.Rules[index])
		if err != nil {
			return nil, err
		}
		policy.rules = append(policy.rules, rule)
		if rule.action == actionPlaceholder {
			policy.hasPlaceholder = true
		}
	}
	return policy, nil
}

func compileRuleEntry(raw *compileRuleInput) (compiledRule, error) {
	if raw.ID == "" {
		return compiledRule{}, fmt.Errorf("gatewaysecpol: 规则缺少 id")
	}
	if raw.Action != actionMask && raw.Action != actionPlaceholder {
		return compiledRule{}, fmt.Errorf("gatewaysecpol: 规则（%s）动作无效：%s", raw.ID, raw.Action)
	}
	label := raw.Label
	if label == "" {
		defaulted, ok := defaultLabels[raw.Type]
		if !ok {
			return compiledRule{}, fmt.Errorf("gatewaysecpol: 规则（%s）类型无效：%s", raw.ID, raw.Type)
		}
		label = defaulted
	}

	var matcher ruleMatcher
	switch raw.Type {
	case ruleTypePhone:
		matcher = &regexMatcher{re: phonePattern}
	case ruleTypeIDCard:
		matcher = &regexMatcher{re: idCardPattern, validate: idCardChecksumValid}
	case ruleTypeBankCard:
		matcher = &regexMatcher{re: bankCardPattern, validate: luhnValid}
	case ruleTypeEmail:
		matcher = &regexMatcher{re: emailPattern}
	case ruleTypeIPV4:
		matcher = &regexMatcher{re: ipv4Pattern}
	case ruleTypeSecret:
		matcher = &secretMatcher{forms: secretFormsPattern, generic: secretGenericPattern}
	case ruleTypeCustomRegex:
		if raw.Pattern == "" {
			return compiledRule{}, fmt.Errorf("gatewaysecpol: 规则（%s）缺少 pattern", raw.ID)
		}
		compiled, err := regexp.Compile(raw.Pattern)
		if err != nil {
			return compiledRule{}, fmt.Errorf("gatewaysecpol: 规则（%s）pattern 编译失败：%s", raw.ID, err.Error())
		}
		matcher = &regexMatcher{re: compiled}
	case ruleTypeCustomKeyword:
		if len(raw.Keywords) == 0 {
			return compiledRule{}, fmt.Errorf("gatewaysecpol: 规则（%s）keywords 不能为空", raw.ID)
		}
		matcher = &keywordsMatcher{keywords: raw.Keywords}
	default:
		return compiledRule{}, fmt.Errorf("gatewaysecpol: 规则（%s）类型无效：%s", raw.ID, raw.Type)
	}

	if raw.MaskKeep != nil && (*raw.MaskKeep < 0 || *raw.MaskKeep > maxMaskKeep) {
		return compiledRule{}, fmt.Errorf("gatewaysecpol: 规则（%s）maskKeep 必须是 0..%d 的整数", raw.ID, maxMaskKeep)
	}

	rule := compiledRule{
		id:        raw.ID,
		label:     label,
		action:    raw.Action,
		digitOnly: digitRuleTypes[raw.Type],
		matcher:   matcher,
	}
	if raw.Action == actionMask {
		rule.maskFn = makeMaskFn(raw.Type, raw.MaskKeep)
	}
	return rule, nil
}

// makeMaskFn 解析 mask 动作的打码函数（设计 §3.2 类型默认形态；maskKeep 显
// 式携带时覆盖类型默认：保留末端 maskKeep 个字符、前段固定 ****，保留计数按
// 字符而非字节）。显式 maskKeep=0 同样走通用形态（整体 ****）；缺省（nil）
// 按类型默认。自定义规则无类型默认，即整体 ****（保留 0）。
func makeMaskFn(ruleType string, maskKeep *int) func(string) string {
	if maskKeep != nil {
		keep := *maskKeep
		return func(match string) string {
			return "****" + tailChars(match, keep)
		}
	}
	switch ruleType {
	case ruleTypePhone:
		// 保留前 3 后 4：138****5678。
		return func(match string) string { return headChars(match, 3) + "****" + tailChars(match, 4) }
	case ruleTypeIDCard:
		// 保留前 6 后 4。
		return func(match string) string { return headChars(match, 6) + "****" + tailChars(match, 4) }
	case ruleTypeBankCard:
		// 保留前 4 后 4。
		return func(match string) string { return headChars(match, 4) + "****" + tailChars(match, 4) }
	case ruleTypeEmail:
		// 首字符 + *** + @域名。
		return func(match string) string {
			at := strings.Index(match, "@")
			if at < 0 {
				return "****"
			}
			return headChars(match, 1) + "***" + match[at:]
		}
	case ruleTypeIPV4:
		// 保留首段其余 *：10.*.*.*。
		return func(match string) string {
			dot := strings.Index(match, ".")
			if dot < 0 {
				return "****"
			}
			return match[:dot] + ".*.*.*"
		}
	case ruleTypeSecret:
		// 保留前 4 后 4，中段 ****。
		return func(match string) string {
			if len(match) <= 8 {
				return "****"
			}
			return headChars(match, 4) + "****" + tailChars(match, 4)
		}
	default:
		// 自定义规则：整体 ****（长度 4，保留 0）。
		return func(string) string { return "****" }
	}
}

// headChars / tailChars 按字符（rune）计数取首/尾片段，避免中文等多字节字符
// 被截断为非法 UTF-8。
func headChars(match string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(match)
	if n >= len(runes) {
		return match
	}
	return string(runes[:n])
}

func tailChars(match string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(match)
	if n >= len(runes) {
		return match
	}
	return string(runes[len(runes)-n:])
}
