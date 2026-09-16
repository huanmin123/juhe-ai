package modelcheckresolver

import (
	"testing"
	"time"
)

// timeoutFor / maxResponseBytesFor 的显式配置分支（w9h 未触达）。
func TestW10ETimeoutAndResponseBytesDefaults(t *testing.T) {
	if got := timeoutFor(Snapshot{Timeout: 3 * time.Second}); got != 3*time.Second {
		t.Fatalf("timeoutFor 显式配置 = %v", got)
	}
	if got := timeoutFor(Snapshot{}); got <= 0 {
		t.Fatalf("timeoutFor 缺省应返回正值，实际 %v", got)
	}
	if got := maxResponseBytesFor(Snapshot{MaxResponseBytes: 4096}); got != 4096 {
		t.Fatalf("maxResponseBytesFor 显式配置 = %v", got)
	}
	if got := maxResponseBytesFor(Snapshot{}); got <= 0 {
		t.Fatalf("maxResponseBytesFor 缺省应返回正值，实际 %v", got)
	}
}
