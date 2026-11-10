package gatewayscheduling

import "testing"

// 两键普通分组策略的纯函数契约：阈值换算、软忙判定、严格两键校验、旧行 16 键
// 按 globalMax 归一映射与退役分组类型读归一。

func TestDefaultPolicyIsDisabled(t *testing.T) {
	policy := DefaultPolicy()
	if policy.DefaultSoftConcurrency != -1 || policy.ClientIPConcurrencyLimit != 0 {
		t.Fatalf("default policy = %+v, want {-1, 0}", policy)
	}
	if !policy.IsDefault() {
		t.Fatal("default policy must report IsDefault")
	}
}

func TestSoftLimitArms(t *testing.T) {
	cases := []struct {
		name      string
		threshold int
		hardLimit int
		want      int
	}{
		{name: "disabled threshold", threshold: -1, hardLimit: 5, want: 0},
		{name: "zero threshold", threshold: 0, hardLimit: 5, want: 0},
		{name: "below hard limit", threshold: 3, hardLimit: 5, want: 3},
		{name: "clamped to hard limit", threshold: 9, hardLimit: 5, want: 5},
		{name: "hard limit floor 1", threshold: 3, hardLimit: 0, want: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy := Policy{DefaultSoftConcurrency: tc.threshold}
			if got := policy.SoftLimit(tc.hardLimit); got != tc.want {
				t.Fatalf("SoftLimit(%d) = %d, want %d", tc.hardLimit, got, tc.want)
			}
		})
	}
}

// 阈值 3：current 达到 3/5 即软忙，2/5 未达；软忙不等于硬满，闸口只在硬限。
func TestSoftBusyArms(t *testing.T) {
	policy := Policy{DefaultSoftConcurrency: 3}
	if !policy.SoftBusy(3, 5) {
		t.Fatal("current 3/5 at threshold 3 must be soft busy")
	}
	if policy.SoftBusy(2, 5) {
		t.Fatal("current 2/5 at threshold 3 must not be soft busy")
	}
	if !policy.SoftBusy(5, 5) {
		t.Fatal("current 5/5 must be judged by soft contract as busy")
	}
	disabled := Policy{DefaultSoftConcurrency: -1}
	if disabled.SoftBusy(100, 1) {
		t.Fatal("disabled threshold never soft-busy")
	}
}

func TestParseMapTwoKeyContract(t *testing.T) {
	policy, err := ParseMap(map[string]any{"defaultSoftConcurrency": float64(-1), "clientIpConcurrencyLimit": float64(0)}, false, 0)
	if err != nil || policy != (Policy{DefaultSoftConcurrency: -1, ClientIPConcurrencyLimit: 0}) {
		t.Fatalf("默认两键 = %+v err=%v", policy, err)
	}
	policy, err = ParseMap(map[string]any{"defaultSoftConcurrency": float64(1_000_000), "clientIpConcurrencyLimit": float64(1_000_000)}, false, 0)
	if err != nil || policy.DefaultSoftConcurrency != 1_000_000 || policy.ClientIPConcurrencyLimit != 1_000_000 {
		t.Fatalf("上限两键 = %+v err=%v", policy, err)
	}
	errors := []struct {
		name   string
		object map[string]any
	}{
		{name: "threshold 0 invalid", object: map[string]any{"defaultSoftConcurrency": float64(0), "clientIpConcurrencyLimit": float64(0)}},
		{name: "threshold over max", object: map[string]any{"defaultSoftConcurrency": float64(1_000_001), "clientIpConcurrencyLimit": float64(0)}},
		{name: "ip limit negative", object: map[string]any{"defaultSoftConcurrency": float64(-1), "clientIpConcurrencyLimit": float64(-1)}},
		{name: "unknown key", object: map[string]any{"defaultSoftConcurrency": float64(-1), "clientIpConcurrencyLimit": float64(0), "mode": "queue"}},
		{name: "missing key", object: map[string]any{"defaultSoftConcurrency": float64(-1)}},
		{name: "non integer", object: map[string]any{"defaultSoftConcurrency": 1.5, "clientIpConcurrencyLimit": float64(0)}},
	}
	for _, tc := range errors {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseMap(tc.object, false, 0); err == nil {
				t.Fatal("非法两键必须报错")
			}
		})
	}
}

// 旧行读归一：16 键旧策略只取两键；旧默认 globalMax 等价硬上限不迁移成阈值；
// 未知旧键容忍。
func TestParseStoredLegacyRowMapping(t *testing.T) {
	policy, err := ParseStored(`{"defaultSoftConcurrency":500,"clientIpConcurrencyLimit":7,"mode":"balanced_fast","maxQueueSize":8}`, true, true, 1234)
	if err != nil {
		t.Fatalf("legacy parse: %v", err)
	}
	if policy.DefaultSoftConcurrency != 500 || policy.ClientIPConcurrencyLimit != 7 {
		t.Fatalf("legacy policy = %+v", policy)
	}
	// 旧默认 ≥ globalMax：等价硬上限，不迁移成阈值（回落 -1）。
	policy, err = ParseStored(`{"defaultSoftConcurrency":1234}`, true, true, 1234)
	if err != nil || policy.DefaultSoftConcurrency != -1 {
		t.Fatalf("legacy globalMax 默认 = %+v err=%v", policy, err)
	}
	// globalMax 非法时回落内置默认 5000。
	policy, err = ParseStored(`{"defaultSoftConcurrency":4999}`, true, true, 0)
	if err != nil || policy.DefaultSoftConcurrency != 4999 {
		t.Fatalf("legacy globalMax 回落 = %+v err=%v", policy, err)
	}
	// legacy 行策略缺失必须报错（不静默回落默认）。
	if _, err := ParseStored("", false, true, 5000); err == nil {
		t.Fatal("legacy 行缺策略必须报错")
	}
	// 非 legacy 空/NULL → 默认两键。
	policy, err = ParseStored("", false, false, 5000)
	if err != nil || policy != DefaultPolicy() {
		t.Fatalf("NULL 策略 = %+v err=%v", policy, err)
	}
}

func TestNormalizeGroupTypeArms(t *testing.T) {
	if _, _, err := NormalizeGroupType(""); err == nil {
		t.Fatal("空类型必须报错（不得静默归 personal）")
	}
	if _, _, err := NormalizeGroupType("  "); err == nil {
		t.Fatal("空白类型必须报错（不得静默归 personal）")
	}
	normalized, legacy, err := NormalizeGroupType(GroupTypePersonal)
	if err != nil || normalized != GroupTypePersonal || legacy {
		t.Fatalf("personal = %q/%v %v", normalized, legacy, err)
	}
	normalized, legacy, err = NormalizeGroupType(LegacyGroupTypeHighConcurrency)
	if err != nil || normalized != GroupTypePersonal || !legacy {
		t.Fatalf("high_concurrency 读归一 = %q/%v %v", normalized, legacy, err)
	}
	if _, _, err := NormalizeGroupType("super"); err == nil {
		t.Fatal("未知类型必须报错")
	}
}
