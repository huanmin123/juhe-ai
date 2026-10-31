package cleanuprepo

import (
	"strings"
	"testing"

)

// 第二轮收尾：deleteaccount 实例授权富场景、account 清理链阶段、杂项死函数。
//（CleanupRetention PG 全路径覆盖已随 chat_* 清理链退役移除。）

func TestMiscTrailingHelpers(t *testing.T) {
	if got := cleanupPendingReason(true, false); !strings.Contains(got, "已被统计安全游标覆盖") {
		t.Fatalf("cleanupPendingReason(true,false) = %q", got)
	}
}
