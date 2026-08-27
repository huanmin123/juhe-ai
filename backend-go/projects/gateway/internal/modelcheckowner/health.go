package modelcheckowner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// HealthFact is the durable J3b health projection input. Raw upstream
// responses and credentials must never be placed here.
type HealthFact struct {
	AccountID, SystemAccountID, StatHour, RunID, ProviderCode, Model, Profile, ScheduleID string
	PolicyRevision, AccountConfigRevision                                                 string
	ObservedAt                                                                            time.Time
	Score, Threshold, RecoveryIntervalMinutes                                             int
	Level, ErrorCode, ErrorMessage, PenaltyAction                                         string
}

// HealthReader is the narrow read-only contract that a future J3c consumer
// may depend on. It deliberately exposes no mutation method and requires an
// explicit account/hour scope for every lookup.
type HealthReader interface {
	ReadHealthFact(context.Context, string, string) (HealthFact, bool, error)
}

var _ HealthReader = (*Store)(nil)

// QualityProjector is the only path that may publish a J3b health fact. It
// requires a fully formed, trusted aggregate and records failed publication
// as retryable state on the same run.
type QualityProjector struct {
	Store       *Store
	Enforcement EnforcementApplier
}

// EnforcementApplier is the Business-owner mutation port for a formed
// quality failure. Implementations must perform account status and
// account_quality_enforcements CAS in one transaction using frozen revisions.
type EnforcementApplier interface {
	Apply(context.Context, QualityEnforcement) error
}

type QualityEnforcement struct {
	AccountID, SystemAccountID, RunID, ProviderCode, Model, Profile, ScheduleID string
	PolicyRevision, AccountConfigRevision                                       string
	Action                                                                      string
	Score, Threshold, RecoveryIntervalMinutes                                   int
	Message                                                                     string
	OccurredAt                                                                  time.Time
}

type HealthSyncRetryExecutor struct {
	Projector *QualityProjector
}

func (e *HealthSyncRetryExecutor) Execute(ctx context.Context, task ScheduleTask) error {
	if e == nil || e.Projector == nil || e.Projector.Store == nil {
		return errors.New("J3b health retry executor is not initialized")
	}
	if task.Kind != SchedulerHealthRetry {
		return fmt.Errorf("J3b health retry executor received %s", task.Kind)
	}
	var payload struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(task.Payload, &payload); err != nil || payload.RunID == "" {
		return errors.New("J3b health retry task payload lacks runId")
	}
	retries, err := e.Projector.Store.ListHealthSyncRetries(ctx, 1000)
	if err != nil {
		return err
	}
	for _, retry := range retries {
		if retry.RunID != payload.RunID {
			continue
		}
		return e.Projector.Project(ctx, retry.RunID, EvidenceAggregate{Formed: retry.EvidenceFormed, TrustFormed: retry.TrustFormed}, HealthFact{AccountID: retry.AccountID, SystemAccountID: retry.SystemAccountID, StatHour: retry.StatHour, RunID: retry.RunID, ProviderCode: retry.ProviderCode, Model: retry.Model, Profile: retry.Profile, ScheduleID: retry.ScheduleID, PolicyRevision: retry.PolicyRevision, AccountConfigRevision: retry.AccountConfigRevision, PenaltyAction: retry.PenaltyAction, RecoveryIntervalMinutes: retry.RecoveryIntervalMinutes, ObservedAt: retry.ObservedAt, Score: retry.Score, Threshold: retry.Threshold, Level: retry.Level})
	}
	return fmt.Errorf("J3b health retry run %s not found", payload.RunID)
}

func (p *QualityProjector) Project(ctx context.Context, runID string, aggregate EvidenceAggregate, fact HealthFact) error {
	if p == nil || p.Store == nil {
		return errors.New("J3b quality projector is not initialized")
	}
	if !aggregate.Formed || !aggregate.TrustFormed {
		if strings.TrimSpace(runID) != "" {
			_ = p.Store.MarkHealthSync(ctx, strings.TrimSpace(runID), "failed")
		}
		return errors.New("J3b evidence is not formed; health projection is denied")
	}
	runID = strings.TrimSpace(runID)
	if runID == "" || strings.TrimSpace(fact.RunID) == "" || fact.RunID != runID {
		// Do not mutate any run on an identity mismatch: the caller supplied
		// contradictory identities, so marking runID failed could poison an
		// unrelated durable run and make retries replay the wrong fact.
		return errors.New("J3b health projection run identity mismatch")
	}
	if strings.TrimSpace(fact.AccountID) == "" || strings.TrimSpace(fact.SystemAccountID) == "" || strings.TrimSpace(fact.ProviderCode) == "" || strings.TrimSpace(fact.Model) == "" || strings.TrimSpace(fact.Profile) == "" || !validHealthStatHour(fact.StatHour) || fact.ObservedAt.IsZero() || fact.Threshold < 40 || fact.Threshold > 100 || fact.Score < 0 || fact.Score > 100 {
		_ = p.Store.MarkHealthSync(ctx, runID, "failed")
		return errors.New("J3b health projection scope is incomplete")
	}
	if fact.Score < fact.Threshold {
		if p.Enforcement == nil {
			_ = p.Store.MarkHealthSync(ctx, runID, "failed")
			return errors.New("J3b quality enforcement owner is not configured")
		}
		action := fact.PenaltyAction
		if action == "" {
			action = "quality_isolate"
		}
		if action != "disable" && action != "fallback" && action != "quality_isolate" {
			_ = p.Store.MarkHealthSync(ctx, runID, "failed")
			return errors.New("J3b quality enforcement action is invalid")
		}
		if err := p.Enforcement.Apply(ctx, QualityEnforcement{AccountID: fact.AccountID, SystemAccountID: fact.SystemAccountID, RunID: fact.RunID, ProviderCode: fact.ProviderCode, Model: fact.Model, Profile: fact.Profile, PolicyRevision: fact.PolicyRevision, AccountConfigRevision: fact.AccountConfigRevision, ScheduleID: fact.ScheduleID, Score: fact.Score, Threshold: fact.Threshold, RecoveryIntervalMinutes: fact.RecoveryIntervalMinutes, Action: action, OccurredAt: fact.ObservedAt, Message: fact.ErrorMessage}); err != nil {
			_ = p.Store.MarkHealthSync(ctx, runID, "failed")
			return fmt.Errorf("apply J3b quality enforcement: %w", err)
		}
	}
	if _, err := p.Store.ApplyHealthFact(ctx, fact); err != nil {
		_ = p.Store.MarkHealthSync(ctx, runID, "failed")
		return err
	}
	return p.Store.MarkHealthSync(ctx, runID, "applied")
}

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
