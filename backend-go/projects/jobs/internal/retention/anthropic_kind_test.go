package retention

// anthropic_claude 快照 job 的解码/校验回归（AI账户Grok用量快照设计 §8.2）：
// 两种采集 kind 都必须通过 isRecordMaintenanceJob 形状校验；未知 kind 仍被
// 拒绝，防止脏行进队头。

import "testing"

func TestDecodeRecordMaintenanceJobAnthropicKind(t *testing.T) {
	valid := map[string]any{
		"type": JobTypeAccountUsageSnapshotUpsert, "accountId": "a", "kind": AccountUsageSnapshotKindAnthropicClaude,
		"snapshot": map[string]any{
			"claude_usage_updated_at": "2026-09-27T00:00:00.000Z",
			"claude_5h_used_percent":  14,
			"claude_unified_status":   "allowed",
		},
		"updatedAt": "2026-09-27T00:00:00Z",
	}
	job, err := DecodeRecordMaintenanceJob(mustJobJSON(t, valid))
	if err != nil {
		t.Fatalf("anthropic_claude job must decode: %v", err)
	}
	if job.Kind != AccountUsageSnapshotKindAnthropicClaude {
		t.Fatalf("kind = %q", job.Kind)
	}

	invalid := valid
	invalid["kind"] = "other_kind"
	if _, err := DecodeRecordMaintenanceJob(mustJobJSON(t, invalid)); err == nil ||
		err.Error() != "Redis Stream 数据维护消息格式无效" {
		t.Fatalf("unknown snapshot kind must be rejected: %v", err)
	}
}
