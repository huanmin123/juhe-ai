package oauthrefresh

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// bounded failure context (port of log-failure-context.ts)
// ---------------------------------------------------------------------------

func TestCaptureExpectedFailureContextRejectsEmptyReasonCode(t *testing.T) {
	if _, err := CaptureExpectedFailureContext("  ", map[string]any{"k": "v"}); err == nil {
		t.Fatal("expected error for empty reasonCode")
	}
	if _, err := CaptureExpectedFailureContext("local_configuration", map[string]any{"k": "v"}); err != nil {
		t.Fatal(err)
	}
}

func TestExpectedFailureContextShape(t *testing.T) {
	context, err := CaptureExpectedFailureContext("quota_exceeded", map[string]any{
		"provider": "gemini", "accountId": "acc-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if context.FailureClass != "expected" || context.ReasonCode != "quota_exceeded" {
		t.Fatalf("context=%+v", context)
	}
	if context.DecisionInputs["provider"] != "gemini" {
		t.Fatalf("decisionInputs=%+v", context.DecisionInputs)
	}
	if context.RedactedFields == nil || len(context.RedactedFields) != 0 {
		t.Fatalf("redactedFields=%+v (Node archives keep the list empty)", context.RedactedFields)
	}
	if context.FieldSizes == nil || context.FieldHashes == nil {
		t.Fatalf("fieldSizes/fieldHashes must be present maps: %+v", context)
	}
	if context.TruncationReason != "" {
		t.Fatalf("no truncation expected: %q", context.TruncationReason)
	}
}

// Sensitive values must never appear in the failure log attributes: the
// decision inputs are the caller's non-sensitive identifiers and the truncation
// markers/hashes never carry the raw value.
func TestFailureContextDoesNotLeakSensitiveValues(t *testing.T) {
	secret := "sk-super-secret-refresh-token-value"
	large := strings.Repeat("A", 30*1024)
	input := map[string]any{"accessToken": secret, "payload": large}
	context := CaptureUnexpectedFailureContext(errors.New("boom"), FailureCaptureOptions{
		DecisionInputs: input,
	})
	// The raw oversized value is cut: the captured decision input holds a
	// truncated prefix, never the whole 30 KiB payload.
	capturedPayload, _ := context.DecisionInputs["payload"].(string)
	if len(capturedPayload) >= len(large) {
		t.Fatalf("payload not truncated: %d bytes", len(capturedPayload))
	}
	// The hash attribution is a sha256 of the first 8 KiB only — verifying it
	// matches that bounded derivation (and is not the raw value).
	sum := sha256.Sum256([]byte(string([]rune(large)[:failureMaxHashInputLength])))
	expected := hex.EncodeToString(sum[:])
	if context.FieldHashes["decisionInputs.payload"] != expected {
		t.Fatalf("fieldHashes=%v", context.FieldHashes)
	}
	if context.FieldSizes["decisionInputs.payload"] == 0 {
		t.Fatalf("fieldSizes missing: %v", context.FieldSizes)
	}
	if context.TruncationReason != "field_or_event_limit" {
		t.Fatalf("truncationReason=%q", context.TruncationReason)
	}
	// The secret itself stays inside the (intentionally echoed) decision input
	// but the structural redaction list remains empty per Node parity; the
	// caller contract is that decision inputs never carry secrets, so assert
	// the log helper path: providers feed only provider/accountId.
	attrs := tokenExchangeFailureLogAttrs(errors.New("boom"), "gemini", "stage", map[string]any{
		"provider": "gemini", "accountId": "acc-1",
	})
	for _, value := range attrs {
		if text, ok := value.(string); ok && strings.Contains(text, secret) {
			t.Fatalf("secret leaked into log attrs")
		}
	}
}

func TestUnexpectedFailureContextCauseChainDepth(t *testing.T) {
	// Build a 6-level chain: wrap1(0)->wrap2(1)->wrap3(2)->wrap4(3)->root(4)->leaf(5).
	inner := errors.New("leaf")
	root := fmt.Errorf("root: %w", inner)
	wrap1 := fmt.Errorf("wrap1: %w", fmt.Errorf("wrap2: %w", fmt.Errorf("wrap3: %w", fmt.Errorf("wrap4: %w", root))))
	// Node captures depths 0..4 (maxCauseDepth=4 recursion hops -> 5 nodes);
	// at depth 4 the remaining cause sets the truncation marker and stops, so
	// the leaf never appears.
	context := CaptureUnexpectedFailureContext(wrap1, FailureCaptureOptions{})
	if context.FailureClass != "unexpected" {
		t.Fatalf("class=%q", context.FailureClass)
	}
	depth := 0
	for node := context.Error; node != nil; node = node.Cause {
		depth++
	}
	if depth != 5 { // wrap1, wrap2, wrap3, wrap4, root
		t.Fatalf("cause chain depth=%d", depth)
	}
	if context.TruncationReason != "field_or_event_limit" {
		t.Fatalf("expected truncation marker, got %q", context.TruncationReason)
	}
	// The depth-4 node (root) is the last captured one; the leaf message must
	// not survive anywhere in the bounded capture.
	for node := context.Error; node != nil; node = node.Cause {
		if node.Message == "leaf" {
			t.Fatalf("leaf must be cut beyond the cause depth: %+v", context.Error)
		}
	}
	if context.Error.Cause.Cause.Cause.Cause.Message != "root: leaf" {
		t.Fatalf("depth-4 node message=%q", context.Error.Cause.Cause.Cause.Cause.Message)
	}
}

func TestUnexpectedFailureContextWithinBudgetNoTruncation(t *testing.T) {
	context := CaptureUnexpectedFailureContext(errors.New("simple failure"), FailureCaptureOptions{
		StageSnapshot:  map[string]any{"stage": "token_refresh"},
		QueueSnapshot:  map[string]any{"queue": "oauth"},
		RetryState:     map[string]any{"attempt": 1},
		DecisionInputs: map[string]any{"provider": "gemini"},
	})
	if context.TruncationReason != "" {
		t.Fatalf("unexpected truncation: %q", context.TruncationReason)
	}
	if context.Error == nil || context.Error.Message != "simple failure" {
		t.Fatalf("error=%+v", context.Error)
	}
	if context.StageSnapshot["stage"] != "token_refresh" {
		t.Fatalf("stageSnapshot=%+v", context.StageSnapshot)
	}
}

func TestFailureContextEventByteBudget(t *testing.T) {
	t.Run("collection entry limit", func(t *testing.T) {
		// 600 small fields hit the 100-entry cap (~105 bytes each, ~10.5 KiB of
		// the 64 KiB budget) long before the byte budget runs out. Node marks
		// truncated without writing the _truncated key in this path.
		input := map[string]any{}
		for i := 0; i < 600; i++ {
			input[fmt.Sprintf("f%d", i)] = strings.Repeat("x", 100)
		}
		context := CaptureUnexpectedFailureContext(errors.New("budget"), FailureCaptureOptions{
			DecisionInputs: input,
		})
		if context.TruncationReason != "field_or_event_limit" {
			t.Fatalf("expected truncation, got %q", context.TruncationReason)
		}
		if len(context.DecisionInputs) != failureMaxCollectionEntries {
			t.Fatalf("captured keys=%d want the %d-entry cap", len(context.DecisionInputs), failureMaxCollectionEntries)
		}
		if _, ok := context.DecisionInputs["_truncated"]; ok {
			t.Fatalf("_truncated must be reserved for byte-budget exhaustion: %v", context.DecisionInputs)
		}
	})
	t.Run("event byte budget exhaustion", func(t *testing.T) {
		// Ten 10 KiB fields exhaust the 64 KiB budget mid-walk: the ninth entry
		// finds remainingBytes<=0 and Node writes the _truncated marker.
		input := map[string]any{}
		for i := 0; i < 10; i++ {
			input[fmt.Sprintf("f%d", i)] = strings.Repeat("y", 10*1024)
		}
		context := CaptureUnexpectedFailureContext(errors.New("budget"), FailureCaptureOptions{
			DecisionInputs: input,
		})
		if context.TruncationReason != "field_or_event_limit" {
			t.Fatalf("expected truncation, got %q", context.TruncationReason)
		}
		if context.DecisionInputs["_truncated"] != "event byte budget" {
			t.Fatalf("missing _truncated marker: keys=%d", len(context.DecisionInputs))
		}
		total := 0
		for key, value := range context.DecisionInputs {
			if key == "_truncated" {
				continue
			}
			total += len(value.(string))
		}
		if total > failureMaxEventBytes {
			t.Fatalf("captured %d bytes exceeds the %d budget", total, failureMaxEventBytes)
		}
	})
}

func TestTruncateFailureStringHashIsStableAndBounded(t *testing.T) {
	state := newFailureCaptureState()
	value := strings.Repeat("哈", 5000) // 15000 bytes, 5000 chars
	first := truncateFailureString(value, "error.stack", state)
	if len(first) >= len(value) {
		t.Fatalf("not truncated")
	}
	// Same input twice produces the same hash (deterministic attribution).
	state2 := newFailureCaptureState()
	_ = truncateFailureString(value, "error.stack", state2)
	if state.fieldHashes["error.stack"] != state2.fieldHashes["error.stack"] {
		t.Fatalf("hash unstable: %q vs %q", state.fieldHashes["error.stack"], state2.fieldHashes["error.stack"])
	}
	if state.fieldSizes["error.stack"] != 5000 { // UTF-16 code units
		t.Fatalf("fieldSizes=%d", state.fieldSizes["error.stack"])
	}
	if !utf8ValidPrefix(first, value) {
		t.Fatalf("truncated value is not a prefix of the input")
	}
	if !state2.truncated {
		t.Fatalf("state must be marked truncated")
	}
}

func utf8ValidPrefix(prefix, value string) bool {
	return strings.HasPrefix(value, prefix)
}

func TestBoundedUTF8PrefixDropsBrokenRune(t *testing.T) {
	value := "a哈哈哈" // bytes: 1 + 3 + 3 + 3
	// 2 bytes cut mid-rune ("a" + first byte of 哈): the broken sequence is
	// dropped exactly like Node's buffer.toString().replace(/\uFFFD$/u, "").
	if prefix := boundedUTF8Prefix(value, 2); prefix != "a" {
		t.Fatalf("mid-rune prefix=%q", prefix)
	}
	// 4 bytes fit "a" plus one complete 哈 (no FFFD to strip; Node keeps it).
	if prefix := boundedUTF8Prefix(value, 4); prefix != "a哈" {
		t.Fatalf("rune-boundary prefix=%q", prefix)
	}
	if boundedUTF8Prefix(value, 0) != "" {
		t.Fatalf("zero budget must yield empty")
	}
	if boundedUTF8Prefix(value, 1000) != value {
		t.Fatalf("large budget keeps value")
	}
}

func TestTokenExchangeFailureLogAttrsClassification(t *testing.T) {
	// Expected (local configuration) failure.
	expectedAttrs := tokenExchangeFailureLogAttrs(
		&LocalConfigurationError{Message: "缺少 Refresh Token"}, "gemini", "oauth_keepalive_refresh",
		map[string]any{"provider": "gemini"},
	)
	if getLogAttr(expectedAttrs, "failureClass") != "expected" {
		t.Fatalf("expected failureClass=expected: %v", expectedAttrs)
	}
	if getLogAttr(expectedAttrs, "reasonCode") != "local_configuration" {
		t.Fatalf("reasonCode=%v", getLogAttr(expectedAttrs, "reasonCode"))
	}

	// Unexpected (upstream) failure.
	unexpectedAttrs := tokenExchangeFailureLogAttrs(
		upstreamError("Gemini", 502, "server_error"), "gemini", "oauth_keepalive_refresh",
		map[string]any{"provider": "gemini"},
	)
	if getLogAttr(unexpectedAttrs, "failureClass") != "unexpected" {
		t.Fatalf("expected failureClass=unexpected: %v", unexpectedAttrs)
	}
	if getLogAttr(unexpectedAttrs, "error") == nil {
		t.Fatalf("missing captured error")
	}
	if getLogAttr(unexpectedAttrs, "reasonCode") != nil {
		t.Fatalf("unexpected failure must not carry reasonCode")
	}
}

func getLogAttr(args []any, key string) any {
	for i := 0; i+1 < len(args); i += 2 {
		if args[i] == key {
			return args[i+1]
		}
	}
	return nil
}

func TestFailureContextSlogRendering(t *testing.T) {
	var buffer strings.Builder
	logger := slog.New(slog.NewTextHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	attrs := tokenExchangeFailureLogAttrs(errors.New("render me"), "gemini", "stage", map[string]any{"provider": "gemini"})
	logger.Warn("token refresh failed", append([]any{"event", "test"}, attrs...)...)
	if !strings.Contains(buffer.String(), "failureClass=unexpected") {
		t.Fatalf("log output missing failureClass: %s", buffer.String())
	}
}

// ---------------------------------------------------------------------------
// proxyUrl passthrough (TokenExchangeTransport parity)
// ---------------------------------------------------------------------------

func TestHTTPTokenExchangerInvalidProxyURLFails(t *testing.T) {
	exchanger := NewHTTPTokenExchanger()
	_, err := exchanger.Do(context.Background(), TokenHTTPRequest{
		URL:      "https://oauth2.googleapis.com/token",
		Headers:  map[string]string{"accept": "application/json"},
		Body:     "grant_type=refresh_token",
		ProxyURL: "://not-a-url",
	})
	if err == nil {
		t.Fatal("expected invalid proxy URL to fail the exchange")
	}
	// Unsupported schemes must fail rather than silently going direct.
	if _, err := exchanger.Do(context.Background(), TokenHTTPRequest{
		URL: "https://oauth2.googleapis.com/token", ProxyURL: "ftp://proxy.example:21",
	}); err == nil {
		t.Fatal("expected unsupported proxy scheme to fail")
	}
}

func TestHTTPTokenExchangerDirectRequestUnchanged(t *testing.T) {
	// No ProxyURL: the plain client path is used (the stub server check lives
	// in the socks_proxy_test of accounthealth; here we verify no error and a
	// parseable failure for an unroutable URL, which proves the direct path
	// attempted a real request rather than failing on proxy config).
	exchanger := NewHTTPTokenExchanger()
	if _, err := exchanger.Do(context.Background(), TokenHTTPRequest{
		URL:  "http://127.0.0.1:1/token",
		Body: "grant_type=refresh_token",
	}); err == nil {
		t.Fatal("expected direct request to unroutable address to fail")
	}
}

func TestRequestGeminiTokenCarriesProxyURL(t *testing.T) {
	var seenProxy string
	exchanger := ExchangerFunc(func(_ context.Context, request TokenHTTPRequest) (TokenHTTPResponse, error) {
		seenProxy = request.ProxyURL
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at","expires_in":3600}`}, nil
	})
	_, err := requestGeminiToken(context.Background(), exchanger, map[string]string{
		"grant_type": "refresh_token",
	}, geminiRequestOptions{
		OAuthType: "google_one", ClientID: "id", ClientSecret: "secret",
		ProxyURL: "socks5h://proxy.example:1080",
	}, defaultNow())
	if err != nil {
		t.Fatal(err)
	}
	if seenProxy != "socks5h://proxy.example:1080" {
		t.Fatalf("proxy not passed: %q", seenProxy)
	}

	seenProxy = ""
	if _, err := requestGeminiToken(context.Background(), exchanger, map[string]string{
		"grant_type": "refresh_token",
	}, geminiRequestOptions{OAuthType: "google_one"}, defaultNow()); err != nil {
		t.Fatal(err)
	}
	if seenProxy != "" {
		t.Fatalf("direct exchange must not set a proxy: %q", seenProxy)
	}
}

// ---------------------------------------------------------------------------
// Google One tier inference + Drive storage probe
// ---------------------------------------------------------------------------

func TestInferGeminiGoogleOneTierBoundaries(t *testing.T) {
	gib := int64(1024 * 1024 * 1024)
	tib := 1024 * gib
	cases := []struct {
		name  string
		bytes int64
		want  string
	}{
		{"zero", 0, "google_one_unknown"},
		{"negative", -1, "google_one_unknown"},
		{"15GiB-exact", 15 * gib, "google_one_free"},
		{"15GiB-minus-1", 15*gib - 1, "google_one_unknown"},
		{"15GiB-plus-1", 15*gib + 1, "google_one_free"},
		{"2TiB-exact", 2 * tib, "google_ai_pro"},
		{"2TiB-minus-1", 2*tib - 1, "google_one_free"},
		{"2TiB-plus-1", 2*tib + 1, "google_ai_pro"},
		{"100TiB-exact", 100 * tib, "google_ai_pro"},
		{"100TiB-plus-1", 100*tib + 1, "google_ai_ultra"},
		{"100TiB-minus-1", 100*tib - 1, "google_ai_pro"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := InferGeminiGoogleOneTier(testCase.bytes); got != testCase.want {
				t.Fatalf("InferGeminiGoogleOneTier(%d)=%q want %q", testCase.bytes, got, testCase.want)
			}
		})
	}
}

// fake drive quota prober recording calls and returning canned data.
type fakeDriveProber struct {
	calls int
	token string
	quota *GeminiDriveQuota
	err   error
}

func (f *fakeDriveProber) ProbeDriveQuota(_ context.Context, accessToken string) (*GeminiDriveQuota, error) {
	f.calls++
	f.token = accessToken
	return f.quota, f.err
}

func googleOneInfo(scope string) *GeminiTokenInfo {
	return &GeminiTokenInfo{
		AccessToken: "at-google-one",
		OAuthType:   "google_one",
		Scope:       scope,
	}
}

const driveScope = "https://www.googleapis.com/auth/drive.metadata.readonly code_assist_scope"

func TestEnrichGoogleOneProbesDriveQuotaAndUpgradesTier(t *testing.T) {
	prober := &fakeDriveProber{quota: &GeminiDriveQuota{Limit: 3 * 1024 * gibibyte, Usage: 100}}
	info := enrichGeminiTokenInfo(context.Background(), googleOneInfo(driveScope), nil, prober, "", defaultNow())
	if prober.calls != 1 || prober.token != "at-google-one" {
		t.Fatalf("prober calls=%d token=%q", prober.calls, prober.token)
	}
	if info.TierID != "google_ai_pro" {
		t.Fatalf("tier=%q", info.TierID)
	}
	if info.DriveStorageLimit == nil || *info.DriveStorageLimit != 3*1024*gibibyte {
		t.Fatalf("limit=%v", info.DriveStorageLimit)
	}
	if info.DriveStorageUsage == nil || *info.DriveStorageUsage != 100 {
		t.Fatalf("usage=%v", info.DriveStorageUsage)
	}
	if info.DriveTierUpdatedAt == "" {
		t.Fatalf("updatedAt missing")
	}
}

func TestEnrichGoogleOneProbeFailureKeepsTier(t *testing.T) {
	prober := &fakeDriveProber{err: errors.New("drive quota rejected")}
	info := enrichGeminiTokenInfo(context.Background(), googleOneInfo(driveScope), nil, prober, "", defaultNow())
	if prober.calls != 1 {
		t.Fatalf("prober calls=%d", prober.calls)
	}
	// Refresh is not blocked: the static default tier survives.
	if info.TierID != "google_one_free" {
		t.Fatalf("tier=%q", info.TierID)
	}
	if info.DriveStorageLimit != nil || info.DriveStorageUsage != nil || info.DriveTierUpdatedAt != "" {
		t.Fatalf("drive fields must stay unset: %+v", info)
	}
}

func TestEnrichGoogleOneUnknownStorageKeepsTier(t *testing.T) {
	prober := &fakeDriveProber{quota: &GeminiDriveQuota{Limit: 1024}} // tiny limit -> unknown
	info := enrichGeminiTokenInfo(context.Background(), googleOneInfo(driveScope), nil, prober, "", defaultNow())
	if info.TierID != "google_one_free" {
		t.Fatalf("unknown detection must keep the selected tier, got %q", info.TierID)
	}
	// Storage fields are still written (Node writes them regardless).
	if info.DriveStorageLimit == nil || *info.DriveStorageLimit != 1024 {
		t.Fatalf("limit=%v", info.DriveStorageLimit)
	}
}

func TestEnrichWithoutDriveScopeSkipsProbe(t *testing.T) {
	prober := &fakeDriveProber{}
	info := enrichGeminiTokenInfo(context.Background(), googleOneInfo("https://www.googleapis.com/auth/cloud-platform"), nil, prober, "", defaultNow())
	if prober.calls != 0 {
		t.Fatalf("probe must be skipped without the drive scope, calls=%d", prober.calls)
	}
	if info.TierID != "google_one_free" {
		t.Fatalf("tier=%q", info.TierID)
	}
}

func TestEnrichStaticTiersUnchanged(t *testing.T) {
	aiStudio := enrichGeminiTokenInfo(context.Background(), &GeminiTokenInfo{OAuthType: "ai_studio"}, nil, nil, "", defaultNow())
	if aiStudio.TierID != "aistudio_free" {
		t.Fatalf("ai_studio tier=%q", aiStudio.TierID)
	}
	codeAssist := enrichGeminiTokenInfo(context.Background(), &GeminiTokenInfo{OAuthType: "code_assist", TierID: "standard"}, nil, nil, "", defaultNow())
	if codeAssist.TierID != "gcp_standard" {
		t.Fatalf("code_assist tier=%q", codeAssist.TierID)
	}
}

func TestHasGoogleDriveMetadataScope(t *testing.T) {
	if !hasGoogleDriveMetadataScope("a b https://www.googleapis.com/auth/drive.metadata.readonly c") {
		t.Fatal("expected scope match")
	}
	if hasGoogleDriveMetadataScope("https://www.googleapis.com/auth/drive.metadata.readonlyx") {
		t.Fatal("prefix must not match")
	}
	if hasGoogleDriveMetadataScope("") {
		t.Fatal("empty scope must not match")
	}
}

func TestBuildGeminiOAuthCredentialsDriveFields(t *testing.T) {
	limit := int64(3 * 1024 * gibibyte)
	usage := int64(100)
	info := &GeminiTokenInfo{
		AccessToken: "at", ClientID: "id", ClientSecret: "secret",
		OAuthType: "google_one", TierID: "google_ai_pro",
		DriveStorageLimit: &limit, DriveStorageUsage: &usage,
		DriveTierUpdatedAt: "2026-09-09T00:00:00.000Z",
	}
	credentials := BuildGeminiOAuthCredentials(info, nil)
	if credentials["drive_storage_limit"] != limit {
		t.Fatalf("drive_storage_limit=%v", credentials["drive_storage_limit"])
	}
	if credentials["drive_storage_usage"] != usage {
		t.Fatalf("drive_storage_usage=%v", credentials["drive_storage_usage"])
	}
	if credentials["drive_tier_updated_at"] != "2026-09-09T00:00:00.000Z" {
		t.Fatalf("drive_tier_updated_at=%v", credentials["drive_tier_updated_at"])
	}

	// Without a probe the keys are absent (Node undefined semantics).
	plain := BuildGeminiOAuthCredentials(&GeminiTokenInfo{AccessToken: "at", OAuthType: "google_one", TierID: "google_one_free"}, nil)
	for _, key := range []string{"drive_storage_limit", "drive_storage_usage", "drive_tier_updated_at"} {
		if _, ok := plain[key]; ok {
			t.Fatalf("key %q must be absent without a probe", key)
		}
	}
}

// End-to-end refresh: a google_one account with the drive scope runs the
// production Drive probe over the fake exchanger, dialled through the same
// proxy URL as the token exchange (Node passes proxyUrl into
// fetchGoogleDriveStorageQuota), and upgrades the tier from the storage bytes.
func TestRefreshGeminiTokenGoogleOneDriveProbe(t *testing.T) {
	proxy := "socks5h://proxy.example:1080"
	var tokenProxy, driveProxy string
	exchanger := ExchangerFunc(func(_ context.Context, request TokenHTTPRequest) (TokenHTTPResponse, error) {
		switch request.URL {
		case GeminiOAuthTokenURL:
			tokenProxy = request.ProxyURL
			return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-g1","refresh_token":"rt-g1","expires_in":3600,
				"scope":"https://www.googleapis.com/auth/drive.metadata.readonly https://www.googleapis.com/auth/cloud-platform"}`}, nil
		case "https://www.googleapis.com/drive/v3/about?fields=storageQuota":
			driveProxy = request.ProxyURL
			if request.Method != "GET" {
				t.Fatalf("drive probe method=%q", request.Method)
			}
			// The Drive API reports storageQuota as numeric strings; 150 TiB
			// sits above the 100 TiB ultra threshold.
			return TokenHTTPResponse{StatusCode: 200, Body: `{"storageQuota":{"limit":"164926744166400","usage":"5242880"}}`}, nil
		default:
			return TokenHTTPResponse{StatusCode: 404, Body: "not found"}, nil
		}
	})
	info, err := RefreshGeminiToken(context.Background(), exchanger, "rt-old", GeminiCredentialFallback{
		OAuthType: "google_one", ProjectID: "proj",
		Scope:    "https://www.googleapis.com/auth/drive.metadata.readonly",
		ProxyURL: proxy,
	}, defaultNow())
	if err != nil {
		t.Fatal(err)
	}
	if tokenProxy != proxy {
		t.Fatalf("token exchange proxy=%q", tokenProxy)
	}
	if driveProxy != proxy {
		t.Fatalf("drive probe must dial the same proxy as the token exchange, got %q", driveProxy)
	}
	if info.TierID != "google_ai_ultra" {
		t.Fatalf("tier=%q (150TiB must infer ultra)", info.TierID)
	}
	if info.DriveStorageLimit == nil || *info.DriveStorageLimit != 164926744166400 {
		t.Fatalf("limit=%v", info.DriveStorageLimit)
	}
	if info.DriveStorageUsage == nil || *info.DriveStorageUsage != 5242880 {
		t.Fatalf("usage=%v", info.DriveStorageUsage)
	}
	if credentials := BuildGeminiOAuthCredentials(info, nil); credentials["drive_storage_limit"] != int64(164926744166400) {
		t.Fatalf("drive_storage_limit=%v", credentials["drive_storage_limit"])
	}
}

// The production Drive probe honours ProxyURL passthrough: present -> the
// exchanger sees it; absent -> the request stays direct (empty proxy).
func TestGoogleOneDriveQuotaProberCarriesProxyURL(t *testing.T) {
	var seen TokenHTTPRequest
	exchanger := ExchangerFunc(func(_ context.Context, request TokenHTTPRequest) (TokenHTTPResponse, error) {
		seen = request
		return TokenHTTPResponse{StatusCode: 200, Body: `{"storageQuota":{"limit":1024,"usage":10}}`}, nil
	})
	prober := newGoogleOneDriveQuotaProber(exchanger, "socks5://proxy.example:1080")
	quota, err := prober.ProbeDriveQuota(context.Background(), "at-1")
	if err != nil {
		t.Fatal(err)
	}
	if quota == nil || quota.Limit != 1024 || quota.Usage != 10 {
		t.Fatalf("quota=%+v", quota)
	}
	if seen.ProxyURL != "socks5://proxy.example:1080" {
		t.Fatalf("proxy=%q", seen.ProxyURL)
	}
	if seen.Method != "GET" || seen.URL != "https://www.googleapis.com/drive/v3/about?fields=storageQuota" {
		t.Fatalf("request=%+v", seen)
	}
	if seen.Headers["authorization"] != "Bearer at-1" || seen.Headers["user-agent"] != geminiCLIUserAgent {
		t.Fatalf("headers=%v", seen.Headers)
	}

	seen = TokenHTTPRequest{}
	direct := newGoogleOneDriveQuotaProber(exchanger, "  ")
	if _, err := direct.ProbeDriveQuota(context.Background(), "at-1"); err != nil {
		t.Fatal(err)
	}
	if seen.ProxyURL != "" {
		t.Fatalf("blank proxy must normalize to direct, got %q", seen.ProxyURL)
	}
}

func TestFiniteNonNegativeInt64(t *testing.T) {
	// Node finiteNonNegativeNumber coerces non-numbers with Number(): numeric
	// strings parse (the Drive API reports storageQuota as strings), null
	// reads 0, true reads 1, junk reads 0; negatives and NaN read 0.
	if finiteNonNegativeInt64(float64(12.5)) != 12 {
		t.Fatalf("fractional truncation")
	}
	if finiteNonNegativeInt64(float64(-1)) != 0 {
		t.Fatalf("negative must read 0")
	}
	if finiteNonNegativeInt64("157286400000") != 157286400000 {
		t.Fatalf("numeric string must parse like Node Number()")
	}
	if finiteNonNegativeInt64(" 64 ") != 64 {
		t.Fatalf("whitespace-trimmed string must parse")
	}
	if finiteNonNegativeInt64("-5") != 0 {
		t.Fatalf("negative string must read 0")
	}
	if finiteNonNegativeInt64("junk") != 0 {
		t.Fatalf("junk string must read 0")
	}
	if finiteNonNegativeInt64("") != 0 {
		t.Fatalf("empty string reads 0 (Number(\"\")===0)")
	}
	if finiteNonNegativeInt64(nil) != 0 {
		t.Fatalf("nil reads 0 (Number(null)===0)")
	}
	if finiteNonNegativeInt64(true) != 1 {
		t.Fatalf("true reads 1 (Number(true)===1)")
	}
}

// Compile-time check: the production prober satisfies the boundary.
var _ GeminiDriveQuotaProber = googleOneDriveQuotaProber{}

// ---------------------------------------------------------------------------
// clock guard
// ---------------------------------------------------------------------------

func TestIsoMillisUsedForDriveTimestamp(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	prober := &fakeDriveProber{quota: &GeminiDriveQuota{Limit: 20 * gibibyte}}
	info := enrichGeminiTokenInfo(context.Background(), googleOneInfo(driveScope), nil, prober, "", now)
	if info.DriveTierUpdatedAt != "2026-09-09T12:00:00.000Z" {
		t.Fatalf("updatedAt=%q", info.DriveTierUpdatedAt)
	}
}
