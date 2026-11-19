package mergesupermigration

import (
	"database/sql"
	"testing"
)

// TestDispatchFingerprintV2IgnoresClientIPLimit 钉住 2026-11-19 指纹 v2：
// 成员一致性只看 group_type + 单键 defaultSoftConcurrency；旧
// clientIpConcurrencyLimit 不参与指纹（已迁至 API Key）。
func TestDispatchFingerprintV2IgnoresClientIPLimit(t *testing.T) {
	fingerprint := func(policy string) string {
		t.Helper()
		got, err := dispatchFingerprint("personal", sql.NullString{String: policy, Valid: policy != ""})
		if err != nil {
			t.Fatalf("dispatchFingerprint(%q): %v", policy, err)
		}
		return got
	}
	a := fingerprint(`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":3}`)
	b := fingerprint(`{"defaultSoftConcurrency":5,"clientIpConcurrencyLimit":9}`)
	if a != b {
		t.Fatalf("仅 IP 限制不同不应改变指纹: %s vs %s", a, b)
	}
	c := fingerprint(`{"defaultSoftConcurrency":6,"clientIpConcurrencyLimit":3}`)
	if a == c {
		t.Fatal("阈值不同必须改变指纹")
	}
	// 单键 / 两键 / NULL 的 soft=-1 都归一为 NULL 语义。
	if fingerprint("") != fingerprint(`{"defaultSoftConcurrency":-1,"clientIpConcurrencyLimit":5}`) {
		t.Fatal("soft=-1 的单键/NULL/两键应归一为同一指纹")
	}
	if fingerprint(`{"defaultSoftConcurrency":-1}`) != fingerprint("") {
		t.Fatal("单键 -1 应等价 NULL")
	}
}
