package main

// 临时探针（覆盖率战役基础设施，用后即删）：验证 dev PG 可达性、创建一次性
// 临时子库 juhe_ai_sub2api_dev_w1cover、应用 maintenance PG DDL + 种子。
// 遵循 .local/project-resources/dev/runbooks/开发数据库生命周期.md 的临时子库
// 约定；凭据只从 dev env 文件读取，不写入任何持久产物。

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
)

const w1coverDB = "juhe_ai_sub2api_dev_w1cover"

func w1pgEnvFile(t *testing.T) map[string]string {
	t.Helper()
	path := "../../../../../.local/project-resources/dev/env/shared.env"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("dev env 不可达（跳过 PG 门禁测试）: %v", err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return values
}

func TestW1PGProbeAndSetup(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过真实 PG")
	}
	env := w1pgEnvFile(t)
	host := env["DEV_POSTGRES_HOST"]
	directPort := env["DEV_POSTGRES_DIRECT_PORT"]
	adminUser := env["DEV_POSTGRES_ADMIN_USERNAME"]
	adminPass := env["DEV_POSTGRES_ADMIN_PASSWORD"]
	appURL := env["JUHE_AI_POSTGRES_URL"]
	if host == "" || directPort == "" || adminUser == "" || appURL == "" {
		t.Skipf("dev env 缺少 PG 键")
	}

	adminDSN := fmt.Sprintf("postgres://%s:%s@%s:%s/postgres?sslmode=disable", adminUser, adminPass, host, directPort)
	adminDB, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatalf("admin open: %v", err)
	}
	defer adminDB.Close()
	if err := adminDB.Ping(); err != nil {
		t.Skipf("dev PG 管理口不可达（跳过）: %v", err)
	}

	// 幂等：先清掉同名残留临时库（只允许动 w1cover 名字）。
	var exists bool
	if err := adminDB.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, w1coverDB).Scan(&exists); err != nil {
		t.Fatalf("查库: %v", err)
	}
	if exists {
		// 终止会话后删除，保证干净重建。
		if _, err := adminDB.Exec(fmt.Sprintf(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s' AND pid <> pg_backend_pid()`, w1coverDB)); err != nil {
			t.Fatalf("终止残留会话: %v", err)
		}
		if _, err := adminDB.Exec(fmt.Sprintf(`DROP DATABASE %s`, w1coverDB)); err != nil {
			t.Fatalf("删除残留临时库: %v", err)
		}
	}
	if _, err := adminDB.Exec(fmt.Sprintf(`CREATE DATABASE %s OWNER %s`, w1coverDB, env["DEV_POSTGRES_APP_USERNAME"])); err != nil {
		t.Fatalf("创建临时子库: %v", err)
	}

	// 应用 URL 指向临时子库（替换路径段）。
	sep := strings.LastIndex(appURL, "/")
	tempAppURL := appURL[:sep+1] + w1coverDB
	appDB, err := sql.Open("pgx", tempAppURL)
	if err != nil {
		t.Fatalf("app open: %v", err)
	}
	defer appDB.Close()
	appDB.SetMaxOpenConns(4)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := appDB.PingContext(ctx); err != nil {
		t.Fatalf("temp db ping: %v", err)
	}
	statements, err := bootstrap.EnsurePostgres(ctx, appDB)
	if err != nil {
		t.Fatalf("EnsurePostgres: %v", err)
	}
	seeded, err := bootstrap.SeedPostgres(ctx, appDB, bootstrap.SeedOptions{Now: time.Now, Secret: "w1cover-secret"})
	if err != nil {
		t.Fatalf("SeedPostgres: %v", err)
	}

	// 六 schema 存在性核验。
	var schemaCount int
	if err := appDB.QueryRow(`SELECT COUNT(*) FROM information_schema.schemata
		WHERE schema_name IN ('juhe_business','juhe_chat','juhe_codex_context','juhe_dataset','juhe_stats','juhe_usage')`).Scan(&schemaCount); err != nil {
		t.Fatalf("核验 schema: %v", err)
	}
	if schemaCount != 6 {
		t.Fatalf("六 schema 数 = %d, want 6", schemaCount)
	}
	fmt.Printf("W1COVER-READY db=%s statements=%d seeds=%d schemas=%d\n", w1coverDB, statements, seeded.StatementCount, schemaCount)
}
