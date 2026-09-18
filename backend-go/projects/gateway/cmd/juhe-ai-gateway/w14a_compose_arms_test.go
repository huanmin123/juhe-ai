package main

// w14a（单元层）：composeSystemAPI SQLite 模式的路径契约 fail-fast 臂。
// 复用 compose_test.go 的 F3/F4 装配夹具；组合根错误路径不关闭 business
// 句柄（Windows 句柄锁会让 t.TempDir 清理失败），因此根目录用手工临时目录
// + 尽力而为清理。
//
// 登记的进程内不可达分支（compose.go，被 X05 六库 preflight 前置拦截或为
// 组合根恒真守卫）：
//   - 283-285/320-326/344-355（business/stats/usage-catalog open+configure
//     与缺失臂）：preflight（storage_bootstrap.go）先缺失校验并创建文件，
//     到达 compose 同名分支时路径恒非空且可打开（w1 boot 测试同款登记）。
//   - 405-407/440-442/458-464/592-599（dataset stat/open/cleanup 臂）：
//     preflight 的 ensureFile("dataset") 保证文件存在。
//   - 其余 `create X store: %w` 构造器防御臂：组合根恒传合法 db/配置
//     （w1w 已登记同款清单）。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
)

// w14aComposeTestConfig 等价 composeTestConfig，但根目录由本测试手工管理
// （组合根 fail-fast 错误臂不关 business 句柄，Windows 下 t.TempDir 的
// RemoveAll 会失败并污染测试结果；此处清理失败仅忽略）。
func w14aComposeTestConfig(t *testing.T) runtimeConfig {
	t.Helper()
	root, err := os.MkdirTemp("", "w14a-compose-")
	if err != nil {
		t.Fatalf("temp root = %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.MkdirAll(filepath.Join(root, "codex-context"), 0o755); err != nil {
		t.Fatalf("create codex context shard root: %v", err)
	}
	cfg := composeTestConfig(t)
	// 以手工根目录重建全部路径（composeTestConfig 的路径指向它自己的
	// t.TempDir，那里的句柄同样会泄漏）。
	cfg.BusinessDatabasePath = filepath.Join(root, "business.sqlite3")
	cfg.StatsDatabasePath = filepath.Join(root, "stats.sqlite3")
	cfg.ChatDatabasePath = filepath.Join(root, "chat.sqlite3")
	cfg.DatasetDatabasePath = filepath.Join(root, "dataset.sqlite3")
	cfg.RuntimeLogDatabasePath = filepath.Join(root, "runtime-log.sqlite3")
	cfg.TableMonitorDatabasePath = filepath.Join(root, "table-monitor.sqlite3")
	cfg.UsageCatalogDatabasePath = filepath.Join(root, "usage-catalog.sqlite3")
	cfg.CodexContextShardRoot = filepath.Join(root, "codex-context")
	cfg.BusinessCutoverEvidencePath = filepath.Join(root, "evidence.json")
	return cfg
}

// TestW14aComposeSystemAPIMissingPathArms 收割 preflight 不校验的路径缺失臂
// （table-monitor / runtime-log；stats 与 usage-catalog / dataset 缺失被
// preflight 先行拦截，见文件头清单）。
func TestW14aComposeSystemAPIMissingPathArms(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(cfg *runtimeConfig)
		wantErr string
	}{
		{
			name:    "table-monitor-path-missing",
			mutate:  func(cfg *runtimeConfig) { cfg.TableMonitorDatabasePath = "" },
			wantErr: "sqlite 模式缺少 JUHE_AI_TABLE_MONITOR_DATABASE_PATH",
		},
		{
			name:    "runtime-log-path-missing",
			mutate:  func(cfg *runtimeConfig) { cfg.RuntimeLogDatabasePath = "" },
			wantErr: "sqlite 模式缺少 JUHE_AI_RUNTIME_LOG_DATABASE_PATH",
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			cfg := w14aComposeTestConfig(t)
			item.mutate(&cfg)
			store := openComposeOperationStore(t)
			auditConfig, auditProducer, closeAudit := openComposeAuditSources(t, filepath.Dir(cfg.DatasetDatabasePath))
			defer closeAudit()
			_, err := composeSystemAPI(cfg, pgpool.NewRegistry(), store, openComposeOperationLease(t, store), auditProducer, auditConfig)
			if err == nil || !strings.Contains(err.Error(), item.wantErr) {
				t.Fatalf("期望错误含 %q, got %v", item.wantErr, err)
			}
		})
	}
}

// TestW14aComposeSystemAPITableMonitorConfigureArm 收割六库物理身份门禁的
// 「不是常规文件」臂（storage_bootstrap_physical.go）：路径指向目录时
// preflight 的 assertDistinctSQLiteStoragePaths 先行拒绝（compose 的
// table-monitor configure 错误臂被该门禁遮蔽，进程内不可达）。
func TestW14aComposeSystemAPITableMonitorConfigureArm(t *testing.T) {
	cfg := w14aComposeTestConfig(t)
	cfg.TableMonitorDatabasePath = filepath.Dir(cfg.TableMonitorDatabasePath)
	store := openComposeOperationStore(t)
	auditConfig, auditProducer, closeAudit := openComposeAuditSources(t, filepath.Dir(cfg.DatasetDatabasePath))
	defer closeAudit()
	_, err := composeSystemAPI(cfg, pgpool.NewRegistry(), store, openComposeOperationLease(t, store), auditProducer, auditConfig)
	if err == nil || !strings.Contains(err.Error(), "的 SQLite 路径不是常规文件") {
		t.Fatalf("期望物理身份门禁「不是常规文件」错误, got %v", err)
	}
}

// TestW14aRedisStateClientProviderInvalidate 覆盖空实现端口（显式 no-op 契约）。
func TestW14aRedisStateClientProviderInvalidate(t *testing.T) {
	redisStateClientProvider{}.Invalidate(context.Background(), nil)
}
