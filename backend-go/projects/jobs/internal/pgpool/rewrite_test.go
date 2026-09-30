package pgpool

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/sqldialect"
)

// 改写 driver 实现与完整 fake 能力矩阵自 2026-09-30（清理批次 C5）起收敛在
// shared/platform/sqldialect（driver.go / driver_test.go）；本文件只保留
// jobs 侧接线断言：openDriver 的 pgx 臂必须经 shared 改写 driver 打开，
// 非 pgx 臂原样透传。

// failingDriverCtx 的 OpenConnector 即报错：覆盖 openDriver 的错误分支。
type failingDriverCtx struct{ err error }

func (d *failingDriverCtx) Open(name string) (driver.Conn, error) { return nil, d.err }
func (d *failingDriverCtx) OpenConnector(string) (driver.Connector, error) {
	return nil, d.err
}

// stubDriver 只满足 sql.Register 的最小接口：Open 惰性不被调用。
type stubDriver struct{}

func (d *stubDriver) Open(name string) (driver.Conn, error) { return nil, nil }

func TestW20cOpenDriverSelectsRewriteForPgx(t *testing.T) {
	db, err := openDriver("pgx", "postgres://127.0.0.1:5432/unused")
	if err != nil {
		t.Fatalf("pgx 打开失败：%v", err)
	}
	if db == nil {
		t.Fatalf("pgx 路径应返回句柄")
	}
	if _, ok := db.Driver().(*sqldialect.Driver); !ok {
		t.Fatalf("pgx 臂必须经 sqldialect 改写 driver 打开：got %T", db.Driver())
	}
	db.Close()
}

func TestW20cOpenDriverConnectorErrorPropagates(t *testing.T) {
	original := defaultPGXDriver
	defaultPGXDriver = &failingDriverCtx{err: errors.New("connector rejected")}
	defer func() { defaultPGXDriver = original }()
	if db, err := openDriver("pgx", "dsn"); err == nil || db != nil {
		t.Fatalf("OpenConnector 错误应使 openDriver 失败：err=%v db=%v", err, db)
	}
}

func TestW20cOpenDriverNonPgxPassthrough(t *testing.T) {
	unique := "w20c-fake-driver"
	if existing := sql.Drivers(); !containsString(existing, unique) {
		sql.Register(unique, &stubDriver{})
	}
	fakeDB, err := openDriver(unique, "")
	if err != nil {
		t.Fatalf("非 pgx driver 打开失败：%v", err)
	}
	if fakeDB == nil {
		t.Fatalf("非 pgx 路径应返回句柄")
	}
	if _, wrapped := fakeDB.Driver().(*sqldialect.Driver); wrapped {
		t.Fatalf("非 pgx 路径不应套改写 driver：%T", fakeDB.Driver())
	}
	fakeDB.Close()
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
