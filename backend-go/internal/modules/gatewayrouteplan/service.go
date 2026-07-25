// Package gatewayrouteplan composes preflight, cross-request binding order and
// per-group candidate windows. It is deliberately not an HTTP listener or an
// attempt executor, so it can be wired by a future gateway owner as one
// immutable request preparation step.
package gatewayrouteplan

import (
	"context"
	"fmt"
	"strings"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewaypreflight"
	"juhe-ai/backend-go/internal/modules/gatewayroutecoordination"
	"juhe-ai/backend-go/internal/modules/gatewayrouting"
)

type PreflightResolver interface {
	Resolve(context.Context, string) (gatewaypreflight.Result, error)
}

type CandidateWindowLoader interface {
	Load(context.Context, gatewaycandidatewindow.LoadInput) (gatewaycandidatewindow.Window, bool, error)
}

type Input struct {
	RawAPIKey      string
	RequestedModel string
	EndpointFamily string
}

type GroupWindow struct {
	Binding gatewaypreflight.Binding
	Window  gatewaycandidatewindow.Window
	Found   bool
}

type Result struct {
	Preflight gatewaypreflight.Result
	Plan      *gatewayroutecoordination.Plan
	Groups    []GroupWindow
}

type Service struct {
	preflight   PreflightResolver
	coordinator gatewayroutecoordination.Coordinator
	candidates  CandidateWindowLoader
}

type Options struct {
	Preflight   PreflightResolver
	Coordinator gatewayroutecoordination.Coordinator
	Candidates  CandidateWindowLoader
}

func NewService(options Options) (*Service, error) {
	if options.Preflight == nil || options.Coordinator == nil || options.Candidates == nil {
		return nil, fmt.Errorf("gateway route plan requires preflight, coordinator and candidate loader")
	}
	return &Service{preflight: options.Preflight, coordinator: options.Coordinator, candidates: options.Candidates}, nil
}

// Build advances a dynamic route policy exactly once after successful
// preflight, then loads each ordered group independently. The allocation is
// intentionally at-most-once even when later hydration finds no candidate or
// fails, so callers cannot repeatedly retry preflight to reclaim a fair-share
// cursor. It never flattens or re-sorts candidates across group boundaries.
func (s *Service) Build(ctx context.Context, input Input) (Result, error) {
	if s == nil || s.preflight == nil || s.coordinator == nil || s.candidates == nil {
		return Result{}, fmt.Errorf("gateway route plan service is not configured")
	}
	preflight, err := s.preflight.Resolve(ctx, input.RawAPIKey)
	if err != nil {
		return Result{}, fmt.Errorf("resolve gateway preflight: %w", err)
	}
	result := Result{Preflight: preflight}
	if !preflight.Decision().Allowed() {
		return result, nil
	}
	apiKey, ok := preflight.APIKey()
	if !ok {
		return Result{}, fmt.Errorf("allowed gateway preflight has no API key")
	}
	bindings := preflight.Bindings()
	snapshot, index, err := snapshotFromPreflight(apiKey, bindings)
	if err != nil {
		return Result{}, err
	}
	plan, err := s.coordinator.Plan(ctx, snapshot)
	if err != nil {
		return Result{}, fmt.Errorf("plan gateway route: %w", err)
	}
	result.Plan = &plan
	result.Groups = make([]GroupWindow, 0, len(plan.Ordered))
	for _, ordered := range plan.Ordered {
		binding, exists := index[ordered.ID]
		if !exists {
			return Result{}, fmt.Errorf("gateway route coordinator returned unknown binding %q", ordered.ID)
		}
		window, found, loadErr := s.candidates.Load(ctx, gatewaycandidatewindow.LoadInput{
			GroupID: binding.GroupID(), SystemAccountID: apiKey.SystemAccountID(),
			RequestedModel: strings.TrimSpace(input.RequestedModel), EndpointFamily: strings.TrimSpace(input.EndpointFamily),
		})
		if loadErr != nil {
			return Result{}, fmt.Errorf("load candidates for route binding %q: %w", binding.ID(), loadErr)
		}
		result.Groups = append(result.Groups, GroupWindow{Binding: binding, Window: window, Found: found})
	}
	return result, nil
}

func snapshotFromPreflight(apiKey gatewaypreflight.APIKey, bindings []gatewaypreflight.Binding) (gatewayroutecoordination.Snapshot, map[string]gatewaypreflight.Binding, error) {
	mode, err := routeMode(apiKey.RouteStrategyMode())
	if err != nil {
		return gatewayroutecoordination.Snapshot{}, nil, err
	}
	routeBindings := make([]gatewayrouting.Binding, 0, len(bindings))
	index := make(map[string]gatewaypreflight.Binding, len(bindings))
	for _, binding := range bindings {
		if _, exists := index[binding.ID()]; exists {
			return gatewayroutecoordination.Snapshot{}, nil, fmt.Errorf("gateway preflight returned duplicate binding %q", binding.ID())
		}
		index[binding.ID()] = binding
		routeBindings = append(routeBindings, gatewayrouting.Binding{
			ID: binding.ID(), GroupID: binding.GroupID(), Priority: binding.Priority(), Weight: binding.Weight(),
			Active: strings.EqualFold(binding.Status(), "active"), GroupEnabled: binding.GroupEnabled(),
		})
	}
	return gatewayroutecoordination.Snapshot{Scope: gatewayroutecoordination.Scope{SystemAccountID: apiKey.SystemAccountID(), RouteStrategyID: apiKey.RouteStrategyID()}, Mode: mode, Bindings: routeBindings}, index, nil
}

func routeMode(value string) (gatewayrouting.Mode, error) {
	switch gatewayrouting.Mode(strings.TrimSpace(value)) {
	case gatewayrouting.ModeNormal, gatewayrouting.ModeFailover, gatewayrouting.ModeRoundRobin, gatewayrouting.ModeWeighted:
		return gatewayrouting.Mode(strings.TrimSpace(value)), nil
	default:
		return "", fmt.Errorf("unsupported gateway route strategy mode %q", value)
	}
}
