package modelcheckhttp

import (
	"context"
	"errors"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckcommand"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckinput"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckruntime"
)

// NewBuildRequestFunc binds the management transport to the Go-owned command
// builder. It is the only production adapter from HTTP JSON into a J3b
// runtime request; account and policy data remain direct-reader concerns.
func NewBuildRequestFunc(builder *modelcheckcommand.Builder) BuildRequestFunc {
	return func(ctx context.Context, scope Scope, command Command) (modelcheckruntime.RunRequest, error) {
		if builder == nil {
			return modelcheckruntime.RunRequest{}, errors.New("model check command builder is not initialized")
		}
		actorID := scope.ActorSystemAccountID
		if actorID == "" {
			actorID = scope.SystemAccountID
		}
		return builder.Build(ctx, modelcheckcommand.Request{
			SystemAccountID:      scope.SystemAccountID,
			ActorSystemAccountID: actorID,
			TargetID:             command.TargetID,
			Model:                command.Model,
			Profile:              command.Profile,
			TrustedComparisonID:  command.TrustedComparisonID,
			Trigger:              modelcheckinput.TriggerManual,
		})
	}
}
