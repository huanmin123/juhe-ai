package aipublic

// 公开账号新增的重名判定回归：findDuplicateAccount 必须按精确名称查找，
// 不能依赖 Keyword 的包含匹配候选窗口——同子串候选超过分页大小时，精确
// 同名行会落到窗口之外并被误判为不重复，最终触发唯一索引冲突（500）。

import (
	"context"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
)

// seedDuplicateFixtureAccount 直插一行 gpt/api_key 账户，priority 决定默认
// 列表排序中的位置（priority asc）。
func seedDuplicateFixtureAccount(t *testing.T, env *aipublicEnv, id, ownerID, name string, priority int) {
	t.Helper()
	sealed, err := accounts.EncryptJSON("test-crypto-secret", accounts.Credentials{"api_key": "sk-dup-" + id})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := env.db.Exec(fixtureBind(`INSERT INTO accounts
		(id, config_revision, dispatch_revision, system_account_id, vendor_code,
		 connection_driver_code, protocol_code, protocol_version, name, type, status,
		 credentials_encrypted, schedulable, priority, created_at, updated_at)
		VALUES (?, 1, 1, ?, 'gpt', 'gpt', 'openai', 'v1', ?, 'api_key', 'active', ?, 1, ?, ?, ?)`),
		id, ownerID, name, sealed, priority, now, now); err != nil {
		t.Fatal(err)
	}
}

func TestFindDuplicateAccountExactNameBeyondKeywordWindow(t *testing.T) {
	env := newAIPublicEnv(t)
	store, err := accounts.NewStore(env.db, "test-crypto-secret", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ownerID := "dup-owner"
	env.seedTargetUser(ownerID, "dup_owner", "active")

	// 25 个包含 "dup-target" 子串的候选先按 priority=0 排序，精确同名行
	// priority=100 排在最后，必然落在 PageSize=20 的包含匹配窗口之外。
	for index := 0; index < 25; index++ {
		name := "dup-target-" + string(rune('a'+index))
		seedDuplicateFixtureAccount(t, env, "dup-cand-"+string(rune('a'+index)), ownerID, name, 0)
	}
	seedDuplicateFixtureAccount(t, env, "dup-exact", ownerID, "dup-target", 100)

	deps := &Deps{DB: env.db, PGDialect: true, Now: time.Now, AiAccounts: store}
	ctx := context.Background()

	// 回归前提：仅用 Keyword 包含匹配的首页不包含精确同名行。
	page, err := store.ListPage(ctx, accounts.AccessScope{ViewerID: ownerID}, accounts.ListOptions{
		Page: 1, PageSize: 20, Keyword: "dup-target", ProviderCode: "gpt",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if item.Name == "dup-target" {
			t.Fatalf("回归场景前提不成立：包含匹配首页已包含精确同名行")
		}
	}

	if !deps.findDuplicateAccount(ctx, ownerID, "gpt", "", "", "dup-target") {
		t.Fatalf("超过包含匹配窗口的精确同名行仍应判定为重复")
	}
	if deps.findDuplicateAccount(ctx, ownerID, "gpt", "", "", "dup-target-not-exist") {
		t.Fatalf("不存在的不同名不应误判为重复")
	}
	// provider 维度仍参与重复判定：同名但不同 vendor 不算重复。
	if deps.findDuplicateAccount(ctx, ownerID, "anthropic", "", "", "dup-target") {
		t.Fatalf("不同 provider 的同名行不应判定为重复")
	}
}
