package j3aproxylatency

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
)

// TestPostgresBootstrapSmoke is deliberately opt-in. It must run against a
// disposable database whose juhe_jobs schema was pre-created by an
// administrator; it never creates or drops a schema/database itself.
func TestPostgresBootstrapSmoke(t *testing.T) {
	rawURL := strings.TrimSpace(os.Getenv("JUHE_AI_MAINTENANCE_J3A_BOOTSTRAP_SMOKE_URL"))
	if rawURL == "" {
		t.Skip("JUHE_AI_MAINTENANCE_J3A_BOOTSTRAP_SMOKE_URL 未设置；跳过外部 PostgreSQL bootstrap smoke")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Port() != "5432" || !strings.HasPrefix(strings.Trim(parsed.Path, "/"), "juhe_ai_sub2api_dev_j3a_") {
		t.Fatalf("bootstrap smoke 只允许直连 5432 的一次性 j3a scratch database")
	}
	db, err := Open(rawURL)
	if err != nil {
		t.Fatalf("打开 bootstrap smoke PostgreSQL 连接失败: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	before, err := Run(ctx, db, false)
	if err != nil {
		t.Fatalf("bootstrap smoke 初始只读检查失败: %v", err)
	}
	if before.Ready() {
		t.Fatalf("bootstrap smoke 必须从缺少 J3a 对象的全新 scratch 开始: %+v", before)
	}
	if before.MissingSchema || before.OwnerMismatch || len(before.MissingTables) != len(requiredTables) || len(before.MissingIndexes) != len(requiredIndexes) || len(before.InvalidIndexes) != 0 {
		t.Fatalf("bootstrap smoke 初始报告不符合预期: %+v", before)
	}

	applied, err := Run(ctx, db, true)
	if err != nil {
		t.Fatalf("bootstrap smoke apply 失败: %v", err)
	}
	if !applied.Applied || !applied.Ready() {
		t.Fatalf("bootstrap smoke apply 报告不符合预期: %+v", applied)
	}

	after, err := Run(ctx, db, false)
	if err != nil {
		t.Fatalf("bootstrap smoke apply 后只读检查失败: %v", err)
	}
	if after.Applied || !after.Ready() {
		t.Fatalf("bootstrap smoke apply 后报告不符合预期: %+v", after)
	}

	if _, err := db.ExecContext(ctx, `ALTER TABLE juhe_jobs.proxy_latency_owner_leases ALTER COLUMN fence_token TYPE TEXT USING fence_token::TEXT`); err != nil {
		t.Fatalf("准备 bootstrap smoke malformed table 失败: %v", err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE juhe_jobs.proxy_latency_inputs DROP CONSTRAINT proxy_latency_inputs_proxy_id_input_version_key`); err != nil {
		t.Fatalf("准备 bootstrap smoke missing constraint 失败: %v", err)
	}
	malformed, err := Run(ctx, db, false)
	if err != nil {
		t.Fatalf("bootstrap smoke malformed table 检查失败: %v", err)
	}
	if malformed.Ready() || !containsString(malformed.InvalidTables, "proxy_latency_owner_leases.fence_token:type=text/text") || !containsString(malformed.InvalidTables, "proxy_latency_inputs:constraint=unique (proxy_id, input_version)") {
		t.Fatalf("bootstrap smoke 未识别 malformed table: %+v", malformed)
	}
	if _, err := Run(ctx, db, true); err == nil {
		t.Fatal("bootstrap smoke apply 不得静默修复既有 malformed table")
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
