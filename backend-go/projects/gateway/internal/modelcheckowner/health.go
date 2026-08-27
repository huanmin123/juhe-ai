package modelcheckowner

import (
	"context"
	"fmt"
	"time"
)

// HealthFact is the durable J3b health projection input. Raw upstream
// responses and credentials must never be placed here.
type HealthFact struct {
	AccountID, SystemAccountID, StatHour, RunID, ProviderCode, Model, Profile string
	ObservedAt                                                                time.Time
	Score, Threshold                                                          int
	Level, ErrorCode, ErrorMessage                                            string
}

// HealthReader is the narrow read-only contract that a future J3c consumer
// may depend on. It deliberately exposes no mutation method and requires an
// explicit account/hour scope for every lookup.
type HealthReader interface {
	ReadHealthFact(context.Context, string, string) (HealthFact, bool, error)
}

var _ HealthReader = (*Store)(nil)

// CompareLatestWins matches Node's predicate: observed_at first, then run ID.
func CompareLatestWins(candidate, current HealthFact) (int, error) {
	if candidate.AccountID == "" || candidate.StatHour == "" || candidate.RunID == "" || candidate.ObservedAt.IsZero() {
		return 0, fmt.Errorf("health fact identity is incomplete")
	}
	if current.AccountID != "" && (candidate.AccountID != current.AccountID || candidate.StatHour != current.StatHour) {
		return 0, fmt.Errorf("health fact scope mismatch")
	}
	if current.ObservedAt.IsZero() {
		return 1, nil
	}
	if candidate.ObservedAt.After(current.ObservedAt) {
		return 1, nil
	}
	if candidate.ObservedAt.Before(current.ObservedAt) {
		return -1, nil
	}
	if candidate.RunID > current.RunID {
		return 1, nil
	}
	if candidate.RunID < current.RunID {
		return -1, nil
	}
	return 0, nil
}
