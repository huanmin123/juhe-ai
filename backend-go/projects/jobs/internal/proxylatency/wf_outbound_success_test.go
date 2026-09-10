package proxylatency

import (
	"context"
	"database/sql/driver"
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 本文件覆盖 RunManual 的成功出口诊断写入链路、执行器 DB 闸门日志分支、
// 凭据信封的细分错误，以及 PG 投影 receipt/cursor 的损坏与冲突分支。

func TestWFExecuteIssuedInputWithGateAndLogger(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	store := wfOpenJobsStore(t)
	ctx := context.Background()
	owner, ok, err := store.AcquireOwnerLease(ctx, "wf-gate", time.Hour)
	if err != nil || !ok {
		t.Fatalf("owner lease ok=%v err=%v", ok, err)
	}
	proxy, ok, err := store.AcquireProxyLease(ctx, owner, "p-gate", time.Hour)
	if err != nil || !ok {
		t.Fatalf("proxy lease ok=%v err=%v", ok, err)
	}
	issued, err := store.IssueInput(ctx, InputDraft{
		ProxyID: "p-gate", ConfigRevision: wfProjRevision, Trigger: TriggerPeriodic,
		IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(5 * time.Minute), PolicyVersion: proxyLatencyInputPolicyVersion,
		ProxyType: "http", ProxyHost: "127.0.0.1", ProxyPort: portOf(server.URL),
		Targets: []Target{{Provider: "gpt", ProfileID: "profile-gpt", URL: "http://127.0.0.1:" + itoaW(portOf(server.URL)) + "/v1"}},
	})
	if err != nil {
		t.Fatalf("IssueInput 失败: %v", err)
	}
	options := ExecutorOptions{
		CredentialSecret: "wf-secret", Timeout: 5 * time.Second,
		DBGate: NewDBConcurrencyGate(2, 2), Logger: slog.New(slog.NewTextHandler(&discardWriter{}, nil)),
	}
	_, committed, runErr := ExecuteIssuedInput(ctx, store, owner, proxy, issued, options)
	if runErr != nil || !committed {
		t.Fatalf("带闸门执行 committed=%v err=%v", committed, runErr)
	}
}

type discardWriter struct{}

func (d *discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestWFDecryptEnvelopeVariants(t *testing.T) {
	secret := "wf-secret-2"
	valid := wfSealPassword(t, secret, "pw")
	parts := strings.Split(valid, ":")
	// iv 长度错误。
	shortNonce := base64.RawURLEncoding.EncodeToString([]byte("short"))
	if _, err := decryptProxyPasswordV1(secret, CredentialEnvelope{Kind: "proxy_password", Ciphertext: "v1:" + shortNonce + ":" + parts[2] + ":" + parts[3]}); err == nil {
		t.Fatal("短 iv 必须拒绝")
	}
	// tag 长度错误。
	shortTag := base64.RawURLEncoding.EncodeToString([]byte("tag"))
	if _, err := decryptProxyPasswordV1(secret, CredentialEnvelope{Kind: "proxy_password", Ciphertext: "v1:" + parts[1] + ":" + shortTag + ":" + parts[3]}); err == nil {
		t.Fatal("短 tag 必须拒绝")
	}
}

func TestWFProjectorPGCursorAndReceiptCorruption(t *testing.T) {
	ctx := context.Background()
	// currentCursor：只有 outcome_id 没有 stored_at → 存储损坏。
	rec := newWFRecorder()
	db := wfOpenRecorderDB(t, rec)
	projector := wfNewPGProjector(db)
	rec.script("FROM juhe_business.proxy_latency_projection_cursors WHERE consumer_key=$1",
		[]string{"stored_at", "outcome_id"}, [][]driver.Value{{nil, "outcome-1"}})
	if _, err := projector.currentCursor(context.Background()); err == nil || !strings.Contains(err.Error(), "存储损坏") {
		t.Fatalf("损坏游标 err=%v", err)
	}

	// receipt：非法 disposition → receiptTx 报错。
	rec2 := newWFRecorder()
	db2 := wfOpenRecorderDB(t, rec2)
	projector2 := &ResultProjector{business: db2, mode: StorePostgres}
	rec2.script("FROM juhe_business.proxy_latency_projection_receipts WHERE outcome_id=$1 FOR UPDATE",
		[]string{"outcome_id", "proxy_id", "input_version", "disposition", "reason"},
		[][]driver.Value{{"o1", "p1", int64(1), "bogus", nil}})
	tx, err := db2.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("开始事务失败: %v", err)
	}
	if _, _, err := projector2.receiptTx(ctx, tx, "o1"); err == nil || !strings.Contains(err.Error(), "disposition 无效") {
		t.Fatalf("非法 disposition err=%v", err)
	}
	_ = tx.Rollback()

	// receipt 冲突：已有 receipt 与本次写入不一致 → 幂等性冲突。
	rec3 := newWFRecorder()
	db3 := wfOpenRecorderDB(t, rec3)
	projector3 := &ResultProjector{business: db3, mode: StorePostgres, cfg: wfPGCfg()}
	rec3.scriptExec("INSERT INTO juhe_business.proxy_latency_projection_receipts", 0)
	rec3.script("FROM juhe_business.proxy_latency_projection_receipts WHERE outcome_id=$1 FOR UPDATE",
		[]string{"outcome_id", "proxy_id", "input_version", "disposition", "reason"},
		[][]driver.Value{{"o2", "p1", int64(1), "stale", "config_revision_stale"}})
	tx3, err := db3.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("开始事务失败: %v", err)
	}
	err = projector3.insertReceiptTx(ctx, tx3, ProjectionResult{OutcomeID: "o2", ProxyID: "p1", InputVersion: 1}, ProjectionApplied, "")
	if err == nil || !strings.Contains(err.Error(), "幂等性冲突") {
		t.Fatalf("PG receipt 冲突 err=%v", err)
	}
	_ = tx3.Rollback()
}
