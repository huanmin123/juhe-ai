package proxylatency

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ExecutorOptions only contains per-process runtime material. CredentialSecret
// is used to decrypt the Node-compatible envelope in memory and is never
// copied into an outcome, error, log, or Store record.
type ExecutorOptions struct {
	CredentialSecret string
	Timeout          time.Duration
	Now              func() time.Time
}

// ExecuteIssuedInput runs one already-issued proxy-latency request. It has no
// scheduling behavior and does not contact any Node service: the Store is the
// sole authority for input identity, fences, and outcome persistence.
//
// A committed replay is returned without another upstream request. All first
// execution failures before an upstream item has run (input, lease, envelope,
// cancellation) return an error and never synthesize a committed outcome.
func ExecuteIssuedInput(ctx context.Context, store *Store, owner OwnerLease, proxy ProxyLease, input IssuedInput, options ExecutorOptions) (outcome Outcome, committed bool, runErr error) {
	if store == nil {
		return Outcome{}, false, errors.New("J3a executor Store 不可用")
	}
	if err := ctx.Err(); err != nil {
		return Outcome{}, false, err
	}
	if options.Timeout <= 0 {
		return Outcome{}, false, errors.New("J3a executor timeout 无效")
	}
	resolved, claimToken, replay, err := store.AdmitExecution(ctx, owner, proxy, input)
	if err != nil {
		return Outcome{}, false, err
	}
	if replay != nil {
		return *replay, false, nil
	}
	input = resolved
	defer func() {
		releaseCtx, releaseCancel := boundedReleaseContext(ctx)
		release := store.releaseExecutionClaim
		if release == nil {
			release = store.ReleaseExecutionClaim
		}
		releaseErr := release(releaseCtx, input.RequestID, claimToken)
		releaseCancel()
		if releaseErr == nil {
			return
		}
		wrapped := fmt.Errorf("J3a execution claim release failed: %w", releaseErr)
		if runErr == nil {
			outcome = Outcome{}
			committed = false
			runErr = wrapped
			return
		}
		runErr = errors.Join(runErr, wrapped)
	}()
	proxyURL, err := proxyURLForIssuedInput(input, options.CredentialSecret)
	if err != nil {
		return Outcome{}, false, err
	}
	defer clearProxyURL(&proxyURL)
	// The execution claim is fenced by the issued input expiry. Every probe
	// must use a timeout no longer than the remaining input validity window;
	// otherwise a claim could expire while its upstream request is still live.
	now := time.Now
	if options.Now != nil {
		now = options.Now
	}
	// observed_at is the beginning of the probe window, not its persistence
	// time. The future Node projector uses this for its last_tested_at CAS.
	observedAt := now().UTC()

	items := make([]ItemResult, 0, len(input.Targets))
	for _, target := range input.Targets {
		if err := ctx.Err(); err != nil {
			return Outcome{}, false, err
		}
		probeTimeout := options.Timeout
		remaining := input.ExpiresAt.Sub(now().UTC())
		if remaining <= 0 {
			return Outcome{}, false, ErrInputFence
		}
		if remaining < probeTimeout {
			probeTimeout = remaining
		}
		// ProbeItem validates each target independently. A bad target or an
		// upstream transport failure therefore becomes one sanitized item and
		// cannot prevent the later targets from being attempted.
		result := ProbeItem(ctx, ProbeRequest{TargetURL: target.URL, ProxyURL: proxyURL, Timeout: probeTimeout})
		// Provider/profile identity is needed for the future projector, while
		// target URLs remain input-only and are never persisted in outcomes.
		result.Provider = target.Provider
		result.ProfileID = target.ProfileID
		items = append(items, result)
	}
	if err := ctx.Err(); err != nil {
		return Outcome{}, false, err
	}
	outcome = Outcome{
		OutcomeID:           stableOutcomeID(input.RequestID),
		RequestID:           input.RequestID,
		ProxyID:             input.ProxyID,
		ObservedAt:          observedAt,
		InputVersion:        input.InputVersion,
		ConfigRevision:      input.ConfigRevision,
		Trigger:             input.Trigger,
		OwnerFenceToken:     owner.FenceToken,
		ProxyFenceToken:     proxy.FenceToken,
		OverallStatus:       SummarizeItems(items),
		Items:               items,
		executionClaimToken: claimToken,
	}
	committed, err = store.AppendOutcome(ctx, owner, proxy, outcome)
	if err != nil {
		return Outcome{}, false, err
	}
	return outcome, committed, nil
}

func proxyURLForIssuedInput(input IssuedInput, secret string) (string, error) {
	if !validProxyLatencyType(input.ProxyType) || strings.TrimSpace(input.ProxyHost) == "" || input.ProxyPort < 1 || input.ProxyPort > 65535 {
		return "", errors.New("J3a executor proxy 输入无效")
	}
	if input.ProxyPassword != nil && strings.TrimSpace(input.ProxyUsername) == "" {
		// Node's URL builder only creates credentials when username is present;
		// never turn a password-only envelope into the invalid :password@ form.
		return "", errors.New("J3a executor proxy password 缺少 username")
	}
	proxyURL := &url.URL{Scheme: input.ProxyType, Host: net.JoinHostPort(input.ProxyHost, strconv.Itoa(input.ProxyPort))}
	if input.ProxyPassword != nil {
		password, err := decryptProxyPasswordV1(secret, *input.ProxyPassword)
		if err != nil {
			return "", err
		}
		defer clearString(&password)
		proxyURL.User = url.UserPassword(input.ProxyUsername, password)
	} else if input.ProxyUsername != "" {
		proxyURL.User = url.User(input.ProxyUsername)
	}
	value := proxyURL.String()
	if _, err := newProxyTransport(value, time.Second); err != nil {
		clearProxyURL(&value)
		return "", errors.New("J3a executor proxy URL 无效")
	}
	return value, nil
}

func decryptProxyPasswordV1(secret string, envelope CredentialEnvelope) (string, error) {
	if strings.TrimSpace(secret) == "" || envelope.Kind != "proxy_password" || !validProxyLatencyEnvelope(envelope.Ciphertext) {
		return "", errors.New("J3a executor proxy password 凭据不可用")
	}
	parts := strings.Split(envelope.Ciphertext, ":")
	decode := func(value string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(value) }
	iv, err := decode(parts[1])
	if err != nil {
		return "", errors.New("J3a executor proxy password 凭据不可用")
	}
	tag, err := decode(parts[2])
	if err != nil {
		return "", errors.New("J3a executor proxy password 凭据不可用")
	}
	ciphertext, err := decode(parts[3])
	if err != nil || len(iv) != 12 || len(tag) != 16 {
		return "", errors.New("J3a executor proxy password 凭据不可用")
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", errors.New("J3a executor proxy password 凭据不可用")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", errors.New("J3a executor proxy password 凭据不可用")
	}
	plaintext, err := gcm.Open(nil, iv, append(ciphertext, tag...), nil)
	if err != nil {
		return "", errors.New("J3a executor proxy password 凭据不可用")
	}
	defer clearBytes(plaintext)
	var payload struct {
		Password *string `json:"password"`
	}
	if err := json.Unmarshal(plaintext, &payload); err != nil || payload.Password == nil {
		return "", errors.New("J3a executor proxy password 凭据不可用")
	}
	return *payload.Password, nil
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func clearString(value *string) {
	if value != nil {
		*value = ""
	}
}

func clearProxyURL(value *string) {
	clearString(value)
}
