package main

// w1_compose_arms2_test.go —— compose.go 深臂与 main.go 助手的补充单元测试
// （w1m 前缀，TestW1M 入口）。
//
// 与既有覆盖的边界（不重复）：
//   - composeSystemAPI 的 nil 依赖臂、SQLite 命名路径失败臂、驱动轴成功变体、
//     ratelimitSettingsProvider 真实仓 seam、openSQLiteReadOnly、
//     authsys/accounts 总线适配器由 w1_compose_root_test.go（w1o）覆盖；
//   - preflight 先行失败、表监控 configure 失败、postgres 缺 URL、验证码/
//     登录守卫驱动臂、Go 运行时指标打开失败、链条 redis 成功变体由
//     w1_compose_deep_test.go（w1q）覆盖。
//
// 本文件新增的可驱动臂：
//   B1 composeSystemAPI：链条开启后 composeChatFamily 的 chat 资产根目录
//      失败臂（compose.go "compose my-chat family" 包装层）。
//   B2 composeSystemAPI：空白 JUHE_AI_SECRET 命中 apikeys.NewStore 校验失败
//      臂（compose.go "create api-key store" 包装层）。
//   B3 aiAccountLimitSettingsAdapter.UserAiAccountLimit：float64 分支与
//      Load 错误透传分支（int64/int/default 分支不可达，见函数注释）。
//   C1 main.go listenLoopback：合法监听返回与端口解析/范围/非环回地址臂。
//   C2 main.go passiveGatewayHealthHandler：GET /health 契约与 404 臂。
//   C3 main.go loadSessionRetentionConfig：nil getenv 回退臂。
//   C4 main.go runSessionRetention：无效配置臂、首次 cleanup 错误臂、
//      tick 轮次 cleanup 错误臂。
//
// 已确认不可达、不在本文件驱动的臂（证据见各分支源码）：
//   - compose.go ratelimitSettingsProvider 缺键/非 float64/越界防御臂与
//     六个逐键错误返回：settings.Store.Load 的 json.Unmarshal 恒产出
//     float64，normalizeSystemSetting 在读取侧前置保证整值且在界内，缺行由
//     assertAllSettingsPresent 报错——经真实仓 seam 无法构造非 float64 快照
//     （w1o 阶段一/四已覆盖 Load 错误透传臂）。
//   - compose.go newCompositionID 的 crypto/rand 失败回退、PG 模式全部
//     分支（无真实 PG，禁止连接真实环境）、preflight 已确保 schema 的
//     stats/usage-catalog/dataset 句柄二次打开错误臂、各 store 构造器的
//     错误返回（合法 SQLite + 合法配置下构造器不校验表结构）、
//     newAccountsRuntimeResetBridge / newChainErrorPolicyEffectsBridge
//     错误臂（accountkeystates 与 apikeys 的空白 secret 校验更早失败）、
//     newChainAccountLocks 错误臂（db 恒非 nil）、spool 缺失错误臂
//     （SQLite 模式由 StatsDatabasePath 派生，preflight 先行必填）、
//     gometrics EnsureReady/NewSampler 错误臂（合法库 + 非 nil 依赖下
//     不报错）、logreads 三个读取器构造错误臂（仅 nil db / 非法 mode）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/session_retention"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/settings"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// B1. compose.go：链条开启后 my-chat 家族的 chat 资产根目录失败臂
// ---------------------------------------------------------------------------

func TestW1MComposeChatFamilyChatAssetsFailureArm(t *testing.T) {
	stack := w1oNewComposeStack(t)
	w1oRedirectDatabasePaths(t, stack)
	stack.cfg.ChainEnabled = true
	// ChatAssetsRoot 指向一个已存在的文件：newChatAssetObjectStore 的
	// os.MkdirAll 必然失败，composeChatFamily 错误沿
	// "compose my-chat family" 包装层返回（compose.go 1173-1175）。
	assetsBlocker := filepath.Join(filepath.Dir(stack.cfg.BusinessDatabasePath), "w1m-assets-blocker")
	if err := os.WriteFile(assetsBlocker, []byte("not-a-dir"), 0o644); err != nil {
		t.Fatalf("写入 chat 资产根目录占位文件失败: %v", err)
	}
	stack.cfg.ChatAssetsRoot = assetsBlocker
	composed, err := stack.compose(t)
	if err == nil {
		if composed != nil {
			composed.Shutdown()
		}
		t.Fatal("chat 资产根目录指向文件时必须 fail-fast，实际组装成功")
	}
	if composed != nil {
		t.Fatalf("失败臂不得返回组合根，实际 %#v", composed)
	}
	if !strings.Contains(err.Error(), "compose my-chat family") {
		t.Fatalf("错误 = %v，want 包含 compose my-chat family", err)
	}
}

// ---------------------------------------------------------------------------
// B2. compose.go：空白 JUHE_AI_SECRET 命中 apikeys.NewStore 校验失败臂
//（空白 secret 在 preflight seed 中合法（回落 Node 默认值），在
// apikeys.NewStore 的 TrimSpace 校验处才失败，因此能精确命中 compose.go
// "create api-key store" 包装层，而不被更早的臂拦截。）
// ---------------------------------------------------------------------------

func TestW1MComposeApiKeyStoreBlankSecretArm(t *testing.T) {
	stack := w1oNewComposeStack(t)
	w1oRedirectDatabasePaths(t, stack)
	stack.cfg.Secret = "   "
	composed, err := stack.compose(t)
	if err == nil {
		if composed != nil {
			composed.Shutdown()
		}
		t.Fatal("空白 JUHE_AI_SECRET 必须 fail-fast，实际组装成功")
	}
	if composed != nil {
		t.Fatalf("失败臂不得返回组合根，实际 %#v", composed)
	}
	if !strings.Contains(err.Error(), "create api-key store") {
		t.Fatalf("错误 = %v，want 包含 create api-key store", err)
	}
}

// ---------------------------------------------------------------------------
// B3. compose.go：aiAccountLimitSettingsAdapter.UserAiAccountLimit
//（int64/int 分支不可达：settings.Store.Load 经 json.Unmarshal 只产出
// float64/string/bool/nil，normalizeSystemSetting 对整值键前置强制
// float64 且在界内，否则 Load 直接报错；default 分支不可达：
// assertAllSettingsPresent 保证键恒存在。）
// ---------------------------------------------------------------------------

func TestW1MUserAiAccountLimitArms(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "w1m-account-limit-settings.sqlite3"))
	if err != nil {
		t.Fatalf("打开临时库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := bootstrap.EnsureSQLiteSchema(context.Background(), bootstrap.SQLiteSchemaBusiness, db); err != nil {
		t.Fatalf("ensure 业务 schema: %v", err)
	}
	if _, err := bootstrap.SeedSQLiteBusiness(context.Background(), db, bootstrap.SeedOptions{Secret: "w1m-limit-secret"}); err != nil {
		t.Fatalf("seed 业务库: %v", err)
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
	adapter := aiAccountLimitSettingsAdapter{settings: store}
	ctx := context.Background()

	// float64 分支：种子默认值 100（float64）→ int64(100)。
	limit, err := adapter.UserAiAccountLimit(ctx)
	if err != nil {
		t.Fatalf("种子默认上限读取: %v", err)
	}
	if limit != 100 {
		t.Fatalf("种子默认 userAiAccountLimit = %d，want 100", limit)
	}

	// float64 分支（显式更新值）：Update 后时钟步进越过缓存 TTL。
	if _, err := store.Update(ctx, map[string]any{"userAiAccountLimit": float64(7)}); err != nil {
		t.Fatalf("Update userAiAccountLimit: %v", err)
	}
	limit, err = adapter.UserAiAccountLimit(ctx)
	if err != nil {
		t.Fatalf("显式上限读取: %v", err)
	}
	if limit != 7 {
		t.Fatalf("显式 userAiAccountLimit = %d，want 7", limit)
	}

	// Load 错误透传分支：删除底层表后必须原样报错。
	if _, err := db.Exec("DROP TABLE system_settings"); err != nil {
		t.Fatalf("DROP system_settings: %v", err)
	}
	if _, err := adapter.UserAiAccountLimit(ctx); err == nil {
		t.Fatal("表损坏时 UserAiAccountLimit 必须报错")
	}
}

// ---------------------------------------------------------------------------
// C1. main.go：listenLoopback 全臂
// ---------------------------------------------------------------------------

func TestW1MListenLoopbackArms(t *testing.T) {
	grabFreePort := func() int {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("挑选空闲端口失败: %v", err)
		}
		defer func() { _ = listener.Close() }()
		return listener.Addr().(*net.TCPAddr).Port
	}
	t.Run("合法环回地址返回监听", func(t *testing.T) {
		listener, err := listenLoopback(fmt.Sprintf("127.0.0.1:%d", grabFreePort()))
		if err != nil {
			t.Fatalf("合法环回地址必须可监听: %v", err)
		}
		if err := listener.Close(); err != nil {
			t.Fatalf("关闭监听: %v", err)
		}
	})
	t.Run("localhost别名可监听", func(t *testing.T) {
		listener, err := listenLoopback(fmt.Sprintf("localhost:%d", grabFreePort()))
		if err != nil {
			t.Fatalf("localhost 别名必须可监听: %v", err)
		}
		if err := listener.Close(); err != nil {
			t.Fatalf("关闭监听: %v", err)
		}
	})
	t.Run("缺端口分隔符", func(t *testing.T) {
		if _, err := listenLoopback("bad-address"); err == nil {
			t.Fatal("缺端口地址必须报错")
		} else if !strings.Contains(err.Error(), "invalid loopback listen address") {
			t.Fatalf("错误 = %v，want 包含 invalid loopback listen address", err)
		}
	})
	t.Run("端口非数字与越界", func(t *testing.T) {
		for _, address := range []string{"127.0.0.1:not-a-port", "127.0.0.1:0", "127.0.0.1:99999"} {
			if _, err := listenLoopback(address); err == nil {
				t.Fatalf("地址 %s 必须报错", address)
			} else if !strings.Contains(err.Error(), "port must be between 1 and 65535") {
				t.Fatalf("地址 %s 错误 = %v，want 包含 port must be between 1 and 65535", address, err)
			}
		}
	})
	t.Run("非环回主机", func(t *testing.T) {
		if _, err := listenLoopback("10.1.2.3:8080"); err == nil {
			t.Fatal("非环回主机必须报错")
		} else if !strings.Contains(err.Error(), "host must be localhost or a loopback IP") {
			t.Fatalf("错误 = %v，want 包含 host must be localhost or a loopback IP", err)
		}
	})
}

// ---------------------------------------------------------------------------
// C2. main.go：passiveGatewayHealthHandler 契约与 404 臂
// ---------------------------------------------------------------------------

func TestW1MPassiveGatewayHealthHandlerArms(t *testing.T) {
	handler := passiveGatewayHealthHandler(ownermode.Mode("standby"))

	t.Run("GET健康路径返回降级契约", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET /health 状态码 = %d，want 200", recorder.Code)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(recorder.Body.String()), &payload); err != nil {
			t.Fatalf("GET /health 响应不是合法 JSON: %v", err)
		}
		if ready, ok := payload["ready"].(bool); !ok || ready {
			t.Fatalf("被动网关 ready = %v，want false", payload["ready"])
		}
	})

	t.Run("POST与错误路径返回404", func(t *testing.T) {
		for _, request := range []*http.Request{
			httptest.NewRequest(http.MethodPost, "/health", nil),
			httptest.NewRequest(http.MethodGet, "/other", nil),
		} {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("%s %s 状态码 = %d，want 404", request.Method, request.URL.Path, recorder.Code)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// C3. main.go：loadSessionRetentionConfig 的 nil getenv 回退臂
// ---------------------------------------------------------------------------

func TestW1MLoadSessionRetentionConfigNilGetenvArm(t *testing.T) {
	interval, limit, err := loadSessionRetentionConfig(nil)
	if err != nil {
		t.Fatalf("nil getenv 必须回退 os.Getenv 并返回默认值: %v", err)
	}
	if interval != 15*time.Minute {
		t.Fatalf("默认间隔 = %v，want 15m", interval)
	}
	if limit != 10000 {
		t.Fatalf("默认批量 = %d，want 10000", limit)
	}
}

// ---------------------------------------------------------------------------
// C4. main.go：runSessionRetention 错误臂
// ---------------------------------------------------------------------------

func TestW1MRunSessionRetentionArms(t *testing.T) {
	t.Run("无效配置", func(t *testing.T) {
		if err := runSessionRetention(context.Background(), nil, time.Minute, 10, nil); err == nil {
			t.Fatal("nil store 必须报错")
		} else if !strings.Contains(err.Error(), "configuration is invalid") {
			t.Fatalf("错误 = %v，want 包含 configuration is invalid", err)
		}
		if err := runSessionRetention(context.Background(), &sessionretention.Store{}, 0, 10, nil); err == nil {
			t.Fatal("零间隔必须报错")
		}
		if err := runSessionRetention(context.Background(), &sessionretention.Store{}, time.Minute, 0, nil); err == nil {
			t.Fatal("零批量必须报错")
		}
	})

	t.Run("首次cleanup错误立即返回", func(t *testing.T) {
		db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "w1m-retention-fail.sqlite3"))
		if err != nil {
			t.Fatalf("打开临时库: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		// owner gate 不满足：Cleanup 在 requireOwner 处失败，无需任何表。
		store, err := sessionretention.New(db, sessionretention.SQLite, "", sessionretention.OwnerGate{})
		if err != nil {
			t.Fatalf("sessionretention.New: %v", err)
		}
		ready := false
		err = runSessionRetention(context.Background(), store, time.Minute, 10, func() { ready = true })
		if err == nil {
			t.Fatal("owner gate 未就绪时首次 cleanup 必须报错")
		}
		if ready {
			t.Fatal("cleanup 失败后不得置 ready")
		}
		if !strings.Contains(err.Error(), "owner handoff gate is not satisfied") {
			t.Fatalf("错误 = %v，want 包含 owner handoff gate is not satisfied", err)
		}
	})

	t.Run("tick轮次cleanup错误返回", func(t *testing.T) {
		db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "w1m-retention-tick.sqlite3"))
		if err != nil {
			t.Fatalf("打开临时库: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if _, err := db.Exec(`CREATE TABLE system_sessions (
			id TEXT PRIMARY KEY,
			system_account_id TEXT,
			token_hash TEXT,
			expires_at TEXT,
			created_at TEXT,
			last_seen_at TEXT
		)`); err != nil {
			t.Fatalf("创建 system_sessions: %v", err)
		}
		store, err := sessionretention.New(db, sessionretention.SQLite, "", sessionretention.OwnerGate{
			Confirmed:         true,
			SchemaReady:       true,
			NodeWriterStopped: true,
		})
		if err != nil {
			t.Fatalf("sessionretention.New: %v", err)
		}
		ready := false
		// markReady 在首次 cleanup 成功后回调：此刻删除底层表，令下一个
		// tick 的 cleanup 必然失败。5ms tick + 3s ctx 上限保证有界等待。
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		err = runSessionRetention(ctx, store, 5*time.Millisecond, 10, func() {
			ready = true
			if _, dropErr := db.Exec("DROP TABLE system_sessions"); dropErr != nil {
				t.Errorf("DROP system_sessions: %v", dropErr)
			}
		})
		if !ready {
			t.Fatal("首次 cleanup 成功后必须置 ready")
		}
		if err == nil {
			t.Fatal("tick 轮次 cleanup 失败必须返回错误")
		}
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("错误 = %v，want cleanup 失败而非 ctx 超时", err)
		}
		if !strings.Contains(err.Error(), "cleanup expired system sessions") {
			t.Fatalf("错误 = %v，want 包含 cleanup expired system sessions", err)
		}
	})
}
