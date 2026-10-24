package modelcheckruntime

import (
	"errors"
	"os"
	"strings"
)

// RuntimeConfig 只保留 Enabled：Solution A 让 Gateway 成为 J3b 唯一运行时
// owner，jobs 对任何 enabled 配置无条件 fail-closed，因此其余历史配置字段
// （连接串、路径、密钥、探针版本等）已无消费者，不再声明。
type RuntimeConfig struct {
	Enabled bool
}

func LoadConfig(getenv func(string) string) (RuntimeConfig, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	cfg := RuntimeConfig{Enabled: strings.EqualFold(strings.TrimSpace(getenv("JUHE_AI_MODEL_CHECK_ENABLED")), "true")}
	if !cfg.Enabled {
		return cfg, nil
	}
	// Solution A makes Gateway the sole J3b runtime owner. Keep the jobs
	// binary unconditionally fail-closed for every enabled configuration so a
	// stale PostgreSQL/SQLite environment cannot resurrect a second owner.
	return RuntimeConfig{}, errors.New("J3b 不允许在 juhe-ai-jobs 中启用：方案 A 的唯一运行时 owner 是 juhe-ai-gateway")
}
