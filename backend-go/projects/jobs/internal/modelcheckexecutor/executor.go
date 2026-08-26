// Package modelcheckexecutor composes the durable input boundary with a
// jobs-owned probe and outcome commit. Runtime-specific account resolution is
// injected; no Node or inter-process fallback is possible.
package modelcheckexecutor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckdurable"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprobe"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

type ResolvedTarget struct {
	ConfigRevision          string
	ProtocolProfileID       string
	ProtocolProfileRevision string
	Endpoint                string
	Protocol                modelcheckprofile.Protocol
	Model                   string
	Prompt                  string
	Stream                  bool
	MaxOutputTokens         int
	Headers                 http.Header
	Timeout                 time.Duration
	MaxResponseBytes        int64
	ModelLimit              int
	CountTokens             func(string) int
}

type TargetResolver func(context.Context, string, string) (ResolvedTarget, error)

type OutcomePayload struct {
	InputID      string `json:"inputId"`
	InputDigest  string `json:"inputDigest"`
	InputVersion int64  `json:"inputVersion"`
	// Item remains the first/basic item for readers that predate suite results.
	Item        modelcheckprobe.EvaluationItem   `json:"item"`
	Items       []modelcheckprobe.EvaluationItem `json:"items,omitempty"`
	CommittedAt time.Time                        `json:"committedAt"`
}

func ExecuteInput(ctx context.Context, store *modelcheckdurable.Store, inputID, ownerID, claimToken, outcomeID string, now time.Time, resolver TargetResolver, retry modelcheckprobe.RetryOptions) (OutcomePayload, error) {
	if store == nil || resolver == nil {
		return OutcomePayload{}, errors.New("model check executor is not initialized")
	}
	issued, err := store.LoadInput(ctx, inputID, now)
	if err != nil {
		return OutcomePayload{}, err
	}
	target, err := resolver(ctx, issued.Input.Target.ID, issued.Input.Target.ConfigRevision)
	if err != nil {
		return OutcomePayload{}, err
	}
	if !matchesSnapshot(target, issued) {
		return OutcomePayload{}, errors.New("model check target revision or profile is stale")
	}
	claim, err := store.Claim(ctx, inputID, ownerID, claimToken, outcomeID, now, issued.Input.DeadlineAt.Sub(now))
	if err != nil {
		return OutcomePayload{}, err
	}
	release := func(original error) (OutcomePayload, error) {
		if releaseErr := store.ReleaseClaim(ctx, claim, now); releaseErr != nil {
			return OutcomePayload{}, errors.Join(original, fmt.Errorf("release model check claim: %w", releaseErr))
		}
		return OutcomePayload{}, original
	}
	rechecked, err := resolver(ctx, issued.Input.Target.ID, issued.Input.Target.ConfigRevision)
	if err != nil {
		return release(err)
	}
	if !matchesSnapshot(rechecked, issued) || rechecked.Endpoint != target.Endpoint || rechecked.Protocol != target.Protocol {
		return release(errors.New("model check target revision or profile is stale"))
	}
	items, err := modelcheckprobe.RunSuite(ctx, modelcheckprobe.BasicProbeInput{Endpoint: rechecked.Endpoint, Protocol: rechecked.Protocol, Model: rechecked.Model, Prompt: rechecked.Prompt, Stream: rechecked.Stream, MaxOutputTokens: rechecked.MaxOutputTokens, Headers: rechecked.Headers, Timeout: rechecked.Timeout, MaxResponseBytes: rechecked.MaxResponseBytes, ModelLimit: rechecked.ModelLimit, CountTokens: rechecked.CountTokens}, modelcheckprobe.SuiteOptions{
		IncludeStream:              rechecked.Stream,
		IncludeStructured:          true,
		IncludeTool:                true,
		IncludeBehavior:            issued.Input.Profile == "full",
		IncludeLongContext:         issued.Input.Profile == "full",
		IncludeStability:           issued.Input.Profile == "full",
		IncludeUsageOnBasicFailure: issued.Input.Profile == "full",
	}, retry)
	if err != nil {
		return release(err)
	}
	if len(items) == 0 {
		return release(errors.New("model check suite produced no evaluation items"))
	}
	payload := OutcomePayload{InputID: issued.Input.InputID, InputDigest: issued.Input.InputDigest, InputVersion: issued.Input.InputVersion, Item: items[0], Items: items, CommittedAt: now.UTC()}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return release(fmt.Errorf("marshal model check outcome: %w", err))
	}
	if err := store.CommitOutcome(ctx, modelcheckdurable.Outcome{OutcomeID: outcomeID, InputID: inputID, InputDigest: issued.Input.InputDigest, Payload: encoded}, claim, now); err != nil {
		return OutcomePayload{}, err
	}
	return payload, nil
}

func matchesSnapshot(target ResolvedTarget, issued modelcheckdurable.Issued) bool {
	return target.ConfigRevision == issued.Input.Target.ConfigRevision && target.ProtocolProfileID == issued.Input.Target.ProtocolProfileID && target.ProtocolProfileRevision == issued.Input.Target.ProtocolProfileRevision && target.Model == issued.Input.Target.MappedUpstreamModel
}
