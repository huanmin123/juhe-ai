package accounts

// w14l 覆盖率收尾（三）：reserveAndEnqueueAccountHealthSnapshot 版本臂、
// groupOwnerAndProvider 空属主臂、测试会话跨域/缺失臂与取消原因传播臂。

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestW14LHealthSnapshotReserveArms(t *testing.T) {
	f := newW14LFaultFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	// 空 ID → 错误臂。
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.reserveAndEnqueueAccountHealthSnapshot(ctx, tx, "   ", 1, 1, now); err == nil {
		t.Fatal("空 account ID 应报错")
	}
	// 全新 ID → INSERT 臂。
	if err := f.store.reserveAndEnqueueAccountHealthSnapshot(ctx, tx, "acc-w14l-health-new", 1, 1, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// 已有版本 → UPDATE 递增臂。
	f.exec(`INSERT INTO account_health_jobs_input_versions (account_id, current_version, reserved_at)
		VALUES ('acc-w14l-health-old', 3, ?)`, now)
	tx, err = f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.reserveAndEnqueueAccountHealthSnapshot(ctx, tx, "acc-w14l-health-old", 1, 1, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := f.db.QueryRow(`SELECT current_version FROM account_health_jobs_input_versions WHERE account_id = 'acc-w14l-health-old'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 4 {
		t.Fatalf("版本应递增到 4：%d", version)
	}
}

func TestW14LGroupOwnerEdgeArms(t *testing.T) {
	f := newW14LFaultFixture(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// 空属主 / 空供应商的分组行 → groupOwnerAndProvider 返回 (nil, nil)。
	f.exec(`INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at)
		VALUES ('grp-w14l-empty', '', '空属主', '', 1, 0, 'personal', ?, ?)`, now, now)
	group, err := f.store.groupOwnerAndProvider(context.Background(), f.db, "grp-w14l-empty")
	if err != nil || group != nil {
		t.Fatalf("空属主分组应返回 nil：%+v %v", group, err)
	}
	// duplicateAccountNameError 的 nil 错误臂。
	if duplicateAccountNameError(nil, "w14l-名字") != nil {
		t.Fatal("nil 错误应返回 nil")
	}
}

func TestW14LTestSessionAccessArms(t *testing.T) {
	f := newW14LFaultFixture(t)
	ctx := context.Background()
	owner := f.scope()
	foreign := AccessScope{ViewerID: "w14l-stranger"}
	session, err := f.store.CreateTestSession(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	// 缺失会话。
	if got, err := f.store.GetTestSession(ctx, "sess-w14l-missing", &owner); got != nil || err != nil {
		t.Fatalf("缺失会话应返回 nil：%+v %v", got, err)
	}
	if got, err := f.store.HeartbeatTestSession(ctx, "sess-w14l-missing", &owner); got != nil || err != nil {
		t.Fatalf("缺失会话心跳应返回 nil：%+v %v", got, err)
	}
	// 跨域读者不可读（返回 nil, nil，与缺失同形）。
	if got, err := f.store.GetTestSession(ctx, session.ID, &foreign); got != nil || err != nil {
		t.Fatalf("跨域读者不应可读：%+v %v", got, err)
	}
	if gotSession, gotTasks, err := f.store.GetTestSessionDetail(ctx, session.ID, &foreign); gotSession != nil || gotTasks != nil || err != nil {
		t.Fatalf("跨域读者不应可读详情：%+v %+v %v", gotSession, gotTasks, err)
	}
	if got, err := f.store.HeartbeatTestSession(ctx, session.ID, &foreign); got != nil || err != nil {
		t.Fatalf("跨域读者不应可心跳：%+v %v", got, err)
	}
	if _, err := f.store.CancelTestSession(ctx, session.ID, &owner, "w14l 取消原因"); err != nil {
		t.Fatal(err)
	}
	// 取消后 reason 传播：非 running 会话心跳跳过更新；CreateTestTask 的
	// reason 更新臂。
	if _, err := f.store.HeartbeatTestSession(ctx, session.ID, &owner); err != nil {
		t.Fatalf("已取消会话心跳应放行（非 running 不更新）：%v", err)
	}
	blocked, err := f.store.CreateTestSession(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CancelTestSession(ctx, blocked.ID, &owner, "w14l 再取消"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateTestTask(ctx, TestTaskCreateInput{
		AccountID: "acc-x", AccountName: "w14l-blocked", ProviderCode: "gpt",
		ProviderProtocolProfileID: "prof-gpt", ProtocolCode: "openai", ProtocolVersion: "v1",
		AccountType: "api_key", Access: owner, Diagnostics: "full", SessionID: blocked.ID,
	}); err == nil || !strings.Contains(err.Error(), "取消") {
		t.Fatalf("已取消会话应拒绝建任务：%v", err)
	}
	// 空任务 ID 的取消臂。
	if got, err := f.store.CancelTestTask(ctx, "   ", &owner); got != nil || err != nil {
		t.Fatalf("空任务 ID 应返回 nil：%+v %v", got, err)
	}
	// 运行中任务的失败臂：建任务后 FailTestTask。
	live, err := f.store.CreateTestSession(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	task, err := f.store.CreateTestTask(ctx, TestTaskCreateInput{
		AccountID: "acc-x", AccountName: "w14l-live", ProviderCode: "gpt",
		ProviderProtocolProfileID: "prof-gpt", ProtocolCode: "openai", ProtocolVersion: "v1",
		AccountType: "api_key", Access: owner, Diagnostics: "full", SessionID: live.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.GetTestTask(ctx, task.ID, &owner); err != nil {
		t.Fatal(err)
	}
	if err := f.store.FailTestTask(ctx, task.ID, "w14l 后台失败"); err != nil {
		t.Fatal(err)
	}
	tasks, err := f.store.ListTestTasks(ctx, []string{task.ID}, &owner)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("任务列表应含 1 项：%+v %v", tasks, err)
	}
}

func TestW14LEmptyIDReadArms(t *testing.T) {
	f := newW14LFaultFixture(t)
	ctx := context.Background()
	scope := f.scope()
	// 空 ID 早退臂（nil, nil）。
	if got, err := f.store.FindOAuthReauthorizationContext(ctx, "   ", scope); got != nil || err != nil {
		t.Fatalf("空 ID 应返回 nil：%+v %v", got, err)
	}
	if got, err := f.store.FindAPIKeyRuntimeAccount(ctx, "  ", scope); got != nil || err != nil {
		t.Fatalf("空 ID 应返回 nil：%+v %v", got, err)
	}
	// 未知 ID → nil, nil。
	if got, err := f.store.FindOAuthReauthorizationContext(ctx, "acc-w14l-none", scope); got != nil || err != nil {
		t.Fatalf("未知账户应返回 nil：%+v %v", got, err)
	}
	if got, err := f.store.FindAPIKeyRuntimeAccount(ctx, "acc-w14l-none", scope); got != nil || err != nil {
		t.Fatalf("未知账户应返回 nil：%+v %v", got, err)
	}
}

func TestW14LOAuthReauthContextArms(t *testing.T) {
	f := newW14LFaultFixture(t)
	ctx := context.Background()
	scope := f.scope()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	sealed, err := EncryptJSON(testSecret, Credentials{
		"google_oauth_client_id": "cid-w14l", "google_oauth_client_secret": "csecret-w14l",
		"google_oauth_project_id": "proj-w14l", "google_oauth_quota_project_id": "qproj-w14l",
	})
	if err != nil {
		t.Fatal(err)
	}
	seedGemini := func(id, authID string) {
		f.exec(`INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
			protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_mask,
			health_check_model, authorization_instance_authorization_id, created_at, updated_at)
			VALUES (?, ?, 'gemini', 'prof-gemini', 'gemini', 'v1', ?, 'google_oauth', 'active', ?, 'cid***', '',
			?, ?, ?)`, id, f.owner, id, sealed, authID, now, now)
	}
	seedGemini("acc-w14l-gem", "")
	seedGemini("acc-w14l-gem-bound", "aa-w14l-bound")
	context, err := f.store.FindOAuthReauthorizationContext(ctx, "acc-w14l-gem", scope)
	if err != nil {
		t.Fatal(err)
	}
	if context == nil || context.ID != "acc-w14l-gem" || context.OAuthType == "" {
		t.Fatalf("重授权上下文应存在：%+v", context)
	}
	if _, err := f.store.FindOAuthReauthorizationContext(ctx, "acc-w14l-gem-bound", scope); err == nil {
		t.Fatal("授权实例应拒绝重新授权")
	}
}

func TestW14LOAuthReauthForbiddenAndBadCredentialsArms(t *testing.T) {
	f := newW14LFaultFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	foreign := AccessScope{ViewerID: "w14l-outsider"}
	// 合法凭据但跨域非管理员 → (nil, nil)。
	sealed, err := EncryptJSON(testSecret, Credentials{"google_oauth_refresh_token": "rt-w14l"})
	if err != nil {
		t.Fatal(err)
	}
	f.exec(`INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_mask,
		health_check_model, created_at, updated_at)
		VALUES ('acc-w14l-gem-f', ?, 'gemini', 'prof-gemini', 'gemini', 'v1', '跨域', 'google_oauth',
		'active', ?, 'rt***', '', ?, ?)`, f.owner, sealed, now, now)
	if got, err := f.store.FindOAuthReauthorizationContext(ctx, "acc-w14l-gem-f", foreign); got != nil || err != nil {
		t.Fatalf("跨域非管理员应返回 nil：%+v %v", got, err)
	}
	// 坏凭据密文 → 解密错误臂。
	f.exec(`INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_mask,
		health_check_model, created_at, updated_at)
		VALUES ('acc-w14l-gem-bad', ?, 'gemini', 'prof-gemini', 'gemini', 'v1', '坏凭据', 'google_oauth',
		'active', 'not-a-valid-sealed-payload', 'x***', '', ?, ?)`, f.owner, now, now)
	if _, err := f.store.FindOAuthReauthorizationContext(ctx, "acc-w14l-gem-bad", f.scope()); err == nil {
		t.Fatal("坏凭据应报错")
	}
}

func TestW14LMiscPureArms(t *testing.T) {
	// 单字符掩码臂。
	if got := MaskSecret("x"); got != "x***" {
		t.Fatalf("单字符掩码不符：%q", got)
	}
	// 内置适配器携带 custom 配置 → 拒绝臂。
	if _, err := NormalizeAccountBalanceConfig(map[string]any{
		"adapter": "builtin", "custom": map[string]any{},
	}); err == nil {
		t.Fatal("内置查询不应接受 custom 配置")
	}
	// 账户名搜索 3-gram。
	terms := accountNameSearchQueryTerms("w14l")
	if len(terms) == 0 {
		t.Fatal("应产出搜索词")
	}
	if got := accountNameSearchQueryTerms(""); got != nil {
		t.Fatalf("空关键词应返回 nil：%v", got)
	}
}

func TestW14LMiscPureArms2(t *testing.T) {
	// 额度恢复策略：超长策略拒绝臂（两个账户类型都带超长时区文本撑大编码长度）。
	huge := strings.Repeat("w", quotaRecoveryMaxPolicyBytes)
	if _, err := normalizeQuotaRecoveryPolicy(map[string]any{
		"api_key":      map[string]any{"timezone": huge},
		"google_oauth": map[string]any{"timezone": huge},
	}); err == nil {
		t.Fatal("超长策略应拒绝")
	}
	// 客户端兼容：codex 响应默认值与非法值（gpt + openai/v1 才走严格校验）。
	profile := protocolProfileRef{ProviderCode: "gpt", ProtocolCode: "openai", ProtocolVersion: "v1"}
	compat, err := normalizeOpenAIAccountClientCompatibility("gpt", "api_key", "", profile)
	if err != nil || compat == "" {
		t.Fatalf("空值应有默认兼容形态：%q %v", compat, err)
	}
	if _, err := normalizeOpenAIAccountClientCompatibility("gpt", "api_key", "bogus-compat", profile); err == nil {
		t.Fatal("非法兼容值应拒绝")
	}
	// 数字判定臂。
	if !isAllDigits("204") || isAllDigits("") || isAllDigits("20a") {
		t.Fatal("isAllDigits 判定不符")
	}
}
