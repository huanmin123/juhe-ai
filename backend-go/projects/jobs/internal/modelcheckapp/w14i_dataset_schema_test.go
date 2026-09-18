package modelcheckapp

import (
	"context"
	"os"
	"strings"
	"testing"
)

// w14i_dataset_schema_test.go 覆盖 dataset EnsureSchema 失败臂：dataset 库
// 文件损坏（非 SQLite 头）时装配必须 fail-closed 并回收全部连接。
//
// w14i 波次不可达清单（沿用 w12a 登记，均已核对）：
//   - host.go 74-77 / 98-101 / 103-106：sql.Open 惰性连接，DSN 在 Open 阶段
//     不解析，构造器错误臂不可达。
//   - host.go 87-90 / 117-120：modelcheckpolicy 读取器构造器只在校验入参时
//     出错，OpenHost 传入的连接与密钥已在上游校验，不可达。
//   - host.go 139-142：modelcheckauth.New 仅在 db==nil 或 mode 非法时报错；
//     OpenHost 的连接非 nil 且 mode 为编译期常量，不可达。
//   - host.go 157-164：sourceAny.(modelcheckexecutor.TargetResolver) 是函数
//     类型断言（要求动态类型完全一致），reader 为结构体指针，断言按契约恒
//     失败（ wo 波用例已锁定该行为），其后的 Service/Handler 组装与成功
//     返回语句在当前装配契约下不可达，且按任务边界不得改动该断言。

func TestW14iOpenHostSQLiteFailsWhenDatasetFileCorrupt(t *testing.T) {
	// 契约：dataset 库文件损坏时，EnsureSchema 阶段必须失败并回收已打开的
	// durable 连接，错误应指向 dataset schema 校验。
	cfg := validSQLiteConfig(t)
	if err := os.WriteFile(cfg.DatasetDatabasePath, []byte("w14i-not-a-sqlite-file-at-all-0000000000000000"), 0o600); err != nil {
		t.Fatalf("写入损坏 dataset 文件失败: %v", err)
	}
	prepareSQLiteBusinessDB(t, cfg.BusinessDatabasePath, sqliteBusinessFixtureSchema)
	host, err := OpenHost(context.Background(), cfg)
	if err == nil {
		_ = host.Close()
		t.Fatalf("dataset 文件损坏时 OpenHost 应报错")
	}
	if host != nil {
		t.Fatalf("失败路径应返回 nil Host")
	}
	if !strings.Contains(err.Error(), "verify J3b dataset schema") {
		t.Fatalf("错误应指向 dataset schema 校验，实际: %v", err)
	}
}
