package proxyresolve

import "testing"

func TestMemberHealthKey(t *testing.T) {
	if got, want := MemberHealthKey("dev", "proxy-1"), "juhe-ai:{dev}:proxy-pool:member-health:proxy-1"; got != want {
		t.Fatalf("MemberHealthKey = %q, want %q", got, want)
	}
	if got := MemberHealthKey("", "proxy-1"); got != "" {
		t.Fatalf("empty namespace key = %q, want empty", got)
	}
	if got := MemberHealthKey("dev", "  "); got != "" {
		t.Fatalf("empty profile key = %q, want empty", got)
	}
}
