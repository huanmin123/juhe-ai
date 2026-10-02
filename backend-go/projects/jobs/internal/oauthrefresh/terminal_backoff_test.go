package oauthrefresh

import (
	"context"
	"testing"
	"time"
)

// 已终态（error × 本家族管理码）账户的刷新失败退避为 24h：确定性死亡的
// refresh token 不再每 300s 重试一次（weisn537301 形态：两个半月 1100+
// 次无效 401）；active 账户维持常规 300s 退避不变；24h 到期后仍保留一次
// 重试（自动恢复通道不受影响）。
func TestRefreshJobTerminalAccountUses24hBackoff(t *testing.T) {
	job, _, db, clock, exchanger := newRefreshJobForTest(t)
	seedAccountRow(t, db, accountRowSeed{
		ID: "acc-terminal-24h", ProviderCode: "gpt", ProfileID: ProfileGPTOpenAIV1, Type: "oauth",
		Status: "error", LastErrorCode: OpenAIOAuthTokenRefreshFailedErrorCode,
		Credentials: openAICredentials(expiresInMillis(0)), Now: clock.Now(),
	})
	seedAccountRow(t, db, accountRowSeed{
		ID: "acc-active-300s", ProviderCode: "gpt", ProfileID: ProfileGPTOpenAIV1, Type: "oauth",
		Status: "active",
		Credentials: openAICredentials(expiresInMillis(0)), Now: clock.Now(),
	})
	exchanger.respond = func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 401, Body: `{"error":"invalid_grant"}`}, nil
	}

	// 第一次失败：两个账户各自记退避（终态 24h / active 300s）。
	first, err := job.RunOnce(context.Background(), RefreshOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Failed != 2 {
		t.Fatalf("first run failed=%d want 2 (%+v)", first.Failed, first)
	}
	// 精确锁定终态退避 = now + 24h（防止常量回归成 1h/4h 仍通过区间断言）。
	terminalState, readErr := job.failures.Read(context.Background(), "acc-terminal-24h",
		clock.Now().UnixMilli(), 1)
	if readErr != nil || terminalState == nil {
		t.Fatalf("terminal failure state read: state=%v err=%v", terminalState, readErr)
	}
	wantBackoff := clock.Now().Add(24 * time.Hour).UnixMilli()
	if terminalState.BackoffUntil != wantBackoff {
		t.Fatalf("terminal backoffUntil=%d want %d (24h)",
			terminalState.BackoffUntil, wantBackoff)
	}

	// 推进 6 分钟（> 常规 300s）：
	// active 账户退避到期重试；终态账户仍在 24h 退避内被跳过。
	clock.current = clock.current.Add(6 * time.Minute)
	second, err := job.RunOnce(context.Background(), RefreshOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if second.Failed != 1 || second.SkippedBackoff != 1 {
		t.Fatalf("second run failed=%d skippedBackoff=%d want 1/1 (%+v)",
			second.Failed, second.SkippedBackoff, second)
	}

	// 再推进 24h：终态账户退避到期，重试一次（恢复通道仍在）。
	clock.current = clock.current.Add(24 * time.Hour)
	third, err := job.RunOnce(context.Background(), RefreshOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if third.Failed < 1 {
		t.Fatalf("third run must retry the terminal account after 24h backoff (%+v)", third)
	}
}
