package auditlog

// w16a 覆盖收尾第一批：不依赖外部服务的纯函数与配置边界缺口。
// 仅补齐 w16 覆盖率 profile 中 count=0 的语句块：
//   - types.go PayloadBody.UnmarshalJSON 非法对象臂、normalizeAuditInput
//     endedAt/attempt.startedAt 错误臂；
//   - store.go nodeUTF16String.MarshalJSON 的转义/代理对分支；
//   - config.go 池上限解析、business settings 同文件、usage shard 根与内嵌臂；
//   - retention.go blobFilePath/blobFileMatchesMetadata 空 key 与
//     store.go cleanupBlobTemps/cleanup*StaleBlobTemps 的平台错误臂。

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestW16aPayloadBodyRejectsUnknownObjectForm(t *testing.T) {
	var payload AuditLogPayloadInput
	if err := json.Unmarshal([]byte(`{"partType":"gateway_response","body":{"type":"NotBuffer","data":[104,105]}}`), &payload); err == nil {
		t.Fatal("非 base64/Buffer 的 body 对象必须被拒绝")
	}
	if err := json.Unmarshal([]byte(`{"partType":"gateway_response","body":{}}`), &payload); err == nil {
		t.Fatal("空 body 对象必须被拒绝")
	}
	// base64 非法臂（152-156 区域）与合法 base64 臂对照。
	if err := json.Unmarshal([]byte(`{"partType":"gateway_response","body":{"base64":"!!!"}}`), &payload); err == nil {
		t.Fatal("非法 base64 必须被拒绝")
	}
	if err := json.Unmarshal([]byte(`{"partType":"gateway_response","body":{"base64":"aGk="}}`), &payload); err != nil {
		t.Fatalf("合法 base64 必须被接受: %v", err)
	}
}

func TestW16aNormalizeAuditInputRejectsBadEndedAndAttemptStart(t *testing.T) {
	base := fixture("w16a-normalize-ended", LifecycleFinalized)
	base.EndedAt = "not-a-time"
	if _, err := normalizeAuditInput(base); err == nil || !strings.Contains(err.Error(), "endedAt") {
		t.Fatalf("非法 endedAt 必须被拒绝: %v", err)
	}
	attempted := fixture("w16a-normalize-attempt", LifecycleFinalized)
	attempted.Attempts = []AuditLogAttemptInput{{
		AttemptIndex:   0,
		UpstreamMethod: "POST",
		UpstreamURL:    "https://w16a.invalid/v1",
		StartedAt:      "2026-09-18 12:00:00",
	}}
	if _, err := normalizeAuditInput(attempted); err == nil || !strings.Contains(err.Error(), "attempts[0].startedAt") {
		t.Fatalf("非法 attempts[0].startedAt 必须被拒绝: %v", err)
	}
}

func TestW16aNodeUTF16StringMarshalEscapesEveryArm(t *testing.T) {
	cases := []struct {
		name string
		in   nodeUTF16String
		want string
	}{
		{"backslash", nodeUTF16String{'\\'}, `"\\"`},
		{"quote", nodeUTF16String{'"'}, `"\""`},
		{"bell", nodeUTF16String{'\b'}, `"\b"`},
		{"formfeed", nodeUTF16String{'\f'}, `"\f"`},
		{"newline", nodeUTF16String{'\n'}, `"\n"`},
		{"carriage", nodeUTF16String{'\r'}, `"\r"`},
		{"tab", nodeUTF16String{'\t'}, `"\t"`},
		{"control", nodeUTF16String{0x01}, `"\u0001"`},
		{"lone high surrogate", nodeUTF16String{0xD83D}, `"\ud83d"`},
		{"lone low surrogate", nodeUTF16String{0xDE00}, `"\ude00"`},
		{"replacement pair", nodeUTF16String{0xDE00, 0xD83D}, `"\ude00\ud83d"`},
		{"plain", nodeUTF16String{'a', 'b'}, `"ab"`},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			encoded, err := json.Marshal(item.in)
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != item.want {
				t.Fatalf("marshal=%s want=%s", encoded, item.want)
			}
		})
	}
	pair := nodeUTF16String{0xD83D, 0xDE00}
	encoded, err := json.Marshal(pair)
	if err != nil || string(encoded) != "\"😀\"" {
		t.Fatalf("合法代理对必须输出完整字符: %s %v", encoded, err)
	}
}

func TestW16aLoadConfigRejectsBadPostgresPoolSetting(t *testing.T) {
	root := t.TempDir()
	env := sqliteEnv(root)
	env["JUHE_AI_AUDIT_LOG_POSTGRES_MAX_OPEN_CONNS"] = "zero"
	if _, err := LoadConfig(func(name string) string { return env[name] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_AUDIT_LOG_POSTGRES_MAX_OPEN_CONNS 必须是正整数") {
		t.Fatalf("非法池上限必须失败: %v", err)
	}
}

func TestW16aLoadConfigRejectsBusinessSettingsSameFile(t *testing.T) {
	root := t.TempDir()
	env := sqliteEnv(root)
	if err := os.WriteFile(env["JUHE_AI_AUDIT_LOG_DATABASE_PATH"], []byte("audit-db"), 0o600); err != nil {
		t.Fatal(err)
	}
	env["JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_PATH"] = env["JUHE_AI_AUDIT_LOG_DATABASE_PATH"]
	if _, err := LoadConfig(func(name string) string { return env[name] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_PATH 不得指向 F3 SQLite 专库") {
		t.Fatalf("business settings 同文件必须失败: %v", err)
	}
}

func TestW16aLoadConfigRejectsUsageShardRootFile(t *testing.T) {
	root := t.TempDir()
	env := sqliteEnv(root)
	usageRoot := filepath.Join(root, "usage-root")
	if err := os.WriteFile(usageRoot, []byte("not-a-directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	env["JUHE_AI_USAGE_SHARD_ROOT"] = usageRoot
	if _, err := LoadConfig(func(name string) string { return env[name] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_USAGE_SHARD_ROOT must be a directory") {
		t.Fatalf("usage shard 根为文件必须失败: %v", err)
	}
}

func TestW16aLoadConfigRejectsAuditInsideUsageShardRoot(t *testing.T) {
	root := t.TempDir()
	env := sqliteEnv(root)
	usageRoot := filepath.Join(root, "usage-shards")
	if err := os.MkdirAll(usageRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	env["JUHE_AI_AUDIT_LOG_DATABASE_PATH"] = filepath.Join(usageRoot, "audit.sqlite3")
	if _, err := LoadConfig(func(name string) string { return env[name] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_AUDIT_LOG_DATABASE_PATH 不得放入 JUHE_AI_USAGE_SHARD_ROOT") {
		t.Fatalf("审计库放入 usage shard 根必须失败: %v", err)
	}
}

func TestW16aBlobPathHelpersRejectEmptyStorageKey(t *testing.T) {
	root := t.TempDir()
	if _, err := blobFilePath(root, "   "); err == nil || !strings.Contains(err.Error(), "不能为空") {
		t.Fatalf("空 storage_key 必须被拒绝: %v", err)
	}
	if _, err := blobFileMatchesMetadata(root, "  ", 1); err == nil || !strings.Contains(err.Error(), "不能为空") {
		t.Fatalf("空 storage_key 校验必须失败: %v", err)
	}
	// 越界 storage_key 命中 blobFilePath 的 rel 越界臂。
	if _, err := blobFilePath(root, "../escape.blob"); err == nil || !strings.Contains(err.Error(), "越界") {
		t.Fatalf("越界 storage_key 必须被拒绝: %v", err)
	}
}

func TestW16aCleanupBlobTempsReportsDirectoryRemoveFailure(t *testing.T) {
	root := t.TempDir()
	occupied := filepath.Join(root, "occupied-dir")
	if err := os.MkdirAll(filepath.Join(occupied, "child"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := cleanupBlobTemps([]blobPlan{{tempPath: occupied}}); err == nil {
		t.Fatal("删除非空目录形态的临时路径必须报错")
	}
}

func TestW16aStaleBlobTempCleanerSkipsFreshOrphanTemps(t *testing.T) {
	root := t.TempDir()
	recent := filepath.Join(root, ".f3-audit-blob-prev-1-recent.tmp")
	if err := os.WriteFile(recent, []byte("temp"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 未过期的旧 fence 临时文件必须被跳过（不删除、不报错）。
	if err := cleanupOrphanedStaleBlobTemps(root, 5, time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("未过期临时文件必须跳过: %v", err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatalf("未过期临时文件不得被删除: %v", err)
	}
	// 非法名字与越界 fence 名单必须跳过。
	for _, name := range []string{".f3-audit-blob-.tmp", ".f3-audit-blob-a-x-y.tmp", ".f3-audit-blob-prev-notanumber.tmp"} {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte("temp"), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-2 * time.Hour)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := cleanupOrphanedStaleBlobTemps(root, 1, time.Now()); err != nil {
		t.Fatalf("非法/未来 fence 临时文件必须跳过: %v", err)
	}
	// 根目录缺失必须按无临时文件处理。
	if err := cleanupOrphanedStaleBlobTemps(filepath.Join(root, "missing"), 5, time.Now()); err != nil {
		t.Fatalf("缺失根目录必须按无临时处理: %v", err)
	}
	if err := cleanupOwnedStaleBlobTemps(filepath.Join(root, "missing"), "owner-7", time.Now()); err != nil {
		t.Fatalf("缺失根目录必须按无临时处理: %v", err)
	}
}

func TestW16aEnsureSchemaRejectsBlobGCNameConflict(t *testing.T) {
	cfg := sqliteConfig(t, t.TempDir())
	store := openSQLiteStore(t, cfg)
	defer store.Close()
	implementation := store.(*sqlStore)
	if err := implementation.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 第二个同库 store：schemaReady=false，先占名再触发 blob GC schema 失败臂。
	second, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	secondImplementation := second.(*sqlStore)
	secondImplementation.schemaReady = false
	if _, err := implementation.db.Exec(`DROP TABLE audit_payload_blob_gc`); err != nil {
		t.Fatal(err)
	}
	if _, err := implementation.db.Exec(`CREATE VIEW audit_payload_blob_gc AS SELECT 1 AS conflict`); err != nil {
		t.Fatal(err)
	}
	if err := secondImplementation.EnsureSchema(context.Background()); err == nil || !strings.Contains(err.Error(), "blob GC schema") {
		t.Fatalf("blob GC schema 建表冲突必须失败: %v", err)
	}
}

var _ = errors.Is
