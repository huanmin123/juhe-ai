package oauthrefresh

// w12f_oauthrefresh_storepaths_test.go 覆盖 RotateCredentials/keepalive 的
// 业务语义错误臂：provider/type/profile 不匹配、配置版本冲突、凭据解密
// 不可用、缺 required source、keepalive 刷新链的冲突恢复分支。

import (
	"context"
	"errors"
	"testing"
	"time"
)

func w12fBaseNow() time.Time { return time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC) }

func TestW12fRotateCredentialsMismatchArms(t *testing.T) {
	store, db, _ := newTestStore(t)
	ctx := context.Background()
	now := w12fBaseNow()
	seedProviderProfiles(t, db)
	seedOpenAIOAuthAccount(t, db, "w12f-rot", openAICredentials(expiresInMillis(0)), now)

	base := RotateCredentialsInput{
		AccountID:                        "w12f-rot",
		ExpectedProviderCode:             "gpt",
		ExpectedAccountType:              "oauth",
		ExpectedProviderProtocolProfileID: "profile_gpt_openai_v1",
		ExpectedConfigRevision:           1,
	}

	// provider 不匹配 → nil, nil。
	mismatch := base
	mismatch.ExpectedProviderCode = "anthropic"
	result, err := store.RotateCredentials(ctx, mismatch)
	if err != nil || result != nil {
		t.Fatalf("provider 不匹配必须 nil,nil: %v %v", result, err)
	}
	// accountType 不匹配。
	mismatch = base
	mismatch.ExpectedAccountType = "api_key"
	if result, err = store.RotateCredentials(ctx, mismatch); err != nil || result != nil {
		t.Fatalf("type 不匹配必须 nil,nil: %v %v", result, err)
	}
	// profile 不匹配。
	mismatch = base
	mismatch.ExpectedProviderProtocolProfileID = "profile_anthropic_anthropic_v1"
	if result, err = store.RotateCredentials(ctx, mismatch); err != nil || result != nil {
		t.Fatalf("profile 不匹配必须 nil,nil: %v %v", result, err)
	}
	// 配置版本冲突。
	conflict := base
	conflict.ExpectedConfigRevision = 42
	if _, err = store.RotateCredentials(ctx, conflict); err == nil {
		t.Fatal("版本冲突必须报错")
	} else {
		var conflictErr *RevisionConflictError
		if !errors.As(err, &conflictErr) {
			t.Fatalf("必须为 RevisionConflictError: %v", err)
		}
	}
	// credentials 相同 → Changed=false 短路。
	same := base
	same.Credentials = openAICredentials(expiresInMillis(0))
	result, err = store.RotateCredentials(ctx, same)
	if err != nil || result == nil || result.Changed {
		t.Fatalf("相同凭据必须未变更: %+v err=%v", result, err)
	}
	// required source 为空（凭据无任何可用来源）→ 报错。
	missing := base
	missing.Credentials = map[string]any{"unrelated": "w12f"}
	if _, err = store.RotateCredentials(ctx, missing); err == nil {
		t.Fatal("凭据无可用来源必须报错")
	}
}

func TestW12fKeepaliveRefreshOneArms(t *testing.T) {
	store, db, _ := newTestStore(t)
	ctx := context.Background()
	now := w12fBaseNow()
	seedProviderProfiles(t, db)
	job := NewKeepaliveJob(store, nil, WithKeepaliveClock(ClockFunc(func() time.Time { return now })))

	// 不支持的供应商。
	plan := KeepalivePlan{Provider: "w12f-unknown", Lead: time.Hour}
	account := &RotationAccount{ID: "w12f-unknown", ConfigRevision: 1}
	if _, err := job.refreshOne(ctx, plan, account, now); err == nil {
		t.Fatal("不支持的供应商必须报错")
	}
	// 源账户缺失 → LocalConfigurationError。
	seedAccountRow(t, db, accountRowSeed{ID: "w12f-ka", ProviderCode: "gpt", ProfileID: "profile_gpt_openai_v1", Type: "oauth", Credentials: openAICredentials(expiresInMillis(0)), Now: now})
	missingPlan := KeepalivePlan{Provider: ProviderAnthropic, AccountType: "oauth", Lead: time.Hour}
	if _, err := job.refreshOne(ctx, missingPlan, &RotationAccount{ID: "w12f-ghost", ConfigRevision: 1}, now); err == nil {
		t.Fatal("源账户缺失必须报错")
	}
	// isRefreshable 不匹配（provider 冲突）→ LocalConfigurationError。
	mismatchPlan := KeepalivePlan{Provider: ProviderAnthropic, AccountType: "oauth", Lead: time.Hour}
	if _, err := job.refreshOne(ctx, mismatchPlan, &RotationAccount{ID: "w12f-ka", ConfigRevision: 1}, now); err == nil {
		t.Fatal("provider 不匹配必须报错")
	}
	// keepalive 仅支持 anthropic/gemini/xai；openai 账户命中不支持分支。
	unsupported := KeepalivePlan{Provider: "gpt", AccountType: "oauth", Lead: time.Hour}
	seedAccountRow(t, db, accountRowSeed{ID: "w12f-fresh", ProviderCode: "gpt", ProfileID: "profile_gpt_openai_v1", Type: "oauth", Credentials: openAICredentials(expiresInMillis(int64((6*time.Hour)/time.Millisecond))), Now: now})
	if _, err := job.refreshOne(ctx, unsupported, &RotationAccount{ID: "w12f-fresh", ConfigRevision: 1}, now); err == nil {
		t.Fatal("不支持的保活供应商必须报错")
	}
	// anthropic 匹配但凭据新鲜（无需刷新）→ false, nil。
	seedAccountRow(t, db, accountRowSeed{ID: "w12f-anth", ProviderCode: "anthropic", ProfileID: "profile_anthropic_anthropic_v1", Type: "oauth", Credentials: map[string]any{"access_token": "w12f-at", "refresh_token": "w12f-rt", "expires_at": isoMillis(now.Add(6 * time.Hour))}, Now: now})
	freshPlan := KeepalivePlan{Provider: ProviderAnthropic, AccountType: "oauth", Lead: time.Hour}
	changed, err := job.refreshOne(ctx, freshPlan, &RotationAccount{ID: "w12f-anth", ConfigRevision: 1}, now)
	if err != nil || changed {
		t.Fatalf("新鲜凭据必须跳过: changed=%v err=%v", changed, err)
	}
	// anthropic 到期但缺 refresh_token → LocalConfigurationError。
	seedAccountRow(t, db, accountRowSeed{ID: "w12f-anth-nort", ProviderCode: "anthropic", ProfileID: "profile_anthropic_anthropic_v1", Type: "oauth", Credentials: map[string]any{"access_token": "w12f-at", "expires_at": expiresInMillis(-int64(time.Hour / time.Millisecond))}, Now: now})
	if _, err := job.refreshOne(ctx, freshPlan, &RotationAccount{ID: "w12f-anth-nort", ConfigRevision: 1}, now); err == nil {
		t.Fatal("缺 refresh_token 必须报错")
	}
}
