package circuitstore

// w15_claim_dirty_pg_test.go 通过门禁化的覆盖库（生产形状 schema）真实运行
// ClaimDirty 的 PG CTE 分支（clock_timestamp + FOR UPDATE SKIP LOCKED）。
// 数据用 w15- 前缀，测试结束清理；DB 不可达时 t.Skip；连接串不进日志/断言。

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func w15W1CoverDSN(t *testing.T) string {
	t.Helper()
	if url := os.Getenv("JUHE_AI_W15_PG_URL"); url != "" {
		return url
	}
	raw, err := os.ReadFile(`F:\sub2api-lite\.local\project-resources\dev\env\shared.env`)
	if err != nil {
		t.Skip("w15 PG gated: shared.env 不可读")
	}
	base := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "JUHE_AI_POSTGRES_URL=") {
			base = strings.TrimPrefix(line, "JUHE_AI_POSTGRES_URL=")
		}
	}
	if base == "" {
		t.Skip("w15 PG gated: 缺少 JUHE_AI_POSTGRES_URL")
	}
	base = strings.Replace(base, ":6432/", ":5432/", 1)
	base = strings.Replace(base, "/juhe_ai_sub2api_dev?", "/juhe_ai_sub2api_dev_w1cover?", 1)
	return base
}

func TestW15ClaimDirtyPostgresOnW1Cover(t *testing.T) {
	db, err := sql.Open("pgx", w15W1CoverDSN(t))
	if err != nil {
		t.Skip("w15 PG gated: 打开失败")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Skip("w15 PG gated: PG 不可达")
	}

	// viewer 父行：优先复用已有 system_accounts 行，否则补一行。
	var viewer string
	if err := db.QueryRowContext(ctx, `SELECT id FROM juhe_business.system_accounts ORDER BY id LIMIT 1`).Scan(&viewer); err != nil {
		viewer = "w15-sys"
		if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.system_accounts (id, username, display_name, password_hash) VALUES ('w15-sys', 'w15-sys', 'w15', 'x') ON CONFLICT (id) DO NOTHING`); err != nil {
			t.Skipf("w15 PG gated: system_accounts 种子不可用: %v", err)
		}
	}
	const acct = "w15-acct"
	// accounts 父行：AI 账户域的必填列取最小集，其余走列默认。
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.accounts
		(id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version, name, type, credentials_encrypted, health_check_model, health_check_endpoint_mode, created_at, updated_at)
		VALUES ('w15-acct', $1, 'deepseek', 'profile_deepseek_openai_v1', 'anthropic', '1', 'w15', 'openai', '', 'gpt-4o', 'chat_json', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')
		ON CONFLICT (id) DO NOTHING`, viewer); err != nil {
		t.Skipf("w15 PG gated: accounts 种子不可用: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM juhe_business.account_list_availability_dirty WHERE account_id LIKE 'w15-%'`)
		_, _ = db.Exec(`DELETE FROM juhe_business.accounts WHERE id LIKE 'w15-%'`)
		_, _ = db.Exec(`DELETE FROM juhe_business.system_accounts WHERE id LIKE 'w15-%'`)
	})

	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.account_list_availability_dirty
		(account_id, viewer_system_account_id, generation, reason, available_at_ms, created_at_ms, updated_at_ms)
		VALUES ('w15-acct', $1, 1, 'w15-test', 1, 1, 1)
		ON CONFLICT (account_id) DO UPDATE SET generation = 1, available_at_ms = 1, claim_token = NULL, claimed_by = NULL, claim_until_ms = NULL`, viewer); err != nil {
		t.Fatalf("dirty 种子行插入失败: %v", err)
	}

	// boundDB 包装路径：QueryRowContext 经 sqldialect.BindSQL 转发。
	bound := &boundDB{DB: db, postgres: true}
	var probe int
	if err := bound.QueryRowContext(ctx, `SELECT 1`).Scan(&probe); err != nil || probe != 1 {
		t.Fatalf("boundDB QueryRowContext 应可用: %v", err)
	}

	repo, err := NewListAvailabilityRepo(ListAvailabilityConfig{DB: db, Postgres: true})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := repo.ClaimDirty(ctx, "w15-owner", 5, 60_000, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("PG CTE claim 应成功: %v", err)
	}
	found := false
	for _, c := range claims {
		if c.AccountID == acct {
			found = true
			if c.ClaimToken == "" {
				t.Fatal("claim 必须携带 claim token")
			}
			if c.Generation != 1 {
				t.Fatalf("generation=%d want 1", c.Generation)
			}
		}
	}
	if !found {
		t.Fatalf("应至少认领到 w15-acct，实际 claims=%d", len(claims))
	}

	// 重复 claim 在租约内不应再返回该行。
	again, err := repo.ClaimDirty(ctx, "w15-owner-2", 5, 60_000, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("二次 claim 应成功: %v", err)
	}
	for _, c := range again {
		if c.AccountID == acct {
			t.Fatal("租约内的行不应被二次认领")
		}
	}
}
