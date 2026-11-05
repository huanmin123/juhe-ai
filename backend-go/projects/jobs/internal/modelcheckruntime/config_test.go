package modelcheckruntime

import "testing"

func TestLoadConfigAlwaysDisabled(t *testing.T) {
	// J3b 的唯一运行时 owner 是 gateway：jobs 侧恒不启用，旧
	// JUHE_AI_MODEL_CHECK_ENABLED 开关已删除，设置它不再改变结果。
	for _, values := range []map[string]string{
		nil,
		{"JUHE_AI_MODEL_CHECK_ENABLED": "true"},
		{"JUHE_AI_MODEL_CHECK_ENABLED": "true", "JUHE_AI_MODEL_CHECK_POSTGRES_URL": "postgres://jobs"},
	} {
		cfg, err := LoadConfig(func(key string) string { return values[key] })
		if err != nil || cfg.Enabled {
			t.Fatalf("values=%v cfg=%+v err=%v", values, cfg, err)
		}
	}
}
