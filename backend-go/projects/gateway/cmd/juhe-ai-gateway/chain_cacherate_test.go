package main

// 期二（缓存率感知调度与用量缓存率展示设计 5.6/7/8.1）：cmd 接线回归——
// chainHotQualityPort 的缓存率注入/stale 门控/纯排序 ReorderOnly、缓存率
// 快照加载器组合根装配（时区源 + 统计库真读）与决策日志观察者的缓存率排
// 序块投影。runtime 用 standalone+memory 单例（与 w2c 既有测试同模式），
// SQL 用临时 SQLite 句柄，不连真实服务。

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycacherate"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
	_ "modernc.org/sqlite"
)

func newW2CHotQualityRuntime(t *testing.T) *gatewayhotquality.GatewayHotQualityRuntime {
	t.Helper()
	gatewayhotquality.ResetGatewayHotQualityRuntimeForTest()
	t.Cleanup(gatewayhotquality.ResetGatewayHotQualityRuntimeForTest)
	runtime, err := gatewayhotquality.GetGatewayHotQualityRuntime(context.Background(), gatewayhotquality.RuntimeDriverConfig{
		RuntimeMode:        "standalone",
		RuntimeStateDriver: "memory",
	})
	if err != nil {
		t.Fatalf("create hot quality runtime: %v", err)
	}
	return runtime
}

func w2cCacheRateAccounts() []gatewaydispatch.AccountCandidate {
	return []gatewaydispatch.AccountCandidate{
		{ID: "a1", ProviderProtocolProfileID: "profile", Priority: 1},
		{ID: "a2", ProviderProtocolProfileID: "profile", Priority: 1},
	}
}

func accountIDsOf(accounts []gatewaydispatch.AccountCandidate) []string {
	ids := make([]string, 0, len(accounts))
	for _, account := range accounts {
		ids = append(ids, account.ID)
	}
	return ids
}

func w2cOrderInput(accounts []gatewaydispatch.AccountCandidate) gatewaydispatch.HotQualityOrderInput {
	return gatewaydispatch.HotQualityOrderInput{
		Accounts:        accounts,
		Mode:            gatewaydispatch.HotQualityModeCostFirst,
		SystemAccountID: "sys",
		GroupID:         "grp",
		RequestLane:     "text",
		Model:           "gpt-4o",
		RequestID:       "req-cacherate",
	}
}

// ReorderOnly：纯排序 + 解释（阈值/候选明细），缓存率窗口经快照取数口注
// 入后真实参与层内排序——有有效样本的 a1 反超输入序靠前，档位/命中率进
// 解释；无样本候选保持 -1 哨兵。
func TestChainHotQualityPortReorderOnlyAppliesCacheRateOrdering(t *testing.T) {
	runtime := newW2CHotQualityRuntime(t)
	rates := map[string]gatewayhotquality.CacheRateWindow{
		"a2": {CacheReadTokens: 50_000, InputTokens: 100_000}, // rate 0.5 → 档位 5
	}
	port := chainHotQualityPort{runtime: runtime, cacheRates: func() (map[string]gatewayhotquality.CacheRateWindow, bool) {
		return rates, false
	}}
	// 输入序 a1 在前；a2 命中缓存样本后应反超（同层同为 cold/合格）。
	order, err := port.ReorderOnly(context.Background(), w2cOrderInput(w2cCacheRateAccounts()))
	if err != nil {
		t.Fatalf("ReorderOnly: %v", err)
	}
	if len(order.Accounts) != 2 || order.Accounts[0].ID != "a2" || order.Accounts[1].ID != "a1" {
		t.Fatalf("order = %#v, want a2（缓存样本）在前", accountIDsOf(order.Accounts))
	}
	if order.Explanation == nil {
		t.Fatal("ReorderOnly 必须返回排序决策解释")
	}
	explanation := order.Explanation
	if explanation.SpeedThresholdMs != gatewayhotquality.SpeedDominanceThresholdCostFirstMs {
		t.Fatalf("speedThresholdMs = %d", explanation.SpeedThresholdMs)
	}
	if !explanation.CacheRateEnabled {
		t.Fatal("存在有效样本时缓存档位键必须参与")
	}
	if explanation.CacheRateStale {
		t.Fatal("非 stale 快照不得透传 stale 标记")
	}
	if len(explanation.CandidateOrderDetails) != 2 {
		t.Fatalf("candidateOrderDetails = %#v", explanation.CandidateOrderDetails)
	}
	first, second := explanation.CandidateOrderDetails[0], explanation.CandidateOrderDetails[1]
	if first.AccountID != "a2" || first.CacheHitRate == nil || *first.CacheHitRate != 0.5 || first.CacheQuantum != 5 {
		t.Fatalf("candidate[0] = %#v, want a2 rate 0.5 quantum 5", first)
	}
	if second.AccountID != "a1" || second.CacheHitRate != nil || second.CacheQuantum != -1 {
		t.Fatalf("candidate[1] = %#v, want a1 无数据（nil rate / -1 哨兵）", second)
	}
	// 纯排序语义：无探索预留、无结算钩子。
	if order.ExplorationReservation != nil || order.SettleExplorationAfterDispatch != nil {
		t.Fatal("ReorderOnly 不得产生探索副作用")
	}
}

// 快照取数口未装配/stale 时：不喂 CacheRates（维度中性），stale 标记透传。
func TestChainHotQualityPortGatesStaleCacheRates(t *testing.T) {
	runtime := newW2CHotQualityRuntime(t)
	rates := map[string]gatewayhotquality.CacheRateWindow{
		"a2": {CacheReadTokens: 50_000, InputTokens: 100_000},
	}
	port := chainHotQualityPort{runtime: runtime, cacheRates: func() (map[string]gatewayhotquality.CacheRateWindow, bool) {
		return rates, true // stale
	}}
	order, err := port.OrderAsync(context.Background(), w2cOrderInput(w2cCacheRateAccounts()))
	if err != nil {
		t.Fatalf("OrderAsync: %v", err)
	}
	if order.Explanation == nil {
		t.Fatal("OrderAsync 必须携带重建的排序解释")
	}
	if !order.Explanation.CacheRateStale {
		t.Fatal("stale 标记必须透传到解释")
	}
	if order.Explanation.CacheRateEnabled {
		t.Fatal("stale 时不得喂 CacheRates（缓存档位键必须整体关闭）")
	}
	for _, detail := range order.Explanation.CandidateOrderDetails {
		if detail.CacheHitRate != nil || detail.CacheQuantum != -1 {
			t.Fatalf("stale 时候选不得携带缓存样本: %#v", detail)
		}
	}

	// 未装配取数口（组合测试零值端口）：与 stale 同语义（无数据 + 标记置位）。
	bare := chainHotQualityPort{runtime: runtime}
	order, err = bare.OrderAsync(context.Background(), w2cOrderInput(w2cCacheRateAccounts()))
	if err != nil {
		t.Fatalf("OrderAsync bare: %v", err)
	}
	if order.Explanation == nil || !order.Explanation.CacheRateStale || order.Explanation.CacheRateEnabled {
		t.Fatalf("nil 取数口应按无数据处理: %#v", order.Explanation)
	}
}

// 组合根装配：缓存率加载器真读统计库 + 业务库时区源（SQLite 双句柄最小
// DDL），Warmup 成功后 Snapshot 报有效快照；缺句柄 fail-fast。
func TestNewChainCacheRateSnapshotSourceWiring(t *testing.T) {
	t.Run("nil handles fail fast", func(t *testing.T) {
		if _, err := newChainCacheRateSnapshotSource(nil, &sql.DB{}, false); err == nil {
			t.Fatal("缺统计库句柄必须报错")
		}
		if _, err := newChainCacheRateSnapshotSource(&sql.DB{}, nil, false); err == nil {
			t.Fatal("缺业务库句柄必须报错")
		}
	})

	t.Run("warmup loads via wired timezone source", func(t *testing.T) {
		dir := t.TempDir()
		statsDB, err := sql.Open("sqlite", filepath.Join(dir, "stats.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = statsDB.Close() })
		statsDB.SetMaxOpenConns(1)
		businessDB, err := sql.Open("sqlite", filepath.Join(dir, "business.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = businessDB.Close() })
		for _, statement := range []string{
			// 统计库：契约 8.1 聚合的列（SQLite 裸表名分支）。
			`CREATE TABLE usage_stats_hourly (
				system_account_id text NOT NULL,
				scope_type text NOT NULL,
				scope_id text NOT NULL DEFAULT '',
				stat_hour text NOT NULL,
				input_tokens bigint NOT NULL DEFAULT 0,
				cache_read_tokens bigint NOT NULL DEFAULT 0
			)`,
			// 业务库：usageStatsTimezone 系统设置（statreads 时区源同源）。
			`CREATE TABLE system_settings (
				system_account_id text NOT NULL,
				key text NOT NULL,
				value_json text
			)`,
			`INSERT INTO system_settings VALUES ('sys_admin', 'usageStatsTimezone', '"UTC"')`,
		} {
			target := statsDB
			if strings.HasPrefix(statement, "CREATE TABLE system_settings") || strings.HasPrefix(statement, "INSERT INTO system_settings") {
				target = businessDB
			}
			if _, err := target.Exec(statement); err != nil {
				t.Fatalf("fixture exec: %v", err)
			}
		}
		source, err := newChainCacheRateSnapshotSource(statsDB, businessDB, false)
		if err != nil {
			t.Fatalf("create source: %v", err)
		}
		currentHour := time.Now().UTC().Format("2006-01-02T15")
		if _, err := statsDB.Exec(`INSERT INTO usage_stats_hourly
			(system_account_id, scope_type, scope_id, stat_hour, input_tokens, cache_read_tokens)
			VALUES ('global', 'account', 'acc-1', ?, 100000, 50000)`, currentHour); err != nil {
			t.Fatal(err)
		}
		source.Warmup(context.Background())
		rates, stale := source.Snapshot()
		if stale || len(rates) != 1 {
			t.Fatalf("rates = %v stale = %v, want acc-1 有效快照", rates, stale)
		}
		if rates["acc-1"].CacheReadTokens != 50000 || rates["acc-1"].InputTokens != 100000 {
			t.Fatalf("window = %#v", rates["acc-1"])
		}
		_ = gatewaycacherate.CacheRateSnapshotTTL // 常量引用守卫（契约出处）
	})
}

// 决策日志观察者：缓存率排序块投影（决策级 + 候选级），未排序时整体缺省。
func TestChainDispatchDecisionObserverCacheRateFields(t *testing.T) {
	rate := 0.5
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	observer := newChainDispatchDecisionObserver(logger)
	observer(gatewaydispatch.DispatchDecisionEvent{
		TraceID: "trace-test",
		Summary: gatewaydispatch.DispatchDecisionSummary{
			SpeedBaseEwmaMs:  &rate,
			SpeedThresholdMs: gatewayhotquality.SpeedDominanceThresholdCostFirstMs,
			CacheRateStale:   true,
			CacheRateEnabled: true,
			CandidateOrder: []gatewaydispatch.DispatchDecisionCandidateOrderDetail{
				{AccountID: "a2", CacheHitRate: &rate, CacheQuantum: 5, SpeedQualified: true},
				{AccountID: "a1", CacheQuantum: -1, SpeedQualified: false},
			},
			HotQualityReorderDegraded: true,
		},
	})
	var record map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buffer.String())), &record); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if record["speedThresholdMs"].(float64) != float64(gatewayhotquality.SpeedDominanceThresholdCostFirstMs) {
		t.Fatalf("speedThresholdMs = %#v", record["speedThresholdMs"])
	}
	if record["speedBaseEwmaMs"].(float64) != 0.5 || record["cacheRateStale"] != true || record["cacheRateEnabled"] != true {
		t.Fatalf("decision-level block = %#v", record)
	}
	if record["hotQualityReorderDegraded"] != true {
		t.Fatalf("degraded flag = %#v", record)
	}
	candidates, ok := record["candidateOrder"].([]any)
	if !ok || len(candidates) != 2 {
		t.Fatalf("candidateOrder = %#v", record["candidateOrder"])
	}
	first := candidates[0].(map[string]any)
	if first["id"] != "a2" || first["cacheHitRate"].(float64) != 0.5 || first["cacheQuantum"].(float64) != 5 || first["speedQualified"] != true {
		t.Fatalf("candidate[0] = %#v", first)
	}
	second := candidates[1].(map[string]any)
	if _, present := second["cacheHitRate"]; present {
		t.Fatalf("无数据候选 cacheHitRate 必须省略: %#v", second)
	}
	if second["cacheQuantum"].(float64) != -1 || second["speedQualified"] != false {
		t.Fatalf("candidate[1] = %#v", second)
	}

	// 未执行质量排序：缓存率块整体缺省。
	buffer.Reset()
	observer(gatewaydispatch.DispatchDecisionEvent{TraceID: "trace-test"})
	record = map[string]any{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(buffer.String())), &record); err != nil {
		t.Fatalf("decode empty: %v", err)
	}
	for _, absent := range []string{"speedBaseEwmaMs", "speedThresholdMs", "cacheRateStale", "cacheRateEnabled", "candidateOrder", "hotQualityReorderDegraded"} {
		if _, present := record[absent]; present {
			t.Fatalf("未排序时 %s 必须缺省: %#v", absent, record)
		}
	}
}
