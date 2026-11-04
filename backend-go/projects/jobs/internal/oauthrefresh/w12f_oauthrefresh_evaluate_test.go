package oauthrefresh

// w12f_oauthrefresh_evaluate_test.go 直调 evaluateScheduleRows 与
// selectBatchCandidates，覆盖状态同步的各归类分支与批次候选的过滤链。

import (
	"context"
	"testing"
	"time"
)

func TestW12fEvaluateScheduleRowsBranches(t *testing.T) {
	store, _, _ := newTestStore(t)
	now := w12fBaseNow()
	valid := w12fScheduleJSON(t)
	rows := []scheduleRow{
		{id: "w12f-invalid-active", status: "active", scheduleJSON: "{bad"},
		{id: "w12f-invalid-paused", status: "paused", scheduleJSON: "{bad"},
		{id: "w12f-valid-active", status: "active", scheduleJSON: valid},
		{id: "w12f-valid-paused", status: "paused", scheduleJSON: valid},
	}
	updates, result, err := store.evaluateScheduleRows(rows, now, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Invalid != 2 || result.Scanned != 4 {
		t.Fatalf("result=%+v", result)
	}
	if len(updates) != 4 {
		t.Fatalf("updates=%+v", updates)
	}
	// 2026-11-03 闸门契约：计划文档不可解析一律 fail-closed 关闸，与 status 无关；
	// 可解析的行按当前时刻（10:00，09:00-18:00 窗口内）开闸。
	wantGates := map[string]int64{
		"w12f-invalid-active": 0,
		"w12f-invalid-paused": 0,
		"w12f-valid-active":   1,
		"w12f-valid-paused":   1,
	}
	for _, update := range updates {
		want, ok := wantGates[update.id]
		if !ok || update.gate == nil || *update.gate != int(want) {
			t.Fatalf("%s gate=%+v want %d", update.id, update.gate, want)
		}
	}
	// 非账户模式（api key）同样的非法行归类。
	updates, result, err = store.evaluateScheduleRows(rows, now, false)
	if err != nil || result.Invalid != 2 {
		t.Fatalf("api key 模式 result=%+v err=%v", result, err)
	}
	for _, update := range updates {
		want, ok := wantGates[update.id]
		if !ok || update.gate == nil || *update.gate != int(want) {
			t.Fatalf("Key %s gate=%+v want %d", update.id, update.gate, want)
		}
	}
}

func TestW12fSelectBatchCandidatesFilterArms(t *testing.T) {
	job, _, db, clock, _ := newRefreshJobForTest(t)
	now := clock.Now()
	// 到期账户（被过滤排除）与未到期账户（shouldPreRefresh 排除）。
	seedOpenAIOAuthAccount(t, db, "w12f-due", openAICredentials(expiresInMillis(60_000)), now)
	seedOpenAIOAuthAccount(t, db, "w12f-far", openAICredentials(expiresInMillis(400_000)), now)
	// 缺 base_url 的账户 → 非 openai 形态排除分支。
	broken := openAICredentials(expiresInMillis(60_000))
	delete(broken, "base_url")
	seedOpenAIOAuthAccount(t, db, "w12f-broken", broken, now)

	result := &RefreshResult{}
	filter := map[string]bool{"w12f-due": false, "w12f-far": false, "w12f-broken": false}
	candidates, err := job.selectBatchCandidates(context.Background(), 300, 10, filter, now, 300_000, now.Add(time.Minute), result)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("全部被过滤必须零候选: %+v", candidates)
	}
	// 无 filter：到期账户入选。
	candidates, err = job.selectBatchCandidates(context.Background(), 300, 10, nil, now, 300_000, now.Add(time.Minute), result)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("无过滤必须入选到期与坏形态账户: %d", len(candidates))
	}
	for _, candidate := range candidates {
		if candidate.Account == nil || (candidate.Account.ID != "w12f-due" && candidate.Account.ID != "w12f-broken") {
			t.Fatalf("候选集必须只含 due/broken: %+v", candidate.Account)
		}
	}
}
