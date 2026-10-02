package settings

import (
	"testing"
)

// normalizeUpstreamClientVersionOverrides（保存校验）：合法对象原样通过，
// 未知家族键 / 坏 semver / 非对象一律拒绝（管理端 4xx）。
func TestNormalizeUpstreamClientVersionOverrides(t *testing.T) {
	cases := []struct {
		name    string
		value   any
		wantErr bool
	}{
		{"空对象合法", map[string]any{}, false},
		{"五家族全合法", map[string]any{"codex": "0.160.0", "claudeCode": "2.1.300", "geminiCLI": "0.62.0", "zcode": "3.15.0", "grokCLI": "1.0.14"}, false},
		{"单家族合法", map[string]any{"grokCLI": "1.0.14"}, false},
		{"未知家族键拒绝", map[string]any{"other": "1.0.0"}, true},
		{"坏 semver 拒绝", map[string]any{"codex": "0.160"}, true},
		{"三段以上拒绝", map[string]any{"codex": "1.2.3.4"}, true},
		{"非字符串值拒绝", map[string]any{"codex": 1}, true},
		{"非对象拒绝", "1.0.0", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			normalized, err := normalizeUpstreamClientVersionOverrides("upstreamClientVersionOverrides", tc.value)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("value=%v must be rejected", tc.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("value=%v must pass, err=%v", tc.value, err)
			}
			_ = normalized
		})
	}
}

// filterUpstreamClientVersionOverrides（消费侧防御）：只保留合法项，非法
// 数据静默忽略（与 jobssettings 的过滤语义一致）。
func TestFilterUpstreamClientVersionOverrides(t *testing.T) {
	value := map[string]any{
		"codex":     "0.160.0",
		"claudeCode": "2.1.300",
		"other":     "1.0.0",
		"zcode":     3,
		"grokCLI":   "bad",
	}
	got := filterUpstreamClientVersionOverrides(value)
	want := map[string]string{"codex": "0.160.0", "claudeCode": "2.1.300"}
	if len(got) != len(want) {
		t.Fatalf("got=%v want=%v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("got=%v want=%v", got, want)
		}
	}
}
