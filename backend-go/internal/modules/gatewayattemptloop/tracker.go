package gatewayattemptloop

import (
	"strings"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	protocolgateway "juhe-ai/backend-go/internal/protocols/gateway"
)

// AttemptTracker is request-local state. It is not safe to share across
// requests; a future group executor must explicitly pass the same tracker to
// each leg of one request.
type AttemptTracker struct {
	runtimes, physical, protocols, keys map[string]struct{}
	runtimePhysical                     map[string]string
}

func NewAttemptTracker() *AttemptTracker {
	return &AttemptTracker{make(map[string]struct{}), make(map[string]struct{}), make(map[string]struct{}), make(map[string]struct{}), make(map[string]string)}
}

func (t *AttemptTracker) CanClaim(c gatewaycandidatewindow.Candidate, index int, request protocolgateway.RequestShape) bool {
	return t.canClaim(c, index, request, false)
}
func (t *AttemptTracker) Claim(c gatewaycandidatewindow.Candidate, index int, request protocolgateway.RequestShape) bool {
	return t.canClaim(c, index, request, true)
}
func (t *AttemptTracker) canClaim(c gatewaycandidatewindow.Candidate, index int, request protocolgateway.RequestShape, record bool) bool {
	if t == nil {
		return false
	}
	runtime, physical := runtimeKey(c), physicalCredentialKey(c)
	if runtime == "" || physical == "" {
		return false
	}
	fingerprint, apiKey := keyFingerprint(c, index)
	if apiKey && fingerprint != "" {
		if _, exists := t.keys[fingerprint]; exists {
			return false
		}
	}
	if previous, seen := t.runtimePhysical[runtime]; seen {
		if previous != physical || !apiKey || fingerprint == "" {
			return false
		}
		if record {
			t.keys[fingerprint] = struct{}{}
		}
		return true
	}
	protocol := protocolModelKey(c, request)
	if _, exists := t.physical[physical]; exists {
		return false
	}
	if _, exists := t.protocols[protocol]; exists {
		return false
	}
	if record {
		t.runtimes[runtime] = struct{}{}
		t.physical[physical] = struct{}{}
		t.protocols[protocol] = struct{}{}
		t.runtimePhysical[runtime] = physical
		if fingerprint != "" {
			t.keys[fingerprint] = struct{}{}
		}
	}
	return true
}
func keyFingerprint(c gatewaycandidatewindow.Candidate, index int) (string, bool) {
	if !strings.EqualFold(effectiveCandidateType(c), "api_key") {
		return "", false
	}
	value := ""
	for _, state := range c.APIKeyRuntime {
		if state.KeyIndex == index {
			if value != "" {
				return "", true
			}
			value = strings.TrimSpace(state.KeyFingerprint)
		}
	}
	return value, true
}
func physicalCredentialKey(c gatewaycandidatewindow.Candidate) string {
	if value := strings.TrimSpace(c.Projection.ResourceAccountID); value != "" {
		return value
	}
	return strings.TrimSpace(c.Projection.AccountID)
}
func runtimeKey(c gatewaycandidatewindow.Candidate) string {
	return strings.TrimSpace(c.Projection.AccountID)
}
func protocolModelKey(c gatewaycandidatewindow.Candidate, request protocolgateway.RequestShape) string {
	return runtimeKey(c) + "\x00" + strings.TrimSpace(c.Projection.ResourceProtocolCode) + "\x00" + strings.TrimSpace(c.Projection.ResourceProtocolVersion) + "\x00" + strings.TrimSpace(request.Model)
}
