package sqlpool

import (
	"strings"
	"testing"
)

// 2026-09-25：Registry.Close 的错误消息不得内嵌完整连接串（可能带
// user:password），只保留 role + host[:port]。Close 本体的 db.Close() 错误
// 难以注入，这里直接锁定脱敏 helper 的契约——错误格式化点仅此一处。
func TestSanitizedURLErrorStripsCredentials(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"with credentials and port", "postgres://app:secret@db.local:5432/juhe_ai", "db.local:5432"},
		{"with credentials no port", "postgres://app:secret@db.local/juhe_ai", "db.local"},
		{"ipv6 host", "postgres://app:secret@[::1]:5432/x", "[::1]:5432"},
		{"no host", "postgres://app:secret@/x", "[no-host]"},
		{"unparsable", "http://[::bad", "[unparsed]"},
	}
	for _, testCase := range cases {
		if got := sanitizedURLForError(testCase.in); got != testCase.want {
			t.Fatalf("%s: sanitizedURLForError(%q) = %q, want %q", testCase.name, testCase.in, got, testCase.want)
		}
	}
	sanitized := sanitizedURLForError("postgres://app:secret@db.local:5432/juhe_ai")
	if strings.Contains(sanitized, "secret") || strings.Contains(sanitized, "app:") {
		t.Fatalf("脱敏结果泄漏 userinfo: %q", sanitized)
	}
}
