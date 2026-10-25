package main

// w1: runtime 环境配置助手 + 路由投影 + 账户锁纯函数 + 请求失败健康标记
// + 桥响应纯函数。

import (
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func TestW1RoutingProjections(t *testing.T) {
	// 分组访问元数据：runtime → routing 投影（指针字段解引用）。
	policy := gatewayruntimecache.GroupSchedulingPolicy{}
	sourceType := "authorization"
	meta := gatewayruntimecache.GroupUsageAccessMetadata{
		GroupOwnerSystemAccountID:      "sys_owner",
		GroupAccessType:                "authorized",
		SchedulingPolicy:               &policy,
		GroupAuthorizationSourceType:   &sourceType,
		GroupAuthorizationQuotaLimited: boolPtr(true),
	}
	projected := projectGroupAccessForRouting(meta)
	if projected.GroupOwnerSystemAccountID != "sys_owner" || projected.GroupAuthorizationQuotaLimited == nil || !*projected.GroupAuthorizationQuotaLimited {
		t.Fatalf("projected = %+v", projected)
	}
	if projected.GroupAuthorizationSourceType != "authorization" {
		t.Fatalf("source type = %q", projected.GroupAuthorizationSourceType)
	}
	// 反向投影：合法策略 JSON 解码；非法 JSON 回落偏好字段。
	runtimeBack := projectGroupAccessForRuntime(projected)
	if runtimeBack.GroupOwnerSystemAccountID != "sys_owner" || runtimeBack.GroupAuthorizationSourceType == nil {
		t.Fatalf("runtime back = %+v", runtimeBack)
	}
	badPolicy := projected
	badPolicy.SchedulingPolicy = "speed_first"
	badBack := projectGroupAccessForRuntime(badPolicy)
	if badBack.SchedulingPolicy == nil {
		t.Fatal("非法策略 JSON 必须回落偏好结构")
	}
	// 账户投影：路由层只带身份与支持模型。
	account := projectAccountForRouting(gatewayruntimecache.OpenAIAccountSecret{
		ID: "acc_1", VendorCode: "openai", SupportedModels: []string{"gpt-5"},
	})
	if account.ID != "acc_1" || len(account.SupportedModels) != 1 || account.SupportedModels[0] != "gpt-5" {
		t.Fatalf("account = %+v", account)
	}
	// 能力过滤器：路由层直通（权威能力门在派发驱动）。
	result := (chainCapabilityFilter{}).FilterAccountsByRequestCapability(nil, []gatewayrouting.UpstreamAccount{{
		ID: "acc_1",
	}}, gatewayrouting.CapabilityFilterInput{})
	if len(result.Accounts) != 1 || result.Accounts[0].ID != "acc_1" {
		t.Fatalf("capability result = %+v", result)
	}
}

func w1LockRow() *chainAccountLockRow {
	return &chainAccountLockRow{
		accountID: "acc_1", enabled: 1, lockState: "ENGAGED",
		generation: 3,
		incidentID: sql.NullString{String: "inc_1", Valid: true},
	}
}

func TestW1AccountLockPureHelpers(t *testing.T) {
	locks := &chainAccountLocks{}
	// 截止时间解析：NULL / 空 / 非法 → false；合法 → 毫秒。
	if _, ok := chainAccountLockDeadlineMs(sql.NullString{}); ok {
		t.Fatal("NULL 必须 false")
	}
	if _, ok := chainAccountLockDeadlineMs(sql.NullString{String: " ", Valid: true}); ok {
		t.Fatal("空串必须 false")
	}
	if _, ok := chainAccountLockDeadlineMs(sql.NullString{String: "junk", Valid: true}); ok {
		t.Fatal("非法时间必须 false")
	}
	deadlineMs, ok := chainAccountLockDeadlineMs(sql.NullString{String: "2026-09-13T12:00:00.500Z", Valid: true})
	if !ok || deadlineMs != time.Date(2026, 9, 13, 12, 0, 0, 500_000_000, time.UTC).UnixMilli() {
		t.Fatalf("deadline = %d, %v", deadlineMs, ok)
	}
	// 跨账户阻断：enabled / 状态 / 截止门。
	if locks.blocksCrossAccount(nil, 0) {
		t.Fatal("nil 行不得阻断")
	}
	disabled := w1LockRow()
	disabled.enabled = 0
	if locks.blocksCrossAccount(disabled, 0) {
		t.Fatal("未启用不得阻断")
	}
	idle := w1LockRow()
	idle.lockState = "LOCKED_IDLE"
	if locks.blocksCrossAccount(idle, 0) {
		t.Fatal("非 ENGAGED 不得阻断")
	}
	noDeadline := w1LockRow()
	if !locks.blocksCrossAccount(noDeadline, 0) {
		t.Fatal("无截止的 ENGAGED 必须阻断")
	}
	future := w1LockRow()
	future.deadlineAt = sql.NullString{String: "2030-01-01T00:00:00Z", Valid: true}
	if !locks.blocksCrossAccount(future, 0) {
		t.Fatal("未到期必须阻断")
	}
	expired := w1LockRow()
	expired.deadlineAt = sql.NullString{String: "2000-01-01T00:00:00Z", Valid: true}
	if locks.blocksCrossAccount(expired, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()) {
		t.Fatal("已到期不得阻断")
	}
	// 视图投影：nil → nil；代次与事故 ID 透传。
	if locks.viewOf(nil, 0) != nil {
		t.Fatal("nil 行视图必须 nil")
	}
	view := locks.viewOf(w1LockRow(), 0)
	if view.Generation != 3 || view.IncidentID != "inc_1" || !view.BlocksCrossAccount {
		t.Fatalf("view = %+v", view)
	}
	// 观察匹配：nil 观察恒匹配；代次 / 事故不匹配拒绝。
	row := w1LockRow()
	if !chainAccountLockObservationMatches(row, nil) {
		t.Fatal("nil 观察必须匹配")
	}
	if chainAccountLockObservationMatches(row, &gatewaydispatch.AccountLockObservation{Generation: 9, IncidentID: "inc_1"}) {
		t.Fatal("代次不匹配必须拒绝")
	}
	if chainAccountLockObservationMatches(row, &gatewaydispatch.AccountLockObservation{Generation: 3, IncidentID: "other"}) {
		t.Fatal("事故不匹配必须拒绝")
	}
}

func TestW1LockObservationRowFence(t *testing.T) {
	// 事务内结算围栏：状态 / 截止 / 代次 / 事故 / 租约逐层。
	deadline := sql.NullString{String: "2030-01-01T00:00:00Z", Valid: true}
	base := func() (string, sql.NullString, int64, sql.NullString, sql.NullString) {
		return "ENGAGED", deadline, 3, sql.NullString{String: "inc_1", Valid: true}, sql.NullString{}
	}
	state, deadlineAt, generation, incidentID, leaseID := base()
	if chainAccountLockObservationMatchesRow("LOCKED_IDLE", deadlineAt, generation, incidentID, leaseID, nil, 0) {
		t.Fatal("非 ENGAGED 必须拒绝")
	}
	if chainAccountLockObservationMatchesRow(state, sql.NullString{}, generation, incidentID, leaseID, nil, 0) {
		t.Fatal("无截止必须拒绝")
	}
	// 截止未到（未来时刻）→ 拒绝结算；已过期 → 围栏放行。
	if chainAccountLockObservationMatchesRow(state, sql.NullString{String: "2030-01-01T00:00:00Z", Valid: true}, generation, incidentID, leaseID, nil, 0) {
		t.Fatal("截止未到必须拒绝")
	}
	expired := sql.NullString{String: "2000-01-01T00:00:00Z", Valid: true}
	if !chainAccountLockObservationMatchesRow(state, expired, generation, incidentID, leaseID, nil, 1<<62) {
		t.Fatal("无观察且已过期时围栏应通过")
	}
	if chainAccountLockObservationMatchesRow(state, expired, 9, incidentID, leaseID, &gatewaydispatch.AccountLockObservation{Generation: 3, IncidentID: "inc_1"}, 1<<62) {
		t.Fatal("代次不匹配必须拒绝")
	}
	if chainAccountLockObservationMatchesRow(state, expired, generation, incidentID, leaseID, &gatewaydispatch.AccountLockObservation{Generation: 3, IncidentID: "x"}, 1<<62) {
		t.Fatal("事故不匹配必须拒绝")
	}
	lease := "lease_1"
	observed := &gatewaydispatch.AccountLockObservation{Generation: 3, IncidentID: "inc_1", LeaseID: &lease}
	if chainAccountLockObservationMatchesRow(state, expired, generation, incidentID, sql.NullString{String: "other", Valid: true}, observed, 1<<62) {
		t.Fatal("租约不匹配必须拒绝")
	}
	if !chainAccountLockObservationMatchesRow(state, expired, generation, incidentID, sql.NullString{String: "lease_1", Valid: true}, observed, 1<<62) {
		t.Fatal("租约匹配应通过")
	}
	// 空租约观察要求行上无租约。
	emptyLease := ""
	observedEmpty := &gatewaydispatch.AccountLockObservation{Generation: 3, IncidentID: "inc_1", LeaseID: &emptyLease}
	if chainAccountLockObservationMatchesRow(state, expired, generation, incidentID, sql.NullString{String: "lease_1", Valid: true}, observedEmpty, 1<<62) {
		t.Fatal("行上有租约时空租约观察必须拒绝")
	}
	if !chainAccountLockObservationMatchesRow(state, expired, generation, incidentID, sql.NullString{}, observedEmpty, 1<<62) {
		t.Fatal("行上无租约时空租约观察应通过")
	}
	// CAS 后缀：nil 观察 / 空租约 / 有租约三种形态。
	if suffix, args := chainAccountLockLeaseFence(nil, 0); suffix != "" || args != nil {
		t.Fatalf("nil fence = %q, %v", suffix, args)
	}
	if suffix, args := chainAccountLockLeaseFence(&gatewaydispatch.AccountLockObservation{LeaseID: &emptyLease}, 0); suffix != " AND lease_id IS NULL" || args != nil {
		t.Fatalf("empty lease fence = %q, %v", suffix, args)
	}
	suffix, args := chainAccountLockLeaseFence(&gatewaydispatch.AccountLockObservation{LeaseID: &lease}, 42)
	if suffix != " AND lease_id = ? AND lease_until_ms > ?" || len(args) != 2 || args[1] != int64(42) {
		t.Fatalf("lease fence = %q, %v", suffix, args)
	}
}

func TestW1RequestDispatchMarksLRU(t *testing.T) {
	// 容量默认：<=0 → 4096。
	marks := newChainRequestDispatchMarks(0)
	request := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/x", nil))
	if marks.marked(request) {
		t.Fatal("未标记必须 false")
	}
	if marks.marked(nil) {
		t.Fatal("nil 请求必须 false")
	}
	marks.mark(nil)
	// 标记后命中；重复标记幂等。
	marks.mark(request)
	if !marks.marked(request) {
		t.Fatal("标记后必须命中")
	}
	marks.mark(request)
	// 容量淘汰：超出容量后最早的条目被逐出。
	small := newChainRequestDispatchMarks(2)
	first := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/1", nil))
	second := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/2", nil))
	third := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/3", nil))
	small.mark(first)
	small.mark(second)
	small.mark(third)
	if small.marked(first) {
		t.Fatal("最早条目必须被逐出")
	}
	if !small.marked(second) || !small.marked(third) {
		t.Fatal("容量内条目必须保留")
	}
}

func TestW1BridgePathAndPrevID(t *testing.T) {
	// gemini 流式路径探测。
	if isGeminiStreamGenerateContentRequestPath(nil) {
		t.Fatal("nil 请求必须 false")
	}
	plain := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini:generateContent", nil))
	if isGeminiStreamGenerateContentRequestPath(plain) {
		t.Fatal("非流式路径必须 false")
	}
	streaming := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini:streamGenerateContent", nil))
	if !isGeminiStreamGenerateContentRequestPath(streaming) {
		t.Fatal("流式路径必须命中")
	}
	// 映射缺席 → 路径探测；chat_completions 源映射 → 请求流式位。
	if geminiCodeAssistDownstreamStreamForMapping(plain, nil) {
		t.Fatal("非流式路径不得判流式")
	}
	streamReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	streamReq.Body = &gatewaybody.Request{State: &gatewaybody.BodyState{Stream: boolPtr(true)}}
	mappingFamily := gatewayproto.ResolvedModelMapping{SourceEndpointFamily: "chat_completions"}
	if !geminiCodeAssistDownstreamStreamForMapping(streamReq, &mappingFamily) {
		t.Fatal("chat 源映射必须跟随请求流式位")
	}
	// 上一响应 ID 提取。
	if got := chainBridgePreviousResponseIDOf(nil); got != "" {
		t.Fatalf("nil body = %q", got)
	}
	if got := chainBridgePreviousResponseIDOf(map[string]any{"previous_response_id": "resp_9"}); got != "resp_9" {
		t.Fatalf("prev id = %q", got)
	}
	_ = errors.New
}
