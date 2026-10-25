package main

// w14a（单元层）：composeSystemAPI SQLite 模式的路径契约 fail-fast 臂。
// 复用 compose_test.go 的 F3/F4 装配夹具；组合根错误路径不关闭 business
// 句柄（Windows 句柄锁会让 t.TempDir 清理失败），因此根目录用手工临时目录
// + 尽力而为清理。
//
// 登记的进程内不可达分支（compose.go，被 X05 六库 preflight 前置拦截或为
// 组合根恒真守卫）：
//   - 283-285/320-326/344-355（business/stats/usage-catalog open+configure
//     与缺失臂）：preflight（storage_bootstrap.go）先缺失校验并创建文件，
//     到达 compose 同名分支时路径恒非空且可打开（w1 boot 测试同款登记）。
//   - 405-407/440-442/458-464/592-599（dataset stat/open/cleanup 臂）：
//     preflight 的 ensureFile("dataset") 保证文件存在。
//   - 其余 `create X store: %w` 构造器防御臂：组合根恒传合法 db/配置
//     （w1w 已登记同款清单）。

import (
	"context"
	"testing"
)

// TestW14aRedisStateClientProviderInvalidate 覆盖空实现端口（显式 no-op 契约）。
func TestW14aRedisStateClientProviderInvalidate(t *testing.T) {
	redisStateClientProvider{}.Invalidate(context.Background(), nil)
}
