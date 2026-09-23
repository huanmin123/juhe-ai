package oauthrefresh

// w12f_oauthrefresh_sweep_test.go 覆盖授权过期 sweep 的方言分支与错误臂、
// 各供应商刷新的传输错误传播，以及 RotateCredentials 的成功事务路径。

import (
	"context"
	"errors"
	"testing"
	"time"
)

type w12fErrExchanger struct{ message string }

func (e *w12fErrExchanger) Do(_ context.Context, _ TokenHTTPRequest) (TokenHTTPResponse, error) {
	return TokenHTTPResponse{}, errors.New(e.message)
}

func TestW12fSweepPGDialectAndClosedArms(t *testing.T) {
	store, db, _ := newTestStore(t)
	ctx := context.Background()
	// 空 grants 表：正常空扫描（SQLite 方言）。
	result, err := store.RunAuthorizationExpirySweep(ctx, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	_ = result
	// pg 方言拼接 FOR UPDATE SKIP LOCKED 后缀（SQLite 不识别，报语法错误
	// 即证明方言分支已执行；真实 PG 路径由门控测试覆盖）。
	pgStore := &Store{db: db, pg: true, secret: "w12f", now: store.now}
	if _, err := pgStore.RunAuthorizationExpirySweep(ctx, nil, 10); err == nil {
		t.Fatal("SQLite 上执行 PG 方言必须报错")
	}
	// 句柄关闭：Begin 失败。
	closed := w12fClosedStore(t)
	if _, err := closed.RunAuthorizationExpirySweep(ctx, nil, 10); err == nil {
		t.Fatal("句柄关闭后 sweep 必须报错")
	}
}

func TestW12fProviderRefreshTransportErrors(t *testing.T) {
	ex := &w12fErrExchanger{message: "w12f-transport-down"}
	ctx := context.Background()
	if _, err := RefreshAnthropicToken(ctx, ex, "w12f-rt", "", time.Now(), ""); err == nil {
		t.Fatal("anthropic 传输错误必须传播")
	}
	fallback := GeminiCredentialFallback{OAuthType: "ai_studio", ClientID: "w12f-cid", ClientSecret: "w12f-secret"}
	if _, err := RefreshGeminiToken(ctx, ex, "w12f-rt", fallback, time.Now()); err == nil {
		t.Fatal("gemini 传输错误必须传播")
	}
	if _, err := RefreshGrokToken(ctx, ex, "w12f-rt", "", time.Now(), ""); err == nil {
		t.Fatal("grok 传输错误必须传播")
	}
}

func TestW12fRotateCredentialsSuccessTx(t *testing.T) {
	store, db, _ := newTestStore(t)
	ctx := context.Background()
	now := w12fBaseNow()
	seedProviderProfiles(t, db)
	seedOpenAIOAuthAccount(t, db, "w12f-rot-ok", openAICredentials(expiresInMillis(0)), now)
	next := openAICredentials(expiresInMillis(0))
	next["access_token"] = "w12f-at-rotated"
	result, err := store.RotateCredentials(ctx, RotateCredentialsInput{
		AccountID:                        "w12f-rot-ok",
		ExpectedProviderCode:             "gpt",
		ExpectedAccountType:              "oauth",
		ExpectedProviderProtocolProfileID: "profile_gpt_openai_v1",
		ExpectedConfigRevision:           1,
		Credentials:                      next,
	})
	if err != nil || result == nil || !result.Changed {
		t.Fatalf("旋转必须成功: %+v err=%v", result, err)
	}
	if result.ConfigRevision != 2 {
		t.Fatalf("旋转后 revision=%d", result.ConfigRevision)
	}
}
