package modelcheckstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// ---- 构造与 schema 生命周期 ----

func TestOpenSQLiteLifecycle(t *testing.T) {
	if _, err := OpenSQLite(""); err == nil {
		t.Fatalf("空路径应报错")
	}
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "dataset.sqlite3"))
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("重复建表应幂等: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("重复关闭应幂等: %v", err)
	}
	var nilStore *Store
	if err := nilStore.Close(); err != nil {
		t.Fatalf("nil store 关闭应安全: %v", err)
	}
	if err := nilStore.EnsureSchema(context.Background()); err == nil {
		t.Fatalf("nil store 建表应报错")
	}
	if _, err := OpenPostgres("", 10, 5); err == nil {
		t.Fatalf("空 DSN 应报错")
	}
	pgStore, err := OpenPostgres("postgres://dataset:pw@127.0.0.1:1/juhe_jobs?connect_timeout=1", 0, 0)
	if err != nil {
		t.Fatalf("懒打开不应报错: %v", err)
	}
	if err := pgStore.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
}

// ---- 值校验与时间解码纯函数 ----

func TestValidEnumTables(t *testing.T) {
	for _, value := range []string{"high_confidence", "likely", "uncertain", "suspicious", "unavailable"} {
		if !validLevel(value) {
			t.Fatalf("级别 %s 应合法", value)
		}
	}
	if validLevel("excellent") {
		t.Fatalf("未知级别应非法")
	}
	for _, value := range []string{"running", "completed", "failed", "canceled"} {
		if !validStatus(value) {
			t.Fatalf("状态 %s 应合法", value)
		}
	}
	if validStatus("queued") {
		t.Fatalf("未知状态应非法")
	}
	for _, value := range []string{"manual", "scheduled", "quality_recovery"} {
		if !validTrigger(value) {
			t.Fatalf("触发方式 %s 应合法", value)
		}
	}
	if validTrigger("cron") {
		t.Fatalf("未知触发方式应非法")
	}
}

func TestReadTimestampVariants(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "ts.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	moment := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	if got, err := store.readTimestamp(moment); err != nil || got != "2026-09-10T08:00:00Z" {
		t.Fatalf("time.Time 应转 RFC3339Nano 文本: %q %v", got, err)
	}
	if got, err := store.readTimestamp("2026-09-10T08:00:00Z"); err != nil || got != "2026-09-10T08:00:00Z" {
		t.Fatalf("字符串应原样返回: %q %v", got, err)
	}
	if got, err := store.readTimestamp([]byte("2026-09-10T08:00:00Z")); err != nil || got != "2026-09-10T08:00:00Z" {
		t.Fatalf("字节应转字符串: %q %v", got, err)
	}
	if _, err := store.readTimestamp(42); err == nil {
		t.Fatalf("未知类型应报错")
	}
}

func TestDecodeObjectVariants(t *testing.T) {
	if got, err := decodeObject(nil); err != nil || len(got) != 0 {
		t.Fatalf("空载荷应返回空对象: %#v %v", got, err)
	}
	if got, err := decodeObject([]byte("null")); err != nil || len(got) != 0 {
		t.Fatalf("null 应返回空对象: %#v %v", got, err)
	}
	got, err := decodeObject([]byte(`{"k":1}`))
	if err != nil || got["k"] != float64(1) {
		t.Fatalf("合法对象应解析: %#v %v", got, err)
	}
	if _, err := decodeObject([]byte("[1]")); err == nil {
		t.Fatalf("非对象应报错")
	}
	if _, err := decodeObject([]byte("{broken")); err == nil {
		t.Fatalf("非法 JSON 应报错")
	}
}

func TestNullableIntEqualAndBoolInt(t *testing.T) {
	ten := int64(10)
	if !nullableIntEqual(&ten, sql.NullInt64{Int64: 10, Valid: true}) {
		t.Fatalf("相等值应匹配")
	}
	if nullableIntEqual(&ten, sql.NullInt64{Int64: 11, Valid: true}) {
		t.Fatalf("不同值不应匹配")
	}
	if !nullableIntEqual(nil, sql.NullInt64{}) {
		t.Fatalf("双方为空应匹配")
	}
	if !nullableIntEqual(nil, sql.NullInt64{Int64: 0, Valid: false}) {
		t.Fatalf("期望为空、实际 NULL 应匹配")
	}
	if nullableIntEqual(&ten, sql.NullInt64{}) {
		t.Fatalf("期望非空、实际为空不应匹配")
	}
	if boolInt(true) != 1 || boolInt(false) != 0 {
		t.Fatalf("布尔转整型不符")
	}
}
