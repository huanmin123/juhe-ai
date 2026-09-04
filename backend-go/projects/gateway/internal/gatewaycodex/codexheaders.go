package gatewaycodex

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Port of the gateway-owned slice of
// adapters/gpt-codex/usage.service.ts (parseOpenAICodexUsageHeaders) plus
// runtime/account-effects.ts persistOpenAICodexHeadersIfNeeded.
//
// The Node db-service side (persistOpenAICodexUsageHeaders + the
// record-maintenance job payload) is not reachable from the gateway import
// chain; the gateway only forwards the raw headers through a sink seam.

// OpenAICodexUsageSnapshot mirrors OpenAICodexUsageSnapshot.
type OpenAICodexUsageSnapshot struct {
	PrimaryUsedPercent          *float64
	PrimaryResetAfterSeconds    *float64
	PrimaryWindowMinutes        *float64
	SecondaryUsedPercent        *float64
	SecondaryResetAfterSeconds  *float64
	SecondaryWindowMinutes      *float64
	PrimaryOverSecondaryPercent *float64
	UpdatedAt                   string
}

// ParseOpenAICodexUsageHeaders mirrors parseOpenAICodexUsageHeaders. nil
// headers mirrors the missing-headers case; a snapshot without any codex
// header reads as undefined like the Node hasData flag.
func ParseOpenAICodexUsageHeaders(headers http.Header, now time.Time) *OpenAICodexUsageSnapshot {
	if headers == nil {
		return nil
	}
	snapshot := &OpenAICodexUsageSnapshot{UpdatedAt: now.UTC().Format("2006-01-02T15:04:05.000Z07:00")}
	hasData := false
	assignNumber := func(key string, apply func(value float64)) {
		value := numberHeader(headers, key)
		if value == nil {
			return
		}
		apply(*value)
		hasData = true
	}

	assignNumber("x-codex-primary-used-percent", func(value float64) { snapshot.PrimaryUsedPercent = &value })
	assignNumber("x-codex-primary-reset-after-seconds", func(value float64) { truncated := math.Trunc(value); snapshot.PrimaryResetAfterSeconds = &truncated })
	assignNumber("x-codex-primary-window-minutes", func(value float64) { truncated := math.Trunc(value); snapshot.PrimaryWindowMinutes = &truncated })
	assignNumber("x-codex-secondary-used-percent", func(value float64) { snapshot.SecondaryUsedPercent = &value })
	assignNumber("x-codex-secondary-reset-after-seconds", func(value float64) { truncated := math.Trunc(value); snapshot.SecondaryResetAfterSeconds = &truncated })
	assignNumber("x-codex-secondary-window-minutes", func(value float64) { truncated := math.Trunc(value); snapshot.SecondaryWindowMinutes = &truncated })
	assignNumber("x-codex-primary-over-secondary-limit-percent", func(value float64) { snapshot.PrimaryOverSecondaryPercent = &value })

	if !hasData {
		return nil
	}
	return snapshot
}

// numberHeader mirrors numberHeader(headers, key): the first value of the
// case-insensitive header, parsed as a finite number.
func numberHeader(headers http.Header, key string) *float64 {
	values := headers.Values(key)
	if len(values) == 0 || values[0] == "" {
		return nil
	}
	return numberValue(values[0])
}

// numberValue mirrors numberValue: Number(value) with the finite check.
func numberValue(value string) *float64 {
	text := strings.TrimSpace(value)
	if text == "" {
		return nil
	}
	number, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return nil
	}
	return &number
}

// CodexUsageHeadersDispatcher mirrors the fire-and-forget
// requestGatewayDbService({type: 'persist_openai_codex_usage_headers'})
// call. Implementations own the error handling (the Node catch logs a warn
// with event gateway_codex_usage_snapshot_side_effect_failed).
type CodexUsageHeadersDispatcher interface {
	PersistOpenAICodexUsageHeaders(ctx context.Context, accountID string, headers http.Header, source string)
}

// PersistOpenAICodexHeadersIfNeeded mirrors persistOpenAICodexHeadersIfNeeded:
// only OAuth OpenAI-protocol accounts with codex usage headers dispatch the
// side effect. The dispatch is fire-and-forget; ineligible accounts and
// header-only snapshots without codex data return silently.
func PersistOpenAICodexHeadersIfNeeded(
	ctx context.Context,
	account gatewayruntimecache.OpenAIAccountSecret,
	headers http.Header,
	source string,
	clock Clock,
	dispatcher CodexUsageHeadersDispatcher,
) {
	if account.Type != "oauth" || !isOpenAIProtocolProfile(account) {
		return
	}
	if dispatcher == nil {
		return
	}
	if ParseOpenAICodexUsageHeaders(headers, nowOrWall(clock)) == nil {
		return
	}
	dispatcher.PersistOpenAICodexUsageHeaders(ctx, account.ID, headers, source)
}

func nowOrWall(clock Clock) time.Time {
	if clock == nil {
		return time.Now()
	}
	return clock.Now()
}
