// 补齐命令测试（真实 PG，pgtestkit 每测试独立库，进程内直调 Run；测试基建
// 镜像 schedprefmigration openMigrationDB）：
//   - 空白账户三件套全补：分组/策略/绑定/Key 的权威值（is_default、mode、
//     status、命名、描述、purpose、内置调度偏好绑定、seal 信封与查找哈希）；
//   - 幂等重跑零写入；已有默认分组的账户只补缺（策略+绑定+Key）；
//   - 普通 Key（含绑非默认策略的存量 Key）不被动，默认链独立补齐；
//   - fail-closed：空 secret、内置调度偏好行缺失均零写入。
package accountdefaultresources

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const testSecret = "unit-test-envelope-secret"

var fixtureClock = time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)

// openEnsurerDB 打开一个完成 ensure 的临时业务库并预置内置调度偏好行
// （api_keys.scheduling_preference_id 的外键目标）。
func openEnsurerDB(t *testing.T, withBuiltin bool) *sql.DB {
	t.Helper()
	return pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		if _, err := schema.EnsurePostgres(ctx, db); err != nil {
			return err
		}
		if withBuiltin {
			if _, err := db.Exec(`INSERT INTO "juhe_business"."scheduling_preferences"
				(id, system_account_id, name, chat_mode, chat_speed_first_config_json, built_in, created_at, updated_at)
				VALUES ('schpref-builtin-chat-cost-first', NULL, '默认调度偏好', 'cost_first', NULL, 1, '2026-10-12T08:00:00Z', '2026-10-12T08:00:00Z')
				ON CONFLICT (id) DO NOTHING`); err != nil {
				return err
			}
		}
		return nil
	})
}

func seedAccount(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO "juhe_business"."system_accounts"
		(id, username, display_name, description, role, status, password_hash, created_at, updated_at)
		VALUES ($1, $1, $1, NULL, 'user', 'active', 'hash', '2026-06-01T00:00:00Z', '2026-06-01T00:00:00Z')`, id); err != nil {
		t.Fatalf("seed account %s: %v", id, err)
	}
}

// seedDefaultGroup 预置一个默认分组（存量账户「只有分组」形态）。
func seedDefaultGroup(t *testing.T, db *sql.DB, accountID, groupID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO "juhe_business"."groups"
		(id, system_account_id, name, description, enabled, is_default, created_at, updated_at)
		VALUES ($1, $2, '默认分组', '', 1, 1, '2026-06-01T00:00:00Z', '2026-06-01T00:00:00Z')`, groupID, accountID); err != nil {
		t.Fatalf("seed default group: %v", err)
	}
}

// seedOrdinaryKey 预置一条普通 Key（绑普通策略，模拟用户自建 Key）。
func seedOrdinaryKey(t *testing.T, db *sql.DB, accountID, strategyID, keyID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO "juhe_business"."route_strategies"
		(id, system_account_id, name, description, mode, status, is_default, config_json, created_at, updated_at)
		VALUES ($1, $2, '自建路由', NULL, 'normal', 'active', 0, NULL, '2026-06-02T00:00:00Z', '2026-06-02T00:00:00Z')`, strategyID, accountID); err != nil {
		t.Fatalf("seed ordinary strategy: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO "juhe_business"."api_keys"
		(id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix, key_secret_encrypted, status, is_default, purpose, scheduling_preference_id, created_at, updated_at)
		VALUES ($1, $2, $3, '自建', NULL, 'ordinary-hash', 'sk-ordi', 'nary-end', 'v1:x:y:z', 'active', 0, 'general', 'schpref-builtin-chat-cost-first', '2026-06-02T00:00:00Z', '2026-06-02T00:00:00Z')`, keyID, accountID, strategyID); err != nil {
		t.Fatalf("seed ordinary key: %v", err)
	}
}

func runOnce(t *testing.T, db *sql.DB) Report {
	t.Helper()
	report, err := Run(context.Background(), db, Options{Secret: testSecret, Now: fixtureClock})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return report
}

func queryCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return count
}

func TestRunBackfillsFullDefaultFamilyForBareAccount(t *testing.T) {
	db := openEnsurerDB(t, true)
	defer db.Close()
	seedAccount(t, db, "acc-bare")

	report := runOnce(t, db)
	if report.AccountsScanned != 1 || report.AccountsTouched != 1 {
		t.Fatalf("scanned=%d touched=%d", report.AccountsScanned, report.AccountsTouched)
	}
	if report.GroupsEnsured != 1 || report.StrategiesEnsured != 1 || report.BindingsEnsured != 1 || report.KeysEnsured != 1 {
		t.Fatalf("report counts: %+v", report)
	}

	var groupEnabled, groupDefault int
	if err := db.QueryRow(`SELECT enabled, is_default FROM "juhe_business"."groups" WHERE system_account_id='acc-bare'`).Scan(&groupEnabled, &groupDefault); err != nil {
		t.Fatalf("group row: %v", err)
	}
	if groupEnabled != 1 || groupDefault != 1 {
		t.Fatalf("group enabled=%d default=%d", groupEnabled, groupDefault)
	}

	var strategyID, strategyName, strategyMode, strategyStatus string
	var strategyDefault int
	if err := db.QueryRow(`SELECT id, name, mode, status, is_default FROM "juhe_business"."route_strategies" WHERE system_account_id='acc-bare'`).Scan(&strategyID, &strategyName, &strategyMode, &strategyStatus, &strategyDefault); err != nil {
		t.Fatalf("strategy row: %v", err)
	}
	if strategyName != "默认路由" || strategyMode != "normal" || strategyStatus != "active" || strategyDefault != 1 {
		t.Fatalf("strategy: name=%q mode=%q status=%q default=%d", strategyName, strategyMode, strategyStatus, strategyDefault)
	}

	var bindingStatus string
	var bindingPriority, bindingWeight int
	if err := db.QueryRow(`SELECT status, priority, weight FROM "juhe_business"."route_strategy_groups" WHERE route_strategy_id=$1`, strategyID).Scan(&bindingStatus, &bindingPriority, &bindingWeight); err != nil {
		t.Fatalf("binding row: %v", err)
	}
	if bindingStatus != "active" || bindingPriority != 1 || bindingWeight != 1 {
		t.Fatalf("binding: %q p=%d w=%d", bindingStatus, bindingPriority, bindingWeight)
	}

	var keyHash, keyPrefix, keySuffix, keySealed, keyStatus, keyPurpose, keySchedPref string
	var keyDefault int
	if err := db.QueryRow(`SELECT key_hash, key_prefix, key_suffix, key_secret_encrypted, status, purpose, scheduling_preference_id, is_default FROM "juhe_business"."api_keys" WHERE system_account_id='acc-bare'`).Scan(&keyHash, &keyPrefix, &keySuffix, &keySealed, &keyStatus, &keyPurpose, &keySchedPref, &keyDefault); err != nil {
		t.Fatalf("key row: %v", err)
	}
	if keyStatus != "active" || keyPurpose != "general" || keyDefault != 1 {
		t.Fatalf("key: status=%q purpose=%q default=%d", keyStatus, keyPurpose, keyDefault)
	}
	if keySchedPref != "schpref-builtin-chat-cost-first" {
		t.Fatalf("key scheduling_preference_id=%q", keySchedPref)
	}
	plaintext := decryptKeyEnvelope(t, testSecret, keySealed)
	if !strings.HasPrefix(plaintext, "sk-") || len(plaintext) != len("sk-")+64 {
		t.Fatalf("plaintext shape: %q", plaintext)
	}
	sum := sha256.Sum256([]byte(plaintext))
	if keyHash != hex.EncodeToString(sum[:]) {
		t.Fatalf("key_hash mismatch")
	}
	if keyPrefix != plaintext[:8] || keySuffix != plaintext[len(plaintext)-8:] {
		t.Fatalf("prefix/suffix mismatch")
	}
}

func TestRunIsIdempotentOnRerun(t *testing.T) {
	db := openEnsurerDB(t, true)
	defer db.Close()
	seedAccount(t, db, "acc-idem")

	runOnce(t, db)
	counts := func() (groups, strategies, bindings, keys int) {
		groups = queryCount(t, db, `SELECT COUNT(*) FROM "juhe_business"."groups" WHERE system_account_id='acc-idem'`)
		strategies = queryCount(t, db, `SELECT COUNT(*) FROM "juhe_business"."route_strategies" WHERE system_account_id='acc-idem'`)
		bindings = queryCount(t, db, `SELECT COUNT(*) FROM "juhe_business"."route_strategy_groups" WHERE system_account_id='acc-idem'`)
		keys = queryCount(t, db, `SELECT COUNT(*) FROM "juhe_business"."api_keys" WHERE system_account_id='acc-idem'`)
		return
	}
	beforeGroups, beforeStrategies, beforeBindings, beforeKeys := counts()
	report := runOnce(t, db)
	afterGroups, afterStrategies, afterBindings, afterKeys := counts()
	if report.AccountsTouched != 0 || report.GroupsEnsured != 0 || report.StrategiesEnsured != 0 || report.BindingsEnsured != 0 || report.KeysEnsured != 0 {
		t.Fatalf("rerun report not zero: %+v", report)
	}
	if beforeGroups != afterGroups || beforeStrategies != afterStrategies || beforeBindings != afterBindings || beforeKeys != afterKeys {
		t.Fatalf("rows changed on rerun: before=(%d,%d,%d,%d) after=(%d,%d,%d,%d)",
			beforeGroups, beforeStrategies, beforeBindings, beforeKeys,
			afterGroups, afterStrategies, afterBindings, afterKeys)
	}
}

func TestRunBackfillsOnlyMissingPieces(t *testing.T) {
	db := openEnsurerDB(t, true)
	defer db.Close()
	seedAccount(t, db, "acc-half")
	seedDefaultGroup(t, db, "acc-half", "grp-existing")

	report := runOnce(t, db)
	if report.GroupsEnsured != 0 || report.StrategiesEnsured != 1 || report.BindingsEnsured != 1 || report.KeysEnsured != 1 {
		t.Fatalf("report counts: %+v", report)
	}
	if got := queryCount(t, db, `SELECT COUNT(*) FROM "juhe_business"."groups" WHERE system_account_id='acc-half'`); got != 1 {
		t.Fatalf("group count=%d want existing only", got)
	}
}

func TestRunLeavesOrdinaryKeysUntouched(t *testing.T) {
	db := openEnsurerDB(t, true)
	defer db.Close()
	seedAccount(t, db, "acc-ordinary")
	seedDefaultGroup(t, db, "acc-ordinary", "grp-ord")
	seedOrdinaryKey(t, db, "acc-ordinary", "route_strategy-ordinary", "key-ordinary")

	report := runOnce(t, db)
	if report.KeysEnsured != 1 || report.StrategiesEnsured != 1 {
		t.Fatalf("report counts: %+v", report)
	}
	var ordinaryDefault int
	var ordinaryRoute string
	if err := db.QueryRow(`SELECT is_default, route_strategy_id FROM "juhe_business"."api_keys" WHERE id='key-ordinary'`).Scan(&ordinaryDefault, &ordinaryRoute); err != nil {
		t.Fatalf("ordinary key row: %v", err)
	}
	if ordinaryDefault != 0 || ordinaryRoute != "route_strategy-ordinary" {
		t.Fatalf("ordinary key mutated: default=%d route=%q", ordinaryDefault, ordinaryRoute)
	}
	var defaultKeyRoute string
	var defaultKeyIsDefault int
	if err := db.QueryRow(`SELECT route_strategy_id, is_default FROM "juhe_business"."api_keys" WHERE system_account_id='acc-ordinary' AND is_default=1`).Scan(&defaultKeyRoute, &defaultKeyIsDefault); err != nil {
		t.Fatalf("default key row: %v", err)
	}
	if defaultKeyIsDefault != 1 || defaultKeyRoute == "route_strategy-ordinary" {
		t.Fatalf("default key: route=%q default=%d", defaultKeyRoute, defaultKeyIsDefault)
	}
}

func TestRunFailsClosedWithoutSecret(t *testing.T) {
	db := openEnsurerDB(t, true)
	defer db.Close()
	seedAccount(t, db, "acc-nosecret")

	_, err := Run(context.Background(), db, Options{Secret: "  ", Now: fixtureClock})
	if err == nil || !strings.Contains(err.Error(), "secret") {
		t.Fatalf("want secret fail-closed error, got %v", err)
	}
	if got := queryCount(t, db, `SELECT COUNT(*) FROM "juhe_business"."groups" WHERE system_account_id='acc-nosecret'`); got != 0 {
		t.Fatalf("groups written despite fail-closed: %d", got)
	}
}

func TestRunFailsWhenBuiltinSchedulingPreferenceMissing(t *testing.T) {
	db := openEnsurerDB(t, false)
	defer db.Close()
	seedAccount(t, db, "acc-nobuiltin")

	_, err := Run(context.Background(), db, Options{Secret: testSecret, Now: fixtureClock})
	if err == nil || !strings.Contains(err.Error(), "内置调度偏好") {
		t.Fatalf("want builtin scheduling-preference error, got %v", err)
	}
	if got := queryCount(t, db, `SELECT COUNT(*) FROM "juhe_business"."groups" WHERE system_account_id='acc-nobuiltin'`); got != 0 {
		t.Fatalf("groups written despite precondition failure: %d", got)
	}
}

// decryptKeyEnvelope 镜像 v1 AES-256-GCM 信封解密（测试侧独立实现，防 seal
// 格式与 gateway 读链漂移）。
func decryptKeyEnvelope(t *testing.T, secret, sealed string) string {
	t.Helper()
	parts := strings.Split(sealed, ":")
	if len(parts) != 4 || parts[0] != "v1" {
		t.Fatalf("envelope shape: %q", sealed)
	}
	iv, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("iv: %v", err)
	}
	tag, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("tag: %v", err)
	}
	cipherText, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		t.Fatalf("ciphertext: %v", err)
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	plain, err := gcm.Open(nil, iv, append(append([]byte{}, cipherText...), tag...), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var payload struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(plain, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	return payload.Key
}
