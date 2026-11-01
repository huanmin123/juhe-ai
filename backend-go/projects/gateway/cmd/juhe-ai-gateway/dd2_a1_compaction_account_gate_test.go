package main

// DD2-A1 预算豁免服务端账户锚闸的钉住测试：压缩豁免锚从「客户端自述
// codexTurn 身份」收窄为「服务端认定的真实上游账户」（用户裁决选①，验收
// 前提 = 不影响真实 Codex 功能）。验收面逐项钉住：
//
//   ① 真 codex OAuth 账户压缩请求 → 豁免保持（不限时）；
//   ② 非 codex 账户 + 伪造头（preflight 已按识别谓词 + codexTurn 辅助条件
//      授予 Unbounded）→ attempt 覆盖为常规预算（到点掐断）；
//   ③ T49 用量压缩标记（preflight 冻结）两场景不变；
//   ④ keepalive 三重门（compactWaitKeepalive）行为不变、与新闸无相互覆盖；
//   ⑤ 非压缩请求（有界预算）全不受影响；
//   ⑥ 媒体 / 桥接（T58 流式提交边界受限重放）排除面不动。

import (
	"net/http"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// dd2a1UnboundedLoop 构造 wall budget 为 Unbounded（preflight 压缩豁免授予
// 形态）的 v1 loop：Unbounded 只有 preflight compactionTimeoutsDisabled 一个
// 授予点，等价「识别谓词 + codexTurn 辅助条件已命中」的压缩语义请求。
func dd2a1UnboundedLoop(t *testing.T, traceID string) *v1DispatchLoop {
	t.Helper()
	loop := newV1TestLoop(t, &recordingFailureSink{})
	budgets, err := newRequestBudgets(traceID, loop.startedAt, gatewaypreauth.SystemClock{})
	if err != nil {
		t.Fatalf("构造请求预算: %v", err)
	}
	budgets.wall = unboundedTestWallBudget(t)
	loop.budgets = budgets
	return loop
}

// dd2a1PlainLoop 构造有界 wall budget（非压缩请求常规形态）的 v1 loop。
func dd2a1PlainLoop(t *testing.T, traceID string) *v1DispatchLoop {
	t.Helper()
	loop := newV1TestLoop(t, &recordingFailureSink{})
	budgets, err := newRequestBudgets(traceID, loop.startedAt, gatewaypreauth.SystemClock{})
	if err != nil {
		t.Fatalf("构造请求预算: %v", err)
	}
	loop.budgets = budgets
	return loop
}

// TestDD2A1RealCodexOAuthWindowKeepsExemption（验收 ①）：真 codex OAuth 档案
// 账户窗口（Type=oauth + vendor=gpt）压缩请求的豁免原样保持——TimeoutsDisabled
// 置位、coordination 仍携带 loop.budgets.wall 同一 Unbounded 实例、总时间档位
// 保持 extended。真 Codex Desktop 走的就是该档案，行为零变化。
func TestDD2A1RealCodexOAuthWindowKeepsExemption(t *testing.T) {
	loop := dd2a1UnboundedLoop(t, "trace_dd2a1_oauth")
	coordination := loop.newRequestCoordination()
	window := []gatewaydispatch.AccountCandidate{
		{ID: "acc_oauth_1", Type: "oauth", VendorCode: "gpt"},
		{ID: "acc_oauth_2", Type: "oauth", VendorCode: "gpt"},
	}
	loop.applyCompactionExemptionAccountGate(coordination, window)
	if !coordination.TimeoutsDisabled {
		t.Fatal("真 codex OAuth 账户窗口必须保持 TimeoutsDisabled=true（豁免不限时）")
	}
	if coordination.GatewayRequestWallBudget != loop.budgets.wall {
		t.Fatal("豁免保持时 coordination 必须仍携带 loop.budgets.wall 同一实例")
	}
	if !coordination.GatewayRequestWallBudget.Unbounded {
		t.Fatal("豁免保持时 wall budget 必须仍是 Unbounded 实例")
	}
	if coordination.TotalTimeLane != gatewaydispatch.TotalTimeLaneExtended {
		t.Fatalf("总时间档位必须保持 extended, got %v", coordination.TotalTimeLane)
	}
}

// TestDD2A1PinnedOAuthRecoveryReplayKeepsExemption（验收 ① 补充）：BUG-0289
// 恢复重放轮把候选窗塌缩到钉住账户——钉住账户是真 codex OAuth 档案时，即使
// 广义窗口混有 API Key 账户，豁免也按钉住账户保持。
func TestDD2A1PinnedOAuthRecoveryReplayKeepsExemption(t *testing.T) {
	loop := dd2a1UnboundedLoop(t, "trace_dd2a1_pinned")
	loop.current.Accounts = []gatewaydispatch.AccountCandidate{
		{ID: "acc_key", Type: "api_key", VendorCode: "gpt"},
		{ID: "acc_oauth", Type: "oauth", VendorCode: "gpt"},
	}
	loop.pendingCodexRecovery = &v1PendingCodexEncryptedContentRecovery{AccountID: "acc_oauth"}
	coordination := loop.newRequestCoordination()
	if coordination.SameAccountRetry == nil || coordination.SameAccountRetry.Account.ID != "acc_oauth" {
		t.Fatalf("恢复重放轮必须钉住目标账户: %+v", coordination.SameAccountRetry)
	}
	// 广义窗口含非 OAuth 账户；引擎实际窗口已塌缩到钉住账户。
	window := append([]gatewaydispatch.AccountCandidate(nil), loop.current.Accounts...)
	loop.applyCompactionExemptionAccountGate(coordination, window)
	if !coordination.TimeoutsDisabled {
		t.Fatal("钉住真 codex OAuth 账户的重放轮必须保持豁免（TimeoutsDisabled=true）")
	}
	if coordination.GatewayRequestWallBudget != loop.budgets.wall || !loop.budgets.wall.Unbounded {
		t.Fatal("钉住豁免保持时预算实例必须原样（Unbounded）")
	}
}

// TestDD2A1NonCodexAccountAttemptGetsRegularBudget（验收 ②）：压缩语义请求
//（识别谓词 + codexTurn 辅助条件命中，伪造头即可达成）落到非「真实 codex
// OAuth 档案」账户时，attempt 覆盖为常规预算——TimeoutsDisabled 复位、wall
// budget 换常规实例（默认 270s，原请求起点起算），到点掐断恢复。
func TestDD2A1NonCodexAccountAttemptGetsRegularBudget(t *testing.T) {
	cases := []struct {
		label   string
		account gatewaydispatch.AccountCandidate
	}{
		{"gpt_api_key_account", gatewaydispatch.AccountCandidate{ID: "acc_key", Type: "api_key", VendorCode: "gpt"}},
		{"non_gpt_vendor_oauth_account", gatewaydispatch.AccountCandidate{ID: "acc_xai", Type: "oauth", VendorCode: "xai"}},
		{"empty_vendor_account", gatewaydispatch.AccountCandidate{ID: "acc_ghost", Type: "oauth", VendorCode: ""}},
	}
	for _, testCase := range cases {
		t.Run(testCase.label, func(t *testing.T) {
			loop := dd2a1UnboundedLoop(t, "trace_dd2a1_gate")
			coordination := loop.newRequestCoordination()
			if !coordination.TimeoutsDisabled {
				t.Fatal("前置：Unbounded 授予形态必须先置位 TimeoutsDisabled")
			}
			loop.applyCompactionExemptionAccountGate(coordination, []gatewaydispatch.AccountCandidate{testCase.account})
			if coordination.TimeoutsDisabled {
				t.Fatal("非 codex OAuth 账户 attempt 必须复位 TimeoutsDisabled（常规超时档恢复）")
			}
			regular := coordination.GatewayRequestWallBudget
			if regular == nil || regular.Unbounded {
				t.Fatalf("coordination 必须携带常规（非 Unbounded）wall budget: %+v", regular)
			}
			if regular == loop.budgets.wall {
				t.Fatal("常规预算必须是新构造实例（Unbounded 实例单向不可收回）")
			}
			if regular.BudgetMs != gatewayrouting.DefaultGatewayRequestWallBudgetMs {
				t.Fatalf("常规预算必须取文本档默认值 %d, got %d", gatewayrouting.DefaultGatewayRequestWallBudgetMs, regular.BudgetMs)
			}
			if regular.RequestAcceptedAtMs != loop.budgets.wall.RequestAcceptedAtMs {
				t.Fatal("常规预算必须从原请求起点起算（已流逝时间计入）")
			}
			if regular.DeadlineAtMs != regular.RequestAcceptedAtMs+regular.BudgetMs {
				t.Fatalf("常规预算截止点必须为起点+预算: %d", regular.DeadlineAtMs)
			}
			// loop 级实例保持 preflight 授予形态：speedFirstCompactionGateOf 首
			// 字压缩守卫与总时间链面档位仍按请求级豁免口径读取（观察面不变）。
			if !loop.budgets.wall.Unbounded {
				t.Fatal("loop.budgets.wall 必须保持 Unbounded 实例（闸只覆盖 coordination）")
			}
			// 总时间档位保持豁免 granted 值（链面 speedFirstTotalTimeThresholdMsOf
			// 同一把尺；总时间是软观察维度，不属墙钟/attempt 预算）。
			if coordination.TotalTimeLane != gatewaydispatch.TotalTimeLaneExtended {
				t.Fatalf("覆盖后总时间档位必须保持 extended, got %v", coordination.TotalTimeLane)
			}
		})
	}
}

// TestDD2A1UsageCompactionExpectedMarkerUntouched（验收 ③）：T49 用量压缩标
// 记是 preflight 冻结的请求级用量上下文字段，闸触发（非 codex 账户）与不触
// 发（codex OAuth 账户）两场景都必须原样保留。
func TestDD2A1UsageCompactionExpectedMarkerUntouched(t *testing.T) {
	for _, testCase := range []struct {
		label   string
		account gatewaydispatch.AccountCandidate
	}{
		{"gate_fires_on_api_key_account", gatewaydispatch.AccountCandidate{ID: "acc_key", Type: "api_key", VendorCode: "gpt"}},
		{"gate_kept_on_codex_oauth_account", gatewaydispatch.AccountCandidate{ID: "acc_oauth", Type: "oauth", VendorCode: "gpt"}},
	} {
		t.Run(testCase.label, func(t *testing.T) {
			loop := dd2a1UnboundedLoop(t, "trace_dd2a1_t49")
			loop.current.UsageContext.CompactionExpected = true
			coordination := loop.newRequestCoordination()
			loop.applyCompactionExemptionAccountGate(coordination, []gatewaydispatch.AccountCandidate{testCase.account})
			if !loop.current.UsageContext.CompactionExpected {
				t.Fatalf("T49 用量压缩标记必须不受闸影响: %+v", loop.current.UsageContext)
			}
		})
	}
}

// TestDD2A1KeepaliveTripleGateUntouchedByBudgetGate（验收 ④）：keepalive 三重
// 门（流式 + codex 画像 + 压缩期望 + responses_sse，compactWaitKeepaliveOf 一
// 次判定）与新闸无相互覆盖——闸触发前后预计算保活布尔保持置位（伪造头的
// codex 画像请求保活口径与预算账户锚解耦，维持既有行为）。
func TestDD2A1KeepaliveTripleGateUntouchedByBudgetGate(t *testing.T) {
	loop := dd2a1UnboundedLoop(t, "trace_dd2a1_keepalive")
	loop.req = bodyAttachedRequest(t, http.MethodPost, "/v1/responses",
		`{"model":"gpt-test","stream":true,"input":[]}`)
	context := &gatewaypreauth.DispatchContext{
		ClientStrategy: gatewaypreauth.ClientStrategyContext{
			ClientProfile:      "codex",
			DownstreamProtocol: "responses_sse",
			Opaque: gatewaycodex.OpenAIGatewayClientStrategyContext{
				ClientProfile:           "codex",
				CodexCompactionExpected: true,
			},
		},
	}
	// 镜像 handler 装配点的预计算接线（调度内核通用化设计 5.6）。
	loop.compactWaitKeepalive = compactWaitKeepaliveOf(loop.req, context)
	if !loop.compactWaitKeepalive {
		t.Fatal("前置：codex 压缩 SSE 请求的保活布尔必须置位")
	}
	coordination := loop.newRequestCoordination()
	loop.applyCompactionExemptionAccountGate(coordination, []gatewaydispatch.AccountCandidate{
		{ID: "acc_key", Type: "api_key", VendorCode: "gpt"},
	})
	if coordination.TimeoutsDisabled {
		t.Fatal("前置失效：闸应已触发（TimeoutsDisabled 复位）")
	}
	if !loop.compactWaitKeepalive {
		t.Fatal("闸触发不得改动 keepalive 三重门的预计算布尔")
	}
}

// TestDD2A1PlainBoundedRequestIsNoop（验收 ⑤）：非压缩请求（有界 wall budget，
// 媒体 / 桥接 / 普通文本全在此形态）闸零操作——预算实例与 TimeoutsDisabled
// 原样，窗口账户构成无关。
func TestDD2A1PlainBoundedRequestIsNoop(t *testing.T) {
	loop := dd2a1PlainLoop(t, "trace_dd2a1_plain")
	coordination := loop.newRequestCoordination()
	if coordination.TimeoutsDisabled {
		t.Fatal("前置：有界预算不得置位 TimeoutsDisabled")
	}
	loop.applyCompactionExemptionAccountGate(coordination, []gatewaydispatch.AccountCandidate{
		{ID: "acc_key", Type: "api_key", VendorCode: "gpt"},
		{ID: "acc_oauth", Type: "oauth", VendorCode: "gpt"},
	})
	if coordination.GatewayRequestWallBudget != loop.budgets.wall {
		t.Fatal("有界预算请求的预算实例必须原样（闸零操作）")
	}
	if coordination.TimeoutsDisabled {
		t.Fatal("有界预算请求不得被闸置位 TimeoutsDisabled")
	}
}

// TestDD2A1MediaAndBridgeExclusionSurfacesUnchanged（验收 ⑥）：媒体创建域与
// 桥接流（T58 流式提交边界受限重放排除面）不因账户锚闸改变——①媒体 lane 的
// 有界预算请求闸零操作；②受限重放窗口排除判定（媒体域 / 桥接流方向）与预算
// 无关原样生效。桥 compact 合成摘要派发路径
//（chain_codex_compact_dispatch.go）不受本次改动触及，其投影回归由既有
// chain_codex_compact_dispatch_test.go 兜底。
func TestDD2A1MediaAndBridgeExclusionSurfacesUnchanged(t *testing.T) {
	t.Run("media_lane_bounded_budget_gate_noop", func(t *testing.T) {
		loop := dd2a1PlainLoop(t, "trace_dd2a1_media")
		coordination := loop.newRequestCoordination()
		loop.applyCompactionExemptionAccountGate(coordination, []gatewaydispatch.AccountCandidate{
			{ID: "acc_key", Type: "api_key", VendorCode: "gpt"},
		})
		if coordination.GatewayRequestWallBudget != loop.budgets.wall || coordination.TimeoutsDisabled {
			t.Fatal("媒体 lane 有界预算请求闸必须零操作")
		}
	})
	t.Run("replay_window_exclusions_independent_of_budget", func(t *testing.T) {
		mediaContext := &gatewaypreauth.DispatchContext{RequestLane: gatewayproto.LaneImage}
		if !preCommitReplayWindowDisabledOf(nil, mediaContext, gatewaydispatch.AccountCandidate{ID: "acc_any"}) {
			t.Fatal("媒体创建域的受限重放窗口排除必须保持生效")
		}
		bridgeContext := &gatewaypreauth.DispatchContext{
			ClientStrategy: gatewaypreauth.ClientStrategyContext{DownstreamProtocol: "messages_sse"},
		}
		if !preCommitReplayWindowDisabledOf(nil, bridgeContext, gatewaydispatch.AccountCandidate{ID: "acc_bridge"}) {
			t.Fatal("桥接流方向的受限重放窗口排除必须保持生效")
		}
	})
}
