// Tests for the SQLite seedDefaults port: fresh database -> ensure -> seed
// twice (pinned clock) -> identical rows, plus the admin-login-critical rows
// and the Node crypto envelope contracts.

package schema

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/sha512"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/schedprefdomain"

	_ "modernc.org/sqlite"
)

var sqliteSeedTestClock = time.Date(2026, 9, 4, 8, 0, 0, 123456000, time.UTC)

const sqliteSeedTestSecret = "juhe-ai-seed-test-secret"

func openSeedTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "business.sqlite3")+"?mode=rwc")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatalf("pragma foreign_keys: %v", err)
	}
	if _, err := EnsureSQLiteBusiness(context.Background(), db); err != nil {
		t.Fatalf("ensure business schema: %v", err)
	}
	return db
}

func seedTestSnapshot(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	tables := []string{
		"system_accounts", "global_settings", "request_quota_hourly_window_configs",
		"vendors", "models", "protocols", "protocol_endpoint_families",
		"groups", "route_strategies", "route_strategy_groups", "api_keys",
		"external_integration_sources", "external_integration_source_tokens", "system_settings",
		"model_check_question_bank",
	}
	snapshot := map[string]string{}
	for _, table := range tables {
		rows, err := db.QueryContext(context.Background(), "SELECT * FROM "+table+" ORDER BY rowid")
		if err != nil {
			t.Fatalf("snapshot %s: %v", table, err)
		}
		defer rows.Close()
		var builder strings.Builder
		columns, err := rows.Columns()
		if err != nil {
			t.Fatalf("snapshot %s columns: %v", table, err)
		}
		values := make([]any, len(columns))
		scan := make([]any, len(columns))
		for i := range values {
			scan[i] = &values[i]
		}
		for rows.Next() {
			if err := rows.Scan(scan...); err != nil {
				t.Fatalf("snapshot %s scan: %v", table, err)
			}
			builder.WriteString("[")
			for i, value := range values {
				switch typed := value.(type) {
				case []byte:
					builder.WriteString(string(typed))
				case nil:
					builder.WriteString("\x00N")
				default:
					builder.WriteString(fmt.Sprintf("%v", typed))
				}
				if i < len(values)-1 {
					builder.WriteString("\x1f")
				}
			}
			builder.WriteString("]\n")
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("snapshot %s iterate: %v", table, err)
		}
		snapshot[table] = builder.String()
	}
	return snapshot
}

func countSeedTestRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	return count
}

func TestSeedSQLiteDefaultsIdempotentAndComplete(t *testing.T) {
	db := openSeedTestDatabase(t)
	ctx := context.Background()
	options := SeedOptions{Now: func() time.Time { return sqliteSeedTestClock }, Secret: sqliteSeedTestSecret}

	first, err := SeedSQLiteDefaults(ctx, db, options)
	if err != nil {
		t.Fatalf("first seed: %v", err)
	}
	// models 表口径：全量快照行落库（212 行，disabled 计入）。
	if first.ModelCatalogRows != 212 {
		t.Fatalf("first seed model catalog rows = %d, want 212 (目录快照全量行数，含 shutdown 已过写 disabled 的 6 行)", first.ModelCatalogRows)
	}
	if first.BuiltinQuestionRows != len(builtinModelCheckQuestions) {
		t.Fatalf("first seed builtin question rows = %d, want %d", first.BuiltinQuestionRows, len(builtinModelCheckQuestions))
	}
	if got := countSeedTestRows(t, db, `SELECT COUNT(*) FROM model_check_question_bank WHERE is_builtin = 1 AND status = 'approved' AND created_by = 'system'`); got != len(builtinModelCheckQuestions) {
		t.Fatalf("builtin questions in db = %d, want %d", got, len(builtinModelCheckQuestions))
	}
	// 内置题题面随 seed 版本演进：人工改动内置行后重跑 seed，题面字段恢复
	// 为 seed 权威值（upsert WHERE is_builtin=1），updated_at 保持不变。
	if _, err := db.Exec(`UPDATE model_check_question_bank SET question_text = '人工改动后的题面', updated_at = '2099-01-01T00:00:00Z' WHERE id = ?`, builtinModelCheckQuestions[0].ID); err != nil {
		t.Fatal(err)
	}
	third, err := SeedSQLiteDefaults(ctx, db, options)
	if err != nil {
		t.Fatalf("third seed (builtin question repair): %v", err)
	}
	if third.BuiltinQuestionRows != len(builtinModelCheckQuestions) {
		t.Fatalf("third seed builtin question rows = %d", third.BuiltinQuestionRows)
	}
	var repairedText, repairedUpdatedAt string
	if err := db.QueryRowContext(ctx, `SELECT question_text, updated_at FROM model_check_question_bank WHERE id = ?`, builtinModelCheckQuestions[0].ID).Scan(&repairedText, &repairedUpdatedAt); err != nil {
		t.Fatal(err)
	}
	if repairedText != builtinModelCheckQuestions[0].QuestionText {
		t.Fatalf("builtin question text not repaired = %q", repairedText)
	}
	if repairedUpdatedAt != "2099-01-01T00:00:00Z" {
		t.Fatalf("repair must not touch updated_at, got %q", repairedUpdatedAt)
	}
	snapshotAfterFirst := seedTestSnapshot(t, db)

	second, err := SeedSQLiteDefaults(ctx, db, options)
	if err != nil {
		t.Fatalf("second seed (idempotency): %v", err)
	}
	if second.ModelCatalogRows != first.ModelCatalogRows {
		t.Fatalf("second seed model catalog rows = %d, want %d", second.ModelCatalogRows, first.ModelCatalogRows)
	}
	snapshotAfterSecond := seedTestSnapshot(t, db)
	for table, rows := range snapshotAfterFirst {
		if snapshotAfterSecond[table] != rows {
			t.Fatalf("table %s changed between seed runs:\nfirst:\n%s\nsecond:\n%s", table, rows, snapshotAfterSecond[table])
		}
	}

	// Admin login: the seeded super admin must verify the default password.
	// BUG-0224: must_change_password must seed as 1 so the first login hits
	// the must_change_password 403 gate (adjudication note on
	// pgSeedSystemAccountsInsert in pg_schema.go).
	var passwordHash string
	var mustChangePassword int
	if err := db.QueryRowContext(ctx, "SELECT password_hash, must_change_password FROM system_accounts WHERE id = 'sys_admin' AND username = 'admin' AND role = 'super_admin' AND status = 'active'").Scan(&passwordHash, &mustChangePassword); err != nil {
		t.Fatalf("load seeded admin: %v", err)
	}
	if err := verifySeedTestPassword("admin", passwordHash); err != nil {
		t.Fatalf("seeded admin password does not verify: %v", err)
	}
	if mustChangePassword != 1 {
		t.Fatalf("seeded admin must_change_password = %d, want 1 (BUG-0224: first login must be forced to change the default password)", mustChangePassword)
	}

	// Key row counts (Node seedDefaults contract; 2026-10-04 M3 第五批
	// qwen 增补 + M3f 长音频批（families 12→13 增 audio_transcription 族、
	// 目录 136→138 增 wan2.2-t2v-plus 与 paraformer-v2；M5b 批 gpt-realtime
	// 再 +1 → 139；M6 语音批 volcengine 档案 +tts 绑定 37→38、目录 +cogtts
	// → 140；M3 回填池 xai 视频 grok-imagine-video-1.5 → 141；对话批
	// volcengine/qwen 档案 +chat_completions 绑定 38→40，复用既有
	// openai_v1_chat_completions 族不新增 family 行；2026-10-05 全厂商补全批
	// 起 141→221（补全批 208、复核批 213、占位行批 214、取证批 217、遗留
	// 取证批 live-translate 218 + openai 三行 221）；TTS 迁移批 +seed-audio-1.0、
	// JSON 生成器修正 -deepseek-v4-flash 残留行 → 221；2026-10-17 口径核对批
	// 五家回正（行数不变）+ 2025 前模型清理批两阶段 -15 行 gpt 旧行 -9 行 →
	// 200（同日缺口补录批 +3）；按种子钟 2026-09-04 过滤 6 行已 shutdown 行 → 194；
	// 同日火山 ASR 承接批 volc.seedasr.auc +1 → 201 / 可见 195；同日缺口补录
	// 第二批 +8（minimax M2 家族五行 + H3-Regeneration + H3-Context-IR + xai
	// 旧版 grok-imagine-video；speech-02-hd/Music/Hailuo-02 经裁决不收录）→
	// 209；同日方舟全能力实测批 volc doubao-embedding-large-text-250515 +1 →
	// 210；同日 seededit 无价行承接批 +1 →
	// 211；同日 3D 承接批 +1 → 212 / 可见 206）后
	// providers 11 / families 13 / profiles
	// 16 / profile 绑定 41（M3f qwen +audio_transcription、M4b hybrid
	// +video_generation+tts、M6 volcengine +tts、对话批 volcengine/qwen
	// +chat_completions、ASR 承接批 volcengine +audio_transcription 复用
	// 既有 audio_transcription 族）/ groups 1（2026-10-07 默认资源收口：仅 GPT）；
	// 默认分组派生 route_strategies 与 bindings 1（仅 GPT）、api_keys 1
	//（1 默认；admin chat 不再预置，由会话创建链按需 Ensure）。
	// global_settings 2 行种子 + 1 行调度偏好迁移完成记录（PLAN-20261012T033000000Z
	// P2 设计门裁决②：fresh 空库 seed 链经 schedprefdomain.SealFreshInstall 自动
	// 封存 scheduling_preferences_migration，stage=completed、counts 全零）。
	// 供应商与上游协议面重构（设计 §1.1/§8/§9）：vendors 恰 9 行（openai/
	// hybrid 删除），profiles 两表随旧供应商维度退役不再 seed。
	expectCounts := map[string]int{
		"global_settings":                     3,
		"request_quota_hourly_window_configs": 8,
		"vendors":                             9,
		"protocols":                           3,
		"protocol_endpoint_families":          13,
		"groups":                              1,
		"route_strategies":                    1,
		"route_strategy_groups":               1,
		"api_keys":                            1,
		"external_integration_sources":        1,
		"external_integration_source_tokens":  1,
		// 72→71：chatImageGenerationTotalTimeoutSeconds 随 2026-10-12 AI 问答
		// 简化批（生图工具退场）从 seed 删除。
		// 71→67：2026-10-14 监控改造批删 systemMetrics*/tableMonitor* 四键
		//（f1991b708 实现侧已删、本计数漏改；2026-10-17 专门批取证回正）。
		// provider_model_catalog 214→203：2026-10-17 口径核对批 221 + 2025 前
		// 模型清理批两阶段 -24 行（gpt 15 + 无日期疑似 2024 老模型 8 + 弃用
		// glm-asr-2512 1）→ 197；同日目录缺口补录批 +3（Hailuo-2.3-Fast、
		// video-1.5-lite、grok-tts 占位行）→ 200；同日火山 ASR 承接批 +1
		//（volc.seedasr.auc）→ 201；同日缺口补录第二批 +8（minimax M2 家族
		// 五行 + H3-Regeneration + H3-Context-IR + xai 旧版 grok-imagine-video）
		// → 209；同日方舟全能力实测批 volc doubao-embedding-large-text-250515
		// +1 → 210；同日 seededit 无价行承接批 +1 → 211；同日 3D 承接批 +1 →
		// 212；同日全能力覆盖批终态（并发协调后合并口径）：+1 vision 向量行
		//（doubao-embedding-vision-251215，现役唯一向量模型，multimodal 面）−2
		// 官方退役死行（text-250515 Retiring 404 实证、seededit-250628 官方
		// status=Shutdown，现役图生图由 seedream 原生 image 参数承接）→ 211；同日流式语音桥接批 seed-tts-2.0 +1 → 212。
		"system_settings": 67,
		"models":          212,
	}
	for table, want := range expectCounts {
		if got := countSeedTestRows(t, db, "SELECT count(*) FROM "+table); got != want {
			t.Fatalf("table %s rows = %d, want %d", table, got, want)
		}
	}

	// 调度偏好迁移完成记录（P2 设计门裁决②）：fresh 空库 seed 链自动封存
	// stage=completed、ruleVersion 一致、counts 全零；记录落库后重复 seed 不改写。
	var recordJSON string
	if err := db.QueryRowContext(ctx, `SELECT value_json FROM global_settings WHERE key = ?`,
		schedprefdomain.MigrationSettingKey).Scan(&recordJSON); err != nil {
		t.Fatalf("fresh seed 必须封存调度偏好迁移记录: %v", err)
	}
	record, err := schedprefdomain.ParseRecord(recordJSON)
	if err != nil {
		t.Fatalf("parse scheduling preferences migration record: %v", err)
	}
	if record.Stage != schedprefdomain.StageCompleted || record.RuleVersion != schedprefdomain.RuleVersion {
		t.Fatalf("fresh seed 封存记录 stage/ruleVersion 异常: %+v", record)
	}
	if record.Counts != (schedprefdomain.Counts{}) {
		t.Fatalf("fresh seed 封存 counts 必须全零: %+v", record.Counts)
	}

	// Every default group is default for sys_admin, exactly the single GPT
	// default family (2026-10-07 默认资源收口).
	var nonDefaultGroups int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM groups WHERE is_default <> 1 OR system_account_id <> 'sys_admin'").Scan(&nonDefaultGroups); err != nil {
		t.Fatal(err)
	}
	if nonDefaultGroups != 0 {
		t.Fatalf("unexpected group rows: %d", nonDefaultGroups)
	}
	// 供应商维度删除后内置默认组名收敛为「默认分组」。
	var groupName string
	if err := db.QueryRowContext(ctx, "SELECT name FROM groups WHERE system_account_id = 'sys_admin' AND is_default = 1").Scan(&groupName); err != nil {
		t.Fatal(err)
	}
	if groupName != "默认分组" {
		t.Fatalf("built-in default group name = %q, want 默认分组", groupName)
	}

	// The models rows carry recomputable ids; the disabled subset is exactly
	// the shutdown-expired rows of the pinned clock（6 行）。
	if got := countSeedTestRows(t, db, "SELECT count(*) FROM models WHERE status <> 'active'"); got != 6 {
		t.Fatalf("disabled model rows = %d, want 6 (xai 2026-05-15 四行 + gemini veo-3.0 两行 2025-11-12 at the pinned clock)", got)
	}
	idRows, err := db.QueryContext(ctx, "SELECT id, vendor_code, upstream_model FROM models")
	if err != nil {
		t.Fatal(err)
	}
	defer idRows.Close()
	for idRows.Next() {
		var id, vendorCode, upstreamModel string
		if err := idRows.Scan(&id, &vendorCode, &upstreamModel); err != nil {
			t.Fatal(err)
		}
		if want := providerModelCatalogID(vendorCode, upstreamModel); id != want {
			t.Fatalf("model id mismatch for %s/%s: %s != %s", vendorCode, upstreamModel, id, want)
		}
	}
	if err := idRows.Err(); err != nil {
		t.Fatal(err)
	}

	// API key envelopes: decrypt, re-hash and compare prefixes/suffixes.
	verifySeedTestAPIKeys(t, db)
	verifySeedTestExternalToken(t, db)
}

// verifySeedTestPassword mirrors Node verifyPassword for the pbkdf2 envelope
// (and the Go gateway verifyNodePBKDF2Password): the base64url salt TEXT
// itself is the PBKDF2 key material, never the decoded raw salt bytes.
func verifySeedTestPassword(password, envelope string) error {
	parts := strings.Split(envelope, "$")
	if len(parts) != 5 || parts[0] != "pbkdf2" || parts[1] != "sha512" || parts[2] != "120000" {
		return fmt.Errorf("unexpected password envelope %q", envelope)
	}
	if _, err := base64.RawURLEncoding.DecodeString(parts[3]); err != nil {
		return fmt.Errorf("decode salt: %w", err)
	}
	expected, err := base64.RawURLEncoding.DecodeString(parts[4])
	if err != nil {
		return fmt.Errorf("decode digest: %w", err)
	}
	derived, err := pbkdf2.Key(sha512.New, password, []byte(parts[3]), 120000, len(expected))
	if err != nil {
		return err
	}
	if hex.EncodeToString(derived) != hex.EncodeToString(expected) {
		return errors.New("derived digest mismatch")
	}
	return nil
}

// decryptSeedTestEnvelope mirrors Node decryptJson for the v1 AES-256-GCM
// envelope (sha256(secret) key, 12-byte iv, 16-byte tag).
func decryptSeedTestEnvelope(t *testing.T, secret, envelope string) string {
	t.Helper()
	parts := strings.Split(envelope, ":")
	if len(parts) != 4 || parts[0] != "v1" {
		t.Fatalf("unexpected encryption envelope %q", envelope)
	}
	iv, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode iv: %v", err)
	}
	tag, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode tag: %v", err)
	}
	cipherText, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		t.Fatalf("decode ciphertext: %v", err)
	}
	if len(iv) != 12 || len(tag) != 16 {
		t.Fatalf("unexpected iv/tag lengths %d/%d", len(iv), len(tag))
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := gcm.Open(nil, iv, append(cipherText, tag...), nil)
	if err != nil {
		t.Fatalf("decrypt envelope: %v", err)
	}
	return string(plain)
}

func verifySeedTestAPIKeys(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), "SELECT id, purpose, key_hash, key_prefix, key_suffix, key_secret_encrypted FROM api_keys")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, purpose, keyHash, keyPrefix, keySuffix, encrypted string
		if err := rows.Scan(&id, &purpose, &keyHash, &keyPrefix, &keySuffix, &encrypted); err != nil {
			t.Fatal(err)
		}
		count++
		plain := decryptSeedTestEnvelope(t, sqliteSeedTestSecret, encrypted)
		var payload struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal([]byte(plain), &payload); err != nil {
			t.Fatalf("api key %s payload: %v", id, err)
		}
		if !strings.HasPrefix(payload.Key, "sk-") || len(payload.Key) != 3+64 {
			t.Fatalf("api key %s has unexpected key shape %q", id, payload.Key)
		}
		if got := seedHashSecret(payload.Key); got != keyHash {
			t.Fatalf("api key %s hash mismatch", id)
		}
		if keyPrefix != payload.Key[:8] || keySuffix != payload.Key[len(payload.Key)-8:] {
			t.Fatalf("api key %s prefix/suffix mismatch", id)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// 2026-10-07 默认资源收口：默认分组派生 Key 仅 GPT 1 把；
	// admin chat Key 不再预置（会话创建链按需 Ensure）。
	if count != 1 {
		t.Fatalf("api key rows = %d, want 1", count)
	}
	// No chat-purpose key may be seeded anymore.
	var chatKeys int
	if err := db.QueryRowContext(context.Background(), "SELECT count(*) FROM api_keys WHERE purpose = 'chat'").Scan(&chatKeys); err != nil {
		t.Fatal(err)
	}
	if chatKeys != 0 {
		t.Fatalf("chat api key rows = %d, want 0 (chat key provisioning moved to the session ensure chain)", chatKeys)
	}
}

func verifySeedTestExternalToken(t *testing.T, db *sql.DB) {
	t.Helper()
	var id, tokenHash, tokenPrefix, tokenSuffix, encrypted string
	if err := db.QueryRowContext(context.Background(),
		"SELECT id, token_hash, token_prefix, token_suffix, token_secret_encrypted FROM external_integration_source_tokens WHERE id = 'exttok_builtin_test'",
	).Scan(&id, &tokenHash, &tokenPrefix, &tokenSuffix, &encrypted); err != nil {
		t.Fatalf("load external integration token: %v", err)
	}
	plain := decryptSeedTestEnvelope(t, sqliteSeedTestSecret, encrypted)
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(plain), &payload); err != nil {
		t.Fatalf("token payload: %v", err)
	}
	if !strings.HasPrefix(payload.Token, "juis_") || len(payload.Token) != 5+43 {
		t.Fatalf("unexpected token shape %q", payload.Token)
	}
	if got := seedHashSecret("external-integration-source-token:" + payload.Token); got != tokenHash {
		t.Fatal("token hash mismatch")
	}
	if tokenPrefix != payload.Token[:8] || tokenSuffix != payload.Token[len(payload.Token)-8:] {
		t.Fatal("token prefix/suffix mismatch")
	}
}
