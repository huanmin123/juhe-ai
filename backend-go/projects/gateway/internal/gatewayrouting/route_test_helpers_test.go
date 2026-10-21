package gatewayrouting

import (
	"context"
	"strings"
	"testing"
)

// Shared fixtures for the normal/目标组路由 tests (super-group expansion and
// the standard target paths).

func routeTestBinding(id, groupID string, priority int64) GroupBindingRow {
	return GroupBindingRow{
		ID: id, APIKeyID: "key1", SystemAccountID: "owner1",
		GroupID: groupID, Priority: priority, Status: RowStatusActive,
		GroupEnabled: 1,
	}
}

func routeTestAPIKey(mode string, bindings ...GroupBindingRow) *APIKeyRow {
	return &APIKeyRow{
		ID: "key1", SystemAccountID: "owner1",
		RouteStrategyMode: mode,
		Status:            RowStatusActive,
		GroupBindings:     bindings,
	}
}

func routeTestAccess() GroupUsageAccessMetadata {
	return GroupUsageAccessMetadata{GroupAccessType: GroupAccessTypeOwner}
}

func routeTestRequest(model string) RequestView {
	return RequestView{Method: "POST", OriginalURL: "/v1/chat/completions", BodyModel: model}
}

// runRouteTest resolves the route with the shared fake runtime cache and the
// pass-through capability probe unless a scripted one is given.
func runRouteTest(t *testing.T, cache *fakeRuntimeCache, capability AccountCapabilityFilter, apiKey *APIKeyRow, request RequestView) NormalGatewayModelRouteResult {
	t.Helper()
	if capability == nil {
		capability = PassthroughCapabilityFilter{}
	}
	service := NewNormalModelRouteService(cache, capability)
	result, err := service.ResolveNormalGatewayModelRoute(context.Background(), ResolveNormalGatewayModelRouteInput{
		Request:      request,
		APIKeyRecord: apiKey,
	})
	if err != nil {
		t.Fatalf("ResolveNormalGatewayModelRoute returned error: %v", err)
	}
	return result
}

// segmentSummary renders segments as "group:acc1,acc2" for order assertions.
func segmentSummary(segments []NormalRouteGroupSegment) []string {
	parts := make([]string, 0, len(segments))
	for _, segment := range segments {
		parts = append(parts, segment.GroupID+":"+strings.Join(accountIDs(segment.Accounts), ","))
	}
	return parts
}
