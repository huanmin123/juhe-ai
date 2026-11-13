package recordmaintenance

// T207 C1：快照行 kind 白名单是 gateway 投递端与 jobs 消费端的共享事实源，
// 锁定集合内容防止两端漂移。

import "testing"

func TestIsValidAccountUsageSnapshotKind(t *testing.T) {
	for _, kind := range []string{"openai_codex", "anthropic_claude"} {
		if !IsValidAccountUsageSnapshotKind(kind) {
			t.Fatalf("白名单应接受 %q", kind)
		}
	}
	for _, kind := range []string{"", "openai", "codex", "anthropic", "openai_codex_compat", "unknown"} {
		if IsValidAccountUsageSnapshotKind(kind) {
			t.Fatalf("白名单应拒绝 %q", kind)
		}
	}
}
