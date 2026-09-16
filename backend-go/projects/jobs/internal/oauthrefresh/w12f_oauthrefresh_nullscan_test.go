package oauthrefresh

// w12f_oauthrefresh_nullscan_test.go 用 credentials_encrypted 为 NULL 的行
// 触发 scan 层错误传播（非 ErrNoRows 的原始错误），以及 Update/Clear/Mark
// 的业务语义错误臂。

import (
	"context"
	"testing"
)

func TestW12fNullCredentialsScanArms(t *testing.T) {
	store, db, _ := newTestStore(t)
	ctx := context.Background()
	now := w12fBaseNow()
	seedProviderProfiles(t, db)
	// credentials_encrypted 为非 JSON 垃圾内容 → DecryptJSON 失败传播。
	seedAccountRow(t, db, accountRowSeed{ID: "w12f-null", ProviderCode: "gpt", ProfileID: "profile_gpt_openai_v1", Type: "oauth", Credentials: map[string]any{"refresh_token": "w12f-rt"}, Now: now})
	if _, err := db.Exec(`UPDATE accounts SET credentials_encrypted = 'w12f-garbage' WHERE id = 'w12f-null'`); err != nil {
		t.Fatal(err)
	}
	seedAccountRow(t, db, accountRowSeed{ID: "w12f-null2", ProviderCode: "gpt", ProfileID: "profile_gpt_openai_v1", Type: "oauth", Credentials: map[string]any{"refresh_token": "w12f-rt"}, Now: now})
	if _, err := db.Exec(`UPDATE accounts SET credentials_encrypted = 'w12f-garbage' WHERE id = 'w12f-null2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RotateCredentials(ctx, RotateCredentialsInput{
		AccountID:                        "w12f-null",
		ExpectedProviderCode:             "gpt",
		ExpectedAccountType:              "oauth",
		ExpectedProviderProtocolProfileID: "profile_gpt_openai_v1",
		ExpectedConfigRevision:           1,
		Credentials:                      map[string]any{"refresh_token": "w12f-rt"},
	}); err == nil {
		t.Fatal("垃圾密文必须报错")
	}
	if _, err := store.FindRotationAccount(ctx, "w12f-null2"); err == nil {
		t.Fatal("垃圾密文 FindRotationAccount 必须报错")
	}
}

func TestW12fUpdateAndMarkArms(t *testing.T) {
	store, db, _ := newTestStore(t)
	ctx := context.Background()
	now := w12fBaseNow()
	seedProviderProfiles(t, db)
	seedOpenAIOAuthAccount(t, db, "w12f-upd", openAICredentials(expiresInMillis(0)), now)

	// UpdateAccountCredentials：版本冲突。
	if _, err := store.UpdateAccountCredentials(ctx, "w12f-upd", openAICredentials(expiresInMillis(0)), 99); err == nil {
		t.Fatal("版本冲突必须报错")
	}
	// UpdateAccountCredentials：不存在账户按幂等处理（不报错）。
	if _, err := store.UpdateAccountCredentials(ctx, "w12f-ghost", openAICredentials(expiresInMillis(0)), 1); err != nil {
		t.Fatalf("不存在账户必须幂等: %v", err)
	}
	// MarkAccountFailureState：状态/版本不匹配 → false。
	changed, err := store.MarkAccountFailureState(ctx, "w12f-upd", "w12f-err", "w12f-reason", 99, "active")
	if err != nil || changed {
		t.Fatalf("版本不匹配必须返回 false: changed=%v err=%v", changed, err)
	}
	// ClearAccountFailureState：错误码不匹配 → false。
	changed, status, err := store.ClearAccountFailureState(ctx, "w12f-upd", []string{"w12f-other"})
	if err != nil || changed {
		t.Fatalf("错误码不匹配必须返回 false: changed=%v status=%s err=%v", changed, status, err)
	}
}
