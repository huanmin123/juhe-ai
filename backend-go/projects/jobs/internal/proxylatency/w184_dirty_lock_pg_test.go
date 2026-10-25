package proxylatency

// w184_dirty_lock_pg_test.go（录制契约部分）用录制驱动锁定生产代码契约：
// PG 方言下凡是将要 UPDATE proxy_profiles 的投影事务（projectStored /
// ProjectManualNoTargets / ProjectManualOutbound），advisory 7001001 必须先于
// receipt/FOR UPDATE 行语句与 CAS UPDATE 出现；SQLite 方言不产生 advisory。
//
// 历史注记（PLAN-20261017T035500000Z）：本文件原有的真实 PG 死锁复现门禁
// （TestW184ProxyDirtyAdvisoryLockOrderOnW1Cover，BUG-0237 三方死锁环修复前/
// 修复后双阶段复现）随 account_list_availability_proxies 触发器移除而失去
// 复现前提（proxy UPDATE 不再经触发器中途取 7001001），已一并拆除；J1 侧
// 双键锁序契约仍由 accounthealth/w184_projection_lockorder_test.go 的录制
// 测试锁定。J3a 事务保留先取 7001001（保守串行化 + 事务级等待有界），是否
// 可随触发器移除一并撤销见问题-0313 后续记录跟进项。

import (
	"strings"
)

// w184StatementIndex 返回录制流中第一条包含 match 的语句下标（-1 未找到）。
func w184StatementIndex(statements []wfStatement, match string) int {
	for index, statement := range statements {
		if strings.Contains(statement.query, match) {
			return index
		}
	}
	return -1
}

