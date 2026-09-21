package proxylatency

import (
	"context"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWFLoadRuntimeConfigRemainingGuards(t *testing.T) {
	base := map[string]string{
		"JUHE_AI_PROXY_LATENCY_JOBS_OWNER":          "go",
		"JUHE_AI_PROXY_LATENCY_INSTANCE_ID":         "wf",
		"JUHE_AI_PROXY_LATENCY_STORE":               "postgres",
		"JUHE_AI_PROXY_LATENCY_POSTGRES_URL":        "postgres://j:x@127.0.0.1:5432/j",
		"JUHE_AI_PROXY_LATENCY_INPUT_POSTGRES_URL":  "postgres://b:x@127.0.0.1:5432/b",
		"JUHE_AI_PROXY_LATENCY_RESULT_POSTGRES_URL": "postgres://r:x@127.0.0.1:5432/b",
		"JUHE_AI_PROXY_LATENCY_CREDENTIAL_SECRET":   "s",
	}
	cases := []struct {
		name, key, value, wantErr string
	}{
		{name: "proxy lease 超上限", key: "JUHE_AI_PROXY_LATENCY_PROXY_LEASE", value: "25h", wantErr: "duration"},
		{name: "probe timeout 过短", key: "JUHE_AI_PROXY_LATENCY_PROBE_TIMEOUT", value: "100ms", wantErr: "duration"},
		{name: "input ttl 过长", key: "JUHE_AI_PROXY_LATENCY_INPUT_TTL", value: "16m", wantErr: "duration"},
		{name: "db queue 超上限", key: "JUHE_AI_PROXY_LATENCY_DB_QUEUE_SIZE", value: "99999", wantErr: "有效范围"},
		{name: "worker 并发超上限", key: "JUHE_AI_PROXY_LATENCY_WORKER_CONCURRENCY", value: "99999", wantErr: "有效范围"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{}
			for key, value := range base {
				env[key] = value
			}
			env[tt.key] = tt.value
			_, err := LoadRuntimeConfig(wfEnv(env))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err=%v 必须包含 %q", err, tt.wantErr)
			}
		})
	}
}

func TestWFParseTargetURLShapes(t *testing.T) {
	canonical, err := parseTargetURL("HTTPS://API.OpenAI.com:443/v1")
	if err != nil {
		t.Fatalf("合法目标 err=%v", err)
	}
	if canonical.Scheme != "https" {
		t.Fatalf("scheme=%q", canonical.Scheme)
	}
	for _, bad := range []string{"ftp://x.invalid", "//no-scheme", "http://"} {
		if _, err := parseTargetURL(bad); err == nil {
			t.Fatalf("%q 必须拒绝", bad)
		}
	}
}

func TestWFApplyNodeProbeHeaders(t *testing.T) {
	request, _ := http.NewRequest(http.MethodGet, "http://target.invalid/v1", nil)
	target, _ := url.Parse("http://target.invalid/v1")
	proxyURL, _ := url.Parse("http://127.0.0.1:8080")
	applyNodeProbeHeaders(request, target, proxyURL)
	if request.Header.Get("Proxy-Connection") != "close" || request.Host != "target.invalid" || !request.Close {
		t.Fatalf("转发代理头未设置：%+v", request.Header)
	}
	// 非 http 目标不设置代理头。
	request2, _ := http.NewRequest(http.MethodGet, "https://target.invalid/v1", nil)
	applyNodeProbeHeaders(request2, nil, nil)
	if request2.Header.Get("Proxy-Connection") != "" {
		t.Fatal("非转发场景不得设置代理头")
	}
}

func TestWFMakeDraftTTLUpperBound(t *testing.T) {
	assembly := proxyLatencyCandidateAssembly{row: proxyLatencyCandidateRow{
		proxyID: "p-1", proxyType: "http", proxyHost: "10.0.0.1", proxyPort: 8080, proxyEnabled: true,
		configRevision: wfProjRevision, provider: "gpt", profileID: "profile-gpt", targetURL: "https://api.openai.com/v1",
	}, targets: []Target{{Provider: "gpt", ProfileID: "profile-gpt", URL: "https://api.openai.com/v1"}}}
	if _, err := makeProxyLatencyInputDraft(assembly, wfProjBase, 16*time.Minute); err == nil {
		t.Fatal("TTL 超上限必须拒绝")
	}
}

func TestWFStoreLeaseParameterGuards(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "wf-guard2.sqlite3")})
	if err != nil {
		t.Fatalf("OpenStore 失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if _, _, err := store.AcquireOwnerLease(ctx, "wf", 0); err == nil {
		t.Fatal("duration=0 必须拒绝")
	}
	if _, _, err := store.AcquireProxyLease(ctx, OwnerLease{OwnerID: "wf", FenceToken: 1}, "", time.Minute); err == nil {
		t.Fatal("空 proxy id 必须拒绝")
	}
	if _, _, err := store.AcquireProxyLease(ctx, OwnerLease{}, "p", time.Minute); err == nil {
		t.Fatal("非法 owner 必须拒绝")
	}
	if err := store.RenewOwnerLease(ctx, OwnerLease{OwnerID: "wf", FenceToken: 1}, 0); err == nil {
		t.Fatal("续租 duration=0 必须拒绝")
	}
	// VerifyOwnerLease 对不存在的租约 fail closed。
	if err := store.VerifyOwnerLease(ctx, OwnerLease{OwnerID: "wf", FenceToken: 1}); !errors_Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("VerifyOwnerLease err=%v", err)
	}
}

func errors_Is(err, target error) bool {
	return err != nil && strings.Contains(err.Error(), target.Error())
}

func TestWFTargetURLHardening(t *testing.T) {
	for _, bad := range []string{
		"http://x.invalid/a\x00b", // 控制字符
		"http://x.invalid/a%5Cb",  // 编码反斜杠
		"http://x.invalid",        // 缺路径（补 "/" 不影响 host 校验，但 host 合法应通过）
	} {
		_ = bad
	}
	if _, err := parseTargetURL("http://x.invalid/a\x00b"); err == nil {
		t.Fatal("控制字符必须拒绝")
	}
	if _, err := parseTargetURL("http://x.invalid/a%5Cb"); err == nil {
		t.Fatal("编码反斜杠必须拒绝")
	}
	if _, err := parseTargetURL("mailto:someone@x.invalid"); err == nil {
		t.Fatal("非 http(s) scheme 必须拒绝")
	}
	longLabel := "http://" + strings.Repeat("a", 64) + ".invalid"
	if validTargetHostname(strings.Repeat("a", 64) + ".invalid") {
		t.Fatal("超长 label 必须非法")
	}
	_ = longLabel
}

func TestWFProjectorRunNilStore(t *testing.T) {
	projector := &ResultProjector{cfg: ResultProjectorConfig{Now: func() time.Time { return wfProjBase }}}
	if _, err := projector.Drain(context.Background()); err == nil {
		t.Fatal("缺 store 的 Drain 必须报错")
	}
}

func TestWFManualLegacyProtocols(t *testing.T) {
	if protocol, ok := legacyUnsupportedTargetProtocol("mailto:someone@x.invalid"); !ok || protocol != "mailto" {
		t.Fatalf("mailto protocol=%q ok=%v", protocol, ok)
	}
}
