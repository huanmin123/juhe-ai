package pgpool

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// ---------------------------------------------------------------------------
// 拒连地址臂：懒打开 + 生命周期 + 校验分支，不依赖任何外部服务。
// ---------------------------------------------------------------------------

func TestW7BRefusedAddressLifecycle(t *testing.T) {
	var nilRegistry *Registry
	if _, err := nilRegistry.Acquire("postgres://127.0.0.1:1/w", "gateway", 1, 1); err == nil {
		t.Fatal("nil registry 必须报错")
	}
	if err := nilRegistry.Close(); err != nil {
		t.Fatalf("nil registry Close = %v", err)
	}

	r := NewRegistry()
	refused := "postgres://127.0.0.1:1/w1cover?sslmode=disable&connect_timeout=2"
	first, err := r.Acquire(refused, "gateway", 2, 1)
	if err != nil {
		t.Fatalf("懒打开必须成功: %v", err)
	}
	second, err := r.Acquire(refused, "gateway", 4, 1)
	if err != nil {
		t.Fatalf("复用必须成功: %v", err)
	}
	if first.DB() != second.DB() {
		t.Fatal("同 URL/role 必须复用连接池")
	}
	if first.DB().Stats().MaxOpenConnections != 4 {
		t.Fatalf("复用必须抬升 maxOpen = %d", first.DB().Stats().MaxOpenConnections)
	}
	pingCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := first.DB().PingContext(pingCtx); err == nil {
		t.Fatal("拒连地址 Ping 必须失败")
	}
	// 校验分支： limits 非法 / 空 opener / nil registry 语义。
	if _, err := r.Acquire(refused, "gateway", 0, 0); err == nil {
		t.Fatal("非法 limits 必须报错")
	}
	if _, err := r.AcquireWith(nil, refused, "gateway", 2, 1); err == nil {
		t.Fatal("空 opener 必须报错")
	}
	if _, err := r.AcquireWith(func() (*sql.DB, error) { return nil, errors.New("w7b 注入打开失败") }, refused+"-open-fail", "gateway", 2, 1); err == nil {
		t.Fatal("opener 错误必须上抛")
	}
	if _, err := r.AcquireWith(func() (*sql.DB, error) { return nil, nil }, refused+"-open-nil", "gateway", 2, 1); err == nil {
		t.Fatal("opener 返回空库必须报错")
	}
	// 引用计数释放：两把句柄都关闭后池被移除。
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("句柄 Close 必须幂等: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("registry Close = %v", err)
	}
}

// ---------------------------------------------------------------------------
// 真实 PG 臂：dev env shared.env + 临时库 juhe_ai_sub2api_dev_w1cover，
// 不可达或无权限时登记证据并跳过。连接串只用于建池，不写入任何输出。
// ---------------------------------------------------------------------------

func w7bSharedEnvValue(t *testing.T, key string) string {
	t.Helper()
	if override := os.Getenv("JUHE_AI_W1COVER_POSTGRES_URL"); override != "" && key == "JUHE_AI_POSTGRES_URL" {
		return override
	}
	raw, err := os.ReadFile(`F:\sub2api-lite\.local\project-resources\dev\env\shared.env`)
	if err != nil {
		t.Skipf("dev env 不可读，真实 PG 用例跳过: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, key+"=") {
			return strings.TrimSpace(strings.TrimPrefix(line, key+"="))
		}
	}
	t.Skipf("shared.env 缺少 %s，真实 PG 用例跳过", key)
	return ""
}

func w7bWithDatabase(rawURL, database string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	parsed.Path = "/" + database
	return parsed.String()
}

func w7bPingQuick(t *testing.T, rawURL string) error {
	t.Helper()
	db, err := sql.Open("pgx", rawURL)
	if err != nil {
		return err
	}
	defer db.Close()
	pingCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	return db.PingContext(pingCtx)
}

func TestW7BRealPostgresPoolLifecycle(t *testing.T) {
	baseURL := w7bSharedEnvValue(t, "JUHE_AI_POSTGRES_URL")
	w1coverURL := w7bWithDatabase(baseURL, "juhe_ai_sub2api_dev_w1cover")
	if err := w7bPingQuick(t, w1coverURL); err != nil {
		// 临时库可能尚不存在：用直连管理端口补建，失败则跳过。
		directPort := w7bSharedEnvValue(t, "DEV_POSTGRES_DIRECT_PORT")
		adminUser := w7bSharedEnvValue(t, "DEV_POSTGRES_ADMIN_USERNAME")
		adminPassword := w7bSharedEnvValue(t, "DEV_POSTGRES_ADMIN_PASSWORD")
		adminBase := w7bWithDatabase(baseURL, "postgres")
		parsed, err := url.Parse(adminBase)
		if err != nil {
			t.Skipf("管理连接串解析失败，真实 PG 用例跳过: %v", err)
		}
		if parsed.Port() == "" && directPort != "" {
			parsed.Host = parsed.Hostname() + ":" + directPort
		}
		parsed.User = url.UserPassword(adminUser, adminPassword)
		admin, err := sql.Open("pgx", parsed.String())
		if err != nil {
			t.Skipf("管理连接打开失败，真实 PG 用例跳过: %v", err)
		}
		pingCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		err = admin.PingContext(pingCtx)
		cancel()
		if err != nil {
			t.Skipf("管理端口不可达，真实 PG 用例跳过: %v", err)
		}
		var exists bool
		if err := admin.QueryRowContext(context.Background(), `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname='juhe_ai_sub2api_dev_w1cover')`).Scan(&exists); err != nil {
			t.Skipf("查询临时库失败，真实 PG 用例跳过: %v", err)
		}
		if !exists {
			if _, err := admin.ExecContext(context.Background(), `CREATE DATABASE juhe_ai_sub2api_dev_w1cover`); err != nil {
				t.Skipf("创建临时库失败，真实 PG 用例跳过: %v", err)
			}
		}
		if err := w7bPingQuick(t, w1coverURL); err != nil {
			t.Skipf("临时库仍不可达，真实 PG 用例跳过: %v", err)
		}
	}

	r := NewRegistry()
	handle, err := r.Acquire(w1coverURL, "gateway", 4, 2)
	if err != nil {
		t.Fatalf("真实 PG Acquire = %v", err)
	}
	reuse, err := r.Acquire(w1coverURL, "gateway", 8, 2)
	if err != nil {
		t.Fatalf("真实 PG 复用 = %v", err)
	}
	if handle.DB() != reuse.DB() {
		t.Fatal("真实 PG 同 URL/role 必须复用")
	}
	pingCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := handle.DB().PingContext(pingCtx); err != nil {
		t.Fatalf("真实 PG Ping = %v", err)
	}
	if stats := handle.DB().Stats(); stats.MaxOpenConnections != 8 {
		t.Fatalf("复用抬升后 maxOpen = %d", stats.MaxOpenConnections)
	}
	// 真实查询往返。
	var one int
	if err := handle.DB().QueryRowContext(context.Background(), `SELECT 1`).Scan(&one); err != nil || one != 1 {
		t.Fatalf("SELECT 1 = %d err=%v", one, err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := reuse.Close(); err != nil {
		t.Fatal(err)
	}
	pingAfter := context.Background()
	if err := reuse.DB().PingContext(pingAfter); err == nil {
		t.Fatal("池关闭后 Ping 必须失败")
	}
	if err := r.Close(); err != nil {
		t.Fatalf("registry Close = %v", err)
	}
}
