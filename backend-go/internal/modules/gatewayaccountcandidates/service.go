package gatewayaccountcandidates

import (
	"context"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

type Service struct {
	store port.GatewayAccountCandidateReader
	now   func() time.Time
}

type ServiceOptions struct {
	Store port.GatewayAccountCandidateReader
	Now   func() time.Time
}

type ProjectInput struct {
	GroupID            string
	SystemAccountID    string
	IncludeUnavailable bool
	RequestedModel     string
	EndpointFamily     string
}

type Projection struct {
	Access       port.GatewayGroupAccess
	Candidates   []port.GatewayAccountCandidate
	ScanLimit    int
	LimitReached bool
}

func NewService(store port.GatewayAccountCandidateReader) *Service {
	return NewServiceWithOptions(ServiceOptions{Store: store})
}

func NewServiceWithOptions(opts ServiceOptions) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{store: opts.Store, now: now}
}

func (s *Service) Project(ctx context.Context, input ProjectInput) (Projection, bool, error) {
	if s.store == nil {
		return Projection{}, false, fmt.Errorf("store is required")
	}
	groupID := strings.TrimSpace(input.GroupID)
	if groupID == "" {
		return Projection{}, false, fmt.Errorf("group id is required")
	}
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	if systemAccountID == "" {
		return Projection{}, false, fmt.Errorf("system account id is required")
	}
	now := s.now().UTC()
	access, found, err := s.store.ResolveGatewayGroupAccess(ctx, port.GatewayGroupAccessInput{
		GroupID:         groupID,
		SystemAccountID: systemAccountID,
		Now:             now,
	})
	if err != nil {
		return Projection{}, false, err
	}
	if !found {
		return Projection{}, false, nil
	}
	candidates, err := s.store.ListGatewayAccountCandidates(ctx, port.GatewayAccountCandidateListInput{
		Access:             access,
		Now:                now,
		IncludeUnavailable: input.IncludeUnavailable,
		RequestedModel:     strings.TrimSpace(input.RequestedModel),
		EndpointFamily:     strings.TrimSpace(input.EndpointFamily),
		Limit:              port.GatewayAccountCandidateScanLimit,
	})
	if err != nil {
		return Projection{}, false, err
	}
	if candidates == nil {
		candidates = []port.GatewayAccountCandidate{}
	}
	return Projection{
		Access:       access,
		Candidates:   candidates,
		ScanLimit:    port.GatewayAccountCandidateScanLimit,
		LimitReached: len(candidates) == port.GatewayAccountCandidateScanLimit,
	}, true, nil
}
