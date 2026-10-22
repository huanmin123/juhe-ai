package gatewayruntimecache

import "testing"

// cacheDriver=redis 的组合根必须显式声明 Shared；漏接时启动 fail-fast，
// 不允许静默退回进程内 LRU。
func TestNewRequiresSharedWhenRedisDriver(t *testing.T) {
	if _, err := New(newFakeModels(), Options{RequireShared: true}); err == nil {
		t.Fatal("RequireShared=true 且 Shared=nil 必须报错")
	}
	if _, err := New(newFakeModels(), Options{RequireShared: true, Shared: newFakeSharedFactory()}); err != nil {
		t.Fatalf("RequireShared=true 且 Shared 已接线不应报错: %v", err)
	}
}
