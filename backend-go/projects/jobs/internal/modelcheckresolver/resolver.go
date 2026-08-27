// Package modelcheckresolver resolves the credential-free J3b execution
// snapshot into an in-memory upstream client. It has no Node, RPC, or
// database dependency: the caller must freeze and validate the snapshot
// before issuing model-check input.
package modelcheckresolver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckexecutor"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckinput"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

// Snapshot is the J3b-owned, credential-encrypted target captured at input
// issuance. Model, protocol and profile revision must be the exact values
// recorded in the issued input; health-probe defaults are intentionally not
// inferred here.
type Snapshot struct {
	AccountID                 string
	ConfigRevision            string
	ProtocolProfileID         string
	ProtocolProfileRevision   string
	EndpointFingerprint       string
	CredentialEnvelopeRef     string
	ProxyConfigurationVersion string
	Endpoint                  string
	Protocol                  modelcheckprofile.Protocol
	Model                     string
	Prompt                    string
	Stream                    bool
	MaxOutputTokens           int
	Provider                  string
	CredentialType            string
	Credential                accounthealth.CredentialEnvelope
	OAuthQuotaProjectID       string
	Proxy                     *accounthealth.CredentialEnvelope
	Timeout                   time.Duration
	MaxResponseBytes          int64
}

type Resolver struct {
	inputs map[string]Snapshot
	secret string
}

func New(inputs []Snapshot, secret string) (*Resolver, error) {
	if strings.TrimSpace(secret) == "" {
		return nil, errors.New("model check resolver credential secret is required")
	}
	values := make(map[string]Snapshot, len(inputs))
	for _, input := range inputs {
		if err := validateSnapshot(input); err != nil {
			return nil, err
		}
		if _, exists := values[input.AccountID]; exists {
			return nil, fmt.Errorf("model check resolver duplicate account %s", input.AccountID)
		}
		values[input.AccountID] = input
	}
	return &Resolver{inputs: values, secret: secret}, nil
}

func (r *Resolver) Resolve(_ context.Context, request modelcheckexecutor.ResolutionRequest) (modelcheckexecutor.ResolvedTarget, error) {
	if r == nil {
		return modelcheckexecutor.ResolvedTarget{}, errors.New("model check resolver is nil")
	}
	account := request.Account
	accountID := strings.TrimSpace(account.ID)
	input, ok := r.inputs[accountID]
	if !ok {
		return modelcheckexecutor.ResolvedTarget{}, fmt.Errorf("model check account %s not found", accountID)
	}
	if !matchesAccountSnapshot(input, account) {
		return modelcheckexecutor.ResolvedTarget{}, errors.New("model check account execution snapshot is stale")
	}
	header, err := credentialHeader(r.secret, input)
	if err != nil {
		return modelcheckexecutor.ResolvedTarget{}, err
	}
	proxyURL, err := proxyURLFromSnapshot(r.secret, input)
	if err != nil {
		return modelcheckexecutor.ResolvedTarget{}, err
	}
	client, err := upstreamhttp.SharedClient(proxyURL, upstreamhttp.TransportOptions{ResponseHeaderTimeout: timeoutFor(input)})
	if err != nil {
		return modelcheckexecutor.ResolvedTarget{}, fmt.Errorf("model check proxy client: %w", err)
	}
	return modelcheckexecutor.ResolvedTarget{
		ConfigRevision:          input.ConfigRevision,
		ProtocolProfileID:       input.ProtocolProfileID,
		ProtocolProfileRevision: input.ProtocolProfileRevision,
		Endpoint:                input.Endpoint,
		Protocol:                input.Protocol,
		Model:                   input.Model,
		Prompt:                  input.Prompt,
		Stream:                  input.Stream,
		MaxOutputTokens:         input.MaxOutputTokens,
		Headers:                 header,
		Client:                  client,
		Timeout:                 timeoutFor(input),
		MaxResponseBytes:        maxResponseBytesFor(input),
	}, nil
}

func validateSnapshot(input Snapshot) error {
	if strings.TrimSpace(input.AccountID) == "" {
		return errors.New("model check resolver input account id is required")
	}
	if strings.TrimSpace(input.ConfigRevision) == "" {
		return fmt.Errorf("model check resolver account %s config revision is required", input.AccountID)
	}
	if strings.TrimSpace(input.ProtocolProfileID) == "" || strings.TrimSpace(input.ProtocolProfileRevision) == "" {
		return fmt.Errorf("model check resolver account %s protocol profile snapshot is required", input.AccountID)
	}
	if strings.TrimSpace(input.Endpoint) == "" || input.Protocol == "" || strings.TrimSpace(input.Model) == "" {
		return fmt.Errorf("model check resolver account %s target snapshot is incomplete", input.AccountID)
	}
	if strings.TrimSpace(input.EndpointFingerprint) == "" || strings.TrimSpace(input.CredentialEnvelopeRef) == "" || strings.TrimSpace(input.ProxyConfigurationVersion) == "" {
		return fmt.Errorf("model check resolver account %s durable identity snapshot is incomplete", input.AccountID)
	}
	if strings.TrimSpace(input.Provider) == "" || strings.TrimSpace(input.CredentialType) == "" || strings.TrimSpace(input.Credential.Ciphertext) == "" {
		return fmt.Errorf("model check resolver account %s credential snapshot is incomplete", input.AccountID)
	}
	switch input.Protocol {
	case modelcheckprofile.ProtocolOpenAIResponses, modelcheckprofile.ProtocolOpenAIChat, modelcheckprofile.ProtocolAnthropic, modelcheckprofile.ProtocolGeminiNative:
	default:
		return fmt.Errorf("model check resolver account %s protocol is unsupported", input.AccountID)
	}
	return nil
}

func matchesAccountSnapshot(input Snapshot, account modelcheckinput.AccountSnapshot) bool {
	return strings.TrimSpace(input.AccountID) == strings.TrimSpace(account.ID) &&
		input.ConfigRevision == strings.TrimSpace(account.ConfigRevision) &&
		input.ProtocolProfileID == strings.TrimSpace(account.ProtocolProfileID) &&
		input.ProtocolProfileRevision == strings.TrimSpace(account.ProtocolProfileRevision) &&
		input.EndpointFingerprint == strings.TrimSpace(account.EndpointFingerprint) &&
		input.Model == strings.TrimSpace(account.MappedUpstreamModel) &&
		input.CredentialEnvelopeRef == strings.TrimSpace(account.CredentialEnvelopeRef) &&
		input.ProxyConfigurationVersion == strings.TrimSpace(account.ProxyConfigurationVersion)
}

func credentialHeader(secret string, input Snapshot) (http.Header, error) {
	plain, err := accounthealth.DecryptV1Envelope(secret, input.Credential.Ciphertext)
	if err != nil {
		return nil, errors.New("model check credential envelope is unavailable")
	}
	token, err := credentialToken(plain)
	if err != nil {
		return nil, err
	}
	return authHeader(input, token), nil
}

func credentialToken(plain []byte) (string, error) {
	var fields map[string]any
	if json.Unmarshal(plain, &fields) == nil {
		for _, key := range []string{"api_key", "access_token", "token"} {
			if value, ok := fields[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value), nil
			}
		}
		if values, ok := fields["api_keys"].([]any); ok {
			for _, value := range values {
				if key, ok := value.(string); ok && strings.TrimSpace(key) != "" {
					return strings.TrimSpace(key), nil
				}
			}
		}
	}
	if strings.TrimSpace(string(plain)) == "" {
		return "", errors.New("model check credential is empty")
	}
	return strings.TrimSpace(string(plain)), nil
}

func authHeader(input Snapshot, token string) http.Header {
	header := make(http.Header)
	switch input.Protocol {
	case modelcheckprofile.ProtocolAnthropic:
		header.Set("anthropic-version", "2023-06-01")
		if input.CredentialType == "oauth" {
			header.Set("Authorization", "Bearer "+token)
		} else {
			header.Set("x-api-key", token)
		}
	case modelcheckprofile.ProtocolGeminiNative:
		if input.CredentialType == "google_oauth" {
			header.Set("Authorization", "Bearer "+token)
			if quotaProject := strings.TrimSpace(input.OAuthQuotaProjectID); quotaProject != "" {
				header.Set("x-goog-user-project", quotaProject)
			}
		} else {
			header.Set("x-goog-api-key", token)
		}
	default:
		header.Set("Authorization", "Bearer "+token)
	}
	return header
}

func proxyURLFromSnapshot(secret string, input Snapshot) (string, error) {
	if input.Proxy == nil {
		return "", nil
	}
	plain, err := accounthealth.DecryptV1Envelope(secret, input.Proxy.Ciphertext)
	if err != nil {
		return "", errors.New("model check proxy envelope is unavailable")
	}
	var fields map[string]string
	if err := json.Unmarshal(plain, &fields); err != nil || strings.TrimSpace(fields["url"]) == "" {
		return "", errors.New("model check proxy envelope is invalid")
	}
	return fields["url"], nil
}

func timeoutFor(input Snapshot) time.Duration {
	if input.Timeout > 0 {
		return input.Timeout
	}
	return 30 * time.Second
}

func maxResponseBytesFor(input Snapshot) int64 {
	if input.MaxResponseBytes > 0 {
		return input.MaxResponseBytes
	}
	return 2 << 20
}
