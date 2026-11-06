package rediscfg

import (
	"strings"
	"testing"
)

// redisSlot 与 Redis Cluster 的 slot 计算一致：存在 hash tag 时只对
// `{...}` 内容做 CRC16，否则对整键计算，再 % 16384。
func redisSlot(key string) int {
	if start := strings.IndexByte(key, '{'); start >= 0 {
		if end := strings.IndexByte(key[start+1:], '}'); end > 0 {
			key = key[start+1 : start+1+end]
		}
	}
	const polynomial = 0x1021
	var crc uint16
	for i := 0; i < len(key); i++ {
		crc ^= uint16(key[i]) << 8
		for bit := 0; bit < 8; bit++ {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ polynomial
			} else {
				crc <<= 1
			}
		}
	}
	return int(crc % 16384)
}

// TestDomainKeysShareSlot 锁定域级 hash tag：同一域 namespace 下的任意键
// 必须落同一 slot，否则 Cluster 下多键 Lua/MGet 会 CROSSSLOT。
func TestDomainKeysShareSlot(t *testing.T) {
	domains := []string{
		DomainCircuit, DomainJ3B, DomainKeyModel, DomainHotQuality,
		DomainClientIP, DomainSession, DomainRuntimeCache, DomainRateLimit,
		DomainCallerTier, DomainQuota, DomainObs, DomainRouteState,
		DomainProxyHealth, DomainInval, DomainRealtime, DomainSpeedFirst,
		DomainOAuth, DomainAuthState, DomainSettings,
	}
	for _, domain := range domains {
		namespace := DomainNamespace("juhe-ai:prod", domain)
		keys := []string{
			"states", "due", "lock:account-1", "model:gpt-4o", "bucket:1.2.3.4",
			"v1:cache:domain:version", "index", "members:acct-9",
		}
		want := redisSlot(TaggedNamespacedKey(keys[0], namespace))
		for _, key := range keys[1:] {
			got := redisSlot(TaggedNamespacedKey(key, namespace))
			if got != want {
				t.Fatalf("domain %s key %q slot=%d want=%d", domain, key, got, want)
			}
		}
	}
}

// TestDomainNamespacesAreDistinct 确认域 namespace 不会互相吞并（j3b 与
// circuit 的 states 布局不兼容，必须落在不同键空间）。
func TestDomainNamespacesAreDistinct(t *testing.T) {
	for _, pair := range [][2]string{
		{DomainCircuit, DomainJ3B},
		{DomainKeyModel, DomainJ3B},
		{DomainSession, DomainRouteState},
	} {
		left := DomainNamespace("prod", pair[0])
		right := DomainNamespace("prod", pair[1])
		if left == right {
			t.Fatalf("domain namespaces collide: %q", left)
		}
		if TaggedNamespacedKey("states", left) == TaggedNamespacedKey("states", right) {
			t.Fatalf("domain keys collide: %s vs %s", left, right)
		}
	}
	if got := DomainNamespace(" juhe-ai:prod ", "circuit"); got != "prod:circuit" {
		t.Fatalf("DomainNamespace canonicalization = %q", got)
	}
}
