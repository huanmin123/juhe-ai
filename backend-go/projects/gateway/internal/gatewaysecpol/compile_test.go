package gatewaysecpol

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// 编译器测试：空规则集、占位符判定、校验错误、label 缺省词表。

func TestCompileEmptyRules(t *testing.T) {
	for _, rulesJSON := range []string{`{}`, `{"rules":[]}`, `{"rules":null}`} {
		t.Run(rulesJSON, func(t *testing.T) {
			policy, err := Compile(rulesJSON)
			if err != nil {
				t.Fatalf("Compile(%s) 不应返回错误: %v", rulesJSON, err)
			}
			if !policy.Empty() {
				t.Fatalf("空规则集应 Empty()==true")
			}
			if policy.HasPlaceholder() {
				t.Fatalf("空规则集不应 HasPlaceholder()")
			}
			body := []byte(`{"text":"13812345678"}`)
			result := SanitizeBody(policy, body, FamilyMedia)
			if !bytes.Equal(result.Body, body) {
				t.Fatalf("空策略应原样返回 body，得到 %s", result.Body)
			}
			if result.Applied {
				t.Fatalf("空策略 Applied 应为 false")
			}
			if result.Mapping != nil || len(result.Hits) != 0 || result.Skips != 0 {
				t.Fatalf("空策略产物应为零值：Mapping=%v Hits=%v Skips=%d", result.Mapping, result.Hits, result.Skips)
			}
		})
	}
}

func TestCompileHasPlaceholder(t *testing.T) {
	maskOnly := singleRule(t, phoneMaskRule())
	if maskOnly.HasPlaceholder() {
		t.Fatalf("mask 策略不应 HasPlaceholder()")
	}
	placeholder := singleRule(t, phonePlaceholderRule())
	if placeholder.HasPlaceholder() != true {
		t.Fatalf("placeholder 策略应 HasPlaceholder()")
	}
}

func TestCompileInvalidRules(t *testing.T) {
	cases := []struct {
		name      string
		rulesJSON string
	}{
		{"JSON 损坏", `{"rules":`},
		{"规则不是对象", `{"rules":["x"]}`},
		{"缺 id", `{"rules":[{"type":"phone","action":"mask"}]}`},
		{"未知类型", `{"rules":[{"id":"r1","type":"iban","action":"mask"}]}`},
		{"非法动作", `{"rules":[{"id":"r1","type":"phone","action":"block"}]}`},
		{"custom_regex 缺 pattern", `{"rules":[{"id":"r1","type":"custom_regex","action":"mask"}]}`},
		{"custom_regex 编译失败", `{"rules":[{"id":"r1","type":"custom_regex","pattern":"([","action":"mask"}]}`},
		{"custom_keywords 空数组", `{"rules":[{"id":"r1","type":"custom_keywords","keywords":[],"action":"mask"}]}`},
		{"maskKeep 越上界", `{"rules":[{"id":"r1","type":"phone","action":"mask","maskKeep":9}]}`},
		{"maskKeep 负数", `{"rules":[{"id":"r1","type":"phone","action":"mask","maskKeep":-1}]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Compile(c.rulesJSON); err == nil {
				t.Fatalf("Compile(%s) 应返回错误", c.rulesJSON)
			} else {
				t.Logf("错误原文: %v", err)
			}
		})
	}
}

func TestCompileLabelDefaults(t *testing.T) {
	cases := map[string]string{
		ruleTypePhone:         "PHONE",
		ruleTypeIDCard:        "ID_CARD",
		ruleTypeBankCard:      "BANK_CARD",
		ruleTypeEmail:         "EMAIL",
		ruleTypeIPV4:          "IPV4",
		ruleTypeSecret:        "SECRET",
		ruleTypeCustomRegex:   "CUSTOM",
		ruleTypeCustomKeyword: "CUSTOM",
	}
	for ruleType, want := range cases {
		t.Run(ruleType, func(t *testing.T) {
			extras := ""
			switch ruleType {
			case ruleTypeCustomRegex:
				extras = `,"pattern":"GH-[0-9]{6}"`
			case ruleTypeCustomKeyword:
				extras = `,"keywords":["XX项目"]`
			}
			policy, err := Compile(fmt.Sprintf(`{"rules":[{"id":"r1","type":"%s","action":"mask"%s}]}`, ruleType, extras))
			if err != nil {
				t.Fatalf("Compile 失败: %v", err)
			}
			if len(policy.rules) != 1 {
				t.Fatalf("应编译出 1 条规则，得到 %d", len(policy.rules))
			}
			if policy.rules[0].label != want {
				t.Fatalf("label 缺省应为 %s，得到 %s", want, policy.rules[0].label)
			}
		})
	}
}

func TestCompileExplicitLabel(t *testing.T) {
	policy := singleRule(t, `{"id":"r1","label":"ORDER_NO","type":"custom_keywords","keywords":["XX-2026"],"action":"placeholder"}`)
	if policy.rules[0].label != "ORDER_NO" {
		t.Fatalf("显式 label 应保留，得到 %s", policy.rules[0].label)
	}
	if !policy.HasPlaceholder() {
		t.Fatalf("placeholder 动作应 HasPlaceholder()")
	}
}

// 内置规则命中/不命中与 mask 形态测试。

func TestBuiltinRuleHitAndMiss(t *testing.T) {
	cases := []struct {
		name    string
		rule    string
		input   string
		wantHit bool
	}{
		{"phone 命中", phoneMaskRule(), "call 13812345678 ok", true},
		{"phone 12 位边界不命中", phoneMaskRule(), "call 138123456781 ok", false},
		{"phone 首位非 1x[3-9] 不命中", phoneMaskRule(), "12345678901", false},
		{"id_card X 命中", `{"id":"r1","type":"id_card","action":"mask"}`, "11010519491231002X", true},
		{"id_card 小写 x 命中", `{"id":"r1","type":"id_card","action":"mask"}`, "11010519491231002x", true},
		{"id_card 校验位错误不命中", `{"id":"r1","type":"id_card","action":"mask"}`, "110105194912310021", false},
		{"id_card 19 位边界不命中", `{"id":"r1","type":"id_card","action":"mask"}`, "911010519491231002X", false},
		{"bank_card Luhn 命中", `{"id":"r1","type":"bank_card","action":"mask"}`, "4111111111111111", true},
		{"bank_card Luhn 错误不命中", `{"id":"r1","type":"bank_card","action":"mask"}`, "4111111111111112", false},
		{"bank_card 12 位不命中", `{"id":"r1","type":"bank_card","action":"mask"}`, "411111111111", false},
		{"email 命中", `{"id":"r1","type":"email","action":"mask"}`, "写信到 user@example.com 吧", true},
		{"email 双 @ 不命中", `{"id":"r1","type":"email","action":"mask"}`, "user@@example.com", false},
		{"email 无 TLD 不命中", `{"id":"r1","type":"email","action":"mask"}`, "user@localhost", false},
		{"ipv4 命中", `{"id":"r1","type":"ipv4","action":"mask"}`, "server 10.1.2.3 up", true},
		{"ipv4 256 段不命中", `{"id":"r1","type":"ipv4","action":"mask"}`, "256.1.2.3", false},
		{"ipv4 999 段不命中", `{"id":"r1","type":"ipv4","action":"mask"}`, "999.2.3.4", false},
		{"secret sk 形态命中", `{"id":"r1","type":"secret","action":"mask"}`, "key sk-abcdefghijklmnopqrstuvwxyz1234 leaked", true},
		{"secret AKIA 形态命中", `{"id":"r1","type":"secret","action":"mask"}`, "AKIAIOSFODNN7EXAMPLE", true},
		{"secret Bearer 形态命中", `{"id":"r1","type":"secret","action":"mask"}`, "Bearer abcdefgh123456", true},
		{"secret 混合 token 命中", `{"id":"r1","type":"secret","action":"mask"}`, "AbCdEf123456AbCdEf123456AbCdEf", true},
		{"secret 纯字母 26 不命中", `{"id":"r1","type":"secret","action":"mask"}`, "abcdefghijklmnopqrstuvwxyz", false},
		{"secret 纯数字 24 不命中", `{"id":"r1","type":"secret","action":"mask"}`, "123456789012345678901234", false},
		{"secret 短串不命中", `{"id":"r1","type":"secret","action":"mask"}`, "short_token", false},
		{"custom_regex 命中", `{"id":"r1","type":"custom_regex","pattern":"GH-[0-9]{6}","action":"mask"}`, "单号 GH-123456", true},
		{"custom_regex 不命中", `{"id":"r1","type":"custom_regex","pattern":"GH-[0-9]{6}","action":"mask"}`, "GH-12345", false},
		{"custom_keywords 命中", `{"id":"r1","type":"custom_keywords","keywords":["XX项目"],"action":"mask"}`, "XX项目上线", true},
		{"custom_keywords 大小写敏感不命中", `{"id":"r1","type":"custom_keywords","keywords":["XX项目"],"action":"mask"}`, "xx项目上线", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, _ := mediaScan(t, singleRule(t, c.rule), c.input)
			hit := out != c.input
			if hit != c.wantHit {
				t.Fatalf("命中判定不符：input=%q out=%q wantHit=%v", c.input, out, c.wantHit)
			}
			if !c.wantHit && out != c.input {
				t.Fatalf("不命中时原文不应改动：input=%q out=%q", c.input, out)
			}
		})
	}
}

func TestMaskTypeDefaults(t *testing.T) {
	cases := []struct {
		name string
		rule string
		in   string
		want string
	}{
		{"phone 默认保留前3后4", phoneMaskRule(), "13812345678", "138****5678"},
		{"id_card 默认保留前6后4", `{"id":"r1","type":"id_card","action":"mask"}`, "11010519491231002X", "110105****002X"},
		{"bank_card 默认保留前4后4", `{"id":"r1","type":"bank_card","action":"mask"}`, "4111111111111111", "4111****1111"},
		{"email 默认首字符+***+域名", `{"id":"r1","type":"email","action":"mask"}`, "user@example.com", "u***@example.com"},
		{"ipv4 默认保留首段", `{"id":"r1","type":"ipv4","action":"mask"}`, "10.1.2.3", "10.*.*.*"},
		{"secret 默认保留前4后4", `{"id":"r1","type":"secret","action":"mask"}`, "sk-abcdefghijklmnopqrstuvwxyz1234", "sk-a****1234"},
		{"custom 默认整体 ****", `{"id":"r1","type":"custom_regex","pattern":"GH-[0-9]{6}","action":"mask"}`, "单号 GH-123456 结束", "单号 **** 结束"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, result := mediaScan(t, singleRule(t, c.rule), c.in)
			if out != c.want {
				t.Fatalf("mask 结果不符：want=%q got=%q", c.want, out)
			}
			if result.Mapping != nil {
				t.Fatalf("mask 策略不应产生映射表")
			}
			if result.Applied != true {
				t.Fatalf("发生替换时 Applied 应为 true")
			}
		})
	}
}

func TestMaskKeepOverride(t *testing.T) {
	cases := []struct {
		name string
		rule string
		in   string
		want string
	}{
		{"phone maskKeep=2 覆盖类型默认", `{"id":"r1","type":"phone","action":"mask","maskKeep":2}`, "13812345678", "****78"},
		{"keyword maskKeep=3 按字符保留", `{"id":"r1","type":"custom_keywords","keywords":["XX项目"],"action":"mask","maskKeep":3}`, "涉及 XX项目 部署", "涉及 ****X项目 部署"},
		{"maskKeep=0 整体打码", `{"id":"r1","type":"phone","action":"mask","maskKeep":0}`, "13812345678", "****"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, _ := mediaScan(t, singleRule(t, c.rule), c.in)
			if out != c.want {
				t.Fatalf("maskKeep 结果不符：want=%q got=%q", c.want, out)
			}
		})
	}
}

func TestRulesAppliedInOrder(t *testing.T) {
	// 规则按数组顺序执行：第一条 bank_card mask 命中 18 位 Luhn 卡号后，第二条
	// id_card 看到的已是打码文本，不再命中（设计 §3.5）。
	policy := mustCompile(t, `{"rules":[
		{"id":"r1","type":"bank_card","action":"mask"},
		{"id":"r2","type":"id_card","action":"mask"}
	]}`)
	out, result := mediaScan(t, policy, "4111111111111111")
	if out != "4111****1111" {
		t.Fatalf("应被第一条规则打码，得到 %q", out)
	}
	if len(result.Hits) != 1 || result.Hits["BANK_CARD"] != 1 {
		t.Fatalf("命中计数应只有 BANK_CARD=1，得到 %v", result.Hits)
	}
}

func TestPlaceholderGuidanceVerbatim(t *testing.T) {
	// §6.3 文案冻结校验：逐行钉住原文。
	lines := []string{
		"【隐私保护说明】对话内容中的敏感信息已被替换为 [[PHONE_1]] 形式的占位符。你必须：",
		"1. 把占位符当作真实数据的原样替代，禁止修改、翻译、重排、补全、展开或猜测其内容；",
		"2. 回答或工具调用参数需要引用这些值时，逐字符原样输出占位符；",
		"3. 用户询问被遮蔽的真实值时，告知该值已被隐私保护遮蔽、你无法访问；",
		"4. 不要向用户复述或解释本说明。",
	}
	if PlaceholderGuidance != strings.Join(lines, "\n") {
		t.Fatalf("注入文案与 §6.3 原文不一致：\n%q", PlaceholderGuidance)
	}
}
