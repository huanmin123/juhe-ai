package accountprobe

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamidentity"
)

// resetProbeIdentityOverrides 清空两层客户端版本覆盖（进程级 atomic，测试间
// 共享），避免覆盖状态泄漏到同包其他测试。
func resetProbeIdentityOverrides(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		upstreamidentity.SetClientVersionOverrides(nil)
		upstreamidentity.SetClientVersionAutoOverrides(nil)
	})
}

// anthropicOAuthProbeView 构造 Anthropic OAuth 账户视图（probeView 的
// anthropic 变体，身份判定输入与 executeAttempt 同口径）。
func anthropicOAuthProbeView(baseURL string) *View {
	view := probeView(baseURL)
	view.Type = "oauth"
	view.ProviderCode = "anthropic"
	view.ProtocolCode = "anthropic"
	view.HealthCheckModel = "claude-sonnet-4-5"
	view.HealthCheckEndpointMode = string(ModeMessagesJSON)
	view.SupportedModels = []string{"claude-sonnet-4-5"}
	view.NormalizeEndpointModes = map[EndpointMode]bool{ModeMessagesJSON: true, ModeMessagesSSE: true}
	view.SelectedAPIKey = "oauth-access-token"
	view.APIKeyEntries = nil
	view.FixedKey = nil
	return view
}

// probeAnthropicIdentity 构造一次 Anthropic OAuth 探针请求（body 经快照变体
// 入口、身份头经 applyProbeIdentityHeaders，与 executeAttempt 同一接缝），
// 返回 body cc_version 版本段与请求头 User-Agent。
func probeAnthropicIdentity(t *testing.T, identitySnapshot upstreamidentity.Snapshot) (ccVersion, userAgent string) {
	t.Helper()
	view := anthropicOAuthProbeView("https://anthropic.test")
	request, err := buildTestRequestWithIdentity(context.Background(), view, ModeMessagesJSON, "claude-sonnet-4-5", OutputChallenge{ExpectedOutput: "juhe", Prompt: "ping"}, "oauth-access-token", identitySnapshot.ClaudeCodeVersion())
	if err != nil {
		t.Fatalf("buildTestRequestWithIdentity() err=%v", err)
	}
	applyProbeIdentityHeaders(request.Header, probeIdentityInput(view, request), identitySnapshot)
	payload, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		System []struct {
			Text string `json:"text"`
		} `json:"system"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if len(body.System) == 0 {
		t.Fatal("payload system block is empty")
	}
	// cc_version=<版本>.<buildID>；版本自身含点号，按「.buildID」切出完整版本段。
	_, rest, _ := strings.Cut(body.System[0].Text, "cc_version=")
	ccVersion, _, _ = strings.Cut(rest, "."+anthropicBuildID)
	if ccVersion == "" {
		t.Fatalf("system[0].text has no cc_version: %q", body.System[0].Text)
	}
	userAgent = request.Header.Get("User-Agent")
	return ccVersion, userAgent
}

// assertAnthropicUAMatchesBody 断言 UA 是 body cc_version 的 claude-cli cli
// 变体（同一次请求构造的两个身份值必须同源）。
func assertAnthropicUAMatchesBody(t *testing.T, ccVersion, userAgent string) {
	t.Helper()
	want := "claude-cli/" + ccVersion + " (external, cli)"
	if userAgent != want {
		t.Fatalf("request User-Agent %q must be the same-source claude-cli variant of body cc_version %q (want %q)", userAgent, ccVersion, want)
	}
}

// 请求内身份快照（2026-11-06）：Anthropic OAuth 探针的头 UA 与 body
// cc_version 必须同源于同一份快照。手动覆盖生效时两处携带覆盖版本；清空
// 两层后回退内置拼接值，两处仍一致。
func TestAnthropicProbeUserAgentMatchesBodyCCVersion(t *testing.T) {
	resetProbeIdentityOverrides(t)
	upstreamidentity.SetClientVersionOverrides(map[string]string{"claudeCode": "2.2.0"})
	upstreamidentity.SetClientVersionAutoOverrides(map[string]string{"claudeCode": "1.9.0"})
	ccVersion, userAgent := probeAnthropicIdentity(t, upstreamidentity.NewSnapshot())
	assertAnthropicUAMatchesBody(t, ccVersion, userAgent)
	if ccVersion != "2.2.0" {
		t.Fatalf("cc_version=%q, want manual override 2.2.0 (manual > auto > builtin)", ccVersion)
	}

	upstreamidentity.SetClientVersionOverrides(nil)
	upstreamidentity.SetClientVersionAutoOverrides(nil)
	ccVersion, userAgent = probeAnthropicIdentity(t, upstreamidentity.NewSnapshot())
	assertAnthropicUAMatchesBody(t, ccVersion, userAgent)
	if ccVersion != upstreamidentity.BuiltInClientVersion("claudeCode") {
		t.Fatalf("cc_version after clearing=%q, want builtin %q", ccVersion, upstreamidentity.BuiltInClientVersion("claudeCode"))
	}
}

// 并发热刷新下同请求两处身份值不再被拆散：一个 goroutine 持续替换两层覆盖，
// 反复构造探针请求断言 UA 版本 == body cc_version 版本（旧实现两条独立
// getter 链在两次取值之间可被 Set 拆散）。
func TestAnthropicProbeUserAgentAndCCVersionSameSourceUnderRefresh(t *testing.T) {
	resetProbeIdentityOverrides(t)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		manual := []map[string]string{nil, {"claudeCode": "3.3.0"}, {"claudeCode": "1.1.0"}}
		for round := 0; ; round++ {
			select {
			case <-stop:
				return
			default:
			}
			upstreamidentity.SetClientVersionOverrides(manual[round%len(manual)])
			upstreamidentity.SetClientVersionAutoOverrides(map[string]string{"claudeCode": "4.4.0"})
		}
	}()
	// 写 goroutine 由 Cleanup 收口：断言中途 t.Fatalf 退出也必须先停写、
	// 再恢复两层，否则泄漏 writer 会污染同包后续测试的进程级覆盖状态。
	t.Cleanup(func() {
		close(stop)
		wg.Wait()
	})
	for round := 0; round < 40; round++ {
		ccVersion, userAgent := probeAnthropicIdentity(t, upstreamidentity.NewSnapshot())
		assertAnthropicUAMatchesBody(t, ccVersion, userAgent)
	}
}

// applyProbeIdentityHeaders 的沉默契约保持：调用方已设置显式 UA（A/B 形态
// Build 已写 Codex UA 的现状）一律不覆盖；非 Anthropic OAuth 输入不触发快照
// 改写，头集合仍由 ApplySystemClientHeaders 落齐。
func TestApplyProbeIdentityHeadersKeepsCallerUserAgentAndOtherFamilies(t *testing.T) {
	resetProbeIdentityOverrides(t)
	upstreamidentity.SetClientVersionOverrides(map[string]string{"claudeCode": "2.2.0", "codex": "0.200.0"})
	identitySnapshot := upstreamidentity.NewSnapshot()

	// 调用方自带 UA 的 Anthropic OAuth 输入：UA 保持原值。
	headers := http.Header{}
	callerUA := upstreamidentity.EffectiveCodexUserAgent()
	headers.Set("User-Agent", callerUA)
	applyProbeIdentityHeaders(headers, upstreamidentity.Input{ProviderCode: "anthropic", CredentialType: "oauth"}, identitySnapshot)
	if got := headers.Get("User-Agent"); got != callerUA {
		t.Fatalf("caller-set UA must be kept, got %q", got)
	}

	// GPT 家族 api_key 输入：UA 由 ApplySystemClientHeaders 落（Effective 消费），
	// 快照覆盖分支不触发。
	headers = http.Header{}
	applyProbeIdentityHeaders(headers, upstreamidentity.Input{ProviderCode: "gpt", CredentialType: "api_key"}, identitySnapshot)
	if got := headers.Get("User-Agent"); got != upstreamidentity.EffectiveCodexUserAgent() {
		t.Fatalf("GPT family UA must come from ApplySystemClientHeaders, got %q", got)
	}

	// Anthropic OAuth 无自带 UA：UA 来自快照且其余身份头照常落齐。
	headers = http.Header{}
	applyProbeIdentityHeaders(headers, upstreamidentity.Input{ProviderCode: "anthropic", CredentialType: "oauth"}, identitySnapshot)
	if got := headers.Get("User-Agent"); got != identitySnapshot.ClaudeCodeUserAgent() {
		t.Fatalf("anthropic oauth UA=%q, want snapshot UA %q", got, identitySnapshot.ClaudeCodeUserAgent())
	}
	if got := headers.Get("x-app"); got != "cli" {
		t.Fatalf("anthropic oauth identity headers must still be applied, x-app=%q", got)
	}
}
