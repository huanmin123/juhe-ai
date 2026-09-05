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
		"providers", "provider_model_catalog", "protocols", "protocol_endpoint_families",
		"provider_protocol_profiles", "provider_protocol_profile_families",
		"groups", "route_strategies", "route_strategy_groups", "api_keys",
		"external_integration_sources", "external_integration_source_tokens", "system_settings",
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
	if first.ModelCatalogRows != 106 {
		t.Fatalf("first seed model catalog rows = %d, want 106", first.ModelCatalogRows)
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
	var passwordHash string
	if err := db.QueryRowContext(ctx, "SELECT password_hash FROM system_accounts WHERE id = 'sys_admin' AND username = 'admin' AND role = 'super_admin' AND status = 'active'").Scan(&passwordHash); err != nil {
		t.Fatalf("load seeded admin: %v", err)
	}
	if err := verifySeedTestPassword("admin", passwordHash); err != nil {
		t.Fatalf("seeded admin password does not verify: %v", err)
	}

	// Key row counts (Node seedDefaults contract).
	expectCounts := map[string]int{
		"global_settings":                     2,
		"request_quota_hourly_window_configs": 8,
		"providers":                           8,
		"protocols":                           3,
		"protocol_endpoint_families":          10,
		"provider_protocol_profiles":          13,
		"provider_protocol_profile_families":  30,
		"groups":                              8,
		"route_strategies":                    7,
		"route_strategy_groups":               7,
		"api_keys":                            8,
		"external_integration_sources":        1,
		"external_integration_source_tokens":  1,
		"system_settings":                     60,
		"provider_model_catalog":              106,
	}
	for table, want := range expectCounts {
		if got := countSeedTestRows(t, db, "SELECT count(*) FROM "+table); got != want {
			t.Fatalf("table %s rows = %d, want %d", table, got, want)
		}
	}

	// Every default group is default for sys_admin, exactly one per provider.
	var nonDefaultGroups int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM groups WHERE is_default <> 1 OR system_account_id <> 'sys_admin'").Scan(&nonDefaultGroups); err != nil {
		t.Fatal(err)
	}
	if nonDefaultGroups != 0 {
		t.Fatalf("unexpected group rows: %d", nonDefaultGroups)
	}

	// The model catalog rows carry recomputable ids and no stale disables.
	if got := countSeedTestRows(t, db, "SELECT count(*) FROM provider_model_catalog WHERE status <> 'active'"); got != 0 {
		t.Fatalf("unexpected non-active model catalog rows: %d", got)
	}
	idRows, err := db.QueryContext(ctx, "SELECT id, provider_code, model FROM provider_model_catalog")
	if err != nil {
		t.Fatal(err)
	}
	defer idRows.Close()
	for idRows.Next() {
		var id, providerCode, model string
		if err := idRows.Scan(&id, &providerCode, &model); err != nil {
			t.Fatal(err)
		}
		if want := providerModelCatalogID(providerCode, model); id != want {
			t.Fatalf("catalog id mismatch for %s/%s: %s != %s", providerCode, model, id, want)
		}
	}
	if err := idRows.Err(); err != nil {
		t.Fatal(err)
	}

	// API key envelopes: decrypt, re-hash and compare prefixes/suffixes.
	verifySeedTestAPIKeys(t, db)
	verifySeedTestExternalToken(t, db)
}

// verifySeedTestPassword mirrors Node verifyPassword for the pbkdf2 envelope.
func verifySeedTestPassword(password, envelope string) error {
	parts := strings.Split(envelope, "$")
	if len(parts) != 5 || parts[0] != "pbkdf2" || parts[1] != "sha512" || parts[2] != "120000" {
		return fmt.Errorf("unexpected password envelope %q", envelope)
	}
	salt, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return fmt.Errorf("decode salt: %w", err)
	}
	expected, err := base64.RawURLEncoding.DecodeString(parts[4])
	if err != nil {
		return fmt.Errorf("decode digest: %w", err)
	}
	derived, err := pbkdf2.Key(sha512.New, password, salt, 120000, len(expected))
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
	if count != 8 {
		t.Fatalf("api key rows = %d, want 8", count)
	}
	// Exactly one chat-purpose key bound to the default GPT route.
	var chatKeys int
	if err := db.QueryRowContext(context.Background(), "SELECT count(*) FROM api_keys WHERE purpose = 'chat' AND id = 'key_chat_sys_admin'").Scan(&chatKeys); err != nil {
		t.Fatal(err)
	}
	if chatKeys != 1 {
		t.Fatalf("chat api key rows = %d, want 1", chatKeys)
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
