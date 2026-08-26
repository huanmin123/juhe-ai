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

type ExecuteOptions struct {
	OnItem func(modelcheckprobe.EvaluationItem)
}

type OutcomePayload struct {
	InputID      string `json:"inputId"`
	InputDigest  string `json:"inputDigest"`
	InputVersion int64  `json:"inputVersion"`
	// Item remains the first/basic item for readers that predate suite results.
	Item        modelcheckprobe.EvaluationItem   `json:"item"`
	Items       []modelcheckprobe.EvaluationItem `json:"items,omitempty"`
	Summary     modelcheckprobe.SummaryResult    `json:"summary"`
	CommittedAt time.Time                        `json:"committedAt"`
}

func ExecuteInput(ctx context.Context, store *modelcheckdurable.Store, inputID, ownerID, claimToken, outcomeID string, now time.Time, resolver TargetResolver, retry modelcheckprobe.RetryOptions) (OutcomePayload, error) {
	return ExecuteInputWithOptions(ctx, store, inputID, ownerID, claimToken, outcomeID, now, resolver, retry, ExecuteOptions{})
}

// ExecuteInputWithOptions keeps the durable claim/fence flow identical to
// ExecuteInput while exposing invocation-local probe progress to its caller.
func ExecuteInputWithOptions(ctx context.Context, store *modelcheckdurable.Store, inputID, ownerID, claimToken, outcomeID string, now time.Time, resolver TargetResolver, retry modelcheckprobe.RetryOptions, options ExecuteOptions) (OutcomePayload, error) {
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
	var comparison ResolvedTarget
	if issued.Input.TrustedComparison {
		comparison, err = resolver(ctx, issued.Input.Comparison.ID, issued.Input.Comparison.ConfigRevision)
		if err != nil {
			return OutcomePayload{}, err
		}
		if !matchesComparisonSnapshot(comparison, issued) || comparison.Protocol != target.Protocol || comparison.ProtocolProfileID != target.ProtocolProfileID {
			return OutcomePayload{}, errors.New("model check trusted comparison revision or profile is stale")
		}
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
	if issued.Input.TrustedComparison {
		comparisonRechecked, comparisonErr := resolver(ctx, issued.Input.Comparison.ID, issued.Input.Comparison.ConfigRevision)
		if comparisonErr != nil {
			return release(comparisonErr)
		}
		if !matchesComparisonSnapshot(comparisonRechecked, issued) || comparisonRechecked.Endpoint != comparison.Endpoint || comparisonRechecked.Protocol != comparison.Protocol {
			return release(errors.New("model check trusted comparison revision or profile is stale"))
		}
		comparison = comparisonRechecked
	}
	items, err := runSuite(ctx, rechecked, issued.Input.Profile, "target", retry, options.OnItem)
	if err != nil {
		return release(err)
	}
	if issued.Input.TrustedComparison && !terminalSuite(items) {
		targetItems := append([]modelcheckprobe.EvaluationItem(nil), items...)
		comparisonItems, comparisonErr := runSuite(ctx, comparison, issued.Input.Profile, "trusted_comparison", retry, options.OnItem)
		if comparisonErr != nil {
			return release(comparisonErr)
		}
		items = append(items, comparisonItems...)
		if !terminalSuite(comparisonItems) {
			comparisonItem := modelcheckprobe.EvaluateTrustedComparison(targetItems, comparisonItems, issued.Input.Profile)
			items = append(items, comparisonItem)
			if options.OnItem != nil {
				options.OnItem(comparisonItem)
			}
		}
	}
	if len(items) == 0 {
		return release(errors.New("model check suite produced no evaluation items"))
	}
	summary := modelcheckprobe.SummarizeChecks(items, issued.Input.TrustedComparison, issued.Input.Profile)
	payload := OutcomePayload{InputID: issued.Input.InputID, InputDigest: issued.Input.InputDigest, InputVersion: issued.Input.InputVersion, Item: items[0], Items: items, Summary: summary, CommittedAt: now.UTC()}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return release(fmt.Errorf("marshal model check outcome: %w", err))
	}
	if err := store.CommitOutcome(ctx, modelcheckdurable.Outcome{OutcomeID: outcomeID, InputID: inputID, InputDigest: issued.Input.InputDigest, Payload: encoded}, claim, now); err != nil {
		return OutcomePayload{}, err
	}
	return payload, nil
}

func runSuite(ctx context.Context, target ResolvedTarget, profile, prefix string, retry modelcheckprobe.RetryOptions, onItem func(modelcheckprobe.EvaluationItem)) ([]modelcheckprobe.EvaluationItem, error) {
	return modelcheckprobe.RunSuite(ctx, modelcheckprobe.BasicProbeInput{Endpoint: target.Endpoint, Protocol: target.Protocol, Model: target.Model, Prompt: target.Prompt, Stream: target.Stream, MaxOutputTokens: target.MaxOutputTokens, Headers: target.Headers, Timeout: target.Timeout, MaxResponseBytes: target.MaxResponseBytes, ModelLimit: target.ModelLimit, CountTokens: target.CountTokens}, modelcheckprobe.SuiteOptions{
		Prefix:                     prefix,
		IncludeStream:              target.Stream,
		IncludeStructured:          true,
		IncludeTool:                true,
		IncludeBehavior:            profile == "full",
		IncludeLongContext:         profile == "full",
		IncludeStability:           profile == "full",
		IncludeUsageOnBasicFailure: profile == "full",
		IncludeTokenIntegrity:      true,
		IncludeIdentity:            profile == "full",
		OnItem:                     onItem,
	}, retry)
}

func matchesSnapshot(target ResolvedTarget, issued modelcheckdurable.Issued) bool {
	return target.ConfigRevision == issued.Input.Target.ConfigRevision && target.ProtocolProfileID == issued.Input.Target.ProtocolProfileID && target.ProtocolProfileRevision == issued.Input.Target.ProtocolProfileRevision && target.Model == issued.Input.Target.MappedUpstreamModel
}

func matchesComparisonSnapshot(target ResolvedTarget, issued modelcheckdurable.Issued) bool {
	comparison := issued.Input.Comparison
	return comparison != nil && target.ConfigRevision == comparison.ConfigRevision && target.ProtocolProfileID == comparison.ProtocolProfileID && target.ProtocolProfileRevision == comparison.ProtocolProfileRevision && target.Model == comparison.MappedUpstreamModel
}

func terminalSuite(items []modelcheckprobe.EvaluationItem) bool {
	for _, item := range items {
		if item.ItemType == "responses_basic" || item.ItemType == "protocol_basic" {
			success, _ := item.Evidence["success"].(bool)
			return !success
		}
	}
	return true
}
