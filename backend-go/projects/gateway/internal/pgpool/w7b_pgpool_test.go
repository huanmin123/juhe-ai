package pgpool

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
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
	refused := "postgres://127.0.0.1:1/w7b-refused?sslmode=disable&connect_timeout=2"
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
// 真实 PG 臂：pgtestkit 每测试独立临时库（原 shared.env + 固定共享覆盖库
// 装配已退役）；PG 不可达由 pgtestkit 显式 Fatal 而非 skip。连接串只用于
// 建池，不写入任何输出；隔离库随测试结束删除。
// ---------------------------------------------------------------------------

func TestW7BRealPostgresPoolLifecycle(t *testing.T) {
	testDB := pgtestkit.NewTestDatabase(t)

	r := NewRegistry()
	handle, err := r.Acquire(testDB.DSN, "gateway", 4, 2)
	if err != nil {
		t.Fatalf("真实 PG Acquire = %v", err)
	}
	reuse, err := r.Acquire(testDB.DSN, "gateway", 8, 2)
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
	// w0219 防御性集成断言：经 gateway pgpool 打开的连接执行含裸 `?` 的
	// 查询，驱动层改写（rewrite.go）应把它改写为 $1 并在真实 PG 上正确执行
	// ——遗漏 bind() 的执行点不再报 42601 的端到端证明。
	var marker string
	if err := handle.DB().QueryRowContext(context.Background(), `SELECT ?::text`, "w0219-rewrite").Scan(&marker); err != nil || marker != "w0219-rewrite" {
		t.Fatalf("裸 ? 查询经驱动层改写应成功: marker=%q err=%v", marker, err)
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
