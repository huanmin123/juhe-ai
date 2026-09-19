package main

// w1_compose_deep_test.go —— w1 波次补充覆盖（w1q 前缀，TestW1Q 入口）。
//
// 与既有覆盖的边界（不重复）：
//   - composeSystemAPI 的 nil 依赖臂、SQLite 命名路径失败臂、memory 成功变体
//     由 w1_compose_root_test.go 覆盖；这里补 preflight fail-fast、PG pool
//     错误臂、表监控 configure 失败臂、redis 认证驱动失败臂、memory 验证码
//     臂、Go runtime metrics 打开失败臂、health outcome 路径变体、链条组装
//     失败臂与 redis 缓存+状态驱动全链条成功变体。
//   - compose.go 小适配器（delegatedSettingsAdapter / unavailableUsageReader /
//     producerLogger）此前零覆盖，这里直接驱动。
//   - compose_accounts_reset.go 的 quota 成功路径与 guard 记忆由
//     w1_accounts_reset_test.go 覆盖（复用其 w1sEnsure*Schema fixture）；这里补
//     bridge 构造错误臂、运行态清理分支臂、时延降级两臂、池重验证两臂、
//     transient 状态加载/清除臂与 quota 各错误臂。
//   - chain_ports.go 的请求错误诊断与投影 helper 由 w1_ports_arms_test.go 与
//     chain_failure_dispatch_test.go 覆盖（复用其 fixture）；这里补
//     Decide 错误臂、effects 写侧错误臂、codexRecoveryMetadataOf、空值头
//     跳过、取消 ctx 读取、非对象 JSON 投影、本地预检直通与目标分组错误臂。
//   - runtime.go 的默认值路径由 compose_test.go 覆盖；这里表驱动补齐错误臂
//     与解析成功臂。
//
// 全部用例确定性可重放：redis 一律 miniredis 或立即解析失败的坏 URL；数据库
// 一律临时 SQLite 文件；无并发等待、无网络依赖。失败臂统一走
// w1oRedirectDatabasePaths 的手管目录（组合根失败路径不回收 SQLite 句柄，
// Windows 上 t.TempDir 清理会因句柄锁失败）。

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accountkeystates"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
	"github.com/huanminabc/juhe-ai/backend-go-platform/gometrics"
)

// ---------------------------------------------------------------------------
// compose.go：小适配器（此前零覆盖）
// ---------------------------------------------------------------------------

func TestW1QComposeSmallAdapterArms(t *testing.T) {
	t.Run("delegated设置读取适配器透传", func(t *testing.T) {
		adapter := delegatedSettingsAdapter{read: func(key string) (string, error) {
			if key == "usageStatsTimezone" {
				return "UTC", nil
			}
			return "", errors.New("w1q 设置键读取失败")
		}}
		value, err := adapter.SettingValue("usageStatsTimezone")
		if err != nil || value != "UTC" {
			t.Fatalf("SettingValue = (%q, %v)，want (UTC, nil)", value, err)
		}
		if _, err := adapter.SettingValue("missing"); err == nil || !strings.Contains(err.Error(), "w1q 设置键读取失败") {
			t.Fatalf("读取失败必须透传原始错误，实际 %v", err)
		}
	})

	t.Run("网关运行态用量读取器恒为不可用契约", func(t *testing.T) {
		value, err := (unavailableUsageReader{}).RequestLimitTotal(context.Background(), "acc-w1q")
		if err == nil {
			t.Fatal("unavailableUsageReader 必须恒报错（降级契约）")
		}
		if value != "" {
			t.Fatalf("错误路径值 = %q，want 空串", value)
		}
		if !strings.Contains(err.Error(), "chain slice") {
			t.Fatalf("错误 = %v，want 包含 chain slice", err)
		}
	})

	t.Run("producer日志器走slog默认句柄", func(t *testing.T) {
		var buf bytes.Buffer
		previous := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
		defer slog.SetDefault(previous)
		producerLogger{}.Warn("w1q-警告消息", "w1q键", "值")
		producerLogger{}.Error("w1q-错误消息")
		out := buf.String()
		if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "w1q-警告消息") {
			t.Fatalf("Warn 输出不符：%s", out)
		}
		if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "w1q-错误消息") {
			t.Fatalf("Error 输出不符：%s", out)
		}
	})
}

// ---------------------------------------------------------------------------
// compose.go：composeSystemAPI 配置变体成功臂与命名错误臂
//
// 组合根依赖栈与手管目录清理策略直接复用 w1_compose_root_test.go 的
// w1oNewComposeStack / w1oRedirectDatabasePaths，避免二次聚合造成漂移。
// ---------------------------------------------------------------------------

func TestW1QComposeSystemAPIStorageFailureArms(t *testing.T) {
	t.Run("缺统计库路径组合根守卫失败", func(t *testing.T) {
		stack := w1oNewComposeStack(t)
		w1oRedirectDatabasePaths(t, stack)
		stack.cfg.StatsDatabasePath = ""
		composed, err := stack.compose(t)
		if err == nil {
			if composed != nil {
				composed.Shutdown()
			}
			t.Fatal("缺统计库路径必须 fail-fast")
		}
		// 2026-09-19 起 preflight 不再缺路径 fail-fast（loadRuntimeConfig 已
		// 派生默认路径，直接构造的空路径由物理门禁跳过空项），空 stats 路径
		// 由组合根打开 stats 数据库前的守卫指名拒绝。
		if !strings.Contains(err.Error(), "JUHE_AI_STATS_DATABASE_PATH") {
			t.Fatalf("错误 = %v，want 组合根守卫指名统计库路径", err)
		}
	})

	t.Run("表监控库路径指向非SQLite文件configure失败", func(t *testing.T) {
		stack := w1oNewComposeStack(t)
		w1oRedirectDatabasePaths(t, stack)
		garbage := filepath.Join(filepath.Dir(stack.cfg.BusinessDatabasePath), "w1q-table-monitor-garbage.sqlite3")
		if err := os.WriteFile(garbage, bytes.Repeat([]byte{0x51}, 512), 0o644); err != nil {
			t.Fatalf("写入非 SQLite 垃圾文件: %v", err)
		}
		stack.cfg.TableMonitorDatabasePath = garbage
		composed, err := stack.compose(t)
		if err == nil {
			if composed != nil {
				composed.Shutdown()
			}
			t.Fatal("表监控库路径为非 SQLite 文件必须 fail-fast")
		}
		if !strings.Contains(err.Error(), "configure table monitor sqlite database") {
			t.Fatalf("错误 = %v，want 包含 configure table monitor sqlite database", err)
		}
	})

	t.Run("postgres驱动缺业务URL连接池错误臂", func(t *testing.T) {
		stack := w1oNewComposeStack(t)
		stack.cfg.DatabaseDriver = "postgres"
		stack.cfg.BusinessPostgresURL = ""
		composed, err := stack.compose(t)
		if err == nil {
			if composed != nil {
				composed.Shutdown()
			}
			t.Fatal("postgres 驱动缺业务 URL 必须 fail-fast")
		}
		if !strings.Contains(err.Error(), "open business PostgreSQL pool") {
			t.Fatalf("错误 = %v，want 包含 open business PostgreSQL pool", err)
		}
	})
}

func TestW1QComposeSystemAPIAuthDriverArms(t *testing.T) {
	t.Run("memory验证码服务臂组装成功", func(t *testing.T) {
		stack := w1oNewComposeStack(t)
		stack.cfg.CaptchaDisabled = false
		composed, err := stack.compose(t)
		if err != nil {
			t.Fatalf("memory 验证码臂必须组装成功: %v", err)
		}
		defer composed.Shutdown()
		if composed.authDeps == nil {
			t.Fatal("authDeps 必须装配")
		}
	})

	t.Run("坏redis状态URL验证码服务失败臂", func(t *testing.T) {
		stack := w1oNewComposeStack(t)
		w1oRedirectDatabasePaths(t, stack)
		stack.cfg.CaptchaDisabled = false
		stack.cfg.RuntimeStateDriver = "redis"
		stack.cfg.RedisStateURL = "://w1q-bad-captcha-url"
		composed, err := stack.compose(t)
		if err == nil {
			if composed != nil {
				composed.Shutdown()
			}
			t.Fatal("坏 redis URL 必须 fail-fast")
		}
		if !strings.Contains(err.Error(), "create shared captcha service") {
			t.Fatalf("错误 = %v，want 包含 create shared captcha service", err)
		}
	})

	t.Run("坏redis状态URL登录守卫失败臂", func(t *testing.T) {
		stack := w1oNewComposeStack(t)
		w1oRedirectDatabasePaths(t, stack)
		stack.cfg.CaptchaDisabled = true
		stack.cfg.RuntimeStateDriver = "redis"
		stack.cfg.RedisStateURL = "://w1q-bad-guard-url"
		composed, err := stack.compose(t)
		if err == nil {
			if composed != nil {
				composed.Shutdown()
			}
			t.Fatal("坏 redis URL 必须 fail-fast")
		}
		if !strings.Contains(err.Error(), "create shared login guard") {
			t.Fatalf("错误 = %v，want 包含 create shared login guard", err)
		}
	})
}

func TestW1QComposeSystemAPIMetricsAndHealthOutcomeArms(t *testing.T) {
	t.Run("Go运行时指标库打开失败臂", func(t *testing.T) {
		stack := w1oNewComposeStack(t)
		w1oRedirectDatabasePaths(t, stack)
		blocker := filepath.Join(filepath.Dir(stack.cfg.BusinessDatabasePath), "w1q-blocker-file")
		if err := os.WriteFile(blocker, nil, 0o644); err != nil {
			t.Fatalf("create blocker file: %v", err)
		}
		stack.cfg.GoRuntimeMetrics = gometrics.Config{
			Enabled:      true,
			Store:        gometrics.DialectSQLite,
			DatabasePath: filepath.Join(blocker, "metrics.sqlite3"),
			Service:      "juhe-ai",
			Role:         "gateway",
		}
		composed, err := stack.compose(t)
		if err == nil {
			if composed != nil {
				composed.Shutdown()
			}
			t.Fatal("metrics 库路径父级为文件必须 fail-fast")
		}
		if !strings.Contains(err.Error(), "open Go runtime metrics store") {
			t.Fatalf("错误 = %v，want 包含 open Go runtime metrics store", err)
		}
	})

	t.Run("健康结果SQLite路径变体组装成功", func(t *testing.T) {
		stack := w1oNewComposeStack(t)
		stack.cfg.AccountHealthOutcomeSQLitePath = filepath.Join(filepath.Dir(stack.cfg.DatasetDatabasePath), "w1q-outcomes.sqlite3")
		composed, err := stack.compose(t)
		if err != nil {
			t.Fatalf("带健康结果路径必须组装成功: %v", err)
		}
		defer composed.Shutdown()
		if composed.Kernel == nil {
			t.Fatal("Kernel 必须装配")
		}
	})
}

func TestW1QComposeSystemAPIChainRuntimeArms(t *testing.T) {
	t.Run("坏缓存redisURL链条组装失败臂", func(t *testing.T) {
		stack := w1oNewComposeStack(t)
		w1oRedirectDatabasePaths(t, stack)
		stack.cfg.ChainEnabled = true
		stack.cfg.CacheDriver = "redis"
		stack.cfg.RedisCacheURL = "://w1q-bad-cache-url"
		composed, err := stack.compose(t)
		if err == nil {
			if composed != nil {
				composed.Shutdown()
			}
			t.Fatal("坏缓存 redis URL 必须 fail-fast")
		}
		if !strings.Contains(err.Error(), "compose gateway chain runtime services") {
			t.Fatalf("错误 = %v，want 包含 compose gateway chain runtime services", err)
		}
	})

	t.Run("redis缓存加状态驱动全链条成功变体", func(t *testing.T) {
		redisServer := miniredis.RunT(t)
		stack := w1oNewComposeStack(t)
		stack.cfg.RuntimeMode = "performance"
		stack.cfg.CacheDriver = "redis"
		stack.cfg.RuntimeStateDriver = "redis"
		stack.cfg.RedisCacheURL = "redis://" + redisServer.Addr()
		stack.cfg.RedisStateURL = "redis://" + redisServer.Addr()
		stack.cfg.RedisNamespace = "w1q-ns"
		stack.cfg.ChainEnabled = true
		stack.cfg.AuditLogEnabled = true
		composed, err := stack.compose(t)
		if err != nil {
			t.Fatalf("redis 全链条变体必须组装成功: %v", err)
		}
		defer composed.Shutdown()
		if composed.chain == nil || composed.chainServices == nil {
			t.Fatal("链条开启时 chain/chainServices 必须装配")
		}
		if composed.chainServices.StateClient == nil {
			t.Fatal("redis 状态驱动必须暴露共享 StateClient（aipublic 限流共用）")
		}
		if composed.chainServices.RateLimitStore == nil {
			t.Fatal("redis 缓存驱动必须装配系统限流 redis store")
		}
	})
}

// ---------------------------------------------------------------------------
// compose_accounts_reset.go：bridge 构造错误臂与运行态清理分支臂
// ---------------------------------------------------------------------------

type w1qResetFixture struct {
	db     *sql.DB
	bus    *inval.Bus
	bridge *accountsRuntimeResetBridge
	now    time.Time
}

func w1qNewResetFixture(t *testing.T) *w1qResetFixture {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "w1q-reset.sqlite3"))
	if err != nil {
		t.Fatalf("打开临时业务库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	w1sEnsureBusinessSchema(t, db)
	w1sEnsureStatsSchema(t, db)
	// w1s 共享 DDL 的 accounts 表缺 revalidate 读取列；按 accountkeystates
	// loadRevalidateAccountRow 的列清单补齐（SQLite ADD COLUMN 逐列追加）。
	for _, column := range []string{
		"provider_code TEXT", "protocol_code TEXT", "protocol_version TEXT",
		"type TEXT", "status TEXT", "schedulable INTEGER DEFAULT 1",
		"config_revision INTEGER DEFAULT 1", "credentials_encrypted TEXT",
	} {
		if _, err := db.Exec("ALTER TABLE accounts ADD COLUMN " + column); err != nil {
			t.Fatalf("补齐 accounts 列 %s: %v", column, err)
		}
	}
	now := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	stats, err := gatewayquota.NewStatsStore(db, false)
	if err != nil {
		t.Fatalf("构造 quota stats store: %v", err)
	}
	guard := gatewayaccounteffects.NewAccountAPIKeyFailureGuard(
		gatewayaccounteffects.SideEffectsConfig{RuntimeStateDriver: "memory"},
		nil, nil, nil)
	bus := inval.New(time.Now)
	composed := &composition{db: db, Bus: bus}
	bridge, err := newAccountsRuntimeResetBridge(composed,
		func(key string) (string, error) {
			if key == "usageStatsTimezone" {
				return "UTC", nil
			}
			return "", nil
		},
		&chainRuntimeServices{QuotaStats: stats, AccountAPIKeyGuard: guard},
		"w1q-reset-secret",
		nil)
	if err != nil {
		t.Fatalf("构造 reset bridge: %v", err)
	}
	concrete := bridge.(*accountsRuntimeResetBridge)
	concrete.now = func() time.Time { return now }
	return &w1qResetFixture{db: db, bus: bus, bridge: concrete, now: now}
}

func TestW1QResetBridgeConstructionErrorArm(t *testing.T) {
	if _, err := newAccountsRuntimeResetBridge(&composition{}, nil, &chainRuntimeServices{}, "w1q-secret", nil); err == nil {
		t.Fatal("缺业务库句柄时 bridge 构造必须报错")
	} else if !strings.Contains(err.Error(), "accountkeystates") {
		t.Fatalf("错误 = %v，want 指名 accountkeystates", err)
	}
}

func TestW1QResetBridgeClearAvailabilityArms(t *testing.T) {
	fixture := w1qNewResetFixture(t)
	ctx := context.Background()

	var runtimeReasons []string
	fixture.bus.Subscribe(inval.TopicGatewayRuntime, func(_, reason string) { runtimeReasons = append(runtimeReasons, reason) })

	t.Run("空账户ID零键短路", func(t *testing.T) {
		result, err := fixture.bridge.ClearAccountRuntimeAvailability(ctx, accounts.RuntimeAvailabilityClearInput{})
		if err != nil || result.Cleared {
			t.Fatalf("空账户ID = (%v, %v)，want (false, nil)", result.Cleared, err)
		}
		if len(runtimeReasons) != 0 {
			t.Fatalf("短路不得发布总线事件，实际 %v", runtimeReasons)
		}
	})

	t.Run("授权绑定加排除基础键全量清理", func(t *testing.T) {
		result, err := fixture.bridge.ClearAccountRuntimeAvailability(ctx, accounts.RuntimeAvailabilityClearInput{
			AccountID: "acc-w1q-clear",
			AuthorizedBinding: &accounts.RuntimeAuthorizedBinding{
				SystemAccountID:        "sys-w1q",
				GroupID:                "grp-w1q",
				AccountAuthorizationID: "az-w1q",
			},
			IncludeBaseAccountKey: false,
		})
		if err != nil || !result.Cleared {
			t.Fatalf("清理结果 = (%v, %v)，want (true, nil)", result.Cleared, err)
		}
		if len(runtimeReasons) != 1 || runtimeReasons[0] != "account_runtime_reset:acc-w1q-clear" {
			t.Fatalf("总线原因 = %v，want [account_runtime_reset:acc-w1q-clear]", runtimeReasons)
		}
	})
}

func TestW1QResetBridgeLatencyAndRevalidateArms(t *testing.T) {
	fixture := w1qNewResetFixture(t)
	ctx := context.Background()

	t.Run("时延降级桥nil服务臂", func(t *testing.T) {
		cleared, err := fixture.bridge.ClearNormalRouteLatencyDegradation(ctx, "sys-w1q", "acc-w1q")
		if err != nil || cleared != 0 {
			t.Fatalf("nil 时延服务 = (%d, %v)，want (0, nil)", cleared, err)
		}
	})

	t.Run("时延降级桥真实服务空索引臂", func(t *testing.T) {
		fixture.bridge.latency = gatewayproxyhealth.NewLatencyDegradationService(
			gatewayproxyhealth.NewMemoryRuntimeStateStore(nil), nil, gatewayproxyhealth.LatencyDegradationOptions{})
		cleared, err := fixture.bridge.ClearNormalRouteLatencyDegradation(ctx, "sys-w1q", "acc-w1q")
		if err != nil || cleared != 0 {
			t.Fatalf("空索引清理 = (%d, %v)，want (0, nil)", cleared, err)
		}
	})

	t.Run("池重验证参数非法错误臂", func(t *testing.T) {
		if _, err := fixture.bridge.RevalidateAccountAPIKeyRuntimePool(ctx, "", 0); err == nil {
			t.Fatal("空账户ID必须报参数无效")
		} else if !strings.Contains(err.Error(), "参数无效") {
			t.Fatalf("错误 = %v，want 包含 参数无效", err)
		}
	})

	t.Run("池重验证账户缺失投影臂", func(t *testing.T) {
		result, err := fixture.bridge.RevalidateAccountAPIKeyRuntimePool(ctx, "acc-w1q-missing", 1)
		if err != nil {
			t.Fatalf("缺失账户不得报错: %v", err)
		}
		if result.Eligible || result.Changed != 0 || result.Reason != accountkeystates.ReasonAccountNotFound {
			t.Fatalf("缺失账户投影 = %+v，want 未变更 + account_not_found", result)
		}
	})
}

// ---------------------------------------------------------------------------
// compose_accounts_reset.go：transient 状态加载 / 清除臂
// ---------------------------------------------------------------------------

// w1qFakeTransientStore 实现 gatewayaccounteffects.AccountApiKeyTransientStateStore。
type w1qFakeTransientStore struct {
	loadStates []gatewayaccounteffects.AccountApiKeyTransientDispatchState
	loadErr    error
	success    gatewayaccounteffects.AccountApiKeyTransientMutationResult
}

func (s *w1qFakeTransientStore) RecordFailure(context.Context, gatewayaccounteffects.TransientMutationInput) (gatewayaccounteffects.AccountApiKeyTransientMutationResult, error) {
	return s.success, nil
}

func (s *w1qFakeTransientStore) RecordSuccess(context.Context, gatewayaccounteffects.TransientMutationInput) (gatewayaccounteffects.AccountApiKeyTransientMutationResult, error) {
	return s.success, nil
}

func (s *w1qFakeTransientStore) LoadMany(context.Context, string, []string) ([]gatewayaccounteffects.AccountApiKeyTransientDispatchState, error) {
	return s.loadStates, s.loadErr
}

// w1qRedisBridge 复用 fixture 的库与总线，但换成调用方指定的 guard。
func w1qRedisBridge(t *testing.T, fixture *w1qResetFixture, guard *gatewayaccounteffects.AccountAPIKeyFailureGuard) *accountsRuntimeResetBridge {
	t.Helper()
	return &accountsRuntimeResetBridge{
		bus:      fixture.bus,
		db:       fixture.db,
		settings: func(string) (string, error) { return "UTC", nil },
		guard:    guard,
		now:      func() time.Time { return fixture.now },
	}
}

func TestW1QResetBridgeTransientStatesArms(t *testing.T) {
	ctx := context.Background()

	t.Run("redis驱动缺状态URL加载错误臂", func(t *testing.T) {
		fixture := w1qNewResetFixture(t)
		redisGuard := gatewayaccounteffects.NewAccountAPIKeyFailureGuard(
			gatewayaccounteffects.SideEffectsConfig{RuntimeStateDriver: "redis"}, nil, nil, nil)
		bridge := w1qRedisBridge(t, fixture, redisGuard)
		if _, err := bridge.LoadAPIKeyTransientStates(ctx, "acc-w1q", []string{"fp-w1q"}); err == nil {
			t.Fatal("redis 驱动缺状态 URL 必须报错")
		} else if !strings.Contains(err.Error(), "JUHE_AI_REDIS_STATE_URL") {
			t.Fatalf("错误 = %v，want 指名 JUHE_AI_REDIS_STATE_URL", err)
		}
	})

	t.Run("共享状态加载记忆generation臂", func(t *testing.T) {
		fixture := w1qNewResetFixture(t)
		generation := "gen-w1q-1"
		redisGuard := gatewayaccounteffects.NewAccountAPIKeyFailureGuard(
			gatewayaccounteffects.SideEffectsConfig{RuntimeStateDriver: "redis"}, nil, nil, nil)
		redisGuard.SetTransientStateStoreForTest(&w1qFakeTransientStore{
			success: gatewayaccounteffects.AccountApiKeyTransientMutationResult{Applied: true},
			loadStates: []gatewayaccounteffects.AccountApiKeyTransientDispatchState{{
				State: &gatewayaccounteffects.AccountApiKeyTransientState{
					KeyFingerprint: "fp-w1q",
					Generation:     generation,
				},
			}},
		})
		bridge := w1qRedisBridge(t, fixture, redisGuard)

		states, err := bridge.LoadAPIKeyTransientStates(ctx, "acc-w1q", []string{"fp-w1q"})
		if err != nil {
			t.Fatalf("加载瞬态状态: %v", err)
		}
		if len(states) != 1 || !states[0].HasGeneration || states[0].KeyFingerprint != "fp-w1q" {
			t.Fatalf("瞬态状态 = %+v，want 1 条带 generation", states)
		}
		if got := bridge.rememberedTransientGeneration("acc-w1q", "fp-w1q"); got != generation {
			t.Fatalf("记忆 generation = %q，want %q", got, generation)
		}

		// 记忆命中后清除走 CAS 成功臂。
		one := int64(1)
		cleared, err := bridge.ClearAPIKeyTransientFailure(ctx, "acc-w1q", "fp-w1q", &one)
		if err != nil || !cleared {
			t.Fatalf("记忆命中清除 = (%v, %v)，want (true, nil)", cleared, err)
		}
	})

	t.Run("清除瞬态失败缺席与未记忆臂", func(t *testing.T) {
		fixture := w1qNewResetFixture(t)
		one := int64(1)
		if cleared, err := fixture.bridge.ClearAPIKeyTransientFailure(ctx, "acc-w1q", "fp-w1q", nil); cleared || err != nil {
			t.Fatalf("nil generation = (%v, %v)，want (false, nil)", cleared, err)
		}
		if cleared, err := fixture.bridge.ClearAPIKeyTransientFailure(ctx, "acc-w1q", "fp-missing", &one); cleared || err != nil {
			t.Fatalf("未记忆 generation = (%v, %v)，want (false, nil)", cleared, err)
		}
	})
}

// ---------------------------------------------------------------------------
// compose_accounts_reset.go：quota 各错误臂
// ---------------------------------------------------------------------------

func TestW1QResetBridgeQuotaErrorArms(t *testing.T) {
	ctx := context.Background()

	t.Run("时区读取失败臂", func(t *testing.T) {
		fixture := w1qNewResetFixture(t)
		fixture.bridge.settings = func(string) (string, error) { return "Invalid/Zone", nil }
		exceeded, err := fixture.bridge.AuthorizationQuotaExceeded(ctx, accounts.AuthorizationQuotaCheckInput{
			AuthorizationID: "ra-w1q", GranteeSystemAccountID: "g1"})
		if err == nil || exceeded {
			t.Fatalf("非法时区 = (%v, %v)，want (false, err)", exceeded, err)
		}
	})

	t.Run("直接授权limits查询失败臂", func(t *testing.T) {
		fixture := w1qNewResetFixture(t)
		if _, err := fixture.db.Exec("DROP TABLE resource_authorizations"); err != nil {
			t.Fatalf("drop resource_authorizations: %v", err)
		}
		if _, err := fixture.bridge.authorizationLimitsJSON(ctx, "ra-w1q"); err == nil {
			t.Fatal("缺表时直接 limits 查询必须报错")
		}
		if _, err := fixture.bridge.AuthorizationQuotaExceeded(ctx, accounts.AuthorizationQuotaCheckInput{
			AuthorizationID: "ra-w1q", GranteeSystemAccountID: "g1"}); err == nil {
			t.Fatal("缺表时 quota 判定必须报错")
		}
	})

	t.Run("团队grant查询失败臂", func(t *testing.T) {
		fixture := w1qNewResetFixture(t)
		if _, err := fixture.db.Exec("DROP TABLE resource_authorization_grants"); err != nil {
			t.Fatalf("drop grants: %v", err)
		}
		if _, err := fixture.bridge.teamGrantLimitsJSON(ctx, "ra-w1q", fixture.now); err == nil {
			t.Fatal("缺 grants 表时团队查询必须报错")
		}
		if _, err := fixture.bridge.AuthorizationQuotaExceeded(ctx, accounts.AuthorizationQuotaCheckInput{
			AuthorizationID: "ra-w1q", GranteeSystemAccountID: "g1", EffectiveSourceTeamID: "tm-w1q"}); err == nil {
			t.Fatal("缺 grants 表时团队 quota 判定必须报错")
		}
	})

	t.Run("实例账户查询失败臂", func(t *testing.T) {
		fixture := w1qNewResetFixture(t)
		futureISO := fixture.now.Add(48*time.Hour).UTC().Format("2006-01-02T15:04:05.000") + "Z"
		if _, err := fixture.db.Exec(
			`INSERT INTO resource_authorizations (id, resource_type, resource_id, effective_source_team_id, status, expires_at)
			VALUES ('ra-w1q-team', 'model', 'm1', 'tm-w1q', 'active', ?)`, futureISO); err != nil {
			t.Fatalf("seed auth: %v", err)
		}
		if _, err := fixture.db.Exec(
			`INSERT INTO resource_authorization_grants (resource_type, resource_id, grantee_type, grantee_team_id, limits_json, status, expires_at)
			VALUES ('model', 'm1', 'team', 'tm-w1q', '{"total":{"enabled":true,"limit":3}}', 'active', ?)`, futureISO); err != nil {
			t.Fatalf("seed grant: %v", err)
		}
		if _, err := fixture.db.Exec("DROP TABLE accounts"); err != nil {
			t.Fatalf("drop accounts: %v", err)
		}
		if _, err := fixture.bridge.authorizationInstanceIDs(ctx, "ra-w1q-team"); err == nil {
			t.Fatal("缺 accounts 表时实例查询必须报错")
		}
		if _, err := fixture.bridge.AuthorizationQuotaExceeded(ctx, accounts.AuthorizationQuotaCheckInput{
			AuthorizationID: "ra-w1q-team", GranteeSystemAccountID: "g1", EffectiveSourceTeamID: "tm-w1q"}); err == nil {
			t.Fatal("缺 accounts 表时团队 quota 判定必须报错")
		}
	})

	t.Run("用量成本批量读取失败臂", func(t *testing.T) {
		fixture := w1qNewResetFixture(t)
		if _, err := fixture.db.Exec(
			`INSERT INTO resource_authorizations (id, resource_type, resource_id, status, limits_json)
			VALUES ('ra-w1q-cost', 'model', 'm1', 'active', '{"daily":{"enabled":true,"limit":10}}')`); err != nil {
			t.Fatalf("seed auth: %v", err)
		}
		for _, table := range []string{
			"usage_stats_totals", "usage_stats_daily", "usage_stats_weekly",
			"usage_stats_monthly", "usage_quota_hourly_windows",
		} {
			if _, err := fixture.db.Exec("DROP TABLE " + table); err != nil {
				t.Fatalf("drop %s: %v", table, err)
			}
		}
		if _, err := fixture.bridge.AuthorizationQuotaExceeded(ctx, accounts.AuthorizationQuotaCheckInput{
			AuthorizationID: "ra-w1q-cost", GranteeSystemAccountID: "g1"}); err == nil {
			t.Fatal("用量表缺失时成本读取必须报错")
		}

		// 小时窗口限额走 appendCheck 的 Hourly 分支后同样在成本读取处报错。
		if _, err := fixture.db.Exec(
			`INSERT INTO resource_authorizations (id, resource_type, resource_id, status, limits_json)
			VALUES ('ra-w1q-hourly', 'model', 'm1', 'active',
				'{"hourly":{"enabled":true,"hours":2,"limit":10}}')`); err != nil {
			t.Fatalf("seed hourly auth: %v", err)
		}
		if _, err := fixture.bridge.AuthorizationQuotaExceeded(ctx, accounts.AuthorizationQuotaCheckInput{
			AuthorizationID: "ra-w1q-hourly", GranteeSystemAccountID: "g1"}); err == nil {
			t.Fatal("小时窗口限额必须在成本读取处报错")
		}
	})
}

// ---------------------------------------------------------------------------
// chain_ports.go：投影 helper 与目标分组错误臂
// ---------------------------------------------------------------------------

// w1qFailingPolicyEffects 实现 chainAccountErrorPolicyEffects，写侧恒失败。
type w1qFailingPolicyEffects struct {
	chainAccountErrorPolicyEffects
}

func (w1qFailingPolicyEffects) ApplyAccountErrorPolicyDecision(context.Context, gatewaydispatch.AccountCandidate, accountErrorPolicyDecision, chainErrorPolicyFailureInput) (bool, string, error) {
	return false, "", errors.New("w1q 策略状态写入失败")
}

func (w1qFailingPolicyEffects) RecordKeyScopedQuotaFailure(context.Context, gatewaydispatch.AccountCandidate, accountErrorPolicyDecision, chainErrorPolicyFailureInput) error {
	return errors.New("w1q 策略状态写入失败")
}

func TestW1QChainPortsProjectionArms(t *testing.T) {
	ctx := context.Background()

	t.Run("codex恢复元数据nil臂与全字段臂", func(t *testing.T) {
		input := gatewaydispatch.FailedUpstreamResponseInput{
			Account:     gatewaydispatch.AccountCandidate{ID: "acc-w1q"},
			UpstreamURL: "https://upstream-w1q.example/chat",
		}
		base := codexRecoveryMetadataOf(input, gatewaycodex.CodexEncryptedContentRecoveryResult{})
		if base["accountId"] != "acc-w1q" || base["upstreamUrl"] != input.UpstreamURL || base["transport"] != "http" || len(base) != 3 {
			t.Fatalf("nil 元数据投影 = %#v", base)
		}
		full := codexRecoveryMetadataOf(input, gatewaycodex.CodexEncryptedContentRecoveryResult{
			Metadata: &gatewaycodex.CodexEncryptedContentRecoveryMetadata{
				Strategy: "w1q-strategy", RemovedReasoningItemCount: 2, BodyBytesBefore: 100, BodyBytesAfter: 80,
			},
		})
		if full["strategy"] != "w1q-strategy" || full["removedReasoningItemCount"] != 2 ||
			full["bodyBytesBefore"] != 100 || full["bodyBytesAfter"] != 80 {
			t.Fatalf("全字段投影 = %#v", full)
		}
	})

	t.Run("响应头投影跳过空值头", func(t *testing.T) {
		headers := responseHeadersOf(&gatewaydispatch.GatewayUpstreamResponse{
			Header: http.Header{"Content-Type": []string{"application/json"}, "X-Empty": nil},
		})
		if headers["content-type"] != "application/json" {
			t.Fatalf("content-type 投影 = %#v", headers)
		}
		if _, ok := headers["x-empty"]; ok {
			t.Fatalf("空值头必须被跳过：%#v", headers)
		}
	})

	t.Run("失败体读取取消ctx错误臂", func(t *testing.T) {
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err := readUpstreamFailureBody(canceled, &gatewaydispatch.GatewayUpstreamResponse{
			Body: io.NopCloser(strings.NewReader("body")),
		}, 1024)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消 ctx = %v，want context.Canceled", err)
		}
	})

	t.Run("非对象JSON失败体投影为nil", func(t *testing.T) {
		response := &gatewaydispatch.GatewayUpstreamResponse{
			Header: http.Header{"Content-Type": []string{"application/json"}},
		}
		if got := parsedFailureBodyOf(response, `"scalar"`); got != nil {
			t.Fatalf("标量 JSON 投影 = %#v，want nil", got)
		}
	})

	t.Run("本地抑制预检直通首次降级", func(t *testing.T) {
		accounts := []gatewaydispatch.AccountCandidate{{ID: "acc-w1q"}}
		result, handled, err := (&disabledSuppression{}).ResolveLocalSuppressionFilter(ctx,
			gatewaydispatch.LocalSuppressionPreflightInput{Accounts: accounts})
		if err != nil || handled {
			t.Fatalf("本地预检 = (%v, %v)，want (false, nil)", handled, err)
		}
		if len(result.Accounts) != 1 || result.Accounts[0].ID != "acc-w1q" {
			t.Fatalf("直通结果 = %#v", result.Accounts)
		}
	})

	t.Run("链身份请求投影Path与Header", func(t *testing.T) {
		// 相对 target：httptest 会把 RequestURI 原样设为 target（Node
		// originalUrl 契约），URL.Path 提供 Path() 投影。
		raw := httptest.NewRequest(http.MethodPost, "/v1/chat?trace=w1q", nil)
		raw.Header.Set("X-Session-Id", "sess-w1q")
		request := chainIdentityRequest{req: &gatewaypreauth.GatewayRequest{HTTP: raw}}
		if request.Path() != "/v1/chat" {
			t.Fatalf("Path = %q，want /v1/chat", request.Path())
		}
		if request.OriginalURL() != "/v1/chat?trace=w1q" {
			t.Fatalf("OriginalURL = %q，want /v1/chat?trace=w1q", request.OriginalURL())
		}
		if values := request.HeaderValues("X-Session-Id"); len(values) != 1 || values[0] != "sess-w1q" {
			t.Fatalf("HeaderValues = %v", values)
		}
	})
}

// ---------------------------------------------------------------------------
// chain_ports.go：失败派发器策略错误臂
// ---------------------------------------------------------------------------

func TestW1QChainFailureDispatcherPolicyArms(t *testing.T) {
	t.Run("策略覆盖凭据非法Decide错误臂", func(t *testing.T) {
		response := failureDispatchUpstreamResponse(t, http.StatusPaymentRequired,
			"application/json", `{"error":{"message":"quota exceeded"}}`)
		dispatcher := &chainFailureDispatcher{affinity: &failureDispatchAffinity{}}
		input := gatewayFailedResponseInput(response, &failureDispatchAuditSink{}, "gateway")
		input.Account = errorPolicyAccount(map[string]any{"error_handling_rule_overrides": "w1q-bad-overrides"})
		result, err := dispatcher.HandleFailedUpstreamResponse(context.Background(), input)
		if err == nil {
			t.Fatalf("非法覆盖凭据必须报错，实际 action=%s", result.Action)
		}
		if !strings.Contains(err.Error(), "错误处理策略覆盖格式无效") {
			t.Fatalf("错误 = %v，want 包含 覆盖格式无效", err)
		}
	})

	t.Run("显式策略状态写侧失败上抛臂", func(t *testing.T) {
		response := failureDispatchUpstreamResponse(t, http.StatusInternalServerError,
			"application/json", `{"error":{"message":"boom"}}`)
		dispatcher := &chainFailureDispatcher{
			affinity: &failureDispatchAffinity{},
			effects:  w1qFailingPolicyEffects{},
		}
		input := gatewayFailedResponseInput(response, &failureDispatchAuditSink{}, "gateway")
		input.AccountStateMutationEnabled = true
		input.Account = errorPolicyAccount(map[string]any{
			"error_handling_rules": []any{map[string]any{
				"enabled": true, "name": "w1q禁用", "priority": float64(1),
				"action": "error_disabled", "status_codes": []any{float64(500)},
			}},
		})
		if _, err := dispatcher.HandleFailedUpstreamResponse(context.Background(), input); err == nil {
			t.Fatal("效果写侧失败必须上抛")
		} else if !strings.Contains(err.Error(), "w1q 策略状态写入失败") {
			t.Fatalf("错误 = %v，want 保留写侧原始错误", err)
		}
	})
}

// ---------------------------------------------------------------------------
// compose_authz_grant_returner.go：Return 桥错误臂与状态投影臂
// ---------------------------------------------------------------------------

func TestW1QAuthzGrantReturnerArms(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC) }

	t.Run("授权grant表缺失错误臂", func(t *testing.T) {
		db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "w1q-return-missing.sqlite3"))
		if err != nil {
			t.Fatalf("打开临时库: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		store, err := authz.NewStore(db, false, now)
		if err != nil {
			t.Fatalf("构造 authz store: %v", err)
		}
		returner := authzGrantReturner{store: store}
		if _, err := returner.Return(context.Background(), "grant-w1q", "", "user-w1q"); err == nil {
			t.Fatal("缺授权表时 Return 必须报错")
		}
	})

	t.Run("grant缺失not_found状态投影臂", func(t *testing.T) {
		db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "w1q-return-empty.sqlite3"))
		if err != nil {
			t.Fatalf("打开临时库: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if _, err := db.Exec(`CREATE TABLE resource_authorization_grants (
			id TEXT PRIMARY KEY, resource_type TEXT, resource_id TEXT,
			resource_owner_system_account_id TEXT, grantee_type TEXT,
			grantee_system_account_id TEXT, grantee_team_id TEXT, status TEXT,
			remark TEXT, expires_at TEXT, limits_json TEXT, created_by TEXT,
			revoked_by TEXT, revoked_at TEXT, created_at TEXT, updated_at TEXT)`); err != nil {
			t.Fatalf("建 grants 表: %v", err)
		}
		store, err := authz.NewStore(db, false, now)
		if err != nil {
			t.Fatalf("构造 authz store: %v", err)
		}
		status, err := (authzGrantReturner{store: store}).Return(context.Background(), "grant-w1q", "", "user-w1q")
		if err != nil {
			t.Fatalf("缺失 grant 不得报错: %v", err)
		}
		if status != "not_found" {
			t.Fatalf("状态 = %q，want not_found", status)
		}
	})
}

// ---------------------------------------------------------------------------
// runtime.go：loadRuntimeConfig 深臂表驱动
// ---------------------------------------------------------------------------

func TestW1QLoadRuntimeConfigDeepArms(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "w1q-runtime.sqlite3")
	env := func(extra map[string]string) func(string) string {
		return func(key string) string {
			if key == "JUHE_AI_DATABASE_PATH" {
				return databasePath
			}
			return extra[key]
		}
	}

	errorCases := []struct {
		name  string
		extra map[string]string
		want  string
	}{
		{"运行态驱动非法", map[string]string{"JUHE_AI_RUNTIME_STATE_DRIVER": "grid"}, "JUHE_AI_RUNTIME_STATE_DRIVER 必须为 memory 或 redis"},
		{"postgres驱动缺URL", map[string]string{"JUHE_AI_DATABASE_DRIVER": "postgres"}, "JUHE_AI_DATABASE_DRIVER=postgres 时缺少 JUHE_AI_POSTGRES_URL"},
		{"codex分片数下界", map[string]string{"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT": "0"}, "JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT 必须在 1 到 256 之间"},
		{"会话轮数下界", map[string]string{"JUHE_AI_CHAT_MAX_TURNS_PER_CONVERSATION": "0"}, "JUHE_AI_CHAT_MAX_TURNS_PER_CONVERSATION 必须在 1 到 1000 之间"},
		{"会话保留天下界", map[string]string{"JUHE_AI_CHAT_RETENTION_DAYS": "0"}, "JUHE_AI_CHAT_RETENTION_DAYS 必须在 1 到 365 之间"},
		{"聊天工具环境非法", map[string]string{"JUHE_AI_NODE_ENV": "staging"}, "必须是 production、test 或 development"},
		{"cookie同站非法", map[string]string{"JUHE_AI_COOKIE_SAME_SITE": "auto"}, "JUHE_AI_COOKIE_SAME_SITE 必须为 lax、strict 或 none"},
		{"信任代理非法", map[string]string{"JUHE_AI_TRUST_PROXY": "maybe"}, "JUHE_AI_TRUST_PROXY 只能配置为 true/false 或 0-16"},
		{"临时访问白名单非法", map[string]string{"JUHE_AI_TEMPORARY_ACCESS_IP_ALLOWLIST": "example.com"}, "只能填写逗号分隔的单个 IPv4 或 IPv6 地址"},
		{"OIDC缺加密密钥", map[string]string{"JUHE_AI_OIDC_ENABLED": "true", "JUHE_AI_OIDC_ISSUER": "https://issuer.example"}, "必须显式配置 JUHE_AI_OIDC_KEY_ENCRYPTION_SECRET"},
		// 2026-09-19 起 system-api 未配置默认开启，联动违规必须显式关闭。
		{"链条缺系统API开关", map[string]string{"JUHE_AI_GATEWAY_CHAIN_ENABLED": "true", "JUHE_AI_GATEWAY_SYSTEM_API_ENABLED": "false"}, "必须同时启用 JUHE_AI_GATEWAY_SYSTEM_API_ENABLED"},
		{"候选上限非整数", map[string]string{"JUHE_AI_GATEWAY_DISPATCH_ACCOUNT_CANDIDATE_LIMIT": "abc"}, "JUHE_AI_GATEWAY_DISPATCH_ACCOUNT_CANDIDATE_LIMIT 必须配置为整数"},
		{"候选上限越界", map[string]string{"JUHE_AI_GATEWAY_DISPATCH_ACCOUNT_CANDIDATE_LIMIT": "0"}, "JUHE_AI_GATEWAY_DISPATCH_ACCOUNT_CANDIDATE_LIMIT 必须在 1-50000 范围内"},
		{"Go运行时指标store非法", map[string]string{"JUHE_AI_GO_RUNTIME_METRICS_STORE": "memory"}, "JUHE_AI_GO_RUNTIME_METRICS_STORE 必须为 sqlite 或 postgres"},
	}
	for _, tc := range errorCases {
		t.Run("错误_"+tc.name, func(t *testing.T) {
			if _, err := loadRuntimeConfig(env(tc.extra)); err == nil {
				t.Fatalf("%s 必须启动失败", tc.name)
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误 = %v，want 包含 %q", err, tc.want)
			}
		})
	}

	t.Run("解析成功臂", func(t *testing.T) {
		cfg, err := loadRuntimeConfig(env(map[string]string{
			"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT":          "7",
			"JUHE_AI_CHAT_MAX_TURNS_PER_CONVERSATION":          "80",
			"JUHE_AI_CHAT_RETENTION_DAYS":                      "30",
			"JUHE_AI_AUDIT_LOG_ENABLED":                        "false",
			"JUHE_AI_GATEWAY_DISPATCH_ACCOUNT_CANDIDATE_LIMIT": "12345",
			"JUHE_AI_LOG_FILE_ENABLED":                         "false",
		}))
		if err != nil {
			t.Fatalf("合法覆盖必须通过: %v", err)
		}
		if cfg.CodexContextShardCount != 7 {
			t.Fatalf("CodexContextShardCount = %d，want 7", cfg.CodexContextShardCount)
		}
		if cfg.ChatMaxTurnsPerConversation != 80 {
			t.Fatalf("ChatMaxTurnsPerConversation = %d，want 80", cfg.ChatMaxTurnsPerConversation)
		}
		if cfg.ChatRetentionDays != 30 {
			t.Fatalf("ChatRetentionDays = %d，want 30", cfg.ChatRetentionDays)
		}
		if cfg.AuditLogEnabled {
			t.Fatal("AuditLogEnabled = true，want false（显式关闭）")
		}
		if cfg.DispatchAccountCandidateLimit != 12345 {
			t.Fatalf("DispatchAccountCandidateLimit = %d，want 12345", cfg.DispatchAccountCandidateLimit)
		}
		if cfg.LogFileEnabled {
			t.Fatal("LogFileEnabled = true，want false（显式关闭）")
		}
	})
}
