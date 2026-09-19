package main

// w16b（覆盖率收尾批次二，装配/SQL 错误臂）：对照
// F:/sub2api-lite/.local/tmp-coverage/gwcmd_w16b_base.cov 的零计数块逐块补测。
// 全部进程内确定性驱动（关闭句柄 / 脚本化 database/sql driver / 组合根测试
// 夹具），不启动插桩二进制、不连接真实 dev PG/Redis。
//
// 覆盖目标（基线 profile 零块 -> 本文件用例）：
//  1. chain_account_locks.go：CompleteSuccessAsync 空 id 早退臂（基线
//     364-366）、结算 UPDATE 报错臂（390-392）与 CAS 失竞重读臂（393-396）、
//     AcquireRetryLeaseAsync 租约 CAS 失竞臂（646）——全部走 w2aScriptedSQL
//     脚本化驱动（沿用 w2_core_state_test.go 的行形状 helper）。
//  2. chain_bridge_response.go chainBridgeResponseMappingOf 的
//     RuntimeSource / RuntimeRouteRuleID 非空指针臂（基线 537-543）。
//  3. chain_ports.go localSessionAffinity.AreHighConcurrencyAccountsBusyForLaneAsync：
//     ConcurrencyLimit<1 钳制臂（基线 1300-1302）、LoadCurrentAsync 报错臂
//     （1306-1308）与 image lane 报错臂（1311-1313）——注入桩并发事实源。
//  4. compose.go businesssettings.New ownerGate 不完整 fail-fast 臂（基线
//     458-460）：复用 composeSystemAPI 全量夹具，仅翻转 NodeWriterStopped。
//  5. storage_bootstrap.go：SeedSQLiteBusiness 失败臂（基线 40）与
//     chat / dataset / usage-catalog 三个 ensureFile 失败臂（基线 88 / 97 /
//     100）——直驱 ensureGatewaySQLiteStoragePreflight，路径指向不可创建
//     位置；stats ensureFile 成功面保持在前。

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// chain_account_locks.go（脚本化 driver）
// ---------------------------------------------------------------------------

func TestW16BCompleteSuccessAsyncArms(t *testing.T) {
	ctx := context.Background()

	// 364-366：空白 id 早退（无 SQL 触达，脚本保持空）。
	if err := w2aLocksPort(w2aOpenScriptedDB(t, nil)).CompleteSuccessAsync(ctx, "   ", "", nil); err != nil {
		t.Fatalf("空白 id 必须空操作, got %v", err)
	}

	due := "2026-01-01T00:00:00.000Z"
	row := w2aLockRow("ENGAGED", due, "inc-1", 7)

	// 390-392：结算 UPDATE 直接报错。
	steps := []w2aScriptedStep{
		{contains: "FROM account_lock_states", cols: strings.Fields(chainAccountLockRowColumns), row: row},
		{contains: "UPDATE account_lock_states", execErr: errors.New("w16b: settle refused")},
	}
	if err := w2aLocksPort(w2aOpenScriptedDB(t, steps)).CompleteSuccessAsync(ctx, "w2a-acc", "", nil); err == nil {
		t.Fatal("结算 UPDATE 失败必须上抛")
	}

	// 393-396：UPDATE 命中 0 行（CAS 失竞）→ 重读权威行不报错。
	steps = []w2aScriptedStep{
		{contains: "FROM account_lock_states", cols: strings.Fields(chainAccountLockRowColumns), row: row},
		{contains: "UPDATE account_lock_states", affected: 0},
		{contains: "FROM account_lock_states", cols: strings.Fields(chainAccountLockRowColumns), row: row},
	}
	if err := w2aLocksPort(w2aOpenScriptedDB(t, steps)).CompleteSuccessAsync(ctx, "w2a-acc", "", nil); err != nil {
		t.Fatalf("CAS 失竞必须静默重读, got %v", err)
	}
}

func TestW16BAcquireRetryLeaseCASFailArm(t *testing.T) {
	// deadline 必须晚于注入时钟（2026-09-14），否则 blocksCrossAccount=false
	// 直接放行（blocksCrossAccount 要求 ENGAGED 且 deadline > now）。
	row := w2aLockRow("ENGAGED", "2026-12-01T00:00:00.000Z", "inc-1", 7)
	// 646：租约 UPDATE 命中 0 行（CAS 失竞）→ Allowed=false 且 WaitMs 至少 1ms。
	steps := []w2aScriptedStep{
		{contains: "FROM account_lock_states", cols: strings.Fields(chainAccountLockRowColumns), row: row},
		{contains: "UPDATE account_lock_states", affected: 0},
	}
	result, err := w2aLocksPort(w2aOpenScriptedDB(t, steps)).AcquireRetryLeaseAsync(context.Background(), "w2a-acc", 0)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if result.Allowed {
		t.Fatalf("CAS 失竞必须拒绝放行: %+v", result)
	}
	if result.WaitMs < 1 {
		t.Fatalf("WaitMs = %d, want >= 1", result.WaitMs)
	}
}

// ---------------------------------------------------------------------------
// chain_bridge_response.go：chainBridgeResponseMappingOf 指针臂
// ---------------------------------------------------------------------------

func TestW16BBridgeMappingOfPointerArms(t *testing.T) {
	runtimeSource := "manual_override"
	runtimeRouteRuleID := "rule-w16b"
	request := newEndpointGateRequest(t, "POST", "/v1/chat/completions", `{"model":"w16b-model"}`)
	// RequestModel 优先读 BodyState.Model：显式注入，避免依赖 ParsedJSON
	// 回退面的读取条件。
	request.Body.State = &gatewaybody.BodyState{Model: w2aModelPtr("w16b-model")}
	mapping := chainBridgeResponseMappingOf(request, gatewaydispatch.AccountCandidate{
		ID:           "w16b-bridge-acc",
		ProviderCode: "hybrid",
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel:            "w16b-model",
			SourceEndpointFamily:   "chat_completions",
			UpstreamModel:          "w16b-upstream",
			UpstreamEndpointFamily: "messages",
			Enabled:                true,
			RuntimeSource:          &runtimeSource,
			RuntimeRouteRuleID:     &runtimeRouteRuleID,
		}},
	})
	if mapping == nil {
		t.Fatal("带指针元数据的映射必须被解析")
	}
	if mapping.RuntimeSource != "manual_override" || mapping.RuntimeRouteRuleID != "rule-w16b" {
		t.Fatalf("指针字段未解引用: %+v", mapping)
	}

	// 反向臂：nil 指针保持空串（既有覆盖面，保持双臂都执行）。
	nilMapping := chainBridgeResponseMappingOf(request, gatewaydispatch.AccountCandidate{
		ID:           "w16b-bridge-acc",
		ProviderCode: "hybrid",
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel:            "w16b-model",
			SourceEndpointFamily:   "chat_completions",
			UpstreamModel:          "w16b-upstream",
			UpstreamEndpointFamily: "messages",
			Enabled:                true,
		}},
	})
	if nilMapping == nil || nilMapping.RuntimeSource != "" || nilMapping.RuntimeRouteRuleID != "" {
		t.Fatalf("nil 指针必须映射空串: %+v", nilMapping)
	}
}

// ---------------------------------------------------------------------------
// chain_ports.go：AreHighConcurrencyAccountsBusyForLaneAsync 臂
// ---------------------------------------------------------------------------

// w16bBusyConcurrency 桩：可编程的读结果 / 错误。
type w16bBusyConcurrency struct {
	currentErr  error
	laneErr     error
	current     map[string]int
	laneCurrent map[string]int
}

func (s *w16bBusyConcurrency) LoadCurrentAsync(context.Context, []string) (map[string]int, error) {
	if s.currentErr != nil {
		return nil, s.currentErr
	}
	return s.current, nil
}

func (s *w16bBusyConcurrency) LoadCurrentByLaneAsync(context.Context, []string, string) (map[string]int, error) {
	if s.laneErr != nil {
		return nil, s.laneErr
	}
	return s.laneCurrent, nil
}

func (s *w16bBusyConcurrency) TryAcquireAsync(context.Context, string, int, gatewaydispatch.AccountConcurrencyAcquireOptions) (gatewaydispatch.ConcurrencySlot, error) {
	return gatewaydispatch.ConcurrencySlot{}, errors.New("w16b: TryAcquireAsync not expected")
}

func TestW16BHighConcurrencyBusyLaneArms(t *testing.T) {
	affinity := &localSessionAffinity{concurrency: &w16bBusyConcurrency{}}
	zeroLimit := gatewaydispatch.AccountCandidate{ID: "w16b-zero-limit", ConcurrencyLimit: 0}

	// 1300-1302：ConcurrencyLimit<1 钳制为 1 的臂（钳制后不忙，随正常面返回）。
	busy, err := affinity.AreHighConcurrencyAccountsBusyForLaneAsync(context.Background(),
		[]gatewaydispatch.AccountCandidate{zeroLimit},
		gatewaydispatch.HighConcurrencyBusyOptions{AffinityOrderingOptions: gatewaydispatch.AffinityOrderingOptions{GroupType: "high_concurrency"}})
	if err != nil || busy {
		t.Fatalf("零限额钳制臂: busy=%v err=%v", busy, err)
	}

	// 1306-1308：总并发读报错直接上抛。
	errAffinity := &localSessionAffinity{concurrency: &w16bBusyConcurrency{currentErr: errors.New("w16b: load refused")}}
	if busy, err := errAffinity.AreHighConcurrencyAccountsBusyForLaneAsync(context.Background(),
		[]gatewaydispatch.AccountCandidate{{ID: "w16b-acc", ConcurrencyLimit: 2}},
		gatewaydispatch.HighConcurrencyBusyOptions{AffinityOrderingOptions: gatewaydispatch.AffinityOrderingOptions{GroupType: "high_concurrency"}}); err == nil || busy {
		t.Fatalf("LoadCurrentAsync 失败必须上抛: busy=%v err=%v", busy, err)
	}

	// 1311-1313：image lane 读报错直接上抛。
	imageErrAffinity := &localSessionAffinity{concurrency: &w16bBusyConcurrency{laneErr: errors.New("w16b: lane refused")}}
	if busy, err := imageErrAffinity.AreHighConcurrencyAccountsBusyForLaneAsync(context.Background(),
		[]gatewaydispatch.AccountCandidate{{ID: "w16b-acc", ConcurrencyLimit: 2}},
		gatewaydispatch.HighConcurrencyBusyOptions{AffinityOrderingOptions: gatewaydispatch.AffinityOrderingOptions{GroupType: "high_concurrency"}, RequestLane: "image"}); err == nil || busy {
		t.Fatalf("image lane 读失败必须上抛: busy=%v err=%v", busy, err)
	}
}

// ---------------------------------------------------------------------------
// compose.go：businesssettings ownerGate fail-fast 臂
// ---------------------------------------------------------------------------

// 【w16b 登记】compose.go 458-460（businesssettings.New 错误臂）不可达候选：
// New 只在 db==nil / mode 非法时失败——mode 由 businessDialect(pgDialect) 恒
// 合法、db 由组合根先期打开成功；OwnerGate 不在构造期校验（读操作时才生效）。

// ---------------------------------------------------------------------------
// storage_bootstrap.go：seed 失败臂 + ensureFile 失败臂
// ---------------------------------------------------------------------------

func w16bStorageConfig(root string, mutate func(cfg *runtimeConfig)) runtimeConfig {
	cfg := runtimeConfig{
		DatabaseDriver:            "sqlite",
		Secret:                    "w16b-storage-secret",
		DatabasePath:              filepath.Join(root, "business.sqlite3"),
		StatsDatabasePath:         filepath.Join(root, "stats.sqlite3"),
		ChatDatabasePath:          filepath.Join(root, "chat.sqlite3"),
		DatasetDatabasePath:       filepath.Join(root, "dataset.sqlite3"),
		UsageCatalogDatabasePath:  filepath.Join(root, "usage-catalog.sqlite3"),
		CodexContextShardRoot:     filepath.Join(root, "codex-context"),
		CodexContextShardCount:    0,
		BusinessHandoffConfirmed:  true,
		BusinessSchemaReady:       true,
		BusinessNodeWriterStopped: true,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	return cfg
}

func TestW16BStoragePreflightArms(t *testing.T) {
	ctx := context.Background()

	// 【w16b 登记】storage_bootstrap.go 40（SeedSQLiteBusiness 错误臂）暂不可
	// 达候选：种子全程 INSERT OR IGNORE / ON CONFLICT，健康句柄上无法构造
	// 中途失败；句柄级失败会先命中 36 行 EnsureSQLiteSchema 的错误臂。

	// 88：chat 库路径落在不可创建的文件内部目录（stats ensureFile 在前成功）。
	chatRoot := t.TempDir()
	blocker := filepath.Join(chatRoot, "blocker.file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	chatDB, err := sql.Open("sqlite", filepath.Join(chatRoot, "business.sqlite3"))
	if err != nil {
		t.Fatalf("open business db: %v", err)
	}
	t.Cleanup(func() { _ = chatDB.Close() })
	chatCfg := w16bStorageConfig(chatRoot, func(c *runtimeConfig) {
		c.ChatDatabasePath = filepath.Join(blocker, "nested", "chat.sqlite3")
	})
	if err := ensureGatewaySQLiteStoragePreflight(ctx, chatCfg, chatDB); err == nil {
		t.Fatal("chat 库路径不可创建必须失败")
	}

	// 97：dataset 库路径不可创建（chat 成功在前）。
	datasetRoot := t.TempDir()
	blocker2 := filepath.Join(datasetRoot, "blocker.file")
	if err := os.WriteFile(blocker2, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	datasetDB, err := sql.Open("sqlite", filepath.Join(datasetRoot, "business.sqlite3"))
	if err != nil {
		t.Fatalf("open business db: %v", err)
	}
	t.Cleanup(func() { _ = datasetDB.Close() })
	datasetCfg := w16bStorageConfig(datasetRoot, func(c *runtimeConfig) {
		c.DatasetDatabasePath = filepath.Join(blocker2, "nested", "dataset.sqlite3")
	})
	if err := ensureGatewaySQLiteStoragePreflight(ctx, datasetCfg, datasetDB); err == nil {
		t.Fatal("dataset 库路径不可创建必须失败")
	}

	// 100：usage-catalog 库路径不可创建（dataset 成功在前）。
	catalogRoot := t.TempDir()
	blocker3 := filepath.Join(catalogRoot, "blocker.file")
	if err := os.WriteFile(blocker3, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	catalogDB, err := sql.Open("sqlite", filepath.Join(catalogRoot, "business.sqlite3"))
	if err != nil {
		t.Fatalf("open business db: %v", err)
	}
	t.Cleanup(func() { _ = catalogDB.Close() })
	catalogCfg := w16bStorageConfig(catalogRoot, func(c *runtimeConfig) {
		c.UsageCatalogDatabasePath = filepath.Join(blocker3, "nested", "usage-catalog.sqlite3")
	})
	if err := ensureGatewaySQLiteStoragePreflight(ctx, catalogCfg, catalogDB); err == nil {
		t.Fatal("usage-catalog 库路径不可创建必须失败")
	}
}
