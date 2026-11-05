package modelcheckruntime

// RuntimeConfig 只保留 Enabled：Solution A 让 Gateway 成为 J3b 唯一运行时
// owner，jobs 对任何 enabled 配置无条件 fail-closed，因此其余历史配置字段
// （连接串、路径、密钥、探针版本等）已无消费者，不再声明。
type RuntimeConfig struct {
	Enabled bool
}

// LoadConfig 恒返回未启用：J3b 的唯一运行时 owner 是 juhe-ai-gateway。
// 原 JUHE_AI_MODEL_CHECK_ENABLED 开关已删除——jobs 进程不可能再被配置成
// 第二个 owner，因此不需要 fail-closed 分支。
func LoadConfig(getenv func(string) string) (RuntimeConfig, error) {
	return RuntimeConfig{Enabled: false}, nil
}
