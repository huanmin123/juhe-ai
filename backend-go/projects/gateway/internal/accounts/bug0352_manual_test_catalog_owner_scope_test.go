package accounts

// BUG-0352 回归：人工测试目录解析必须按账户所有者作用域过滤 personal 行。
//
// 生产复现形状：Grok OAuth 账户 acc_f6a60b47564ab148（xai/oauth，
// health_check_model=grok-4.7）的目录里 builtin 行声明 chat/responses 四形态，
// 但另一用户 sysacc_1782397535511_cc38ab2a 的同名 personal 行只有 chat 双形态；
// 旧实现 CollectTestCatalogCandidates 的 custom 臂 WHERE 只有
// scope IN ('global','personal')，不按 system_account_id 过滤归属，合并时
// personal 优先级 3 > builtin 1，他人 personal 行胜出 → OAuth 账户的
// AccountTestEndpointModeOrder 只给 responses_sse/responses_json，与 chat 协议
// 过滤后为空 → 400「账户上游接口能力中没有可用于连接测试的请求形态」。
// 修复前第一臂必红：FindTestCatalogItem 会选中他人 personal 行的 chat 协议。

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
)

func bug0352Contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func bug0352SameModes(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for _, value := range left {
		if !bug0352Contains(right, value) {
			return false
		}
	}
	return true
}

func TestBug0352ManualTestCatalogOwnerScope(t *testing.T) {
	env := newTestEnv(t)
	env.exec(t, w14bTestCatalogRows)
	ownerID := w14bSeedTestAccount(t, env, "acc-bug0352-oauth", "bug0352-oauth")
	otherID := env.login(t, "bug0352-other", "bug0352-pass", "user")

	// 另一用户的同名 personal 行：只有 chat 双形态，status active、
	// catalog_visible=false（custom 目录臂沿旧表语义不看可见性）。
	env.exec(t, `INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, system_account_id, status,
		supported_endpoint_modes_json, release_date, catalog_visible, created_at, updated_at)
		VALUES ('mdl-bug0352-other', 'gpt', 'gpt-4o-mini', 'gpt-4o-mini', 'personal', ?, 'active',
			'["chat_json","chat_sse"]', '2024-07-18', false, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, otherID)

	// 生产账户为 OAuth（凭证无 supported_endpoint_modes，默认集为 responses 双形态）。
	env.exec(t, `UPDATE accounts SET type = 'oauth' WHERE id = 'acc-bug0352-oauth'`)

	ctx := context.Background()
	scope := AccessScope{ViewerID: ownerID, IsAdmin: true}
	got, err := env.store.FindManualTestOptionsContext(ctx, "acc-bug0352-oauth", &scope)
	if err != nil || got == nil {
		t.Fatalf("测试上下文应返回：%v %v", got, err)
	}
	if got.OwnerSystemAccountID != ownerID {
		t.Fatalf("账户归属不符：%s", got.OwnerSystemAccountID)
	}

	// 第一臂：owner 目录解析不得被他人 personal 行遮蔽，应命中 builtin 行。
	item, err := env.store.findTestCatalogItem(ctx, "gpt", ownerID, "gpt-4o-mini")
	if err != nil || item == nil {
		t.Fatalf("目录解析应返回 builtin 行：%v %v", item, err)
	}
	if item.Mode != "responses" || !bug0352Contains(item.SupportedAPIProtocols, "responses") {
		t.Fatalf("目录解析被跨用户 personal 行遮蔽（修复前必红）：%+v", *item)
	}
	capabilities, err := env.store.AccountManualTestModelCapabilities(ctx, got, "gpt-4o-mini")
	if err != nil {
		t.Fatalf("OAuth 账户能力查询应成功（修复前为 400 无请求形态）：%v", err)
	}
	if !bug0352Contains(capabilities.TestEndpointModes, "responses_sse") ||
		!bug0352Contains(capabilities.TestEndpointModes, "responses_json") {
		t.Fatalf("OAuth 测试形态应同时含 responses_sse/responses_json：%+v", capabilities)
	}
	// 列表路径与能力路径共用单一 loader：选项展示的形态必须与能力解析一致，
	// 钉住「列表可见 = 能力可测」。修复前列表走 ListCustomTestCatalogOptions
	// （归属正确）、能力走 CollectTestCatalogCandidates（缺归属），二者会分叉。
	options, err := env.store.AccountManualTestOptions(ctx, got, ManualTestOptionsQuery{Limit: 50})
	if err != nil {
		t.Fatalf("测试选项列表应成功：%v", err)
	}
	var optionModes []string
	for _, option := range options {
		if option.ID == "gpt-4o-mini" {
			optionModes = option.TestEndpointModes
			break
		}
	}
	if optionModes == nil {
		t.Fatalf("选项列表缺少 gpt-4o-mini：%+v", options)
	}
	if !bug0352SameModes(optionModes, capabilities.TestEndpointModes) {
		t.Fatalf("列表形态与能力形态应一致：options=%v capabilities=%v",
			optionModes, capabilities.TestEndpointModes)
	}

	// 第二臂：归属改回 owner 后 personal 行应重新胜出，证明过滤不误伤本人目录。
	env.exec(t, `UPDATE models SET system_account_id = ? WHERE id = 'mdl-bug0352-other'`, ownerID)
	item, err = env.store.findTestCatalogItem(ctx, "gpt", ownerID, "gpt-4o-mini")
	if err != nil || item == nil {
		t.Fatalf("本人 personal 行应被选中：%v %v", item, err)
	}
	if item.Mode != "chat" || len(item.SupportedAPIProtocols) != 1 ||
		item.SupportedAPIProtocols[0] != "chat_completions" {
		t.Fatalf("本人 personal 行应重新胜出：%+v", *item)
	}
}

// TestBug0352AuthorizedInstanceTestViewUsesSourceOwner 覆盖授权实例 POST /test
// 的 owner 错配：实例行 system_account_id 是被授权用户（grantee），但人工测试
// 的目录解析必须使用来源账户 owner（FindManualTestContextRow 的 COALESCE
// 结果）。修复前 AccountForTestView.manualContext() 取 item.OwnerSystemAccountID
// （grantee），授权实例仍会被 grantee 名下的同名 personal 行遮蔽。
func TestBug0352AuthorizedInstanceTestViewUsesSourceOwner(t *testing.T) {
	env, authzStore := newAuthorizedTestEnv(t)
	ctx := context.Background()
	ownerID := env.login(t, "bug0352-src-owner", "bug0352-pass", "user")
	memberID := env.login(t, "bug0352-member", "bug0352-pass", "user")
	env.seedAccount(t, "acc-bug0352-src", ownerID, "bug0352-source", "active")
	env.seedProviderAndDefaultGroup(t, memberID)
	env.seedTeamMember(t, "team-bug0352", ownerID, memberID)

	if _, err := authzStore.Create(ctx, authz.CreateInput{
		ResourceType: "account", ResourceID: "acc-bug0352-src",
		GranteeType: "team", GranteeID: "team-bug0352",
	}, ownerID); err != nil {
		t.Fatal(err)
	}
	runtimeID := env.queryCell(t, `SELECT id FROM resource_authorizations
		WHERE grantee_system_account_id = ? AND resource_id = 'acc-bug0352-src'`, memberID)
	if runtimeID == "" {
		t.Fatal("授权运行时行缺失")
	}
	// 实例由 Node 写入被授权用户命名空间（system_account_id = grantee）。
	env.seedAuthorizationInstance(t, "acc-bug0352-inst", memberID, runtimeID, "acc-bug0352-src")

	view, err := env.store.FindAccountForTestView(ctx, AccessScope{ViewerID: memberID}, "acc-bug0352-inst")
	if err != nil || view == nil {
		t.Fatalf("授权实例测试视图应返回：%v %v", view, err)
	}
	if view.item.OwnerSystemAccountID != memberID {
		t.Fatalf("实例行 owner 应为被授权用户：%s", view.item.OwnerSystemAccountID)
	}
	if view.ownerSystemAccountID != ownerID {
		t.Fatalf("测试视图应使用来源账户 owner（修复前为 grantee）：%s", view.ownerSystemAccountID)
	}
	if got := view.manualContext().OwnerSystemAccountID; got != ownerID {
		t.Fatalf("manualContext 应使用来源账户 owner（修复前为 grantee）：%s", got)
	}
}

// TestBug0352SavedDraftUsesAccountOwnerNotGroupOwner 覆盖已保存账户草稿测试的
// owner 推导：prepare 链按 group.ownerAccountID 造快照，管理员用他人 groupId
// 提交时 owner 会变成 group 主；saved 链必须以账户归属覆盖，否则目录解析用错
// personal 作用域。修复前 draft.OwnerSystemAccountID 为 groupOwner，
// manualTestCapabilitiesContextFromDraft 会命中 groupOwner 的 chat-only 行。
func TestBug0352SavedDraftUsesAccountOwnerNotGroupOwner(t *testing.T) {
	env := newTestEnv(t)
	env.exec(t, w14bTestCatalogRows)
	ctx := context.Background()
	adminID := env.login(t, "root", "root-pass", "super_admin")
	accountOwnerID := env.login(t, "bug0352-draft-owner", "bug0352-pass", "user")
	groupOwnerID := env.login(t, "bug0352-group-owner", "bug0352-pass", "user")
	env.seedAccount(t, "acc-bug0352-draft", accountOwnerID, "bug0352-draft", "active")
	env.seedProviderAndDefaultGroup(t, groupOwnerID)
	groupID := "grp-default-" + groupOwnerID

	// group 主名下的同名 personal 行：chat-only；修复前草稿 owner 取 group 主，
	// 目录解析会选它，导致 responses 能力消失。
	env.exec(t, `INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, system_account_id, status,
		supported_endpoint_modes_json, release_date, catalog_visible, created_at, updated_at)
		VALUES ('mdl-bug0352-group-owner', 'gpt', 'gpt-4o-mini', 'gpt-4o-mini', 'personal', ?, 'active',
			'["chat_json","chat_sse"]', '2024-07-18', false, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, groupOwnerID)

	access := AccessScope{ViewerID: adminID, IsAdmin: true}
	view, err := env.store.FindAccountForTestView(ctx, access, "acc-bug0352-draft")
	if err != nil || view == nil {
		t.Fatalf("已保存账户测试视图应返回：%v %v", view, err)
	}
	draft, err := env.store.savedAccountDraftTestSnapshot(ctx, view, &TestDraftAccountInput{
		ProviderCode: "gpt", Name: "bug0352-draft", Type: "api_key",
		HealthCheckModel: "gpt-4o-mini", HealthCheckEndpointMode: "chat_json", GroupID: groupID,
		Credentials:     Credentials{"api_key": "sk-bug0352-draft", "base_url": "https://api.openai.com/v1"},
		SupportedModels: []string{"gpt-4o-mini"},
	}, access)
	if err != nil {
		t.Fatalf("已保存账户草稿快照应成功：%v", err)
	}
	if draft.OwnerSystemAccountID != accountOwnerID {
		t.Fatalf("草稿 owner 应为账户归属（修复前为 group 主）：%s", draft.OwnerSystemAccountID)
	}
	if draft.StateTargetAccountID == nil || *draft.StateTargetAccountID != "acc-bug0352-draft" {
		t.Fatalf("草稿目标账户不符：%v", draft.StateTargetAccountID)
	}
	capabilities, err := env.store.AccountManualTestModelCapabilities(ctx,
		manualTestCapabilitiesContextFromDraft(draft), "gpt-4o-mini")
	if err != nil {
		t.Fatalf("草稿能力查询应成功：%v", err)
	}
	if !bug0352Contains(capabilities.TestEndpointModes, "responses_sse") ||
		!bug0352Contains(capabilities.TestEndpointModes, "responses_json") {
		t.Fatalf("草稿目录解析应使用账户 owner 的 builtin 行（修复前命中 group 主 chat-only 行）：%+v", capabilities)
	}
}

// TestBug0352SavedDraftImagesEvidenceUsesAccountOwner 覆盖 saved draft 的
// images_json 目录证据 owner 边界：prepare 链内的 images 证据查询发生在
// 快照返回之前，只在 savedAccountDraftTestSnapshot 外层覆盖 draft owner 无法
// 影响它。管理员用 group 主的 groupId 提交已保存账户草稿时，修复前 images
// 证据按 group 主查目录，命中 group 主 chat-only personal 行 → 判定模型不支持
// Images API 并拒绝；修复后 ownerOverride=账户 owner，命中 builtin images 行。
// 此测试在修复前必红（expected success, got images 证据拒绝）。
func TestBug0352SavedDraftImagesEvidenceUsesAccountOwner(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	adminID := env.login(t, "root", "root-pass", "super_admin")
	accountOwnerID := env.login(t, "bug0352-image-owner", "bug0352-pass", "user")
	groupOwnerID := env.login(t, "bug0352-image-group-owner", "bug0352-pass", "user")
	env.seedAccount(t, "acc-bug0352-image", accountOwnerID, "bug0352-image", "active")
	env.seedProviderAndDefaultGroup(t, groupOwnerID)
	groupID := "grp-default-" + groupOwnerID

	env.exec(t, `INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, system_account_id, status,
		supported_endpoint_modes_json, release_date, catalog_visible, created_at, updated_at)
		VALUES ('mdl-bug0352-image-builtin', 'gpt', 'gpt-image-bug0352', 'gpt-image-bug0352', 'builtin', NULL, 'active',
			'["images_json"]', '2026-01-01', true, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	// group 主名下同名 personal 行：无 images 协议，修复前会胜出并拒绝 images 证据。
	env.exec(t, `INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, system_account_id, status,
		supported_endpoint_modes_json, release_date, catalog_visible, created_at, updated_at)
		VALUES ('mdl-bug0352-image-group-owner', 'gpt', 'gpt-image-bug0352', 'gpt-image-bug0352', 'personal', ?, 'active',
			'["chat_json"]', '2026-01-01', false, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, groupOwnerID)

	access := AccessScope{ViewerID: adminID, IsAdmin: true}
	view, err := env.store.FindAccountForTestView(ctx, access, "acc-bug0352-image")
	if err != nil || view == nil {
		t.Fatalf("已保存账户测试视图应返回：%v %v", view, err)
	}
	draft, err := env.store.savedAccountDraftTestSnapshot(ctx, view, &TestDraftAccountInput{
		ProviderCode: "gpt", Name: "bug0352-image", Type: "api_key",
		HealthCheckModel: "gpt-image-bug0352", HealthCheckEndpointMode: "images_json", GroupID: groupID,
		Credentials:     Credentials{"api_key": "sk-bug0352-image", "base_url": "https://api.openai.com/v1"},
		SupportedModels: []string{"gpt-image-bug0352"},
	}, access)
	if err != nil {
		t.Fatalf("saved draft images 证据应按账户 owner 成功（修复前按 group 主拒绝）：%v", err)
	}
	if draft.OwnerSystemAccountID != accountOwnerID {
		t.Fatalf("草稿 owner 应为账户归属：%s", draft.OwnerSystemAccountID)
	}
	if draft.HealthCheckEndpointMode != "images_json" {
		t.Fatalf("images 检查形态不符：%s", draft.HealthCheckEndpointMode)
	}
}
