package operationlog

import "testing"

func TestLeaseKeyForInstance(t *testing.T) {
	if got := LeaseKeyForInstance(""); got != DefaultLeaseKey {
		t.Fatalf("empty instance = %q, want %q", got, DefaultLeaseKey)
	}
	if got := LeaseKeyForInstance(" gw-a "); got != DefaultLeaseKey+":gw-a" {
		t.Fatalf("instance key = %q", got)
	}
	if LeaseKeyForInstance("gw-a") == LeaseKeyForInstance("gw-b") {
		t.Fatal("不同实例必须使用不同写入 lease 键")
	}
}
