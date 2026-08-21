package accounthealth

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestDirectInputRequiredRelationsStayOutsideJobsSchema(t *testing.T) {
	if len(directInputRequiredRelations) == 0 {
		t.Fatal("direct input contract must declare its read-only business relations")
	}
	for _, relation := range directInputRequiredRelations {
		if strings.HasPrefix(relation, "juhe_jobs.") || !strings.Contains(relation, ".") {
			t.Fatalf("direct input relation %q must remain outside the jobs-owned schema", relation)
		}
	}
}

func TestDirectInputCandidatesIncludeResponsesSSE(t *testing.T) {
	if !strings.Contains(directInputCandidatesSQL, "'responses_sse'") {
		t.Fatal("PG direct input 候选查询必须包含 responses_sse")
	}
	if !strings.Contains(directInputCandidatesSQL, "a.type <> 'oauth' OR a.health_check_endpoint_mode = 'responses_json'") {
		t.Fatal("PG direct input 候选查询必须排除 OAuth responses_sse")
	}
}

func TestDirectInputCandidatesPrioritizeOverdueSchedulesBeforeRecentUpdates(t *testing.T) {
	const expectedOrder = "ORDER BY CASE WHEN a.status = 'pending_test' THEN 0 ELSE 1 END,"
	if !strings.Contains(directInputCandidatesSQL, expectedOrder) {
		t.Fatalf("PG direct input candidates must prioritize activation before periodic and cooldown checks: %s", directInputCandidatesSQL)
	}
	if !strings.Contains(directInputCandidatesSQL, "ROW_NUMBER() OVER") || !strings.Contains(directInputCandidatesSQL, "PARTITION BY CASE WHEN a.status IN ('temporary_unavailable', 'rate_limited') THEN 0 ELSE 1 END") {
		t.Fatal("active and cooldown candidates must use separate row-number partitions to prevent starvation")
	}
	if !strings.Contains(directInputCandidatesSQL, "THEN a.cooldown_until ELSE a.next_health_check_at END ASC NULLS FIRST") {
		t.Fatal("cooldown candidates must use cooldown_until as their due-order key")
	}
	orderBy := directInputCandidatesSQL[strings.LastIndex(directInputCandidatesSQL, "ORDER BY"):]
	if strings.Contains(orderBy, "updated_at") {
		t.Fatal("updated_at must not participate in direct-input scheduling, or full batches can starve overdue accounts")
	}
	if !strings.Contains(directInputCandidatesSQL, "LIMIT $2") {
		t.Fatal("direct input candidate query must preserve its bounded database limit")
	}
	if !strings.Contains(directInputCandidatesSQL, "OFFSET $5") {
		t.Fatal("direct input candidate query must support a stable refill offset after quota filtering")
	}
}

func TestDirectInputCandidateFairSlotsAdmitActiveAndCooldown(t *testing.T) {
	// The SQL row-number partitions are intentionally interleaved by rank. With
	// both classes backlogged, every bounded page must admit both classes rather
	// than consuming the entire LIMIT from whichever class sorts first.
	got := fairCandidateStatusSlots(6, 8, 8)
	want := []string{"cooldown", "active", "cooldown", "active", "cooldown", "active"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("fair candidate slots = %v, want %v", got, want)
	}
	got = fairCandidateStatusSlots(6, 1, 8)
	want = []string{"cooldown", "active", "active", "active", "active", "active"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("one-sided cooldown slots = %v, want %v", got, want)
	}
}

func fairCandidateStatusSlots(limit, cooldownCount, activeCount int) []string {
	if limit <= 0 {
		return nil
	}
	result := make([]string, 0, limit)
	for rank := 0; len(result) < limit && (rank < cooldownCount || rank < activeCount); rank++ {
		if rank < cooldownCount {
			result = append(result, "cooldown")
			if len(result) == limit {
				break
			}
		}
		if rank < activeCount {
			result = append(result, "active")
		}
	}
	return result
}

func TestCollectDirectCandidatePagesRefillsAfterQuotaFilteredPage(t *testing.T) {
	const pageSize = 3
	var offsets []int
	accepted := 0 // The first page is entirely quota-ineligible.
	err := collectDirectCandidatePages(pageSize, func(offset int) (int, error) {
		offsets = append(offsets, offset)
		switch offset {
		case 0:
			if accepted != 0 {
				t.Fatalf("first full page must be entirely filtered, accepted = %d", accepted)
			}
			return pageSize, nil
		case pageSize:
			accepted += 2
			return pageSize, nil
		case 2 * pageSize:
			accepted++
			return 1, nil
		default:
			t.Fatalf("unexpected candidate page offset %d", offset)
			return 0, nil
		}
	}, func() int { return accepted })
	if err != nil {
		t.Fatalf("collect pages: %v", err)
	}
	if accepted != 3 {
		t.Fatalf("accepted = %d, want 3", accepted)
	}
	if got, want := fmt.Sprint(offsets), "[0 3 6]"; got != want {
		t.Fatalf("page offsets = %s, want %s", got, want)
	}
}

func TestCollectDirectCandidatePagesRejectsInvalidPageSize(t *testing.T) {
	err := collectDirectCandidatePages(3, func(int) (int, error) {
		return 4, nil
	}, func() int { return 0 })
	if err == nil {
		t.Fatal("page count above the SQL limit must fail closed")
	}
}

func TestDirectInputRejectsOAuthResponsesSSE(t *testing.T) {
	now := time.Date(2030, 8, 16, 0, 0, 0, 0, time.UTC)
	err := validateDirectAccount(DirectAccount{
		ID:                   "oauth-account",
		ConfigRevision:       1,
		DispatchRevision:     1,
		Provider:             "openai",
		Type:                 "oauth",
		Status:               "active",
		Schedulable:          true,
		EndpointMode:         "responses_sse",
		HealthModel:          "gpt-test",
		CredentialsEncrypted: "encrypted",
	}, now)
	if err == nil {
		t.Fatal("OAuth responses_sse must remain outside the frozen Go J1 scope")
	}
}

func TestDirectInputToInputUsesEffectiveSourceAndProxy(t *testing.T) {
	secret := "j1-direct-input-secret"
	credentialCiphertext, err := EncryptV1Envelope(secret, []byte(`{"api_keys":["key-a","key-b"],"base_url":"https://upstream.example/"}`))
	if err != nil {
		t.Fatal(err)
	}
	passwordCiphertext, err := EncryptV1Envelope(secret, []byte(`{"password":"p@ss"}`))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 8, 16, 0, 0, 0, 0, time.UTC)
	input, err := (DirectInput{
		Account:      DirectAccount{ID: "account-1", ConfigRevision: 7, DispatchRevision: 9, Provider: "openai", Type: "api_key", Status: "active", Schedulable: true, EndpointMode: "responses_json", HealthModel: "gpt-test", CredentialsEncrypted: credentialCiphertext},
		Binding:      DirectBinding{GroupID: "group-1", Enabled: true},
		Proxy:        &DirectProxy{ID: "proxy-1", Enabled: true, Type: "socks5", Host: "127.0.0.1", Port: 1080, Username: "user", PasswordEncrypted: passwordCiphertext},
		InputVersion: 3, IssuedAt: now, ExpiresAt: now.Add(time.Hour), TLSPolicy: "j1-direct-upstream-v1",
		Schedule: Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 2, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownNeutralBaseMS: int64(time.Second / time.Millisecond), CooldownNeutralMaxMS: int64(time.Minute / time.Millisecond), CooldownFailureBackoffMS: int64(time.Minute / time.Millisecond)},
	}).ToInput(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	if input.BaseURL != "https://upstream.example" || len(input.APIKeys) != 2 || input.Proxy == nil {
		t.Fatalf("mapped input = %#v", input)
	}
	proxyPlaintext, err := DecryptV1Envelope(secret, input.Proxy.Ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	var proxyPayload struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(proxyPlaintext, &proxyPayload); err != nil {
		t.Fatal(err)
	}
	if proxyPayload.URL != "socks5h://user:p%40ss@127.0.0.1:1080" {
		t.Fatalf("proxy = %q", proxyPayload.URL)
	}
}

func TestDirectInputNormalizesGPTProviderToOpenAIProtocol(t *testing.T) {
	secret := "j1-direct-input-secret"
	credentials, err := EncryptV1Envelope(secret, []byte(`{"api_key":"key"}`))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 8, 16, 0, 0, 0, 0, time.UTC)
	input, err := (DirectInput{
		Account:      DirectAccount{ID: "gpt-account", ConfigRevision: 1, DispatchRevision: 1, Provider: "gpt", Type: "api_key", Status: "pending_test", EndpointMode: "responses_sse", HealthModel: "gpt-test", CredentialsEncrypted: credentials},
		Binding:      DirectBinding{GroupID: "group-1", Enabled: true},
		InputVersion: 1, IssuedAt: now, ExpiresAt: now.Add(time.Hour), TLSPolicy: "j1-direct-upstream-v1", Schedule: Schedule{HealthIntervalMS: 1, FailureThreshold: 1, FailureRetryMS: 1, CooldownNeutralBaseMS: 1, CooldownNeutralMaxMS: 1, CooldownFailureBackoffMS: 1},
	}).ToInput(secret, now)
	if err != nil {
		t.Fatalf("GPT OpenAI-v1 provider must be accepted: %v", err)
	}
	if input.Provider != "openai" {
		t.Fatalf("normalized provider = %q, want openai", input.Provider)
	}
	if input.EndpointMode != "responses_sse" {
		t.Fatalf("endpoint mode = %q, want responses_sse", input.EndpointMode)
	}
}

func TestDirectInputRejectsIncompleteAuthorization(t *testing.T) {
	secret := "j1-direct-input-secret"
	credentials, err := EncryptV1Envelope(secret, []byte(`{"api_key":"key"}`))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 8, 16, 0, 0, 0, 0, time.UTC)
	_, err = (DirectInput{
		Account:       DirectAccount{ID: "account-1", ConfigRevision: 1, DispatchRevision: 1, Provider: "openai", Type: "api_key", Status: "active", Schedulable: true, EndpointMode: "chat_json", HealthModel: "gpt-test", CredentialsEncrypted: credentials},
		Authorization: &DirectAuthorization{ID: "auth-1", Status: "active", QuotaEligible: false},
		Source:        &DirectSource{ID: "source-1", ConfigRevision: 1, Provider: "openai", Type: "api_key", Status: "active", Schedulable: true, CredentialsEncrypted: credentials},
		Binding:       DirectBinding{GroupID: "group-1", Enabled: true, AuthorizationBindingID: "auth-1"},
		InputVersion:  1, IssuedAt: now, ExpiresAt: now.Add(time.Hour), TLSPolicy: "j1-direct-upstream-v1", Schedule: Schedule{HealthIntervalMS: 1, FailureThreshold: 1, FailureRetryMS: 1, CooldownNeutralBaseMS: 1, CooldownNeutralMaxMS: 1, CooldownFailureBackoffMS: 1},
	}).ToInput(secret, now)
	if err == nil {
		t.Fatal("expected authorization quota failure")
	}
}

func TestDirectInputAllowsOwnerCooldownFenceWithoutSourceRevision(t *testing.T) {
	secret := "j1-direct-input-secret"
	credentials, err := EncryptV1Envelope(secret, []byte(`{"api_key":"key"}`))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 8, 16, 0, 0, 0, 0, time.UTC)
	fence := &CooldownFence{ObservationStartedAt: now.Add(-time.Minute), Generation: "owner-generation"}
	cooldownUntil := now.Add(-time.Second)
	input, err := (DirectInput{
		Account:      DirectAccount{ID: "account-owner", ConfigRevision: 1, DispatchRevision: 1, Provider: "openai", Type: "api_key", Status: "temporary_unavailable", Schedulable: true, EndpointMode: "chat_json", HealthModel: "gpt-test", CredentialsEncrypted: credentials, CooldownUntil: &cooldownUntil, Cooldown: fence},
		Binding:      DirectBinding{GroupID: "group-1", Enabled: true},
		InputVersion: 1, IssuedAt: now, ExpiresAt: now.Add(time.Hour), TLSPolicy: "j1-direct-upstream-v1", Schedule: Schedule{HealthIntervalMS: 1, FailureThreshold: 1, FailureRetryMS: 1, CooldownNeutralBaseMS: 1, CooldownNeutralMaxMS: 1, CooldownFailureBackoffMS: 1},
	}).ToInput(secret, now)
	if err != nil {
		t.Fatalf("owner cooldown input must be accepted: %v", err)
	}
	if !validCooldownFence(input.Cooldown, input) {
		t.Fatal("owner cooldown fence without source revision must remain valid")
	}
}

func TestDirectInputRejectsUnavailableProxy(t *testing.T) {
	secret := "j1-direct-input-secret"
	credentials, err := EncryptV1Envelope(secret, []byte(`{"api_key":"key"}`))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 8, 16, 0, 0, 0, 0, time.UTC)
	base := DirectInput{
		Account:      DirectAccount{ID: "account-proxy", ConfigRevision: 1, DispatchRevision: 1, Provider: "openai", Type: "api_key", Status: "active", Schedulable: true, EndpointMode: "chat_json", HealthModel: "gpt-test", CredentialsEncrypted: credentials},
		Binding:      DirectBinding{GroupID: "group-1", Enabled: true},
		InputVersion: 1, IssuedAt: now, ExpiresAt: now.Add(time.Hour), TLSPolicy: "j1-direct-upstream-v1", Schedule: Schedule{HealthIntervalMS: 1, FailureThreshold: 1, FailureRetryMS: 1, CooldownNeutralBaseMS: 1, CooldownNeutralMaxMS: 1, CooldownFailureBackoffMS: 1},
	}
	for _, proxy := range []*DirectProxy{
		{ID: "disabled", Enabled: false, Type: "http", Host: "127.0.0.1", Port: 8080},
		{ID: "bad-password", Enabled: true, Type: "http", Host: "127.0.0.1", Port: 8080, Username: "user", PasswordEncrypted: "not-an-envelope"},
	} {
		candidate := base
		candidate.Proxy = proxy
		if _, err := candidate.ToInput(secret, now); err == nil {
			t.Fatalf("proxy %q must fail closed", proxy.ID)
		}
	}
}

func TestDirectInputAllowsAuthorizedCooldownFenceWithSourceRevision(t *testing.T) {
	secret := "j1-direct-input-secret"
	credentials, err := EncryptV1Envelope(secret, []byte(`{"api_key":"key"}`))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 8, 16, 0, 0, 0, 0, time.UTC)
	sourceRevision := int64(7)
	cooldownUntil := now.Add(-time.Second)
	fence := &CooldownFence{ObservationStartedAt: now.Add(-time.Minute), Generation: "authorized-generation", SourceConfigRevision: &sourceRevision}
	input, err := (DirectInput{
		Account:       DirectAccount{ID: "authorized-cooldown", ConfigRevision: 1, DispatchRevision: 1, Provider: "openai", Type: "api_key", Status: "temporary_unavailable", Schedulable: true, EndpointMode: "chat_json", HealthModel: "gpt-test", CredentialsEncrypted: credentials, CooldownUntil: &cooldownUntil, Cooldown: fence},
		Authorization: &DirectAuthorization{ID: "auth-1", Status: "active", QuotaEligible: true},
		Source:        &DirectSource{ID: "source-1", ConfigRevision: sourceRevision, Provider: "openai", Type: "api_key", Status: "active", Schedulable: true, CredentialsEncrypted: credentials},
		Binding:       DirectBinding{GroupID: "group-1", Enabled: true, AuthorizationBindingID: "auth-1"},
		InputVersion:  1, IssuedAt: now, ExpiresAt: now.Add(time.Hour), TLSPolicy: "j1-direct-upstream-v1", Schedule: Schedule{HealthIntervalMS: 1, FailureThreshold: 1, FailureRetryMS: 1, CooldownNeutralBaseMS: 1, CooldownNeutralMaxMS: 1, CooldownFailureBackoffMS: 1},
	}).ToInput(secret, now)
	if err != nil {
		t.Fatalf("authorized cooldown input must be accepted: %v", err)
	}
	if !validCooldownFence(input.Cooldown, input) {
		t.Fatal("authorized cooldown fence must retain source revision")
	}
}
