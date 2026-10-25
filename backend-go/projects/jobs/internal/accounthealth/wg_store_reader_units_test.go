package accounthealth

import (
	"context"
	"database/sql"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
	"strings"
	"testing"
	"time"
)

// wgOutcomeBase 构造通过幂等/字段校验的最小 outcome。
func wgOutcomeBase(requestID string) Outcome {
	return Outcome{
		OutcomeID:        "oid-" + requestID,
		RequestID:        requestID,
		AccountID:        "acct-store",
		Outcome:          exactkeyprobe.OutcomeUpstreamFailed,
		ObservedAt:       time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
		InputVersion:     1,
		ConfigRevision:   2,
		DispatchRevision: 3,
	}
}

// wgStoreLease 获取一个测试租约并登记释放。
func wgStoreLease(t *testing.T, store *Store, owner string) OwnerLease {
	t.Helper()
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), owner, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("获取租约: %v %v", acquired, err)
	}
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(context.Background(), lease) })
	return lease
}

func TestParseDirectBoolMatrix(t *testing.T) {
	if value, err := parseDirectBool(sql.NullString{}); err != nil || value {
		t.Fatalf("NULL 必须为 false: %v %v", value, err)
	}
	if value, err := parseDirectBool(sql.NullString{Valid: true, String: "1"}); err != nil || !value {
		t.Fatalf("1 必须为 true: %v %v", value, err)
	}
	if value, err := parseDirectBool(sql.NullString{Valid: true, String: "0"}); err != nil || value {
		t.Fatalf("0 必须为 false: %v %v", value, err)
	}
	if _, err := parseDirectBool(sql.NullString{Valid: true, String: "yes"}); err == nil {
		t.Fatal("非法布尔必须报错")
	}
}

// TestParseNullableAndRequiredDirectTime 覆盖时间解析助手。
func TestParseNullableAndRequiredDirectTime(t *testing.T) {
	if value, err := parseNullableDirectTime(sql.NullString{}); err != nil || value != nil {
		t.Fatalf("NULL 必须为 nil: %v %v", value, err)
	}
	if value, err := parseNullableDirectTime(sql.NullString{Valid: true, String: " "}); err != nil || value != nil {
		t.Fatalf("空白必须为 nil: %v %v", value, err)
	}
	if _, err := parseNullableDirectTime(sql.NullString{Valid: true, String: "bad"}); err == nil {
		t.Fatal("非法时间必须报错")
	}
	parsed, err := parseRequiredDirectTime(sql.NullString{Valid: true, String: "2026-09-10T00:00:00Z"}, "expires")
	if err != nil || parsed.IsZero() {
		t.Fatalf("必填时间必须解析: %v %v", parsed, err)
	}
	if _, err := parseRequiredDirectTime(sql.NullString{}, "expires"); err == nil || !strings.Contains(err.Error(), "expires") {
		t.Fatalf("缺必填时间必须报错并带字段名: %v", err)
	}
}
