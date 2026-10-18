package gatewayoauthcodex

import (
	"errors"
	"fmt"
	"testing"
)

// gpt 覆盖枚举断言（assertGptOverrideEnumValues）行为快照：作为收敛到
// shared/platform/requestoverride 单一真源切片（零行为差）的验收口径，穷举
// tier×effort 组合（合法值、大小写变体、首尾空白、超集/枚举外值）钉住接受
// 集合、拒绝集合、错误类型与错误文案。期望值全部手工写死，不引用真源词表
// 或实现内部常量，避免测试与实现同源循环论证。

const (
	gptEnumSnapshotTierInvalidMessage   = "GPT 账户请求覆盖字段 service_tier_override 无效"
	gptEnumSnapshotEffortInvalidMessage = "GPT 账户请求覆盖字段 reasoning_effort_override 无效"
)

var gptEnumSnapshotTierValues = []struct {
	value string
	valid bool
}{
	{"", true},
	{"default", true},
	{"priority", true},
	{"flex", true},
	{"Default", false},
	{"PRIORITY", false},
	{"Flex", false},
	{" default", false},
	{"default ", false},
	{" flex ", false},
	{"standard", false},
	{"auto", false},
	{"turbo", false},
	{"flexx", false},
	{"priority flex", false},
}

var gptEnumSnapshotEffortValues = []struct {
	value string
	valid bool
}{
	{"", true},
	{"none", true},
	{"minimal", true},
	{"low", true},
	{"medium", true},
	{"high", true},
	{"xhigh", true},
	{"max", true},
	{"None", false},
	{"Minimal", false},
	{"LOW", false},
	{"High", false},
	{"XHigh", false},
	{"Max", false},
	{" none", false},
	{"high ", false},
	{"xhigh ", false},
	{"ultra", false},
	{"maximum", false},
	{"min", false},
	{"xhighmax", false},
}

// checkGptOverrideEnumSnapshot 断言一次枚举断言调用的结果：want 为空串表示
// 必须接受（返回 nil），否则必须返回 *GptAccountRequestOverrideError 且文案
// 逐字相等。
func checkGptOverrideEnumSnapshot(t *testing.T, err error, overrides GptAccountRequestOverrides, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("overrides = %+v, err = %v, want 接受", overrides, err)
		}
		return
	}
	if err == nil {
		t.Fatalf("overrides = %+v, err = nil, want %q", overrides, want)
	}
	var overrideErr *GptAccountRequestOverrideError
	if !errors.As(err, &overrideErr) {
		t.Fatalf("overrides = %+v, error type = %T, want *GptAccountRequestOverrideError", overrides, err)
	}
	if err.Error() != want {
		t.Fatalf("overrides = %+v, err = %q, want %q", overrides, err.Error(), want)
	}
}

func TestAssertGptOverrideEnumValuesSnapshot(t *testing.T) {
	for _, tier := range gptEnumSnapshotTierValues {
		for _, effort := range gptEnumSnapshotEffortValues {
			want := ""
			switch {
			case !tier.valid:
				want = gptEnumSnapshotTierInvalidMessage
			case !effort.valid:
				want = gptEnumSnapshotEffortInvalidMessage
			}
			overrides := GptAccountRequestOverrides{ServiceTier: tier.value, ReasoningEffort: effort.value}
			t.Run(fmt.Sprintf("tier=%q/effort=%q", tier.value, effort.value), func(t *testing.T) {
				// 未导出实现与两个导出入口同语义。
				checkGptOverrideEnumSnapshot(t, assertGptOverrideEnumValues(overrides), overrides, want)
				checkGptOverrideEnumSnapshot(t, AssertGptAccountRequestOverrideValues(overrides), overrides, want)
				checkGptOverrideEnumSnapshot(t, AssertGptAccountRequestOverrideValuesForProvider(overrides, "gpt"), overrides, want)
				// providerCode 规范化（lowercase+trim）命中 gpt 同样执行枚举。
				checkGptOverrideEnumSnapshot(t, AssertGptAccountRequestOverrideValuesForProvider(overrides, " GPT "), overrides, want)
				// 非 gpt 与空 providerCode 跳过枚举（现状快照：一律放行）。
				checkGptOverrideEnumSnapshot(t, AssertGptAccountRequestOverrideValuesForProvider(overrides, "deepseek"), overrides, "")
				checkGptOverrideEnumSnapshot(t, AssertGptAccountRequestOverrideValuesForProvider(overrides, ""), overrides, "")
			})
		}
	}
}
