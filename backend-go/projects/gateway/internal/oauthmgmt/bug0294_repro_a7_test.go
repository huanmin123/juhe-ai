// BUG-0294 / A7 红绿回归（原最小复现定性载体，2026-10-23 修复后转回归）。
//
// 档案：docs/bug/问题-0294-迁移期行为存疑按现状断言未建档清点.md A7。
// 缺陷（已修复）：openai.go extractOpenAICodeAndState 的 fragment 臂
// normalizeText(fragment["code"]) 与裸 query 臂 normalizeText(query["code"])
// 把 url.Values 解析出的 []string 传给只认 string 的 normalizeText
// （transport.go:157）→ 恒空串 → 恒报「回调 URL 必须包含 code 和 state」；
// 函数 doc 宣称支持 query / fragment / bare "code#state" / "?code=..." 四种
// 形态，与实现矛盾。修复：两臂改用 url.Values.Get（首元素 + 缺键空串，
// 对应 Node 侧 new URLSearchParams(...).get('code')）。
//
// 本测试钉住修复后的契约：fragment 与裸 query 形态成功提取 code/state，
// 原正常臂（全 URL+query、"code#state"）不回归，缺 code/state 仍按原文案拒绝。
package oauthmgmt

import "testing"

// TestBug0294A7FragmentAndBareQueryExtract：A7 修复后回归。
func TestBug0294A7FragmentAndBareQueryExtract(t *testing.T) {
	// 修复臂 1：fragment 形态成功提取。
	if code, state, err := extractOpenAICodeAndState("https://cb#code=c2&state=s2"); err != nil || code != "c2" || state != "s2" {
		t.Fatalf("fragment 形态应成功: %q %q %v", code, state, err)
	}

	// 修复臂 2：裸 query 形态成功提取。
	if code, state, err := extractOpenAICodeAndState("?code=c4&state=s4"); err != nil || code != "c4" || state != "s4" {
		t.Fatalf("裸 query 形态应成功: %q %q %v", code, state, err)
	}

	// 对照臂不回归：全 URL+query 与 "code#state" 形态。
	if code, state, err := extractOpenAICodeAndState("https://cb?code=ok&state=s0"); err != nil || code != "ok" || state != "s0" {
		t.Fatalf("对照：全 URL+query 形态应成功: %q %q %v", code, state, err)
	}
	if code, state, err := extractOpenAICodeAndState("c3#s3"); err != nil || code != "c3" || state != "s3" {
		t.Fatalf("对照：code#state 形态应成功: %q %q %v", code, state, err)
	}

	// 拒绝臂不回归：缺 code / 缺 state / 两者皆缺，仍按原文案拒绝。
	for _, callbackURL := range []string{"https://cb#code=c5", "?state=s5", "https://cb"} {
		code, state, err := extractOpenAICodeAndState(callbackURL)
		if err == nil || err.Error() != "回调 URL 必须包含 code 和 state" || code != "" || state != "" {
			t.Fatalf("缺 code/state 应按原文案拒绝（%q）: %q %q %v", callbackURL, code, state, err)
		}
	}
}
