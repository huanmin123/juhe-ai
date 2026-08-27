// Package modelchecksource freezes a business-reader candidate into the two
// J3b snapshots used by durable input and the in-memory executor. Database
// adapters are deliberately separate: they must first prove authorization and
// business eligibility, then pass the resulting immutable facts here.
package modelchecksource

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckinput"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckresolver"
)

// Request is the already-authenticated model-check target request. The
// reader must use SystemAccountID as a scope predicate, not merely filter the
// result after loading credentials.
type Request struct {
	SystemAccountID         string
	AccountID               string
	Model                   string
	AllowQualityIsolated    bool
	TrustedComparison       bool
	ComparisonAccountID     string
	ProtocolProfileRevision string
}

// Candidate contains only facts selected through a Go-owned business reader.
// Credential values remain encrypted; source never returns a plaintext token.
type Candidate struct {
	AccountID           string
	SystemAccountID     string
	TargetName          string
	TargetOwnerSystemID string
	GroupID             string
	ConfigRevision      string
	ProviderCode        string
	ProtocolProfileID   string
	ProtocolRevision    string
	Status              string
	Eligible            bool
	EndpointMode        string
	Endpoint            string
	CredentialType      string
	Credential          accounthealth.CredentialEnvelope
	CredentialRef       string
	Proxy               *accounthealth.CredentialEnvelope
	ProxyVersion        string
	OAuthQuotaProjectID string
	SupportedModels     []string
	ModelMappings       []ModelMapping
	Timeout             time.Duration
	MaxResponseBytes    int64
}

// ModelMapping represents a validated active mapping on the effective
// physical account. SourceModel is the requested account model; UpstreamModel
// is the model sent to the provider.
type ModelMapping struct {
	Enabled                bool
	SourceModel            string
	UpstreamModel          string
	SourceEndpointFamily   string
	UpstreamEndpointFamily string
}

// FrozenTarget keeps durable data and decryptable execution material separate.
// DurableAccount never contains endpoint, credentials, or proxy values.
type FrozenTarget struct {
	DurableAccount      modelcheckinput.AccountSnapshot
	Execution           modelcheckresolver.Snapshot
	TargetName          string
	TargetOwnerSystemID string
	GroupID             string
}

// Freeze validates the Node-equivalent account/profile/model boundary before
// a J3b input is issued. identitySecret is only used for opaque fingerprints;
// it never changes the encrypted credential envelope.
func Freeze(request Request, candidate Candidate, identitySecret string) (FrozenTarget, error) {
	if strings.TrimSpace(identitySecret) == "" {
		return FrozenTarget{}, errors.New("model check source identity secret is required")
	}
	if strings.TrimSpace(request.SystemAccountID) == "" || strings.TrimSpace(request.AccountID) == "" || strings.TrimSpace(request.Model) == "" {
		return FrozenTarget{}, errors.New("model check source request is incomplete")
	}
	if strings.TrimSpace(candidate.AccountID) != strings.TrimSpace(request.AccountID) || strings.TrimSpace(candidate.SystemAccountID) != strings.TrimSpace(request.SystemAccountID) {
		return FrozenTarget{}, errors.New("model check account does not belong to request scope")
	}
	if !candidate.Eligible || strings.EqualFold(strings.TrimSpace(candidate.Status), "disabled") || (strings.EqualFold(strings.TrimSpace(candidate.Status), "quality_isolated") && !request.AllowQualityIsolated) {
		return FrozenTarget{}, errors.New("model check account is unavailable")
	}
	profile, ok := modelcheckprofile.FindForModel(candidate.ProviderCode, candidate.ProtocolProfileID, request.Model)
	if !ok {
		return FrozenTarget{}, errors.New("model is not supported by the account provider protocol profile")
	}
	if !endpointModeMatches(profile.Protocol, candidate.EndpointMode) {
		return FrozenTarget{}, errors.New("model check endpoint mode does not match provider protocol profile")
	}
	upstreamModel, err := resolveUpstreamModel(profile, request.Model, candidate)
	if err != nil {
		return FrozenTarget{}, err
	}
	if strings.TrimSpace(candidate.ConfigRevision) == "" || strings.TrimSpace(candidate.ProtocolRevision) == "" || strings.TrimSpace(candidate.Endpoint) == "" || strings.TrimSpace(candidate.Credential.Ciphertext) == "" || strings.TrimSpace(candidate.CredentialType) == "" {
		return FrozenTarget{}, errors.New("model check account execution snapshot is incomplete")
	}
	credentialRef := strings.TrimSpace(candidate.CredentialRef)
	if credentialRef == "" {
		credentialRef = fingerprint(identitySecret, "credential", candidate.Credential.Ciphertext)
	}
	proxyVersion := strings.TrimSpace(candidate.ProxyVersion)
	if proxyVersion == "" {
		proxyVersion = "direct"
		if candidate.Proxy != nil {
			proxyVersion = fingerprint(identitySecret, "proxy", candidate.Proxy.Ciphertext)
		}
	}
	return FrozenTarget{
		DurableAccount: modelcheckinput.AccountSnapshot{
			ID:                        strings.TrimSpace(candidate.AccountID),
			ConfigRevision:            strings.TrimSpace(candidate.ConfigRevision),
			ProviderCode:              strings.TrimSpace(candidate.ProviderCode),
			ProtocolProfileID:         strings.TrimSpace(candidate.ProtocolProfileID),
			ProtocolProfileRevision:   strings.TrimSpace(candidate.ProtocolRevision),
			EndpointFingerprint:       fingerprint(identitySecret, "endpoint", candidate.Endpoint),
			MappedUpstreamModel:       upstreamModel,
			CredentialEnvelopeRef:     credentialRef,
			ProxyConfigurationVersion: proxyVersion,
		},
		Execution: modelcheckresolver.Snapshot{
			AccountID:                 strings.TrimSpace(candidate.AccountID),
			ConfigRevision:            strings.TrimSpace(candidate.ConfigRevision),
			ProtocolProfileID:         strings.TrimSpace(candidate.ProtocolProfileID),
			ProtocolProfileRevision:   strings.TrimSpace(candidate.ProtocolRevision),
			EndpointFingerprint:       fingerprint(identitySecret, "endpoint", candidate.Endpoint),
			CredentialEnvelopeRef:     credentialRef,
			ProxyConfigurationVersion: proxyVersion,
			Endpoint:                  strings.TrimSpace(candidate.Endpoint),
			Protocol:                  profile.Protocol,
			Model:                     upstreamModel,
			Prompt:                    "Reply with exactly: OK-MODEL-CHECK",
			Stream:                    strings.HasSuffix(strings.TrimSpace(candidate.EndpointMode), "_sse"),
			MaxOutputTokens:           32,
			Provider:                  strings.TrimSpace(candidate.ProviderCode),
			CredentialType:            strings.TrimSpace(candidate.CredentialType),
			Credential:                candidate.Credential,
			OAuthQuotaProjectID:       strings.TrimSpace(candidate.OAuthQuotaProjectID),
			Proxy:                     candidate.Proxy,
			Timeout:                   candidate.Timeout,
			MaxResponseBytes:          candidate.MaxResponseBytes,
		},
		TargetName:          strings.TrimSpace(candidate.TargetName),
		TargetOwnerSystemID: strings.TrimSpace(candidate.TargetOwnerSystemID),
		GroupID:             strings.TrimSpace(candidate.GroupID),
	}, nil
}

func resolveUpstreamModel(profile modelcheckprofile.ProtocolProfile, requested string, candidate Candidate) (string, error) {
	if len(candidate.SupportedModels) == 0 || contains(candidate.SupportedModels, requested) {
		return requested, nil
	}
	sourceFamily, ok := endpointModeFamily(candidate.EndpointMode)
	if !ok || !containsEndpointFamily(modelcheckprofile.SourceEndpointFamilies(profile), sourceFamily) {
		return "", errors.New("model check endpoint mode does not have an executable source family")
	}
	for _, mapping := range candidate.ModelMappings {
		if !mapping.Enabled || strings.TrimSpace(mapping.SourceModel) != requested || strings.TrimSpace(mapping.UpstreamModel) == "" || modelcheckprofile.EndpointFamily(mapping.SourceEndpointFamily) != sourceFamily {
			continue
		}
		if contains(candidate.SupportedModels, mapping.UpstreamModel) {
			if !mappingTransportCompatible(sourceFamily, modelcheckprofile.EndpointFamily(mapping.UpstreamEndpointFamily)) {
				return "", fmt.Errorf("model mapping %s requires an upstream endpoint-family conversion that Go model check does not implement", requested)
			}
			return strings.TrimSpace(mapping.UpstreamModel), nil
		}
	}
	return "", fmt.Errorf("account model restriction does not include %s", requested)
}

func endpointModeFamily(mode string) (modelcheckprofile.EndpointFamily, bool) {
	switch strings.TrimSpace(mode) {
	case "responses_json", "responses_sse":
		return modelcheckprofile.EndpointResponses, true
	case "chat_json", "chat_sse":
		return modelcheckprofile.EndpointChatCompletions, true
	case "messages_json", "messages_sse":
		return modelcheckprofile.EndpointMessages, true
	case "generate_content_json":
		return modelcheckprofile.EndpointGenerateContent, true
	case "generate_content_sse":
		return modelcheckprofile.EndpointStreamGenerate, true
	default:
		return "", false
	}
}

func mappingTransportCompatible(source, upstream modelcheckprofile.EndpointFamily) bool {
	return source == upstream || (source == modelcheckprofile.EndpointStreamGenerate && upstream == modelcheckprofile.EndpointGenerateContent)
}

func containsEndpointFamily(values []modelcheckprofile.EndpointFamily, wanted modelcheckprofile.EndpointFamily) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func endpointModeMatches(protocol modelcheckprofile.Protocol, mode string) bool {
	switch protocol {
	case modelcheckprofile.ProtocolOpenAIResponses:
		return mode == "responses_json" || mode == "responses_sse"
	case modelcheckprofile.ProtocolOpenAIChat:
		return mode == "chat_json" || mode == "chat_sse"
	case modelcheckprofile.ProtocolAnthropic:
		return mode == "messages_json" || mode == "messages_sse"
	case modelcheckprofile.ProtocolGeminiNative:
		return mode == "generate_content_json" || mode == "generate_content_sse"
	default:
		return false
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == strings.TrimSpace(wanted) {
			return true
		}
	}
	return false
}

func fingerprint(secret, label, value string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(label))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}
