package accounthealth

// direct_input_pool_test.go 覆盖 J1 探测输入的池绑定有效目标解析（代理池设计
// §5.2/§5.3/§6.6 只读入口）：池绑定候选在只读事务内经共享 proxyresolve 解析
// 当前租约成员并冻结为输入代理信封；无有效租约/池不可用隔离为该输入失败
// （保留 suppression 重试机制），不直连、不写账户状态、不补租约。授权实例
// 取 source 池且租约键 = source account，实例自身池绑定不参与。

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
)

// directInputPoolOpenDB 在 w9h 权威 DDL 夹具上补建池域两表与租约 generation
// 序列（§4 生产形状零漂移；w9h 共享夹具表清单不含池成员/租约表，此处只对
// 本测试的隔离库追加，不改共享夹具）。
func directInputPoolOpenDB(t *testing.T) *sql.DB {
	t.Helper()
	db, _ := w9hOpenKitDB(t)
	poolDDL := w9hExtractTableDDL(t, "../../../../projects/maintenance/internal/schema/pg_schema_business_tables.go",
		[]string{"proxy_pool_members", "account_proxy_leases"})
	w9hMustExec(t, db, `CREATE SEQUENCE IF NOT EXISTS juhe_business.account_proxy_lease_generation_seq`)
	for _, table := range []string{"proxy_pool_members", "account_proxy_leases"} {
		w9hMustExec(t, db, `SET search_path = juhe_business; `+poolDDL[table])
	}
	return db
}

// directInputPoolSeedProxyPool 写入池/成员/租约夹具。密码封套与 reader secret
// 同一 v1 envelope 家族（exactkeyprobe.EncryptV1Envelope 与
// accountcrypto.DecryptJSON 互操作）。
func directInputPoolSeedProxyPool(t *testing.T, db *sql.DB, poolID, memberID string, memberEnabled, poolEnabled bool, leaseKey string) {
	t.Helper()
	sealed, err := exactkeyprobe.EncryptV1Envelope("w9h-credential-secret", []byte(`{"password":"pool-pass"}`))
	if err != nil {
		t.Fatal(err)
	}
	w9hMustExec(t, db, `INSERT INTO juhe_business.proxy_profiles (id, system_account_id, name, type, host, port, username, password_encrypted, enabled, created_at, updated_at)
		VALUES ($1, 'sys_admin', $1, 'socks5', 'member.example', 1080, 'pool-user', $2, $3, $4, $4)
		ON CONFLICT (id) DO UPDATE SET enabled = excluded.enabled, password_encrypted = excluded.password_encrypted`,
		memberID, sealed, memberEnabled, w9hFixtureNow)
	w9hMustExec(t, db, `INSERT INTO juhe_business.proxy_pools (id, system_account_id, name, strategy, enabled, created_at, updated_at)
		VALUES ($1, 'sys_admin', $1, 'round_robin', $2, $3, $3)
		ON CONFLICT (id) DO UPDATE SET enabled = excluded.enabled`, poolID, poolEnabled, w9hFixtureNow)
	w9hMustExec(t, db, `INSERT INTO juhe_business.proxy_pool_members (pool_id, proxy_profile_id, priority, weight, enabled, created_at, updated_at)
		VALUES ($1, $2, 0, 1, $3, $4, $4)
		ON CONFLICT (pool_id, proxy_profile_id) DO UPDATE SET enabled = excluded.enabled`, poolID, memberID, memberEnabled, w9hFixtureNow)
	if leaseKey != "" {
		w9hMustExec(t, db, `INSERT INTO juhe_business.account_proxy_leases (account_id, pool_id, proxy_profile_id, generation, assignment_reason, assigned_at, updated_at)
			VALUES ($1, $2, $3, 1, 'initial', $4, $4)
			ON CONFLICT (account_id) DO UPDATE SET pool_id = excluded.pool_id, proxy_profile_id = excluded.proxy_profile_id`,
			leaseKey, poolID, memberID, w9hFixtureNow)
	}
}

func directInputPoolLeaseCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM juhe_business.account_proxy_leases`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// directInputPoolInputProxyURL 解冻输入代理信封，返回 proxy URL（明文）。
func directInputPoolInputProxyURL(t *testing.T, inputProxy *exactkeyprobe.CredentialEnvelope) string {
	t.Helper()
	plaintext, err := exactkeyprobe.DecryptV1Envelope("w9h-credential-secret", inputProxy.Ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(plaintext, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.URL
}

// TestDirectInputPoolBindingResolvesLeaseMember 池绑定候选冻结当前租约成员
// 代理，且解析全程只读（不补租约）。
func TestDirectInputPoolBindingResolvesLeaseMember(t *testing.T) {
	db := directInputPoolOpenDB(t)
	ctx := context.Background()
	w9hSeedBusinessAccount(t, db, "w-pool-ok", "deepseek", "profile_deepseek_openai_v1", "api_key", "active", "chat_json", sql.NullString{}, sql.NullString{})
	directInputPoolSeedProxyPool(t, db, "w-pool", "w-pool-member", true, true, "w-pool-ok")
	w9hMustExec(t, db, `UPDATE juhe_business.accounts SET proxy_pool_id = 'w-pool' WHERE id = 'w-pool-ok'`)
	before := directInputPoolLeaseCount(t, db)

	reader, err := NewPostgresDirectInputReader(db, "w9h-credential-secret", time.Hour, func() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	result, err := reader.LoadDueWithFailures(ctx, 10)
	if err != nil {
		t.Fatalf("load 池绑定候选必须成功: %v", err)
	}
	var matched *exactkeyprobe.Input
	for index := range result.Inputs {
		if result.Inputs[index].AccountID == "w-pool-ok" {
			matched = &result.Inputs[index]
		}
	}
	if matched == nil {
		t.Fatalf("池绑定候选必须在输入中: inputs=%#v failures=%#v", result.Inputs, result.Failures)
	}
	if matched.Proxy == nil {
		t.Fatal("池绑定候选的输入必须冻结租约成员代理信封")
	}
	url := directInputPoolInputProxyURL(t, matched.Proxy)
	if !strings.Contains(url, "socks5h://pool-user:pool-pass@member.example:1080") {
		t.Fatalf("输入代理必须是租约成员 URL，得到 %s", url)
	}
	for _, failure := range result.Failures {
		if failure.AccountID == "w-pool-ok" {
			t.Fatalf("可用租约不得产生候选失败: %#v", result.Failures)
		}
	}
	if after := directInputPoolLeaseCount(t, db); after != before {
		t.Fatalf("解析不得补租约：before=%d after=%d", before, after)
	}
}

// TestDirectInputPoolUnavailableIsolatesFailure 无租约/池停用时该输入明确
// 失败（Reason 携带池级原因），不产生输入、不直连、不补租约。
func TestDirectInputPoolUnavailableIsolatesFailure(t *testing.T) {
	db := directInputPoolOpenDB(t)
	ctx := context.Background()
	w9hSeedBusinessAccount(t, db, "w-pool-nolease", "deepseek", "profile_deepseek_openai_v1", "api_key", "active", "chat_json", sql.NullString{}, sql.NullString{})
	w9hSeedBusinessAccount(t, db, "w-pool-off", "deepseek", "profile_deepseek_openai_v1", "api_key", "active", "chat_json", sql.NullString{}, sql.NullString{})
	directInputPoolSeedProxyPool(t, db, "w-pool-nl", "w-pool-nl-member", true, true, "")
	directInputPoolSeedProxyPool(t, db, "w-pool-disabled", "w-pool-off-member", true, false, "w-pool-off")
	w9hMustExec(t, db, `UPDATE juhe_business.accounts SET proxy_pool_id = 'w-pool-nl' WHERE id = 'w-pool-nolease'`)
	w9hMustExec(t, db, `UPDATE juhe_business.accounts SET proxy_pool_id = 'w-pool-disabled' WHERE id = 'w-pool-off'`)

	reader, err := NewPostgresDirectInputReader(db, "w9h-credential-secret", time.Hour, func() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	result, err := reader.LoadDueWithFailures(ctx, 10)
	if err != nil {
		t.Fatalf("load 池不可用候选必须以失败隔离而非整体读失败: %v", err)
	}
	reasons := map[string]string{}
	for _, failure := range result.Failures {
		reasons[failure.AccountID] = failure.Reason
	}
	for _, accountID := range []string{"w-pool-nolease", "w-pool-off"} {
		if _, ok := reasons[accountID]; !ok {
			t.Fatalf("池不可用账户 %s 必须产生候选失败: failures=%#v", accountID, result.Failures)
		}
		if !strings.Contains(reasons[accountID], "proxy_pool_unavailable") {
			t.Fatalf("%s 的失败原因必须携带池级原因，得到 %s", accountID, reasons[accountID])
		}
	}
	for _, input := range result.Inputs {
		if input.AccountID == "w-pool-nolease" || input.AccountID == "w-pool-off" {
			t.Fatalf("池不可用账户 %s 不得产生输入（不直连）", input.AccountID)
		}
	}
	if after := directInputPoolLeaseCount(t, db); after != 1 {
		t.Fatalf("只读解析不得写租约，租约行数=%d", after)
	}
}

// TestDirectInputPoolInstanceFollowsSourcePool 授权实例取 source 池的租约成员
// （租约键 = source account），实例自身池绑定不参与解析。
func TestDirectInputPoolInstanceFollowsSourcePool(t *testing.T) {
	db := directInputPoolOpenDB(t)
	ctx := context.Background()
	w2bSeedInstanceChain(t, db, "w-pool-team", "w-pool-src", "w-pool-auth", "w-pool-inst")
	directInputPoolSeedProxyPool(t, db, "w-pool-src", "w-pool-src-member", true, true, "w-pool-src")
	directInputPoolSeedProxyPool(t, db, "w-pool-own", "w-pool-own-member", true, true, "w-pool-inst")
	w9hMustExec(t, db, `UPDATE juhe_business.accounts SET proxy_pool_id = 'w-pool-src' WHERE id = 'w-pool-src'`)
	// 实例行误写自身池绑定：按 §5.2 不参与解析，不得覆盖 source 池结果。
	w9hMustExec(t, db, `UPDATE juhe_business.accounts SET proxy_pool_id = 'w-pool-own' WHERE id = 'w-pool-inst'`)

	reader, err := NewPostgresDirectInputReader(db, "w9h-credential-secret", time.Hour, func() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	result, err := reader.LoadDueWithFailures(ctx, 10)
	if err != nil {
		t.Fatalf("load 实例候选必须成功: %v", err)
	}
	var matched *exactkeyprobe.Input
	for index := range result.Inputs {
		if result.Inputs[index].AccountID == "w-pool-inst" {
			matched = &result.Inputs[index]
		}
	}
	if matched == nil {
		t.Fatalf("实例候选必须在输入中: inputs=%#v failures=%#v", result.Inputs, result.Failures)
	}
	if matched.Proxy == nil {
		t.Fatal("实例候选必须冻结 source 池租约成员代理信封")
	}
	url := directInputPoolInputProxyURL(t, matched.Proxy)
	if !strings.Contains(url, "socks5h://pool-user:pool-pass@member.example:1080") {
		t.Fatalf("实例输入代理必须是 source 池租约成员 URL，得到 %s", url)
	}
}

// TestDirectInputPoolDirectProfileUnchanged 直接代理候选（配置档案）保持既有
// 读面：档案行原样冻结为 DirectProxy，不经过池解析路径。
func TestDirectInputPoolDirectProfileUnchanged(t *testing.T) {
	db := directInputPoolOpenDB(t)
	ctx := context.Background()
	w9hSeedBusinessAccount(t, db, "w-pool-direct", "deepseek", "profile_deepseek_openai_v1", "api_key", "active", "chat_json", sql.NullString{}, sql.NullString{})
	sealed, err := exactkeyprobe.EncryptV1Envelope("w9h-credential-secret", []byte(`{"password":"direct-pass"}`))
	if err != nil {
		t.Fatal(err)
	}
	w9hMustExec(t, db, `INSERT INTO juhe_business.proxy_profiles (id, system_account_id, name, type, host, port, username, password_encrypted, enabled, created_at, updated_at)
		VALUES ('w-pool-direct-profile', 'sys_admin', 'w-pool-direct-profile', 'http', 'direct.example', 8080, 'direct-user', $1, true, $2, $2)`, sealed, w9hFixtureNow)
	w9hMustExec(t, db, `UPDATE juhe_business.accounts SET proxy_profile_id = 'w-pool-direct-profile' WHERE id = 'w-pool-direct'`)

	reader, err := NewPostgresDirectInputReader(db, "w9h-credential-secret", time.Hour, func() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	result, err := reader.LoadDueWithFailures(ctx, 10)
	if err != nil {
		t.Fatalf("load 直接代理候选必须成功: %v", err)
	}
	var matched *exactkeyprobe.Input
	for index := range result.Inputs {
		if result.Inputs[index].AccountID == "w-pool-direct" {
			matched = &result.Inputs[index]
		}
	}
	if matched == nil || matched.Proxy == nil {
		t.Fatalf("直接代理候选必须保持既有信封: inputs=%#v failures=%#v", result.Inputs, result.Failures)
	}
	url := directInputPoolInputProxyURL(t, matched.Proxy)
	if !strings.Contains(url, "http://direct-user:direct-pass@direct.example:8080") {
		t.Fatalf("直接代理 URL 必须保持既有语义，得到 %s", url)
	}
}
