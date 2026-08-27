// Package modelcheckcommand converts an authenticated management command into
// the immutable Go J3b runtime request. It deliberately depends on a
// credential-free source interface and never calls Node or a DB-service.
package modelcheckcommand

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckinput"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckruntime"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelchecksource"
)

// TargetFreezer is implemented by both the PostgreSQL and read-only SQLite
// J3b business readers.
type TargetFreezer interface {
	FreezeTarget(context.Context, modelchecksource.Request) (modelchecksource.FrozenTarget, error)
}

type PolicyLoader interface {
	Load(context.Context, string) (modelcheckinput.PolicySnapshot, error)
}

type Config struct {
	Freezer         TargetFreezer
	PolicyLoader    PolicyLoader
	ProbeSetVersion string
	Deadline        time.Duration
	Now             func() time.Time
}

type Request struct {
	SystemAccountID      string
	ActorSystemAccountID string
	TargetID             string
	Model                string
	Profile              string
	TrustedComparisonID  string
	Trigger              modelcheckinput.Trigger
	ScheduleID           string
	TraceID              string
}

type Builder struct {
	config Config
}

func New(config Config) (*Builder, error) {
	if config.Freezer == nil {
		return nil, errors.New("model check command target freezer is required")
	}
	if config.PolicyLoader == nil {
		return nil, errors.New("model check command policy loader is required")
	}
	if strings.TrimSpace(config.ProbeSetVersion) == "" {
		return nil, errors.New("model check command probe set snapshot is required")
	}
	if config.Deadline <= 0 {
		return nil, errors.New("model check command deadline must be positive")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Builder{config: config}, nil
}

func (b *Builder) Build(ctx context.Context, request Request) (modelcheckruntime.RunRequest, error) {
	if b == nil {
		return modelcheckruntime.RunRequest{}, errors.New("model check command builder is not initialized")
	}
	policy, err := b.config.PolicyLoader.Load(ctx, strings.TrimSpace(request.SystemAccountID))
	if err != nil {
		return modelcheckruntime.RunRequest{}, fmt.Errorf("load model check policy: %w", err)
	}
	return b.BuildWithPolicy(ctx, request, policy)
}

// BuildWithPolicy constructs a request from an already-claimed immutable
// policy. Scheduled and quality-recovery callers use this after their lease
// transaction has frozen the schedule revision; reloading the mutable global
// policy here would make the run disagree with its claim fence.
func (b *Builder) BuildWithPolicy(ctx context.Context, request Request, policy modelcheckinput.PolicySnapshot) (modelcheckruntime.RunRequest, error) {
	if b == nil {
		return modelcheckruntime.RunRequest{}, errors.New("model check command builder is not initialized")
	}
	request = normalize(request)
	if err := validate(request); err != nil {
		return modelcheckruntime.RunRequest{}, err
	}
	if err := policy.Verify(); err != nil {
		return modelcheckruntime.RunRequest{}, fmt.Errorf("verify model check policy: %w", err)
	}
	target, err := b.config.Freezer.FreezeTarget(ctx, modelchecksource.Request{
		SystemAccountID:      request.SystemAccountID,
		AccountID:            request.TargetID,
		Model:                request.Model,
		AllowQualityIsolated: request.Trigger == modelcheckinput.TriggerQualityRecovery,
	})
	if err != nil {
		return modelcheckruntime.RunRequest{}, fmt.Errorf("freeze model check target: %w", err)
	}
	if target.TargetName == "" || target.TargetOwnerSystemID == "" || target.GroupID == "" {
		return modelcheckruntime.RunRequest{}, errors.New("model check target metadata is incomplete")
	}
	var comparison *modelcheckinput.AccountSnapshot
	if request.TrustedComparisonID != "" {
		frozenComparison, err := b.config.Freezer.FreezeTarget(ctx, modelchecksource.Request{
			SystemAccountID: request.SystemAccountID,
			AccountID:       request.TrustedComparisonID,
			Model:           request.Model,
		})
		if err != nil {
			return modelcheckruntime.RunRequest{}, fmt.Errorf("freeze trusted comparison target: %w", err)
		}
		comparison = &frozenComparison.DurableAccount
	}
	now := b.config.Now().UTC()
	return modelcheckruntime.RunRequest{
		SystemAccountID:      request.SystemAccountID,
		ActorSystemAccountID: request.ActorSystemAccountID,
		Target:               target.DurableAccount,
		Comparison:           comparison,
		Model:                request.Model,
		Profile:              request.Profile,
		Trigger:              request.Trigger,
		ScheduleID:           request.ScheduleID,
		TrustedComparison:    comparison != nil,
		ProbeSetVersion:      b.config.ProbeSetVersion,
		Policy:               policy,
		StartedAt:            now,
		DeadlineAt:           now.Add(b.config.Deadline),
		TargetName:           target.TargetName,
		TargetOwnerSystemID:  target.TargetOwnerSystemID,
		ProviderCode:         target.DurableAccount.ProviderCode,
		TargetType:           "account",
		GroupID:              target.GroupID,
		TraceID:              request.TraceID,
	}, nil
}

func normalize(request Request) Request {
	request.SystemAccountID = strings.TrimSpace(request.SystemAccountID)
	request.ActorSystemAccountID = strings.TrimSpace(request.ActorSystemAccountID)
	request.TargetID = strings.TrimSpace(request.TargetID)
	request.Model = strings.TrimSpace(request.Model)
	request.Profile = strings.TrimSpace(request.Profile)
	request.TrustedComparisonID = strings.TrimSpace(request.TrustedComparisonID)
	request.ScheduleID = strings.TrimSpace(request.ScheduleID)
	request.TraceID = strings.TrimSpace(request.TraceID)
	return request
}

func validate(request Request) error {
	if request.SystemAccountID == "" || request.ActorSystemAccountID == "" || request.TargetID == "" || request.Model == "" {
		return errors.New("model check command scope, actor, target and model are required")
	}
	if request.Profile != "quick" && request.Profile != "full" {
		return errors.New("model check command profile is invalid")
	}
	if request.Trigger != modelcheckinput.TriggerManual && request.Trigger != modelcheckinput.TriggerScheduled && request.Trigger != modelcheckinput.TriggerQualityRecovery {
		return errors.New("model check command trigger is invalid")
	}
	if request.Trigger == modelcheckinput.TriggerScheduled && request.ScheduleID == "" {
		return errors.New("scheduled model check command requires schedule ID")
	}
	if request.Trigger != modelcheckinput.TriggerScheduled && request.ScheduleID != "" {
		return errors.New("non-scheduled model check command must not include schedule ID")
	}
	if request.TrustedComparisonID != "" && request.TrustedComparisonID == request.TargetID {
		return errors.New("trusted comparison account must differ from target")
	}
	return nil
}
