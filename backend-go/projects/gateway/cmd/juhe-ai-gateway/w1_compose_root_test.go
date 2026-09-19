package main

// w1_compose_root_test.go —— compose.go 组合根可驱动臂 + chain_runtime.go
// composeChainRuntimeServices 的单元测试（w1o 前缀，TestW1O 入口）。
//
// 与既有覆盖的边界（不重复）：
//   - composeSystemAPI 成功路径的 HTTP 契约由 compose_test.go /
//     compose_assembly_test.go / chain_test.go 覆盖；这里只补 nil 依赖失败臂、
//     SQLite 命名路径失败臂与三条不同分支组合的成功变体。
//   - settingsString / settingsValueReader 由 w1_compose_pure_test.go 覆盖；
//     settingsTimezone / businessDialect / configureSQLiteConnection /
//     trustProxyCount / settingsTimezoneProvider 由 w1_misc_pure_test.go 与
//     w1_accounts_reset_test.go 覆盖；这里不再重复。
//   - composeChainRuntimeServices 的 redis-cache 成功路径由 chain_phase3_test.go
//     覆盖；这里补守卫臂、坏 redis URL 失败臂、memory 全字段成功路径与
//     runtime-state 驱动分叉（standalone fail-fast / performance 成功）。
//
// 全部用例确定性可重放：redis 一律 miniredis；数据库一律临时 SQLite 文件；
// 无并发等待、无网络依赖；composeChainRuntimeServices 返回值携带 Close，
// 成功路径统一经 t.Cleanup(services.Close) 回收后台协调器与客户端。

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/auditlog"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/operationlog"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/ratelimit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/settings"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
)

// ---------------------------------------------------------------------------
// 共享 w1o fixture：composeSystemAPI 的完整依赖栈
// ---------------------------------------------------------------------------

// w1oComposeStack 复用 compose_test.go 的既有 fixture 助手（open* 系列），
// 聚合成一次 composeSystemAPI 调用所需的全部实参；用例再按需改写 cfg /
// auditConfig 的单个字段以命中特定失败臂。
type w1oComposeStack struct {
	cfg           runtimeConfig
	store         operationlog.Store
	lease         *operationlog.LeaseKeeper
	auditConfig   auditlog.Config
	auditProducer *auditlog.Producer
}

func w1oNewComposeStack(t *testing.T) *w1oComposeStack {
	t.Helper()
	cfg := composeTestConfig(t)
	store := openComposeOperationStore(t)
	createRuntimeLogDataset(t, cfg.RuntimeLogDatabasePath)
	auditConfig, auditProducer, closeAudit := openComposeAuditSources(t, filepath.Dir(cfg.DatasetDatabasePath))
	t.Cleanup(closeAudit)
	lease := openComposeOperationLease(t, store)
	return &w1oComposeStack{
		cfg:           cfg,
		store:         store,
		lease:         lease,
		auditConfig:   auditConfig,
		auditProducer: auditProducer,
	}
}

func (s *w1oComposeStack) compose(t *testing.T) (*composition, error) {
	t.Helper()
	return composeSystemAPI(s.cfg, pgpool.NewRegistry(), s.store, s.lease, s.auditProducer, s.auditConfig, composeTestOwnerHealth())
}

// ---------------------------------------------------------------------------
// A1. composeSystemAPI：nil 依赖失败臂（无需任何 fixture）
// ---------------------------------------------------------------------------

func TestW1OComposeSystemAPINilDependencyArms(t *testing.T) {
	stack := w1oNewComposeStack(t)
	// lease / producer 在本组用例里被刻意置 nil，先取值避免 vet 误报。
	lease, producer := stack.lease, stack.auditProducer
	cases := []struct {
		name     string
		store    operationlog.Store
		lease    *operationlog.LeaseKeeper
		producer *auditlog.Producer
		want     string
	}{
		{"缺F4操作日志store", nil, lease, producer, "F4 操作日志 store"},
		{"缺F4共享租约", stack.store, nil, producer, "F4 共享租约"},
		{"缺F3审计producer", stack.store, lease, nil, "F3 审计进程内 producer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			composed, err := composeSystemAPI(stack.cfg, pgpool.NewRegistry(), tc.store, tc.lease, tc.producer, stack.auditConfig, composeTestOwnerHealth())
			if err == nil {
				if composed != nil {
					composed.Shutdown()
				}
				t.Fatalf("nil 依赖必须 fail-fast，实际 composed=%v err=nil", composed != nil)
			}
			if composed != nil {
				t.Fatalf("失败臂不得返回组合根，实际 %#v", composed)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误 = %v，want 包含 %q", err, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// A2. composeSystemAPI：SQLite 命名路径失败臂
// ---------------------------------------------------------------------------

// w1oRedirectDatabasePaths 把六个 SQLite 库路径与 codex/chat 根重定向到独立
// 手管目录：组合根在这些 fail-fast 失败路径上不回收已打开的 SQLite 句柄
// （Windows 上句柄锁定文件会导致 t.TempDir 清理失败），因此改走尽力清理
// （RemoveAll 错误不判失败），锁残留文件由进程退出后操作系统回收。
func w1oRedirectDatabasePaths(t *testing.T, stack *w1oComposeStack) {
	t.Helper()
	root := filepath.Join(os.TempDir(), "w1o-compose-failure-"+newCompositionID("id"))
	if err := os.MkdirAll(filepath.Join(root, "codex-context"), 0o755); err != nil {
		t.Fatalf("create manual compose root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	stack.cfg.BusinessDatabasePath = filepath.Join(root, "business.sqlite3")
	stack.cfg.StatsDatabasePath = filepath.Join(root, "stats.sqlite3")
	stack.cfg.ChatDatabasePath = filepath.Join(root, "chat.sqlite3")
	stack.cfg.DatasetDatabasePath = filepath.Join(root, "dataset.sqlite3")
	stack.cfg.RuntimeLogDatabasePath = filepath.Join(root, "runtime-log.sqlite3")
	stack.cfg.TableMonitorDatabasePath = filepath.Join(root, "table-monitor.sqlite3")
	stack.cfg.UsageCatalogDatabasePath = filepath.Join(root, "usage-catalog.sqlite3")
	stack.cfg.CodexContextShardRoot = filepath.Join(root, "codex-context")
	stack.cfg.ChatAssetsRoot = filepath.Join(root, "chat-assets")
}

func TestW1OComposeSystemAPISQLitePathFailureArms(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, stack *w1oComposeStack)
		want   string
	}{
		{"缺表监控库路径", func(_ *testing.T, stack *w1oComposeStack) {
			stack.cfg.TableMonitorDatabasePath = ""
		}, "JUHE_AI_TABLE_MONITOR_DATABASE_PATH"},
		{"缺审计库路径", func(_ *testing.T, stack *w1oComposeStack) {
			stack.auditConfig.AuditDatabasePath = ""
		}, "JUHE_AI_AUDIT_LOG_DATABASE_PATH"},
		{"审计数据集文件缺失", func(t *testing.T, stack *w1oComposeStack) {
			stack.auditConfig.AuditDatabasePath = filepath.Join(t.TempDir(), "w1o-missing-audit.sqlite3")
		}, "audit 数据集文件不存在"},
		{"缺运行日志库路径", func(_ *testing.T, stack *w1oComposeStack) {
			stack.cfg.RuntimeLogDatabasePath = ""
		}, "JUHE_AI_RUNTIME_LOG_DATABASE_PATH"},
		{"业务库路径不可用", func(_ *testing.T, stack *w1oComposeStack) {
			stack.cfg.BusinessDatabasePath = filepath.Join(filepath.Dir(stack.cfg.BusinessDatabasePath), "w1o-no-such-dir", "business.sqlite3")
		}, "business sqlite database"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stack := w1oNewComposeStack(t)
			w1oRedirectDatabasePaths(t, stack)
			tc.mutate(t, stack)
			composed, err := stack.compose(t)
			if err == nil {
				if composed != nil {
					composed.Shutdown()
				}
				t.Fatalf("坏路径配置必须 fail-fast，实际 composed=%v err=nil", composed != nil)
			}
			if composed != nil {
				t.Fatalf("失败臂不得返回组合根，实际 %#v", composed)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误 = %v，want 包含 %q", err, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// A3. composeSystemAPI：成功变体（不同驱动轴 / 开关组合）
// ---------------------------------------------------------------------------

func TestW1OComposeSystemAPISQLiteSuccessVariants(t *testing.T) {
	t.Run("链条关闭", func(t *testing.T) {
		stack := w1oNewComposeStack(t)
		composed, err := stack.compose(t)
		if err != nil {
			t.Fatalf("链条关闭变体必须组装成功: %v", err)
		}
		if composed == nil || composed.Kernel == nil || composed.Bus == nil {
			t.Fatalf("组合根关键字段缺失: Kernel=%v Bus=%v", composed.Kernel != nil, composed.Bus != nil)
		}
		if composed.chain != nil || composed.chainServices != nil {
			t.Fatalf("链条关闭时 chain/chainServices 必须为 nil，实际 %v/%v", composed.chain != nil, composed.chainServices != nil)
		}
		if !composed.ownDB || !composed.ownStatsDB || composed.db == nil || composed.statsDB == nil {
			t.Fatalf("SQLite 变体必须自有业务/统计句柄: ownDB=%v ownStatsDB=%v", composed.ownDB, composed.ownStatsDB)
		}
		if composed.settingsStore == nil || composed.AuthzStore == nil || composed.producer == nil || composed.auditProducer == nil {
			t.Fatalf("settings/authz/F4/F3 组件必须装配完成")
		}
		composed.Shutdown()
	})

	t.Run("redis运行态驱动加验证码关闭", func(t *testing.T) {
		redisServer := miniredis.RunT(t)
		stack := w1oNewComposeStack(t)
		stack.cfg.RuntimeStateDriver = "redis"
		stack.cfg.RedisStateURL = "redis://" + redisServer.Addr()
		stack.cfg.RedisNamespace = "w1o-compose-ns"
		stack.cfg.CaptchaDisabled = true
		composed, err := stack.compose(t)
		if err != nil {
			t.Fatalf("redis 登录守卫 + 验证码关闭变体必须组装成功: %v", err)
		}
		if composed.chain != nil {
			t.Fatalf("链条未开启时 chain 必须为 nil")
		}
		composed.Shutdown()
	})

	t.Run("锁开关经env关闭加链条开启", func(t *testing.T) {
		t.Setenv("JUHE_AI_ACCOUNT_LOCKS_DISABLED", "true")
		stack := w1oNewComposeStack(t)
		stack.cfg.ChainEnabled = true
		composed, err := stack.compose(t)
		if err != nil {
			t.Fatalf("锁开关关闭 + 链条开启变体必须组装成功: %v", err)
		}
		if composed.chain == nil || composed.chainServices == nil || composed.chainServices.Identity == nil {
			t.Fatalf("链条开启时 chain/chainServices/Identity 必须装配")
		}
		composed.Shutdown()
	})
}

// ---------------------------------------------------------------------------
// B4. compose.go：authsysBusInvalidator
// ---------------------------------------------------------------------------

func TestW1OAuthsysBusInvalidatorArms(t *testing.T) {
	t.Run("nil总线", func(t *testing.T) {
		var nilInvalidator authsysBusInvalidator
		nilInvalidator.InvalidateRuntime("w1o-原因") // 不 panic 即为 no-op 契约。
		if err := nilInvalidator.InvalidateAPIKeyValidation("w1o-原因"); err == nil {
			t.Fatal("nil 总线 InvalidateAPIKeyValidation 必须报错")
		} else if !strings.Contains(err.Error(), "not wired") {
			t.Fatalf("nil 总线错误 = %v，want 包含 not wired", err)
		}
	})

	t.Run("真实总线发布", func(t *testing.T) {
		bus := inval.New(time.Now)
		var runtimeReasons, validationReasons []string
		bus.Subscribe(inval.TopicGatewayRuntime, func(_, reason string) { runtimeReasons = append(runtimeReasons, reason) })
		bus.Subscribe(inval.TopicGatewayAPIKeyValidation, func(_, reason string) { validationReasons = append(validationReasons, reason) })
		invalidator := authsysBusInvalidator{bus: bus}

		invalidator.InvalidateRuntime("账户写入后失效")
		if err := invalidator.InvalidateAPIKeyValidation("密钥校验失效"); err != nil {
			t.Fatalf("真实总线 InvalidateAPIKeyValidation = %v，want nil", err)
		}
		if len(runtimeReasons) != 1 || runtimeReasons[0] != "账户写入后失效" {
			t.Fatalf("runtime 主题发布 = %v，want [账户写入后失效]", runtimeReasons)
		}
		if len(validationReasons) != 1 || validationReasons[0] != "密钥校验失效" {
			t.Fatalf("api-key validation 主题发布 = %v，want [密钥校验失效]", validationReasons)
		}
	})
}

// ---------------------------------------------------------------------------
// B5. compose.go：accountsBusInvalidator
// ---------------------------------------------------------------------------

func TestW1OAccountsBusInvalidatorArms(t *testing.T) {
	t.Run("nil总线错误臂", func(t *testing.T) {
		var nilInvalidator accountsBusInvalidator
		if err := nilInvalidator.InvalidateGatewayRuntime("r"); err == nil {
			t.Error("InvalidateGatewayRuntime nil 总线必须报错")
		}
		if err := nilInvalidator.InvalidateRuntime("r"); err == nil {
			t.Error("InvalidateRuntime nil 总线必须报错")
		}
		if err := nilInvalidator.InvalidateAPIKeyValidation("r"); err == nil {
			t.Error("InvalidateAPIKeyValidation nil 总线必须报错")
		}
		if err := nilInvalidator.InvalidateGroupAccountIds(); err == nil {
			t.Error("InvalidateGroupAccountIds nil 总线必须报错")
		}
		if err := nilInvalidator.InvalidateAuthorizationQuota("r"); err == nil {
			t.Error("InvalidateAuthorizationQuota nil 总线必须报错")
		}
		// 文档化 no-op 通道：永远 nil error。
		if err := nilInvalidator.InvalidateAccountLookup("acc-1"); err != nil {
			t.Errorf("InvalidateAccountLookup 必须恒为 nil error，实际 %v", err)
		}
		if err := nilInvalidator.ClearResourceAuthorizationLookupCaches(); err != nil {
			t.Errorf("ClearResourceAuthorizationLookupCaches 必须恒为 nil error，实际 %v", err)
		}
	})

	t.Run("真实总线发布与固定原因", func(t *testing.T) {
		bus := inval.New(time.Now)
		var runtimeReasons, quotaReasons, validationReasons []string
		bus.Subscribe(inval.TopicGatewayRuntime, func(_, reason string) { runtimeReasons = append(runtimeReasons, reason) })
		bus.Subscribe(inval.TopicAuthorizationQuota, func(_, reason string) { quotaReasons = append(quotaReasons, reason) })
		bus.Subscribe(inval.TopicGatewayAPIKeyValidation, func(_, reason string) { validationReasons = append(validationReasons, reason) })
		invalidator := accountsBusInvalidator{bus: bus}

		if err := invalidator.InvalidateGatewayRuntime("账户补丁"); err != nil {
			t.Fatalf("InvalidateGatewayRuntime = %v，want nil", err)
		}
		if err := invalidator.InvalidateRuntime("轮转失效"); err != nil {
			t.Fatalf("InvalidateRuntime = %v，want nil", err)
		}
		if err := invalidator.InvalidateGroupAccountIds(); err != nil {
			t.Fatalf("InvalidateGroupAccountIds = %v，want nil", err)
		}
		if err := invalidator.InvalidateAuthorizationQuota("配额失效"); err != nil {
			t.Fatalf("InvalidateAuthorizationQuota = %v，want nil", err)
		}
		if err := invalidator.InvalidateAPIKeyValidation("密钥失效"); err != nil {
			t.Fatalf("InvalidateAPIKeyValidation = %v，want nil", err)
		}

		wantRuntime := []string{"账户补丁", "轮转失效", "account_deleted_group_account_ids"}
		if len(runtimeReasons) != len(wantRuntime) {
			t.Fatalf("runtime 主题发布次数 = %d（%v），want %d", len(runtimeReasons), runtimeReasons, len(wantRuntime))
		}
		for index, want := range wantRuntime {
			if runtimeReasons[index] != want {
				t.Fatalf("runtime 第 %d 条原因 = %q，want %q", index, runtimeReasons[index], want)
			}
		}
		if len(quotaReasons) != 1 || quotaReasons[0] != "配额失效" {
			t.Fatalf("authorization quota 主题发布 = %v，want [配额失效]", quotaReasons)
		}
		if len(validationReasons) != 1 || validationReasons[0] != "密钥失效" {
			t.Fatalf("api-key validation 主题发布 = %v，want [密钥失效]", validationReasons)
		}
	})
}

// ---------------------------------------------------------------------------
// B6. compose.go：apikeySecretSealer
// ---------------------------------------------------------------------------

func TestW1OApikeySecretSealerRoundTrip(t *testing.T) {
	sealer := apikeySecretSealer{secret: "w1o-seal-secret"}
	envelope, err := sealer.SealSecret(context.Background(), "sk-w1o-plaintext")
	if err != nil {
		t.Fatalf("SealSecret: %v", err)
	}
	if envelope == "" || envelope == "sk-w1o-plaintext" {
		t.Fatalf("封套 = %q，want 非空且异于明文", envelope)
	}
	var sealed struct {
		Key string `json:"key"`
	}
	if err := apikeys.DecryptJSON("w1o-seal-secret", envelope, &sealed); err != nil {
		t.Fatalf("DecryptJSON 同密钥: %v", err)
	}
	if sealed.Key != "sk-w1o-plaintext" {
		t.Fatalf("解封 key = %q，want 原明文", sealed.Key)
	}
	if err := apikeys.DecryptJSON("w1o-wrong-secret", envelope, &sealed); err == nil {
		t.Fatal("错误密钥解封必须报错")
	}
}

// ---------------------------------------------------------------------------
// B7. compose.go：ratelimitSettingsProvider（具体 *settings.Store，无法注入
// fake；经真实仓覆盖合法值、缺行错误透传与表损坏错误透传。provider 内部的
// 缺键 / 非 float64 / 越界防御臂在真实仓 seam 上不可达：Load 的 JSON 反序列化
// 恒产出 float64，normalizeSystemSetting 前置保证整值且在 [0,1000000] 内，
// 缺行由 assertAllSettingsPresent 兜底报错）
// ---------------------------------------------------------------------------

func TestW1ORatelimitSettingsProviderArms(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "w1o-ratelimit-settings.sqlite3"))
	if err != nil {
		t.Fatalf("打开临时库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := bootstrap.EnsureSQLiteSchema(context.Background(), bootstrap.SQLiteSchemaBusiness, db); err != nil {
		t.Fatalf("ensure 业务 schema: %v", err)
	}

	base := time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)
	step := 0
	now := func() time.Time {
		step++
		return base.Add(time.Duration(step) * 2 * time.Minute)
	}
	store, err := settings.NewStore(db, false, now, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	provider := ratelimitSettingsProvider(store)
	ctx := context.Background()

	// 阶段一（种子前）：无任何设置行 → Load 缺字段错误原样透传。
	if _, err := provider(ctx); err == nil {
		t.Fatal("缺设置行时 provider 必须报错")
	} else if !strings.Contains(err.Error(), "缺少字段") {
		t.Fatalf("缺行错误 = %v，want 包含 缺少字段", err)
	}

	// 阶段二：seed 业务库（Node seedDefaults 移植，写入系统设置默认行）→
	// provider 必须能加载完整快照（缺行臂由阶段一锁定，不再重复）。
	if _, err := bootstrap.SeedSQLiteBusiness(context.Background(), db, bootstrap.SeedOptions{Secret: "w1o-provider-secret"}); err != nil {
		t.Fatalf("seed 业务库: %v", err)
	}
	got, err := provider(ctx)
	if err != nil {
		t.Fatalf("种子默认值 provider: %v", err)
	}
	for _, value := range []int{
		got.IPReadPerMinute, got.IPReadBurstPer10s, got.IPWritePerMinute,
		got.IPWriteBurstPer10s, got.UserReadPerMinute, got.UserWritePerMinute,
	} {
		if value < 0 || value > 1000000 {
			t.Fatalf("种子默认设置越界：%+v", got)
		}
	}

	// 阶段三：Update 写入 float64（含 0 与 1000000 边界）→ provider 逐字段透传。
	if _, err := store.Update(ctx, map[string]any{
		"systemApiRateLimitIpReadPerMinute":          float64(0),
		"systemApiRateLimitIpReadBurstPer10Seconds":  float64(1000000),
		"systemApiRateLimitIpWritePerMinute":         float64(5),
		"systemApiRateLimitIpWriteBurstPer10Seconds": float64(6),
		"systemApiRateLimitUserReadPerMinute":        float64(7),
		"systemApiRateLimitUserWritePerMinute":       float64(8),
	}); err != nil {
		t.Fatalf("Update 边界值: %v", err)
	}
	got, err = provider(ctx)
	if err != nil {
		t.Fatalf("边界值 provider: %v", err)
	}
	want := ratelimit.Settings{
		IPReadPerMinute:    0,
		IPReadBurstPer10s:  1000000,
		IPWritePerMinute:   5,
		IPWriteBurstPer10s: 6,
		UserReadPerMinute:  7,
		UserWritePerMinute: 8,
	}
	if got != want {
		t.Fatalf("边界值设置 = %+v，want %+v", got, want)
	}

	// 阶段四：表损坏 → Load 错误透传（时钟步进必然越过 60s 缓存 TTL）。
	if _, err := db.Exec("DROP TABLE system_settings"); err != nil {
		t.Fatalf("DROP system_settings: %v", err)
	}
	if _, err := provider(ctx); err == nil {
		t.Fatal("表损坏时 provider 必须报错")
	}
}

// ---------------------------------------------------------------------------
// B8. compose.go：newCompositionID（格式与唯一性；rand 失败回退纳秒分支无法
// 确定性注入 crypto/rand，跳过）
// ---------------------------------------------------------------------------

func TestW1ONewCompositionIDFormat(t *testing.T) {
	pattern := regexp.MustCompile(`^w1o-id_[0-9a-f]{16}$`)
	seen := make(map[string]bool, 64)
	for index := 0; index < 64; index++ {
		id := newCompositionID("w1o-id")
		if !pattern.MatchString(id) {
			t.Fatalf("ID 形状不符（前缀_16位十六进制）：%q", id)
		}
		if seen[id] {
			t.Fatalf("第 %d 次 ID 重复：%q", index, id)
		}
		seen[id] = true
	}
	if id := newCompositionID(""); !strings.HasPrefix(id, "_") {
		t.Fatalf("空前缀 ID = %q，want 下划线开头的同形状输出", id)
	}
}

// ---------------------------------------------------------------------------
// B9. compose.go：openSQLiteReadOnly（存在文件可读不可写 / 缺失文件连接失败）
// ---------------------------------------------------------------------------

func TestW1OOpenSQLiteReadOnlyArms(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "w1o-source.sqlite3")
	source, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		t.Fatalf("打开源库: %v", err)
	}
	if _, err := source.Exec("CREATE TABLE w1o_probe (value TEXT)"); err != nil {
		t.Fatalf("建表: %v", err)
	}
	if _, err := source.Exec("INSERT INTO w1o_probe (value) VALUES ('行值')"); err != nil {
		t.Fatalf("写入: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("关闭源库: %v", err)
	}

	ro, err := openSQLiteReadOnly(sourcePath)
	if err != nil {
		t.Fatalf("openSQLiteReadOnly 存在文件: %v", err)
	}
	t.Cleanup(func() { _ = ro.Close() })
	var value string
	if err := ro.QueryRow("SELECT value FROM w1o_probe LIMIT 1").Scan(&value); err != nil {
		t.Fatalf("只读查询: %v", err)
	}
	if value != "行值" {
		t.Fatalf("只读查询值 = %q，want 行值", value)
	}
	if _, err := ro.Exec("INSERT INTO w1o_probe (value) VALUES ('写入')"); err == nil {
		t.Fatal("只读句柄写入必须报错")
	}

	missing, err := openSQLiteReadOnly(filepath.Join(root, "w1o-missing.sqlite3"))
	if err != nil {
		t.Fatalf("openSQLiteReadOnly 缺失文件（惰性打开）: %v", err)
	}
	t.Cleanup(func() { _ = missing.Close() })
	if err := missing.Ping(); err == nil {
		t.Fatal("缺失文件只读连接必须报错")
	}
}

// ---------------------------------------------------------------------------
// C10. chain_runtime.go：composeChainRuntimeServices 守卫臂与坏 redis URL
// （错误均发生在任何数据库访问之前，最小 composition 即可驱动）
// ---------------------------------------------------------------------------

func TestW1OComposeChainRuntimeServicesGuardsAndBadRedisURLs(t *testing.T) {
	fakeRead := func(string) (string, error) { return "UTC", nil }
	t.Run("nil组合根", func(t *testing.T) {
		services, err := composeChainRuntimeServices(nil, runtimeConfig{}, fakeRead)
		if err == nil {
			if services != nil {
				services.Close()
			}
			t.Fatal("nil 组合根必须报错")
		}
		if !strings.Contains(err.Error(), "缺少 composition") {
			t.Fatalf("错误 = %v，want 包含 缺少 composition", err)
		}
	})
	t.Run("nil设置读取器", func(t *testing.T) {
		services, err := composeChainRuntimeServices(&composition{}, runtimeConfig{}, nil)
		if err == nil {
			if services != nil {
				services.Close()
			}
			t.Fatal("nil 设置读取器必须报错")
		}
		if !strings.Contains(err.Error(), "缺少全局设置读取器") {
			t.Fatalf("错误 = %v，want 包含 缺少全局设置读取器", err)
		}
	})
	t.Run("坏缓存redis URL", func(t *testing.T) {
		cfg := composeTestConfig(t)
		cfg.CacheDriver = "redis"
		cfg.RedisCacheURL = "://w1o-bad-cache-url"
		services, err := composeChainRuntimeServices(&composition{}, cfg, fakeRead)
		if err == nil {
			if services != nil {
				services.Close()
			}
			t.Fatal("坏缓存 redis URL 必须报错")
		}
		if !strings.Contains(err.Error(), "cache") || !strings.Contains(err.Error(), "redis") {
			t.Fatalf("错误 = %v，want 指名 cache redis", err)
		}
	})
	t.Run("坏状态redis URL", func(t *testing.T) {
		cfg := composeTestConfig(t)
		cfg.RuntimeStateDriver = "redis"
		cfg.RedisStateURL = "://w1o-bad-state-url"
		services, err := composeChainRuntimeServices(&composition{}, cfg, fakeRead)
		if err == nil {
			if services != nil {
				services.Close()
			}
			t.Fatal("坏状态 redis URL 必须报错")
		}
		if !strings.Contains(err.Error(), "parse state redis url") {
			t.Fatalf("错误 = %v，want 包含 parse state redis url", err)
		}
	})
}

// ---------------------------------------------------------------------------
// C11. chain_runtime.go：composeChainRuntimeServices memory 全字段成功路径
// （复用 chain_test.go 的 newChainFixture：其 schema 已被 chain_phase3_test.go
// 证明满足该函数的读模型 / 选择器 / 策略源构造需求）
// ---------------------------------------------------------------------------

func TestW1OComposeChainRuntimeServicesMemorySuccess(t *testing.T) {
	fixture := newChainFixture(t)
	cfg := composeTestConfig(t)
	composed := &composition{
		db:      fixture.db,
		statsDB: fixture.statsDB,
		Bus:     inval.New(time.Now),
	}
	services, err := composeChainRuntimeServices(composed, cfg, func(string) (string, error) { return "UTC", nil })
	if err != nil {
		t.Fatalf("memory 驱动成功路径: %v", err)
	}
	t.Cleanup(services.Close)

	nonNil := map[string]bool{
		"Cache":                     services.Cache != nil,
		"Circuits":                  services.Circuits != nil,
		"IPPolicy":                  services.IPPolicy != nil,
		"UserLimits":                services.UserLimits != nil,
		"ModelsRateLimit":           services.ModelsRateLimit != nil,
		"Avoidance":                 services.Avoidance != nil,
		"Affinity":                  services.Affinity != nil,
		"APIKeyQuota":               services.APIKeyQuota != nil,
		"AuthzQuota":                services.AuthzQuota != nil,
		"InflightQuota":             services.InflightQuota != nil,
		"Accounts":                  services.Accounts != nil,
		"Identity":                  services.Identity != nil,
		"QuotaStats":                services.QuotaStats != nil,
		"LatencyDegradation":        services.LatencyDegradation != nil,
		"AccountAPIKeyGuard":        services.AccountAPIKeyGuard != nil,
		"AccountAPIKeyEffects":      services.AccountAPIKeyEffects != nil,
		"ConfiguredPolicyAvoidance": services.ConfiguredPolicyAvoidance != nil,
		"AccountCircuits":           services.AccountCircuits != nil,
		"ClientIPSlots":             services.ClientIPSlots != nil,
		"HighConcurrencyQueue":      services.HighConcurrencyQueue != nil,
		"SuppressionStore":          services.SuppressionStore != nil,
		"SuppressionWaiter":         services.SuppressionWaiter != nil,
		"ProxyHealth":               services.ProxyHealth != nil,
		"HotQuality":                services.HotQuality != nil,
		"KeyModelStore":             services.KeyModelStore != nil,
		"Recoverable":               services.Recoverable != nil,
	}
	for name, ok := range nonNil {
		if !ok {
			t.Errorf("%s 不得为 nil（fail-fast 契约：返回包无空端口）", name)
		}
	}
	if services.Identity.Identity == nil || services.Identity.Affinity == nil {
		t.Fatalf("G14 内部服务缺失: Identity=%v Affinity=%v",
			services.Identity.Identity != nil, services.Identity.Affinity != nil)
	}
	if services.StateClient != nil || services.HybridScoringCache != nil ||
		services.HybridRuntimeState != nil || services.RateLimitStore != nil {
		t.Fatalf("memory 驱动下 redis 协作者必须为 nil: StateClient=%v HybridScoringCache=%v HybridRuntimeState=%v RateLimitStore=%v",
			services.StateClient != nil, services.HybridScoringCache != nil,
			services.HybridRuntimeState != nil, services.RateLimitStore != nil)
	}
}

// ---------------------------------------------------------------------------
// C12. chain_runtime.go：runtime-state 驱动分叉（standalone fail-fast /
// performance + miniredis 成功）
// ---------------------------------------------------------------------------

func TestW1OComposeChainRuntimeServicesRedisStateArms(t *testing.T) {
	fakeRead := func(string) (string, error) { return "UTC", nil }

	t.Run("standalone加redis状态驱动fail-fast", func(t *testing.T) {
		redisServer := miniredis.RunT(t)
		fixture := newChainFixture(t)
		cfg := composeTestConfig(t)
		cfg.RuntimeStateDriver = "redis"
		cfg.RedisStateURL = "redis://" + redisServer.Addr()
		cfg.RedisNamespace = "w1o-chain-ns"
		composed := &composition{db: fixture.db, statsDB: fixture.statsDB, Bus: inval.New(time.Now)}
		services, err := composeChainRuntimeServices(composed, cfg, fakeRead)
		if err == nil {
			if services != nil {
				services.Close()
			}
			t.Fatal("standalone 模式 + redis 状态驱动必须在热质量装配处 fail-fast")
		}
		if !strings.Contains(err.Error(), "热质量") {
			t.Fatalf("错误 = %v，want 包含 热质量", err)
		}
	})

	t.Run("performance加redis状态驱动成功", func(t *testing.T) {
		redisServer := miniredis.RunT(t)
		fixture := newChainFixture(t)
		cfg := composeTestConfig(t)
		cfg.RuntimeMode = "performance"
		cfg.RuntimeStateDriver = "redis"
		cfg.RedisStateURL = "redis://" + redisServer.Addr()
		cfg.RedisNamespace = "w1o-chain-ns"
		composed := &composition{db: fixture.db, statsDB: fixture.statsDB, Bus: inval.New(time.Now)}
		services, err := composeChainRuntimeServices(composed, cfg, fakeRead)
		if err != nil {
			t.Fatalf("performance + redis 状态驱动成功路径: %v", err)
		}
		t.Cleanup(services.Close)

		if services.StateClient == nil {
			t.Error("redis 状态驱动必须暴露共享 StateClient")
		}
		if services.HybridRuntimeState == nil {
			t.Error("redis 状态驱动必须装配 HybridRuntimeState")
		}
		if services.HotQuality == nil || services.Identity == nil || services.Affinity == nil {
			t.Errorf("热质量 / 身份服务必须装配: HotQuality=%v Identity=%v Affinity=%v",
				services.HotQuality != nil, services.Identity != nil, services.Affinity != nil)
		}
		// 缓存驱动仍为 memory：混合评分共享缓存与系统限流 redis store 不得装配。
		if services.HybridScoringCache != nil || services.RateLimitStore != nil {
			t.Errorf("memory 缓存驱动下 HybridScoringCache/RateLimitStore 必须为 nil: %v/%v",
				services.HybridScoringCache != nil, services.RateLimitStore != nil)
		}
	})
}
