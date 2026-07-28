package gatewaycandidatewindow

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestBatchHydratorLoadsResourceFactsProxyRuntimeAndFreshQuality(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	reader := &hydrationReaderStub{facts: port.GatewayCandidateHydrationFacts{
		Accounts: map[string]port.GatewayCandidateAccountFacts{
			"source_1": {
				SupportedModels: []string{"gpt-upstream"},
				ModelMappings:   []port.GatewayCandidateModelMapping{{ProviderCode: "gpt", SourceModel: "gpt-client", SourceEndpointFamily: "responses", UpstreamModel: "gpt-upstream", UpstreamEndpointFamily: "responses", Enabled: true}},
				DefaultBaseURL:  "https://profile.example.com",
			},
		},
		Proxies: map[string]port.GatewayCandidateProxyFacts{
			"proxy_1": {ID: "proxy_1", Type: "http", Host: "proxy.local", Port: 8080, Username: "user", PasswordEncrypted: "proxy-secret", Enabled: true},
		},
	}}
	codec := &hydrationCodecStub{values: map[string]map[string]any{
		"account-secret": {"api_keys": []any{"sk-first", "sk-second"}, "base_url": "https://api.example.com"},
		"proxy-secret":   {"password": "proxy-password"},
	}}
	runtime := &apiKeyRuntimeStub{}
	quality := &qualityReaderStub{facts: map[string]port.GatewayCandidateQualityFacts{"view_1": {QualityScore: ptr(int64(91)), QualityState: "fresh"}}}
	hydrator := NewBatchHydrator(BatchHydratorOptions{
		Reader: reader, QualityReader: quality, APIKeyRuntime: runtime, CredentialCodec: codec,
		FingerprintSecret: "fingerprint-secret", Now: func() time.Time { return now },
	})
	rows := []port.GatewayAccountCandidate{{
		AccountID: "view_1", Type: "oauth", CredentialsEncrypted: "view-secret",
		ResourceAccountID: "source_1", ResourceType: "api_key", ResourceCredentialsEncrypted: "account-secret", ResourceProxyProfileID: "proxy_1",
	}}
	ranks, err := hydrator.PreRank(context.Background(), rows)
	if err != nil {
		t.Fatal(err)
	}
	results, err := hydrator.Hydrate(context.Background(), HydrateInput{Candidates: rows, PreRanks: ranks})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].DropReason != "" {
		t.Fatalf("results = %+v", results)
	}
	candidate := results[0].Candidate
	if candidate.Projection.AccountID != "view_1" || candidate.Projection.ResourceAccountID != "source_1" {
		t.Fatalf("candidate projection = %+v", candidate.Projection)
	}
	if candidate.DefaultBaseURL != "https://profile.example.com" {
		t.Fatalf("default base URL = %q", candidate.DefaultBaseURL)
	}
	if value, ok := candidate.Credentials.StringValue("base_url"); !ok || value != "https://api.example.com" {
		t.Fatalf("base_url = %q/%v", value, ok)
	}
	if candidate.Proxy == nil || candidate.Proxy.Host != "proxy.local" {
		t.Fatalf("proxy = %+v", candidate.Proxy)
	}
	if password, ok := candidate.Proxy.Credentials.StringValue("password"); !ok || password != "proxy-password" {
		t.Fatalf("proxy password = %q/%v", password, ok)
	}
	if candidate.QualityScore == nil || *candidate.QualityScore != 91 || len(candidate.ModelMappings) != 1 || len(candidate.APIKeyRuntime) != 2 {
		t.Fatalf("candidate facts = %+v", candidate)
	}
	if candidate.APIKeyRuntime[0].Status != "active" || candidate.APIKeyRuntime[1].Status != "active" {
		t.Fatalf("api key runtime = %+v", candidate.APIKeyRuntime)
	}
	if !reflect.DeepEqual(reader.input.AccountIDs, []string{"source_1"}) || !reflect.DeepEqual(reader.input.ProxyIDs, []string{"proxy_1"}) {
		t.Fatalf("reader input = %+v", reader.input)
	}
	if quality.calls != 1 || !quality.freshAfter.Equal(now.Add(-24*time.Hour)) {
		t.Fatalf("quality calls/fresh = %d/%s", quality.calls, quality.freshAfter)
	}
	if len(runtime.input["source_1"]) != 2 {
		t.Fatalf("runtime fingerprints = %+v", runtime.input)
	}
	encoded, err := json.Marshal(candidate)
	if err != nil || strings.Contains(string(encoded), "sk-first") || strings.Contains(string(encoded), "proxy-password") {
		t.Fatalf("candidate JSON leaked credentials: %s err=%v", encoded, err)
	}
	if text := candidate.Credentials.String(); text != "[REDACTED]" {
		t.Fatalf("credential string = %q", text)
	}
}

func TestBatchHydratorDropsCandidateLocalFailuresWithoutAbortingBatch(t *testing.T) {
	reader := &hydrationReaderStub{facts: port.GatewayCandidateHydrationFacts{
		Accounts: map[string]port.GatewayCandidateAccountFacts{"good": {}},
		Proxies:  map[string]port.GatewayCandidateProxyFacts{"disabled": {ID: "disabled", Enabled: false}},
	}}
	codec := &hydrationCodecStub{
		values: map[string]map[string]any{
			"good-secret": {"api_key": "sk-good"},
			"empty-key":   {"api_key": " "},
			"empty-oauth": {"expires_at": "later"},
		},
		errors: map[string]error{"broken": errors.New("bad ciphertext")},
	}
	results, err := NewBatchHydrator(BatchHydratorOptions{
		Reader: reader, APIKeyRuntime: &apiKeyRuntimeStub{}, CredentialCodec: codec, FingerprintSecret: "secret",
	}).Hydrate(context.Background(), HydrateInput{Candidates: []port.GatewayAccountCandidate{
		{AccountID: "broken", Type: "api_key", CredentialsEncrypted: "broken"},
		{AccountID: "empty", Type: "api_key", CredentialsEncrypted: "empty-key"},
		{AccountID: "good", Type: "api_key", CredentialsEncrypted: "good-secret", ProxyProfileID: "disabled"},
		{AccountID: "oauth", Type: "oauth", CredentialsEncrypted: "empty-oauth"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{DropCredentialDecrypt, DropAPIKeyMissing, "", DropOAuthTokenMissing}
	for index := range want {
		if results[index].DropReason != want[index] {
			t.Fatalf("result %d = %+v, want %q", index, results[index], want[index])
		}
	}
	if results[2].Candidate.Proxy == nil || results[2].Candidate.Proxy.Available || results[2].Candidate.Proxy.UnavailableReason != DropProxyDisabled {
		t.Fatalf("disabled proxy = %+v", results[2].Candidate.Proxy)
	}
}

func TestBatchHydratorValidatesDependenciesBoundsAndStoreErrors(t *testing.T) {
	codec := &hydrationCodecStub{values: map[string]map[string]any{"secret": {"access_token": "value"}}}
	if _, err := NewBatchHydrator(BatchHydratorOptions{CredentialCodec: codec}).Hydrate(context.Background(), HydrateInput{}); err == nil {
		t.Fatal("missing reader error = nil")
	}
	if _, err := NewBatchHydrator(BatchHydratorOptions{Reader: &hydrationReaderStub{}}).Hydrate(context.Background(), HydrateInput{}); err == nil {
		t.Fatal("missing codec error = nil")
	}
	tooMany := make([]port.GatewayAccountCandidate, FinalLimit+1)
	if _, err := NewBatchHydrator(BatchHydratorOptions{Reader: &hydrationReaderStub{}, CredentialCodec: codec}).Hydrate(context.Background(), HydrateInput{Candidates: tooMany}); err == nil {
		t.Fatal("oversized batch error = nil")
	}
	wantErr := errors.New("database unavailable")
	reader := &hydrationReaderStub{err: wantErr}
	_, err := NewBatchHydrator(BatchHydratorOptions{Reader: reader, CredentialCodec: codec}).Hydrate(context.Background(), HydrateInput{Candidates: []port.GatewayAccountCandidate{{AccountID: "a", Type: "oauth", CredentialsEncrypted: "secret"}}})
	if !errors.Is(err, wantErr) {
		t.Fatalf("store error = %v", err)
	}
}

func TestAPIKeyPoolPreservesOriginalIndexAndDoesNotFallback(t *testing.T) {
	entries := credentialAPIKeys(map[string]any{
		"api_keys": []any{"", "sk-second", "sk-second"},
		"api_key":  "sk-fallback",
	})
	if len(entries) != 1 || entries[0].key != "sk-second" || entries[0].index != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries := credentialAPIKeys(map[string]any{"api_keys": []any{""}, "api_key": "sk-fallback"}); len(entries) != 0 {
		t.Fatalf("invalid pool fell back to api_key: %+v", entries)
	}
	if entries := credentialAPIKeys(map[string]any{"api_keys": []any{}, "api_key": "sk-fallback"}); len(entries) != 1 || entries[0].key != "sk-fallback" {
		t.Fatalf("empty pool did not fall back: %+v", entries)
	}
	keys := fingerprintKeys(entries, "secret")
	runtime := mapAPIKeyRuntime(keys, nil)
	if len(runtime) != 1 || runtime[0].KeyIndex != 1 {
		t.Fatalf("runtime = %+v", runtime)
	}
	runtime = mapAPIKeyRuntime(keys, []port.ManagementAccountAPIKeyRuntimeState{{KeyFingerprint: keys[0].fingerprint, KeyIndex: 99, Status: "active"}})
	if runtime[0].KeyIndex != 1 {
		t.Fatalf("stale runtime index replaced credential index: %+v", runtime)
	}
}

func TestHydrateProxyAllowsEmptyPasswordAndNormalizesSocks5(t *testing.T) {
	hydrator := NewBatchHydrator(BatchHydratorOptions{CredentialCodec: &hydrationCodecStub{values: map[string]map[string]any{"empty-password": {}}}})
	proxy, err := hydrator.hydrateProxy(port.GatewayCandidateProxyFacts{
		ID: "proxy", Type: "socks5", Host: "127.0.0.1", Port: 1080, PasswordEncrypted: "empty-password", Enabled: true,
	})
	if err != nil || !proxy.Available || proxy.Type != "socks5h" {
		t.Fatalf("proxy = %+v err=%v", proxy, err)
	}
}

func TestBatchHydratorPreRanksFullScanWithOneQualityRead(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	quality := &qualityReaderStub{facts: map[string]port.GatewayCandidateQualityFacts{"account_511": {QualityScore: ptr(int64(7))}}}
	hydrator := NewBatchHydrator(BatchHydratorOptions{
		Reader: &hydrationReaderStub{}, QualityReader: quality, CredentialCodec: &hydrationCodecStub{}, Now: func() time.Time { return now },
	})
	candidates := make([]port.GatewayAccountCandidate, port.GatewayAccountCandidateScanLimit)
	for index := range candidates {
		candidates[index].AccountID = "account_" + itoa(index)
	}
	ranks, err := hydrator.PreRank(context.Background(), candidates)
	if err != nil {
		t.Fatal(err)
	}
	if quality.calls != 1 || len(quality.ids) != port.GatewayAccountCandidateScanLimit || !quality.freshAfter.Equal(now.Add(-24*time.Hour)) {
		t.Fatalf("quality calls/ids/fresh = %d/%d/%s", quality.calls, len(quality.ids), quality.freshAfter)
	}
	if ranks["account_511"].QualityScore == nil || *ranks["account_511"].QualityScore != 7 {
		t.Fatalf("ranks = %+v", ranks["account_511"])
	}
}

func TestBatchHydratorPreRankPropagatesQualityReaderError(t *testing.T) {
	wantErr := errors.New("quality unavailable")
	hydrator := NewBatchHydrator(BatchHydratorOptions{
		Reader: &hydrationReaderStub{}, QualityReader: &qualityReaderStub{err: wantErr}, CredentialCodec: &hydrationCodecStub{},
	})
	_, err := hydrator.PreRank(context.Background(), []port.GatewayAccountCandidate{{AccountID: "account"}})
	if !errors.Is(err, wantErr) {
		t.Fatalf("PreRank() error = %v", err)
	}
}

type hydrationReaderStub struct {
	input port.GatewayCandidateHydrationInput
	facts port.GatewayCandidateHydrationFacts
	err   error
}

func (s *hydrationReaderStub) LoadGatewayCandidateHydrationFacts(_ context.Context, input port.GatewayCandidateHydrationInput) (port.GatewayCandidateHydrationFacts, error) {
	s.input = input
	return s.facts, s.err
}

type hydrationCodecStub struct {
	values map[string]map[string]any
	errors map[string]error
}

func (s *hydrationCodecStub) DecryptJSON(value string) (map[string]any, error) {
	if err := s.errors[value]; err != nil {
		return nil, err
	}
	return s.values[value], nil
}

type apiKeyRuntimeStub struct {
	input  map[string][]string
	states map[string][]port.ManagementAccountAPIKeyRuntimeState
	err    error
}

type qualityReaderStub struct {
	ids        []string
	freshAfter time.Time
	facts      map[string]port.GatewayCandidateQualityFacts
	err        error
	calls      int
}

func (s *qualityReaderStub) LoadGatewayCandidateQualityFacts(_ context.Context, ids []string, freshAfter time.Time) (map[string]port.GatewayCandidateQualityFacts, error) {
	s.calls++
	s.ids = ids
	s.freshAfter = freshAfter
	return s.facts, s.err
}

func (s *apiKeyRuntimeStub) ListManagementAccountAPIKeyRuntimeStatesByFingerprints(_ context.Context, input map[string][]string) (map[string][]port.ManagementAccountAPIKeyRuntimeState, error) {
	s.input = input
	return s.states, s.err
}
