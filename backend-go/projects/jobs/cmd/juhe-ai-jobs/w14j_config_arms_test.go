// 波次 w14j：worker 配置范围校验臂与 circuit 族 disabled 分支批测。
package main

import (
	"testing"
)

// TestW14JLoadWorkerConfigRangeArms 用非法 env 值打配置范围校验臂。
func TestW14JLoadWorkerConfigRangeArms(t *testing.T) {
	base := func() map[string]string {
		return map[string]string{
			"JUHE_AI_INSTANCE_ID":                 "w14j-cfg",
			"JUHE_AI_WORKER_ROLE":                 "stats-worker",
			"JUHE_AI_SECRET":                      "0123456789abcdef0123456789abcdef",
		}
	}
	scenarios := []struct {
		name  string
		patch map[string]string
	}{
		{"chat-retention-zero", map[string]string{"JUHE_AI_CHAT_RETENTION_DAYS": "0"}},
		{"chat-retention-over", map[string]string{"JUHE_AI_CHAT_RETENTION_DAYS": "366"}},
		// record-batch / list-projection 批量族错误臂已删除
		// （2026-09-30 C4 收编为常量，env 不再被读取）。
		{"probe-concurrency-zero", map[string]string{"JUHE_AI_JOBS_PROBE_CONCURRENCY": "0"}},
		{"probe-concurrency-over", map[string]string{"JUHE_AI_JOBS_PROBE_CONCURRENCY": "5097"}},
		{"list-projection-interval-low", map[string]string{"JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_INTERVAL_MS": "999"}},
		{"list-projection-interval-high", map[string]string{"JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_INTERVAL_MS": "60001"}},
		{"usage-shard-count-bad", map[string]string{"JUHE_AI_USAGE_SHARD_COUNT": "not-a-number"}},
		{"codex-shard-count-bad", map[string]string{"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT": "not-a-number"}},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			env := base()
			for key, value := range scenario.patch {
				env[key] = value
			}
			if _, err := loadWorkerConfig(getenvFrom(env)); err == nil {
				t.Fatalf("非法配置 %s 必须报错", scenario.name)
			}
		})
	}
}

