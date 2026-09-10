package proxylatency

import (
	"context"
	"database/sql/driver"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 本文件集中覆盖剩余错误分支：ManualAdminSource 契约检查的逐段失败传播、
// RunManual 复用当前 owner lease 与游标/快照查询失败，以及 runCycle 的
// 代理释放失败记账。

func TestWFManualAdminCheckContractFailures(t *testing.T) {
	ctx := context.Background()
	source := func(t *testing.T, rec *wfRecorder) *PostgresManualAdminSource {
		db := wfOpenRecorderDB(t, rec)
		source, err := NewPostgresManualAdminSource(db, func() time.Time { return wfProjBase })
		if err != nil {
			t.Fatalf("构造失败: %v", err)
		}
		return source
	}
	// 快照查询失败。
	rec1 := newWFRecorder()
	s1 := source(t, rec1)
	rec1.script("has_table_privilege", []string{"ok"}, [][]driver.Value{{true}})
	rec1.failQuery("FROM juhe_business.proxy_profiles p", errors.New("snapshot boom"))
	if err := s1.CheckContract(ctx); err == nil || !strings.Contains(err.Error(), "快照查询失败") {
		t.Fatalf("快照查询失败 err=%v", err)
	}
	// 审计 EXPLAIN 失败。
	rec2 := newWFRecorder()
	s2 := source(t, rec2)
	rec2.script("has_table_privilege", []string{"ok"}, [][]driver.Value{{true}})
	rec2.failExec("operation_log_targets", errors.New("explain boom"))
	if err := s2.CheckContract(ctx); err == nil || !strings.Contains(err.Error(), "审计表契约失败") {
		t.Fatalf("审计契约失败 err=%v", err)
	}
	// 权限读取失败。
	rec3 := newWFRecorder()
	s3 := source(t, rec3)
	rec3.script("has_table_privilege", []string{"ok"}, [][]driver.Value{{true}})
	rec3.failQuery("juhe_dataset.operation_logs', 'INSERT'", errors.New("priv boom"))
	if err := s3.CheckContract(ctx); err == nil || !strings.Contains(err.Error(), "写权限失败") {
		t.Fatalf("写权限读取失败 err=%v", err)
	}
	// COMMIT 失败。
	rec4 := newWFRecorder()
	s4 := source(t, rec4)
	rec4.script("has_table_privilege", []string{"ok"}, [][]driver.Value{{true}})
	rec4.script("has_table_privilege", []string{"ok"}, [][]driver.Value{{true}})
	rec4.commitErr = errors.New("commit boom")
	// COMMIT 失败最先在 auth 契约事务提交时暴露，原始错误保留。
	if err := s4.CheckContract(ctx); err == nil || !strings.Contains(err.Error(), "commit boom") {
		t.Fatalf("COMMIT 失败 err=%v", err)
	}
}

func TestWFManualAdminExistsAndSnapshotFailures(t *testing.T) {
	rec := newWFRecorder()
	db := wfOpenRecorderDB(t, rec)
	source, _ := NewPostgresManualAdminSource(db, func() time.Time { return wfProjBase })
	ctx := context.Background()
	// 快照行扫描类型错误。
	rec.script("FROM juhe_business.proxy_profiles p", wfSnapshotColumns(), [][]driver.Value{{
		"p-1", "代理一", "http", "10.0.0.1", "bad-port", "user", "", wfProjRevision,
		"passed", nil, nil, nil, nil, nil, "gpt", "gpt", "profile-gpt", "https://api.openai.com/v1",
	}})
	if _, err := source.LoadSnapshot(ctx, "p-1", time.Minute); err == nil || !strings.Contains(err.Error(), "解码") {
		t.Fatalf("扫描错误 err=%v", err)
	}
	// Exists 查询错误已在其他用例覆盖；这里补快照行 ErrNoRows 之外的遍历错误。
	rec2 := newWFRecorder()
	db2 := wfOpenRecorderDB(t, rec2)
	source2, _ := NewPostgresManualAdminSource(db2, func() time.Time { return wfProjBase })
	rec2.scriptRowsErr("FROM juhe_business.proxy_profiles p", errors.New("row boom"))
	if _, err := source2.LoadSnapshot(ctx, "p-2", time.Minute); err == nil {
		t.Fatal("快照行迭代失败必须传播")
	}
}

func TestWFRunManualReusesCurrentOwnerLease(t *testing.T) {
	store := wfOpenJobsStore(t)
	business := wfOpenBusinessDB(t)
	projector := wfNewProjector(t, store, business)
	runner := NewRunner(RuntimeConfig{InstanceID: "wf-reuse", OwnerLease: time.Hour, ProxyLease: time.Hour}, store, &wfFakeReader{}, nil)
	runner.SetResultProjector(projector)
	ctx := context.Background()

	owner, ok, err := store.AcquireOwnerLease(ctx, "wf-reuse", time.Hour)
	if err != nil || !ok {
		t.Fatalf("owner lease ok=%v err=%v", ok, err)
	}
	proxy, ok, err := store.AcquireProxyLease(ctx, owner, "p-reuse", time.Hour)
	if err != nil || !ok {
		t.Fatalf("proxy lease ok=%v err=%v", ok, err)
	}
	_ = proxy
	runner.setOwnerLease(owner)
	runner.setOwnerHeld(true)

	// 复用当前 owner lease：计数器不得再次 acquire（hook 返回错误即暴露）。
	runner.acquireOwnerLease = func(context.Context, string, time.Duration) (OwnerLease, bool, error) {
		return OwnerLease{}, false, errors.New("不应再次 acquire")
	}
	runner.executeIssuedInput = func(_ context.Context, store *Store, owner OwnerLease, proxy ProxyLease, issued IssuedInput, _ ExecutorOptions) (Outcome, bool, error) {
		outcome, committed := wfCycleCommitOutcome(t, store, owner, proxy, issued)
		return outcome, committed, nil
	}

	wfSeedProxyRow(t, business, "p-1", wfProjRevision, nil)
	request := wfManualRequest()
	request.ProxyHost = "127.0.0.1"
	request.ProxyPort = 1
	if _, err := runner.RunManual(ctx, request); err != nil {
		t.Fatalf("复用 lease 的 RunManual 失败: %v", err)
	}

	// 释放代理后：runCycle 的代理释放失败记账（释放错误 + 非致命）。
	runner.releaseProxyLease = func(context.Context, ProxyLease) error {
		return errors.New("proxy release boom")
	}
	runner.reader = &wfFakeReader{drafts: []InputDraft{wfCycleDraft("p-reuse-2")}}
	runner.acquireProxyLease = func(context.Context, OwnerLease, string, time.Duration) (ProxyLease, bool, error) {
		proxy2, ok, err := store.AcquireProxyLease(ctx, owner, "p-reuse-2", time.Hour)
		if err != nil || !ok {
			return ProxyLease{}, false, errors.New("no lease")
		}
		return proxy2, true, nil
	}
	if err := runner.runCycle(ctx, owner); err != nil {
		t.Fatalf("释放失败不得致命: %v", err)
	}
	if runner.Status().ReleaseFailures != 1 {
		t.Fatalf("释放失败未记账：%+v", runner.Status())
	}
}

func TestWFHealthHandlerReady(t *testing.T) {
	store := wfOpenJobsStore(t)
	runner := NewRunner(RuntimeConfig{InstanceID: "wf-health", Enabled: true, OwnerLease: time.Hour}, store, &wfFakeReader{}, nil)
	runner.setOwnerHeld(true)
	owner, ok, err := store.AcquireOwnerLease(context.Background(), "wf-health", time.Hour)
	if err != nil || !ok {
		t.Fatalf("owner lease ok=%v err=%v", ok, err)
	}
	runner.setOwnerLease(owner)
	handler := runner.HealthHandler()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("GET", "/health", nil))
	if recorder.Body == nil || !strings.Contains(recorder.Body.String(), "j3aEnabled") {
		t.Fatalf("健康负载缺失：%s", recorder.Body.String())
	}
}
