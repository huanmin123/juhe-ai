package securitypolicies

// 真实集成测试（写后缓存端到端，任务验收 3 镜像 pgtestkit/redis gate 先例）：
// INCRBY × N → FlushOnce GETDEL 合并 → PG protection_count 累计 → Store 列表
// 投影读回。授权门 JUHE_AI_TEST_EXTERNAL=1（pgtestkit.SkipUnlessExternalAutho
// rized，日常回归默认 SKIP）；PG 走 pgtestkit 一次性隔离库（夹具与
// pgkit_test.go 同源），Redis 走 .local/project-resources/dev/env/shared.env
// 的 JUHE_AI_REDIS_URL/MODE，使用一次性子 namespace，清理只 DEL 本用例键，
// 绝不 FLUSHDB/FLUSHALL（共享 Redis 红线）。连接串不进日志与断言。

import (
	"bufio"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
)

func TestProtectionWriteBehindExternal(t *testing.T) {
	pgtestkit.SkipUnlessExternalAuthorized(t)
	db := newSecurityPoliciesPGDatabase(t)
	url, rawMode := sharedEnvRedis(t)
	redisMode, err := rediscfg.ParseMode(rawMode)
	if err != nil {
		t.Fatalf("parse redis mode: %v", err)
	}
	client, err := rediscfg.NewUniversalClient(rediscfg.Config{URL: url, Mode: redisMode})
	if err != nil {
		t.Fatalf("create redis client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	// 一次性子 namespace：键只增删于本用例空间，清理 DEL 具体键（共享 Redis
	// 禁止 FLUSHDB/FLUSHALL）。
	namespace := "dev-test-protect-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	policyID := "secpol_external_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	key := ProtectionCountKey(namespace, policyID)
	t.Cleanup(func() {
		_ = client.Del(context.Background(), key).Err()
	})

	// 夹具库种一行策略（protection_count 默认 0）。
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.security_policies
		(id, system_account_id, name, description, rules_json, built_in, created_at, updated_at)
		VALUES ($1, 'acc_external', '外部集成策略', NULL, '{"rules":[]}', 0, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, policyID); err != nil {
		t.Fatalf("seed policy row: %v", err)
	}

	store := NewProtectionCountStore(client, namespace)
	flusher := NewProtectionFlusher(client, namespace, db)
	if store == nil || flusher == nil {
		t.Fatal("store/flusher 不应为 nil")
	}

	// 两次终态 INCRBY（2 + 3），首轮刷写合并为 5。
	if err := store.Increment(ctx, policyID, 2, "t1"); err != nil {
		t.Fatalf("Increment#1: %v", err)
	}
	if err := store.Increment(ctx, policyID, 3, "t2"); err != nil {
		t.Fatalf("Increment#2: %v", err)
	}
	flushed, err := flusher.FlushOnce(ctx)
	if err != nil {
		t.Fatalf("FlushOnce#1: %v", err)
	}
	if flushed != 1 {
		t.Fatalf("首轮应合并 1 键，得到 %d", flushed)
	}
	assertProtectionCount(t, db, policyID, 5, "首轮合并后")

	// 再 INCRBY 4，二轮刷写累计 9（写后缓存求和幂等）。
	if err := store.Increment(ctx, policyID, 4, "t3"); err != nil {
		t.Fatalf("Increment#3: %v", err)
	}
	if flushed, err = flusher.FlushOnce(ctx); err != nil {
		t.Fatalf("FlushOnce#2: %v", err)
	}
	if flushed != 1 {
		t.Fatalf("二轮应合并 1 键，得到 %d", flushed)
	}
	assertProtectionCount(t, db, policyID, 9, "二轮合并后")

	// GETDEL 后 Redis 键已清空（无残留增量）。
	if n, err := client.Exists(ctx, key).Result(); err != nil || n != 0 {
		t.Fatalf("GETDEL 后键应不存在: n=%d err=%v", n, err)
	}

	// 读侧投影：Store.ListPage 从 PG 列读回累计值。
	storeSQL, err := NewStore(db, time.Now, nil, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	result, err := storeSQL.ListPage(ctx, AccessScope{ViewerID: "acc_external"}, ListOptions{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatalf("ListPage: %v", err)
	}
	for _, item := range result.Items {
		if item.ID != policyID {
			continue
		}
		if item.ProtectionCount != 9 {
			t.Fatalf("列表投影 protectionCount 应为 9，得到 %d", item.ProtectionCount)
		}
		return
	}
	t.Fatalf("列表未返回策略行 %s", policyID)
}

func assertProtectionCount(t *testing.T, db *sql.DB, policyID string, want int64, label string) {
	t.Helper()
	var got int64
	if err := db.QueryRowContext(context.Background(),
		`SELECT protection_count FROM juhe_business.security_policies WHERE id = $1`, policyID).Scan(&got); err != nil {
		t.Fatalf("%s: 查询 protection_count: %v", label, err)
	}
	if got != want {
		t.Fatalf("%s: protection_count 应为 %d，得到 %d", label, want, got)
	}
}

// sharedEnvRedis 自 .local/project-resources/dev/env/shared.env 解析
// JUHE_AI_REDIS_URL / JUHE_AI_REDIS_MODE（pgtestkit sharedEnvRelative 同款向
// 上查找）；值只进连接串，不进日志与断言。
func sharedEnvRedis(t *testing.T) (url, mode string) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	var path string
	for {
		candidate := filepath.Join(dir, ".local", "project-resources", "dev", "env", "shared.env")
		if _, statErr := os.Stat(candidate); statErr == nil {
			path = candidate
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip("未找到 .local/project-resources/dev/env/shared.env，跳过真实 Redis 集成")
		}
		dir = parent
	}
	values := map[string]string{}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open shared.env: %v", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if found {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	url = values["JUHE_AI_REDIS_URL"]
	if url == "" {
		t.Skip("shared.env 缺少 JUHE_AI_REDIS_URL，跳过真实 Redis 集成")
	}
	mode = values["JUHE_AI_REDIS_MODE"]
	return url, mode
}
