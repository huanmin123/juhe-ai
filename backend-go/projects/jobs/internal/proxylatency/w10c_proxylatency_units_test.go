package proxylatency

import (
	"errors"
	"net/http/httptest"
	"testing"
	"time"
)

// w10c_proxylatency_units_test.go 覆盖 proxy-latency 域剩余可直达纯函数臂：
// runner 并发配额、时间/状态/校验纯函数、claim/request ID、租赁释放结果、
// outcome 投影校验、命令管理请求元数据。DB 无关。

type fakeResult struct {
	rowsAffected int64
	err          error
}

func (f fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (f fakeResult) RowsAffected() (int64, error) { return f.rowsAffected, f.err }

func TestW10CRunnerConcurrencyHelpers(t *testing.T) {
	// workerConcurrency：cfg>0 用 cfg，否则 1。
	r0 := &Runner{}
	if got := r0.workerConcurrency(); got != 1 {
		t.Fatalf("默认 worker concurrency = %d want 1", got)
	}
	r1 := &Runner{cfg: RuntimeConfig{WorkerConcurrency: 7}}
	if got := r1.workerConcurrency(); got != 7 {
		t.Fatalf("cfg worker concurrency = %d want 7", got)
	}
	// dbConcurrency / dbQueueSize：cfg>0 用 cfg，否则 0。
	if got := r0.dbConcurrency(); got != 0 {
		t.Fatalf("默认 dbConcurrency = %d want 0", got)
	}
	if got := r1.dbConcurrency(); got != 0 {
		t.Fatalf("dbConcurrency cfg=0 = %d want 0", got)
	}
	r2 := &Runner{cfg: RuntimeConfig{DBConcurrency: 3, DBQueueSize: 5}}
	if got := r2.dbConcurrency(); got != 3 {
		t.Fatalf("dbConcurrency = %d want 3", got)
	}
	if got := r2.dbQueueSize(); got != 5 {
		t.Fatalf("dbQueueSize = %d want 5", got)
	}
}

func TestW10CFormatRuntimeTime(t *testing.T) {
	if got := formatRuntimeTime(time.Time{}); got != "" {
		t.Fatalf("零值时间 = %q want 空", got)
	}
	value := time.Date(2026, 9, 6, 12, 0, 0, 123000000, time.UTC)
	if got := formatRuntimeTime(value); got != "2026-09-06T12:00:00.123Z" {
		t.Fatalf("formatRuntimeTime = %s", got)
	}
}

func TestW10CValidateOutcomeArms(t *testing.T) {
	revision := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	valid := Outcome{
		OutcomeID: "o1", RequestID: "r1", ProxyID: "p1",
		ObservedAt: time.Now().UTC(), InputVersion: 1,
		ConfigRevision: revision, Trigger: TriggerPeriodic,
		OwnerFenceToken: 1, ProxyFenceToken: 1, OverallStatus: OverallPassed,
	}
	if err := validateOutcome(valid); err != nil {
		t.Fatalf("合法 outcome 应通过: %v", err)
	}
	// 缺幂等/fence 字段。
	mut := valid
	mut.OutcomeID = ""
	if err := validateOutcome(mut); err == nil {
		t.Fatalf("缺 outcome_id 应报错")
	}
	mut = valid
	mut.RequestID = ""
	if err := validateOutcome(mut); err == nil {
		t.Fatalf("缺 request_id 应报错")
	}
	mut = valid
	mut.ProxyID = ""
	if err := validateOutcome(mut); err == nil {
		t.Fatalf("缺 proxy_id 应报错")
	}
	mut = valid
	mut.InputVersion = 0
	if err := validateOutcome(mut); err == nil {
		t.Fatalf("input version 非法应报错")
	}
	mut = valid
	mut.OwnerFenceToken = 0
	if err := validateOutcome(mut); err == nil {
		t.Fatalf("owner fence 非法应报错")
	}
	mut = valid
	mut.ObservedAt = time.Time{}
	if err := validateOutcome(mut); err == nil {
		t.Fatalf("observed_at 零值应报错")
	}
	// config revision 非法（非 UTC / 空白）。
	mut = valid
	mut.ConfigRevision = " not-rfc3339 "
	if err := validateOutcome(mut); err == nil {
		t.Fatalf("config revision 非法应报错")
	}
	// trigger 非法。
	mut = valid
	mut.Trigger = "other"
	if err := validateOutcome(mut); err == nil {
		t.Fatalf("trigger 非法应报错")
	}
	// overall status 非法。
	mut = valid
	mut.OverallStatus = "weird"
	if err := validateOutcome(mut); err == nil {
		t.Fatalf("overall status 非法应报错")
	}
}

func TestW10CCanonicalizeInputDraftArms(t *testing.T) {
	// 非 UTC / 缺字段错误臂（完整合法 draft 需全字段 target/policy 等，
	// 成功路径由既有 batch/issue 测试覆盖）。
	draft := InputDraft{ProxyID: "p1"}
	if _, err := canonicalizeInputDraft(draft); err == nil {
		t.Fatalf("非 UTC draft 应报错")
	}
	// validateInputDraft 包装错误臂。
	if err := validateInputDraft(InputDraft{}); err == nil {
		t.Fatalf("非法 draft 应报错")
	}
}

func TestW10CClaimAndRequestIDs(t *testing.T) {
	token, err := newClaimToken()
	if err != nil || len(token) != 32 {
		t.Fatalf("claim token = %q len=%d err=%v", token, len(token), err)
	}
	requestID, err := newRequestID()
	if err != nil || len(requestID) != len("j3a-")+32 {
		t.Fatalf("request id = %q err=%v", requestID, err)
	}
	// stableOutcomeID 确定性。
	if stableOutcomeID("req-1") != stableOutcomeID("req-1") {
		t.Fatalf("stableOutcomeID 应确定")
	}
	if stableOutcomeID("req-1") == stableOutcomeID("req-2") {
		t.Fatalf("不同 request 不应同 outcome id")
	}
	// validOwnerLease。
	if validOwnerLease(OwnerLease{OwnerID: "owner", FenceToken: 1}) != true {
		t.Fatalf("合法 owner lease 应为真")
	}
	if validOwnerLease(OwnerLease{OwnerID: "  ", FenceToken: 1}) != false {
		t.Fatalf("owner 空白应非法")
	}
	if validOwnerLease(OwnerLease{OwnerID: "owner", FenceToken: 0}) != false {
		t.Fatalf("fence 0 应非法")
	}
}

func TestW10CReleasedLeaseResult(t *testing.T) {
	// err 透传。
	if got := releasedLeaseResult(nil, errors.New("boom"), errors.New("lost")); got == nil || got.Error() != "boom" {
		t.Fatalf("err 应透传, got %v", got)
	}
	// RowsAffected 错误。
	if got := releasedLeaseResult(fakeResult{err: errors.New("rows")}, nil, errors.New("lost")); got == nil || got.Error() != "rows" {
		t.Fatalf("rows affected 错误应透传, got %v", got)
	}
	// updated != 1 → lost。
	if got := releasedLeaseResult(fakeResult{rowsAffected: 0}, nil, errors.New("lost")); got == nil || got.Error() != "lost" {
		t.Fatalf("未更新应返回 lost, got %v", got)
	}
	// 成功。
	if got := releasedLeaseResult(fakeResult{rowsAffected: 1}, nil, errors.New("lost")); got != nil {
		t.Fatalf("成功应返回 nil, got %v", got)
	}
}

func TestW10CValidateProjectionOutcome(t *testing.T) {
	revision := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	base := Outcome{
		OutcomeID: "o1", RequestID: "r1", ProxyID: "p1",
		ObservedAt: time.Now().UTC(), InputVersion: 1,
		ConfigRevision: revision, Trigger: TriggerPeriodic,
		OwnerFenceToken: 1, ProxyFenceToken: 1,
		OverallStatus: OverallPassed,
		Items:         []ItemResult{{Status: ItemPassed}},
	}
	if got := validateProjectionOutcome(base); got != "" {
		t.Fatalf("合法投影 outcome 应通过, got %q", got)
	}
	// 契约非法。
	bad := base
	bad.OutcomeID = ""
	if got := validateProjectionOutcome(bad); got != "outcome_contract_invalid" {
		t.Fatalf("契约非法 = %q", got)
	}
	// trigger 不允许臂：validateOutcome 已在投影前拒绝非法 trigger，故
	// validateProjectionOutcome 的 trigger_not_allowed 分支不可达（防御死臂，
	// w10c 取证后不再单独断言）。
	bad = base
	bad.OverallStatus = "contract-breaker"
	if got := validateProjectionOutcome(bad); got != "outcome_contract_invalid" {
		t.Fatalf("契约非法 = %q", got)
	}
	// items 缺失。
	bad = base
	bad.Items = nil
	if got := validateProjectionOutcome(bad); got != "outcome_items_missing" {
		t.Fatalf("items 缺失 = %q", got)
	}
	// overall 状态不匹配。
	bad = base
	bad.OverallStatus = OverallWarning
	if got := validateProjectionOutcome(bad); got != "overall_status_mismatch" {
		t.Fatalf("状态不匹配 = %q", got)
	}
}

func TestW10CProjectionInstantAndStatusValidators(t *testing.T) {
	// sameProjectionInstant：同值 true，异值 false，非法 false。
	if !sameProjectionInstant("2026-09-06T12:00:00Z", "2026-09-06T12:00:00.000Z") {
		t.Fatalf("同投影时刻应相等")
	}
	if sameProjectionInstant("2026-09-06T12:00:00Z", "2026-09-06T13:00:00Z") {
		t.Fatalf("异投影时刻不应相等")
	}
	if sameProjectionInstant("garbage", "2026-09-06T12:00:00Z") {
		t.Fatalf("非法时刻不应相等")
	}
	// validProjectionInstant。
	if !validProjectionInstant("2026-09-06T12:00:00Z") || validProjectionInstant("garbage") {
		t.Fatalf("validProjectionInstant 断言失败")
	}
	// validProjectionStatus 全值。
	for _, status := range []OverallStatus{OverallPassed, OverallWarning, OverallFailed, OverallUnknown} {
		if !validProjectionStatus(string(status)) {
			t.Fatalf("投影状态 %s 应合法", status)
		}
	}
	if validProjectionStatus("weird") {
		t.Fatalf("非法投影状态应非法")
	}
	// validProjectionDisposition 全值。
	for _, disposition := range []string{string(ProjectionApplied), string(ProjectionStale), string(ProjectionIgnored), string(ProjectionRejected)} {
		if !validProjectionDisposition(disposition) {
			t.Fatalf("投影处置 %s 应合法", disposition)
		}
	}
	if validProjectionDisposition("weird") {
		t.Fatalf("非法投影处置应非法")
	}
	// compareOutcomeCursor：stored_at 比较 + 相同时 outcome_id 字典序。
	if compareOutcomeCursor(OutcomeCursor{StoredAt: time.Now(), OutcomeID: "a"}, OutcomeCursor{StoredAt: time.Now().Add(-time.Hour), OutcomeID: "a"}) != 1 {
		t.Fatalf("后时刻应大于前时刻")
	}
	if compareOutcomeCursor(OutcomeCursor{StoredAt: time.Now().Add(-time.Hour), OutcomeID: "a"}, OutcomeCursor{StoredAt: time.Now(), OutcomeID: "a"}) != -1 {
		t.Fatalf("前时刻应小于后时刻")
	}
	if compareOutcomeCursor(OutcomeCursor{StoredAt: time.Now(), OutcomeID: "a"}, OutcomeCursor{StoredAt: time.Now(), OutcomeID: "b"}) >= 0 {
		t.Fatalf("同刻应比 outcome_id")
	}
}

func TestW10CManualAdminRequestMetadata(t *testing.T) {
	// 合法 RemoteAddr。
	req := httptest.NewRequest("POST", "/api/proxies/test", nil)
	req.RemoteAddr = "1.2.3.4:5678"
	info := manualAdminRequestMetadata(req)
	if info.clientIP != "1.2.3.4" || info.path != "/api/proxies/test" {
		t.Fatalf("请求元数据 = %+v", info)
	}
	// 非法 RemoteAddr（无端口）→ clientIP 空。
	req.RemoteAddr = "not-an-addr"
	if info2 := manualAdminRequestMetadata(req); info2.clientIP != "" {
		t.Fatalf("非法 remote addr clientIP = %q want 空", info2.clientIP)
	}
}

func TestW10CCompareOutcomeCursorEquals(t *testing.T) {
	now := time.Now()
	if got := compareOutcomeCursor(OutcomeCursor{StoredAt: now, OutcomeID: "same"}, OutcomeCursor{StoredAt: now, OutcomeID: "same"}); got != 0 {
		t.Fatalf("同刻同 id 应相等, got %d", got)
	}
}
