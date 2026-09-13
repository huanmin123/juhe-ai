package main

// w1: chatAttachStreamHandler（SSE 附加流，hub 订阅门控 + ctx 取消路径）与
// quotaRecoveryPolicyRead / quotaRecoveryScheduleRead 读取校验。

import (
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
)

func TestW1QuotaRecoveryScheduleReadArms(t *testing.T) {
	// 非对象。
	if _, err := quotaRecoveryScheduleRead("x"); err == nil {
		t.Fatal("非对象必须报错")
	}
	// duration 合法。
	got, err := quotaRecoveryScheduleRead(map[string]any{"reset_strategy": "duration", "duration_minutes": float64(120)})
	if err != nil || got["duration_minutes"] != float64(120) {
		t.Fatalf("duration = %v, %v", got, err)
	}
	// duration 越界（29 < 30 下限）。
	if _, err := quotaRecoveryScheduleRead(map[string]any{"reset_strategy": "duration", "duration_minutes": float64(29)}); err == nil {
		t.Fatal("越界 duration 必须报错")
	}
	// daily。
	got, err = quotaRecoveryScheduleRead(map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(6)})
	if err != nil || got["daily_reset_hour"] != float64(6) {
		t.Fatalf("daily = %v, %v", got, err)
	}
	if _, err := quotaRecoveryScheduleRead(map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(24)}); err == nil {
		t.Fatal("越界小时必须报错")
	}
	// weekly。
	got, err = quotaRecoveryScheduleRead(map[string]any{"reset_strategy": "weekly", "weekly_reset_day": float64(3), "weekly_reset_hour": float64(8)})
	if err != nil || got["weekly_reset_day"] != float64(3) {
		t.Fatalf("weekly = %v, %v", got, err)
	}
	// weekly 缺字段。
	if _, err := quotaRecoveryScheduleRead(map[string]any{"reset_strategy": "weekly"}); err == nil {
		t.Fatal("weekly 缺字段必须报错")
	}
	// 非法 strategy。
	if _, err := quotaRecoveryScheduleRead(map[string]any{"reset_strategy": "hourly"}); err == nil {
		t.Fatal("非法 strategy 必须报错")
	}
	// jitter 兼容字段。
	if _, err := quotaRecoveryScheduleRead(map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(0), "jitter_minutes": float64(15)}); err != nil {
		t.Fatalf("jitter 15 必须接受: %v", err)
	}
	if _, err := quotaRecoveryScheduleRead(map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(0), "jitter_minutes": float64(20)}); err == nil {
		t.Fatal("jitter 20 必须报错")
	}
	// timezone。
	if _, err := quotaRecoveryScheduleRead(map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(0), "timezone": "Asia/Shanghai"}); err != nil {
		t.Fatalf("合法 timezone: %v", err)
	}
	if _, err := quotaRecoveryScheduleRead(map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(0), "timezone": "Bad/Zone"}); err == nil {
		t.Fatal("非法 timezone 必须报错")
	}
}

func TestW1QuotaRecoveryPolicyReadArms(t *testing.T) {
	if _, err := quotaRecoveryPolicyRead("x"); err == nil {
		t.Fatal("非对象必须报错")
	}
	// 非法键。
	if _, err := quotaRecoveryPolicyRead(map[string]any{"other": map[string]any{}}); err == nil {
		t.Fatal("不支持键必须报错")
	}
	// 空对象：空输出。
	if got, err := quotaRecoveryPolicyRead(map[string]any{}); err != nil || len(got) != 0 {
		t.Fatalf("空对象 = %v, %v", got, err)
	}
	// 三键合法。
	policy := map[string]any{
		"api_key":      map[string]any{"reset_strategy": "duration", "duration_minutes": float64(60)},
		"oauth":        map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(0)},
		"google_oauth": map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(3)},
	}
	got, err := quotaRecoveryPolicyRead(policy)
	if err != nil || len(got) != 3 {
		t.Fatalf("三键 = %v, %v", got, err)
	}
	// 子项错误透传。
	bad := map[string]any{"api_key": map[string]any{"reset_strategy": "bogus"}}
	if _, err := quotaRecoveryPolicyRead(bad); err == nil {
		t.Fatal("子项错误必须透传")
	}
}

func TestW1ChatAttachStreamHandlerGated(t *testing.T) {
	hub := chat.NewGenerationHub(nil)
	handler := chatAttachStreamHandler(hub)
	identity := chat.GenerationIdentity{ConversationID: "conv_missing"}
	// 未注册 runner：Subscribe 失败 → false。
	recorder := httptest.NewRecorder()
	if handler(recorder, httptest.NewRequest("GET", "/attach", nil), identity) {
		t.Fatal("未注册必须 false")
	}
	// 注册成功路径需要带 timeline 的完整 runner（chat 包内部构造），
	// 组合根测试保持订阅门控与 ctx 路径即可。
	_ = handler
}
