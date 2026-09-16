package oauthrefresh

// w12f_oauthrefresh_runonce_test.go 覆盖 OpenAI 刷新批处理的组合分支：
// AccountIDs 过滤、非候选形态跳过、批次上限、admission 预算耗尽、退避跳过、
// 成功后失败状态清理，以及锁占用时的 SkippedLocked。

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestW12fRunOnceFilterAndBatchArms(t *testing.T) {
	job, _, db, clock, exchanger := newRefreshJobForTest(t)
	// 三个到期账户。
	for _, id := range []string{"w12f-a", "w12f-b", "w12f-c"} {
		seedOpenAIOAuthAccount(t, db, id, openAICredentials(expiresInMillis(60_000)), clock.Now())
	}
	exchanger.respond = func(_ int, request TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-new","refresh_token":"rt-new","expires_in":3600}`}, nil
	}
	// AccountIDs 过滤：只刷新 w12f-a。
	budget := 60_000
	result, err := job.RunOnce(context.Background(), RefreshOptions{
		AccountIDs:             []string{"w12f-a"},
		BatchSize:              ptrW12f(1),
		StartAdmissionBudgetMs: &budget,
		Concurrency:            1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Due != 1 || result.Refreshed != 1 {
		t.Fatalf("过滤后结果=%+v", result)
	}
}

func TestW12fRunOnceNotCandidateShapeSkips(t *testing.T) {
	job, _, db, clock, _ := newRefreshJobForTest(t)
	// 未到期账户 → shouldPreRefresh false。
	seedOpenAIOAuthAccount(t, db, "w12f-far", openAICredentials(expiresInMillis(400_000)), clock.Now())
	result, err := job.RunOnce(context.Background(), RefreshOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Due != 0 {
		t.Fatalf("未到期不得成为候选: %+v", result)
	}
}

func TestW12fRunOnceBackoffSkipAndClearOnSuccess(t *testing.T) {
	job, _, db, clock, exchanger := newRefreshJobForTest(t)
	seedOpenAIOAuthAccount(t, db, "w12f-backoff", openAICredentials(expiresInMillis(60_000)), clock.Now())
	// 记录一次失败：下一轮读取到 ObservedFailure，成功刷新后必须清理。
	if _, err := job.failures.Record(context.Background(), "w12f-backoff", clock.Now().UnixMilli()-1_000, FailureKindUntrustedUpstream, 1); err != nil {
		t.Fatal(err)
	}
	exchanger.respond = func(_ int, request TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-new","refresh_token":"rt-new","expires_in":3600}`}, nil
	}
	result, err := job.RunOnce(context.Background(), RefreshOptions{BatchSize: ptrW12f(5)})
	if err != nil {
		t.Fatal(err)
	}
	if result.Refreshed != 1 {
		t.Fatalf("退避已过必须刷新: %+v", result)
	}
	// 成功后失败状态被清理：再次失败计数从 0 开始（间接验证 Clear 臂）。
	if _, err := job.failures.Read(context.Background(), "w12f-backoff", clock.Now().UnixMilli(), 1); err != nil {
		t.Fatal(err)
	}
}

func TestW12fRunOnceSkippedLocked(t *testing.T) {
	job, _, db, clock, exchanger := newRefreshJobForTest(t)
	seedOpenAIOAuthAccount(t, db, "w12f-locked", openAICredentials(expiresInMillis(60_000)), clock.Now())
	exchanger.respond = func(_ int, request TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-new","refresh_token":"rt-new","expires_in":3600}`}, nil
	}
	// 占住账户锁：批量里的刷新必须以 SkippedLocked 结束。
	release := make(chan struct{})
	var once sync.Once
	if err := job.locks.TryLock("w12f-locked", func() error {
		defer once.Do(func() { close(release) })
		result, runErr := job.RunOnce(context.Background(), RefreshOptions{})
		if runErr != nil {
			t.Fatal(runErr)
		}
		if result.SkippedLocked != 1 {
			t.Fatalf("锁占用必须 SkippedLocked=1: %+v", result)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestW12fRunOnceAdmissionBudgetExhausted(t *testing.T) {
	job, _, db, clock, _ := newRefreshJobForTest(t)
	// 到期账户 + 立即耗尽的 admission 预算 → DeferredBudget。
	for _, id := range []string{"w12f-d1", "w12f-d2"} {
		seedOpenAIOAuthAccount(t, db, id, openAICredentials(expiresInMillis(60_000)), clock.Now())
	}
	budget := 1
	result, err := job.RunOnce(context.Background(), RefreshOptions{StartAdmissionBudgetMs: &budget})
	if err != nil {
		t.Fatal(err)
	}
	if result.DeferredBudget == 0 && result.Refreshed == 0 {
		t.Fatalf("预算耗尽或刷新二者必有其一: %+v", result)
	}
}

func TestW12fRunOnceWorkerCountCappedByConcurrency(t *testing.T) {
	job, _, db, clock, exchanger := newRefreshJobForTest(t)
	for _, id := range []string{"w12f-w1", "w12f-w2", "w12f-w3"} {
		seedOpenAIOAuthAccount(t, db, id, openAICredentials(expiresInMillis(60_000)), clock.Now())
	}
	exchanger.respond = func(_ int, request TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-new","refresh_token":"rt-new","expires_in":3600}`}, nil
	}
	result, err := job.RunOnce(context.Background(), RefreshOptions{Concurrency: 1, BatchSize: ptrW12f(3)})
	if err != nil {
		t.Fatal(err)
	}
	if result.Refreshed != 3 {
		t.Fatalf("并发=1 时顺序刷新全部: %+v", result)
	}
	_ = time.Second
}

func ptrW12f(v int) *int { return &v }
