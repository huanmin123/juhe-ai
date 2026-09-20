package main

// w16b（覆盖率收尾批次一，纯函数与零装配臂）：对照
// F:/sub2api-lite/.local/tmp-coverage/gwcmd_w16b_base.cov 的零计数块逐块补测。
// 全部进程内确定性驱动，不启动插桩二进制、不连接真实 dev PG/Redis。
//
// 覆盖目标（基线 profile 零块 -> 本文件用例）：
//  1. chain_usage.go applyUsageAccountScope 的非 OpenAIAccountView 早退臂与
//     derefString 非空指针分支（基线 360-362 / 380）。
//  2. chain_driver.go gptRequestOverrideEndpointFamily 的 default 臂与
//     requiredSupportedEndpointMode 跨协议映射未知上游族 default 臂
//     （基线 1073-1074 / 1148-1149）：账户映射携带未登记的 upstream 族词元，
//     NormalizeEndpointFamily 对未知词元原样透传。
//  3. chain_chat_images.go encodeChatModelImage 两个编码错误臂（零尺寸源图
//     与零尺寸目标触发 chainVP8LEncode 宽度守卫；基线 153-155 / 160-162）与
//     sqrtOf 64 轮牛顿迭代不收敛的兜底返回（基线 280）。
//  4. chain_v1.go newRequestBudgets 空 RequestID 触发
//     RouteCoordinationBudget 构造错误臂（基线 2685-2687）。
//  5. compose_prewarm.go 预热失败的 logger.Warn 臂（关闭的 sqlite 运行态
//     缓存；基线 35-40）。
//  6. compose_account_test_local.go wireInProcessAccountTestDispatch 的队列
//     env 非法值 fail-closed 臂（基线 180-182）。
//  7. compose_account_reads.go authorizationStatsSourceAdapter 读错误透传臂
//     与 wireAccountReadCompanions 空 secret 的 accountkeystates 构造错误臂
//     （基线 31-33 / 60-62）。
//
// 隔离说明：全部使用进程内 sqlite（t.TempDir）与关闭句柄，不触碰任何真实
// 数据库与 Redis 实例；不改变任何全局状态（os.Args/env 用 t.Setenv）。

import (
	"database/sql"
	"image"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// w16bPlainAccountView 是未实现 OpenAIAccountView 的最小 AccountView 桩：
// applyUsageAccountScope 对非 OpenAI 视图按缺失 scope 清空投影（Node 对齐）。
type w16bPlainAccountView struct{ id string }

func (v w16bPlainAccountView) GetID() string                        { return v.id }
func (v w16bPlainAccountView) GetName() string                      { return "w16b-plain" }
func (v w16bPlainAccountView) GetProviderCode() string              { return "openai" }
func (v w16bPlainAccountView) GetProviderProtocolProfileID() string { return "" }
func (v w16bPlainAccountView) GetProtocolCode() string              { return "openai" }
func (v w16bPlainAccountView) GetProtocolVersion() string           { return "v1" }
func (v w16bPlainAccountView) GetClientCompatibility() string       { return "" }

func TestW16BUsageAccountScopeArms(t *testing.T) {
	// 非 OpenAIAccountView：scope 投影早退，accountId 仍取 GetID，其余清空。
	record := &gatewayusage.UsageRecordInput{AccountID: "origin"}
	applyUsageAccountScope(record, w16bPlainAccountView{id: "w16b-plain-id"})
	if record.AccountID != "w16b-plain-id" {
		t.Fatalf("accountId = %q, want GetID 投影", record.AccountID)
	}
	if record.AccountOwnerSystemAccountID != "" || record.AccountAccessType != "" ||
		record.AccountAuthorizationID != "" || record.AccountAuthorizationSourceType != "" ||
		record.AccountAuthorizationSourceTeamID != "" {
		t.Fatalf("非 OpenAI 视图必须按缺失 scope 清空投影: %+v", record)
	}

	// nil 账户视图：整段早退（守卫臂，保持既有覆盖）。
	nilRecord := &gatewayusage.UsageRecordInput{AccountID: "keep"}
	applyUsageAccountScope(nilRecord, nil)
	if nilRecord.AccountID != "keep" {
		t.Fatalf("nil 视图不应改动记录: %+v", nilRecord)
	}

	// derefString 非空指针分支（基线 380 零块）。
	value := "w16b-deref"
	if got := derefString(&value); got != "w16b-deref" {
		t.Fatalf("derefString = %q", got)
	}
	if got := derefString(nil); got != "" {
		t.Fatalf("derefString(nil) = %q", got)
	}
}

func TestW16BDriverUnknownUpstreamFamilyArms(t *testing.T) {
	driver := newChainProviderDriver()
	chatReq := newEndpointGateRequest(t, "POST", "/v1/chat/completions", `{"model":"w16b-mapped"}`)

	// gptRequestOverrideEndpointFamily 直接接收 mapping 参数：注入未登记的
	// 上游族词元（NormalizeEndpointFamily 对未知词元原样透传，不进入覆盖
	// 词表 switch）→ default 臂返回空串（基线 1073-1074）。
	bogus := &gatewayproto.ResolvedModelMapping{
		SourceModel:            "w16b-mapped",
		SourceEndpointFamily:   "chat_completions",
		UpstreamModel:          "w16b-upstream",
		UpstreamEndpointFamily: "w16b_unknown_family",
	}
	if got := gptRequestOverrideEndpointFamily(chatReq, gatewaydispatch.AccountCandidate{}, bogus); got != "" {
		t.Fatalf("gptRequestOverrideEndpointFamily(未知族) = %q, want 空", got)
	}
	if got := gptRequestOverrideEndpointFamily(chatReq, gatewaydispatch.AccountCandidate{}, nil); got != "chat_completions" {
		t.Fatalf("无映射时 override = %q, want chat_completions", got)
	}

	// 夹具自检：真实 resolver 对未知上游族直接拒绝（据此登记
	// requiredSupportedEndpointMode 的映射 switch default 臂经 resolver
	// 不可达——未登记转换对在 isOpenAIModelMappingRuntimeConversionSupported
	// 处先被过滤）。
	account := gatewaydispatch.AccountCandidate{
		ID:           "w16b-acc",
		ProtocolCode: "openai",
		ProviderCode: "w16b-provider",
		Type:         "api_key",
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel:            "w16b-mapped",
			SourceEndpointFamily:   "chat_completions",
			UpstreamModel:          "w16b-upstream",
			UpstreamEndpointFamily: "w16b_unknown_family",
			Enabled:                true,
		}},
	}
	if mapping := driver.resolveAccountModelMapping(account, chatReq, ""); mapping != nil {
		t.Fatalf("未知上游族必须被 resolver 过滤, got %+v", mapping)
	}
}

func TestW16BChatImageEncodeErrorArms(t *testing.T) {
	dimensions := chatImageDimensions{}

	// 零尺寸源图与零尺寸目标：直接命中 chainVP8LEncode 的宽度守卫（151-155）。
	empty := image.NewNRGBA(image.Rect(0, 0, 0, 0))
	if output, err := encodeChatModelImage(empty, 0, 0, dimensions); err == nil {
		t.Fatalf("零尺寸源图必须编码失败, got %+v", output)
	} else if !strings.Contains(err.Error(), "WebP") {
		t.Fatalf("编码错误文案不符: %v", err)
	}

	// 非零源图 + 零尺寸目标：resize 后再编码失败（158-162 臂）。
	small := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	if output, err := encodeChatModelImage(small, 0, 0, dimensions); err == nil {
		t.Fatalf("零尺寸目标必须编码失败, got %+v", output)
	}

	// sqrtOf 64 轮牛顿迭代不收敛：超大值直达循环后兜底返回（基线 280）。
	estimate := sqrtOf(1e308)
	if estimate <= 0 || estimate >= 1e308 {
		t.Fatalf("sqrtOf(1e308) = %v, want 有限收敛中间值", estimate)
	}
	if got := sqrtOf(0); got != 0 {
		t.Fatalf("sqrtOf(0) = %v", got)
	}
	if got := sqrtOf(16); got < 4-1e-9 || got > 4+1e-9 {
		t.Fatalf("sqrtOf(16) = %v, want 4", got)
	}
}

func TestW16BNewRequestBudgetsErrorArm(t *testing.T) {
	// 空 RequestID：RouteCoordinationBudget 归一化报错并包装
	// （chain_v1.go 2685-2687）。
	if budgets, err := newRequestBudgets("   ", 123, gatewaypreauth.SystemClock{}); err == nil {
		t.Fatalf("空 RequestID 必须报错, got %+v", budgets)
	} else if !strings.Contains(err.Error(), "route coordination budget") {
		t.Fatalf("错误包装不符: %v", err)
	}

	// 合法 RequestID：三个预算全部就绪（保持成功面覆盖）。
	budgets, err := newRequestBudgets("w16b-trace", 123, gatewaypreauth.SystemClock{})
	if err != nil || budgets.wall == nil || budgets.coordination == nil || budgets.tracker == nil {
		t.Fatalf("newRequestBudgets = %+v, %v", budgets, err)
	}
}

func TestW16BPrewarmErrorLogArm(t *testing.T) {
	// 关闭的 sqlite 运行态缓存：Prewarm 首查即错，进入 logger.Warn 臂
	// （compose_prewarm.go 35-40）。goroutine 内短路径立即返回，等待即可。
	cache := w2aBrokenRoutingCache(t)
	startGatewayAPIKeyCachePrewarm(cache, slog.Default())
	time.Sleep(250 * time.Millisecond)
}

func TestW16BAccountTestQueueEnvFailClosed(t *testing.T) {
	t.Setenv("JUHE_AI_JOBS_PROBE_CONCURRENCY", "w16b-not-a-number")
	// env 读取在组合访问之前发生：nil composed 不会被解引用。
	if err := wireInProcessAccountTestDispatch(nil, runtimeConfig{}, nil); err == nil {
		t.Fatal("非法并发 env 必须 fail closed")
	} else if !strings.Contains(err.Error(), "必须是整数") {
		t.Fatalf("错误文案不符: %v", err)
	}
}

func TestW16BAccountReadCompanionsArms(t *testing.T) {
	root := t.TempDir()

	// authorizationStatsSourceAdapter 读错误透传（31-33）：底库句柄关闭。
	closedDB := w2aClosedSQLiteDB(t)
	authzStore, err := authz.NewStore(closedDB, false, time.Now)
	if err != nil {
		t.Fatalf("create authz store: %v", err)
	}
	adapter := authorizationStatsSourceAdapter{store: authzStore}
	if stats, err := adapter.ResourceAuthorizationStatsByResourceIds(t.Context(), "group", []string{"w16b-grp"}); err == nil {
		t.Fatalf("关闭句柄必须报错, got %+v", stats)
	}

	// wireAccountReadCompanions 空 secret：accountkeystates.NewStore 构造失败
	// （60-62）。业务/授权 store 用独立有效 sqlite 打开。
	db, err := sql.Open("sqlite", root+"/w16b-business.sqlite3")
	if err != nil {
		t.Fatalf("open business db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	accountStore, err := accounts.NewStore(db, false, "w16b-secret", time.Now, func(kind string) string { return kind })
	if err != nil {
		t.Fatalf("create account store: %v", err)
	}
	validAuthz, err := authz.NewStore(db, false, time.Now)
	if err != nil {
		t.Fatalf("create valid authz store: %v", err)
	}
	composed := &composition{db: db, pgDialect: false}
	if err := wireAccountReadCompanions(composed, accountStore, validAuthz, ""); err == nil {
		t.Fatal("空 secret 必须让 keyStates 构造失败")
	}
}
