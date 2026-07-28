package gatewaycandidatewindow

import (
	"fmt"
	"strings"
	"time"
)

type SelectedAPIKey struct {
	secret      string
	fingerprint string
	index       int
}

func (k SelectedAPIKey) Secret() string      { return k.secret }
func (k SelectedAPIKey) Fingerprint() string { return k.fingerprint }
func (k SelectedAPIKey) Index() int          { return k.index }
func (SelectedAPIKey) String() string        { return "[REDACTED]" }
func (SelectedAPIKey) GoString() string      { return "[REDACTED]" }

// SelectProbeAPIKey returns the first currently eligible runtime key while
// preserving the original credential-array index used to compute its
// fingerprint. It is an account-level recovery-probe selection, not the
// normal gateway's RR/weighted dispatch policy or a distributed claim.
func SelectProbeAPIKey(candidate Candidate, now time.Time) (SelectedAPIKey, bool, error) {
	entries := credentialAPIKeys(candidate.Credentials.values)
	if len(entries) == 0 {
		return SelectedAPIKey{}, false, nil
	}
	byIndex := make(map[int]string, len(entries))
	for _, entry := range entries {
		byIndex[entry.index] = entry.key
	}
	seen := make(map[int]bool, len(candidate.APIKeyRuntime))
	for _, state := range candidate.APIKeyRuntime {
		if state.KeyIndex < 0 || seen[state.KeyIndex] {
			continue
		}
		seen[state.KeyIndex] = true
		secret, exists := byIndex[state.KeyIndex]
		if !exists {
			return SelectedAPIKey{}, false, fmt.Errorf("API key runtime index %d does not exist in hydrated credentials", state.KeyIndex)
		}
		fingerprint := strings.TrimSpace(state.KeyFingerprint)
		if fingerprint == "" {
			return SelectedAPIKey{}, false, fmt.Errorf("API key runtime index %d has no fingerprint", state.KeyIndex)
		}
		if probeKeyRuntimeAvailable(state, now) {
			return SelectedAPIKey{secret: secret, fingerprint: fingerprint, index: state.KeyIndex}, true, nil
		}
	}
	return SelectedAPIKey{}, false, nil
}

func probeKeyRuntimeAvailable(state APIKeyRuntime, now time.Time) bool {
	status := strings.ToLower(strings.TrimSpace(state.Status))
	if status == "" || status == "active" {
		return true
	}
	if status == "disabled" {
		return false
	}
	until, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(state.CooldownUntil))
	return err == nil && !until.After(now)
}
