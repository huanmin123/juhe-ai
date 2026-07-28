package accountprobe

import (
	"context"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/store/port"
)

type CandidateReader interface {
	ResolveGatewayGroupAccess(context.Context, port.GatewayGroupAccessInput) (port.GatewayGroupAccess, bool, error)
	ListGatewayAccountCandidates(context.Context, port.GatewayAccountCandidateListInput) ([]port.GatewayAccountCandidate, error)
}

type CandidateHydrator interface {
	Hydrate(context.Context, gatewaycandidatewindow.HydrateInput) ([]gatewaycandidatewindow.HydrationResult, error)
}

type LoadInput struct {
	AccountID       string
	GroupID         string
	SystemAccountID string
	RequestedModel  string
	EndpointFamily  string
	Now             time.Time
}

type Loader struct {
	Reader   CandidateReader
	Hydrator CandidateHydrator
}

func (l Loader) Load(ctx context.Context, input LoadInput) (gatewaycandidatewindow.Candidate, bool, error) {
	if l.Reader == nil || l.Hydrator == nil {
		return gatewaycandidatewindow.Candidate{}, false, fmt.Errorf("account probe candidate reader and hydrator are required")
	}
	input.AccountID = strings.TrimSpace(input.AccountID)
	input.GroupID = strings.TrimSpace(input.GroupID)
	input.SystemAccountID = strings.TrimSpace(input.SystemAccountID)
	input.RequestedModel = strings.TrimSpace(input.RequestedModel)
	input.EndpointFamily = strings.TrimSpace(input.EndpointFamily)
	if input.AccountID == "" || input.GroupID == "" || input.SystemAccountID == "" || input.RequestedModel == "" || input.EndpointFamily == "" {
		return gatewaycandidatewindow.Candidate{}, false, fmt.Errorf("account probe target identity, model, and endpoint family are required")
	}
	if input.Now.IsZero() {
		input.Now = time.Now()
	}
	input.Now = input.Now.UTC()
	access, found, err := l.Reader.ResolveGatewayGroupAccess(ctx, port.GatewayGroupAccessInput{
		GroupID: input.GroupID, SystemAccountID: input.SystemAccountID, Now: input.Now,
	})
	if err != nil || !found {
		return gatewaycandidatewindow.Candidate{}, found, err
	}
	rows, err := l.Reader.ListGatewayAccountCandidates(ctx, port.GatewayAccountCandidateListInput{
		Access: access, AccountID: input.AccountID, Now: input.Now, IncludeUnavailable: true,
		RequestedModel: input.RequestedModel, EndpointFamily: input.EndpointFamily, Limit: 2,
	})
	if err != nil {
		return gatewaycandidatewindow.Candidate{}, false, fmt.Errorf("load exact account probe candidate: %w", err)
	}
	if len(rows) == 0 {
		return gatewaycandidatewindow.Candidate{}, false, nil
	}
	if len(rows) != 1 || rows[0].AccountID != input.AccountID {
		return gatewaycandidatewindow.Candidate{}, false, fmt.Errorf("account probe candidate query returned a non-unique target")
	}
	results, err := l.Hydrator.Hydrate(ctx, gatewaycandidatewindow.HydrateInput{
		Candidates: rows, RequestedModel: input.RequestedModel, EndpointFamily: input.EndpointFamily,
	})
	if err != nil {
		return gatewaycandidatewindow.Candidate{}, false, fmt.Errorf("hydrate account probe candidate: %w", err)
	}
	if len(results) != 1 || results[0].AccountID != input.AccountID {
		return gatewaycandidatewindow.Candidate{}, false, fmt.Errorf("account probe hydration returned a non-unique target")
	}
	if results[0].DropReason != "" {
		return gatewaycandidatewindow.Candidate{}, false, fmt.Errorf("account probe candidate unavailable: %s", results[0].DropReason)
	}
	if results[0].Candidate.Projection.AccountID != input.AccountID {
		return gatewaycandidatewindow.Candidate{}, false, fmt.Errorf("account probe hydration lost the target projection")
	}
	if !gatewaycandidatewindow.CandidateSupportsRequest(results[0].Candidate, input.RequestedModel, input.EndpointFamily) {
		return gatewaycandidatewindow.Candidate{}, false, fmt.Errorf("account probe candidate no longer supports the requested model and endpoint family")
	}
	return results[0].Candidate, true, nil
}
