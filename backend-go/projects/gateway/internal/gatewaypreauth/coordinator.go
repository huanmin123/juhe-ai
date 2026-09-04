package gatewaypreauth

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// glue.go: small adapter types shared by the orchestration and the helpers —
// the closure-based route coordinator owner, the client-ip avoidance factory
// port, endpoint family shims and the JSON/RFC3339 decode helpers.

// gatewayProtoLane aliases the protocol lane union.
type gatewayProtoLane = gatewayproto.RequestLane

// gatewayprotoResolvedMapping aliases the resolved mapping type.
type gatewayprotoResolvedMapping = gatewayproto.ResolvedModelMapping

// ClientIPAccountAvoidanceInput mirrors createClientIpAccountAvoidanceTracker's
// input (runtime/client-ip-account-avoidance.service.ts, G13).
type ClientIPAccountAvoidanceInput struct {
	SystemAccountID string
	APIKeyID        string
	GroupID         string
	ClientIP        string
}

// ClientIPAccountAvoidanceFactory mirrors the tracker factory.
type ClientIPAccountAvoidanceFactory interface {
	CreateTracker(input ClientIPAccountAvoidanceInput) ClientIPAccountAvoidanceTracker
}

// preflightCoordinatorState carries the mutable orchestration locals the
// route coordinator reads and writes (Node closures over them).
type preflightCoordinatorState struct {
	interactionResourceAffinity *gatewaygemini.AffinityBinding
	apiKeyRecord                **gatewayruntimecache.GatewayAPIKeyRow
	groupID                     *string
	requestLane                 *gatewayproto.RequestLane
	requestClientCompatibility  *string
	routePlanSnapshot           **gatewayrouting.RoutePlanSnapshot[string]
	pendingRouteReason          *string
	pendingRouteFailure         **gatewayrouting.GatewayRouteFinalFailure
}

// preflightRouteCoordinator mirrors the routeCoordinator owner literal in
// prepareOpenAIGatewayDispatchContext.
type preflightRouteCoordinator struct {
	s     *Service
	ctx   context.Context
	state *preflightCoordinatorState
}

// newPreflightRouteCoordinator builds the coordinator.
func newPreflightRouteCoordinator(s *Service, ctx context.Context, state *preflightCoordinatorState) *preflightRouteCoordinator {
	return &preflightRouteCoordinator{s: s, ctx: ctx, state: state}
}

// RequestFallback mirrors requestFallback: only advertise the fallback when
// the route plan still has a concrete later target.
func (c *preflightRouteCoordinator) RequestFallback(ctx context.Context, reason string) (gatewayrouting.GatewayRouteFallbackDecision, error) {
	if c.state.interactionResourceAffinity != nil {
		return gatewayrouting.GatewayRouteFallbackDecision{Attempted: false}, nil
	}
	if !canAttemptAPIKeyGroupFallback(*c.state.apiKeyRecord, *c.state.groupID, *c.state.routePlanSnapshot) {
		return gatewayrouting.GatewayRouteFallbackDecision{Attempted: false}, nil
	}
	_, found, err := c.s.Candidates.ResolveNextGroupFallbackCandidate(ctx, GroupFallbackCandidateInput{
		Reason:                     reason,
		APIKeyRecord:               *c.state.apiKeyRecord,
		SystemAccountID:            "",
		GroupID:                    *c.state.groupID,
		RequestLane:                string(*c.state.requestLane),
		RequestClientCompatibility: *c.state.requestClientCompatibility,
		RoutePlanSnapshot:          **c.state.routePlanSnapshot,
	})
	if err != nil {
		return gatewayrouting.GatewayRouteFallbackDecision{}, err
	}
	if !found {
		return gatewayrouting.GatewayRouteFallbackDecision{Attempted: false}, nil
	}
	*c.state.pendingRouteReason = reason
	return gatewayrouting.GatewayRouteFallbackDecision{Attempted: true}, nil
}

// CompleteFailure mirrors completeFailure.
func (c *preflightRouteCoordinator) CompleteFailure(ctx context.Context, failure gatewayrouting.GatewayRouteFinalFailure) error {
	copied := failure
	*c.state.pendingRouteFailure = &copied
	return nil
}

// geminiEndpointFamilyForPath maps the gatewaygemini endpoint family onto
// the string vocabulary used here.
func geminiEndpointFamilyForPath(value string) string {
	return string(gatewaygemini.EndpointFamilyFromPath(value))
}

// gatewaybodyDecodeJSON decodes a stored JSON object (normal routing config
// payloads).
func gatewaybodyDecodeJSON(raw []byte) (map[string]any, error) {
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

// gatewayruntimecacheRFC3339Millis mirrors rfc3339InstantMilliseconds: an
// RFC3339 instant with mandatory Z or numeric offset.
func gatewayruntimecacheRFC3339Millis(value string) (int64, bool) {
	parsed, err := timeParseRFC3339Instant(value)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

// timeParseRFC3339Instant mirrors the shared/rfc3339.ts parser contract: a
// calendar datetime with mandatory offset (Z or ±HH:MM); bare datetimes and
// other layouts fail.
func timeParseRFC3339Instant(value string) (int64, error) {
	text := strings.TrimSpace(value)
	if !rfc3339InstantPatternCopy.MatchString(text) {
		return 0, errRFC3339Malformed
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return 0, errRFC3339Malformed
	}
	return parsed.UnixMilli(), nil
}

var rfc3339InstantPatternCopy = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$`)

var errRFC3339Malformed = errors.New("可恢复账户 cooldownUntil 必须是带 Z 或数值 offset 的 RFC3339 时间")
