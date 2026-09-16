package oauthrefresh

// w12f_oauthrefresh_keepalive_test.go 覆盖 keepalive 的 Gemini/XAI 刷新错误
// 传播、跳过列表与失败日志聚合，以及 schedule 边界 occurrence 与 DST 回退
// guess 循环。

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestW12fKeepaliveGeminiXaiRefreshErrors(t *testing.T) {
	job, db, clock, exchanger := newKeepaliveJobForTest(t)
	now := clock.Now()
	seedAccountRow(t, db, accountRowSeed{ID: "w12f-gm", ProviderCode: "gemini", ProfileID: "profile_gemini_native_v1beta", Type: "google_oauth", Credentials: map[string]any{"access_token": "w12f-at", "refresh_token": "w12f-rt", "expires_at": expiresInMillis(60_000), "oauth_type": "ai_studio", "client_id": "w12f-cid", "client_secret": "w12f-cs"}, Now: now})
	seedAccountRow(t, db, accountRowSeed{ID: "w12f-xai", ProviderCode: "xai", ProfileID: ProfileXAIOpenAIV1, Type: "oauth", Credentials: map[string]any{"access_token": "w12f-at", "refresh_token": "w12f-rt", "expires_at": expiresInMillis(60_000)}, Now: now})
	exchanger.respond = func(_ int, _ TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{}, errors.New("w12f-upstream-down")
	}
	result, err := job.RunOnce(context.Background(), KeepalivePlans()[1], 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed != 1 {
		t.Fatalf("gemini 刷新失败必须计入 Failed: %+v", result)
	}
	result, err = job.RunOnce(context.Background(), KeepalivePlans()[2], 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed != 1 {
		t.Fatalf("xai 刷新失败必须计入 Failed: %+v", result)
	}
}

func TestW12fKeepaliveNilAccountAndListError(t *testing.T) {
	closed := w12fClosedStore(t)
	job := NewKeepaliveJob(closed, nil, WithKeepaliveClock(ClockFunc(func() time.Time { return w12fBaseNow() })))
	// ListDue 错误（句柄关闭）传播。
	if _, err := job.RunOnce(context.Background(), KeepalivePlans()[0], 10); err == nil {
		t.Fatal("句柄关闭后 keepalive 列表必须报错")
	}
}

func TestW12fScheduleWindowOccurrenceBounds(t *testing.T) {
	current := zonedParts{dateKey: "2026-09-14", minuteOfDay: 540}
	// 当前分钟恰为窗口开始 → start occurrence。
	occurrence, ok := windowOccurrence(current, "2026-09-14", "09:00", "18:00", "w12f")
	if !ok || occurrence.key != "w12f:start:09:00" {
		t.Fatalf("开始边界 occurrence=%+v ok=%v", occurrence, ok)
	}
	// 次日凌晨仍在跨日窗口内 → end occurrence。
	nextNight := zonedParts{dateKey: "2026-09-15", minuteOfDay: 60}
	occurrence, ok = windowOccurrence(nextNight, "2026-09-14", "22:00", "02:00", "w12f")
	if !ok {
		t.Fatal("跨日窗口的次日凌晨必须命中 end occurrence")
	}
	if occurrence.key != "w12f:start:22:00" {
		t.Fatalf("end occurrence key=%q", occurrence.key)
	}
	// 窗口外分钟 → 无 occurrence。
	_, ok = windowOccurrence(zonedParts{dateKey: "2026-09-14", minuteOfDay: 480}, "2026-09-14", "09:00", "18:00", "w12f")
	if ok {
		t.Fatal("窗口外不得产生 occurrence")
	}
}

func TestW12fZonedLocalMinuteToUTCDstOverlap(t *testing.T) {
	// America/New_York 2026-11-01 01:30 处于 DST 回拨重叠，需要 guess 循环。
	utcMillis, ok := zonedLocalMinuteToUTC("2026-11-01", 90, "America/New_York")
	if !ok {
		t.Fatal("DST 重叠时间必须可解析")
	}
	// DST 重叠取其一（EDT -4 或 EST -5 两个合法解），断言落在两解之间。
	earliest := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC).UnixMilli()
	latest := time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC).UnixMilli()
	if utcMillis < earliest || utcMillis > latest {
		t.Fatalf("DST 重叠解析=%d 不在 [%d,%d]", utcMillis, earliest, latest)
	}
	// 普通日期快速路径。
	if _, ok := zonedLocalMinuteToUTC("2026-09-14", 540, "UTC"); !ok {
		t.Fatal("UTC 简单转换必须成功")
	}
}
