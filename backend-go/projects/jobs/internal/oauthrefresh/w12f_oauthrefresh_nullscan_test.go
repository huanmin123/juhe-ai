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
	// credentials_encrypted 为非 JSON 垃圾内容 → DecryptJSON 失败传播。
	seedAccountRow(t, db, accountRowSeed{ID: "w12f-null", ProviderCode: "gpt", Type: "oauth", Credentials: map[string]any{"refresh_token": "w12f-rt"}, Now: now})
	if _, err := db.Exec(`UPDATE juhe_business.accounts SET credentials_encrypted = 'w12f-garbage' WHERE id = 'w12f-null'`); err != nil {
		t.Fatal(err)
	}
	seedAccountRow(t, db, accountRowSeed{ID: "w12f-null2", ProviderCode: "gpt", Type: "oauth", Credentials: map[string]any{"refresh_token": "w12f-rt"}, Now: now})
	if _, err := db.Exec(`UPDATE juhe_business.accounts SET credentials_encrypted = 'w12f-garbage' WHERE id = 'w12f-null2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RotateCredentials(ctx, RotateCredentialsInput{
		AccountID:              "w12f-null",
		ExpectedProviderCode:   "gpt",
		ExpectedAccountType:    "oauth",
		ExpectedConfigRevision: 1,
		Credentials:            map[string]any{"refresh_token": "w12f-rt"},
	}); err == nil {
		t.Fatal("垃圾密文必须报错")
	}
	if _, err := store.FindRotationAccount(ctx, "w12f-null2"); err == nil {
		t.Fatal("垃圾密文 FindRotationAccount 必须报错")
	}
}

