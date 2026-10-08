package oauthrefresh

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamidentity"
)

// 出站实效（设计 §10）：geminiCLI 族自动覆盖生效后，生产 Drive 配额探针
// 经出站传输边界携带的 user-agent 必须为覆盖值。复用出站请求捕获基建
// （ExchangerFunc 捕获 TokenHTTPRequest，同 TestGoogleOneDriveQuotaProberCarriesProxyURL）。
func TestGoogleOneDriveQuotaProberUsesAutoOverrideUserAgent(t *testing.T) {
	t.Cleanup(func() {
		upstreamidentity.SetClientVersionOverrides(nil)
		upstreamidentity.SetClientVersionAutoOverrides(nil)
	})
	upstreamidentity.SetClientVersionAutoOverrides(map[string]string{"geminiCLI": "9.9.9"})

	var seen TokenHTTPRequest
	exchanger := ExchangerFunc(func(_ context.Context, request TokenHTTPRequest) (TokenHTTPResponse, error) {
		seen = request
		return TokenHTTPResponse{StatusCode: 200, Body: `{"storageQuota":{"limit":1024,"usage":10}}`}, nil
	})
	prober := newGoogleOneDriveQuotaProber(exchanger, "")
	quota, err := prober.ProbeDriveQuota(context.Background(), "at-1")
	if err != nil {
		t.Fatal(err)
	}
	if quota == nil || quota.Limit != 1024 || quota.Usage != 10 {
		t.Fatalf("quota=%+v", quota)
	}
	wantUA := "GeminiCLI/9.9.9 (Windows; AMD64)"
	if got := seen.Headers["user-agent"]; got != wantUA {
		t.Fatalf("probe outbound user-agent=%q, want %q", got, wantUA)
	}
}
