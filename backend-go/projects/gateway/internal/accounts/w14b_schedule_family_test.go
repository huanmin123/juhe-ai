package accounts

// w14b 时间计划调度臂与家族版本推进锁存臂：例外日期 allow/deny 边界、
// 跨午夜窗口出现、zoned 本地分钟换算失败分支、家族根重查错误臂。

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestW14BScheduleExceptionAndOccurrenceArms(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC) // 周五
	// deny 例外日期命中：无出现 → allowedScheduleOccurrences 空。
	denySchedule := &AvailabilitySchedule{
		Enabled: true, Timezone: "UTC",
		Windows: []ScheduleWindow{{DaysOfWeek: []int{5}, Start: "00:01", End: "23:59"}},
		Exceptions: []ScheduleException{
			{Date: "2026-09-18", Action: "deny"},
		},
	}
	parts := scheduleZonedParts(now, "UTC")
	if got := allowedScheduleOccurrences(denySchedule, parts); len(got) != 0 {
		t.Fatalf("deny 例外应清空出现：%+v", got)
	}
	// allow 例外：now 取窗口内时刻（exception allow 分支 + 跨午夜窗口出现）。
	nowInWindow := time.Date(2026, 9, 18, 23, 30, 0, 0, time.UTC)
	allowSchedule := &AvailabilitySchedule{
		Enabled: true, Timezone: "UTC",
		Windows: []ScheduleWindow{{DaysOfWeek: []int{1}, Start: "09:00", End: "10:00"}},
		Exceptions: []ScheduleException{
			{Date: "2026-09-18", Action: "allow", Windows: []ScheduleWindow{{Start: "23:00", End: "01:00"}}},
		},
	}
	got := allowedScheduleOccurrences(allowSchedule, scheduleZonedParts(nowInWindow, "UTC"))
	if len(got) == 0 {
		t.Fatal("allow 例外应产生出现")
	}
	foundStart := false
	for _, occurrence := range got {
		if strings.Contains(occurrence.key, "start:23:00") {
			foundStart = true
		}
	}
	if !foundStart {
		t.Fatalf("应包含跨午夜窗口开始键：%+v", got)
	}
	// 非法时区：zonedLocalMinuteToUTC false 分支。
	if _, ok := zonedLocalMinuteToUTC("2026-09-18", 540, "Bad/Zone"); ok {
		t.Fatal("非法时区应返回 false")
	}
	// DST 春季跳变缺口内不存在的本地时刻应返回 false（2026-03-08 美东 02:30）。
	if _, ok := zonedLocalMinuteToUTC("2026-03-08", 150, "America/New_York"); ok {
		t.Fatal("DST 缺口时刻应返回 false")
	}
	// scheduleZonedParts 非法时区回退 UTC。
	if parts := scheduleZonedParts(now, "Bad/Zone"); parts.dateKey != "2026-09-18" {
		t.Fatalf("非法时区应回退 UTC：%+v", parts)
	}
	// 无效计划 JSON 解析回退。
	if _, err := NormalizeSchedule(map[string]any{"enabled": true, "mode": "allow_windows",
		"timezone": 3}); err == nil {
		t.Fatal("时区非字符串应报错")
	}
	if _, err := NormalizeSchedule(map[string]any{"enabled": true, "mode": "allow_windows",
		"windows": []any{map[string]any{"start": "09:00", "end": "18:00", "daysOfWeek": []any{}}}}); err == nil {
		t.Fatal("空 daysOfWeek 应报错")
	}
	if _, err := NormalizeSchedule(map[string]any{"enabled": true, "mode": "allow_windows",
		"windows":  []any{map[string]any{"start": "18:00", "end": "09:00", "daysOfWeek": []any{1}}},
		"dateRange": map[string]any{"startDate": "2026-03-01", "endDate": "bad"}}); err == nil {
		t.Fatal("非法结束日期应报错")
	}
}

func TestW14BBatchEffectsFamilyArms(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedAccount(t, "acc-w14b-fx", adminID, "w14b-fx", "active")
	ctx := context.Background()

	// 家族根推进：正常账户（家族根 = 自身）。
	tx, err := env.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = env.store.advanceBatchDispatchRevisionFamily(ctx, tx, batchDispatchRevision{
		accountID: "acc-w14b-fx", transitionID: "w14b-fx-1", nowMS: time.Now().UnixMilli(),
	})
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("家族推进应成功：%v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// 幂等重放：同一 transitionID 再次推进不报错。
	tx2, err := env.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = env.store.advanceBatchDispatchRevisionFamily(ctx, tx2, batchDispatchRevision{
		accountID: "acc-w14b-fx", transitionID: "w14b-fx-1", nowMS: time.Now().UnixMilli(),
	})
	if err != nil {
		_ = tx2.Rollback()
		t.Fatalf("幂等重放应成功：%v", err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatal(err)
	}
	// 不存在的账户 → 账户不存在错误臂。
	tx3, err := env.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = env.store.advanceBatchDispatchRevisionFamily(ctx, tx3, batchDispatchRevision{
		accountID: "acc-w14b-fx-none", transitionID: "w14b-fx-2", nowMS: time.Now().UnixMilli(),
	})
	if err == nil || !strings.Contains(err.Error(), "AI 账户不存在") {
		t.Fatalf("缺失账户应报错：%v", err)
	}
	_ = tx3.Rollback()
	// advanceBatchDispatchRevision 单家族入口。
	tx4, err := env.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.advanceBatchDispatchRevision(ctx, tx4, "acc-w14b-fx", "w14b-fx-3", time.Now().UnixMilli()); err != nil {
		_ = tx4.Rollback()
		t.Fatalf("单家族推进应成功：%v", err)
	}
	if err := tx4.Commit(); err != nil {
		t.Fatal(err)
	}
}
