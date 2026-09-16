package businesshandoff

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
	_ "modernc.org/sqlite"
)

// w9aCreateEmptySQLite 建一个最小的合法 SQLite 文件。
func w9aCreateEmptySQLite(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(context.Background(), "CREATE TABLE placeholder (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestW9AVerifyPathRules 覆盖 Verify 的空路径、同路径、缺失、目录、同文件与
// 完整就绪场景。
func TestW9AVerifyPathRules(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	business := w9aCreateEmptySQLite(t, dir, "business.sqlite3")
	j3b := w9aCreateEmptySQLite(t, dir, "j3b.sqlite3")

	// 空路径。
	report, err := Verify(ctx, "", "")
	if err != nil {
		t.Fatalf("Verify 空 path: %v", err)
	}
	joined := strings.Join(report.Errors, "\n")
	if !strings.Contains(joined, "Business SQLite path is empty") || !strings.Contains(joined, "J3b SQLite path is empty") {
		t.Fatalf("空路径错误缺失: %v", report.Errors)
	}

	// 仅 business 空路径 + j3b 合法：跳过 distinct 检查。
	report, err = Verify(ctx, "", j3b)
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(report.Errors, "\n")
	if !strings.Contains(joined, "Business SQLite path is empty") {
		t.Fatalf("business 空路径错误缺失: %v", report.Errors)
	}

	// 字符串相同的路径 → identical。
	report, err = Verify(ctx, business, business)
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(report.Errors, "\n")
	if !strings.Contains(joined, "paths are identical") {
		t.Fatalf("identical 错误缺失: %v", report.Errors)
	}

	// 文件缺失。
	missing := filepath.Join(dir, "missing.sqlite3")
	report, err = Verify(ctx, missing, j3b)
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(report.Errors, "\n")
	if !strings.Contains(joined, "Business SQLite: path does not exist") {
		t.Fatalf("缺失文件错误缺失: %v", report.Errors)
	}

	// 目录 → 非 regular file。
	report, err = Verify(ctx, dir, j3b)
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(report.Errors, "\n")
	if !strings.Contains(joined, "Business SQLite path is not a regular file") {
		t.Fatalf("目录错误缺失: %v", report.Errors)
	}

	// 硬链接 → os.SameFile 检测。
	hardlink := filepath.Join(dir, "hardlink.sqlite3")
	if err := os.Link(business, hardlink); err != nil {
		t.Skipf("无法创建硬链接: %v", err)
	}
	report, err = Verify(ctx, business, hardlink)
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(report.Errors, "\n")
	if !strings.Contains(joined, "resolve to the same file") {
		t.Fatalf("same file 错误缺失: %v", report.Errors)
	}

	// 完整就绪：两个不同的合法 SQLite 文件。
	report, err = Verify(ctx, business, j3b)
	if err != nil {
		t.Fatal(err)
	}
	if !report.PathIsolationReady || !report.Ready || !report.UserDatabaseTouched == false {
		t.Fatalf("完整场景必须就绪: %+v errors=%v", report, report.Errors)
	}
}

// TestW9AVerifySQLiteSchemaErrorPaths 覆盖空路径与不可打开文件的错误分支。
func TestW9AVerifySQLiteSchemaErrorPaths(t *testing.T) {
	ctx := context.Background()
	if report, err := VerifySQLiteSchema(ctx, ""); err != nil || len(report.Errors) != 1 || !strings.Contains(report.Errors[0], "path is empty") {
		t.Fatalf("空 path: %v %v", report, err)
	}
	missing := filepath.Join(t.TempDir(), "missing.sqlite3")
	if _, err := VerifySQLiteSchema(ctx, missing); err == nil || !strings.Contains(err.Error(), "query_only pragma") {
		t.Fatalf("缺失文件必须触发 PRAGMA 错误: %v", err)
	}
}

// TestW9ASQLiteHelpersOnReadOnlyMissingDB 用 mode=ro 指向缺失文件的懒打开
// 句柄覆盖各检查函数的查询错误传播分支（Query 时才打开并失败）。
func TestW9ASQLiteHelpersOnReadOnlyMissingDB(t *testing.T) {
	ctx := context.Background()
	missing := filepath.Join(t.TempDir(), "missing.sqlite3")
	db, err := sql.Open("sqlite", "file:"+missing+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if _, err := sqliteObjects(ctx, db, "table"); err == nil {
		t.Fatal("sqliteObjects 只读缺失文件必须报错")
	}
	if _, err := sqliteColumns(ctx, db, "t"); err == nil {
		t.Fatal("sqliteColumns 只读缺失文件必须报错")
	}
	if _, err := sqlitePrimaryKey(ctx, db, "t"); err == nil {
		t.Fatal("sqlitePrimaryKey 只读缺失文件必须报错")
	}
	if _, err := sqliteHasUniqueConstraint(ctx, db, "t", []string{"id"}); err == nil {
		t.Fatal("sqliteHasUniqueConstraint 只读缺失文件必须报错")
	}
	if _, err := sqliteIndexColumns(ctx, db, "idx"); err == nil {
		t.Fatal("sqliteIndexColumns 只读缺失文件必须报错")
	}
	if _, err := sqliteForeignKeys(ctx, db, "t"); err == nil {
		t.Fatal("sqliteForeignKeys 只读缺失文件必须报错")
	}
	if _, _, err := sqliteIndexMatches(ctx, db, "t", contracts.SQLiteIndexDefinition{Name: "idx", Columns: []string{"a"}}); err == nil {
		t.Fatal("sqliteIndexMatches 只读缺失文件必须报错")
	}
}

// TestW9ASQLiteIndexInspectionOnRealDB 用真实索引（普通/表达式/部分/缺失）覆盖
// sqliteIndexMatches 与 sqliteIndexColumns 的判定分支。
func TestW9ASQLiteIndexInspectionOnRealDB(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "idx.sqlite3")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT, note TEXT);
		CREATE INDEX idx_plain ON t(name);
		CREATE UNIQUE INDEX idx_unique ON t(name, note);
		CREATE INDEX idx_expr ON t(lower(name));
		CREATE INDEX idx_partial ON t(name) WHERE note IS NOT NULL;
	`); err != nil {
		t.Fatal(err)
	}

	// 表达式索引 → "index contains an expression"。
	if _, err := sqliteIndexColumns(ctx, db, "idx_expr"); err == nil || !strings.Contains(err.Error(), "expression") {
		t.Fatalf("表达式索引必须报错: %v", err)
	}
	ok, detail, err := sqliteIndexMatches(ctx, db, "t", contracts.SQLiteIndexDefinition{Name: "idx_expr", Columns: []string{"lower(name)"}})
	if err != nil {
		t.Fatal(err)
	}
	if ok || !strings.Contains(detail, "expression") {
		t.Fatalf("表达式索引判定 = %v %q", ok, detail)
	}

	// 普通索引精确匹配 → 通过。
	if ok, detail, err := sqliteIndexMatches(ctx, db, "t", contracts.SQLiteIndexDefinition{Name: "idx_plain", Columns: []string{"name"}}); err != nil || !ok {
		t.Fatalf("普通索引应匹配: ok=%v detail=%q err=%v", ok, detail, err)
	}

	// unique 漂移。
	ok, detail, err = sqliteIndexMatches(ctx, db, "t", contracts.SQLiteIndexDefinition{Name: "idx_plain", Columns: []string{"name"}, Unique: true})
	if err != nil || ok || !strings.Contains(detail, "unique=") {
		t.Fatalf("unique 漂移 = %v %q %v", ok, detail, err)
	}

	// 列序漂移（unique 需匹配才能走到列比较）。
	ok, detail, err = sqliteIndexMatches(ctx, db, "t", contracts.SQLiteIndexDefinition{Name: "idx_unique", Columns: []string{"note", "name"}, Unique: true})
	if err != nil || ok || !strings.Contains(detail, "columns=") {
		t.Fatalf("列漂移 = %v %q %v", ok, detail, err)
	}

	// 缺失索引。
	ok, detail, err = sqliteIndexMatches(ctx, db, "t", contracts.SQLiteIndexDefinition{Name: "idx_missing", Columns: []string{"name"}})
	if err != nil || ok || !strings.Contains(detail, "missing") {
		t.Fatalf("缺失索引 = %v %q %v", ok, detail, err)
	}

	// 部分索引 + 谓词一致 → 通过；谓词不一致 → 拒绝；required 无谓词 → 拒绝。
	if ok, detail, err = sqliteIndexMatches(ctx, db, "t", contracts.SQLiteIndexDefinition{
		Name: "idx_partial", Columns: []string{"name"}, Predicate: "note IS NOT NULL",
	}); err != nil || !ok {
		t.Fatalf("部分索引应匹配: ok=%v detail=%q err=%v", ok, detail, err)
	}
	if ok, detail, err = sqliteIndexMatches(ctx, db, "t", contracts.SQLiteIndexDefinition{
		Name: "idx_partial", Columns: []string{"name"}, Predicate: "name IS NOT NULL",
	}); err != nil || ok || !strings.Contains(detail, "predicate=") {
		t.Fatalf("谓词漂移 = %v %q %v", ok, detail, err)
	}
	if ok, detail, err = sqliteIndexMatches(ctx, db, "t", contracts.SQLiteIndexDefinition{
		Name: "idx_partial", Columns: []string{"name"},
	}); err != nil || ok || !strings.Contains(detail, "partial") {
		t.Fatalf("意外部分谓词 = %v %q %v", ok, detail, err)
	}

	// Unique 索引被 sqliteHasUniqueConstraint 识别。
	found, err := sqliteHasUniqueConstraint(ctx, db, "t", []string{"name", "note"})
	if err != nil || !found {
		t.Fatalf("unique 约束应被识别: %v %v", found, err)
	}
}

// TestW9AForeignKeySignatureAndPredicates 锁定签名与谓词等价归一化。
func TestW9AForeignKeySignatureAndPredicates(t *testing.T) {
	got := foreignKeySignature("accounts", contracts.SQLiteForeignKeySpec{
		Columns: []string{"system_account_id"}, RefTable: "system_accounts", RefColumns: []string{"id"},
	})
	if got != "accounts(system_account_id)->system_accounts(id) onDelete=NO ACTION onUpdate=NO ACTION" {
		t.Fatalf("foreignKeySignature 默认动作 = %q", got)
	}
	got = foreignKeySignature("accounts", contracts.SQLiteForeignKeySpec{
		Columns: []string{"a", "b"}, RefTable: "x", RefColumns: []string{"c"}, OnDelete: "CASCADE", OnUpdate: "SET NULL",
	})
	if got != "accounts(a,b)->x(c) onDelete=CASCADE onUpdate=SET NULL" {
		t.Fatalf("foreignKeySignature 显式动作 = %q", got)
	}

	if !predicatesEquivalent("note IS NOT NULL", "note is not null") {
		t.Fatal("大小写差异必须等价")
	}
	if !predicatesEquivalent(`"scope_kind" = 'key_model'`, "scope_kind = 'key_model'") {
		t.Fatal("引号差异必须等价")
	}
	if predicatesEquivalent("a = 1", "a = 2") {
		t.Fatal("不同谓词不得等价")
	}
	if got := sqliteIndexPredicate("CREATE INDEX x ON t(a) WHERE scope_kind = 'key_model'"); got != "scope_kind = 'key_model'" {
		t.Fatalf("sqliteIndexPredicate = %q", got)
	}
	if got := sqliteIndexPredicate("CREATE INDEX x ON t(a)"); got != "" {
		t.Fatalf("无谓词索引 = %q", got)
	}
}

// TestW9AVerifyJ3bBackupArtifactDirectory 覆盖 artifact 为目录时的拒绝分支。
func TestW9AVerifyJ3bBackupArtifactDirectory(t *testing.T) {
	dir := t.TempDir()
	err := verifyJ3bBackupArtifact(J3bBackupArtifact{Path: dir, Hash: strings.Repeat("ab", 32)})
	if err == nil || !strings.Contains(err.Error(), "must be a regular file") {
		t.Fatalf("目录 artifact 必须被拒绝: %v", err)
	}
	err = verifyJ3bBackupArtifact(J3bBackupArtifact{Path: filepath.Join(dir, "missing.bin"), Hash: strings.Repeat("ab", 32)})
	if err == nil || !strings.Contains(err.Error(), "path is unreadable") {
		t.Fatalf("缺失 artifact 必须报错: %v", err)
	}
}

// TestW9AVerifyJ3bReadbackManifestIdentityMismatch 覆盖 manifest 文件与
// evidence 引用的身份不一致分支。
func TestW9AVerifyJ3bReadbackManifestIdentityMismatch(t *testing.T) {
	dir := t.TempDir()
	manifest := map[string]any{
		"formatVersion":          contracts.J3bReadbackManifestFormatVersion,
		"scope":                  contracts.J3bReadbackManifestScope,
		"producer":               "probe",
		"sourceSnapshotIdentity": "snap-1",
		"sourceSchema":           "legacy-sqlite-dataset+stats",
		"targetSchema":           "juhe-j3b-sqlite",
		"projectionComplete":     true,
		"verifiedAt":             time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02T15:04:05Z07:00"),
		"tables":                 []json.RawMessage{},
		"manifestHash":           strings.Repeat("ab", 32),
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)

	// 破坏 reference 的 snapshot identity → 身份不一致。
	bad := contracts.J3bReadbackManifestReference{
		Path:                   manifestPath,
		Hash:                   "sha256:" + hex.EncodeToString(sum[:]),
		FormatVersion:          contracts.J3bReadbackManifestFormatVersion,
		Scope:                  contracts.J3bReadbackManifestScope,
		SourceSnapshotIdentity: "different",
		SourceSchema:           "legacy-sqlite-dataset+stats",
		TargetSchema:           "juhe-j3b-sqlite",
	}
	err = verifyJ3bReadbackManifest(bad, J3bCutoverEvidence{}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "identity does not match") {
		t.Fatalf("身份不一致必须报错: %v", err)
	}

	// 文件内容与 hash 不匹配。
	hdr := bad
	hdr.SourceSnapshotIdentity = "snap-1"
	hdr.Hash = "sha256:" + strings.Repeat("cd", 32)
	err = verifyJ3bReadbackManifest(hdr, J3bCutoverEvidence{}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "does not match evidence reference") {
		t.Fatalf("hash 不匹配必须报错: %v", err)
	}

	// manifest 内容不是合法 JSON（hash 正确）→ decode 失败。
	broken := []byte(`{invalid`)
	brokenPath := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(brokenPath, broken, 0o644); err != nil {
		t.Fatal(err)
	}
	brokenSum := sha256.Sum256(broken)
	brokenRef := contracts.J3bReadbackManifestReference{Path: brokenPath, Hash: "sha256:" + hex.EncodeToString(brokenSum[:])}
	err = verifyJ3bReadbackManifest(brokenRef, J3bCutoverEvidence{}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "decode manifest") {
		t.Fatalf("损坏 manifest 必须报 decode 错误: %v", err)
	}

	// 文件缺失 → read manifest 失败。
	err = verifyJ3bReadbackManifest(contracts.J3bReadbackManifestReference{Path: filepath.Join(dir, "nope.json")}, J3bCutoverEvidence{}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "read manifest") {
		t.Fatalf("缺失 manifest 必须报错: %v", err)
	}
}

// TestW9AQuoteIdentifier 覆盖引号转义。
func TestW9AQuoteIdentifier(t *testing.T) {
	if got := quoteIdentifier(`ta"ble`); got != `"ta""ble"` {
		t.Fatalf("quoteIdentifier = %q", got)
	}
	if got := quoteIdentifier("plain"); got != `"plain"` {
		t.Fatalf("quoteIdentifier plain = %q", got)
	}
}

// TestW9ASameStringsHelper 锁定字符串切片比较。
func TestW9ASameStringsHelper(t *testing.T) {
	if !sameStrings([]string{"a"}, []string{"a"}) {
		t.Fatal("相同内容必须相等")
	}
	if sameStrings([]string{"a"}, []string{"b"}) {
		t.Fatal("不同内容不得相等")
	}
	if sameStrings(nil, []string{"b"}) {
		t.Fatal("长度不同不得相等")
	}
}

// 未使用的占位，防止 driver 导入在精简后失效。
var _ = driver.ErrSkip
var _ = errors.New
var _ = fmt.Sprintf

// TestW9AVerifyJ3BDirectoryAndPredicates 补充 j3b 目录、复合外键排序与括号
// 谓词的分支覆盖。
func TestW9AVerifyJ3BDirectoryAndPredicates(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	business := w9aCreateEmptySQLite(t, dir, "business.sqlite3")

	// j3b 是目录 → 非 regular file。
	report, err := Verify(ctx, business, dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(report.Errors, "\n"), "J3b SQLite path is not a regular file") == false {
		t.Fatalf("j3b 目录错误缺失: %v", report.Errors)
	}

	// 复合外键 → foreign key 分组排序闭包执行。
	path := filepath.Join(dir, "fk.sqlite3")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE parent (a INTEGER, b TEXT, PRIMARY KEY(a, b));
		CREATE TABLE child (pa INTEGER, pb TEXT, FOREIGN KEY(pa, pb) REFERENCES parent(a, b) ON DELETE CASCADE ON UPDATE SET NULL);
	`); err != nil {
		t.Fatal(err)
	}
	keys, err := sqliteForeignKeys(ctx, db, "child")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for signature := range keys {
		if strings.Contains(signature, "onDelete=CASCADE onUpdate=SET NULL") && strings.Contains(signature, "(pa,pb)") {
			found = true
		}
	}
	if !found {
		t.Fatalf("复合外键签名缺失: %v", keys)
	}

	// 外层括号包裹的谓词归一化。
	if !predicatesEquivalent("(scope_kind = 'key_model')", "scope_kind = 'key_model'") {
		t.Fatal("外层括号必须归一化")
	}
}
