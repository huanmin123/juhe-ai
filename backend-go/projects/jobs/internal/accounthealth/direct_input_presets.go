package accounthealth

import (
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
)

// J1 专用 preset→profile 映射（供应商解耦 §8 的 jobs 侧扩展）。
//
// 共享 exactkeyprobe.ConnectionPresetProfileID 刻意保持 10 对对话 preset：
// J3a 代理延迟、代理档案读面与账户投影共用该表，J1 扩面不得改变其 target
// 集。J1 候选面需要额外覆盖 volcengine_ark / qwen_dashscope 两个对话厂商
// preset，故在 jobs 侧以“共享 10 对 + J1 扩展对”组装 J1 唯一映射；共享表
// 不新增、不修改。
var directInputPresetProfileAdditions = []struct {
	preset  string
	profile string
}{
	{"volcengine_ark", "profile_volcengine_openai_v1"},
	{"qwen_dashscope", "profile_qwen_openai_v1"},
}

// directInputProfileIDForPreset 是 J1 候选行的 preset→profile 派生入口：
// J1 扩展对优先，其余回落共享映射（10 个既有对话 preset）。未知 preset
// 返回 false，由 exactkeyprobe 的 profile 门禁 fail closed。
func directInputProfileIDForPreset(preset string) (string, bool) {
	trimmed := strings.TrimSpace(preset)
	for _, item := range directInputPresetProfileAdditions {
		if item.preset == trimmed {
			return item.profile, true
		}
	}
	return exactkeyprobe.ConnectionPresetProfileID(trimmed)
}

// directInputPresetForProfileID 是 directInputProfileIDForPreset 的逆向查询
// （J1 扩面对优先，其余回落共享映射）。仅供 J1 映射自检与后续读面复用，
// 不改变共享 ConnectionPresetForProfileID 的读面语义。
func directInputPresetForProfileID(profileID string) (string, bool) {
	trimmed := strings.TrimSpace(profileID)
	for _, item := range directInputPresetProfileAdditions {
		if item.profile == trimmed {
			return item.preset, true
		}
	}
	return exactkeyprobe.ConnectionPresetForProfileID(trimmed)
}
