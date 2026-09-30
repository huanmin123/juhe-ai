package pgpool

import (
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/sqldialect"
)

// 改写 driver 实现与完整 fake 能力矩阵自 2026-09-30（清理批次 C5）起收敛在
// shared/platform/sqldialect（driver.go / driver_test.go）；本文件只保留
// gateway 侧接线断言：openPGX 必须经 shared 改写 driver 打开，注入点可用。

// failingDriverCtx 的 OpenConnector 即报错：覆盖 openPGX 的错误分支。
type failingDriverCtx struct{ err error }

func (d *failingDriverCtx) Open(name string) (driver.Conn, error) { return nil, d.err }
func (d *failingDriverCtx) OpenConnector(string) (driver.Connector, error) {
	return nil, d.err
}

func TestW0219OpenPGXRoutesThroughSqldialectRewrite(t *testing.T) {
	// 真实 pgx driver 惰性打开（不触网），Driver() 必须是 shared 改写包装。
	db, err := openPGX("postgres://127.0.0.1:1/w0219?sslmode=disable")
	if err != nil {
		t.Fatalf("openPGX 惰性打开失败：%v", err)
	}
	if _, ok := db.Driver().(*sqldialect.Driver); !ok {
		t.Fatalf("openPGX 必须经 sqldialect 改写 driver 打开：got %T", db.Driver())
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭失败：%v", err)
	}
}

func TestW0219OpenPGXConnectorErrorPropagates(t *testing.T) {
	original := defaultPGXDriver
	defaultPGXDriver = &failingDriverCtx{err: errors.New("connector rejected")}
	defer func() { defaultPGXDriver = original }()
	if db, err := openPGX("dsn"); err == nil || db != nil {
		t.Fatalf("OpenConnector 错误应使 openPGX 失败：err=%v db=%v", err, db)
	}
}
