package accounts

// #B4 ⑪ oauth_type 读面切列同步约束测试：判定列（accounts.oauth_type）三值
// 优先、credentials["oauth_type"] 三值回落。回落服务迁移窗口存量行（B1 前创建
// 的行列 NULL）与「refresh 删键」切片落地后的封套缺键形态；覆盖克隆预填
// （列有值/列空回落两形态）、重授权上下文判定与 legacy 全量提交合并的保列。

import (
	"context"
	"testing"
)

// b4SeedGoogleOAuthAccount 种一个 gemini google_oauth 账户行，凭据封套与
// oauth_type 列由用例自定（envelopeOAuthType 传空串 = 封套不带键）。
func b4SeedGoogleOAuthAccount(t *testing.T, env *testEnv, id, ownerID, envelopeOAuthType, columnOAuthType string) {
	t.Helper()
	env.seedAccount(t, id, ownerID, "b4-"+id, "active")
	credentials := Credentials{
		"refresh_token": "rt-" + id,
		"client_id":     geminiCLIOAuthClientID,
	}
	if envelopeOAuthType != "" {
		credentials["oauth_type"] = envelopeOAuthType
	}
	sealed, err := EncryptJSON(testSecret, credentials)
	if err != nil {
		t.Fatal(err)
	}
	column := any(columnOAuthType)
	if columnOAuthType == "" {
		column = nil
	}
	env.exec(t, `UPDATE accounts SET type = 'google_oauth', vendor_code = 'gemini',
		connection_driver_code = 'gemini', credentials_encrypted = ?, oauth_type = ? WHERE id = ?`,
		sealed, column, id)
}

func TestB4ClonePrefersOAuthTypeColumn(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	access := AccessScope{ViewerID: adminID, IsAdmin: true}

	// 形态一（列有值）：正常 B1 后写入形态——列与封套键一致，预填取判定值。
	b4SeedGoogleOAuthAccount(t, env, "acc-b4-clone-col", adminID, "ai_studio", "ai_studio")
	clone, err := env.store.FindCloneContext(context.Background(), "acc-b4-clone-col", access)
	if err != nil || clone == nil {
		t.Fatalf("列有值克隆上下文：%v %v", clone, err)
	}
	if clone.CredentialOptions.OAuthType == nil || *clone.CredentialOptions.OAuthType != "ai_studio" {
		t.Fatalf("列有值应预填 ai_studio：%v", clone.CredentialOptions.OAuthType)
	}

	// 列有值 + 封套键缺失（「refresh 删键」切片模拟）：预填不丢判定值——
	// 丢值会让克隆体 google_oauth 组合门五元组缺 oauth_type → 400。
	b4SeedGoogleOAuthAccount(t, env, "acc-b4-clone-del", adminID, "", "google_one")
	clone, err = env.store.FindCloneContext(context.Background(), "acc-b4-clone-del", access)
	if err != nil || clone == nil {
		t.Fatalf("删键克隆上下文：%v %v", clone, err)
	}
	if clone.CredentialOptions.OAuthType == nil || *clone.CredentialOptions.OAuthType != "google_one" {
		t.Fatalf("封套缺键应按列回填 google_one：%v", clone.CredentialOptions.OAuthType)
	}

	// 形态二（列空回落）：迁移窗口存量行——列 NULL、封套带键，回落键值。
	b4SeedGoogleOAuthAccount(t, env, "acc-b4-clone-legacy", adminID, "code_assist", "")
	clone, err = env.store.FindCloneContext(context.Background(), "acc-b4-clone-legacy", access)
	if err != nil || clone == nil {
		t.Fatalf("列空克隆上下文：%v %v", clone, err)
	}
	if clone.CredentialOptions.OAuthType == nil || *clone.CredentialOptions.OAuthType != "code_assist" {
		t.Fatalf("列空应回落凭据键 code_assist：%v", clone.CredentialOptions.OAuthType)
	}
}

func TestB4GeminiOAuthContextTypeColumnFirst(t *testing.T) {
	if got := geminiOAuthContextType("google_one", Credentials{}); got != "google_one" {
		t.Fatalf("列三值应优先：%s", got)
	}
	if got := geminiOAuthContextType("", Credentials{"oauth_type": "ai_studio"}); got != "ai_studio" {
		t.Fatalf("列空应回落凭据键：%s", got)
	}
	// 词表外列值不生效，回落凭据键（迁移窗口双源形态取键）。
	if got := geminiOAuthContextType("bogus", Credentials{"oauth_type": "ai_studio"}); got != "ai_studio" {
		t.Fatalf("词表外列应回落凭据键：%s", got)
	}
	// 双源皆无三值 → 原启发式不变（默认 code_assist）。
	if got := geminiOAuthContextType("", Credentials{}); got != "code_assist" {
		t.Fatalf("双源空应保持启发式默认：%s", got)
	}
}

func TestB4MergeRestoresOAuthTypeFromColumn(t *testing.T) {
	// 封套键缺失 + 列有值：legacy 全量提交合并按列回填，重封链按同列重算
	// oauth_type 列，两侧不漂移。
	merged := mergeAccountCredentialsForUpdate("google_oauth", "google_one",
		Credentials{"refresh_token": "rt"}, Credentials{})
	if merged["oauth_type"] != "google_one" {
		t.Fatalf("列回填：%v", merged["oauth_type"])
	}
	// requested 显式值优先于列（正常替换语义不变）。
	merged = mergeAccountCredentialsForUpdate("google_oauth", "google_one",
		Credentials{}, Credentials{"oauth_type": "ai_studio"})
	if merged["oauth_type"] != "ai_studio" {
		t.Fatalf("requested 优先：%v", merged["oauth_type"])
	}
	// 现值封套键优先于列（列与键一致的正常形态，行为不变）。
	merged = mergeAccountCredentialsForUpdate("google_oauth", "code_assist",
		Credentials{"oauth_type": "code_assist"}, Credentials{})
	if merged["oauth_type"] != "code_assist" {
		t.Fatalf("现值封套键保留：%v", merged["oauth_type"])
	}
	// 列空 + 封套缺键（无法判定）：不回填，行为与现状一致。
	merged = mergeAccountCredentialsForUpdate("google_oauth", "",
		Credentials{"refresh_token": "rt"}, Credentials{})
	if _, exists := merged["oauth_type"]; exists {
		t.Fatalf("双源空不应回填：%v", merged["oauth_type"])
	}
	// 非 google_oauth 类型不受列影响（oauth 类型无该键保列语义）。
	merged = mergeAccountCredentialsForUpdate("oauth", "google_one",
		Credentials{"access_token": "at"}, Credentials{})
	if _, exists := merged["oauth_type"]; exists {
		t.Fatalf("非 google_oauth 不应注入 oauth_type：%v", merged["oauth_type"])
	}
	if merged["access_token"] != "at" {
		t.Fatalf("oauth 保留应不受影响：%v", merged["access_token"])
	}
}
