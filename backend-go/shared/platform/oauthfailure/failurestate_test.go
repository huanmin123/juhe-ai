package oauthfailure

import (
	"strings"
	"testing"
)

func TestKeyShapeMatchesJobsHistoricalPrefix(t *testing.T) {
	const accountID = "acc_123"
	legacy := Key("", accountID)
	if !strings.HasPrefix(legacy, OpenAIKeyPrefix) {
		t.Fatalf("legacy key=%q, want prefix %q", legacy, OpenAIKeyPrefix)
	}
	if got := strings.TrimPrefix(legacy, OpenAIKeyPrefix); got != KeySuffix(accountID) {
		t.Fatalf("legacy suffix=%q, want %q", got, KeySuffix(accountID))
	}
	namespaced := Key("dev:oauth", accountID)
	if !strings.HasPrefix(namespaced, "juhe-ai:{dev:oauth}:state:openai-oauth-refresh-failure:") {
		t.Fatalf("namespaced key=%q", namespaced)
	}
	if !strings.HasSuffix(namespaced, KeySuffix(accountID)) {
		t.Fatalf("namespaced key=%q missing suffix", namespaced)
	}
	if got := KeyForVendor("gpt", "dev:oauth", accountID); got != namespaced {
		t.Fatalf("gpt key=%q, want %q", got, namespaced)
	}
	for _, vendor := range []string{"anthropic", "gemini", "xai"} {
		if got := KeyForVendor(vendor, "dev:oauth", accountID); !strings.HasPrefix(got, "juhe-ai:{dev:oauth}:state:oauth-keepalive-failure:") {
			t.Fatalf("%s key=%q, want keepalive prefix", vendor, got)
		}
	}
}

func TestActiveBackoffRevisionAndExpiryGuards(t *testing.T) {
	const raw = `{"count":2,"localConfigurationCount":0,"backoffUntil":2000,"configRevision":3}`
	if until, active := ActiveBackoff(raw, 1000, 3); !active || until != 2000 {
		t.Fatalf("active = (%d,%v), want (2000,true)", until, active)
	}
	if _, active := ActiveBackoff(raw, 2000, 3); active {
		t.Fatal("退避到期不得继续阻断")
	}
	if _, active := ActiveBackoff(raw, 1000, 4); active {
		t.Fatal("凭据 revision 已前进时旧退避必须失效")
	}
	if _, active := ActiveBackoff(raw, 1000, 2); active {
		t.Fatal("存储 revision 新于调用方快照时不得阻断")
	}
	if _, active := ActiveBackoff(`{bad`, 1000, 3); active {
		t.Fatal("非法 JSON 不得当成有效退避")
	}
	if _, active := ActiveBackoff("", 1000, 3); active {
		t.Fatal("空记录不得阻断")
	}
	if _, active := ActiveBackoff(`{"backoffUntil":2000}`, 1000, 1); active {
		t.Fatal("缺失 revision 的记录不得阻断")
	}
}
