package retention

// media-jobs-retention 语义测试（媒体设计 §8.2）：未终态超期行置 expired、
// 全部超期行（含终态）删除、批次限流、零任务快速返回、表缺席快速返回。
// store 语义用内存 SQLite 实库验证（子查询/方言 SQL 真执行）。
import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newMediaJobsTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:mediajobs-retention-"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE media_jobs (
		id TEXT PRIMARY KEY, kind TEXT NOT NULL, api_key_id TEXT NOT NULL, account_id TEXT NOT NULL,
		provider_code TEXT NOT NULL, provider_protocol_profile_id TEXT, upstream_job_id TEXT NOT NULL,
		status TEXT NOT NULL, request_snapshot_json TEXT NOT NULL DEFAULT '{}', artifact_json TEXT NOT NULL DEFAULT '{}',
		error_json TEXT NOT NULL DEFAULT '{}', usage_json TEXT NOT NULL DEFAULT '{}', cost_usd REAL NOT NULL DEFAULT 0,
		params_applied_json TEXT NOT NULL DEFAULT '[]', params_ignored_json TEXT NOT NULL DEFAULT '[]',
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("create media_jobs: %v", err)
	}
	return db
}

func seedMediaJobsRow(t *testing.T, db *sql.DB, id, status string, age time.Duration) {
	t.Helper()
	at := time.Now().UTC().Add(-age).Format("2006-01-02T15:04:05.000Z")
	if _, err := db.Exec(`INSERT INTO media_jobs (id, kind, api_key_id, account_id, provider_code,
		upstream_job_id, status, created_at, updated_at) VALUES (?, 'video', 'key', 'acc', 'openai', ?, ?, ?, ?)`,
		id, "up_"+id, status, at, at); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func mediaJobsCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	return count
}

func TestMediaJobsRetentionExpireThenDelete(t *testing.T) {
	db := newMediaJobsTestDB(t)
	seedMediaJobsRow(t, db, "video_stale_queued", "queued", 8*24*time.Hour)
	seedMediaJobsRow(t, db, "video_stale_progress", "in_progress", 9*24*time.Hour)
	seedMediaJobsRow(t, db, "video_stale_completed", "completed", 8*24*time.Hour)
	seedMediaJobsRow(t, db, "video_fresh_queued", "queued", time.Hour)
	seedMediaJobsRow(t, db, "video_fresh_completed", "completed", 2*24*time.Hour)

	job := &MediaJobsRetentionJob{Store: &MediaJobsSQLStore{DB: db}}
	result, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// 未终态超期 2 行置 expired；超期行共 3 行全删（置 expired 后同轮删除）。
	if result.ExpiredNonTerminal != 2 {
		t.Fatalf("expiredNonTerminal = %d, want 2", result.ExpiredNonTerminal)
	}
	if result.DeletedRows != 3 {
		t.Fatalf("deletedRows = %d, want 3", result.DeletedRows)
	}
	// 剩余仅新鲜行，状态未被动过。
	if got := mediaJobsCount(t, db, `SELECT COUNT(*) FROM media_jobs`); got != 2 {
		t.Fatalf("remaining rows = %d, want 2", got)
	}
	if got := mediaJobsCount(t, db, `SELECT COUNT(*) FROM media_jobs WHERE status = 'expired'`); got != 0 {
		t.Fatalf("expired rows = %d, want 0（超期行已删净）", got)
	}
	if got := mediaJobsCount(t, db, `SELECT COUNT(*) FROM media_jobs WHERE id = 'video_fresh_queued' AND status = 'queued'`); got != 1 {
		t.Fatalf("新鲜未终态行被误终态化: %d", got)
	}
}

func TestMediaJobsRetentionBatchLimit(t *testing.T) {
	db := newMediaJobsTestDB(t)
	for index := 0; index < 7; index++ {
		seedMediaJobsRow(t, db, "video_old_"+string(rune('a'+index)), "completed", 10*24*time.Hour)
	}
	job := &MediaJobsRetentionJob{
		Store:     &MediaJobsSQLStore{DB: db},
		BatchSize: 3,
		Clock:     func() time.Time { return time.Now() },
	}
	result, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.DeletedRows != 3 {
		t.Fatalf("deletedRows = %d, want 3（批次限流）", result.DeletedRows)
	}
	if got := mediaJobsCount(t, db, `SELECT COUNT(*) FROM media_jobs`); got != 4 {
		t.Fatalf("remaining = %d, want 4（限流后未删尽，下轮收敛）", got)
	}
	// 第二/三轮删剩余（每轮仍受 BatchSize=3 限流：4 = 3 + 1）。
	result, err = job.Run(context.Background())
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if result.DeletedRows != 3 || result.ExpiredNonTerminal != 0 {
		t.Fatalf("second run = %+v, want deleted 3 expired 0", result)
	}
	result, err = job.Run(context.Background())
	if err != nil {
		t.Fatalf("run 3: %v", err)
	}
	if result.DeletedRows != 1 {
		t.Fatalf("third run deleted = %d, want 1", result.DeletedRows)
	}
	if got := mediaJobsCount(t, db, `SELECT COUNT(*) FROM media_jobs`); got != 0 {
		t.Fatalf("final remaining = %d, want 0", got)
	}
}

// TestMediaJobsRetentionDeleteOnlyTerminal MAJOR-B 语义回归：DELETE 只删
// 终态/已 expired 行——终态化批次限流残留的超期未终态行留给下一轮先终态化
// 再删，不得直接删除仍处未终态的行（防与网关并发终态回填竞争时误删在推进
// 的行）。
func TestMediaJobsRetentionDeleteOnlyTerminal(t *testing.T) {
	db := newMediaJobsTestDB(t)
	for index := 0; index < 4; index++ {
		seedMediaJobsRow(t, db, "video_stale_queued_"+string(rune('a'+index)), "queued", 8*24*time.Hour)
	}
	job := &MediaJobsRetentionJob{
		Store:     &MediaJobsSQLStore{DB: db},
		BatchSize: 2,
	}
	// 第一轮：终态化 2 行（批次限流）→ DELETE 只删这 2 行已 expired 的行；
	// 残留 2 行超期 queued 行不被直接删除。
	result, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if result.ExpiredNonTerminal != 2 || result.DeletedRows != 2 {
		t.Fatalf("run 1 = %+v, want expired 2 deleted 2", result)
	}
	if got := mediaJobsCount(t, db, `SELECT COUNT(*) FROM media_jobs WHERE status = 'queued'`); got != 2 {
		t.Fatalf("残留超期未终态行 = %d, want 2（DELETE 不得删未终态行）", got)
	}
	// 第二轮：残留行先终态化再删，收敛到 0。
	result, err = job.Run(context.Background())
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if result.ExpiredNonTerminal != 2 || result.DeletedRows != 2 {
		t.Fatalf("run 2 = %+v, want expired 2 deleted 2（先标后删收敛）", result)
	}
	if got := mediaJobsCount(t, db, `SELECT COUNT(*) FROM media_jobs`); got != 0 {
		t.Fatalf("final remaining = %d, want 0", got)
	}
}

func TestMediaJobsRetentionZeroTasksFastReturn(t *testing.T) {
	db := newMediaJobsTestDB(t)
	job := &MediaJobsRetentionJob{Store: &MediaJobsSQLStore{DB: db}}
	result, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.ExpiredNonTerminal != 0 || result.DeletedRows != 0 {
		t.Fatalf("empty run = %+v, want 零计数", result)
	}
}

func TestMediaJobsRetentionTableMissingFastReturn(t *testing.T) {
	// 不建表的内存库：表缺席按零任务返回，不报错。
	db, err := sql.Open("sqlite", "file:mediajobs-retention-missing?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	job := &MediaJobsRetentionJob{Store: &MediaJobsSQLStore{DB: db}}
	result, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("table missing run err = %v, want nil（零任务快速返回）", err)
	}
	if result.ExpiredNonTerminal != 0 || result.DeletedRows != 0 {
		t.Fatalf("table missing run = %+v, want 零计数", result)
	}
	if !IsMediaJobsTableMissing(errors.New(`no such table: media_jobs`)) {
		t.Fatal("IsMediaJobsTableMissing 未命中 SQLite 缺表错误")
	}
	if IsMediaJobsTableMissing(errors.New("other error")) {
		t.Fatal("IsMediaJobsTableMissing 误报")
	}
}

func TestMediaJobsRetentionStoreNilFailsLoud(t *testing.T) {
	job := &MediaJobsRetentionJob{}
	if _, err := job.Run(context.Background()); err == nil {
		t.Fatal("store 未初始化必须显式报错")
	}
}
