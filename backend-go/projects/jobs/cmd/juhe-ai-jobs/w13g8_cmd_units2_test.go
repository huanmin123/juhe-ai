// 波次 w13g8 第二批 cmd 单元批测：
//   - loadWorkerConfig 剩余范围校验与 env 解析臂（worker_config.go）；
//   - retention 组合根端口适配器的错误分支、codex 结算、checkpoint、
//     flushLoop 失败重试（worker_retention.go；SQLite 进程内）；
//   - balanceDetectRuntime 提交/启用/快照替换的围栏与错误分支
//     （worker_balance_detect.go）。
//
// 不可达语句归因（见文末登记）：normalizeBalanceConfigJSON 对
// balanceConfigJSON struct 的 json.Marshal 不会失败，其错误返回分支与
// EnableDetectedQuery 的 normalize 错误分支为防御性不可达；retention
// location() 的 LoadLocation 失败分支同理（timezoneName 已预校验）。
package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestW13G8LoadWorkerConfigRemainingArms 覆盖 loadWorkerConfig 尚未覆盖的
// 范围校验与解析失败臂。
func TestW13G8LoadWorkerConfigRemainingArms(t *testing.T) {
	base := map[string]string{
		"JUHE_AI_DATABASE_PATH": filepath.Join(t.TempDir(), "business.sqlite3"),
	}
	for _, test := range []struct {
		name     string
		patch    map[string]string
		fragment string
	}{
		{"chat 保留天数非法", map[string]string{"JUHE_AI_CHAT_RETENTION_DAYS": "0"}, "JUHE_AI_CHAT_RETENTION_DAYS"},
		{"chat 保留天数越界", map[string]string{"JUHE_AI_CHAT_RETENTION_DAYS": "366"}, "JUHE_AI_CHAT_RETENTION_DAYS"},
		{"chat 保留天数非整数", map[string]string{"JUHE_AI_CHAT_RETENTION_DAYS": "w13g8"}, "JUHE_AI_CHAT_RETENTION_DAYS"},
		// 维护批量 / 队列 / 关停冲刷批与投影批量族错误臂已删除
		// （2026-09-30 C4 收编为常量，env 不再被读取）。
		{"探针并发越界", map[string]string{"JUHE_AI_JOBS_PROBE_CONCURRENCY": "5097"}, "JUHE_AI_JOBS_PROBE_CONCURRENCY"},
		{"投影间隔越界", map[string]string{"JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_INTERVAL_MS": "999"}, "PROJECTION_INTERVAL_MS"},
		// 家族开关与 sqlite 路径门禁的失败臂已删除（2026-09-19 家族开关移除 +
		// DATA_DIR 派生：开关 env 不再被读取，路径类 env 派生后恒非空）。
		// 非 production 空 SECRET 回退开发密钥，仅 production 仍 fail closed。
		{"production probe 缺 secret", map[string]string{"JUHE_AI_SECRET": "", "NODE_ENV": "production"}, "JUHE_AI_SECRET"},
	} {
		t.Run(test.name, func(t *testing.T) {
			getenv := func(key string) string {
				if value, ok := test.patch[key]; ok {
					return value
				}
				if value, ok := base[key]; ok {
					return value
				}
				return ""
			}
			config, err := loadWorkerConfig(getenv)
			if err == nil {
				t.Fatalf("必须报配置错误，得到合法配置 %+v", config)
			}
			if !strings.Contains(err.Error(), test.fragment) {
				t.Fatalf("错误必须包含 %q: %v", test.fragment, err)
			}
		})
	}
}
