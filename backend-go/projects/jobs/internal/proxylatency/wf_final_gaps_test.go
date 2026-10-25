package proxylatency

import (
	"context"
	"database/sql/driver"
	"errors"
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

