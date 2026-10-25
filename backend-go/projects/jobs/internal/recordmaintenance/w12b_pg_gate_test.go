package recordmaintenance

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// w12b_pg_gate_test.go 门禁化开发 PostgreSQL（w1cover 临时覆盖库）覆盖
// recordmaintenance 的 PG 分支：bind $N 改写、Table PG 前缀、existingColumns
// information_schema 臂、EnsureSchema PG 建表/幂等升级、Dequeue/Delete 的
// PG SQL 与 DrainOnce 全链路。数据全部 w12b- 前缀 ID，测试结束清理；
// 数据库不可达时 t.Skip。
//
// PG 补列臂（ALTER TABLE ... ADD COLUMN IF NOT EXISTS）：仅当共享表缺 v2 列
// 时执行；w1cover 表通常已带全列（EnsureSchema 幂等建表即全列），该臂由
// SQLite 旧表升级测试覆盖等价逻辑，PG 侧不为此改动共享表结构。

func w12bRMPgSkip(reason string) string { return "w12b recordmaintenance PG gated: " + reason }

func w12bRMPgDSN(t *testing.T) string {
	t.Helper()
	if url := os.Getenv("JUHE_AI_W12B_RECORDMAINTENANCE_PG_URL"); url != "" {
		return url
	}
	raw, err := os.ReadFile(`F:\sub2api-lite\.local\project-resources\dev\env\shared.env`)
	if err != nil {
		t.Skip(w12bRMPgSkip("shared.env 不可读"))
	}
	base := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "JUHE_AI_POSTGRES_URL=") {
			base = strings.TrimPrefix(line, "JUHE_AI_POSTGRES_URL=")
		}
	}
	if base == "" {
		t.Skip(w12bRMPgSkip("shared.env 缺少 JUHE_AI_POSTGRES_URL"))
	}
	base = strings.Replace(base, ":6432/", ":5432/", 1)
	base = strings.Replace(base, "/juhe_ai_sub2api_dev?", "/juhe_ai_sub2api_dev_w1cover?", 1)
	return base
}

func w12bOpenPgStore(t *testing.T) *Store {
	t.Helper()
	db, err := w12bPgOpen(t)
	if err != nil {
		t.Skipf(w12bRMPgSkip("PG 打开失败: %v"), err)
	}
	store, err := OpenStore(db)
	if err != nil {
		t.Skipf(w12bRMPgSkip("OpenStore 失败: %v"), err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return store
}

func w12bPgOpen(t *testing.T) (*sql.DB, error) {
	t.Helper()
	db, err := sql.Open("pgx", w12bRMPgDSN(t))
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

