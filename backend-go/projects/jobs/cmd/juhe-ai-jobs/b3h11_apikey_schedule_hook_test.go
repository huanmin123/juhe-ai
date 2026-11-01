package main

// B3-H11 回归守卫：api-key-availability-schedule-status-sync 的
// SyncApiKeyScheduleStatuses 调用必须注入激活 hook，禁止以 nil 调用。nil hook
// 只翻状态、不重建小时额度窗口绑定，计划启用的 Key 会永久缺失
// request_quota_hourly_window_scope_bindings，statsagg 因此不生成
// usage_quota_hourly_windows，小时额度静默 fail-open。
//
// 与 p0_projection_sync_hook_test.go 同风格：遍历 cmd/juhe-ai-jobs 非测试源码
// 池，断言宿组根标识符存在且不存在 nil hook 调用点。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAPIKeyScheduleSyncNeverNilActivationHook(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	foundHook := false
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(source)
		if strings.Contains(text, "newAPIKeyScheduleActivationHook(") {
			foundHook = true
		}
		for index, line := range strings.Split(text, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if strings.Contains(line, "SyncApiKeyScheduleStatuses(") && strings.Contains(line, "nil)") {
				t.Fatalf("%s:%d 禁止以 nil hook 调用 SyncApiKeyScheduleStatuses（激活翻转将不重建小时额度窗口绑定）: %s", name, index+1, trimmed)
			}
		}
	}
	if !foundHook {
		t.Fatal("组合根必须构造 newAPIKeyScheduleActivationHook 并注入 SyncApiKeyScheduleStatuses")
	}
}
