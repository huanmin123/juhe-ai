package proxylatency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件通过直接改写 SQLite jobs 库的行数据，覆盖 ListCommittedOutcomes /
// FindCommittedOutcome / LoadCommittedOutcome 对损坏持久化记录的 fail-closed
// 分支（时间非法、载荷非法、摘要不一致、行元数据与载荷不一致）。

func wfTamperedStore(t *testing.T) (*Store, *struct{}) {
	t.Helper()
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "wf-tamper.sqlite3")})
	if err != nil {
		t.Fatalf("OpenStore 失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, nil
}

func wfInsertRawOutcome(t *testing.T, store *Store, columns, values string) {
	t.Helper()
	if _, err := store.db.Exec(`INSERT INTO proxy_latency_outcomes(outcome_id,request_id,proxy_id,input_version,config_revision,trigger,owner_fence_token,proxy_fence_token,observed_at,stored_at,payload,payload_digest,committed) VALUES(` + values + `)`); err != nil {
		t.Fatalf("插入原始行失败: %v", err)
	}
}

func TestWFListCommittedOutcomesTamperVariants(t *testing.T) {
	base := time.Now().UTC().Add(-time.Minute)
	payload := `{"outcome_id":"o-x","request_id":"r-x","proxy_id":"p-x","observed_at":"` + base.Add(time.Second).Format(time.RFC3339Nano) + `","input_version":1,"config_revision":"` + base.Format(time.RFC3339Nano) + `","trigger":"periodic","owner_fence_token":1,"proxy_fence_token":1,"overall_status":"passed","items":[{"provider":"gpt","profile_id":"profile-gpt","status":"passed"}]}`
	digest := sha256Hex([]byte(payload))

	tests := []struct {
		name    string
		values  string
		wantErr string
	}{
		{name: "observed_at 非法", values: `'o1','r1','p1',1,'` + base.Format(time.RFC3339Nano) + `','periodic',1,1,'not-a-time','` + base.Format(time.RFC3339Nano) + `','` + payload + `','` + digest + `',1`, wantErr: "observed_at 无效"},
		{name: "stored_at 为零值", values: `'o2','r2','p2',1,'` + base.Format(time.RFC3339Nano) + `','periodic',1,1,'` + base.Add(time.Second).Format(time.RFC3339Nano) + `','0001-01-01T00:00:00Z','` + payload + `','` + digest + `',1`, wantErr: "stored_at 无效"},
		{name: "载荷非法", values: `'o3','r3','p3',1,'` + base.Format(time.RFC3339Nano) + `','periodic',1,1,'` + base.Add(time.Second).Format(time.RFC3339Nano) + `','` + base.Format(time.RFC3339Nano) + `','not-json','x',1`, wantErr: "payload 无效"},
		{name: "行元数据不一致", values: `'o4','r4','p4',1,'` + base.Format(time.RFC3339Nano) + `','periodic',1,1,'` + base.Add(time.Second).Format(time.RFC3339Nano) + `','` + base.Format(time.RFC3339Nano) + `','` + payload + `','` + digest + `',1`, wantErr: "行元数据与 payload 不一致"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, _ := wfTamperedStore(t)
			wfInsertRawOutcome(t, store, "", tt.values)
			_, err := store.ListCommittedOutcomes(context.Background(), nil, 10)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err=%v 必须包含 %q", err, tt.wantErr)
			}
			// FindCommittedOutcome 走相同校验（行 id 是 values 中的第一个引号字段）。
			outcomeID := strings.Split(strings.Split(tt.values, "'")[1], "'")[0]
			if _, _, err := store.FindCommittedOutcome(context.Background(), outcomeID); err == nil {
				t.Fatal("FindCommittedOutcome 必须对损坏行 fail closed")
			}
		})
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestWFStoreInputAndClaimTamper(t *testing.T) {
	store := wfOpenJobsStore(t)
	ctx := context.Background()
	owner, ok, err := store.AcquireOwnerLease(ctx, "wf-tamper", time.Hour)
	if err != nil || !ok {
		t.Fatalf("owner lease ok=%v err=%v", ok, err)
	}
	proxy, ok, err := store.AcquireProxyLease(ctx, owner, "p-tamper", time.Hour)
	if err != nil || !ok {
		t.Fatalf("proxy lease ok=%v err=%v", ok, err)
	}
	issued, err := store.IssueInput(ctx, wfCycleDraft("p-tamper"))
	if err != nil {
		t.Fatalf("IssueInput 失败: %v", err)
	}

	// 篡改 issued_at 为非法字符串 → admission 的 claim 查询/输入快照 fail closed。
	if _, err := store.db.Exec(`UPDATE proxy_latency_inputs SET issued_at='garbage' WHERE request_id=?`, issued.RequestID); err != nil {
		t.Fatalf("篡改 issued_at 失败: %v", err)
	}
	if err := store.VerifyExecutionInput(ctx, owner, proxy, issued); !errors.Is(err, ErrInputFence) {
		t.Fatalf("非法 issued_at err=%v", err)
	}
	// 恢复 issued_at，篡改 expires_at 为过去 → 输入过期。
	if _, err := store.db.Exec(`UPDATE proxy_latency_inputs SET issued_at=?, expires_at=? WHERE request_id=?`,
		issued.IssuedAt.Format(time.RFC3339Nano), time.Now().Add(-time.Minute).Format(time.RFC3339Nano), issued.RequestID); err != nil {
		t.Fatalf("篡改 expires_at 失败: %v", err)
	}
	if err := store.VerifyExecutionInput(ctx, owner, proxy, issued); !errors.Is(err, ErrInputFence) {
		t.Fatalf("过期输入 err=%v", err)
	}

	// 手工插入一个过期 claim → admission 判定可重试（claim 过期）。
	if _, err := store.db.Exec(`INSERT INTO proxy_latency_execution_claims(request_id,claim_token,outcome_id,proxy_id,input_version,config_revision,trigger,owner_id,owner_fence_token,proxy_fence_token,input_digest,claim_until,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		issued.RequestID, "stale-token", stableOutcomeID(issued.RequestID), "p-tamper", issued.InputVersion, issued.ConfigRevision, string(issued.Trigger),
		owner.OwnerID, owner.FenceToken, proxy.FenceToken, "digest", time.Now().Add(-time.Minute).Format(time.RFC3339Nano), time.Now().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("插入过期 claim 失败: %v", err)
	}
	if _, claimToken, replay, err := store.AdmitExecution(ctx, owner, proxy, issued); err != nil || replay != nil || claimToken == "" {
		t.Fatalf("过期 claim 应可重新准入 claim=%q err=%v", claimToken, err)
	}

	// ReleaseExecutionClaim / VerifyExecutionInput 参数守卫。
	if err := store.ReleaseExecutionClaim(ctx, "", ""); !errors.Is(err, ErrInputFence) {
		t.Fatalf("空参数 err=%v", err)
	}
	if err := store.VerifyExecutionInput(ctx, owner, proxy, IssuedInput{RequestID: "x", InputVersion: 0}); !errors.Is(err, ErrInputFence) {
		t.Fatalf("非法输入 err=%v", err)
	}
}

func TestWFStoreAdmitExecutionPoisonedPayload(t *testing.T) {
	store := wfOpenJobsStore(t)
	ctx := context.Background()
	owner, ok, err := store.AcquireOwnerLease(ctx, "wf-poison", time.Hour)
	if err != nil || !ok {
		t.Fatalf("owner lease ok=%v err=%v", ok, err)
	}
	proxy, ok, err := store.AcquireProxyLease(ctx, owner, "p-poison", time.Hour)
	if err != nil || !ok {
		t.Fatalf("proxy lease ok=%v err=%v", ok, err)
	}
	issued, err := store.IssueInput(ctx, wfCycleDraft("p-poison"))
	if err != nil {
		t.Fatalf("IssueInput 失败: %v", err)
	}
	// 载荷投毒 → admission fail closed。
	if _, err := store.db.Exec(`UPDATE proxy_latency_inputs SET payload='not-json' WHERE request_id=?`, issued.RequestID); err != nil {
		t.Fatalf("投毒失败: %v", err)
	}
	if _, _, _, err := store.AdmitExecution(ctx, owner, proxy, issued); !errors.Is(err, ErrInputFence) {
		t.Fatalf("投毒载荷 err=%v", err)
	}
	// 已提交 outcome 载荷投毒 → 重放读取 fail closed。
	outcome := Outcome{
		OutcomeID: stableOutcomeID(issued.RequestID), RequestID: issued.RequestID, ProxyID: "p-poison",
		ObservedAt: issued.IssuedAt.Add(time.Second), InputVersion: issued.InputVersion, ConfigRevision: issued.ConfigRevision,
		Trigger: issued.Trigger, OwnerFenceToken: owner.FenceToken, ProxyFenceToken: proxy.FenceToken,
		OverallStatus: OverallPassed,
		Items:         []ItemResult{{Provider: "gpt", ProfileID: "profile-gpt", Status: ItemPassed, HTTPStatus: 200, LatencyMS: 42, Outcome: OutcomeSuccess}},
	}
	if _, err := store.AppendOutcome(ctx, owner, proxy, outcome); err != nil {
		t.Fatalf("AppendOutcome 失败: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE proxy_latency_outcomes SET payload='not-json' WHERE request_id=?`, issued.RequestID); err != nil {
		t.Fatalf("投毒 outcome 失败: %v", err)
	}
	if _, _, err := store.LoadCommittedOutcome(ctx, issued); err == nil {
		t.Fatal("投毒 outcome 必须拒绝")
	}
}
