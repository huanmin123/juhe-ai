package accounthealth

import (
	"encoding/json"
	"testing"
	"time"
)

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
		Account:      DirectAccount{ID: "gpt-account", ConfigRevision: 1, DispatchRevision: 1, Provider: "gpt", Type: "api_key", Status: "pending_test", EndpointMode: "chat_json", HealthModel: "gpt-test", CredentialsEncrypted: credentials},
		Binding:      DirectBinding{GroupID: "group-1", Enabled: true},
		InputVersion: 1, IssuedAt: now, ExpiresAt: now.Add(time.Hour), TLSPolicy: "j1-direct-upstream-v1", Schedule: Schedule{HealthIntervalMS: 1, FailureThreshold: 1, FailureRetryMS: 1, CooldownNeutralBaseMS: 1, CooldownNeutralMaxMS: 1, CooldownFailureBackoffMS: 1},
	}).ToInput(secret, now)
	if err != nil {
		t.Fatalf("GPT OpenAI-v1 provider must be accepted: %v", err)
	}
	if input.Provider != "openai" {
		t.Fatalf("normalized provider = %q, want openai", input.Provider)
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
