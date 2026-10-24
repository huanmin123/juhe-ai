// Package pgtestkit 为后端测试提供隔离的 PostgreSQL 测试库（每测试一个独立
// database，PLAN-20261024T051925945Z 测试基建唯一存储设施）：
//
//   - 管理连接解析顺序：JUHE_AI_TEST_PG_ADMIN_URL 显式 env（库名部分被替换
//     为 postgres 维护库）→ 回退解析仓库内 .local/project-resources/dev/env/
//     shared.env（自当前目录向上查找），以 DEV_POSTGRES_ADMIN_USERNAME/
//     PASSWORD 管理员凭据与 DEV_POSTGRES_HOST/DEV_POSTGRES_DIRECT_PORT 直连
//     端口组装，sslmode 沿用 JUHE_AI_POSTGRES_URL 的查询参数（缺省补
//     disable）。连接串内容永不进入错误消息、日志或断言。
//   - 库名遵守开发库临时子库约定前缀 juhe_ai_sub2api_dev_<name>，由测试名
//     派生可读段 + 进程号 + 进程内计数保证唯一；测试结束终止残余连接并删库。
//   - prepare 回调在独立库上注入 fresh DDL/种子（如 maintenance
//     bootstrap.EnsurePostgres）；kit 自身不 import 任何项目的 schema 包
//     （跨模块边界由调用方回调承接）。
//
// 配置缺失或 PG 不可达时测试显式失败而非 skip：存储测试迁移 PG 后即为核心
// 回归面，静默跳过等于假绿；离线环境须显式提供 JUHE_AI_TEST_PG_ADMIN_URL。
package pgtestkit

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// EnvAdminURL 是显式管理员连接串 env（优先于 shared.env 回退）。
const EnvAdminURL = "JUHE_AI_TEST_PG_ADMIN_URL"

// sharedEnvRelative 是开发库 env 事实文件相对仓库根的固定位置。
const sharedEnvRelative = ".local/project-resources/dev/env/shared.env"

// testDBPrefix 是开发库约定允许的临时子库严格前缀（juhe_ai_sub2api_dev_<name>）。
const testDBPrefix = "juhe_ai_sub2api_dev_t"

// adminPingTimeout / opTimeout 是管理连接与建删库单操作的超时上限。
const (
	adminPingTimeout = 10 * time.Second
	opTimeout        = 30 * time.Second
)

var testDBCounter atomic.Uint64

// PrepareFunc 在新建的独立测试库上准备 schema 与种子；任一失败即测试失败。
type PrepareFunc func(ctx context.Context, db *sql.DB) error

// TestDatabase 是一个已创建的隔离测试库句柄。DSN 可注入子进程 env
// （JUHE_AI_POSTGRES_URL 等），DB 是已 ping 通过的连接池。
type TestDatabase struct {
	DB   *sql.DB
	DSN  string
	Name string
}

// New 创建隔离测试库、依次执行 prepare 并返回连接池；测试结束自动关闭并
// 删库。等价于 NewTestDatabase(t, prepares...).DB。
func New(t testing.TB, prepares ...PrepareFunc) *sql.DB {
	return NewTestDatabase(t, prepares...).DB
}

// NewTestDatabase 创建隔离测试库并返回完整句柄（含 DSN 与库名）。
func NewTestDatabase(t testing.TB, prepares ...PrepareFunc) *TestDatabase {
	t.Helper()
	adminDSN := resolveAdminDSN(t)
	name := newDatabaseName(t)
	createTestDatabase(t, adminDSN, name)

	appDSN := withDatabaseName(t, adminDSN, name)
	db, err := sql.Open("pgx", appDSN)
	if err != nil {
		t.Fatalf("pgtestkit: 打开测试库连接失败: %v", err)
	}
	pingCtx, pingCancel := context.WithTimeout(context.Background(), opTimeout)
	if err := db.PingContext(pingCtx); err != nil {
		pingCancel()
		_ = db.Close()
		dropTestDatabase(t, adminDSN, name)
		t.Fatalf("pgtestkit: 测试库 ping 失败: %v", err)
	}
	pingCancel()

	for i, prepare := range prepares {
		prepareCtx, prepareCancel := context.WithTimeout(context.Background(), opTimeout)
		if err := prepare(prepareCtx, db); err != nil {
			prepareCancel()
			_ = db.Close()
			dropTestDatabase(t, adminDSN, name)
			t.Fatalf("pgtestkit: prepare[%d] 失败: %v", i, err)
		}
		prepareCancel()
	}

	testDB := &TestDatabase{DB: db, DSN: appDSN, Name: name}
	t.Cleanup(func() {
		_ = db.Close()
		dropTestDatabase(t, adminDSN, name)
	})
	return testDB
}

// resolveAdminDSN 解析管理员连接串：显式 env 优先，缺失时回退 shared.env。
func resolveAdminDSN(t testing.TB) string {
	t.Helper()
	if raw := strings.TrimSpace(os.Getenv(EnvAdminURL)); raw != "" {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			t.Fatalf("pgtestkit: %s 不是合法连接串（内容不回显）", EnvAdminURL)
		}
		return withDatabaseName(t, raw, "postgres")
	}
	values, err := readSharedEnv(t)
	if err != nil {
		t.Fatalf("pgtestkit: %s 未配置且 shared.env 不可用（%v）。请设置 %s，或在仓库根准备 %s",
			EnvAdminURL, err, EnvAdminURL, sharedEnvRelative)
	}
	return adminDSNFromSharedEnv(t, values)
}

// adminDSNFromSharedEnv 以 shared.env 的管理员凭据与直连端口组装维护库连接串。
func adminDSNFromSharedEnv(t testing.TB, values map[string]string) string {
	t.Helper()
	base := strings.TrimSpace(values["JUHE_AI_POSTGRES_URL"])
	if base == "" {
		t.Fatalf("pgtestkit: shared.env 缺少 JUHE_AI_POSTGRES_URL，无法派生测试库管理连接")
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		t.Fatalf("pgtestkit: shared.env 的 JUHE_AI_POSTGRES_URL 不是合法连接串（内容不回显）")
	}
	username := strings.TrimSpace(values["DEV_POSTGRES_ADMIN_USERNAME"])
	password := strings.TrimSpace(values["DEV_POSTGRES_ADMIN_PASSWORD"])
	if username == "" || password == "" {
		t.Fatalf("pgtestkit: shared.env 缺少 DEV_POSTGRES_ADMIN_USERNAME/PASSWORD，无法组装测试库管理连接")
	}
	host := strings.TrimSpace(values["DEV_POSTGRES_HOST"])
	port := strings.TrimSpace(values["DEV_POSTGRES_DIRECT_PORT"])
	if host == "" {
		host = parsed.Hostname()
	}
	if port == "" {
		port = parsed.Port()
		if port == "6432" {
			port = "5432"
		}
	}
	query := parsed.Query()
	if query.Get("sslmode") == "" {
		query.Set("sslmode", "disable")
	}
	admin := &url.URL{
		Scheme:   parsed.Scheme,
		User:     url.UserPassword(username, password),
		Host:     joinHostPort(host, port),
		Path:     "/postgres",
		RawQuery: query.Encode(),
	}
	return admin.String()
}

// readSharedEnv 自当前目录向上查找并解析 shared.env 的 KEY=VALUE。
func readSharedEnv(t testing.TB) (map[string]string, error) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("读取工作目录失败: %w", err)
	}
	var envPath string
	for i := 0; i < 32; i++ {
		candidate := filepath.Join(dir, filepath.FromSlash(sharedEnvRelative))
		if _, statErr := os.Stat(candidate); statErr == nil {
			envPath = candidate
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if envPath == "" {
		return nil, fmt.Errorf("未找到 %s", sharedEnvRelative)
	}
	raw, err := os.ReadFile(envPath)
	if err != nil {
		return nil, err
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
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		values[key] = value
	}
	return values, nil
}

// newDatabaseName 生成唯一的临时测试库名（约定前缀 + 可读测试名段 + 进程与
// 计数后缀，总长受 PG 63 字节标识符上限约束）。
func newDatabaseName(t testing.TB) string {
	sanitized := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 'a' - 'A'
		default:
			return '_'
		}
	}, t.Name())
	if len(sanitized) > 20 {
		sanitized = sanitized[len(sanitized)-20:]
	}
	sanitized = strings.Trim(sanitized, "_")
	serial := testDBCounter.Add(1)
	name := testDBPrefix + sanitized + "_" + strconv.Itoa(os.Getpid()) + "_" + strconv.FormatUint(serial, 10)
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}

// createTestDatabase 在维护库连接上创建独立测试库。
func createTestDatabase(t testing.TB, adminDSN, name string) {
	t.Helper()
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatalf("pgtestkit: 打开管理连接失败: %v", err)
	}
	defer admin.Close()
	pingCtx, pingCancel := context.WithTimeout(context.Background(), adminPingTimeout)
	if err := admin.PingContext(pingCtx); err != nil {
		pingCancel()
		t.Fatalf("pgtestkit: 测试库管理连接不可达（检查开发 PG 或配置 %s；错误: %v）", EnvAdminURL, err)
	}
	pingCancel()
	createCtx, createCancel := context.WithTimeout(context.Background(), opTimeout)
	defer createCancel()
	if _, err := admin.ExecContext(createCtx, fmt.Sprintf(`CREATE DATABASE %s`, quoteIdent(name))); err != nil {
		t.Fatalf("pgtestkit: 创建测试库 %s 失败: %v", name, err)
	}
}

// dropTestDatabase 终止残余连接并删除测试库；删除失败按测试失败上报（开发
// 库约定临时子库用完必删，泄漏不可静默）。
func dropTestDatabase(t testing.TB, adminDSN, name string) {
	t.Helper()
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Errorf("pgtestkit: 删库阶段打开管理连接失败，测试库 %s 可能泄漏: %v", name, err)
		return
	}
	defer admin.Close()
	dropCtx, dropCancel := context.WithTimeout(context.Background(), opTimeout)
	defer dropCancel()
	if _, err := admin.ExecContext(dropCtx,
		`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, name); err != nil {
		t.Errorf("pgtestkit: 终止测试库 %s 残余连接失败: %v", name, err)
	}
	if _, err := admin.ExecContext(dropCtx, fmt.Sprintf(`DROP DATABASE %s`, quoteIdent(name))); err != nil {
		t.Errorf("pgtestkit: 删除测试库 %s 失败（泄漏需手工清理）: %v", name, err)
	}
}

// withDatabaseName 返回指向指定库名的连接串。
func withDatabaseName(t testing.TB, rawDSN, name string) string {
	t.Helper()
	parsed, err := url.Parse(rawDSN)
	if err != nil {
		t.Fatalf("pgtestkit: 连接串不是合法 URL（内容不回显）: %v", err)
	}
	parsed.Path = "/" + name
	return parsed.String()
}

// joinHostPort 处理 IPv6 字面量主机。
func joinHostPort(host, port string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}

// quoteIdent 防御性引用 PG 标识符（库名由本包生成，此处约束生成规则）。
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
