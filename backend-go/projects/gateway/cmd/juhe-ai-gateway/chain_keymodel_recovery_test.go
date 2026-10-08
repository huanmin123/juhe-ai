package main

// chain_keymodel_recovery_test.go — PLAN-20261008T113056000Z 根治阶段：
// key-model memory 恢复驱动的装配存在性、窄 InputLoader（真 SQLite 库）、
// 探针适配器 fence/错误臂与 outcome 三分支映射测试。

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
	_ "modernc.org/sqlite"
)

// ---- 装配存在性（照 chain_circuit_recovery_test.go 模式） ----

func TestChainKeyModelRecoveryWiredForMemoryDriver(t *testing.T) {
	fixture := newChainFixture(t)
	cfg := composeTestConfig(t)
	composed := &composition{db: fixture.db, statsDB: fixture.statsDB, Bus: nil}
	services, err := composeChainRuntimeServices(composed, cfg, func(string) (string, error) { return "UTC", nil })
	if err != nil {
		t.Fatalf("composeChainRuntimeServices: %v", err)
	}
	t.Cleanup(services.Close)

	if services.KeyModelStore == nil {
		t.Fatal("memory 形态必须装配 key-model 前台准入 store")
	}
	if services.KeyModelMemoryRecovery.Run == nil {
		t.Fatal("memory 形态必须装配 key-model 恢复组件（空转缺口根治断言）")
	}
	if services.KeyModelMemoryRecovery.Name != "key-model-memory-recovery" {
		t.Fatalf("恢复组件名 = %q", services.KeyModelMemoryRecovery.Name)
	}
	// 组件节拍循环不进入：cancel ctx 验证 Run 可被取消退出（生命周期契约）。
	runCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if runErr := services.KeyModelMemoryRecovery.Run(runCtx); runErr == nil {
		t.Fatal("取消后 Run 必须返回 ctx 错误")
	}
}

func TestChainKeyModelRecoveryNotWiredForRedisDriver(t *testing.T) {
	fixture := newChainFixture(t)
	cfg := composeTestConfig(t)
	server := miniredis.RunT(t)
	cfg.RuntimeMode = "performance"
	cfg.RuntimeStateDriver = "redis"
	cfg.RedisStateURL = "redis://" + server.Addr()
	cfg.RedisNamespace = "chain-keymodel-recovery-test"
	composed := &composition{db: fixture.db, statsDB: fixture.statsDB, Bus: nil}
	services, err := composeChainRuntimeServices(composed, cfg, func(string) (string, error) { return "UTC", nil })
	if err != nil {
		t.Fatalf("composeChainRuntimeServices: %v", err)
	}
	t.Cleanup(services.Close)

	if services.KeyModelStore == nil {
		t.Fatal("redis 形态仍必须装配 key-model 前台准入 store（主链行为不变）")
	}
	// redis 形态恢复职责归 jobs keymodelrecovery：组件保持零值，main.go
	// 判 Run 非 nil 不挂载（避免双 owner 重复探针）。
	if services.KeyModelMemoryRecovery.Run != nil || services.KeyModelMemoryRecovery.Name != "" {
		t.Fatalf("redis 形态不得装配 key-model 恢复组件: name=%q run=%v",
			services.KeyModelMemoryRecovery.Name, services.KeyModelMemoryRecovery.Run != nil)
	}
}

// ---- 窄 InputLoader（真 SQLite 库） ----

// newKeyModelRecoveryLoaderDB 用维护侧权威 business schema 建测试库（复审
// 建议③：fixture 建表 = 权威 DDL，loader SQL 列名漂移在真库上直接报错而非
// 被手写 fixture 静默吸收）。bootstrap 是用户批准的 gateway→maintenance 受控
// 导出面（bootstrap.go 包头基线注记），gateway 生产 storage_bootstrap.go 同源
// 调用。权威脚本自带 PRAGMA foreign_keys=ON（Node 同位契约，ensure 后生效）；
// 本 loader 是单账户只读投影，FK/父表种子（providers/system_accounts/groups
// 等）不在测试目标内，ensure 后显式关闭，种子行只覆盖 loader 实际读取的
// 5 张表并补齐权威 DDL 的 NOT NULL 列（name/created_at/updated_at）。
func newKeyModelRecoveryLoaderDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "keymodel-recovery.sqlite3"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// 连接级 PRAGMA（foreign_keys）只作用于命中的池化连接；固定单连接使
	// pragma 状态确定（对齐生产 OpenSQLiteFile 的单连接约束）。
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := bootstrap.EnsureSQLiteSchema(context.Background(), bootstrap.SQLiteSchemaBusiness, db); err != nil {
		t.Fatalf("ensure authoritative business sqlite schema: %v", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatalf("disable foreign keys (loader read-only projection): %v", err)
	}
	const secret = "keymodel-recovery-test-secret"
	return db, secret
}

// 主路径：真 SQLite 库 + EncryptV1Envelope 造密文，断言 LoadAccount 产出的
// Input 关键字段（账户/revision fence、base_url、provider/profile、健康模型
// 直通、Key 指纹 HMAC 与封套可解密、输入有效期）。
func TestChainKeyModelProbeInputLoaderSQLite(t *testing.T) {
	db, secret := newKeyModelRecoveryLoaderDB(t)
	const (
		accountID   = "km-loader-acc"
		systemID    = "sys_owner"
		groupID     = "grp_main"
		apiKey      = "sk-recovery-key"
		baseURL     = "https://upstream.example/v1"
		revisionID  = int64(5)
		configRevID = int64(2)
	)
	credentials, err := exactkeyprobe.EncryptV1Envelope(secret, []byte(`{"api_key":"`+apiKey+`","base_url":"`+baseURL+`"}`))
	if err != nil {
		t.Fatalf("encrypt credentials: %v", err)
	}
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed row: %v: %v", query, err)
		}
	}
	seed(`INSERT INTO accounts (id, system_account_id, config_revision, dispatch_revision, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
		name, type, client_compatibility, status, schedulable, health_check_endpoint_mode, health_check_model,
		credentials_encrypted, temporary_unavailable_continuous_probe_enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'openai', 'profile_openai_openai_v1', 'openai', 'v1',
		'km-loader-acc', 'api_key', 'openai_standard', 'active', 1, 'chat_json', 'gpt-test', ?, 1,
		'2026-10-08T00:00:00.000Z', '2026-10-08T00:00:00.000Z')`,
		accountID, systemID, configRevID, revisionID, credentials)
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, account_authorization_id, created_at, updated_at)
		VALUES (?, ?, ?, 1, NULL, '2026-10-08T00:00:00.000Z', '2026-10-08T00:00:00.000Z')`, groupID, systemID, accountID)
	seed(`INSERT INTO account_model_mappings (account_id, provider_code, source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled, created_at, updated_at)
		VALUES (?, 'openai', 'gpt-test', 'chat_completions', 'gpt-upstream', 'chat_completions', 1, '2026-10-08T00:00:00.000Z', '2026-10-08T00:00:00.000Z')`, accountID)

	loader, err := newChainKeyModelProbeInputLoader(db, false, secret)
	if err != nil {
		t.Fatalf("newChainKeyModelProbeInputLoader: %v", err)
	}
	if err := loader.CheckContract(context.Background()); err != nil {
		t.Fatalf("CheckContract: %v", err)
	}
	input, err := loader.LoadAccount(context.Background(), accountID)
	if err != nil {
		t.Fatalf("LoadAccount: %v", err)
	}
	if input.AccountID != accountID {
		t.Fatalf("AccountID = %q, want %q", input.AccountID, accountID)
	}
	if input.DispatchRevision != revisionID || input.ConfigRevision != configRevID {
		t.Fatalf("revision fence = (dispatch %d, config %d), want (%d, %d)", input.DispatchRevision, input.ConfigRevision, revisionID, configRevID)
	}
	if input.Type != "api_key" {
		t.Fatalf("Type = %q, want api_key", input.Type)
	}
	if input.Provider != "openai" || input.ProtocolProfileID != "profile_openai_openai_v1" {
		t.Fatalf("provider/profile = (%q, %q)", input.Provider, input.ProtocolProfileID)
	}
	if input.BaseURL != baseURL {
		t.Fatalf("BaseURL = %q, want %q", input.BaseURL, baseURL)
	}
	if input.EndpointMode != "chat_json" || input.HealthModel != "gpt-test" {
		t.Fatalf("探针路由 = (%q, %q), want (chat_json, gpt-test)", input.EndpointMode, input.HealthModel)
	}
	if !input.Eligibility.BoundGroup {
		t.Fatal("启用绑定的账户 Eligibility.BoundGroup 必须为 true（ToInput 契约）")
	}
	if !input.ExpiresAt.After(time.Now()) {
		t.Fatalf("ExpiresAt 必须在当前时间之后, got %v", input.ExpiresAt)
	}
	if len(input.APIKeys) != 1 {
		t.Fatalf("APIKeys = %d 把, want 1", len(input.APIKeys))
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(apiKey))
	wantFingerprint := hex.EncodeToString(mac.Sum(nil))
	if input.APIKeys[0].Fingerprint != wantFingerprint {
		t.Fatalf("Key 指纹 = %q, want HMAC-SHA256(secret,key) = %q", input.APIKeys[0].Fingerprint, wantFingerprint)
	}
	plaintext, err := exactkeyprobe.DecryptV1Envelope(secret, input.APIKeys[0].Credential.Ciphertext)
	if err != nil {
		t.Fatalf("解密 Key 封套: %v", err)
	}
	if !strings.Contains(string(plaintext), apiKey) {
		t.Fatalf("封套明文不含原 Key: %s", plaintext)
	}
}

// 委托账户分支（复审建议②）：authorization instance 账户行经
// authorization_instance_source_account_id / authorization_instance_authorization_id
// 触发 loader SQL 的委托投影——
//   - LEFT JOIN accounts source（deleted_at IS NULL）、LEFT JOIN
//     resource_authorizations ra；
//   - 凭据/base_url/协议/provider 取 source 行（ToInput effective 覆盖）；
//   - mapping 子查询 CASE 走 source.id + source.provider_code（hybrid profile
//     消费映射 → HealthModel = 映射后 upstream model）；
//   - 代理 join 的 CASE 取 source.proxy_profile_id；
//   - binding 子查询要求 ga.account_authorization_id = 授权 ID；
//   - cooldown fence 列 + sourceID.Valid 臂 → fence.SourceConfigRevision 填
//     source.config_revision（validCooldownFence 匹配面）。
// 负臂（下方 DelegatedBindingMismatch）：绑定授权失配 → Binding.Enabled=
// false → ToInput fail-closed 报错 → 适配器 unknown——列名/谓词漂移在两个
// 方向都会被真库抓住。
func TestChainKeyModelProbeInputLoaderSQLiteDelegated(t *testing.T) {
	db, secret := newKeyModelRecoveryLoaderDB(t)
	const (
		instanceID       = "km-del-acc"
		sourceID         = "km-src-acc"
		instanceSystemID = "sys_del"
		sourceSystemID   = "sys_src"
		authID           = "auth-del-1"
		groupID          = "grp_del"
		proxyID          = "proxy-del-1"
		instanceKey      = "sk-instance-key"
		sourceKey        = "sk-source-key"
		baseURL          = "https://src.example/v1"
		sourceConfigRev  = int64(7)
		dispatchRev      = int64(5)
		configRev        = int64(2)
	)
	instanceCredentials, err := exactkeyprobe.EncryptV1Envelope(secret, []byte(`{"api_key":"`+instanceKey+`","base_url":"https://instance.example/v1"}`))
	if err != nil {
		t.Fatalf("encrypt instance credentials: %v", err)
	}
	sourceCredentials, err := exactkeyprobe.EncryptV1Envelope(secret, []byte(`{"api_key":"`+sourceKey+`","base_url":"`+baseURL+`"}`))
	if err != nil {
		t.Fatalf("encrypt source credentials: %v", err)
	}
	// directProxyEnvelope 契约：username 非空时 password_encrypted 必须是
	// {"password":...} 封套，解密后拼进代理 URL 的 userinfo。
	proxyPassword, err := exactkeyprobe.EncryptV1Envelope(secret, []byte(`{"password":"proxy-pass"}`))
	if err != nil {
		t.Fatalf("encrypt proxy password: %v", err)
	}
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed row: %v: %v", query, err)
		}
	}
	// 授权实例行：rate_limited + 已过期的 cooldown_until + 完整 fence 列
	//（恢复探针的典型到期场景；ToInput 对 rate_limited 强制 validCooldownFence）。
	// provider 故意用非 hybrid 的合法 profile：effective 走 source 覆盖不受
	// 影响，而 mapping 子查询 CASE 误取 a.provider_code 时映射落空（HealthModel
	// 回退 gpt-test），provider_code 半边也可独立失败。
	seed(`INSERT INTO accounts (id, system_account_id, config_revision, dispatch_revision, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
		name, type, client_compatibility, status, schedulable, health_check_endpoint_mode, health_check_model,
		credentials_encrypted, temporary_unavailable_continuous_probe_enabled,
		cooldown_until, cooldown_retest_observation_started_at, cooldown_retest_generation,
		authorization_instance_source_account_id, authorization_instance_authorization_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'openai', 'profile_openai_openai_v1', 'openai', 'v1',
		'del-instance', 'api_key', 'openai_standard', 'rate_limited', 1, 'chat_json', 'gpt-test', ?, 1,
		'2026-10-07T00:00:00.000Z', '2026-10-07T12:00:00.000Z', 'gen-del-1',
		?, ?, '2026-10-01T00:00:00.000Z', '2026-10-01T00:00:00.000Z')`,
		instanceID, instanceSystemID, configRev, dispatchRev, instanceCredentials, sourceID, authID)
	// 物理来源行：active + 可调度 + 独立凭据（ToInput effective 取此行）+ 委托代理。
	seed(`INSERT INTO accounts (id, system_account_id, config_revision, dispatch_revision, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
		name, type, client_compatibility, status, schedulable, health_check_endpoint_mode, health_check_model,
		credentials_encrypted, proxy_profile_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'hybrid', 'profile_hybrid_openai_chat_v1', 'openai', 'v1',
		'src-physical', 'api_key', 'openai_standard', 'active', 1, 'chat_json', 'gpt-src', ?, ?,
		'2026-10-01T00:00:00.000Z', '2026-10-01T00:00:00.000Z')`,
		sourceID, sourceSystemID, sourceConfigRev, 3, sourceCredentials, proxyID)
	seed(`INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, status, created_by, created_at, updated_at)
		VALUES (?, 'ai_account', ?, ?, ?, 'active', 'admin', '2026-10-01T00:00:00.000Z', '2026-10-01T00:00:00.000Z')`,
		authID, sourceID, sourceSystemID, instanceSystemID)
	// 委托绑定：account_authorization_id 必须等于授权 ID（loader SQL 谓词）。
	seed(`INSERT INTO group_accounts (system_account_id, group_id, account_id, account_authorization_id, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, '2026-10-01T00:00:00.000Z', '2026-10-01T00:00:00.000Z')`,
		instanceSystemID, groupID, instanceID, authID)
	// 映射挂 source.id + source.provider_code（委托 CASE 分支）。
	seed(`INSERT INTO account_model_mappings (account_id, provider_code, source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled, created_at, updated_at)
		VALUES (?, 'hybrid', 'gpt-test', 'chat_completions', 'gpt-upstream', 'chat_completions', 1, '2026-10-01T00:00:00.000Z', '2026-10-01T00:00:00.000Z')`, sourceID)
	seed(`INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, username, password_encrypted, enabled, created_at, updated_at)
		VALUES (?, ?, 'del-proxy', 'http', 'proxy.example', 8080, 'proxy-user', ?, 1, '2026-10-01T00:00:00.000Z', '2026-10-01T00:00:00.000Z')`,
		proxyID, sourceSystemID, proxyPassword)

	loader, err := newChainKeyModelProbeInputLoader(db, false, secret)
	if err != nil {
		t.Fatalf("newChainKeyModelProbeInputLoader: %v", err)
	}
	input, err := loader.LoadAccount(context.Background(), instanceID)
	if err != nil {
		t.Fatalf("LoadAccount(委托账户): %v", err)
	}
	if input.AccountID != instanceID {
		t.Fatalf("AccountID = %q, want %q", input.AccountID, instanceID)
	}
	if input.DispatchRevision != dispatchRev || input.ConfigRevision != configRev {
		t.Fatalf("revision fence = (dispatch %d, config %d), want (%d, %d)", input.DispatchRevision, input.ConfigRevision, dispatchRev, configRev)
	}
	// 映射模型：hybrid profile 消费 source.id 的映射（gpt-test → gpt-upstream）。
	if input.EndpointMode != "chat_json" || input.HealthModel != "gpt-upstream" {
		t.Fatalf("探针路由 = (%q, %q), want (chat_json, gpt-upstream)", input.EndpointMode, input.HealthModel)
	}
	if input.Provider != "openai" || input.ProtocolProfileID != "profile_hybrid_openai_chat_v1" {
		t.Fatalf("provider/profile = (%q, %q)", input.Provider, input.ProtocolProfileID)
	}
	// 凭据取 source 行（instance 凭据列非空但不得被消费）。
	if len(input.APIKeys) != 1 {
		t.Fatalf("APIKeys = %d 把, want 1", len(input.APIKeys))
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(sourceKey))
	wantFingerprint := hex.EncodeToString(mac.Sum(nil))
	if input.APIKeys[0].Fingerprint != wantFingerprint {
		t.Fatalf("Key 指纹 = %q, want HMAC(secret, source key) = %q", input.APIKeys[0].Fingerprint, wantFingerprint)
	}
	plaintext, err := exactkeyprobe.DecryptV1Envelope(secret, input.APIKeys[0].Credential.Ciphertext)
	if err != nil {
		t.Fatalf("解密 Key 封套: %v", err)
	}
	if !strings.Contains(string(plaintext), sourceKey) {
		t.Fatalf("封套明文不含 source Key: %s", plaintext)
	}
	if input.BaseURL != baseURL {
		t.Fatalf("BaseURL = %q, want %q（source 凭据）", input.BaseURL, baseURL)
	}
	// 代理 envelope：委托 CASE 取 source.proxy_profile_id；封套 Kind=proxy_url，
	// 解密回读 URL（含 userinfo 与 host:port）验证端到端代理语义。
	if input.Proxy == nil {
		t.Fatal("委托账户必须产出 source 行的代理 envelope")
	}
	if input.Proxy.Kind != "proxy_url" {
		t.Fatalf("代理封套 Kind = %q, want proxy_url", input.Proxy.Kind)
	}
	proxyPlain, err := exactkeyprobe.DecryptV1Envelope(secret, input.Proxy.Ciphertext)
	if err != nil {
		t.Fatalf("解密代理封套: %v", err)
	}
	proxyURL := string(proxyPlain)
	if !strings.Contains(proxyURL, "http://proxy-user:proxy-pass@proxy.example:8080") {
		t.Fatalf("代理封套 URL = %s, want 含 http://proxy-user:proxy-pass@proxy.example:8080", proxyURL)
	}
	// fence 值：fence 列 + sourceID.Valid 臂 → SourceConfigRevision = source 行。
	if input.Cooldown == nil {
		t.Fatal("rate_limited 账户必须保留 cooldown fence")
	}
	if input.Cooldown.Generation != "gen-del-1" {
		t.Fatalf("fence generation = %q, want gen-del-1", input.Cooldown.Generation)
	}
	if !input.Cooldown.ObservationStartedAt.Equal(time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("fence observation = %v", input.Cooldown.ObservationStartedAt)
	}
	if input.Cooldown.SourceConfigRevision == nil || *input.Cooldown.SourceConfigRevision != sourceConfigRev {
		t.Fatalf("fence SourceConfigRevision = %v, want %d", input.Cooldown.SourceConfigRevision, sourceConfigRev)
	}
	if input.Eligibility.SourceConfigRevision == nil || *input.Eligibility.SourceConfigRevision != sourceConfigRev {
		t.Fatalf("Eligibility.SourceConfigRevision = %v, want %d", input.Eligibility.SourceConfigRevision, sourceConfigRev)
	}
	if !input.Eligibility.BoundGroup {
		t.Fatal("委托绑定账户 Eligibility.BoundGroup 必须为 true（ToInput 契约）")
	}
}

// 委托负臂：group_accounts.account_authorization_id 与授权实例授权 ID 失配 →
// binding 子查询谓词失配 → Binding.Enabled=false → ToInput fail-closed 报错
//（消费方适配器按 unknown 中性）。
func TestChainKeyModelProbeInputLoaderDelegatedBindingMismatch(t *testing.T) {
	db, secret := newKeyModelRecoveryLoaderDB(t)
	const (
		instanceID       = "km-del-acc"
		sourceID         = "km-src-acc"
		instanceSystemID = "sys_del"
		sourceSystemID   = "sys_src"
		authID           = "auth-del-1"
	)
	instanceCredentials, err := exactkeyprobe.EncryptV1Envelope(secret, []byte(`{"api_key":"sk-instance-key","base_url":"https://instance.example/v1"}`))
	if err != nil {
		t.Fatalf("encrypt instance credentials: %v", err)
	}
	sourceCredentials, err := exactkeyprobe.EncryptV1Envelope(secret, []byte(`{"api_key":"sk-source-key","base_url":"https://src.example/v1"}`))
	if err != nil {
		t.Fatalf("encrypt source credentials: %v", err)
	}
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed row: %v: %v", query, err)
		}
	}
	seed(`INSERT INTO accounts (id, system_account_id, config_revision, dispatch_revision, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
		name, type, client_compatibility, status, schedulable, health_check_endpoint_mode, health_check_model,
		credentials_encrypted, authorization_instance_source_account_id, authorization_instance_authorization_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'hybrid', 'profile_hybrid_openai_chat_v1', 'openai', 'v1',
		'del-instance', 'api_key', 'openai_standard', 'active', 1, 'chat_json', 'gpt-test', ?, ?, ?,
		'2026-10-01T00:00:00.000Z', '2026-10-01T00:00:00.000Z')`,
		instanceID, instanceSystemID, 2, 5, instanceCredentials, sourceID, authID)
	seed(`INSERT INTO accounts (id, system_account_id, config_revision, dispatch_revision, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
		name, type, client_compatibility, status, schedulable, health_check_endpoint_mode, health_check_model,
		credentials_encrypted, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'hybrid', 'profile_hybrid_openai_chat_v1', 'openai', 'v1',
		'src-physical', 'api_key', 'openai_standard', 'active', 1, 'chat_json', 'gpt-src', ?,
		'2026-10-01T00:00:00.000Z', '2026-10-01T00:00:00.000Z')`,
		sourceID, sourceSystemID, 7, 3, sourceCredentials)
	seed(`INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, status, created_by, created_at, updated_at)
		VALUES (?, 'ai_account', ?, ?, ?, 'active', 'admin', '2026-10-01T00:00:00.000Z', '2026-10-01T00:00:00.000Z')`,
		authID, sourceID, sourceSystemID, instanceSystemID)
	// 绑定行的授权 ID 刻意失配（auth-other ≠ auth-del-1）。
	seed(`INSERT INTO group_accounts (system_account_id, group_id, account_id, account_authorization_id, enabled, created_at, updated_at)
		VALUES (?, 'grp_del', ?, 'auth-other', 1, '2026-10-01T00:00:00.000Z', '2026-10-01T00:00:00.000Z')`,
		instanceSystemID, instanceID)

	loader, err := newChainKeyModelProbeInputLoader(db, false, secret)
	if err != nil {
		t.Fatalf("newChainKeyModelProbeInputLoader: %v", err)
	}
	if _, err := loader.LoadAccount(context.Background(), instanceID); err == nil {
		t.Fatal("绑定授权失配必须 fail-closed 报错（消费方按 unknown 中性处理）")
	}
}

func TestChainKeyModelProbeInputLoaderMissingAccount(t *testing.T) {
	db, secret := newKeyModelRecoveryLoaderDB(t)
	loader, err := newChainKeyModelProbeInputLoader(db, false, secret)
	if err != nil {
		t.Fatalf("newChainKeyModelProbeInputLoader: %v", err)
	}
	if _, err := loader.LoadAccount(context.Background(), "missing-acc"); err == nil {
		t.Fatal("账户不存在必须报错（消费方按 unknown 中性处理）")
	}
}

func TestChainKeyModelProbeInputLoaderInvalidConfig(t *testing.T) {
	if _, err := newChainKeyModelProbeInputLoader(nil, false, "secret"); err == nil {
		t.Fatal("缺业务库句柄必须报错")
	}
	db, _ := newKeyModelRecoveryLoaderDB(t)
	if _, err := newChainKeyModelProbeInputLoader(db, false, "  "); err == nil {
		t.Fatal("缺 secret 必须报错")
	}
}

// PG 方言改写：至少保证 bind/table 的编译与语义覆盖（占位符 ?→$n、schema
// 前缀）；真实 PG 执行由生产单机形态覆盖（本项目无 cmd 级 PG 测试基建）。
func TestChainKeyModelProbeInputLoaderPGDialectBind(t *testing.T) {
	db, _ := newKeyModelRecoveryLoaderDB(t)
	loader, err := newChainKeyModelProbeInputLoader(db, true, "secret")
	if err != nil {
		t.Fatalf("newChainKeyModelProbeInputLoader: %v", err)
	}
	if got := loader.table("accounts"); got != "juhe_business.accounts" {
		t.Fatalf("table = %q, want juhe_business.accounts", got)
	}
	bound := loader.bind("SELECT 1 FROM x WHERE a = ? AND b = ? LIMIT 0")
	if bound != "SELECT 1 FROM x WHERE a = $1 AND b = $2 LIMIT 0" {
		t.Fatalf("bind = %q", bound)
	}
	query := loader.bind("SELECT * FROM x WHERE id = ?")
	if !strings.Contains(query, "$1") || strings.Contains(query, "?") {
		t.Fatalf("PG 占位符改写不完整: %q", query)
	}
}

// ---- 探针适配器（stub 输入源隔离，jobs executeProbe 语义） ----

type stubKeyModelProbeInputSource struct {
	input exactkeyprobe.Input
	err   error
}

func (s stubKeyModelProbeInputSource) LoadAccount(ctx context.Context, accountID string) (exactkeyprobe.Input, error) {
	if s.err != nil {
		return exactkeyprobe.Input{}, s.err
	}
	return s.input, nil
}

func keyModelRecoveryProbeState() gatewayaccounteffects.KeyModelState {
	return gatewayaccounteffects.KeyModelState{
		CapabilityKey: gatewayaccounteffects.CapabilityKey{
			CredentialSourceAccountID: "km-probe-acc",
			KeyFingerprint:            "fp",
			ClientModel:               "gpt-test",
			ClientEndpointFamily:      "chat_completions",
			FinalUpstreamModel:        "gpt-upstream",
			UpstreamEndpointMode:      "chat_json",
			DispatchRevision:          5,
		},
	}
}

// fence 不匹配（账户行 dispatch_revision 已变）→ unknown 中性。
func TestChainKeyModelRecoveryProbeRevisionMismatchNeutral(t *testing.T) {
	probe := chainKeyModelRecoveryProbe{loader: stubKeyModelProbeInputSource{input: exactkeyprobe.Input{AccountID: "km-probe-acc", DispatchRevision: 9}}, secret: "s"}
	outcome := probe.Probe(gatewayaccounteffects.KeyModelRecoveryProbeInput{State: keyModelRecoveryProbeState(), Ctx: context.Background()})
	if outcome != gatewayaccounteffects.KeyModelOutcomeUnknown {
		t.Fatalf("revision mismatch outcome = %q, want unknown", outcome)
	}
}

// 账户加载失败 → unknown（不中断恢复扫描）。
func TestChainKeyModelRecoveryProbeLoadFailureNeutral(t *testing.T) {
	probe := chainKeyModelRecoveryProbe{loader: stubKeyModelProbeInputSource{err: errors.New("db down")}, secret: "s"}
	outcome := probe.Probe(gatewayaccounteffects.KeyModelRecoveryProbeInput{State: keyModelRecoveryProbeState(), Ctx: context.Background()})
	if outcome != gatewayaccounteffects.KeyModelOutcomeUnknown {
		t.Fatalf("load failure outcome = %q, want unknown", outcome)
	}
}

// 加载失败告警按账户 30s 节流（契约源 jobs runner.go warnInputLoadFailed：
// 首条告警、窗口内静默、窗口过期再告警、不同账户互不影响；注入 clock 不
// 依赖真实 sleep）。outcome 始终 unknown，节流只治理日志量。
func TestChainKeyModelRecoveryProbeLoadFailureWarnThrottle(t *testing.T) {
	logs := captureSlogWarnings(t)
	current := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	probe := chainKeyModelRecoveryProbe{
		loader: stubKeyModelProbeInputSource{err: errors.New("db down")},
		secret: "s",
		now:    func() time.Time { return current },
	}
	countWarns := func() int {
		return strings.Count(logs.String(), "event=gateway_key_model_recovery_input_load_failed")
	}
	runProbe := func(accountID string) gatewayaccounteffects.KeyModelOutcome {
		state := keyModelRecoveryProbeState()
		state.CapabilityKey.CredentialSourceAccountID = accountID
		return probe.Probe(gatewayaccounteffects.KeyModelRecoveryProbeInput{State: state, Ctx: context.Background()})
	}
	const accountID = "km-probe-acc"

	// 首次失败 → 告警一条，outcome unknown。
	if outcome := runProbe(accountID); outcome != gatewayaccounteffects.KeyModelOutcomeUnknown {
		t.Fatalf("load failure outcome = %q, want unknown", outcome)
	}
	if got := countWarns(); got != 1 {
		t.Fatalf("首条失败后告警数 = %d, want 1", got)
	}
	// 30s 窗口内同账户再失败 → 静默（仍 1 条）。
	current = current.Add(29 * time.Second)
	runProbe(accountID)
	if got := countWarns(); got != 1 {
		t.Fatalf("窗口内重复失败后告警数 = %d, want 1（静默）", got)
	}
	// 窗口过期（>30s）→ 再次告警。
	current = current.Add(31 * time.Second)
	runProbe(accountID)
	if got := countWarns(); got != 2 {
		t.Fatalf("窗口过期后告警数 = %d, want 2", got)
	}
	// 不同账户互不影响 → 立即告警。
	runProbe("km-other-acc")
	if got := countWarns(); got != 3 {
		t.Fatalf("其他账户首条失败后告警数 = %d, want 3", got)
	}
	// 同一轮里再次确认另一账户也进入自己的节流窗。
	current = current.Add(10 * time.Second)
	runProbe("km-other-acc")
	if got := countWarns(); got != 3 {
		t.Fatalf("其他账户窗口内重复失败后告警数 = %d, want 3", got)
	}
}

// ctx 已取消（租约丢失/进程退出）→ unknown。
func TestChainKeyModelRecoveryProbeCanceledNeutral(t *testing.T) {
	probe := chainKeyModelRecoveryProbe{loader: stubKeyModelProbeInputSource{}, secret: "s"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	outcome := probe.Probe(gatewayaccounteffects.KeyModelRecoveryProbeInput{State: keyModelRecoveryProbeState(), Ctx: ctx})
	if outcome != gatewayaccounteffects.KeyModelOutcomeUnknown {
		t.Fatalf("canceled outcome = %q, want unknown", outcome)
	}
}

// jobs defaultProbe 三分支 outcome 映射（逐分支对照）。
func TestChainKeyModelOutcomeFromProbeResult(t *testing.T) {
	cases := []struct {
		outcome string
		want    gatewayaccounteffects.KeyModelOutcome
	}{
		{exactkeyprobe.OutcomeSuccess, gatewayaccounteffects.KeyModelOutcomeCompleteSuccess},
		{exactkeyprobe.OutcomeTaskFailed, gatewayaccounteffects.KeyModelOutcomeUnknown},
		{exactkeyprobe.OutcomeNeutral, gatewayaccounteffects.KeyModelOutcomeUpstreamNotComplete},
		{exactkeyprobe.OutcomeUpstreamFailed, gatewayaccounteffects.KeyModelOutcomeUpstreamNotComplete},
	}
	for _, testCase := range cases {
		if got := chainKeyModelOutcomeFromProbeResult(exactkeyprobe.ProbeResult{Outcome: testCase.outcome}); got != testCase.want {
			t.Fatalf("outcome %q → %q, want %q", testCase.outcome, got, testCase.want)
		}
	}
}
