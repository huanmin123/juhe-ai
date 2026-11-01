package accounthealth

// B3-H25 回归：显式/恢复账户装载（LoadAccount，keymodelrecovery 的唯一输入
// 来源）必须 join account_api_key_runtime_states，排除人工置 disabled 的
// Key；可用 Key 为空时返回可识别的无可用 Key 错误。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
)

func TestB3H25LoadAccountExcludesDisabledKeys(t *testing.T) {
	db, _ := w12dPgDB(t)
	w12dSeedSettings(t, db)
	const secret = "b3h25-reader-secret"
	const accountID = "b3h25-acc"
	w12dSeedAPIKeyAccount(t, db, secret, accountID, "deepseek", "profile_deepseek_openai_v1")
	envelope, err := exactkeyprobe.EncryptV1Envelope(secret, []byte(`{"api_keys":["sk-b3h25-active","sk-b3h25-disabled"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE juhe_business.accounts SET credentials_encrypted = $1 WHERE id = $2`, envelope, accountID); err != nil {
		t.Fatal(err)
	}
	reader, err := NewPostgresDirectInputReader(db, secret, time.Hour, func() time.Time {
		return time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := reader.LoadAccount(ctx, accountID)
	if err != nil || len(first) != 1 || len(first[0].APIKeys) != 2 {
		t.Fatalf("初始装载应含两个 Key: %d err=%v", len(first), err)
	}
	if len(first[0].APIKeys) != 2 {
		t.Fatalf("初始 Key 池: %#v", first[0].APIKeys)
	}

	seedDisabled := func(fingerprint string, index int) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO juhe_business.account_api_key_runtime_states
			(id, system_account_id, account_id, key_fingerprint, key_index, status, created_at, updated_at)
			VALUES ($1, 'sys_admin', $2, $3, $4, 'disabled', $5, $5)`,
			"b3h25-state-"+fingerprint[:8], accountID, fingerprint, index, w12dFixtureNowText); err != nil {
			t.Fatal(err)
		}
	}
	// 同账户一个 disabled + 一个 active：只返回 active Key。
	seedDisabled(first[0].APIKeys[1].Fingerprint, 1)
	second, err := reader.LoadAccount(ctx, accountID)
	if err != nil || len(second) != 1 {
		t.Fatalf("排除 disabled 后仍应装载账户: %d err=%v", len(second), err)
	}
	if len(second[0].APIKeys) != 1 || second[0].APIKeys[0].Fingerprint != first[0].APIKeys[0].Fingerprint {
		t.Fatalf("只应保留 active Key: %#v", second[0].APIKeys)
	}

	// 全部 Key disabled：返回可识别的无可用 Key 错误（调用方不得发上游请求）。
	seedDisabled(first[0].APIKeys[0].Fingerprint, 0)
	if _, err := reader.LoadAccount(ctx, accountID); !errors.Is(err, exactkeyprobe.ErrNoAvailableAPIKey) {
		t.Fatalf("全部 disabled 必须返回 ErrNoAvailableAPIKey: %v", err)
	}
}
