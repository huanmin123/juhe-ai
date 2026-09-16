package oauthrefresh

// w12f_oauthrefresh_final_test.go 补齐批处理候选的错误臂：failure store
// 读取失败、批次上限截断、ListDue 查询失败与 Rotate 的 BeginTx 失败。

import (
	"context"
	"errors"
	"testing"
	"time"
)

type w12fErrFailureStore struct{ FailureStateStore }

func (s *w12fErrFailureStore) Read(_ context.Context, _ string, _ int64, _ int64) (*RefreshFailureState, error) {
	return nil, errors.New("w12f-failure-store-read")
}

func TestW12fSelectBatchFailureStoreReadError(t *testing.T) {
	job, _, db, clock, _ := newRefreshJobForTest(t)
	seedOpenAIOAuthAccount(t, db, "w12f-due2", openAICredentials(expiresInMillis(60_000)), clock.Now())
	job.failures = &w12fErrFailureStore{}
	if _, err := job.selectBatchCandidates(context.Background(), 300, 10, nil, clock.Now(), 300_000, clock.Now().Add(time.Minute), &RefreshResult{}); err == nil {
		t.Fatal("failure store 读取失败必须传播")
	}
}

func TestW12fSelectBatchBatchSizeCap(t *testing.T) {
	job, _, db, clock, _ := newRefreshJobForTest(t)
	for _, id := range []string{"w12f-b1", "w12f-b2"} {
		seedOpenAIOAuthAccount(t, db, id, openAICredentials(expiresInMillis(60_000)), clock.Now())
	}
	candidates, err := job.selectBatchCandidates(context.Background(), 300, 1, nil, clock.Now(), 300_000, clock.Now().Add(time.Minute), &RefreshResult{})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("batchSize=1 必须截断为 1: %d", len(candidates))
	}
}

func TestW12fSelectBatchListDueError(t *testing.T) {
	closed := w12fClosedStore(t)
	job := NewRefreshJob(closed, nil, WithClock(ClockFunc(func() time.Time { return w12fBaseNow() })))
	if _, err := job.selectBatchCandidates(context.Background(), 300, 10, nil, w12fBaseNow(), 300_000, w12fBaseNow().Add(time.Minute), &RefreshResult{}); err == nil {
		t.Fatal("句柄关闭后 ListDue 必须报错")
	}
}

func TestW12fRotateBeginTxError(t *testing.T) {
	closed := w12fClosedStore(t)
	if _, err := closed.RotateCredentials(context.Background(), RotateCredentialsInput{
		AccountID:                        "w12f-x",
		ExpectedConfigRevision:           1,
		ExpectedProviderCode:             "gpt",
		ExpectedAccountType:              "oauth",
		ExpectedProviderProtocolProfileID: "p",
		Credentials:                      map[string]any{"refresh_token": "w12f-rt"},
	}); err == nil {
		t.Fatal("句柄关闭后 BeginTx 必须报错")
	}
	closedKeepalive := w12fClosedStore(t)
	if _, err := closedKeepalive.ListDueKeepaliveAccounts(context.Background(), "anthropic", "oauth", "", time.Hour, 5, w12fBaseNow()); err == nil {
		t.Fatal("句柄关闭后 keepalive 列表必须报错")
	}
	if _, err := closedKeepalive.MarkAccountFailureState(context.Background(), "w12f-x", "code", "reason", 1, "active"); err == nil {
		t.Fatal("句柄关闭后 MarkAccountFailureState 必须报错")
	}
	if _, _, err := closedKeepalive.ClearAccountFailureState(context.Background(), "w12f-x", nil); err == nil {
		t.Fatal("句柄关闭后 ClearAccountFailureState 必须报错")
	}
}
