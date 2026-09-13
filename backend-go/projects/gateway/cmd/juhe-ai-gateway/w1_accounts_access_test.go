package main

// w1: chain_accounts.go 资源授权调度门直测（selector 私有读方法 + access
// 解析 + canSchedule 授权门）。SQLite 种子行，无外部依赖。

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func newW1AccessSelector(t *testing.T) *chainAccountsSelector {
	t.Helper()
	fixture := newChainFixture(t)
	selector, err := newChainAccountsSelector(fixture.db, false, "chain-test-secret", time.Now, 20)
	if err != nil {
		t.Fatalf("create selector: %v", err)
	}
	return selector
}

func (s *chainAccountsSelector) w1SeedAuthorization(t *testing.T, id, resourceType, resourceID, owner, grantee, status, expiresAt string) {
	t.Helper()
	var expiresArg any
	if expiresAt != "" {
		expiresArg = expiresAt
	}
	query := `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id,
		grantee_system_account_id, status, expires_at, effective_source_type, effective_source_team_id, limits_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'team', 'team_1', '{"quotaLimited":true}')`
	if _, err := s.db.Exec(query, id, resourceType, resourceID, owner, grantee, status, expiresArg); err != nil {
		t.Fatalf("seed authorization: %v", err)
	}
}

func TestW1ActiveResourceAuthorizationByID(t *testing.T) {
	selector := newW1AccessSelector(t)
	ctx := context.Background()
	selector.w1SeedAuthorization(t, "authz_1", "account", "acc_1", "sys_owner", "sys_caller", "active", "")
	authorization, err := selector.activeResourceAuthorizationByID(ctx, "authz_1", "sys_caller")
	if err != nil || authorization == nil {
		t.Fatalf("authorization = %v, %v", authorization, err)
	}
	if authorization.id != "authz_1" || authorization.resourceID != "acc_1" || authorization.resourceOwnerID != "sys_owner" {
		t.Fatalf("authorization = %+v", authorization)
	}
	if authorization.effectiveSourceType == nil || *authorization.effectiveSourceType != "team" {
		t.Fatalf("source type = %v", authorization.effectiveSourceType)
	}
	// grantee 不匹配：nil。
	if authorization, err := selector.activeResourceAuthorizationByID(ctx, "authz_1", "sys_other"); err != nil || authorization != nil {
		t.Fatalf("错误 grantee = %v, %v", authorization, err)
	}
	// 过期：nil。
	selector.w1SeedAuthorization(t, "authz_expired", "account", "acc_1", "sys_owner", "sys_caller", "active", "2020-01-01T00:00:00.000Z")
	if authorization, err := selector.activeResourceAuthorizationByID(ctx, "authz_expired", "sys_caller"); err != nil || authorization != nil {
		t.Fatalf("过期授权 = %v, %v", authorization, err)
	}
	// 按 resource_type 读取。
	byResource, err := selector.activeResourceAuthorization(ctx, "account", "acc_1", "sys_caller")
	if err != nil || byResource == nil || byResource.id != "authz_1" {
		t.Fatalf("by resource = %v, %v", byResource, err)
	}
	if _, err := selector.activeResourceAuthorization(ctx, "group", "grp_x", "sys_caller"); err != nil {
		t.Fatalf("缺失资源类型 = %v", err)
	}
}

func TestW1ActiveResourceAuthorizationsByIDs(t *testing.T) {
	selector := newW1AccessSelector(t)
	ctx := context.Background()
	selector.w1SeedAuthorization(t, "authz_a", "account", "res_a", "sys_owner", "sys_caller", "active", "")
	selector.w1SeedAuthorization(t, "authz_b", "group", "grp_b", "sys_owner", "sys_caller", "active", "")
	got, err := selector.activeResourceAuthorizationsByIDs(ctx, []string{"authz_a", "authz_b", " ", ""}, "sys_caller")
	if err != nil {
		t.Fatalf("by ids: %v", err)
	}
	if len(got) != 4 { // id 与 resourceID 双键。
		t.Fatalf("map size = %d，want 4（id+resource 双键）", len(got))
	}
	if got["authz_a"] == nil || got["res_a"] == nil || got["authz_b"] == nil || got["grp_b"] == nil {
		t.Fatalf("键缺失: %v", got)
	}
	// 空列表 → 空 map。
	if got, err := selector.activeResourceAuthorizationsByIDs(ctx, nil, "sys_caller"); err != nil || len(got) != 0 {
		t.Fatalf("空列表 = %v, %v", got, err)
	}
}

func TestW1ResolveChainAccountAccessArms(t *testing.T) {
	selector := newW1AccessSelector(t)
	ctx := context.Background()
	selector.w1SeedAuthorization(t, "authz_1", "account", "acc_1", "sys_owner", "sys_caller", "active", "")
	groupAccessOwner := &gatewayruntimecache.GroupUsageAccessMetadata{GroupAccessType: gatewayruntimecache.GroupAccessTypeOwner}
	groupAccessAuthorized := &gatewayruntimecache.GroupUsageAccessMetadata{
		GroupAccessType: gatewayruntimecache.GroupAccessTypeAuthorized, GroupOwnerSystemAccountID: "sys_owner",
	}

	// 授权实例 + prefetch 命中：authorized access（实例行的账户 owner 即
	// caller——授权实例把资源行复制到被授权人视角）。
	row := &chainCandidateRow{ID: "acc_1", SystemAccountID: "sys_caller"}
	row.AuthorizationInstanceAuthorizationID.Valid = true
	row.AuthorizationInstanceAuthorizationID.String = "authz_1"
	prefetch, err := selector.activeResourceAuthorizationsByIDs(ctx, []string{"authz_1"}, "sys_caller")
	if err != nil {
		t.Fatalf("prefetch: %v", err)
	}
	access, err := selector.resolveChainAccountAccess(ctx, row, "sys_caller", groupAccessOwner, "", prefetch)
	if err != nil || access == nil || access.accountAccessType != chainAccountAccessAuthorized {
		t.Fatalf("authorized access = %+v, %v", access, err)
	}
	if access.accountAuthorizationID == nil || *access.accountAuthorizationID != "authz_1" {
		t.Fatalf("authz id = %v", access.accountAuthorizationID)
	}
	// 授权实例但行 owner != caller：nil。
	foreignInstance := &chainCandidateRow{ID: "acc_1", SystemAccountID: "sys_other"}
	foreignInstance.AuthorizationInstanceAuthorizationID.Valid = true
	foreignInstance.AuthorizationInstanceAuthorizationID.String = "authz_1"
	access, err = selector.resolveChainAccountAccess(ctx, foreignInstance, "sys_caller", groupAccessOwner, "", prefetch)
	if err != nil || access != nil {
		t.Fatalf("跨租户授权实例 = %+v, %v", access, err)
	}
	// bound 授权 ID 不匹配：nil。
	access, err = selector.resolveChainAccountAccess(ctx, row, "sys_caller", groupAccessOwner, "authz_other", prefetch)
	if err != nil || access != nil {
		t.Fatalf("bound 不匹配 = %+v, %v", access, err)
	}
	// 无授权实例 + owner：owner access。
	plain := &chainCandidateRow{ID: "acc_2", SystemAccountID: "sys_caller"}
	access, err = selector.resolveChainAccountAccess(ctx, plain, "sys_caller", groupAccessOwner, "", nil)
	if err != nil || access == nil || access.accountAccessType != chainAccountAccessOwner {
		t.Fatalf("owner access = %+v, %v", access, err)
	}
	// authorized 组 + owner 匹配：group_authorized。
	foreign := &chainCandidateRow{ID: "acc_3", SystemAccountID: "sys_owner"}
	access, err = selector.resolveChainAccountAccess(ctx, foreign, "sys_caller", groupAccessAuthorized, "", nil)
	if err != nil || access == nil || access.accountAccessType != chainAccountAccessGroupAuthorized {
		t.Fatalf("group authorized = %+v, %v", access, err)
	}
	// authorized 组 + owner 非 caller 且非组 owner：nil。
	third := &chainCandidateRow{ID: "acc_4", SystemAccountID: "sys_third"}
	access, err = selector.resolveChainAccountAccess(ctx, third, "sys_caller", groupAccessAuthorized, "", nil)
	if err != nil || access != nil {
		t.Fatalf("组授权 owner 不匹配 = %+v, %v", access, err)
	}
	// owner 组 + 无实例 + prefetch 按资源 ID（row.ID）命中。
	selector.w1SeedAuthorization(t, "authz_res", "account", "acc_3", "sys_owner", "sys_caller", "active", "")
	prefetch2, err := selector.activeResourceAuthorizationsByIDs(ctx, []string{"authz_res"}, "sys_caller")
	if err != nil {
		t.Fatalf("prefetch2: %v", err)
	}
	access, err = selector.resolveChainAccountAccess(ctx, foreign, "sys_caller", groupAccessOwner, "", prefetch2)
	if err != nil || access == nil || access.accountAccessType != chainAccountAccessAuthorized {
		t.Fatalf("资源 ID 命中 = %+v, %v", access, err)
	}
	// loadAccountAuthorizationsForSelection：authorized 组 → nil。
	if got, err := selector.loadAccountAuthorizationsForSelection(ctx, nil, groupAccessAuthorized, "sys_caller"); err != nil || got != nil {
		t.Fatalf("authorized 组 = %v, %v", got, err)
	}
}

func TestW1CanScheduleChainAuthorizedAccount(t *testing.T) {
	ownerAccess := &chainAccountAccess{accountAccessType: chainAccountAccessOwner}
	row := &chainCandidateRow{ID: "acc_1"}
	if !canScheduleChainAuthorizedAccount(row, nil, ownerAccess) {
		t.Fatal("owner access 必须可调度")
	}
	groupAuthorized := &chainAccountAccess{accountAccessType: chainAccountAccessGroupAuthorized}
	if !canScheduleChainAuthorizedAccount(row, nil, groupAuthorized) {
		t.Fatal("group_authorized access 必须可调度")
	}
	// authorized 但无授权 ID：false。
	authorizedNoID := &chainAccountAccess{accountAccessType: chainAccountAccessAuthorized}
	if canScheduleChainAuthorizedAccount(row, nil, authorizedNoID) {
		t.Fatal("缺授权 ID 必须不可调度")
	}
	authzID := "authz_1"
	authorized := &chainAccountAccess{accountAccessType: chainAccountAccessAuthorized, accountAuthorizationID: &authzID}
	authorization := &chainAuthorizationRow{id: "authz_1"}
	if !canScheduleChainAuthorizedAccount(row, map[string]*chainAuthorizationRow{"authz_1": authorization}, authorized) {
		t.Fatal("prefetch 命中必须可调度")
	}
	// 按 row.ID 回退命中。
	if !canScheduleChainAuthorizedAccount(row, map[string]*chainAuthorizationRow{"acc_1": authorization}, authorized) {
		t.Fatal("row.ID 回退必须可调度")
	}
	if canScheduleChainAuthorizedAccount(row, map[string]*chainAuthorizationRow{"other": authorization}, authorized) {
		t.Fatal("未命中必须不可调度")
	}
	// 无 prefetch map：false（authorized 分支需要 map）。
	if canScheduleChainAuthorizedAccount(row, nil, authorized) {
		t.Fatal("无 map 的 authorized 必须不可调度")
	}
}

func TestW1ChainCandidateResourceProjections(t *testing.T) {
	resourceID := sql.NullString{String: "res_acc", Valid: true}
	plainID := sql.NullString{}
	row := &chainCandidateRow{ID: "acc_1", ResourceAccountID: resourceID}
	if got := row.resourceAccountID(); got != "res_acc" {
		t.Fatalf("resource account = %q", got)
	}
	row.ResourceAccountID = plainID
	if got := row.resourceAccountID(); got != "acc_1" {
		t.Fatalf("回退 account = %q", got)
	}
	proxyID := sql.NullString{String: "proxy_1", Valid: true}
	row.ProxyProfileID = proxyID
	if got := row.resourceProxyProfileID(); got != "proxy_1" {
		t.Fatalf("proxy = %q", got)
	}
	row.ResourceProxyProfileID = sql.NullString{String: "res_proxy", Valid: true}
	if got := row.resourceProxyProfileID(); got != "res_proxy" {
		t.Fatalf("resource proxy = %q", got)
	}
	providerCode := sql.NullString{String: "gemini", Valid: true}
	row.ResourceProviderCode = providerCode
	if got := row.resourceProviderCode(); got != "gemini" {
		t.Fatalf("resource provider = %q", got)
	}
	row.ResourceProviderCode = plainID
	row.ProviderCode = "openai"
	if got := row.resourceProviderCode(); got != "openai" {
		t.Fatalf("回退 provider = %q", got)
	}
	protocolCode := sql.NullString{String: "openai", Valid: true}
	row.ProtocolCode = protocolCode
	if got := row.resourceProtocolCode(); got != "openai" {
		t.Fatalf("protocol = %q", got)
	}
	accountType := sql.NullString{String: "api_key", Valid: true}
	row.ResourceType = accountType
	if got := row.resourceType(); got != "api_key" {
		t.Fatalf("resource type = %q", got)
	}
	row.ResourceType = plainID
	row.Type = "oauth"
	if got := row.resourceType(); got != "oauth" {
		t.Fatalf("回退 type = %q", got)
	}
	limit := sql.NullInt64{Int64: 9, Valid: true}
	row.ResourceConcurrencyLimit = limit
	row.ConcurrencyLimit = 4
	if got := row.resourceConcurrencyLimit(); got != 9 {
		t.Fatalf("resource limit = %d", got)
	}
	row.ResourceConcurrencyLimit = sql.NullInt64{}
	if got := row.resourceConcurrencyLimit(); got != 4 {
		t.Fatalf("回退 limit = %d", got)
	}
	credentials := sql.NullString{String: "sealed", Valid: true}
	row.ResourceCredentialsEncrypted = credentials
	if got := row.resourceCredentialsEncrypted(); got != "sealed" {
		t.Fatalf("credentials = %q", got)
	}
	if !strings.Contains(chainAuthorizationColumns, "resource_owner_system_account_id") {
		t.Fatal("授权列清单缺失")
	}
}
