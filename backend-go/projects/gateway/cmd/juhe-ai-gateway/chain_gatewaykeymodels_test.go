package main

// /v1/models 账户并集装载测试（chain_gatewaykeymodels.go；权威契约：
// docs/functions/网关模型列表账户并集设计.md §6.5 聚合语义/授权门/validUntil/
// 映射有效性各行）。SQLite 种子行复用 newChainFixture 夹具模式，无外部依赖。

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// gkmBase 是装载器注入时钟的基准；所有到期点以它为参照。
var gkmBase = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func gkmNow() time.Time { return gkmBase }

func gkmTS(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z07:00") }

// ---------------------------------------------------------------------------
// seed helpers（业务库直插；schema 由 newChainFixture 的 seedChainBusinessSchema 建）
// ---------------------------------------------------------------------------

type gkmAccountSeed struct {
	id            string
	owner         string
	provider      string
	profile       string
	protocol      string
	version       string
	accountType   string
	status        string
	schedDisabled bool
	deleted       bool
	expiresAt     time.Time
	cooldownUntil time.Time
	sourceID      string
	instanceAuthz string
}

func gkmSeedAccount(t *testing.T, db *sql.DB, a gkmAccountSeed) {
	t.Helper()
	if a.provider == "" {
		a.provider = "openai"
	}
	if a.status == "" {
		a.status = "active"
	}
	if a.profile == "" {
		a.profile = "prof_1"
	}
	if a.protocol == "" {
		a.protocol = "openai"
	}
	if a.version == "" {
		a.version = "v1"
	}
	if a.accountType == "" {
		a.accountType = "api_key"
	}
	schedulable := 1
	if a.schedDisabled {
		schedulable = 0
	}
	var deletedAt, expiresAt, cooldownUntil, sourceID, instanceAuthz any
	if a.deleted {
		deletedAt = gkmTS(gkmBase)
	}
	if !a.expiresAt.IsZero() {
		expiresAt = gkmTS(a.expiresAt)
	}
	if !a.cooldownUntil.IsZero() {
		cooldownUntil = gkmTS(a.cooldownUntil)
	}
	if a.sourceID != "" {
		sourceID = a.sourceID
	}
	if a.instanceAuthz != "" {
		instanceAuthz = a.instanceAuthz
	}
	_, err := db.Exec(`INSERT INTO accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, schedulable, deleted_at, account_expires_at, cooldown_until,
			authorization_instance_source_account_id, authorization_instance_authorization_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.id, a.owner, a.provider, a.profile, a.protocol, a.version,
		a.id, a.accountType, a.status, schedulable, deletedAt, expiresAt, cooldownUntil,
		sourceID, instanceAuthz)
	if err != nil {
		t.Fatalf("seed account %s: %v", a.id, err)
	}
}

func gkmSeedGroup(t *testing.T, db *sql.DB, id, owner, provider string, enabled int) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type) VALUES (?, ?, ?, ?, 'personal')`,
		id, owner, provider, enabled); err != nil {
		t.Fatalf("seed group %s: %v", id, err)
	}
}

func gkmSeedBinding(t *testing.T, db *sql.DB, groupID, bindingOwner, accountID, accountAuthorizationID string, enabled int) {
	t.Helper()
	var authz any
	if accountAuthorizationID != "" {
		authz = accountAuthorizationID
	}
	if _, err := db.Exec(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, account_authorization_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, groupID, bindingOwner, accountID, enabled, authz, gkmTS(gkmBase)); err != nil {
		t.Fatalf("seed binding %s/%s: %v", groupID, accountID, err)
	}
}

func gkmSeedSupportedModel(t *testing.T, db *sql.DB, accountID, provider, model string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at) VALUES (?, ?, ?, ?)`,
		accountID, provider, model, gkmTS(gkmBase)); err != nil {
		t.Fatalf("seed supported model %s/%s: %v", accountID, model, err)
	}
}

func gkmSeedMapping(t *testing.T, db *sql.DB, accountID, provider, source, sourceFamily, upstream, upstreamFamily string, enabled int) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO account_model_mappings (account_id, provider_code, source_model, source_endpoint_family,
			upstream_model, upstream_endpoint_family, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		accountID, provider, source, sourceFamily, upstream, upstreamFamily, enabled, gkmTS(gkmBase), gkmTS(gkmBase)); err != nil {
		t.Fatalf("seed mapping %s/%s: %v", accountID, source, err)
	}
}

func gkmSeedAuthorization(t *testing.T, db *sql.DB, id, resourceType, resourceID, owner, grantee, status string, expiresAt time.Time) {
	t.Helper()
	var expires any
	if !expiresAt.IsZero() {
		expires = gkmTS(expiresAt)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id,
			grantee_system_account_id, status, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, resourceType, resourceID, owner, grantee, status, expires); err != nil {
		t.Fatalf("seed authorization %s: %v", id, err)
	}
}

func gkmSeedGroupSettings(t *testing.T, db *sql.DB, authzID, grantee, groupID string, enabled int) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO group_authorization_settings (authorization_id, system_account_id, group_id, enabled, group_type)
		VALUES (?, ?, ?, ?, 'personal')`, authzID, grantee, groupID, enabled); err != nil {
		t.Fatalf("seed group settings %s: %v", authzID, err)
	}
}

func gkmUpdateAuthorizationStatus(t *testing.T, db *sql.DB, id, status string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE resource_authorizations SET status = ? WHERE id = ?`, status, id); err != nil {
		t.Fatalf("update authorization %s: %v", id, err)
	}
}

// gkmUnionLoader 以固定时钟构建被测装载器（newChainFixture 夹具）。
func gkmUnionLoader(t *testing.T, fixture *chainFixture) chainGroupModelUnionLoader {
	t.Helper()
	selector, err := newChainAccountsSelectorWithStats(fixture.db, fixture.statsDB, false, "", gkmNow, 20)
	if err != nil {
		t.Fatalf("create selector: %v", err)
	}
	loader, err := newChainGroupModelUnionLoader(selector)
	if err != nil {
		t.Fatalf("create union loader: %v", err)
	}
	return *loader
}

func gkmLoad(t *testing.T, loader chainGroupModelUnionLoader, caller string, groupIDs ...string) gatewayruntimecache.GroupModelUnionEntry {
	t.Helper()
	entry, err := loader.ListGroupModelUnion(context.Background(), gatewayruntimecache.GroupModelUnionListOptions{
		CallerSystemAccountID: caller,
		GroupIDs:              groupIDs,
	})
	if err != nil {
		t.Fatalf("ListGroupModelUnion: %v", err)
	}
	return entry
}

func gkmAssertModels(t *testing.T, entry gatewayruntimecache.GroupModelUnionEntry, want ...string) {
	t.Helper()
	// 变参零参调用产生 nil 切片，装载器契约返回非 nil 空切片：两侧归一后再比。
	expected := want
	if expected == nil {
		expected = []string{}
	}
	got := entry.Models
	if got == nil {
		got = []string{}
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("models = %#v, want %#v", entry.Models, want)
	}
}

// ---------------------------------------------------------------------------
// A. 并集装载器：成员资格 / 分组属主域 / 静态门
// ---------------------------------------------------------------------------

// 基本并集 + 跨属主组级授权可见 + 不同属主域绑定不并入（§6.5 聚合语义行、§8.19）。
func TestGKMUnionLoaderOwnerDomainUnion(t *testing.T) {
	fixture := newChainFixture(t)
	db := fixture.db
	loader := gkmUnionLoader(t, fixture)

	// 夹具既有：group_main（owner=sys_owner）+ acc_1（SM gpt-test）。
	entry := gkmLoad(t, loader, "sys_owner", "group_main")
	gkmAssertModels(t, entry, "gpt-test")
	if !entry.ValidUntil.Equal(gkmBase.Add(time.Hour)) {
		t.Fatalf("无未来到期点 validUntil = %v, want 基础 TTL", entry.ValidUntil)
	}

	// 跨属主组级授权：group_b 属 sys_other，调用方经有效组级授权可见。
	gkmSeedGroup(t, db, "group_b", "sys_other", "openai", 1)
	gkmSeedAccount(t, db, gkmAccountSeed{id: "acc_b1", owner: "sys_other"})
	gkmSeedSupportedModel(t, db, "acc_b1", "openai", "m3")
	gkmSeedBinding(t, db, "group_b", "sys_other", "acc_b1", "", 1)
	gkmSeedAuthorization(t, db, "authz_grp_b", "group", "group_b", "sys_other", "sys_owner", "active", time.Time{})
	entry = gkmLoad(t, loader, "sys_owner", "group_main", "group_b")
	gkmAssertModels(t, entry, "gpt-test", "m3")

	// 不同属主域绑定不并入：group_c 属 sys_other2，绑定行却写在调用方域
	//（ga.system_account_id = sys_owner ≠ g.system_account_id）→ 过滤。
	gkmSeedGroup(t, db, "group_c", "sys_other2", "openai", 1)
	gkmSeedAccount(t, db, gkmAccountSeed{id: "acc_c1", owner: "sys_other2"})
	gkmSeedSupportedModel(t, db, "acc_c1", "openai", "m-c")
	gkmSeedBinding(t, db, "group_c", "sys_owner", "acc_c1", "", 1)
	gkmSeedAuthorization(t, db, "authz_grp_c", "group", "group_c", "sys_other2", "sys_owner", "active", time.Time{})
	entry = gkmLoad(t, loader, "sys_owner", "group_c")
	gkmAssertModels(t, entry)

	// 多分组输入去重：重复分组 ID 只装载一次。
	entry = gkmLoad(t, loader, "sys_owner", "group_main", "group_main")
	gkmAssertModels(t, entry, "gpt-test")
}

// 静态门（§4.4/§6.5 聚合语义行）：状态放宽档保留、cooldown/pending/删除/
// 不可调度/过期/禁用剔除；禁用分组整组不贡献。
func TestGKMUnionLoaderStaticGates(t *testing.T) {
	fixture := newChainFixture(t)
	db := fixture.db
	loader := gkmUnionLoader(t, fixture)

	gkmSeedGroup(t, db, "gkm_gates", "sys_owner", "openai", 1)
	gkmSeedAccount(t, db, gkmAccountSeed{id: "acc_rate", owner: "sys_owner", status: "rate_limited", cooldownUntil: gkmBase.Add(time.Hour)})
	gkmSeedSupportedModel(t, db, "acc_rate", "openai", "m-rate")
	gkmSeedBinding(t, db, "gkm_gates", "sys_owner", "acc_rate", "", 1)

	gkmSeedAccount(t, db, gkmAccountSeed{id: "acc_temp", owner: "sys_owner", status: "temporary_unavailable"})
	gkmSeedSupportedModel(t, db, "acc_temp", "openai", "m-temp")
	gkmSeedBinding(t, db, "gkm_gates", "sys_owner", "acc_temp", "", 1)

	gkmSeedAccount(t, db, gkmAccountSeed{id: "acc_pending", owner: "sys_owner", status: "pending_test"})
	gkmSeedSupportedModel(t, db, "acc_pending", "openai", "m-pending")
	gkmSeedBinding(t, db, "gkm_gates", "sys_owner", "acc_pending", "", 1)

	gkmSeedAccount(t, db, gkmAccountSeed{id: "acc_unsched", owner: "sys_owner", schedDisabled: true})
	gkmSeedSupportedModel(t, db, "acc_unsched", "openai", "m-unsched")
	gkmSeedBinding(t, db, "gkm_gates", "sys_owner", "acc_unsched", "", 1)

	gkmSeedAccount(t, db, gkmAccountSeed{id: "acc_deleted", owner: "sys_owner", deleted: true})
	gkmSeedSupportedModel(t, db, "acc_deleted", "openai", "m-deleted")
	gkmSeedBinding(t, db, "gkm_gates", "sys_owner", "acc_deleted", "", 1)

	gkmSeedAccount(t, db, gkmAccountSeed{id: "acc_expired", owner: "sys_owner", expiresAt: gkmBase.Add(-time.Minute)})
	gkmSeedSupportedModel(t, db, "acc_expired", "openai", "m-expired")
	gkmSeedBinding(t, db, "gkm_gates", "sys_owner", "acc_expired", "", 1)

	gkmSeedAccount(t, db, gkmAccountSeed{id: "acc_ok", owner: "sys_owner"})
	gkmSeedSupportedModel(t, db, "acc_ok", "openai", "m-ok")
	gkmSeedBinding(t, db, "gkm_gates", "sys_owner", "acc_ok", "", 1)

	gkmSeedAccount(t, db, gkmAccountSeed{id: "acc_binding_off", owner: "sys_owner"})
	gkmSeedSupportedModel(t, db, "acc_binding_off", "openai", "m-binding-off")
	gkmSeedBinding(t, db, "gkm_gates", "sys_owner", "acc_binding_off", "", 0)

	entry := gkmLoad(t, loader, "sys_owner", "gkm_gates")
	gkmAssertModels(t, entry, "m-ok", "m-rate", "m-temp")

	// 禁用分组：整组不贡献。
	gkmSeedGroup(t, db, "gkm_disabled", "sys_owner", "openai", 0)
	gkmSeedAccount(t, db, gkmAccountSeed{id: "acc_disabled_grp", owner: "sys_owner"})
	gkmSeedSupportedModel(t, db, "acc_disabled_grp", "openai", "m-disabled")
	gkmSeedBinding(t, db, "gkm_disabled", "sys_owner", "acc_disabled_grp", "", 1)
	entry = gkmLoad(t, loader, "sys_owner", "gkm_disabled")
	gkmAssertModels(t, entry)
}

// ---------------------------------------------------------------------------
// A. 并集装载器：授权门（实例臂互斥 / 后置绑定门 / 撤销与到期 / settings 三态）
// ---------------------------------------------------------------------------

// 实例模型取自来源账户（§8.9）+ 实例自有模型行不计数 + 后置绑定门
// （绑定授权空/不一致拒绝，§8.10 后半）。
func TestGKMUnionLoaderInstanceFactSubjectAndBindingGate(t *testing.T) {
	fixture := newChainFixture(t)
	db := fixture.db
	loader := gkmUnionLoader(t, fixture)

	gkmSeedGroup(t, db, "gkm_inst", "sys_owner", "openai", 1)
	// 绑定一致：可见，模型取自来源账户。
	gkmSeedAccount(t, db, gkmAccountSeed{id: "src_ok", owner: "sys_owner"})
	gkmSeedSupportedModel(t, db, "src_ok", "openai", "m-ok")
	gkmSeedAccount(t, db, gkmAccountSeed{id: "inst_ok", owner: "sys_owner", accountType: "oauth", sourceID: "src_ok", instanceAuthz: "authz_ok"})
	gkmSeedSupportedModel(t, db, "inst_ok", "openai", "m-own-ignored")
	gkmSeedAuthorization(t, db, "authz_ok", "account", "src_ok", "sys_owner", "sys_owner", "active", time.Time{})
	gkmSeedBinding(t, db, "gkm_inst", "sys_owner", "inst_ok", "authz_ok", 1)

	// 绑定授权为空：拒绝。
	gkmSeedAccount(t, db, gkmAccountSeed{id: "src_empty", owner: "sys_owner"})
	gkmSeedSupportedModel(t, db, "src_empty", "openai", "m-empty")
	gkmSeedAccount(t, db, gkmAccountSeed{id: "inst_empty", owner: "sys_owner", accountType: "oauth", sourceID: "src_empty", instanceAuthz: "authz_empty"})
	gkmSeedAuthorization(t, db, "authz_empty", "account", "src_empty", "sys_owner", "sys_owner", "active", time.Time{})
	gkmSeedBinding(t, db, "gkm_inst", "sys_owner", "inst_empty", "", 1)

	// 绑定授权不一致：拒绝。
	gkmSeedAccount(t, db, gkmAccountSeed{id: "src_mismatch", owner: "sys_owner"})
	gkmSeedSupportedModel(t, db, "src_mismatch", "openai", "m-mismatch")
	gkmSeedAccount(t, db, gkmAccountSeed{id: "inst_mismatch", owner: "sys_owner", accountType: "oauth", sourceID: "src_mismatch", instanceAuthz: "authz_mismatch"})
	gkmSeedAuthorization(t, db, "authz_mismatch", "account", "src_mismatch", "sys_owner", "sys_owner", "active", time.Time{})
	gkmSeedBinding(t, db, "gkm_inst", "sys_owner", "inst_mismatch", "authz_other", 1)

	entry := gkmLoad(t, loader, "sys_owner", "gkm_inst")
	gkmAssertModels(t, entry, "m-ok")
}

// 实例臂互斥（§8.16）：A 的实例账户所在分组再授权给 C → C 不可见实例模型，
// 但同组 A 直连账户经授权分组臂仍可见。
func TestGKMUnionLoaderInstanceArmExclusivity(t *testing.T) {
	fixture := newChainFixture(t)
	db := fixture.db
	loader := gkmUnionLoader(t, fixture)

	gkmSeedGroup(t, db, "gkm_excl", "sys_A", "openai", 1)
	gkmSeedAccount(t, db, gkmAccountSeed{id: "acc16src", owner: "sys_A"})
	gkmSeedSupportedModel(t, db, "acc16src", "openai", "m16inst")
	gkmSeedAccount(t, db, gkmAccountSeed{id: "acc16inst", owner: "sys_B", accountType: "oauth", sourceID: "acc16src", instanceAuthz: "authz16"})
	gkmSeedAuthorization(t, db, "authz16", "account", "acc16src", "sys_A", "sys_B", "active", time.Time{})
	gkmSeedBinding(t, db, "gkm_excl", "sys_A", "acc16inst", "authz16", 1)

	gkmSeedAccount(t, db, gkmAccountSeed{id: "acc16direct", owner: "sys_A"})
	gkmSeedSupportedModel(t, db, "acc16direct", "openai", "m16direct")
	gkmSeedBinding(t, db, "gkm_excl", "sys_A", "acc16direct", "", 1)

	gkmSeedAuthorization(t, db, "authz_grp16", "group", "gkm_excl", "sys_A", "sys_C", "active", time.Time{})
	entry := gkmLoad(t, loader, "sys_C", "gkm_excl")
	gkmAssertModels(t, entry, "m16direct")
}

// 账户级/组级授权撤销与到期剔除（§6.5 授权门行、§8.10/§8.11）。
//
// 账户级授权经实例臂覆盖：owner 域批量预取（loadAccountAuthorizationsForSelection
// 同款）只装载候选行的实例授权 ID，直连他人账户的独立授权行不在预取 map 中、
// 与调度 resolveChainAccountAccess 同样不可达——这是镜像调度实态的结果，不在
// 本装载器另开 DB 解析旁路。
func TestGKMUnionLoaderAuthorizationRevocation(t *testing.T) {
	fixture := newChainFixture(t)
	db := fixture.db
	loader := gkmUnionLoader(t, fixture)

	// 实例账户：账户级授权撤销/过期 → 模型消失。
	gkmSeedGroup(t, db, "gkm_acc_authz", "sys_owner", "openai", 1)
	gkmSeedAccount(t, db, gkmAccountSeed{id: "src_foreign", owner: "sys_other"})
	gkmSeedSupportedModel(t, db, "src_foreign", "openai", "m-foreign")
	gkmSeedAccount(t, db, gkmAccountSeed{id: "inst_foreign", owner: "sys_owner", accountType: "oauth", sourceID: "src_foreign", instanceAuthz: "authz_acc"})
	gkmSeedAuthorization(t, db, "authz_acc", "account", "src_foreign", "sys_other", "sys_owner", "active", time.Time{})
	gkmSeedBinding(t, db, "gkm_acc_authz", "sys_owner", "inst_foreign", "authz_acc", 1)
	entry := gkmLoad(t, loader, "sys_owner", "gkm_acc_authz")
	gkmAssertModels(t, entry, "m-foreign")

	gkmUpdateAuthorizationStatus(t, db, "authz_acc", "revoked")
	entry = gkmLoad(t, loader, "sys_owner", "gkm_acc_authz")
	gkmAssertModels(t, entry)

	gkmUpdateAuthorizationStatus(t, db, "authz_acc", "active")
	if _, err := db.Exec(`UPDATE resource_authorizations SET expires_at = ? WHERE id = 'authz_acc'`, gkmTS(gkmBase.Add(-time.Minute))); err != nil {
		t.Fatalf("expire authz_acc: %v", err)
	}
	entry = gkmLoad(t, loader, "sys_owner", "gkm_acc_authz")
	gkmAssertModels(t, entry)

	// 组级授权撤销 → 整组消失（即使组内有调用方自己的账户）。
	gkmSeedGroup(t, db, "gkm_grp_authz", "sys_other", "openai", 1)
	gkmSeedAccount(t, db, gkmAccountSeed{id: "acc_grp_owner", owner: "sys_other"})
	gkmSeedSupportedModel(t, db, "acc_grp_owner", "openai", "m-grp")
	gkmSeedBinding(t, db, "gkm_grp_authz", "sys_other", "acc_grp_owner", "", 1)
	gkmSeedAuthorization(t, db, "authz_grp", "group", "gkm_grp_authz", "sys_other", "sys_owner", "active", time.Time{})
	entry = gkmLoad(t, loader, "sys_owner", "gkm_grp_authz")
	gkmAssertModels(t, entry, "m-grp")

	gkmUpdateAuthorizationStatus(t, db, "authz_grp", "suspended")
	entry = gkmLoad(t, loader, "sys_owner", "gkm_grp_authz")
	gkmAssertModels(t, entry)

	gkmUpdateAuthorizationStatus(t, db, "authz_grp", "active")
	if _, err := db.Exec(`UPDATE resource_authorizations SET expires_at = ? WHERE id = 'authz_grp'`, gkmTS(gkmBase.Add(-time.Minute))); err != nil {
		t.Fatalf("expire authz_grp: %v", err)
	}
	entry = gkmLoad(t, loader, "sys_owner", "gkm_grp_authz")
	gkmAssertModels(t, entry)
}

// 分组授权本地设置三态（§8.15）：不存在放行、enabled=0 拒绝、恢复后重现。
func TestGKMUnionLoaderGroupSettingsTriState(t *testing.T) {
	fixture := newChainFixture(t)
	db := fixture.db
	loader := gkmUnionLoader(t, fixture)

	gkmSeedGroup(t, db, "gkm_settings", "sys_other", "openai", 1)
	gkmSeedAccount(t, db, gkmAccountSeed{id: "acc_set", owner: "sys_other"})
	gkmSeedSupportedModel(t, db, "acc_set", "openai", "m-set")
	gkmSeedBinding(t, db, "gkm_settings", "sys_other", "acc_set", "", 1)
	gkmSeedAuthorization(t, db, "authz_set", "group", "gkm_settings", "sys_other", "sys_owner", "active", time.Time{})

	// 不存在 → 放行。
	entry := gkmLoad(t, loader, "sys_owner", "gkm_settings")
	gkmAssertModels(t, entry, "m-set")

	// enabled=0 → 拒绝。
	gkmSeedGroupSettings(t, db, "authz_set", "sys_owner", "gkm_settings", 0)
	entry = gkmLoad(t, loader, "sys_owner", "gkm_settings")
	gkmAssertModels(t, entry)

	// 恢复 enabled=1 → 重新出现。
	if _, err := db.Exec(`UPDATE group_authorization_settings SET enabled = 1 WHERE authorization_id = 'authz_set'`); err != nil {
		t.Fatalf("restore settings: %v", err)
	}
	entry = gkmLoad(t, loader, "sys_owner", "gkm_settings")
	gkmAssertModels(t, entry, "m-set")
}

// ---------------------------------------------------------------------------
// A. 并集装载器：映射有效性（§4.2/§6.5 映射有效性行）
// ---------------------------------------------------------------------------

func TestGKMUnionLoaderMappingValidity(t *testing.T) {
	fixture := newChainFixture(t)
	db := fixture.db
	loader := gkmUnionLoader(t, fixture)

	gkmSeedGroup(t, db, "gkm_map", "sys_owner", "openai", 1)
	gkmSeedAccount(t, db, gkmAccountSeed{id: "acc_map", owner: "sys_owner"})
	gkmSeedSupportedModel(t, db, "acc_map", "openai", "gpt-test")

	// 有效映射：同族跨模型改写 → 并入。
	gkmSeedMapping(t, db, "acc_map", "openai", "m-cross", "chat_completions", "gpt-test", "chat_completions", 1)
	// upstream ∉ SM → 不并入。
	gkmSeedMapping(t, db, "acc_map", "openai", "m-noup", "chat_completions", "m-nosuch", "chat_completions", 1)
	// 恒等改写 → 不并入。
	gkmSeedMapping(t, db, "acc_map", "openai", "gpt-test", "chat_completions", "gpt-test", "chat_completions", 1)
	// 矩阵不支持的族对（openai 非混合账户：video_generation → chat_completions）→ 不并入。
	gkmSeedMapping(t, db, "acc_map", "openai", "m-matrix", "video_generation", "gpt-test", "chat_completions", 1)
	// 矩阵支持的族对（stream_generate_content → generate_content，全档案放行）→ 并入。
	gkmSeedMapping(t, db, "acc_map", "openai", "m-gemini", "stream_generate_content", "gpt-test", "generate_content", 1)
	// 供应商门：跨供应商历史映射行 → 不并入。
	gkmSeedMapping(t, db, "acc_map", "anthropic", "m-provider", "chat_completions", "gpt-test", "chat_completions", 1)
	// 禁用映射 → 不并入。
	gkmSeedMapping(t, db, "acc_map", "openai", "m-disabled", "chat_completions", "gpt-test", "chat_completions", 0)
	// 跨端点族同源模型 → 去重为单条目（responses → chat_completions 依赖
	// openai 协议档案，夹具账户 prof_1/openai/v1 命中）。
	gkmSeedMapping(t, db, "acc_map", "openai", "m-fam", "chat_completions", "gpt-test", "chat_completions", 1)
	gkmSeedMapping(t, db, "acc_map", "openai", "m-fam", "responses", "gpt-test", "chat_completions", 1)
	gkmSeedBinding(t, db, "gkm_map", "sys_owner", "acc_map", "", 1)

	// 空 SM 事实主体：直接模型与映射源都不产出（§4.3/§8.18）。
	gkmSeedAccount(t, db, gkmAccountSeed{id: "acc_nosm", owner: "sys_owner"})
	gkmSeedMapping(t, db, "acc_nosm", "openai", "m-nosm", "chat_completions", "whatever", "chat_completions", 1)
	gkmSeedBinding(t, db, "gkm_map", "sys_owner", "acc_nosm", "", 1)

	entry := gkmLoad(t, loader, "sys_owner", "gkm_map")
	gkmAssertModels(t, entry, "gpt-test", "m-cross", "m-fam", "m-gemini")

	// 协议档案排除（Gemini OpenAI-chat 档案 + anthropic messages 源族）。
	gkmSeedGroup(t, db, "gkm_profile", "sys_owner", "openai", 1)
	gkmSeedAccount(t, db, gkmAccountSeed{id: "acc_gemini", owner: "sys_owner", profile: "profile_gemini_openai_chat_v1beta"})
	gkmSeedSupportedModel(t, db, "acc_gemini", "openai", "gpt-x")
	gkmSeedMapping(t, db, "acc_gemini", "openai", "m-gem-excluded", "messages", "gpt-x", "chat_completions", 1)
	gkmSeedMapping(t, db, "acc_gemini", "openai", "m-gem-kept", "chat_completions", "gpt-x", "chat_completions", 1)
	gkmSeedBinding(t, db, "gkm_profile", "sys_owner", "acc_gemini", "", 1)
	entry = gkmLoad(t, loader, "sys_owner", "gkm_profile")
	gkmAssertModels(t, entry, "gpt-x", "m-gem-kept")
}

// ---------------------------------------------------------------------------
// A. 并集装载器：validUntil（注入时钟；§6.3/§6.5）
// ---------------------------------------------------------------------------

func TestGKMUnionLoaderValidUntil(t *testing.T) {
	fixture := newChainFixture(t)
	db := fixture.db
	loader := gkmUnionLoader(t, fixture)
	baseTTL := gkmBase.Add(time.Hour)

	assertValidUntil := func(name string, entry gatewayruntimecache.GroupModelUnionEntry, want time.Time) {
		t.Helper()
		if !entry.ValidUntil.Equal(want) {
			t.Fatalf("%s validUntil = %v, want %v", name, entry.ValidUntil, want)
		}
	}

	// 账户到期收窄。
	gkmSeedGroup(t, db, "gkm_v_acc", "sys_owner", "openai", 1)
	gkmSeedAccount(t, db, gkmAccountSeed{id: "acc_v", owner: "sys_owner", expiresAt: gkmBase.Add(30 * time.Minute)})
	gkmSeedSupportedModel(t, db, "acc_v", "openai", "m-v")
	gkmSeedBinding(t, db, "gkm_v_acc", "sys_owner", "acc_v", "", 1)
	assertValidUntil("账户到期", gkmLoad(t, loader, "sys_owner", "gkm_v_acc"), gkmBase.Add(30*time.Minute))

	// 来源账户到期收窄（实例臂）。
	gkmSeedGroup(t, db, "gkm_v_src", "sys_owner", "openai", 1)
	gkmSeedAccount(t, db, gkmAccountSeed{id: "src_v", owner: "sys_owner", expiresAt: gkmBase.Add(20 * time.Minute)})
	gkmSeedSupportedModel(t, db, "src_v", "openai", "m-src")
	gkmSeedAccount(t, db, gkmAccountSeed{id: "inst_v", owner: "sys_owner", accountType: "oauth", sourceID: "src_v", instanceAuthz: "authz_v"})
	gkmSeedAuthorization(t, db, "authz_v", "account", "src_v", "sys_owner", "sys_owner", "active", time.Time{})
	gkmSeedBinding(t, db, "gkm_v_src", "sys_owner", "inst_v", "authz_v", 1)
	assertValidUntil("来源账户到期", gkmLoad(t, loader, "sys_owner", "gkm_v_src"), gkmBase.Add(20*time.Minute))

	// 组级授权到期收窄。
	gkmSeedGroup(t, db, "gkm_v_grp", "sys_other", "openai", 1)
	gkmSeedAccount(t, db, gkmAccountSeed{id: "acc_vg", owner: "sys_other"})
	gkmSeedSupportedModel(t, db, "acc_vg", "openai", "m-vg")
	gkmSeedBinding(t, db, "gkm_v_grp", "sys_other", "acc_vg", "", 1)
	gkmSeedAuthorization(t, db, "authz_vg", "group", "gkm_v_grp", "sys_other", "sys_owner", "active", gkmBase.Add(10*time.Minute))
	assertValidUntil("组级授权到期", gkmLoad(t, loader, "sys_owner", "gkm_v_grp"), gkmBase.Add(10*time.Minute))

	// 账户级授权到期收窄（实例臂——owner 域预取 map 只含实例授权行，与调度
	// loadAccountAuthorizationsForSelection 同款）。
	gkmSeedGroup(t, db, "gkm_v_accauthz", "sys_owner", "openai", 1)
	gkmSeedAccount(t, db, gkmAccountSeed{id: "src_va", owner: "sys_other"})
	gkmSeedSupportedModel(t, db, "src_va", "openai", "m-va")
	gkmSeedAccount(t, db, gkmAccountSeed{id: "inst_va", owner: "sys_owner", accountType: "oauth", sourceID: "src_va", instanceAuthz: "authz_va"})
	gkmSeedAuthorization(t, db, "authz_va", "account", "src_va", "sys_other", "sys_owner", "active", gkmBase.Add(5*time.Minute))
	gkmSeedBinding(t, db, "gkm_v_accauthz", "sys_owner", "inst_va", "authz_va", 1)
	assertValidUntil("账户级授权到期", gkmLoad(t, loader, "sys_owner", "gkm_v_accauthz"), gkmBase.Add(5*time.Minute))

	// 已过期到期点忽略：保持基础 TTL。
	gkmSeedGroup(t, db, "gkm_v_past", "sys_owner", "openai", 1)
	gkmSeedAccount(t, db, gkmAccountSeed{id: "acc_vp", owner: "sys_owner", expiresAt: gkmBase.Add(-time.Minute)})
	gkmSeedSupportedModel(t, db, "acc_vp", "openai", "m-vp")
	gkmSeedBinding(t, db, "gkm_v_past", "sys_owner", "acc_vp", "", 1)
	assertValidUntil("过去到期点", gkmLoad(t, loader, "sys_owner", "gkm_v_past"), baseTTL)
}

// ---------------------------------------------------------------------------
// B. 端口实现（chainGatewayKeyModelCatalog）
// ---------------------------------------------------------------------------

// gkmFakeUnionLoader 是并集装载端口的可控替身：返回预置条目/错误并记录调用。
type gkmFakeUnionLoader struct {
	mu    sync.Mutex
	entry gatewayruntimecache.GroupModelUnionEntry
	err   error
	calls []gatewayruntimecache.GroupModelUnionListOptions
}

func (f *gkmFakeUnionLoader) ListGroupModelUnion(_ context.Context, input gatewayruntimecache.GroupModelUnionListOptions) (gatewayruntimecache.GroupModelUnionEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, input)
	if f.err != nil {
		return gatewayruntimecache.GroupModelUnionEntry{}, f.err
	}
	return f.entry, nil
}

func (f *gkmFakeUnionLoader) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// gkmCountingModels 在 w1hReadModels 之上记录目录读取调用。
type gkmCountingModels struct {
	*w1hReadModels
	mu           sync.Mutex
	catalogCalls []gatewayruntimecache.ModelCatalogListOptions
}

func (m *gkmCountingModels) ListProviderModelCatalog(ctx context.Context, input gatewayruntimecache.ModelCatalogListOptions) ([]gatewayruntimecache.ProviderModelCatalogItem, error) {
	m.mu.Lock()
	m.catalogCalls = append(m.catalogCalls, input)
	m.mu.Unlock()
	return m.w1hReadModels.ListProviderModelCatalog(ctx, input)
}

func (m *gkmCountingModels) catalogCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.catalogCalls)
}

func gkmPortCache(t *testing.T, models gatewayruntimecache.ReadModels, union gatewayruntimecache.GroupModelUnionLoader) *gatewayruntimecache.Service {
	t.Helper()
	service, err := gatewayruntimecache.New(models, gatewayruntimecache.Options{Union: union})
	if err != nil {
		t.Fatalf("组装端口 runtime cache: %v", err)
	}
	t.Cleanup(service.Close)
	return service
}

// 端口主路径：并集去重排序入参、字典 best-scope 命中元数据、未命中合成、
// 字典序排序、空绑定空列表不触发装载。
func TestGKMModelCatalogPortMerge(t *testing.T) {
	window := int64(128000)
	personalWindow := int64(200000)
	models := &gkmCountingModels{w1hReadModels: &w1hReadModels{catalog: []gatewayruntimecache.ProviderModelCatalogItem{
		{Scope: "global", Status: "active", ProviderCode: "openai", Model: "m-hit", ContextWindowTokens: &window},
		// 同模型 personal 作用域副本：字典取 best-scope（personal）。
		{Scope: "personal", Status: "active", ProviderCode: "openai", Model: "m-hit", ContextWindowTokens: &personalWindow},
		// 非 active 行不进字典。
		{Scope: "global", Status: "inactive", ProviderCode: "openai", Model: "m-hit"},
		// 并集未包含的目录模型不出现（成员资格来自并集，目录只是元数据字典）。
		{Scope: "global", Status: "active", ProviderCode: "openai", Model: "m-not-in-union"},
	}}}
	union := &gkmFakeUnionLoader{entry: gatewayruntimecache.GroupModelUnionEntry{
		Models:     []string{"m-missing", "m-hit", "a-first"},
		ValidUntil: gkmBase.Add(30 * time.Minute),
	}}
	loader := chainGatewayKeyModelCatalog{cache: gkmPortCache(t, models, union), now: gkmNow}

	result, err := loader.ListGatewayKeyModels(context.Background(), "sys-1", []gatewayresponse.GatewayModelBinding{
		{GroupID: "grp-b", ProviderCode: "openai"},
		{GroupID: "grp-a", ProviderCode: "openai"},
		{GroupID: "grp-b", ProviderCode: "openai"},
	})
	if err != nil {
		t.Fatalf("ListGatewayKeyModels: %v", err)
	}
	// 字典序排序 + 命中/合成。
	if len(result.Entries) != 3 {
		t.Fatalf("entries = %+v, want 3", result.Entries)
	}
	if result.Entries[0].Model != "a-first" || result.Entries[0].Scope != "" || result.Entries[0].ContextWindowTokens != 0 {
		t.Fatalf("合成条目不符: %+v", result.Entries[0])
	}
	if result.Entries[1].Model != "m-hit" || result.Entries[1].Scope != "personal" || result.Entries[1].ContextWindowTokens != 200000 {
		t.Fatalf("字典命中应取 personal 最优行: %+v", result.Entries[1])
	}
	if result.Entries[2].Model != "m-missing" || result.Entries[2].Scope != "" {
		t.Fatalf("合成条目不符: %+v", result.Entries[2])
	}
	if !result.ValidUntil.Equal(gkmBase.Add(30 * time.Minute)) {
		t.Fatalf("ValidUntil = %v, want 并集条目有效期", result.ValidUntil)
	}
	// 并集输入：分组去重排序 + 调用方透传；目录调用：单 provider 一次。
	if union.callCount() != 1 {
		t.Fatalf("并集调用次数 = %d, want 1", union.callCount())
	}
	got := union.calls[0]
	if got.CallerSystemAccountID != "sys-1" || !reflect.DeepEqual(got.GroupIDs, []string{"grp-a", "grp-b"}) {
		t.Fatalf("并集入参 = %+v", got)
	}
	if models.catalogCallCount() != 1 || models.catalogCalls[0].ProviderCode != "openai" {
		t.Fatalf("目录调用 = %+v", models.catalogCalls)
	}

	// 空 bindings：空列表（非 nil）+ 不触发并集装载（§4.7）。
	empty, err := loader.ListGatewayKeyModels(context.Background(), "sys-1", nil)
	if err != nil {
		t.Fatalf("空绑定: %v", err)
	}
	if empty.Entries == nil || len(empty.Entries) != 0 {
		t.Fatalf("空绑定 entries = %+v, want 非 nil 空列表", empty.Entries)
	}
	if !empty.ValidUntil.Equal(gkmBase.Add(time.Hour)) {
		t.Fatalf("空绑定 validUntil = %v, want 基础 TTL", empty.ValidUntil)
	}
	if union.callCount() != 1 {
		t.Fatalf("空绑定不得触发并集装载，调用次数 = %d", union.callCount())
	}
}

// 端口错误臂：union loader 错误、目录装载错误、cache 缺席均原样上抛，
// 不渲染成空列表（§4.6.2）。
func TestGKMModelCatalogPortErrorArms(t *testing.T) {
	boom := errors.New("并集装载失败")
	union := &gkmFakeUnionLoader{err: boom}
	loader := chainGatewayKeyModelCatalog{cache: gkmPortCache(t, &w1hReadModels{}, union), now: gkmNow}
	if _, err := loader.ListGatewayKeyModels(context.Background(), "sys-1", []gatewayresponse.GatewayModelBinding{{GroupID: "g", ProviderCode: "openai"}}); !errors.Is(err, boom) {
		t.Fatalf("union 错误必须上抛, got %v", err)
	}

	// 目录装载错误（w1tTailModels 的 catalogErr 注入臂）。
	catalogUnion := &gkmFakeUnionLoader{entry: gatewayruntimecache.GroupModelUnionEntry{Models: []string{"m1"}, ValidUntil: gkmBase.Add(time.Hour)}}
	_, err := chainGatewayKeyModelCatalog{cache: gkmPortCache(t, &w1tTailModels{catalogErr: errors.New("目录查询失败")}, catalogUnion), now: gkmNow}.
		ListGatewayKeyModels(context.Background(), "sys-1", []gatewayresponse.GatewayModelBinding{{GroupID: "g", ProviderCode: "openai"}})
	if err == nil {
		t.Fatal("目录装载错误必须上抛")
	}

	// cache 缺席（装配错误）。
	if _, err := (chainGatewayKeyModelCatalog{}).ListGatewayKeyModels(context.Background(), "sys-1", nil); err == nil {
		t.Fatal("cache 缺席必须报错")
	}
}
