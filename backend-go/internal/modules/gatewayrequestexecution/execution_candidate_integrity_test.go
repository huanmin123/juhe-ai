package gatewayrequestexecution

import (
	"testing"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
)

func TestBuildRejectsCandidateIntegrityViolations(t *testing.T) {
	t.Parallel()

	valid := candidate("account-a", "group-one")
	tests := []struct {
		name       string
		candidates []gatewaycandidatewindow.Candidate
	}{
		{
			name: "cross system account",
			candidates: func() []gatewaycandidatewindow.Candidate {
				crossSystem := valid
				crossSystem.Projection.SystemAccountID = "other-system"
				return []gatewaycandidatewindow.Candidate{crossSystem}
			}(),
		},
		{
			name: "cross group account",
			candidates: func() []gatewaycandidatewindow.Candidate {
				crossGroup := valid
				crossGroup.Projection.GroupID = "other-group"
				return []gatewaycandidatewindow.Candidate{crossGroup}
			}(),
		},
		{
			name: "duplicate account id",
			candidates: []gatewaycandidatewindow.Candidate{
				valid,
				valid,
			},
		},
		{
			name: "account id control character",
			candidates: func() []gatewaycandidatewindow.Candidate {
				controlCharacter := valid
				controlCharacter.Projection.AccountID = "account-\x00forged"
				return []gatewaycandidatewindow.Candidate{controlCharacter}
			}(),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			route := testRoute(t, "normal", []testRouteGroup{{
				bindingID:  "binding-one",
				groupID:    "group-one",
				priority:   1,
				candidates: test.candidates,
			}})

			result := Build(Input{
				Request:  openAIRequest(),
				Route:    route,
				Identity: Identity{TraceID: "trace-candidate-integrity", MutationID: "mutation-candidate-integrity"},
			})
			if result.Outcome() != OutcomeReject || result.RejectReason() != RejectRoutePlanInvalid {
				t.Fatalf("result = %#v", result)
			}
			if execution, ok := result.Execution(); ok {
				t.Fatalf("unexpected execution = %#v", execution)
			}
		})
	}
}
