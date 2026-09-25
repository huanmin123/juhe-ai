package gatewayusage

import "testing"

// 2026-09-25 凭据脱敏契约（usage 快照面与日志面共用 SanitizeURLForLog）：
// Gemini native 的 `?key=` 载体与 token 类 query 不得随 originalUrl 明文
// 落快照；path、query 顺序与非凭据参数保留诊断价值；无凭据时字节原样。
func TestSanitizeURLForLogCredentialQueryMasked(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"gemini key", "/v1/gemini/models?key=sk-xxx", "/v1/gemini/models?key=[redacted]"},
		{"multi param order kept", "/v1/gemini/m?key=sk-xxx&alt=sse&b=2", "/v1/gemini/m?key=[redacted]&alt=sse&b=2"},
		{"unsorted rest untouched", "/v1/x?z=3&a=1&token=t-1", "/v1/x?z=3&a=1&token=[redacted]"},
		{"access_token", "/v1/y?access_token=at-1&x=1", "/v1/y?access_token=[redacted]&x=1"},
		{"refresh_token", "/v1/y?refresh_token=rt-1", "/v1/y?refresh_token=[redacted]"},
		{"apikey variants", "/v1/y?apikey=v&api_key=v2&api-key=v3", "/v1/y?apikey=[redacted]&api_key=[redacted]&api-key=[redacted]"},
		{"name case insensitive", "/v1/y?KEY=up", "/v1/y?KEY=[redacted]"},
		{"encoded name", "/v1/y?%61pi_key=v", "/v1/y?%61pi_key=[redacted]"},
		{"bare param untouched", "/v1/y?key", "/v1/y?key"},
		{"empty query", "/v1/y?", "/v1/y?"},
		{"no query", "/v1/y", "/v1/y"},
	}
	for _, testCase := range cases {
		if got := SanitizeURLForLog(testCase.in); got != testCase.want {
			t.Fatalf("%s: SanitizeURLForLog(%q) = %q, want %q", testCase.name, testCase.in, got, testCase.want)
		}
	}
}

// TestSanitizeURLForLogOAuthSemanticsUnchanged 固定 /oauth 分支原有语义不回归：
// 非敏感名保留、敏感名 [redacted]、仅 path+query 输出。
func TestSanitizeURLForLogOAuthSemanticsUnchanged(t *testing.T) {
	sanitized := SanitizeURLForLog("/oauth/authorize?client_id=abc&state=xyz&nonce=n-1")
	if sanitized != "/oauth/authorize?client_id=abc&nonce=%5Bredacted%5D&state=%5Bredacted%5D" {
		t.Fatalf("oauth authorize 语义变化: %q", sanitized)
	}
	device := SanitizeURLForLog("/oauth/device?user_code=top-secret")
	if device != "/oauth/device?user_code=%5Bredacted%5D" {
		t.Fatalf("oauth device 语义变化: %q", device)
	}
	oauthKey := SanitizeURLForLog("/oauth/authorize?key=sk-xxx&state=xyz")
	if oauthKey != "/oauth/authorize?key=%5Bredacted%5D&state=%5Bredacted%5D" {
		t.Fatalf("oauth 路径凭据名未掩码: %q", oauthKey)
	}
}

// TestBuildUsageRequestSnapshotMaskedOriginalURL 固定 usage 请求快照面的
// originalUrl 落库契约：凭据 query 落快照前掩码（新写入；历史行不回填）。
func TestBuildUsageRequestSnapshotMaskedOriginalURL(t *testing.T) {
	snapshot := BuildUsageRequestSnapshot(BuildUsageRequestSnapshotInput{
		Method:      "POST",
		Path:        "/v1/gemini/models",
		OriginalURL: "/v1/gemini/models?key=sk-xxx&alt=sse",
		TraceID:     "t1",
	})
	if snapshot.OriginalURL != "/v1/gemini/models?key=[redacted]&alt=sse" {
		t.Fatalf("快照 originalUrl 未脱敏: %q", snapshot.OriginalURL)
	}
	if snapshot.Path != "/v1/gemini/models" {
		t.Fatalf("快照 path 必须原样: %q", snapshot.Path)
	}
	plain := BuildUsageRequestSnapshot(BuildUsageRequestSnapshotInput{
		Method:      "POST",
		Path:        "/v1/x",
		OriginalURL: "/v1/x?b=2&a=1",
		TraceID:     "t1",
	})
	if plain.OriginalURL != "/v1/x?b=2&a=1" {
		t.Fatalf("无凭据快照 originalUrl 必须原样: %q", plain.OriginalURL)
	}
}
