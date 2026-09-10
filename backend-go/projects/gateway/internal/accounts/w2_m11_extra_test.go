package accounts

// W2 M11 补充测试：读面辅助函数与端点的小分支（m11_reads.go /
// m11_routes.go / m11_balance.go / m11_authorized_dispatch.go）。主契约由
// m11_test.go 覆盖，此处补齐 reader 注入、读错误映射与纯函数分支。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeAPIKeyRuntimeDetails 是 APIKeyRuntimeDetailsReader 的可回放桩。
type fakeAPIKeyRuntimeDetails struct {
	items []map[string]any
	err   error
}

func (f *fakeAPIKeyRuntimeDetails) LoadAPIKeyRuntimeDetails(_ context.Context, _ string) ([]map[string]any, error) {
	return f.items, f.err
}

func TestW2APIKeyRuntimeReaderPort(t *testing.T) {
	env, _ := newM11TestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedM11Account(t, "acc-runtime-w2", adminID, "运行时账户", "api_key", "active",
		Credentials{"api_key": "sk-runtime-secret"})

	// 未注入 reader：items 为空列表。
	response, err := env.store.LoadAPIKeyRuntimeResponse(context.Background(), &APIKeyRuntimeAccount{
		ID: "acc-runtime-w2", AccessType: "owner", ConfigRevision: 1,
	})
	if err != nil || response == nil || len(response.Items) != 0 {
		t.Fatalf("无 reader 响应不一致：%+v %v", response, err)
	}
	// 非 owner（授权实例）不读 runtime 细节。
	response, err = env.store.LoadAPIKeyRuntimeResponse(context.Background(), &APIKeyRuntimeAccount{
		ID: "acc-runtime-w2", AccessType: "authorized", ConfigRevision: 1,
	})
	if err != nil || response != nil {
		t.Fatalf("授权实例应返回 nil：%+v %v", response, err)
	}

	// 注入 reader：正常返回与错误透传。
	details := &fakeAPIKeyRuntimeDetails{items: []map[string]any{{"fingerprint": "fp-1", "status": "active"}}}
	env.store.SetAPIKeyRuntimeDetailsReader(details)
	response, err = env.store.LoadAPIKeyRuntimeResponse(context.Background(), &APIKeyRuntimeAccount{
		ID: "acc-runtime-w2", AccessType: "owner", ConfigRevision: 7,
	})
	if err != nil || response == nil || len(response.Items) != 1 || response.ConfigRevision != 7 {
		t.Fatalf("reader 注入后响应不一致：%+v %v", response, err)
	}
	details.err = errors.New("读取失败")
	if _, err = env.store.LoadAPIKeyRuntimeResponse(context.Background(), &APIKeyRuntimeAccount{
		ID: "acc-runtime-w2", AccessType: "owner",
	}); err == nil {
		t.Fatal("reader 错误应透传")
	}
}

func TestW2WriteM11ReadError(t *testing.T) {
	// 行为存疑：m11 读面的 writeM11ReadError 对所有错误（包括
	// ValidationError）统一渲染 500，而 test 面的 writeTestError 对
	// ValidationError 渲染 400。按当前实际行为断言。
	recorder := httptest.NewRecorder()
	(&Deps{}).writeM11ReadError(recorder, &ValidationError{Message: "参数不合法"})
	if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), "服务器内部错误") {
		t.Fatalf("统一 500 映射不一致：%d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	(&Deps{}).writeM11ReadError(recorder, errors.New("boom"))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("500 映射不一致：%d", recorder.Code)
	}
}

func TestW2M11PureHelpers(t *testing.T) {
	t.Run("sqlTextOr", func(t *testing.T) {
		if got := sqlTextOr(sqlNullString("值"), "回退"); got != "值" {
			t.Fatalf("有效值应直通：%q", got)
		}
		if got := sqlTextOr(sqlNullString("  "), "回退"); got != "回退" {
			t.Fatalf("空白应回退：%q", got)
		}
	})
	t.Run("instantLater 与 instantNotAfter", func(t *testing.T) {
		if later, ok := instantLater("2026-01-02T00:00:00Z", "2026-01-01T00:00:00Z"); !ok || !later {
			t.Fatal("右侧更早应判定 later")
		}
		if later, ok := instantLater("bad", "2026-01-01T00:00:00Z"); ok || later {
			t.Fatal("非法时间应不可判定")
		}
		if notAfter, ok := instantNotAfter("2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z"); !ok || !notAfter {
			t.Fatal("不晚于判定不一致")
		}
	})
	t.Run("containsStatusChanged", func(t *testing.T) {
		if !containsStatusChanged("账户状态已变化：active -> disabled") {
			t.Fatal("应命中状态变化文案")
		}
		if containsStatusChanged("其他消息") {
			t.Fatal("普通消息不应命中")
		}
	})
	t.Run("m11ScheduleAllowed", func(t *testing.T) {
		now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
		if !m11ScheduleAllowed("", now) {
			t.Fatal("空计划默认放行")
		}
		if !m11ScheduleAllowed("not-json", now) {
			t.Fatal("坏计划默认放行")
		}
		// always-allow 计划放行（与 m09 契约窗口一致）。
		if !m11ScheduleAllowed(alwaysAllowSchedule, now) {
			t.Fatal("allow 窗口应放行")
		}
		// 01:00-02:00 之外的 UTC 时刻拒绝。
		deny := `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[
			{"daysOfWeek":[1,2,3,4,5,6,7],"start":"01:00","end":"02:00"}]}`
		if m11ScheduleAllowed(deny, now) {
			t.Fatal("窗口外应拒绝")
		}
	})
	t.Run("statsTable 方言", func(t *testing.T) {
		if env, _ := newM11TestEnv(t); env != nil {
			if env.store.statsTable("account_usage_snapshots") == "juhe_stats.account_usage_snapshots" {
				t.Fatal("SQLite 方言不应带 schema 前缀")
			}
		}
	})
	t.Run("balanceSnapshotTimestampMs", func(t *testing.T) {
		if ms, ok := balanceSnapshotTimestampMs("2026-01-02T03:04:05.006Z"); !ok || ms <= 0 {
			t.Fatalf("ISO 时间戳解析不一致：%d %v", ms, ok)
		}
		if _, ok := balanceSnapshotTimestampMs("garbage"); ok {
			t.Fatal("垃圾输入应不可解析")
		}
	})
}

